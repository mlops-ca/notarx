import {
  cleanup,
  isInaccessible,
  render,
  screen,
  waitFor,
  within,
} from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import {
  getNexusMigrationCapabilities,
  getNexusMigrationBatch,
  listNexusMigrationBatches,
  listNexusMigrationEntries,
  getRepository,
  pauseNexusMigrationBatch,
  resumeNexusMigrationBatch,
  retryNexusMigrationBatch,
  cancelNexusMigrationBatch,
} from "../../client";
import { AntdProvider } from "../../app/AntdProvider";
import { PreferencesProvider } from "../../lib/preferences";
import { TestQueryProvider } from "../../test/queryClient";
import {
  nexusBatch,
  nexusCapabilities,
  nexusEntries,
  nexusTargets,
  nexusScanFailure,
} from "../../test/nexusBatchFixtures";
import { NexusBatchesPanel } from "./NexusBatchesPanel";
import * as batchModel from "./nexusBatchModel";

vi.mock("../../client", () => ({
  getNexusMigrationCapabilities: vi.fn(),
  getNexusMigrationBatch: vi.fn(),
  listNexusMigrationBatches: vi.fn(),
  listNexusMigrationEntries: vi.fn(),
  getRepository: vi.fn(),
  pauseNexusMigrationBatch: vi.fn(),
  resumeNexusMigrationBatch: vi.fn(),
  retryNexusMigrationBatch: vi.fn(),
  cancelNexusMigrationBatch: vi.fn(),
}));
const list = vi.mocked(listNexusMigrationBatches);
const detail = vi.mocked(getNexusMigrationBatch);
const entries = vi.mocked(listNexusMigrationEntries);
function show(id = nexusBatch.id) {
  return render(
    <TestQueryProvider>
      <PreferencesProvider>
        <AntdProvider>
          <MemoryRouter
            initialEntries={[
              `/operations?tab=migrations&view=batches${id ? `&batch=${id}` : ""}`,
            ]}
          >
            <NexusBatchesPanel draft={null} onDraftConsumed={() => undefined} />
          </MemoryRouter>
        </AntdProvider>
      </PreferencesProvider>
    </TestQueryProvider>,
  );
}
beforeEach(() => {
  vi.mocked(getNexusMigrationCapabilities).mockResolvedValue({
    data: nexusCapabilities,
  } as never);
  list.mockResolvedValue({
    data: { items: [nexusBatch], nextCursor: "" },
  } as never);
  detail.mockResolvedValue({ data: nexusBatch } as never);
  entries.mockResolvedValue({
    data: { items: nexusEntries, nextCursor: "" },
  } as never);
  vi.mocked(getRepository).mockImplementation(
    async (options) =>
      ({
        data: nexusTargets.find(
          (target) => target.id === options.path.repositoryId,
        ),
      }) as never,
  );
});
afterEach(() => {
  cleanup();
  vi.resetAllMocks();
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
});
// Resolve the label once, then check accessibility on that field alone.
function evidenceInput(dialog: HTMLElement, label: string) {
  const input = within(dialog).getByLabelText(label, { selector: "input" });
  expect(input).toHaveRole("textbox");
  expect(input).toHaveAccessibleName(label);
  expect(isInaccessible(input)).toBe(false);
  return input;
}

