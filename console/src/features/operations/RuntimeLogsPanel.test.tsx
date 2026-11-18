import { App } from "antd";
import { cleanup, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter } from "react-router-dom";
import { afterEach, describe, expect, it, vi } from "vitest";
import { listRuntimeLogs } from "../../client";
import { PreferencesProvider } from "../../lib/preferences";
import { RuntimeLogsPanel } from "./RuntimeLogsPanel";

vi.mock("../../client", () => ({ listRuntimeLogs: vi.fn() }));
const mockListRuntimeLogs = vi.mocked(listRuntimeLogs);

afterEach(() => {
  cleanup();
  vi.clearAllMocks();
});

describe("RuntimeLogsPanel", () => {
  it.each([
    "Bad Gateway",
    "<html><body>503 Service Unavailable</body></html>",
    { message: "upstream transport failure", requestId: "gateway-error-1" },
  ])(
    "localizes unknown gateway errors and retains diagnostics: %j",
    async (error) => {
      mockListRuntimeLogs.mockResolvedValueOnce({ error } as never);
      render(
        <MemoryRouter initialEntries={["/system?tab=logs"]}>
          <PreferencesProvider>
            <App>
              <RuntimeLogsPanel />
            </App>
          </PreferencesProvider>
        </MemoryRouter>,
      );
      expect(await screen.findByRole("alert")).toHaveTextContent(
        "Log query failed. Check the connection and retry.",
      );
      expect(screen.getByRole("alert")).not.toHaveTextContent(
        /Bad Gateway|Service Unavailable|upstream transport failure/,
      );
      if (typeof error === "object")
        expect(screen.getByRole("alert")).toHaveTextContent("gateway-error-1");
    },
  );

  it("retains the shared endpoint version mismatch guidance", async () => {
    mockListRuntimeLogs.mockResolvedValueOnce({
      error: "404 page not found",
    } as never);
    render(
      <MemoryRouter initialEntries={["/system?tab=logs"]}>
        <PreferencesProvider>
          <App>
            <RuntimeLogsPanel />
          </App>
        </PreferencesProvider>
      </MemoryRouter>,
    );
    expect(await screen.findByRole("alert")).toHaveTextContent(
      "The Console and Gateway versions may not match",
    );
  });

  it("uses the audit request ID and appends older process-local results", async () => {
    mockListRuntimeLogs
      .mockResolvedValueOnce({
        data: {
          scope: "local",
          instanceId: "gateway-01",
          sessionId: "session-01",
          items: [
            {
              sequence: 2,
              time: "2026-09-29T10:00:00Z",
              level: "ERROR",
              message: "worker failed",
              instanceId: "gateway-01",
              sessionId: "session-01",
              component: "worker",
              operation: "job.run",
              requestId: "job-1",
              traceId: "trace-1",
            },
          ],
          nextSequence: 2,
        },
      } as never)
      .mockResolvedValueOnce({
        data: {
          scope: "local",
          instanceId: "gateway-01",
          sessionId: "session-01",
          items: [],
        },
      } as never);
    render(
      <MemoryRouter initialEntries={["/system?tab=logs&requestId=job-1"]}>
        <PreferencesProvider>
          <App>
            <RuntimeLogsPanel />
          </App>
        </PreferencesProvider>
      </MemoryRouter>,
    );
    expect(await screen.findByText("worker failed")).toBeInTheDocument();
    expect(
      screen.getByText(
        /Search this process’s in-memory logs; restart clears them\./,
      ),
    ).toBeInTheDocument();
    expect(mockListRuntimeLogs).toHaveBeenCalledWith(
      expect.objectContaining({
        query: expect.objectContaining({ requestId: "job-1" }),
      }),
    );
    await userEvent.click(
      screen.getByRole("button", { name: /Load older logs/ }),
    );
    await waitFor(() =>
      expect(mockListRuntimeLogs).toHaveBeenLastCalledWith(
        expect.objectContaining({
          query: expect.objectContaining({
            beforeSequence: 2,
            requestId: "job-1",
          }),
        }),
      ),
    );
    expect(screen.getByText("worker failed")).toBeInTheDocument();
    expect(
      screen.queryByRole("button", { name: /Load older logs/ }),
    ).toBeNull();
  });

  it("does not show pagination for an empty result", async () => {
    mockListRuntimeLogs.mockResolvedValueOnce({
      data: {
        scope: "local",
        instanceId: "gateway-01",
        sessionId: "session-01",
        items: [],
      },
    } as never);
    render(
      <MemoryRouter initialEntries={["/system?tab=logs"]}>
        <PreferencesProvider>
          <App>
            <RuntimeLogsPanel />
          </App>
        </PreferencesProvider>
      </MemoryRouter>,
    );
    expect(
      await screen.findByText("No logs in this range"),
    ).toBeInTheDocument();
    expect(
      screen.queryByRole("button", { name: /Load older logs/ }),
    ).toBeNull();
    expect(screen.queryByRole("alert")).toBeNull();
  });
});
