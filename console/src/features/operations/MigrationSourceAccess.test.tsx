import { act, cleanup, render, screen, waitFor } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import userEvent from "@testing-library/user-event";
import { Link, MemoryRouter } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import {
  getMigrationSource,
  listMigrationSources,
  preflightMigrationSource,
  discoverMigrationSource,
  previewMigrationSourceMapping,
  getNexusMigrationCapabilities,
  listNexusMigrationBatches,
  updateMigrationSource,
} from "../../client";
import { AntdProvider } from "../../app/AntdProvider";
import { PreferencesProvider } from "../../lib/preferences";
import {
  migrationPreflight,
  migrationSource,
  migrationDiscovery,
  migrationMapping,
} from "../../test/migrationFixtures";
import { MigrationSourcesPanel } from "./MigrationSourcesPanel";
import { migrationKeys } from "./migrationQueries";
import { NexusMigrationWorkspace } from "./NexusMigrationWorkspace";
import { nexusCapabilities } from "../../test/nexusBatchFixtures";

vi.mock("../../client", () => ({
  getMigrationSource: vi.fn(),
  listMigrationSources: vi.fn(),
  preflightMigrationSource: vi.fn(),
  discoverMigrationSource: vi.fn(),
  previewMigrationSourceMapping: vi.fn(),
  createMigrationSource: vi.fn(),
  updateMigrationSource: vi.fn(),
  deleteMigrationSource: vi.fn(),
  getNexusMigrationCapabilities: vi.fn(),
  listNexusMigrationBatches: vi.fn(),
  getNexusMigrationBatch: vi.fn(),
  listNexusMigrationEntries: vi.fn(),
  getRepository: vi.fn(),
  listRepositories: vi.fn(),
  pauseNexusMigrationBatch: vi.fn(),
  resumeNexusMigrationBatch: vi.fn(),
  retryNexusMigrationBatch: vi.fn(),
  cancelNexusMigrationBatch: vi.fn(),
  startNexusMigrationBatch: vi.fn(),
}));
const list = vi.mocked(listMigrationSources);
const detail = vi.mocked(getMigrationSource);
function show(workspace = false) {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false, gcTime: Infinity } },
  });
  const mount = () =>
    render(
      <QueryClientProvider client={client}>
        <PreferencesProvider>
          <AntdProvider>
            <MemoryRouter
              initialEntries={[`/operations?source=${migrationSource.id}`]}
            >
              <Link to="/operations?source=second">
                Another source deep link
              </Link>
              {workspace ? (
                <NexusMigrationWorkspace />
              ) : (
                <MigrationSourcesPanel onPlanBatch={vi.fn()} />
              )}
            </MemoryRouter>
          </AntdProvider>
        </PreferencesProvider>
      </QueryClientProvider>,
    );
  let view = mount();
  return {
    client,
    remount: () => {
      view.unmount();
      view = mount();
    },
  };
}
beforeEach(() => {
  list.mockResolvedValue({
    data: { items: [migrationSource], nextCursor: "" },
  } as never);
  detail.mockResolvedValue({ data: migrationSource } as never);
  vi.mocked(preflightMigrationSource).mockResolvedValue({
    data: migrationPreflight,
  } as never);
  vi.mocked(discoverMigrationSource).mockResolvedValue({
    data: migrationDiscovery,
  } as never);
  vi.mocked(previewMigrationSourceMapping).mockResolvedValue({
    data: migrationMapping,
  } as never);
  vi.mocked(getNexusMigrationCapabilities).mockResolvedValue({
    data: nexusCapabilities,
  } as never);
  vi.mocked(listNexusMigrationBatches).mockResolvedValue({
    data: { items: [], nextCursor: "" },
  } as never);
});
afterEach(() => {
  cleanup();
  vi.resetAllMocks();
});

