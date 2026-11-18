import {
  cleanup,
  fireEvent,
  render,
  screen,
  waitFor,
} from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import {
  createRepositoryArtifactScan,
  listRepositoryArtifactIdentities,
  listRepositoryLifecycleJobs,
  reconcileRepositoryArtifactScans,
} from "../../client";
import type { Repository, RepositoryCapabilities } from "../../client";
import { PreferencesProvider } from "../../lib/preferences";
import { RepositoryScanningTab } from "./RepositoryScanningTab";
import { TestQueryProvider } from "../../test/queryClient";

vi.mock("../../client", () => ({
  createRepositoryArtifactScan: vi.fn(),
  listRepositoryArtifactIdentities: vi.fn(),
  listRepositoryLifecycleJobs: vi.fn(),
  reconcileRepositoryArtifactScans: vi.fn(),
}));

const mockCreateScan = vi.mocked(createRepositoryArtifactScan);
const mockListJobs = vi.mocked(listRepositoryLifecycleJobs);
const mockReconcileScans = vi.mocked(reconcileRepositoryArtifactScans);
const mockListArtifactIdentities = vi.mocked(listRepositoryArtifactIdentities);
const digest = `sha256:${"a".repeat(64)}`;
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
const capabilities: RepositoryCapabilities = {
  format: "npm",
  type: "hosted",
  operations: ["read", "publish"],
  artifactScanning: true,
  publicationScanning: true,
};

beforeEach(() => {
  mockListArtifactIdentities.mockResolvedValue({
    data: { items: [] },
  } as never);
});

afterEach(() => {
  cleanup();
  localStorage.clear();
  vi.clearAllMocks();
});

