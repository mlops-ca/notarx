import { App } from "antd";
import {
  act,
  cleanup,
  configure,
  fireEvent,
  getConfig,
  render,
  screen,
  waitFor,
} from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";
import { afterEach, describe, expect, it, vi } from "vitest";
import { listRuntimeLogs } from "../../client";
import type { RuntimeLogEntry, RuntimeLogPage } from "../../client";
import { PreferencesProvider } from "../../lib/preferences";
import { RuntimeLogsPanel } from "./RuntimeLogsPanel";

vi.mock("../../client", () => ({ listRuntimeLogs: vi.fn() }));
const api = vi.mocked(listRuntimeLogs);
const row = (sequence: number, sessionId = "session"): RuntimeLogEntry => ({
  sequence,
  time: "2026-10-03T00:00:00Z",
  level: "INFO",
  instanceId: "node",
  sessionId,
  component: "http",
  operation: "http.request",
  message: `event-${sequence}`,
  requestId: "request",
  traceId: "trace",
  method: "GET",
  status: 200,
  durationMs: 12,
});
function page(
  first: number,
  last: number,
  extra: Partial<RuntimeLogPage> = {},
): RuntimeLogPage {
  return {
    scope: "local",
    instanceId: "node",
    sessionId: "session",
    items: Array.from({ length: Math.max(0, last - first + 1) }, (_, i) =>
      row(first + i),
    ),
    afterCursor: `cursor-${last}`,
    order: "ascending",
    hasMore: false,
    retention: { earliestSequence: 1, latestSequence: last, gap: false },
    source: {
      minimumLevel: "INFO",
      accessMode: "limited",
      slowThresholdMs: 1000,
      capacityLines: 1000,
      maxLineBytes: 16384,
      components: ["http", "proxy_worker"],
      componentsTruncated: false,
    },
    ...extra,
  };
}
const reply = (data: RuntimeLogPage) => ({
  data,
  request: new Request("http://127.0.0.1/api/v2/runtime/logs"),
  response: new Response(),
});
const mount = () =>
  render(
    <MemoryRouter initialEntries={["/system?tab=logs&requestId=request"]}>
      <PreferencesProvider>
        <App>
          <RuntimeLogsPanel />
        </App>
      </PreferencesProvider>
    </MemoryRouter>,
  );
const clipboardDescriptor = Object.getOwnPropertyDescriptor(
  navigator,
  "clipboard",
);

const defaultAsyncWrapper = getConfig().asyncWrapper;

afterEach(() => {
  configure({ asyncWrapper: defaultAsyncWrapper });
  cleanup();
  vi.useRealTimers();
  vi.restoreAllMocks();
  api.mockReset();
  localStorage.clear();
  if (clipboardDescriptor)
    Object.defineProperty(navigator, "clipboard", clipboardDescriptor);
  else Reflect.deleteProperty(navigator, "clipboard");
});