describe("cached source authorization boundary", () => {
  for (const status of [401, 403])
    for (const query of ["list", "detail"] as const)
      it(`hides all cached source facts and open controls after ${query} refetch ${status}`, async () => {
        const { client } = show();
        await screen.findByRole("heading", { name: migrationSource.name });
        await userEvent.click(
          screen.getByRole("button", { name: "Run preflight" }),
        );
        await screen.findByRole("checkbox", { name: "Select releases" });
        const editing = status === 401;
        await userEvent.click(
          screen.getByRole("button", {
            name: editing ? "Edit source" : "Delete source",
          }),
        );
        await screen.findByRole("dialog");
        (query === "list" ? list : detail).mockResolvedValue({
          error: { status, message: "never-show-private-denial" },
        } as never);
        await act(async () => {
          await client.refetchQueries({
            queryKey:
              query === "list"
                ? migrationKeys.list()
                : migrationKeys.detail(migrationSource.id),
            exact: true,
          });
        });
        await screen.findAllByText(
          /This migration source requires a platform administrator identity\./,
        );
        expect(document.body).not.toHaveTextContent(migrationSource.name);
        expect(document.body).not.toHaveTextContent(migrationSource.baseUrl);
        expect(
          screen.queryByDisplayValue(migrationSource.baseUrl),
        ).not.toBeInTheDocument();
        expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
        for (const name of [
          "Add migration source",
          "Edit source",
          "Delete source",
          "Run preflight",
        ])
          expect(
            screen.queryByRole("button", { name }),
          ).not.toBeInTheDocument();
        expect(
          screen.queryByRole("checkbox", { name: "Select releases" }),
        ).not.toBeInTheDocument();
        expect(document.body).not.toHaveTextContent(
          "never-show-private-denial",
        );
      });

  for (const query of ["list", "detail"] as const)
    it(`retains authorized observations and an open editor after temporary ${query} 500`, async () => {
      const { client } = show();
      await screen.findByRole("heading", { name: migrationSource.name });
      await userEvent.click(
        screen.getByRole("button", { name: "Edit source" }),
      );
      (query === "list" ? list : detail).mockResolvedValue({
        error: { status: 500, message: "never-show-private-error" },
      } as never);
      await act(async () => {
        await client.refetchQueries({
          queryKey:
            query === "list"
              ? migrationKeys.list()
              : migrationKeys.detail(migrationSource.id),
          exact: true,
        });
      });
      await screen.findByText(/request did not complete/);
      expect(
        screen.getByRole("heading", { name: migrationSource.name }),
      ).toBeInTheDocument();
      expect(
        screen.getByDisplayValue(migrationSource.baseUrl),
      ).toBeInTheDocument();
      expect(screen.getByRole("dialog")).toBeInTheDocument();
      expect(document.body).not.toHaveTextContent("never-show-private-error");
    });

  it("does not let an onSuccess-style cache write reveal a previously refused source", async () => {
    const { client } = show();
    await screen.findByRole("heading", { name: migrationSource.name });
    detail.mockResolvedValue({ error: { status: 403 } } as never);
    await act(async () => {
      await client.refetchQueries({
        queryKey: migrationKeys.detail(migrationSource.id),
        exact: true,
      });
    });
    await screen.findAllByText(
      /This migration source requires a platform administrator identity\./,
    );
    await act(async () => {
      client.setQueryData(migrationKeys.detail(migrationSource.id), {
        ...migrationSource,
        version: "2",
      });
    });
    await waitFor(() =>
      expect(
        screen.queryByRole("heading", { name: migrationSource.name }),
      ).not.toBeInTheDocument(),
    );
    expect(document.body).not.toHaveTextContent(migrationSource.baseUrl);
  });

  it("retains refusal across source URLs, both workspace views and a remount with the same QueryClient", async () => {
    const { client, remount } = show(true);
    await screen.findByRole("heading", { name: migrationSource.name });
    list.mockResolvedValue({ error: { status: 403 } } as never);
    await act(async () => {
      await client.refetchQueries({
        queryKey: migrationKeys.list(),
        exact: true,
      });
    });
    await screen.findByText(
      /This migration source requires a platform administrator identity\./,
    );
    await userEvent.click(
      screen.getByRole("link", { name: "Another source deep link" }),
    );
    expect(detail).not.toHaveBeenCalledWith(
      expect.objectContaining({ path: { sourceId: "second" } }),
    );
    await userEvent.click(
      screen.getByRole("button", { name: "Batches and acceptance" }),
    );
    await userEvent.click(
      await screen.findByRole("button", { name: "Create migration batch" }),
    );
    await screen.findByText(
      /This migration source requires a platform administrator identity\./,
    );
    expect(
      screen.queryByRole("combobox", { name: "Saved migration source" }),
    ).not.toBeInTheDocument();
    await userEvent.click(
      screen.getByRole("button", { name: "Connections and mappings" }),
    );
    remount();
    await screen.findByText(
      /This migration source requires a platform administrator identity\./,
    );
    expect(document.body).not.toHaveTextContent(migrationSource.name);
    expect(document.body).not.toHaveTextContent(migrationSource.baseUrl);
  });

  it("does not accept a late editor success after revocation has closed its protected form", async () => {
    let finish!: (value: unknown) => void;
    vi.mocked(updateMigrationSource).mockImplementation(
      () =>
        new Promise((resolve) => {
          finish = resolve;
        }) as never,
    );
    const { client } = show();
    await userEvent.click(
      await screen.findByRole("button", { name: "Edit source" }),
    );
    await userEvent.click(
      screen.getByRole("button", { name: "Save migration source" }),
    );
    await waitFor(() => expect(updateMigrationSource).toHaveBeenCalledTimes(1));
    list.mockResolvedValue({ error: { status: 403 } } as never);
    await act(async () => {
      await client.refetchQueries({
        queryKey: migrationKeys.list(),
        exact: true,
      });
    });
    await screen.findByText(
      /This migration source requires a platform administrator identity\./,
    );
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
    await act(async () => {
      finish({
        data: {
          ...migrationSource,
          version: "2",
          name: "Late saved private metadata",
        },
      });
    });
    expect(document.body).not.toHaveTextContent("Late saved private metadata");
    expect(document.body).not.toHaveTextContent(migrationSource.baseUrl);
    expect(
      screen.queryByRole("button", { name: "Edit source" }),
    ).not.toBeInTheDocument();
  });

  it("rejects an old in-flight read, keeps refusal while retrying, and recovers only from a new successful read", async () => {
    const { client } = show();
    await screen.findByRole("heading", { name: migrationSource.name });
    let finish!: (value: unknown) => void;
    detail.mockImplementationOnce(
      () =>
        new Promise((resolve) => {
          finish = resolve;
        }) as never,
    );
    let oldRead!: Promise<void>;
    await act(async () => {
      oldRead = client.refetchQueries({
        queryKey: migrationKeys.detail(migrationSource.id),
        exact: true,
      });
    });
    list.mockResolvedValue({ error: { status: 403 } } as never);
    await act(async () => {
      await client.refetchQueries({
        queryKey: migrationKeys.list(),
        exact: true,
      });
    });
    await screen.findByText(
      /This migration source requires a platform administrator identity\./,
    );
    await act(async () => {
      finish({ data: { ...migrationSource, name: "Late revoked metadata" } });
      await oldRead;
    });
    expect(document.body).not.toHaveTextContent("Late revoked metadata");
    let recover!: (value: unknown) => void;
    list.mockImplementationOnce(
      () =>
        new Promise((resolve) => {
          recover = resolve;
        }) as never,
    );
    await userEvent.click(screen.getByRole("button", { name: /Retry/ }));
    expect(document.body).not.toHaveTextContent(migrationSource.name);
    expect(
      screen.queryByRole("button", { name: "Add migration source" }),
    ).not.toBeInTheDocument();
    detail.mockResolvedValue({
      data: { ...migrationSource, name: "Fresh authorized metadata" },
    } as never);
    await act(async () => {
      recover({
        data: {
          items: [{ ...migrationSource, name: "Fresh authorized metadata" }],
          nextCursor: "",
        },
      });
    });
    await screen.findByRole("heading", { name: "Fresh authorized metadata" });
    expect(document.body).not.toHaveTextContent("Late revoked metadata");
    expect(
      screen.queryByRole("checkbox", { name: "Select releases" }),
    ).not.toBeInTheDocument();
  });

  for (const operation of ["preflight", "discovery", "preview"] as const)
    it(`closes the complete protected observation and mapping subtree after ${operation} refuses access`, async () => {
      show();
      await userEvent.click(
        await screen.findByRole("button", { name: "Run preflight" }),
      );
      await userEvent.click(
        await screen.findByRole("checkbox", { name: "Select releases" }),
      );
      await userEvent.type(
        screen.getByRole("textbox", { name: "Target name for releases" }),
        "native-releases",
      );
      await userEvent.click(
        screen.getByRole("button", {
          name: "Discover selected Hosted content (1)",
        }),
      );
      await screen.findByText(/2 source assets/);
      await userEvent.click(
        screen.getByRole("button", { name: "Preview explicit mappings" }),
      );
      await screen.findByRole("button", {
        name: "Create a batch with 1 Hosted mappings",
      });
      const mocked =
        operation === "preflight"
          ? vi.mocked(preflightMigrationSource)
          : operation === "discovery"
            ? vi.mocked(discoverMigrationSource)
            : vi.mocked(previewMigrationSourceMapping);
      mocked.mockResolvedValue({
        error: { status: 403, message: "do-not-echo" },
      } as never);
      await userEvent.click(
        screen.getByRole("button", {
          name:
            operation === "preflight"
              ? "Run preflight"
              : operation === "discovery"
                ? "Discover selected Hosted content (1)"
                : "Preview explicit mappings",
        }),
      );
      await screen.findByText(
        /This migration source requires a platform administrator identity\./,
      );
      expect(document.body).not.toHaveTextContent(migrationSource.baseUrl);
      expect(
        screen.queryByRole("button", {
          name: /Create a batch with .+ Hosted mappings/,
        }),
      ).not.toBeInTheDocument();
      expect(
        screen.queryByRole("region", { name: "Source asset discovery" }),
      ).not.toBeInTheDocument();
      expect(
        screen.queryByRole("checkbox", { name: "Select releases" }),
      ).not.toBeInTheDocument();
      expect(document.body).not.toHaveTextContent("do-not-echo");
    });
});
