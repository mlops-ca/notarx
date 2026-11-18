import {
  act,
  cleanup,
  fireEvent,
  render,
  screen,
  within,
} from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { PreferencesProvider } from "../../lib/preferences";
import { TestQueryProvider } from "../../test/queryClient";
import { DashboardPage } from "./Dashboard";

const api = vi.hoisted(() => ({
  listGroups: vi.fn(),
  listAudits: vi.fn(),
  getOverviewStatistics: vi.fn(),
}));
vi.mock("../../client", () => api);
vi.mock("../../components/ui/dashboard-charts/loadDashboardPiePlot", () => ({
  preloadDashboardPiePlot: vi.fn(),
}));
vi.mock("../../components/ui/DashboardCharts", () => ({
  StorageByFormatChart: () => <div data-testid="storage-chart" />,
}));

const snapshot = {
  generatedAt: "2026-10-07T00:00:00Z",
  totals: {
    requests: { oneDay: 1, sevenDays: 2, thirtyDays: 3 },
    denied: { oneDay: 0, sevenDays: 0, thirtyDays: 0 },
    usedBytes: 1024,
    objectCount: 1,
  },
  repositories: [
    {
      repositoryId: "synthetic-repository",
      name: "synthetic-raw",
      format: "raw",
      requests: { oneDay: 1, sevenDays: 2, thirtyDays: 3 },
      denied: { oneDay: 0, sevenDays: 0, thirtyDays: 0 },
      usedBytes: 1024,
      objectCount: 1,
    },
  ],
};

function deferred<T>() {
  let resolve!: (value: T) => void;
  const promise = new Promise<T>((done) => {
    resolve = done;
  });
  return { promise, resolve };
}

function renderDashboard() {
  return render(
    <MemoryRouter>
      <TestQueryProvider>
        <PreferencesProvider>
          <DashboardPage />
        </PreferencesProvider>
      </TestQueryProvider>
    </MemoryRouter>,
  );
}