describe("incremental runtime log controls", () => {
  for (const newSession of [false, true])
    it(`invalidates selection on a replacement snapshot (session change: ${newSession})`, async () => {
      api
        .mockResolvedValueOnce(reply(page(1, 1)))
        .mockResolvedValueOnce(
          reply(
            page(
              2,
              2,
              newSession
                ? { sessionId: "new-session", items: [row(2, "new-session")] }
                : {},
            ),
          ),
        );
      mount();
      await screen.findByText("event-1");
      await act(async () => {});
      const range = document.createRange();
      range.selectNodeContents(screen.getByText("event-1"));
      const selection = window.getSelection()!;
      selection.removeAllRanges();
      selection.addRange(range);
      fireEvent(document, new Event("selectionchange"));
      expect(
        screen.getByRole("button", { name: /Copy selection/ }),
      ).toBeEnabled();
      fireEvent.click(screen.getByRole("button", { name: /Refresh snapshot/ }));
      await screen.findByText("event-2");
      expect(
        screen.getByRole("button", { name: /Copy selection/ }),
      ).toBeDisabled();
      expect(selection.toString()).toBe("");
    });

  it("does not revive selected protected rows after authorization recovery", async () => {
    api
      .mockResolvedValueOnce(reply(page(1, 1)))
      .mockRejectedValueOnce({
        status: 403,
        code: "password_change_required",
        message: "synthetic reset",
      })
      .mockResolvedValueOnce(reply(page(2, 2)));
    mount();
    await screen.findByText("event-1");
    // Let the snapshot's selection reset settle before creating a selection.
    await act(async () => {});
    const range = document.createRange();
    range.selectNodeContents(screen.getByText("event-1"));
    const selection = window.getSelection()!;
    selection.removeAllRanges();
    selection.addRange(range);
    fireEvent(document, new Event("selectionchange"));
    expect(
      screen.getByRole("button", { name: /Copy selection/ }),
    ).toBeEnabled();
    fireEvent.click(screen.getByRole("button", { name: /Refresh snapshot/ }));
    await screen.findByText(
      "Update your password before querying runtime logs.",
    );
    expect(screen.queryByRole("button", { name: /Copy selection/ })).toBeNull();
    fireEvent.click(screen.getByRole("button", { name: /Refresh snapshot/ }));
    await screen.findByText("event-2");
    expect(
      screen.getByRole("button", { name: /Copy selection/ }),
    ).toBeDisabled();
  });

  it("rechecks an empty native selection at click time even without a selection event", async () => {
    // Observe the committed row without Testing Library draining passive effects.
    // The selection must be created after the snapshot reset effect settles.
    configure({ asyncWrapper: async (callback) => callback() });
    api.mockResolvedValue(reply(page(1, 1)));
    const writeText = vi.fn().mockResolvedValue(undefined);
    Object.defineProperty(navigator, "clipboard", {
      configurable: true,
      value: { writeText },
    });
    mount();
    await screen.findByText("event-1");
    await act(async () => {});
    const range = document.createRange();
    range.selectNodeContents(screen.getByText("event-1"));
    const selection = window.getSelection()!;
    selection.removeAllRanges();
    selection.addRange(range);
    fireEvent(document, new Event("selectionchange"));
    expect(
      screen.getByRole("button", { name: /Copy selection/ }),
    ).toBeEnabled();
    selection.removeAllRanges();
    fireEvent.click(screen.getByRole("button", { name: /Copy selection/ }));
    expect(writeText).not.toHaveBeenCalled();
    expect(
      screen.getByRole("button", { name: /Copy selection/ }),
    ).toBeDisabled();
  });

  it("copies a full multi-row selection and rejects an oversized UTF-8 selection visibly", async () => {
    const items = Array.from({ length: 8 }, (_, i) => ({
      ...row(i + 1),
      message: String(i + 1).repeat(3000),
    }));
    api.mockResolvedValue(reply(page(1, 8, { items })));
    const writeText = vi.fn().mockResolvedValue(undefined);
    Object.defineProperty(navigator, "clipboard", {
      configurable: true,
      value: { writeText },
    });
    mount();
    const viewport = await screen.findByLabelText("Runtime log stream");
    const range = document.createRange();
    range.selectNodeContents(viewport);
    const selection = window.getSelection()!;
    selection.removeAllRanges();
    selection.addRange(range);
    const actualSelected = selection.toString();
    expect(actualSelected.length).toBeGreaterThan(24_000);
    fireEvent(document, new Event("selectionchange"));
    fireEvent.click(screen.getByRole("button", { name: /Copy selection/ }));
    await waitFor(() => expect(writeText).toHaveBeenCalledWith(actualSelected));
    vi.spyOn(selection, "toString").mockReturnValue("€".repeat(350_000));
    fireEvent(document, new Event("selectionchange"));
    expect(
      screen.getByRole("button", { name: /Copy selection/ }),
    ).toBeDisabled();
    expect(
      screen.getByText("Selection exceeds 1 MiB; select less text to copy"),
    ).toBeInTheDocument();
    expect(writeText).toHaveBeenCalledTimes(1);
  });

  it("counts duplicate sequences inside one response only once as unread", async () => {
    api
      .mockResolvedValueOnce(reply(page(1, 1)))
      .mockResolvedValueOnce(reply(page(2, 2, { items: [row(2), row(2)] })));
    mount();
    await screen.findByText("event-1");
    const viewport = screen.getByLabelText("Runtime log stream");
    Object.defineProperties(viewport, {
      scrollHeight: { configurable: true, value: 1000 },
      clientHeight: { configurable: true, value: 100 },
      scrollTop: { configurable: true, writable: true, value: 20 },
    });
    fireEvent.scroll(viewport);
    await act(async () =>
      fireEvent.click(screen.getByRole("button", { name: /Follow new logs/ })),
    );
    expect(screen.getAllByText("event-2")).toHaveLength(1);
    expect(
      screen.getByRole("button", { name: /.+ unread · Resume at bottom/ }),
    ).toHaveTextContent("1 unread");
  });
  it("drains 100+ pages from consumed cursors, deduplicates, and preserves the live anchor through older history", async () => {
    api
      .mockResolvedValueOnce(reply(page(230, 230, { nextSequence: 230 })))
      .mockResolvedValueOnce(
        reply(page(1, 2, { afterCursor: "wrong-history-anchor" })),
      )
      .mockResolvedValueOnce(reply(page(231, 330, { hasMore: true })))
      .mockResolvedValueOnce(reply(page(330, 429, { hasMore: true })))
      .mockResolvedValueOnce(reply(page(430, 460)));
    mount();
    await screen.findByText("event-230");
    fireEvent.click(screen.getByRole("button", { name: /Load older logs/ }));
    await screen.findByText("event-1");
    vi.useFakeTimers();
    await act(async () =>
      fireEvent.click(screen.getByRole("button", { name: /Follow new logs/ })),
    );
    expect(api).toHaveBeenLastCalledWith(
      expect.objectContaining({
        query: expect.objectContaining({
          afterCursor: "cursor-230",
          beforeSequence: undefined,
          requestId: "request",
          limit: 100,
        }),
      }),
    );
    await act(async () => vi.advanceTimersByTimeAsync(1000));
    expect(api).toHaveBeenLastCalledWith(
      expect.objectContaining({
        query: expect.objectContaining({ afterCursor: "cursor-330" }),
      }),
    );
    await act(async () => vi.advanceTimersByTimeAsync(1000));
    expect(api).toHaveBeenLastCalledWith(
      expect.objectContaining({
        query: expect.objectContaining({ afterCursor: "cursor-429" }),
      }),
    );
    const numbers = [
      ...document.querySelectorAll<HTMLElement>("[data-sequence]"),
    ].map((node) => Number(node.dataset.sequence));
    expect(numbers).toEqual([
      1,
      2,
      ...Array.from({ length: 231 }, (_, i) => i + 230),
    ]);
    expect(
      screen.queryByText(/Server retention overwrote some unread logs/),
    ).toBeNull();
  });

  it("pauses network reads, suspends hidden-page polling and resumes from the same cursor", async () => {
    api
      .mockResolvedValueOnce(reply(page(1, 1)))
      .mockResolvedValueOnce(reply(page(2, 2)))
      .mockResolvedValue(reply(page(3, 3)));
    mount();
    await screen.findByText("event-1");
    vi.useFakeTimers();
    await act(async () =>
      fireEvent.click(screen.getByRole("button", { name: /Follow new logs/ })),
    );
    expect(screen.getByText("event-2")).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: /Pause follow/ }));
    await act(async () => vi.advanceTimersByTimeAsync(10_000));
    expect(api).toHaveBeenCalledTimes(2);
    await act(async () =>
      fireEvent.click(screen.getByRole("button", { name: /Follow new logs/ })),
    );
    expect(api).toHaveBeenLastCalledWith(
      expect.objectContaining({
        query: expect.objectContaining({ afterCursor: "cursor-2" }),
      }),
    );
    const hidden = vi.spyOn(document, "hidden", "get").mockReturnValue(true);
    fireEvent(document, new Event("visibilitychange"));
    const calls = api.mock.calls.length;
    await act(async () => vi.advanceTimersByTimeAsync(10_000));
    expect(api).toHaveBeenCalledTimes(calls);
    hidden.mockReturnValue(false);
    fireEvent(document, new Event("visibilitychange"));
    await act(async () => vi.advanceTimersByTimeAsync(3000));
    expect(api).toHaveBeenCalledTimes(calls + 1);
    expect(api).toHaveBeenLastCalledWith(
      expect.objectContaining({
        query: expect.objectContaining({ afterCursor: "cursor-3" }),
      }),
    );
  });

  it("does not force the bottom while reading above it and reports only fetched unread events", async () => {
    api
      .mockResolvedValueOnce(reply(page(1, 1)))
      .mockResolvedValueOnce(reply(page(2, 3)));
    mount();
    await screen.findByText("event-1");
    // Complete the initial snapshot's scroll effect before simulating a reader
    // who has scrolled away from the bottom.
    await act(async () => {});
    const viewport = screen.getByLabelText("Runtime log stream");
    Object.defineProperties(viewport, {
      scrollHeight: { configurable: true, value: 1000 },
      clientHeight: { configurable: true, value: 100 },
      scrollTop: { configurable: true, writable: true, value: 20 },
    });
    fireEvent.scroll(viewport);
    await act(async () =>
      fireEvent.click(screen.getByRole("button", { name: /Follow new logs/ })),
    );
    expect(viewport.scrollTop).toBe(20);
    expect(
      screen.getByRole("button", { name: /2 unread/ }),
    ).toBeInTheDocument();
  });

  it("keeps refresh data after failure, but discards protected data on permission revocation", async () => {
    // Observe the committed snapshot before passive effects drain. Ant Design
    // may still ignore clicks while loading without setting HTML disabled.
    configure({ asyncWrapper: async (callback) => callback() });
    api
      .mockResolvedValueOnce(reply(page(1, 1)))
      .mockRejectedValueOnce({
        status: 503,
        code: "log_buffer_unavailable",
        message: "synthetic unavailable",
      })
      .mockRejectedValueOnce({
        status: 403,
        code: "password_change_required",
        message: "synthetic reset",
      });
    mount();
    await screen.findByText("event-1");
    await waitFor(() => {
      const refresh = screen.getByRole("button", { name: /Refresh snapshot/ });
      expect(refresh).not.toBeDisabled();
      expect(refresh).not.toHaveClass("ant-btn-loading");
    });
    fireEvent.click(screen.getByRole("button", { name: /Refresh snapshot/ }));
    expect(api).toHaveBeenCalledTimes(2);
    await screen.findByText(
      "Memory log queries are disabled on this node. Check the buffer configuration.",
    );
    expect(screen.getByText("event-1")).toBeInTheDocument();
    await waitFor(() => {
      const refresh = screen.getByRole("button", { name: /Refresh snapshot/ });
      expect(refresh).not.toBeDisabled();
      expect(refresh).not.toHaveClass("ant-btn-loading");
    });
    fireEvent.click(screen.getByRole("button", { name: /Refresh snapshot/ }));
    expect(api).toHaveBeenCalledTimes(3);
    await screen.findByText(
      "Update your password before querying runtime logs.",
    );
    expect(screen.queryByText("event-1")).toBeNull();
    expect(
      screen.queryByRole("status", { name: /SearchRuntime logs/ }),
    ).toBeNull();
  });

  it("clears the stream when an incremental response changes session", async () => {
    api.mockResolvedValueOnce(reply(page(1, 1))).mockResolvedValueOnce(
      reply(
        page(2, 2, {
          sessionId: "new-session",
          items: [row(2, "new-session")],
        }),
      ),
    );
    mount();
    await screen.findByText("event-1");
    fireEvent.click(screen.getByRole("button", { name: /Follow new logs/ }));
    await screen.findByText("Process or session changed; refresh the snapshot");
    expect(screen.queryByText("event-1")).toBeNull();
    expect(screen.queryByText("event-2")).toBeNull();
  });

  it("resets filters and cursors, rejecting a stale in-flight follow result", async () => {
    let complete: ((value: ReturnType<typeof reply>) => void) | undefined;
    api
      .mockResolvedValueOnce(reply(page(1, 1)))
      .mockImplementationOnce(
        () =>
          new Promise<ReturnType<typeof reply>>((resolve) => {
            complete = resolve;
          }),
      )
      .mockResolvedValueOnce(reply(page(7, 7)));
    mount();
    await screen.findByText("event-1");
    fireEvent.click(screen.getByRole("button", { name: /Follow new logs/ }));
    fireEvent.click(screen.getByRole("button", { name: /Clear filters/ }));
    await screen.findByText("event-7");
    await act(async () => complete?.(reply(page(99, 99))));
    expect(screen.queryByText("event-99")).toBeNull();
    expect(screen.queryByText("event-1")).toBeNull();
    expect(api.mock.calls.at(-1)?.[0]?.query?.afterCursor).toBeUndefined();
    expect(api.mock.calls.at(-1)?.[0]?.query?.requestId).toBeUndefined();
  });

  it("bounds a stalled request, retaining the previous snapshot with an error", async () => {
    api.mockResolvedValueOnce(reply(page(1, 1))).mockImplementationOnce(
      (options) =>
        new Promise<ReturnType<typeof reply>>((_, reject) => {
          options?.signal?.addEventListener("abort", () =>
            reject(new DOMException("Canceled", "AbortError")),
          );
        }),
    );
    mount();
    await screen.findByText("event-1");
    vi.useFakeTimers();
    fireEvent.click(screen.getByRole("button", { name: /Follow new logs/ }));
    await act(async () => vi.advanceTimersByTimeAsync(5000));
    expect(
      screen.getByText("Log query failed. Check the connection and retry."),
    ).toBeInTheDocument();
    expect(screen.getByText("event-1")).toBeInTheDocument();
    expect(api.mock.calls.at(-1)?.[0]?.signal?.aborted).toBe(true);
  });

  it("preserves a paused in-flight cursor and discards the canceled result", async () => {
    let complete: ((value: ReturnType<typeof reply>) => void) | undefined;
    api
      .mockResolvedValueOnce(reply(page(1, 1)))
      .mockImplementationOnce(
        () =>
          new Promise<ReturnType<typeof reply>>((resolve) => {
            complete = resolve;
          }),
      )
      .mockResolvedValueOnce(reply(page(2, 3)));
    mount();
    await screen.findByText("event-1");
    fireEvent.click(screen.getByRole("button", { name: /Follow new logs/ }));
    fireEvent.click(screen.getByRole("button", { name: /Pause follow/ }));
    await act(async () => complete?.(reply(page(2, 2))));
    expect(screen.queryByText("event-2")).toBeNull();
    fireEvent.click(screen.getByRole("button", { name: /Follow new logs/ }));
    await screen.findByText("event-3");
    expect(screen.getByText("event-2")).toBeInTheDocument();
    expect(api.mock.calls.at(-1)?.[0]?.query?.afterCursor).toBe("cursor-1");
  });

  it("announces a new session on refresh without mixing records", async () => {
    // Keep the snapshot commit separate from Ant Design's loading effect.
    configure({ asyncWrapper: async (callback) => callback() });
    api.mockResolvedValueOnce(reply(page(1, 1))).mockResolvedValueOnce(
      reply(
        page(2, 2, {
          sessionId: "new-session",
          items: [row(2, "new-session")],
        }),
      ),
    );
    mount();
    await screen.findByText("event-1");
    await waitFor(() => {
      const refresh = screen.getByRole("button", { name: /Refresh snapshot/ });
      expect(refresh).not.toBeDisabled();
      expect(refresh).not.toHaveClass("ant-btn-loading");
    });
    fireEvent.click(screen.getByRole("button", { name: /Refresh snapshot/ }));
    expect(api).toHaveBeenCalledTimes(2);
    await screen.findByText("event-2");
    expect(screen.queryByText("event-1")).toBeNull();
    expect(
      screen.getByText(
        "Changed to a new process or session; previous records cleared",
      ),
    ).toBeInTheDocument();
  });

  it("separates retention loss from local trimming and bounds a high-volume stream", async () => {
    api
      .mockResolvedValueOnce(reply(page(1, 50)))
      .mockResolvedValueOnce(reply(page(51, 150, { hasMore: true })))
      .mockResolvedValueOnce(reply(page(151, 250, { hasMore: true })))
      .mockResolvedValueOnce(reply(page(251, 350, { hasMore: true })))
      .mockResolvedValueOnce(
        reply(
          page(351, 430, {
            retention: {
              earliestSequence: 350,
              latestSequence: 430,
              gap: true,
            },
          }),
        ),
      );
    mount();
    await screen.findByText("event-50");
    vi.useFakeTimers();
    await act(async () =>
      fireEvent.click(screen.getByRole("button", { name: /Follow new logs/ })),
    );
    for (let i = 0; i < 3; i++)
      await act(async () => vi.advanceTimersByTimeAsync(1000));
    expect(document.querySelectorAll("[data-sequence]")).toHaveLength(300);
    expect(
      screen.getByText(/Client capacity removed 130 rows/),
    ).toBeInTheDocument();
    expect(
      screen.getByText("Server retention overwrote some unread logs"),
    ).toBeInTheDocument();
  });

  for (const locale of ["en-CA"])
    it(`reports actual source policy and missing-event uncertainty in ${locale}`, async () => {
      localStorage.setItem("ag.console.locale", locale);
      api.mockResolvedValueOnce(reply(page(1, 0)));
      mount();
      await waitFor(() =>
        expect(screen.getByText("No logs in this range")).toBeInTheDocument(),
      );
      expect(
        screen.getByText(/INFO.*limited.*1000ms.*1000/),
      ).toBeInTheDocument();
      expect(
        screen.getByText(/A DEBUG filter does not enable DEBUG/),
      ).toBeInTheDocument();
    });
});
