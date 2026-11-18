import { act, cleanup, render, screen, waitFor } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import userEvent from "@testing-library/user-event";
import { MemoryRouter } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import {
  getMigrationSource,
  listMigrationSources,
  listRepositories,
  preflightMigrationSource,
  startNexusMigrationBatch,
  getNexusMigrationBatch,
} from "../../client";
import { AntdProvider } from "../../app/AntdProvider";
import { PreferencesProvider } from "../../lib/preferences";
import {
  migrationPreflight,
  migrationSource,
} from "../../test/migrationFixtures";
import {
  nexusBatch,
  nexusCapabilities,
  nexusTargets,
} from "../../test/nexusBatchFixtures";
import { NexusBatchStart } from "./NexusBatchStart";
import { migrationKeys } from "./migrationQueries";

vi.mock("../../client", () => ({
  getMigrationSource: vi.fn(),
  listMigrationSources: vi.fn(),
  listRepositories: vi.fn(),
  preflightMigrationSource: vi.fn(),
  startNexusMigrationBatch: vi.fn(),
  getNexusMigrationBatch: vi.fn(),
}));
beforeEach(() => {
  vi.mocked(getMigrationSource).mockResolvedValue({
    data: migrationSource,
  } as never);
  vi.mocked(listMigrationSources).mockResolvedValue({
    data: { items: [migrationSource], nextCursor: "" },
  } as never);
  vi.mocked(listRepositories).mockResolvedValue({
    data: { items: nexusTargets, nextPageToken: "" },
  } as never);
  vi.mocked(preflightMigrationSource).mockResolvedValue({
    data: migrationPreflight,
  } as never);
  vi.mocked(startNexusMigrationBatch).mockResolvedValue({
    data: { ...nexusBatch, state: "scanning", version: "1", totalBytes: null },
  } as never);
  vi.mocked(getNexusMigrationBatch).mockResolvedValue({
    data: nexusBatch,
  } as never);
});
afterEach(() => {
  cleanup();
  vi.resetAllMocks();
});
function show(
  client = new QueryClient({
    defaultOptions: { queries: { retry: false, gcTime: Infinity } },
  }),
) {
  const accepted = vi.fn();
  render(
    <QueryClientProvider client={client}>
      <PreferencesProvider>
        <AntdProvider>
          <MemoryRouter>
            <NexusBatchStart
              binding={nexusCapabilities}
              draft={{
                sourceId: migrationSource.id,
                mappings: [
                  {
                    sourceRepository: "releases",
                    targetName: "native-releases",
                  },
                ],
              }}
              onClose={() => undefined}
              onCreated={accepted}
            />
          </MemoryRouter>
        </AntdProvider>
      </PreferencesProvider>
    </QueryClientProvider>,
  );
  return accepted;
}
async function fillCapacity() {
  const user = userEvent.setup();
  for (const label of [
    "Free storage-pool bytes",
    "Peak download bytes",
    "Peak upload bytes",
    "Peak backup bytes",
    "Peak restore bytes",
    "Headroom bytes",
  ]) {
    await user.click(screen.getByRole("textbox", { name: label }));
    await user.paste(label === "Free storage-pool bytes" ? "1000000" : "0");
  }
  await user.click(
    screen.getByRole("textbox", {
      name: "Capacity observation time (with time zone)",
    }),
  );
  await user.paste(new Date(Date.now() - 1000).toISOString());
  await user.click(
    screen.getByRole("textbox", {
      name: "Evidence expiry time (with time zone)",
    }),
  );
  await user.paste(new Date(Date.now() + 600000).toISOString());
}
describe("explicit start", () => {
  it.each([
    ["docker", "oci"],
    ["conan", "conan"],
    ["npm", "npm"],
  ] as const)(
    "starts a %s batch only against its %s Hosted target",
    async (sourceFormat, targetFormat) => {
      vi.mocked(preflightMigrationSource).mockResolvedValue({
        data: {
          ...migrationPreflight,
          repositories: [
            { ...migrationPreflight.repositories[0], format: sourceFormat },
          ],
        },
      } as never);
      vi.mocked(listRepositories).mockResolvedValue({
        data: {
          items: [
            { ...nexusTargets[0], format: targetFormat },
            nexusTargets[1],
          ],
          nextPageToken: "",
        },
      } as never);
      const accepted = show();
      await userEvent.click(
        await screen.findByRole("button", { name: "Run preflight" }),
      );
      await screen.findByRole("combobox", {
        name: "Target repository for releases",
      });
      await fillCapacity();
      const submit = screen.getByRole("button", {
        name: "Apply mappings and create batch",
      });
      expect(submit).toBeEnabled();
      await userEvent.click(submit);
      await waitFor(() => expect(accepted).toHaveBeenCalledWith(nexusBatch));
      expect(startNexusMigrationBatch).toHaveBeenCalledWith(
        expect.objectContaining({
          body: expect.objectContaining({
            policyVersion: "native-v1",
            mappings: [
              {
                sourceRepository: "releases",
                targetRepositoryId: nexusTargets[0].id,
              },
            ],
          }),
        }),
      );
    },
  );
  it("does not infer a Raw target for an unknown source format", async () => {
    vi.mocked(preflightMigrationSource).mockResolvedValue({
      data: {
        ...migrationPreflight,
        repositories: [
          { ...migrationPreflight.repositories[0], format: "future-format" },
        ],
      },
    } as never);
    show();
    await userEvent.click(
      await screen.findByRole("button", { name: "Run preflight" }),
    );
    await screen.findByText("No repositories visible to these credentials");
    await fillCapacity();
    expect(
      screen.queryByRole("combobox", {
        name: "Target repository for releases",
      }),
    ).not.toBeInTheDocument();
    expect(
      screen.getByRole("button", { name: "Apply mappings and create batch" }),
    ).toBeDisabled();
    expect(startNexusMigrationBatch).not.toHaveBeenCalled();
  });
  for (const status of [401, 403])
    for (const query of ["list", "detail", "preflight"] as const)
      it(`discards an executable draft and its source facts after ${query} refuses ${status}`, async () => {
        const client = new QueryClient({
          defaultOptions: { queries: { retry: false, gcTime: Infinity } },
        });
        const accepted = show(client);
        await userEvent.click(
          await screen.findByRole("button", { name: "Run preflight" }),
        );
        await screen.findByRole("combobox", {
          name: "Target repository for releases",
        });
        await fillCapacity();
        expect(
          screen.getByRole("button", {
            name: "Apply mappings and create batch",
          }),
        ).toBeEnabled();
        if (query === "preflight") {
          vi.mocked(preflightMigrationSource).mockResolvedValue({
            error: { status },
          } as never);
          await userEvent.click(
            screen.getByRole("button", { name: "Run preflight" }),
          );
        } else {
          (query === "list"
            ? vi.mocked(listMigrationSources)
            : vi.mocked(getMigrationSource)
          ).mockResolvedValue({ error: { status } } as never);
          await act(async () => {
            await client.refetchQueries({
              queryKey:
                query === "list"
                  ? migrationKeys.list()
                  : migrationKeys.detail(migrationSource.id),
              exact: true,
            });
          });
        }
        await screen.findByText(
          /This migration source requires a platform administrator identity\./,
        );
        expect(document.body).not.toHaveTextContent(migrationSource.name);
        expect(
          screen.queryByRole("combobox", { name: "Saved migration source" }),
        ).not.toBeInTheDocument();
        expect(
          screen.queryByRole("button", {
            name: "Apply mappings and create batch",
          }),
        ).not.toBeInTheDocument();
        expect(startNexusMigrationBatch).not.toHaveBeenCalled();
        cleanup();
        show(client);
        await screen.findByText(
          /This migration source requires a platform administrator identity\./,
        );
        vi.mocked(listMigrationSources).mockResolvedValue({
          data: { items: [migrationSource], nextCursor: "" },
        } as never);
        await userEvent.click(screen.getByRole("button", { name: /Retry/ }));
        await screen.findByRole("combobox", { name: "Saved migration source" });
        expect(
          screen.getByRole("combobox", { name: "Saved migration source" }),
        ).toHaveValue("");
        expect(
          screen.getByRole("textbox", { name: "Free storage-pool bytes" }),
        ).toHaveValue("");
        expect(
          screen.queryByRole("combobox", {
            name: "Target repository for releases",
          }),
        ).not.toBeInTheDocument();
        expect(accepted).not.toHaveBeenCalled();
      });
  it("does not execute a draft or unknown capacity, then binds real target IDs and re-reads the response", async () => {
    const accepted = show();
    const submit = screen.getByRole("button", {
      name: "Apply mappings and create batch",
    });
    expect(submit).toBeDisabled();
    await userEvent.click(
      await screen.findByRole("button", { name: "Run preflight" }),
    );
    await screen.findByRole("combobox", {
      name: "Target repository for releases",
    });
    expect(submit).toBeDisabled();
    expect(startNexusMigrationBatch).not.toHaveBeenCalled();
    await fillCapacity();
    await userEvent.click(submit);
    await waitFor(() => expect(accepted).toHaveBeenCalledWith(nexusBatch));
    expect(startNexusMigrationBatch).toHaveBeenCalledWith(
      expect.objectContaining({
        headers: { "Idempotency-Key": expect.any(String) },
        body: expect.objectContaining({
          sourceId: migrationSource.id,
          sourceVersion: "1",
          policyVersion: "native-v1",
          mappings: [
            {
              sourceRepository: "releases",
              targetRepositoryId: nexusTargets[0].id,
            },
          ],
          capacityEvidence: expect.objectContaining({
            targetId: nexusCapabilities.targetId,
            poolId: nexusCapabilities.poolId,
            freeBytes: 1000000,
          }),
        }),
      }),
    );
  });
  it("requires a real compatible target instead of treating an invented target name as a UUID", async () => {
    vi.mocked(listRepositories).mockResolvedValue({
      data: {
        items: [
          { ...nexusTargets[0], type: "proxy" },
          { ...nexusTargets[1], name: "native-releases" },
        ],
        nextPageToken: "",
      },
    } as never);
    show();
    await userEvent.click(
      await screen.findByRole("button", { name: "Run preflight" }),
    );
    await fillCapacity();
    expect(
      screen.getByRole("button", { name: "Apply mappings and create batch" }),
    ).toBeDisabled();
    expect(startNexusMigrationBatch).not.toHaveBeenCalled();
  });
  it("coalesces repeated clicks while a real start command is in flight", async () => {
    let complete: () => void = () => undefined;
    vi.mocked(startNexusMigrationBatch).mockImplementation(
      () =>
        new Promise<never>((resolve) => {
          complete = () => resolve({ data: nexusBatch } as never);
        }),
    );
    const accepted = show();
    await userEvent.click(
      await screen.findByRole("button", { name: "Run preflight" }),
    );
    await screen.findByRole("combobox", {
      name: "Target repository for releases",
    });
    await fillCapacity();
    await userEvent.dblClick(
      screen.getByRole("button", { name: "Apply mappings and create batch" }),
    );
    expect(startNexusMigrationBatch).toHaveBeenCalledTimes(1);
    complete();
    await waitFor(() => expect(accepted).toHaveBeenCalledTimes(1));
  });
  it("rejects a mismatched source-version observation and does not submit from stale preview facts", async () => {
    vi.mocked(preflightMigrationSource).mockResolvedValue({
      data: { ...migrationPreflight, sourceVersion: "2" },
    } as never);
    show();
    await userEvent.click(
      await screen.findByRole("button", { name: "Run preflight" }),
    );
    await screen.findByText(/source connection version changed/);
    expect(
      screen.getByRole("button", { name: "Apply mappings and create batch" }),
    ).toBeDisabled();
    expect(startNexusMigrationBatch).not.toHaveBeenCalled();
  });
});