describe("RepositoryScanningTab", () => {
  it("labels scan jobs as a bounded recent window without claiming a complete total", async () => {
    mockListJobs.mockResolvedValue({
      data: Array.from({ length: 100 }, (_, index) => ({
        id: `recent-job-${index}`,
        kind: index < 26 ? "scan" : "intelligence",
        state: "pending",
        createdAt: "2026-08-12T06:00:00Z",
        attempts: 0,
        maxAttempts: 3,
        progressCurrent: 0,
        progressTotal: 0,
      })),
    } as never);
    const { container } = render(
      <TestQueryProvider>
        <PreferencesProvider>
          <RepositoryScanningTab
            repo={repository}
            capabilities={capabilities}
            capabilitiesLoading={false}
            capabilitiesError={null}
            canManage
            canViewJobs
          />
        </PreferencesProvider>
      </TestQueryProvider>,
    );
    expect(
      await screen.findByText(
        "Shows scans among the latest 100 lifecycle jobs; this is not the complete scan history.",
      ),
    ).toBeVisible();
    await waitFor(() =>
      expect(container.querySelectorAll("tbody tr.ant-table-row")).toHaveLength(
        26,
      ),
    );
    expect(container.querySelector(".ant-pagination")).toBeNull();
    expect(screen.queryByText(/of 26 items/)).not.toBeInTheDocument();
  });

  it("searches, selects, and queues a repository artifact without manual identity input", async () => {
    const user = userEvent.setup();
    mockListJobs.mockResolvedValue({ data: [] } as never);
    mockListArtifactIdentities.mockResolvedValue({
      data: {
        items: [
          {
            coordinate: "@team/widget@1.2.3",
            digest,
            size: 2048,
            publishedAt: "2026-08-12T05:00:00Z",
          },
        ],
      },
    } as never);
    mockCreateScan.mockResolvedValue({
      data: {
        id: "scan-job-1",
        kind: "scan",
        state: "pending",
        createdAt: "2026-08-12T06:00:00Z",
        attempts: 0,
        maxAttempts: 3,
        progressCurrent: 0,
        progressTotal: 0,
        details: {
          format: "npm",
          coordinate: "@team/widget@1.2.3",
          digest,
        },
      },
    } as never);

    render(
      <TestQueryProvider>
        <PreferencesProvider>
          <RepositoryScanningTab
            repo={repository}
            capabilities={capabilities}
            capabilitiesLoading={false}
            capabilitiesError={null}
            canManage
            canViewJobs
          />
        </PreferencesProvider>
      </TestQueryProvider>,
    );

    expect(
      await screen.findByText("Select and scan an immutable artifact"),
    ).toBeInTheDocument();
    const artifactPicker = await screen.findByRole("combobox", {
      name: "Search and select an artifact",
    });
    await waitFor(() =>
      expect(mockListArtifactIdentities).toHaveBeenCalledTimes(1),
    );
    await user.click(artifactPicker);
    expect(
      await screen.findByRole("option", {
        name: /@team\/widget@1\.2\.3/,
      }),
    ).toBeInTheDocument();
    const visibleOption = document.querySelector<HTMLElement>(
      ".ant-select-item-option",
    );
    expect(visibleOption).not.toBeNull();
    await user.click(visibleOption!);
    expect(await screen.findByText(digest)).toBeInTheDocument();
    const submitScan = screen.getByRole("button", { name: "Queue scan" });
    expect(submitScan).toBeEnabled();
    await user.click(submitScan);

    await waitFor(() => expect(mockCreateScan).toHaveBeenCalledTimes(1));
    expect(mockCreateScan).toHaveBeenCalledWith({
      path: { repositoryId: repository.id },
      headers: { "Idempotency-Key": expect.stringMatching(/^manual-scan:/) },
      body: { coordinate: "@team/widget@1.2.3", digest },
    });
    expect(await screen.findByText("Scan job queued")).toBeInTheDocument();
    expect(screen.getAllByText("scan-job-1")).not.toHaveLength(0);
    expect(screen.getAllByText("@team/widget@1.2.3")).not.toHaveLength(0);
  });

  it("sends artifact picker text to the canonical identity query", async () => {
    const user = userEvent.setup();
    mockListJobs.mockResolvedValue({ data: [] } as never);

    render(
      <TestQueryProvider>
        <PreferencesProvider>
          <RepositoryScanningTab
            repo={repository}
            capabilities={capabilities}
            capabilitiesLoading={false}
            capabilitiesError={null}
            canManage
            canViewJobs
          />
        </PreferencesProvider>
      </TestQueryProvider>,
    );

    const artifactPicker = await screen.findByRole("combobox", {
      name: "Search and select an artifact",
    });
    await user.click(artifactPicker);
    await user.type(artifactPicker, "widget");

    await waitFor(() =>
      expect(mockListArtifactIdentities).toHaveBeenCalledWith({
        path: { repositoryId: repository.id },
        query: { purpose: "scan", q: "widget", pageSize: 50 },
        signal: expect.any(AbortSignal),
      }),
    );
  });

  it("keeps exact coordinate and digest entry as an advanced fallback", async () => {
    const user = userEvent.setup();
    mockListJobs.mockResolvedValue({ data: [] } as never);
    mockCreateScan.mockResolvedValue({
      data: {
        id: "manual-scan-job",
        kind: "scan",
        state: "pending",
        createdAt: "2026-08-12T06:00:00Z",
        attempts: 0,
        maxAttempts: 3,
        progressCurrent: 0,
        progressTotal: 0,
        details: {
          format: "npm",
          coordinate: "@team/legacy@0.9.0",
          digest,
        },
      },
    } as never);

    render(
      <TestQueryProvider>
        <PreferencesProvider>
          <RepositoryScanningTab
            repo={repository}
            capabilities={capabilities}
            capabilitiesLoading={false}
            capabilitiesError={null}
            canManage
            canViewJobs
          />
        </PreferencesProvider>
      </TestQueryProvider>,
    );

    await user.click(
      await screen.findByRole("button", { name: "Advanced manual input" }),
    );
    fireEvent.change(screen.getByLabelText("Artifact coordinate"), {
      target: { value: "@team/legacy@0.9.0" },
    });
    fireEvent.change(screen.getByLabelText("SHA-256 digest"), {
      target: { value: digest },
    });
    await user.click(screen.getByRole("button", { name: "Queue scan" }));

    await waitFor(() => expect(mockCreateScan).toHaveBeenCalledTimes(1));
    expect(mockCreateScan).toHaveBeenCalledWith({
      path: { repositoryId: repository.id },
      headers: { "Idempotency-Key": expect.stringMatching(/^manual-scan:/) },
      body: { coordinate: "@team/legacy@0.9.0", digest },
    });
  });

  it("explains scanner configuration when the repository cannot scan", async () => {
    mockListJobs.mockResolvedValue({ data: [] } as never);

    render(
      <TestQueryProvider>
        <PreferencesProvider>
          <RepositoryScanningTab
            repo={repository}
            capabilities={{
              ...capabilities,
              artifactScanning: false,
              publicationScanning: false,
            }}
            capabilitiesLoading={false}
            capabilitiesError={null}
            canManage
            canViewJobs
          />
        </PreferencesProvider>
      </TestQueryProvider>,
    );

    expect(
      await screen.findByText("No scanner is configured for this repository"),
    ).toBeInTheDocument();
    expect(screen.getByText("NOTARX_SCANNER_ENDPOINT")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Queue scan" })).toBeDisabled();
    expect(
      screen.getByRole("button", { name: "Reconcile historical artifacts" }),
    ).toBeDisabled();
    expect(mockReconcileScans).not.toHaveBeenCalled();
  });

  it("backfills visible publications from the same scanning workspace", async () => {
    const user = userEvent.setup();
    mockListJobs.mockResolvedValue({ data: [] } as never);
    mockReconcileScans.mockResolvedValue({
      data: {
        repositoryId: repository.id,
        inspected: 7,
        enqueued: 3,
        retried: 1,
        skipped: 3,
        jobIds: ["scan-job-2"],
      },
    } as never);

    render(
      <TestQueryProvider>
        <PreferencesProvider>
          <RepositoryScanningTab
            repo={repository}
            capabilities={capabilities}
            capabilitiesLoading={false}
            capabilitiesError={null}
            canManage
            canViewJobs
          />
        </PreferencesProvider>
      </TestQueryProvider>,
    );

    await user.click(
      await screen.findByRole("button", {
        name: "Reconcile historical artifacts",
      }),
    );

    await waitFor(() => expect(mockReconcileScans).toHaveBeenCalledTimes(1));
    expect(mockReconcileScans).toHaveBeenCalledWith({
      path: { repositoryId: repository.id },
      query: { limit: 500 },
    });
    expect(
      await screen.findByText("Inspected 7 artifacts, queued 3, retried 1"),
    ).toBeInTheDocument();
  });

  it("does not turn capability failures into scanner configuration advice", async () => {
    render(
      <TestQueryProvider>
        <PreferencesProvider>
          <RepositoryScanningTab
            repo={repository}
            capabilities={null}
            capabilitiesLoading={false}
            capabilitiesError={new Error("capability request failed")}
            canManage={false}
            canViewJobs={false}
          />
        </PreferencesProvider>
      </TestQueryProvider>,
    );

    expect(await screen.findByText("Request failed")).toBeInTheDocument();
    expect(
      screen.queryByText("No scanner is configured for this repository"),
    ).not.toBeInTheDocument();
    expect(mockListJobs).not.toHaveBeenCalled();
  });

  it("keeps intelligence mutations disabled for a read-only identity", async () => {
    render(
      <TestQueryProvider>
        <PreferencesProvider>
          <RepositoryScanningTab
            repo={repository}
            capabilities={capabilities}
            capabilitiesLoading={false}
            capabilitiesError={null}
            canManage={false}
            canViewJobs={false}
          />
        </PreferencesProvider>
      </TestQueryProvider>,
    );

    expect(
      await screen.findByText("This identity cannot manage artifact scans"),
    ).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Queue scan" })).toBeDisabled();
    expect(
      screen.getByRole("button", { name: "Reconcile historical artifacts" }),
    ).toBeDisabled();
    expect(
      screen.getByText("Recent jobs are visible to repository administrators"),
    ).toBeInTheDocument();
    expect(mockListJobs).not.toHaveBeenCalled();
  });

  it("asks OCI users for an image name while keeping the digest separate", async () => {
    const user = userEvent.setup();
    render(
      <TestQueryProvider>
        <PreferencesProvider>
          <RepositoryScanningTab
            repo={{ ...repository, format: "oci", name: "oci-hosted" }}
            capabilities={{ ...capabilities, format: "oci" }}
            capabilitiesLoading={false}
            capabilitiesError={null}
            canManage
            canViewJobs={false}
          />
        </PreferencesProvider>
      </TestQueryProvider>,
    );

    await user.click(
      await screen.findByRole("button", { name: "Advanced manual input" }),
    );

    expect(await screen.findByLabelText("Artifact coordinate")).toHaveAttribute(
      "placeholder",
      "team/widget",
    );
    expect(screen.getByLabelText("SHA-256 digest")).toHaveAttribute(
      "placeholder",
      "sha256:…",
    );
  });

  it("localizes the coordinate placeholder for formats without a canonical example", async () => {
    const user = userEvent.setup();
    localStorage.setItem("ag.console.locale", "en-CA");
    mockListJobs.mockResolvedValue({ data: [] } as never);

    render(
      <TestQueryProvider>
        <PreferencesProvider>
          <RepositoryScanningTab
            repo={{ ...repository, format: "apt", name: "apt-hosted" }}
            capabilities={{ ...capabilities, format: "apt" }}
            capabilitiesLoading={false}
            capabilitiesError={null}
            canManage
            canViewJobs={false}
          />
        </PreferencesProvider>
      </TestQueryProvider>,
    );

    await user.click(
      await screen.findByRole("button", { name: "Advanced manual input" }),
    );

    expect(await screen.findByLabelText("Artifact coordinate")).toHaveAttribute(
      "placeholder",
      "Canonical artifact coordinate",
    );
  });
});