describe("independent dashboard requests", () => {
  beforeEach(() => {
    localStorage.clear();
    vi.resetAllMocks();
    api.listGroups.mockResolvedValue({ data: { items: [] } });
    api.listAudits.mockResolvedValue({ data: [] });
    api.getOverviewStatistics.mockResolvedValue({ data: snapshot });
  });
  afterEach(cleanup);

  it("recovers all sections sharing a malformed answer after one Retry", async () => {
    const error = vi.spyOn(console, "error").mockImplementation(() => {});
    api.getOverviewStatistics.mockResolvedValueOnce({
      data: { ...snapshot, repositories: null },
    });
    try {
      renderDashboard();
      const title = await screen.findByText(
        "Repository activity is unavailable",
      );
      const section = title.closest('[role="alert"]') as HTMLElement;
      fireEvent.click(within(section).getByRole("button", { name: /Retry/ }));
      expect(await screen.findByText("synthetic-raw")).toBeVisible();
      expect(await screen.findByTestId("storage-chart")).toBeVisible();
      expect(screen.getByText("Total requests")).toBeVisible();
      expect(screen.getByText("No audit records")).toBeVisible();
      expect(screen.queryByRole("alert")).not.toBeInTheDocument();
      expect(api.getOverviewStatistics).toHaveBeenCalledTimes(2);
      expect(api.listAudits).toHaveBeenCalledTimes(1);
    } finally {
      error.mockRestore();
    }
  });

  it.each(["statistics", "groups"] as const)(
    "waits for both Retry answers without rendering damaged groups (%s first)",
    async (first) => {
      const error = vi.spyOn(console, "error").mockImplementation(() => {});
      const groups = deferred<{ data: { items: never[] } }>();
      const statistics = deferred<{ data: typeof snapshot }>();
      api.listGroups
        .mockResolvedValueOnce({ data: { items: { length: 1 } } })
        .mockReturnValueOnce(groups.promise);
      api.getOverviewStatistics
        .mockResolvedValueOnce({ data: snapshot })
        .mockReturnValueOnce(statistics.promise);
      try {
        renderDashboard();
        const title = await screen.findByText("Runtime summary is unavailable");
        const section = title.closest('[role="alert"]') as HTMLElement;
        expect(screen.getByText("synthetic-raw")).toBeVisible();
        expect(screen.getByText("No audit records")).toBeVisible();
        error.mockClear();
        fireEvent.click(within(section).getByRole("button", { name: /Retry/ }));
        expect(api.listGroups).toHaveBeenCalledTimes(2);
        expect(api.getOverviewStatistics).toHaveBeenCalledTimes(2);
        await act(async () => {
          if (first === "statistics")
            statistics.resolve({ data: { ...snapshot } });
          else groups.resolve({ data: { items: [] } });
        });
        expect(error).not.toHaveBeenCalled();
        expect(
          screen.getByText("Runtime summary is unavailable"),
        ).toBeVisible();
        expect(screen.getByText("synthetic-raw")).toBeVisible();
        expect(screen.getByText("No audit records")).toBeVisible();
        await act(async () => {
          if (first === "statistics") groups.resolve({ data: { items: [] } });
          else statistics.resolve({ data: { ...snapshot } });
        });
        expect(await screen.findByText("0 member references")).toBeVisible();
        expect(screen.getByText("Total requests")).toBeVisible();
        expect(screen.queryByRole("alert")).not.toBeInTheDocument();
        expect(error).not.toHaveBeenCalled();
        expect(api.listAudits).toHaveBeenCalledTimes(1);
      } finally {
        error.mockRestore();
      }
    },
  );

  it.each([
    ["repository collection", { ...snapshot, repositories: null }],
    [
      "repository requests",
      {
        ...snapshot,
        repositories: [
          { ...snapshot.repositories[0], requests: undefined },
          { ...snapshot.repositories[0], repositoryId: "second" },
        ],
      },
    ],
    [
      "total requests",
      { ...snapshot, totals: { ...snapshot.totals, requests: undefined } },
    ],
  ])("contains malformed %s inside its section", async (_, malformed) => {
    const error = vi.spyOn(console, "error").mockImplementation(() => {});
    api.getOverviewStatistics.mockResolvedValue({ data: malformed });
    try {
      renderDashboard();
      expect(await screen.findByText("No audit records")).toBeVisible();
      expect(screen.getByRole("heading", { name: "Overview" })).toBeVisible();
      expect(screen.getAllByRole("alert").length).toBeGreaterThan(0);
      expect(
        screen
          .getAllByRole("link", { name: "View all →" })
          .map((link) => link.getAttribute("href")),
      ).toContain("/audits");
    } finally {
      error.mockRestore();
    }
  });

  it("shows storage while both optional requests are still pending", async () => {
    api.listGroups.mockReturnValue(new Promise(() => {}));
    api.listAudits.mockReturnValue(new Promise(() => {}));
    renderDashboard();
    expect(await screen.findByTestId("storage-chart")).toBeVisible();
    expect(screen.getByText("synthetic-raw")).toBeVisible();
    expect(screen.getByText("Loading groups…")).toBeVisible();
    expect(screen.getByText("Loading audit events…")).toBeVisible();
  });

  it("keeps storage visible and retries only the failed groups request", async () => {
    api.listGroups.mockResolvedValueOnce({
      error: new Error("synthetic group failure"),
    });
    renderDashboard();
    expect(await screen.findByTestId("storage-chart")).toBeVisible();
    expect(screen.getByText("synthetic group failure")).toBeVisible();
    fireEvent.click(screen.getByRole("button", { name: /Retry/ }));
    expect(await screen.findByText("0 member references")).toBeVisible();
    expect(api.listGroups).toHaveBeenCalledTimes(2);
    expect(api.listAudits).toHaveBeenCalledTimes(1);
    expect(api.getOverviewStatistics).toHaveBeenCalledTimes(1);
  });

  it("shows audit data when statistics reject, then recovers statistics alone", async () => {
    api.getOverviewStatistics.mockRejectedValueOnce(
      new Error("synthetic capacity failure"),
    );
    renderDashboard();
    expect(await screen.findByText("synthetic capacity failure")).toBeVisible();
    expect(screen.getByText("No audit records")).toBeVisible();
    expect(screen.queryByTestId("storage-chart")).not.toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: /Retry/ }));
    expect(await screen.findByTestId("storage-chart")).toBeVisible();
    expect(api.listAudits).toHaveBeenCalledTimes(1);
  });

  it("aborts requests on unmount and ignores their late completion", async () => {
    const late = deferred<{ data: typeof snapshot }>();
    api.getOverviewStatistics.mockReturnValue(late.promise);
    const view = renderDashboard();
    const signal = api.getOverviewStatistics.mock.calls[0][0]
      .signal as AbortSignal;
    expect(signal.aborted).toBe(false);
    view.unmount();
    expect(signal.aborted).toBe(true);
    await act(async () => {
      late.resolve({ data: snapshot });
    });
    expect(screen.queryByTestId("storage-chart")).not.toBeInTheDocument();
  });

  it("preserves the optional endpoint 404 fallback", async () => {
    api.listGroups.mockResolvedValue({ error: { status: 404 } });
    api.listAudits.mockResolvedValue({ error: "404 page not found" });
    renderDashboard();
    expect(await screen.findByTestId("storage-chart")).toBeVisible();
    expect(screen.getByText("0 member references")).toBeVisible();
    expect(screen.getByText("No audit records")).toBeVisible();
    expect(screen.queryByRole("alert")).not.toBeInTheDocument();
  });
});