describe("real batch read/control states", () => {
  it("shows exactly one initial error branch and sanitizes unknown transport text", async () => {
    list.mockRejectedValue(new Error("password=never-show"));
    show("");
    await screen.findByText(
      /The batch request did not complete\. Read current server state before retrying\. No source credentials or upstream response body are displayed\./,
    );
    expect(
      screen.queryByText("No migration batches yet"),
    ).not.toBeInTheDocument();
    expect(screen.queryByRole("status")).not.toBeInTheDocument();
    expect(document.body).not.toHaveTextContent("never-show");
  });
  it("retains loaded facts on refresh failure and keeps nullable total unknown", async () => {
    detail.mockResolvedValue({
      data: { ...nexusBatch, totalBytes: null, scanComplete: false },
    } as never);
    show();
    await screen.findByText("Unknown; complete manifest is not sealed");
    expect(
      screen.getByText(
        "Target-only cold-client acceptance for this batch: unverified",
      ),
    ).toBeInTheDocument();
    list.mockRejectedValue(new Error("private-upstream"));
    await userEvent.click(
      screen.getAllByRole("button", { name: "Refresh batches" })[0],
    );
    await screen.findByText(
      /The batch request did not complete\. Read current server state before retrying\. No source credentials or upstream response body are displayed\./,
    );
    expect(
      screen.getByRole("heading", { name: nexusBatch.id }),
    ).toBeInTheDocument();
    expect(document.body).not.toHaveTextContent("private-upstream");
  });
  it("reads each entry cursor and exposes full digest only as the server observation", async () => {
    entries.mockImplementation(
      async (options) =>
        ({
          data: options.query?.cursor
            ? { items: [nexusEntries[1]], nextCursor: "" }
            : { items: [nexusEntries[0]], nextCursor: "next-page" },
        }) as never,
    );
    show();
    await screen.findByText("Loaded 1 / 2 planned entries");
    expect(
      screen.getByText(
        /Inventory is not fully loaded or scanning is not sealed\. This report is not complete inventory acceptance\./,
      ),
    ).toBeInTheDocument();
    await userEvent.click(
      screen.getByRole("button", { name: "Read complete inventory" }),
    );
    await screen.findByText("Loaded 2 / 2 planned entries");
    await waitFor(() =>
      expect(
        screen.queryByText(
          /Inventory is not fully loaded or scanning is not sealed\. This report is not complete inventory acceptance\./,
        ),
      ).not.toBeInTheDocument(),
    );
    expect(entries).toHaveBeenCalledWith(
      expect.objectContaining({ query: { limit: 100, cursor: "next-page" } }),
    );
    await userEvent.click(
      screen.getAllByRole("button", { name: "Expand row" })[0],
    );
    expect(
      screen.getByText(nexusEntries[0].assets[0].sha256),
    ).toBeInTheDocument();
    expect(document.body).not.toHaveTextContent(
      "must-not-export-upstream-validator",
    );
  });
  it("pauses with CAS and idempotency then GETs authoritative state even if replay body is older", async () => {
    const running = { ...nexusBatch, state: "running" as const };
    detail.mockResolvedValue({ data: running } as never);
    vi.mocked(pauseNexusMigrationBatch).mockImplementation(async () => {
      detail.mockResolvedValue({
        data: { ...nexusBatch, state: "paused", version: "12" },
      } as never);
      return { data: { ...running, state: "pausing", version: "8" } } as never;
    });
    show();
    await userEvent.click(
      await screen.findByRole("button", { name: "Pause batch" }),
    );
    await screen.findByRole("button", { name: "Resume batch" });
    expect(vi.mocked(pauseNexusMigrationBatch)).toHaveBeenCalledWith(
      expect.objectContaining({
        path: { batchId: nexusBatch.id },
        headers: { "If-Match": "7", "Idempotency-Key": expect.any(String) },
      }),
    );
    expect(screen.getByText("Batch version 12")).toBeInTheDocument();
  });
  it("keeps the identical control request key on uncertain retry and never renders an upstream error", async () => {
    detail.mockResolvedValue({
      data: { ...nexusBatch, state: "running" },
    } as never);
    vi.mocked(pauseNexusMigrationBatch).mockRejectedValue(
      new Error("password=hide-me"),
    );
    show();
    await userEvent.click(
      await screen.findByRole("button", { name: "Pause batch" }),
    );
    await screen.findByText(
      /The submission outcome is unconfirmed\. Refresh batches first\. Retrying unchanged inputs reuses the same request to avoid duplicate creation or control\./,
    );
    await userEvent.click(screen.getByRole("button", { name: "Pause batch" }));
    await waitFor(() =>
      expect(pauseNexusMigrationBatch).toHaveBeenCalledTimes(2),
    );
    const calls = vi.mocked(pauseNexusMigrationBatch).mock.calls;
    expect(calls[0][0].headers?.["Idempotency-Key"]).toBe(
      calls[1][0].headers?.["Idempotency-Key"],
    );
    expect(document.body).not.toHaveTextContent("hide-me");
  });
  it("leaves resume blank, blocks unknown evidence, and uses a fresh explicit runtime-bound observation", async () => {
    const user = userEvent.setup();
    detail.mockResolvedValue({
      data: { ...nexusBatch, state: "paused" },
    } as never);
    vi.mocked(resumeNexusMigrationBatch).mockImplementation(async () => {
      detail.mockResolvedValue({
        data: { ...nexusBatch, state: "paused", version: "9" },
      } as never);
      return { error: { status: 412 } } as never;
    });
    show();
    await user.click(
      await screen.findByRole("button", { name: "Resume batch" }),
    );
    // rc-util uses a fixed ID in jsdom; table pagination also owns that ID.
    // The real-browser gate checks the dialog's actual accessible title.
    const dialog = screen.getByRole("dialog");
    const submit = within(dialog).getByRole("button", { name: "Resume batch" });
    expect(submit).toBeDisabled();
    for (const label of [
      "Free storage-pool bytes",
      "Peak download bytes",
      "Peak upload bytes",
      "Peak backup bytes",
      "Peak restore bytes",
      "Headroom bytes",
    ]) {
      const input = evidenceInput(dialog, label);
      expect(input).toHaveValue("");
      await user.type(
        input,
        label === "Free storage-pool bytes" ? "1000000" : "0",
      );
    }
    // Timestamps are whole observations; paste them without replaying every key.
    await user.click(
      evidenceInput(dialog, "Capacity observation time (with time zone)"),
    );
    await user.paste(new Date(Date.now() - 1000).toISOString());
    await user.click(
      evidenceInput(dialog, "Evidence expiry time (with time zone)"),
    );
    await user.paste(new Date(Date.now() + 600_000).toISOString());
    await user.click(submit);
    await screen.findByText(
      /A worker or another administrator updated the batch\. A server read was attempted; select the operation again and check the new version\. Writes are never automatically resent\./,
    );
    await waitFor(() =>
      expect(screen.queryByRole("dialog")).not.toBeInTheDocument(),
    );
    expect(screen.getByText("Batch version 9")).toBeInTheDocument();
    expect(resumeNexusMigrationBatch).toHaveBeenCalledTimes(1);
    expect(resumeNexusMigrationBatch).toHaveBeenCalledWith(
      expect.objectContaining({
        headers: { "If-Match": "7", "Idempotency-Key": expect.any(String) },
        body: {
          capacityEvidence: expect.objectContaining({
            targetId: nexusCapabilities.targetId,
            poolId: nexusCapabilities.poolId,
            freeBytes: 1000000,
            downloadBytes: 0,
          }),
        },
      }),
    );
    expect(retryNexusMigrationBatch).not.toHaveBeenCalled();
  });
  it("confirms cancel, preserves the captured version, and never sends on merely opening/closing a page", async () => {
    detail.mockResolvedValue({
      data: { ...nexusBatch, state: "paused" },
    } as never);
    vi.mocked(cancelNexusMigrationBatch).mockResolvedValue({
      data: { ...nexusBatch, state: "cancelled" },
    } as never);
    const rendered = show();
    await userEvent.click(
      await screen.findByRole("button", { name: "Cancel batch" }),
    );
    const dialog = screen.getByRole("dialog");
    expect(cancelNexusMigrationBatch).not.toHaveBeenCalled();
    await userEvent.click(
      within(dialog).getByRole("button", { name: "Cancel batch" }),
    );
    await waitFor(() =>
      expect(cancelNexusMigrationBatch).toHaveBeenCalledTimes(1),
    );
    expect(
      vi.mocked(cancelNexusMigrationBatch).mock.calls[0][0].headers?.[
        "If-Match"
      ],
    ).toBe("7");
    rendered.unmount();
    expect(resumeNexusMigrationBatch).not.toHaveBeenCalled();
    expect(pauseNexusMigrationBatch).not.toHaveBeenCalled();
  });
  it("refreshes entry facts when progress changes without a CAS version change", async () => {
    const running = {
      ...nexusBatch,
      state: "running" as const,
      counts: { ...nexusBatch.counts, pending: 2, committed: 0 },
      visibleIdentities: 0,
    };
    detail.mockResolvedValue({ data: running } as never);
    entries.mockResolvedValue({
      data: {
        items: nexusEntries.map((entry) => ({ ...entry, state: "pending" })),
        nextCursor: "",
      },
    } as never);
    show();
    const table = await screen.findByRole("table");
    expect(within(table).getAllByText("Pending")).toHaveLength(2);
    const before = entries.mock.calls.length;
    detail.mockResolvedValue({
      data: {
        ...running,
        visibleIdentities: 1,
        counts: { ...running.counts, pending: 1, committed: 1 },
      },
    } as never);
    entries.mockResolvedValue({
      data: {
        items: [nexusEntries[0], { ...nexusEntries[1], state: "pending" }],
        nextCursor: "",
      },
    } as never);
    await userEvent.click(
      screen.getAllByRole("button", { name: "Refresh batches" })[1],
    );
    await waitFor(() =>
      expect(within(table).getByText("Published")).toBeInTheDocument(),
    );
    expect(entries.mock.calls.length).toBeGreaterThan(before);
    expect(screen.getByText("Batch version 7")).toBeInTheDocument();
  });
  it("retries a failed unsealed scan in the same batch with fresh evidence rather than pretending it was ready", async () => {
    const user = userEvent.setup();
    const failed = {
      ...nexusBatch,
      state: "failed" as const,
      scanComplete: false,
      entryCount: 0,
      totalBytes: null,
      metadataDiscovered: 0,
      visibleIdentities: 0,
      counts: { ...nexusBatch.counts, committed: 0 },
      lastError: "scan_interrupted",
    };
    detail.mockResolvedValue({ data: failed } as never);
    entries.mockResolvedValue({ data: { items: [], nextCursor: "" } } as never);
    vi.mocked(retryNexusMigrationBatch).mockImplementation(async () => {
      const scanning = {
        ...failed,
        version: "8",
        state: "scanning" as const,
        lastError: "",
      };
      detail.mockResolvedValue({ data: scanning } as never);
      return { data: scanning } as never;
    });
    show();
    await user.click(
      await screen.findByRole("button", {
        name: "Retry eligible failed entries",
      }),
    );
    const dialog = screen.getByRole("dialog");
    expect(
      within(dialog).getByText(
        /An unsealed scan restarts full scanning in the same batch/,
      ),
    ).toBeInTheDocument();
    const submit = within(dialog).getByRole("button", {
      name: "Retry eligible failed entries",
    });
    expect(submit).toBeDisabled();
    for (const label of [
      "Free storage-pool bytes",
      "Peak download bytes",
      "Peak upload bytes",
      "Peak backup bytes",
      "Peak restore bytes",
      "Headroom bytes",
    ])
      await user.type(
        evidenceInput(dialog, label),
        label === "Free storage-pool bytes" ? "1000000" : "0",
      );
    // Timestamps are whole observations; paste them without replaying every key.
    await user.click(
      evidenceInput(dialog, "Capacity observation time (with time zone)"),
    );
    await user.paste(new Date(Date.now() - 1000).toISOString());
    await user.click(
      evidenceInput(dialog, "Evidence expiry time (with time zone)"),
    );
    await user.paste(new Date(Date.now() + 600000).toISOString());
    await user.click(submit);
    await waitFor(() =>
      expect(screen.queryByRole("dialog")).not.toBeInTheDocument(),
    );
    expect(retryNexusMigrationBatch).toHaveBeenCalledTimes(1);
    expect(vi.mocked(retryNexusMigrationBatch).mock.calls[0][0]).toMatchObject({
      path: { batchId: failed.id },
      headers: { "If-Match": "7", "Idempotency-Key": expect.any(String) },
    });
    expect(
      screen.getByText("Unknown; complete manifest is not sealed"),
    ).toBeInTheDocument();
    expect(screen.getByText("Batch version 8")).toBeInTheDocument();
  });
  it("polls active entries independently from the immutable CAS version", async () => {
    detail.mockResolvedValue({
      data: { ...nexusBatch, state: "running" },
    } as never);
    entries.mockResolvedValueOnce({
      data: {
        items: nexusEntries.map((entry) => ({ ...entry, state: "pending" })),
        nextCursor: "",
      },
    } as never);
    show();
    const table = await screen.findByRole("table");
    expect(within(table).getAllByText("Pending")).toHaveLength(2);
    await waitFor(
      () => expect(within(table).getAllByText("Published")).toHaveLength(2),
      { timeout: 8000 },
    );
    expect(screen.getByText("Batch version 7")).toBeInTheDocument();
  }, 10_000);
  it("shows precise durable scan failure scope without promoting an unsealed manifest or leaking arbitrary codes", async () => {
    const failure = { ...nexusScanFailure, code: "password=upstream-body" };
    detail.mockResolvedValue({
      data: {
        ...nexusBatch,
        state: "failed",
        scanComplete: false,
        totalBytes: null,
        entryCount: 0,
        lastScanFailure: failure,
      },
    } as never);
    entries.mockResolvedValue({ data: { items: [], nextCursor: "" } } as never);
    show();
    const diagnostic = await screen.findByRole("region", {
      name: "Most recent durable scan failure",
    });
    expect(
      within(diagnostic).getByText(failure.sourcePath),
    ).toBeInTheDocument();
    expect(within(diagnostic).getByText(failure.identity)).toBeInTheDocument();
    expect(
      within(diagnostic).getByText("releases, raw-source"),
    ).toBeInTheDocument();
    expect(
      within(diagnostic).getByText("Open source verification stream"),
    ).toBeInTheDocument();
    expect(
      within(diagnostic).getByText("unclassified_failure"),
    ).toBeInTheDocument();
    expect(
      screen.getByText("Unknown; complete manifest is not sealed"),
    ).toBeInTheDocument();
    expect(document.body).not.toHaveTextContent("upstream-body");
    expect(
      screen.getByRole("button", { name: "Retry eligible failed entries" }),
    ).toBeEnabled();
  });
  it("retains the real source_missing scan code in the view and export while distinguishing asset changes from connection versions", async () => {
    const report = vi.spyOn(batchModel, "observationReport");
    vi.stubGlobal(
      "URL",
      class extends URL {
        static createObjectURL() {
          return "blob:observation";
        }
        static revokeObjectURL() {}
      },
    );
    vi.spyOn(HTMLAnchorElement.prototype, "click").mockImplementation(
      () => undefined,
    );
    detail.mockResolvedValue({
      data: {
        ...nexusBatch,
        state: "failed",
        scanComplete: false,
        totalBytes: null,
        entryCount: 0,
        lastScanFailure: nexusScanFailure,
      },
    } as never);
    entries.mockResolvedValue({ data: { items: [], nextCursor: "" } } as never);
    show();
    const diagnostic = await screen.findByRole("region", {
      name: "Most recent durable scan failure",
    });
    expect(within(diagnostic).getByText("source_missing")).toBeInTheDocument();
    expect(
      within(diagnostic).getByText("Source missing during read"),
    ).toBeInTheDocument();
    await userEvent.click(
      screen.getByRole("button", { name: "Export current observation report" }),
    );
    await waitFor(() => expect(report).toHaveBeenCalled());
    expect(report.mock.results[0].value.lastScanFailure?.code).toBe(
      "source_missing",
    );
    expect(report.mock.results[0].value.inventoryComplete).toBe(false);
  });
  it.each([
    ["maven", "/maven/native-releases"],
    ["raw", "/raw/native-releases/"],
    ["oci", "/v2/native-releases/"],
    ["conan", "/conan/native-releases"],
    ["npm", "/npm/native-releases/"],
  ] as const)(
    "exports a canonical %s URL independent of display commands",
    async (format, path) => {
      const report = vi.spyOn(batchModel, "observationReport");
      vi.stubGlobal(
        "URL",
        class extends URL {
          static createObjectURL() {
            return "blob:observation";
          }
          static revokeObjectURL() {}
        },
      );
      vi.spyOn(HTMLAnchorElement.prototype, "click").mockImplementation(
        () => undefined,
      );
      vi.mocked(getRepository).mockImplementation(
        async (options) =>
          ({
            data:
              options.path.repositoryId === nexusTargets[0].id
                ? { ...nexusTargets[0], format }
                : nexusTargets[1],
          }) as never,
      );
      show();
      await screen.findByRole("heading", {
        name: "Migration acceptance observation report",
      });
      await userEvent.click(
        screen.getByRole("button", {
          name: "Export current observation report",
        }),
      );
      await waitFor(() => expect(report).toHaveBeenCalled());
      expect(report.mock.calls[0][3]).toContainEqual({
        repositoryId: nexusTargets[0].id,
        name: nexusTargets[0].name,
        format,
        url: `${window.location.origin}${path}`,
      });
    },
  );
  it("exports freshly read checkpoints and refuses to call an unfinished cursor a complete inventory", async () => {
    const report = vi.spyOn(batchModel, "observationReport");
    vi.stubGlobal(
      "URL",
      class extends URL {
        static createObjectURL() {
          return "blob:observation";
        }
        static revokeObjectURL() {}
      },
    );
    vi.spyOn(HTMLAnchorElement.prototype, "click").mockImplementation(
      () => undefined,
    );
    entries.mockResolvedValueOnce({
      data: {
        items: nexusEntries.map((entry) => ({
          ...entry,
          state: "pending",
          assets: entry.assets.map((asset) => ({ ...asset, verified: false })),
        })),
        nextCursor: "",
      },
    } as never);
    show();
    await screen.findByRole("table");
    const before = entries.mock.calls.length;
    detail.mockResolvedValue({
      data: { ...nexusBatch, visibleIdentities: 1 },
    } as never);
    entries.mockResolvedValue({
      data: { items: [nexusEntries[0]], nextCursor: "still-more" },
    } as never);
    await userEvent.click(
      screen.getByRole("button", { name: "Export current observation report" }),
    );
    await waitFor(() => expect(report).toHaveBeenCalled());
    const [batch, inventory, complete, , times] = report.mock.calls[0];
    expect(batch.version).toBe("7");
    expect(batch.visibleIdentities).toBe(1);
    expect(inventory[0].state).toBe("committed");
    expect(inventory[0].assets[0].verified).toBe(true);
    expect(complete).toBe(false);
    expect(times?.entriesObservedAt).toMatch(/Z$/);
    expect(entries.mock.calls.length).toBeGreaterThan(before);
  });
  it("hides protected detail facts after authorization is revoked rather than retaining a stale report", async () => {
    show();
    await screen.findByRole("heading", { name: nexusBatch.id });
    detail.mockResolvedValue({ error: { status: 403 } } as never);
    await userEvent.click(
      screen.getAllByRole("button", { name: "Refresh batches" })[1],
    );
    await screen.findByText(
      /This migration source requires a platform administrator identity\./,
    );
    expect(
      screen.queryByRole("heading", { name: nexusBatch.id }),
    ).not.toBeInTheDocument();
    expect(
      screen.queryByRole("button", {
        name: "Export current observation report",
      }),
    ).not.toBeInTheDocument();
  });
});
