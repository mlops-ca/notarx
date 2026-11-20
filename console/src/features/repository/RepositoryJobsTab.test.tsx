import { cleanup, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";
import { listRepositoryLifecycleJobs } from "../../client";
import type { Repository } from "../../client";
import { PreferencesProvider } from "../../lib/preferences";
import { TestQueryProvider } from "../../test/queryClient";
import { RepositoryJobsTab } from "./RepositoryJobsTab";

vi.mock("../../client", () => ({
  listRepositoryLifecycleJobs: vi.fn(),
  reconcileRepositoryArtifactIntelligence: vi.fn(),
  reconcileRepositoryArtifactScans: vi.fn(),
}));

const mockListJobs = vi.mocked(listRepositoryLifecycleJobs);
const repository: Repository = {
  id: "11111111-1111-4111-8111-111111111111",
  name: "npm-hosted",
  format: "npm",
  type: "hosted",
  visibility: "private",
  mavenStrictPublication: false,
  state: "active",
  version: "1",
};

const job = (id: string) => ({
  id,
  kind: "retention",
  state: "completed",
  createdAt: "2026-10-01T08:00:00Z",
  attempts: 1,
  maxAttempts: 5,
  progressCurrent: 0,
  progressTotal: 0,
});

afterEach(() => {
  cleanup();
  vi.clearAllMocks();
});

describe("RepositoryJobsTab", () => {
  it("continues past the first window with the returned cursor", async () => {
    const user = userEvent.setup();
    mockListJobs.mockImplementation((async (options?: {
      query?: { pageToken?: string };
    }) =>
      options?.query?.pageToken === "jobs-2"
        ? { data: { items: [job("bbbbbbbb-2222")] } }
        : {
            data: { items: [job("aaaaaaaa-1111")], nextPageToken: "jobs-2" },
          }) as never);

    render(
      <TestQueryProvider>
        <PreferencesProvider>
          <RepositoryJobsTab repo={repository} />
        </PreferencesProvider>
      </TestQueryProvider>,
    );

    expect(await screen.findByText("aaaaaaaa…")).toBeInTheDocument();
    expect(mockListJobs.mock.calls[0]?.[0]?.query).toEqual({
      limit: 100,
      pageToken: undefined,
    });
    await user.click(await screen.findByRole("button", { name: "Next Page" }));

    expect(await screen.findByText("bbbbbbbb…")).toBeInTheDocument();
    expect(mockListJobs).toHaveBeenLastCalledWith(
      expect.objectContaining({
        query: { limit: 100, pageToken: "jobs-2" },
      }),
    );
  });
});
