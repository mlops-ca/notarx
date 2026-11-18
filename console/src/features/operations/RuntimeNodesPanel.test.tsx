import { act, cleanup, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";
import { listRuntimeNodes } from "../../client";
import type { RuntimeNode } from "../../client";
import { RuntimeNodesPanel } from "./RuntimeNodesPanel";
import { PreferencesProvider } from "../../lib/preferences";
import { TestQueryProvider } from "../../test/queryClient";

const renderPanel = () =>
  render(
    <TestQueryProvider>
      <PreferencesProvider>
        <RuntimeNodesPanel />
      </PreferencesProvider>
    </TestQueryProvider>,
  );

vi.mock("../../client", () => ({
  listRuntimeNodes: vi.fn(),
}));

const mockListRuntimeNodes = vi.mocked(listRuntimeNodes);

const healthy = {
  status: "healthy" as const,
  online: 1,
  stale: 0,
  offline: 0,
  issues: [],
};

function runtimeNode(status: RuntimeNode["status"]): RuntimeNode {
  return {
    instanceId: "worker-01",
    sessionId: "session-worker-01",
    version: "v0.4.3",
    revision: "abc123",
    roles: ["worker"],
    workerFormats: ["oci"],
    workerKinds: ["reclaim"],
    startedAt: "2026-08-08T08:00:00Z",
    lastSeenAt: "2026-08-08T08:01:00Z",
    status,
  };
}

afterEach(() => {
  cleanup();
  vi.useRealTimers();
  vi.clearAllMocks();
});

describe("RuntimeNodesPanel", () => {
  it("loads and renders runtime capabilities independently", async () => {
    mockListRuntimeNodes.mockResolvedValue({
      data: {
        items: [runtimeNode("online")],
        health: healthy,
        currentSessionId: "session-worker-01",
        releaseSource: "not_configured",
      },
    } as never);

    renderPanel();

    expect(await screen.findByText("worker-01")).toBeInTheDocument();
    expect(screen.getByText("Online")).toBeInTheDocument();
    expect(screen.getByText("oci")).toBeInTheDocument();
    expect(screen.getByText("reclaim")).toBeInTheDocument();
    expect(screen.getByText("1 instances")).toBeInTheDocument();
    expect(screen.getByText("Healthy")).toBeInTheDocument();
    expect(screen.getByText("Connected node")).toBeInTheDocument();
    expect(screen.getByText("v0.4.3")).toBeInTheDocument();
    expect(screen.getByText("abc123")).toBeInTheDocument();
    expect(
      screen.getByText(
        /No release source is configured, so the latest version cannot be determined\./,
      ),
    ).toBeInTheDocument();
  });

  it("keeps polling while no lifecycle task is active", async () => {
    vi.useFakeTimers();
    mockListRuntimeNodes
      .mockResolvedValueOnce({
        data: { items: [runtimeNode("online")], health: healthy },
      } as never)
      .mockResolvedValueOnce({
        data: {
          items: [runtimeNode("stale")],
          health: {
            ...healthy,
            status: "degraded",
            stale: 1,
            issues: [
              {
                code: "stale_nodes",
                severity: "warning",
                message: "Runtime nodes with stale heartbeats exist",
              },
            ],
          },
        },
      } as never);

    renderPanel();
    // Query results are delivered on a scheduler tick, which fake timers hold.
    await act(async () => {
      await vi.advanceTimersByTimeAsync(0);
    });
    expect(screen.getByText("Online")).toBeInTheDocument();

    await act(async () => {
      await vi.advanceTimersByTimeAsync(15_000);
    });
    // The polled answer resolves and is delivered over a few more ticks.
    await act(async () => {
      await vi.advanceTimersByTimeAsync(50);
    });
    expect(mockListRuntimeNodes).toHaveBeenCalledTimes(2);
    expect(screen.getByText("Stale")).toBeInTheDocument();
    expect(
      screen.getByText("Cluster capabilities need attention"),
    ).toBeInTheDocument();
    // In-card alerts keep the shared rounded inset; only filter bars sit flush.
    const banner = screen
      .getByText("Cluster capabilities need attention")
      .closest(".ant-alert");
    expect(banner).not.toHaveClass("rounded-none");
    expect(banner?.parentElement).toHaveClass("px-5");
  });

  it("retries a node-specific error without involving task loading", async () => {
    const user = userEvent.setup();
    mockListRuntimeNodes
      .mockResolvedValueOnce({
        error: new Error("node request failed"),
      } as never)
      .mockResolvedValueOnce({ data: { items: [], health: healthy } } as never);

    renderPanel();
    expect(await screen.findByText("node request failed")).toBeInTheDocument();

    await user.click(screen.getByRole("button", { name: /Retry/ }));
    expect(
      await screen.findByText("No node heartbeats received"),
    ).toBeInTheDocument();
    expect(mockListRuntimeNodes).toHaveBeenCalledTimes(2);
  });

  it("renders legacy rows whose optional arrays are null", async () => {
    mockListRuntimeNodes.mockResolvedValue({
      data: {
        items: [
          {
            ...runtimeNode("offline"),
            sessionId: null,
            roles: null,
            workerFormats: null,
            workerKinds: null,
            version: null,
            revision: null,
          },
        ],
        health: { ...healthy, issues: null },
      },
    } as never);

    renderPanel();

    expect(await screen.findByText("worker-01")).toBeInTheDocument();
    expect(screen.getByText("No format worker")).toBeInTheDocument();
    expect(screen.getByText("Version unknown")).toBeInTheDocument();
  });
});
