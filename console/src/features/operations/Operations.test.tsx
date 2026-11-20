import { cleanup, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import {
  listAuditRetentionJobs,
  listLifecycleJobs,
  runRepositoryLifecycleJobNow,
} from "../../client";
import { AntdProvider } from "../../app/AntdProvider";
import { PreferencesProvider } from "../../lib/preferences";
import { TestQueryProvider } from "../../test/queryClient";
import { OperationsPage } from "./Operations";

vi.mock("../../client", () => ({
  cancelRepositoryLifecycleJob: vi.fn(),
  listAuditRetentionJobs: vi.fn(),
  listLifecycleJobs: vi.fn(),
  retryRepositoryLifecycleJob: vi.fn(),
  runRepositoryLifecycleJobNow: vi.fn(),
}));

vi.mock("./ScheduledTasksPanel", () => ({
  ScheduledTasksPanel: () => <div>scheduled tasks</div>,
}));
vi.mock("./WebhookDeliveriesPanel", () => ({
  WebhookDeliveriesPanel: () => <div>webhook deliveries</div>,
}));

const mockListLifecycleJobs = vi.mocked(listLifecycleJobs);
const mockListAuditRetentionJobs = vi.mocked(listAuditRetentionJobs);
const mockRunNow = vi.mocked(runRepositoryLifecycleJobNow);

const repositoryId = "00000000-0000-4000-8000-000000000001";

function lifecycleRecord(state: string) {
  return {
    repositoryId,
    repositoryName: "releases",
    job: {
      id: "job-1",
      kind: "retention",
      state,
      createdAt: "2026-10-01T08:00:00Z",
      attempts: 1,
      maxAttempts: 5,
      progressCurrent: 0,
      progressTotal: 0,
    },
  };
}

function renderPage(path: string) {
  return render(
    <TestQueryProvider>
      <PreferencesProvider>
        <AntdProvider>
          <MemoryRouter initialEntries={[path]}>
            <OperationsPage />
          </MemoryRouter>
        </AntdProvider>
      </PreferencesProvider>
    </TestQueryProvider>,
  );
}

// The card layout renders every row in jsdom; the virtual desktop table does
// not lay out without real geometry.
beforeEach(() => {
  const stub = window.matchMedia;
  vi.spyOn(window, "matchMedia").mockImplementation((query: string) => ({
    ...stub(query),
    matches: query.includes("max-width"),
  }));
});

afterEach(() => {
  cleanup();
  vi.clearAllMocks();
  vi.restoreAllMocks();
});

describe("OperationsPage job history", () => {
  it("loads when the page opens directly on the job history tab", async () => {
    mockListLifecycleJobs.mockResolvedValue({
      data: { items: [lifecycleRecord("failed")] },
    } as never);
    mockListAuditRetentionJobs.mockResolvedValue({ data: [] } as never);

    renderPage("/operations?tab=jobs");

    expect(await screen.findByText("releases")).toBeInTheDocument();
    expect(mockListLifecycleJobs).toHaveBeenCalledTimes(1);
    expect(screen.queryByText("Loading job status…")).not.toBeInTheDocument();
  });

  it("queues a pending job and re-reads the history", async () => {
    mockListLifecycleJobs
      .mockResolvedValueOnce({
        data: { items: [lifecycleRecord("pending")] },
      } as never)
      .mockResolvedValue({
        data: { items: [lifecycleRecord("running")] },
      } as never);
    mockListAuditRetentionJobs.mockResolvedValue({ data: [] } as never);
    mockRunNow.mockResolvedValue({ data: {} } as never);

    renderPage("/operations?tab=jobs");
    await userEvent.click(
      await screen.findByRole("button", {
        name: "Queue for immediate execution",
      }),
    );

    await waitFor(() =>
      expect(mockRunNow).toHaveBeenCalledWith({
        path: { repositoryId, lifecycleJobId: "job-1" },
      }),
    );
    await waitFor(() =>
      expect(
        screen.queryByRole("button", { name: "Queue for immediate execution" }),
      ).not.toBeInTheDocument(),
    );
    expect(mockListLifecycleJobs).toHaveBeenCalledTimes(2);
  });

  it("does not read the job history until its tab is opened", async () => {
    mockListLifecycleJobs.mockResolvedValue({
      data: { items: [] },
    } as never);
    mockListAuditRetentionJobs.mockResolvedValue({ data: [] } as never);

    renderPage("/operations?tab=webhooks");

    await screen.findByRole("tab", { name: "Job history" });
    expect(mockListLifecycleJobs).not.toHaveBeenCalled();
  });

  it("continues past the first window with the returned cursor", async () => {
    const first = lifecycleRecord("completed");
    const second = {
      ...lifecycleRecord("completed"),
      repositoryName: "snapshots",
      job: { ...first.job, id: "job-2" },
    };
    mockListLifecycleJobs.mockImplementation((async (options?: {
      query?: { pageToken?: string };
    }) =>
      options?.query?.pageToken === "tok-2"
        ? { data: { items: [second] } }
        : {
            data: { items: [first], nextPageToken: "tok-2" },
          }) as never);
    mockListAuditRetentionJobs.mockResolvedValue({
      data: [
        {
          id: "audit-job-1",
          state: "completed",
          createdAt: "2026-10-02T08:00:00Z",
        },
      ],
    } as never);

    renderPage("/operations?tab=jobs");
    expect(await screen.findByText("releases")).toBeInTheDocument();
    expect(mockListLifecycleJobs.mock.calls[0]?.[0]?.query).toMatchObject({
      limit: 100,
    });

    await userEvent.click(
      await screen.findByRole("button", { name: "Next Page" }),
    );

    expect(await screen.findByText("snapshots")).toBeInTheDocument();
    expect(mockListLifecycleJobs).toHaveBeenLastCalledWith(
      expect.objectContaining({
        query: { limit: 100, pageToken: "tok-2" },
      }),
    );
    // The audit-retention list is not paged: it belongs to the first page, so
    // page 2 neither repeats it nor reads it again.
    expect(
      screen.queryByRole("heading", { name: "Audit retention" }),
    ).not.toBeInTheDocument();
    expect(mockListAuditRetentionJobs).toHaveBeenCalledTimes(1);
  });

  it("keeps audit-retention jobs out of the paged rows and the pager", async () => {
    mockListLifecycleJobs.mockResolvedValue({
      data: { items: [lifecycleRecord("completed")] },
    } as never);
    mockListAuditRetentionJobs.mockResolvedValue({
      data: [
        {
          id: "audit-job-1",
          state: "completed",
          createdAt: "2026-10-02T08:00:00Z",
        },
      ],
    } as never);

    renderPage("/operations?tab=jobs");

    expect(
      await screen.findByRole("heading", { name: "Audit retention" }),
    ).toBeInTheDocument();
    expect(screen.getByText("releases")).toBeInTheDocument();
    // One lifecycle job and one audit job: no next cursor, so no second page,
    // and no scope notice either.
    expect(screen.getByRole("button", { name: "Next Page" })).toBeDisabled();
    expect(
      screen.queryByText(/cover the 1 jobs loaded/),
    ).not.toBeInTheDocument();
  });

  it("says that the filters and the summary cover the loaded page only", async () => {
    mockListLifecycleJobs.mockResolvedValue({
      data: {
        items: [lifecycleRecord("completed")],
        nextPageToken: "tok-2",
      },
    } as never);
    mockListAuditRetentionJobs.mockResolvedValue({ data: [] } as never);

    renderPage("/operations?tab=jobs");

    expect(
      await screen.findByText(
        "The summary and filters cover the 1 jobs loaded on this page only. Older jobs are on further pages; use the pager to load them.",
      ),
    ).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Next Page" })).toBeEnabled();
  });

  it("re-reads malformed job data when the operator retries", async () => {
    const record = lifecycleRecord("failed");
    mockListLifecycleJobs
      .mockResolvedValueOnce({
        data: {
          items: [
            { ...record, repositoryName: null },
            {
              ...record,
              repositoryId: "00000000-0000-4000-8000-000000000002",
              repositoryName: null,
              job: { ...record.job, id: "job-2" },
            },
          ],
        },
      } as never)
      .mockResolvedValue({ data: { items: [record] } } as never);
    mockListAuditRetentionJobs.mockResolvedValue({ data: [] } as never);
    vi.spyOn(console, "error").mockImplementation(() => undefined);

    renderPage("/operations?tab=jobs");
    await userEvent.click(await screen.findByRole("button", { name: /Retry/ }));

    expect(await screen.findByText("releases")).toBeInTheDocument();
    expect(mockListLifecycleJobs).toHaveBeenCalledTimes(2);
    expect(mockListAuditRetentionJobs).toHaveBeenCalledTimes(2);
    expect(screen.queryByText("Request failed")).not.toBeInTheDocument();
  });
});
