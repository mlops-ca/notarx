import { cleanup, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";
import { dryRunRepositoryRetention, getRetentionPolicy } from "../../client";
import type { Repository } from "../../client";
import { PreferencesProvider } from "../../lib/preferences";
import { RepositoryRetentionTab } from "./RepositoryRetentionTab";
import { TestQueryProvider } from "../../test/queryClient";

vi.mock("../../client", () => ({
  dryRunRepositoryRetention: vi.fn(),
  executeRepositoryRetention: vi.fn(),
  getRetentionPolicy: vi.fn(),
  replaceRetentionPolicy: vi.fn(),
}));

vi.mock("../../lib/auth", () => ({
  useAuth: () => ({ token: "" }),
}));

const mockGetRetentionPolicy = vi.mocked(getRetentionPolicy);
const mockDryRunRepositoryRetention = vi.mocked(dryRunRepositoryRetention);
const repository: Repository = {
  id: "11111111-1111-4111-8111-111111111111",
  name: "packages",
  format: "npm",
  type: "hosted",
  anonymousRead: false,
  mavenStrictPublication: false,
  state: "active",
  version: "1",
};

afterEach(() => {
  cleanup();
  vi.clearAllMocks();
});

describe("RepositoryRetentionTab", () => {
  it("renders an enabled registered Cargo retention policy", async () => {
    mockGetRetentionPolicy.mockResolvedValue({
      data: {
        version: "1",
        enabled: false,
        keepDays: 30,
        snapshotKeepDays: 14,
        minimumVersions: 1,
        maximumVersions: 0,
        coordinatePatterns: [],
        protectedPatterns: [],
      },
    } as never);
    render(
      <TestQueryProvider>
        <PreferencesProvider>
          <RepositoryRetentionTab repo={{ ...repository, format: "cargo" }} />
        </PreferencesProvider>
      </TestQueryProvider>,
    );
    expect(
      await screen.findByRole("button", { name: "Save policy" }),
    ).toBeInTheDocument();
    expect(
      screen.queryByText("Hosted repository retention is unavailable"),
    ).not.toBeInTheDocument();
  });
  it.each([
    {
      format: "maven" as const,
      age: "Maven release retention days",
      match: "Only clean matching Maven coordinates",
      protect: "Protect Maven coordinates",
    },
    {
      format: "oci" as const,
      age: "Image version retention days",
      match: "Only clean matching images",
      protect: "Protect image versions",
    },
    {
      format: "conan" as const,
      age: "Recipe revision retention days",
      match: "Only clean matching references",
      protect: "Protect Conan versions",
    },
    {
      format: "raw" as const,
      age: "Asset inactivity retention days",
      match: "Only clean matching paths",
      protect: "Protect paths",
    },
    {
      format: "go" as const,
      age: "Go module version retention days",
      match: "Only clean matching Go modules",
      protect: "Protect Go module versions",
    },
  ])("uses $format cleanup-unit terminology", async (expected) => {
    mockGetRetentionPolicy.mockResolvedValue({
      data: {
        version: "1",
        enabled: false,
        keepDays: 30,
        snapshotKeepDays: 14,
        minimumVersions: 1,
        maximumVersions: 0,
        coordinatePatterns: [],
        protectedPatterns: [],
      },
    } as never);

    render(
      <TestQueryProvider>
        <PreferencesProvider>
          <RepositoryRetentionTab
            repo={{ ...repository, format: expected.format }}
          />
        </PreferencesProvider>
      </TestQueryProvider>,
    );

    expect(await screen.findByText(expected.age)).toBeInTheDocument();
    expect(screen.getByText(expected.match)).toBeInTheDocument();
    expect(screen.getByText(expected.protect)).toBeInTheDocument();
  });

  it("uses npm package terminology instead of Maven coordinates", async () => {
    mockGetRetentionPolicy.mockResolvedValue({
      data: {
        version: "1",
        enabled: false,
        keepDays: 30,
        snapshotKeepDays: 30,
        minimumVersions: 1,
        maximumVersions: 0,
        coordinatePatterns: [],
        protectedPatterns: [],
      },
    } as never);

    render(
      <TestQueryProvider>
        <PreferencesProvider>
          <RepositoryRetentionTab repo={repository} />
        </PreferencesProvider>
      </TestQueryProvider>,
    );

    expect(
      await screen.findByText("npm package version retention days"),
    ).toBeInTheDocument();
    expect(
      screen.getByText("Minimum versions per npm package"),
    ).toBeInTheDocument();
    expect(
      screen.getByText("Only clean matching npm packages"),
    ).toBeInTheDocument();
    expect(
      screen.getByText("Protect npm package versions"),
    ).toBeInTheDocument();
    expect(
      screen.queryByText("Protect Maven coordinates"),
    ).not.toBeInTheDocument();
    expect(screen.queryByText(/groupId:artifactId/)).not.toBeInTheDocument();
  });

  it("uses PyPI project terminology instead of Maven coordinates", async () => {
    mockGetRetentionPolicy.mockResolvedValue({
      data: {
        version: "1",
        enabled: false,
        keepDays: 30,
        snapshotKeepDays: 30,
        minimumVersions: 1,
        maximumVersions: 0,
        coordinatePatterns: [],
        protectedPatterns: [],
      },
    } as never);

    render(
      <TestQueryProvider>
        <PreferencesProvider>
          <RepositoryRetentionTab repo={{ ...repository, format: "pypi" }} />
        </PreferencesProvider>
      </TestQueryProvider>,
    );

    expect(
      await screen.findByText("PyPI project version retention days"),
    ).toBeInTheDocument();
    expect(
      screen.getByText("Minimum versions per PyPI project"),
    ).toBeInTheDocument();
    expect(
      screen.getByText("Only clean matching PyPI projects"),
    ).toBeInTheDocument();
    expect(
      screen.getByText("Protect PyPI project versions"),
    ).toBeInTheDocument();
    expect(
      screen.queryByText("Protect Maven coordinates"),
    ).not.toBeInTheDocument();
  });

  it("labels npm dry-run candidates as package versions", async () => {
    const user = userEvent.setup();
    mockGetRetentionPolicy.mockResolvedValue({
      data: {
        version: "3",
        enabled: true,
        keepDays: 30,
        snapshotKeepDays: 30,
        minimumVersions: 1,
        maximumVersions: 0,
        coordinatePatterns: [],
        protectedPatterns: [],
      },
    } as never);
    mockDryRunRepositoryRetention.mockResolvedValue({
      data: {
        policyVersion: "3",
        totalCandidates: 1,
        summary: {
          reasonCounts: { age: 1, maximumVersions: 0 },
          versionTypeCounts: {
            release: 0,
            snapshot: 0,
            version: 1,
            asset: 0,
          },
          oldestCandidateAt: "2026-07-01T00:00:00Z",
        },
        candidates: [
          {
            format: "npm",
            coordinate: "@team/widget@1.0.0",
            digest: `sha256:${"a".repeat(64)}`,
            createdAt: "2026-07-01T00:00:00Z",
            reasons: ["age"],
            ageDays: 42,
            versionType: "version",
          },
        ],
      },
    } as never);

    render(
      <TestQueryProvider>
        <PreferencesProvider>
          <RepositoryRetentionTab repo={repository} />
        </PreferencesProvider>
      </TestQueryProvider>,
    );

    await user.click(await screen.findByRole("button", { name: "Dry run" }));

    expect(await screen.findByText("npm package version")).toBeInTheDocument();
    expect(screen.queryByText("Recipe revision")).not.toBeInTheDocument();
  });

  it("shows a readable Raw path in retention dry-run results", async () => {
    const user = userEvent.setup();
    mockGetRetentionPolicy.mockResolvedValue({
      data: {
        version: "3",
        enabled: true,
        keepDays: 30,
        snapshotKeepDays: 30,
        minimumVersions: 1,
        maximumVersions: 0,
        coordinatePatterns: [],
        protectedPatterns: [],
      },
    } as never);
    mockDryRunRepositoryRetention.mockResolvedValue({
      data: {
        policyVersion: "3",
        totalCandidates: 1,
        summary: {
          reasonCounts: { age: 1, maximumVersions: 0 },
          versionTypeCounts: {
            release: 0,
            snapshot: 0,
            version: 0,
            asset: 1,
          },
          oldestCandidateAt: "2026-07-01T00:00:00Z",
        },
        candidates: [
          {
            format: "raw",
            coordinate: "ChatGPT%20Image%20%282%29.png",
            digest: `sha256:${"a".repeat(64)}`,
            createdAt: "2026-07-01T00:00:00Z",
            reasons: ["age"],
            ageDays: 42,
            versionType: "asset",
          },
        ],
      },
    } as never);

    render(
      <TestQueryProvider>
        <PreferencesProvider>
          <RepositoryRetentionTab repo={{ ...repository, format: "raw" }} />
        </PreferencesProvider>
      </TestQueryProvider>,
    );

    await user.click(await screen.findByRole("button", { name: "Dry run" }));

    expect(
      await screen.findByText("ChatGPT Image (2).png"),
    ).toBeInTheDocument();
    expect(
      screen.queryByText("ChatGPT%20Image%20%282%29.png"),
    ).not.toBeInTheDocument();
  });
});
