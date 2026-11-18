import { act, cleanup, render, screen, waitFor } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import {
  getAptRepositorySigningState,
  getQuarantineReadPolicy,
  getSecurityPolicy,
  replaceQuarantineReadPolicy,
  replaceSecurityPolicy,
} from "../../client";
import type {
  QuarantineReadPolicy,
  Repository,
  SecurityPolicy,
} from "../../client";
import { PreferencesProvider } from "../../lib/preferences";
import { RepositorySecurityTab } from "./RepositorySecurityTab";
import { TestQueryProvider } from "../../test/queryClient";
import { repositoryKeys } from "../../lib/query/keys";

vi.mock("../../client", () => ({
  getAptRepositorySigningState: vi.fn(),
  getSecurityPolicy: vi.fn(),
  replaceSecurityPolicy: vi.fn(),
  getQuarantineReadPolicy: vi.fn(),
  replaceQuarantineReadPolicy: vi.fn(),
}));

const mockGetAptRepositorySigningState = vi.mocked(
  getAptRepositorySigningState,
);
const mockGetSecurityPolicy = vi.mocked(getSecurityPolicy);
const mockReplaceSecurityPolicy = vi.mocked(replaceSecurityPolicy);
const mockGetQuarantineReadPolicy = vi.mocked(getQuarantineReadPolicy);
const mockReplaceQuarantineReadPolicy = vi.mocked(replaceQuarantineReadPolicy);
const repo: Repository = {
  id: "11111111-1111-4111-8111-111111111111",
  name: "releases",
  format: "maven",
  type: "hosted",
  anonymousRead: false,
  mavenStrictPublication: false,
  state: "active",
  version: "1",
};

type PolicyAnswer<T> = { data: T; request: Request; response: Response };
function policyAnswer<T>(data: T): PolicyAnswer<T> {
  return {
    data,
    request: new Request("https://gateway.example.test/policy"),
    response: new Response(),
  };
}

afterEach(() => {
  cleanup();
  vi.clearAllMocks();
});

beforeEach(() => {
  mockGetQuarantineReadPolicy.mockResolvedValue({
    data: { version: "1", enabled: false },
  } as never);
});

describe("RepositorySecurityTab", () => {
  it("ignores completed saves from the previous repository in cached policy editors", async () => {
    const user = userEvent.setup();
    const nextRepo = { ...repo, id: "22222222-2222-4222-8222-222222222222" };
    const client = new QueryClient({
      defaultOptions: { queries: { retry: false, staleTime: Infinity } },
    });
    client.setQueryData(repositoryKeys.securityPolicy(repo.id), {
      version: "1",
      enabled: false,
      requireSignature: false,
    });
    client.setQueryData(repositoryKeys.quarantineReadPolicy(repo.id), {
      version: "1",
      enabled: true,
    });
    client.setQueryData(repositoryKeys.securityPolicy(nextRepo.id), {
      version: "2",
      enabled: true,
      requireSignature: true,
    });
    client.setQueryData(repositoryKeys.quarantineReadPolicy(nextRepo.id), {
      version: "2",
      enabled: true,
    });
    let admissionAnswer!: (value: PolicyAnswer<SecurityPolicy>) => void;
    let quarantineAnswer!: (value: PolicyAnswer<QuarantineReadPolicy>) => void;
    mockReplaceSecurityPolicy
      .mockImplementationOnce(
        () =>
          new Promise<PolicyAnswer<SecurityPolicy>>(
            (resolve) => (admissionAnswer = resolve),
          ),
      )
      .mockResolvedValueOnce(
        policyAnswer({
          version: "3",
          enabled: true,
          requireSignature: true,
          autoScanOnPublish: true,
        }),
      );
    mockReplaceQuarantineReadPolicy
      .mockImplementationOnce(
        () =>
          new Promise<PolicyAnswer<QuarantineReadPolicy>>(
            (resolve) => (quarantineAnswer = resolve),
          ),
      )
      .mockResolvedValueOnce(policyAnswer({ version: "3", enabled: false }));
    const content = (repository: Repository) => (
      <QueryClientProvider client={client}>
        <PreferencesProvider>
          <RepositorySecurityTab repo={repository} publicationScanning />
        </PreferencesProvider>
      </QueryClientProvider>
    );
    const view = render(content(repo));
    await screen.findByText("Current version 1");
    await user.click(
      screen.getByRole("switch", { name: "Scan after publication" }),
    );
    await user.click(screen.getByRole("button", { name: "Save policy" }));
    await user.click(
      screen.getByRole("switch", { name: "Block quarantined artifact reads" }),
    );
    await user.click(screen.getByRole("button", { name: "Save read policy" }));
    view.rerender(content(nextRepo));
    await screen.findByText("Current version 2");
    await act(async () => {
      admissionAnswer(
        policyAnswer({
          version: "2",
          enabled: false,
          requireSignature: false,
          autoScanOnPublish: true,
        }),
      );
      quarantineAnswer(policyAnswer({ version: "2", enabled: false }));
    });
    expect(
      screen.getByRole("switch", { name: "Enforce admission checks" }),
    ).toBeChecked();
    expect(
      screen.getByRole("switch", { name: "Require a signature" }),
    ).toBeChecked();
    expect(
      screen.getByRole("switch", { name: "Block quarantined artifact reads" }),
    ).toBeChecked();
    expect(screen.getByRole("button", { name: "Save policy" })).toBeDisabled();
    expect(
      screen.getByRole("button", { name: "Save read policy" }),
    ).toBeDisabled();
    expect(
      screen.queryByText("Repository security policy saved"),
    ).not.toBeInTheDocument();
    expect(
      screen.queryByText("Quarantine read policy saved"),
    ).not.toBeInTheDocument();
    await user.click(
      screen.getByRole("switch", { name: "Scan after publication" }),
    );
    await user.click(screen.getByRole("button", { name: "Save policy" }));
    await user.click(
      screen.getByRole("switch", { name: "Block quarantined artifact reads" }),
    );
    await user.click(screen.getByRole("button", { name: "Save read policy" }));
    expect(mockReplaceSecurityPolicy).toHaveBeenLastCalledWith(
      expect.objectContaining({
        path: { repositoryId: nextRepo.id },
        headers: { "If-Match": "2" },
        body: expect.objectContaining({
          version: "2",
          enabled: true,
          requireSignature: true,
          autoScanOnPublish: true,
        }),
      }),
    );
    expect(mockReplaceQuarantineReadPolicy).toHaveBeenLastCalledWith({
      path: { repositoryId: nextRepo.id },
      headers: { "If-Match": "2" },
      body: { version: "2", enabled: false },
    });
  });

  it("updates a cached admission draft before saving a newer policy version", async () => {
    const user = userEvent.setup();
    const client = new QueryClient({
      defaultOptions: { queries: { retry: false, staleTime: 0 } },
    });
    client.setQueryData(repositoryKeys.securityPolicy(repo.id), {
      version: "1",
      enabled: false,
      requireSignature: false,
    });
    client.setQueryData(repositoryKeys.quarantineReadPolicy(repo.id), {
      version: "1",
      enabled: false,
    });
    let answer!: (value: PolicyAnswer<SecurityPolicy>) => void;
    mockGetSecurityPolicy.mockImplementationOnce(
      () =>
        new Promise<PolicyAnswer<SecurityPolicy>>(
          (resolve) => (answer = resolve),
        ),
    );
    mockReplaceSecurityPolicy.mockResolvedValue(
      policyAnswer({
        version: "3",
        enabled: true,
        requireSignature: true,
        autoScanOnPublish: true,
      }),
    );
    render(
      <QueryClientProvider client={client}>
        <PreferencesProvider>
          <RepositorySecurityTab repo={repo} publicationScanning />
        </PreferencesProvider>
      </QueryClientProvider>,
    );
    expect(await screen.findByText("Current version 1")).toBeInTheDocument();
    await act(async () =>
      answer(
        policyAnswer({ version: "2", enabled: true, requireSignature: true }),
      ),
    );
    await screen.findByText("Current version 2");
    expect(
      screen.getByRole("switch", { name: "Enforce admission checks" }),
    ).toBeChecked();
    expect(
      screen.getByRole("switch", { name: "Require a signature" }),
    ).toBeChecked();
    expect(screen.getByRole("button", { name: "Save policy" })).toBeDisabled();
    await user.click(
      screen.getByRole("switch", { name: "Scan after publication" }),
    );
    await user.click(screen.getByRole("button", { name: "Save policy" }));
    await waitFor(() =>
      expect(mockReplaceSecurityPolicy).toHaveBeenCalledWith(
        expect.objectContaining({
          headers: { "If-Match": "2" },
          body: expect.objectContaining({
            version: "2",
            enabled: true,
            requireSignature: true,
            autoScanOnPublish: true,
          }),
        }),
      ),
    );
  });

  it("updates a cached quarantine draft before saving a newer policy version", async () => {
    const user = userEvent.setup();
    const client = new QueryClient({
      defaultOptions: { queries: { retry: false, staleTime: 0 } },
    });
    client.setQueryData(repositoryKeys.securityPolicy(repo.id), {
      version: "1",
      enabled: false,
    });
    client.setQueryData(repositoryKeys.quarantineReadPolicy(repo.id), {
      version: "1",
      enabled: false,
    });
    mockGetSecurityPolicy.mockResolvedValue({
      data: { version: "1", enabled: false },
    } as never);
    let answer!: (value: PolicyAnswer<QuarantineReadPolicy>) => void;
    mockGetQuarantineReadPolicy.mockImplementationOnce(
      () =>
        new Promise<PolicyAnswer<QuarantineReadPolicy>>(
          (resolve) => (answer = resolve),
        ),
    );
    mockReplaceQuarantineReadPolicy.mockResolvedValue({
      data: { version: "3", enabled: false },
    } as never);
    render(
      <QueryClientProvider client={client}>
        <PreferencesProvider>
          <RepositorySecurityTab repo={repo} publicationScanning />
        </PreferencesProvider>
      </QueryClientProvider>,
    );
    await screen.findByText("Read policy version 1");
    await act(async () =>
      answer(policyAnswer({ version: "2", enabled: true })),
    );
    await screen.findByText("Read policy version 2");
    expect(
      screen.getByRole("switch", { name: "Block quarantined artifact reads" }),
    ).toBeChecked();
    expect(
      screen.getByRole("button", { name: "Save read policy" }),
    ).toBeDisabled();
    await user.click(
      screen.getByRole("switch", { name: "Block quarantined artifact reads" }),
    );
    await user.click(screen.getByRole("button", { name: "Save read policy" }));
    await waitFor(() =>
      expect(mockReplaceQuarantineReadPolicy).toHaveBeenCalledWith({
        path: { repositoryId: repo.id },
        headers: { "If-Match": "2" },
        body: { version: "2", enabled: false },
      }),
    );
  });

  it("shows APT trust rotation and immutable snapshot evidence without loading unsupported policies", async () => {
    mockGetAptRepositorySigningState.mockResolvedValue({
      data: {
        repositoryId: repo.id,
        signerMode: "remote",
        readiness: "rotation_overlap",
        trustedFingerprints: ["a".repeat(40), "b".repeat(40)],
        currentKeyRole: "active",
        currentSnapshot: {
          id: "22222222-2222-4222-8222-222222222222",
          repositoryId: repo.id,
          suite: "stable",
          sequence: 7,
          state: "visible",
          releaseDigest: `sha256:${"c".repeat(64)}`,
          inReleaseDigest: `sha256:${"d".repeat(64)}`,
          signerIdentity: "release@example.test",
          keyFingerprint: "a".repeat(40),
          signatureAlgorithm: "rsa4096-sha256",
          createdAt: "2026-08-13T08:00:00Z",
          publishedAt: "2026-08-13T08:01:00Z",
        },
      },
    } as never);

    render(
      <TestQueryProvider>
        <PreferencesProvider>
          <RepositorySecurityTab
            repo={{ ...repo, format: "apt" }}
            publicationScanning={false}
          />
        </PreferencesProvider>
      </TestQueryProvider>,
    );

    expect(
      await screen.findByText("Signing trust and current snapshot"),
    ).toBeInTheDocument();
    expect(screen.getByText("Key rotation overlap")).toBeInTheDocument();
    expect(screen.getByText("stable / 7")).toBeInTheDocument();
    expect(screen.getByText("release@example.test")).toBeInTheDocument();
    expect(screen.getByText("In use")).toBeInTheDocument();
    expect(mockGetSecurityPolicy).not.toHaveBeenCalled();
    expect(mockGetQuarantineReadPolicy).not.toHaveBeenCalled();
  });

  it("does not show stale APT signing evidence after switching repositories", async () => {
    let resolveFirst: ((value: unknown) => void) | undefined;
    const first = new Promise((resolve) => {
      resolveFirst = resolve;
    });
    mockGetAptRepositorySigningState
      .mockReturnValueOnce(first as never)
      .mockResolvedValueOnce({
        data: {
          repositoryId: "33333333-3333-4333-8333-333333333333",
          signerMode: "remote",
          readiness: "ready",
          trustedFingerprints: ["b".repeat(40)],
          currentKeyRole: "active",
        },
      } as never);

    const view = render(
      <TestQueryProvider>
        <PreferencesProvider>
          <RepositorySecurityTab
            repo={{ ...repo, format: "apt" }}
            publicationScanning={false}
          />
        </PreferencesProvider>
      </TestQueryProvider>,
    );
    view.rerender(
      <TestQueryProvider>
        <PreferencesProvider>
          <RepositorySecurityTab
            repo={{
              ...repo,
              id: "33333333-3333-4333-8333-333333333333",
              name: "apt-next",
              format: "apt",
            }}
            publicationScanning={false}
          />
        </PreferencesProvider>
      </TestQueryProvider>,
    );

    expect(
      await screen.findByText("Production trust pinned"),
    ).toBeInTheDocument();
    resolveFirst?.({
      data: {
        repositoryId: repo.id,
        signerMode: "reference",
        readiness: "fixture",
        trustedFingerprints: [],
      },
    });
    await Promise.resolve();
    expect(screen.queryByText("Reference signer only")).not.toBeInTheDocument();
    expect(screen.getByText("Production trust pinned")).toBeInTheDocument();
  });

  it("explains an unavailable APT signing endpoint during a rolling upgrade", async () => {
    mockGetAptRepositorySigningState.mockResolvedValue({
      error: {
        status: 404,
        code: "not_found",
        message: "repository not found",
      },
    } as never);

    render(
      <TestQueryProvider>
        <PreferencesProvider>
          <RepositorySecurityTab
            repo={{ ...repo, format: "apt" }}
            publicationScanning={false}
          />
        </PreferencesProvider>
      </TestQueryProvider>,
    );

    expect(
      await screen.findByText("APT signing state is unavailable"),
    ).toBeInTheDocument();
    expect(
      screen.getByText(/current backend does not expose/),
    ).toBeInTheDocument();
    expect(screen.queryByText("Request failed")).not.toBeInTheDocument();
  });

  it("loads and saves a versioned security admission policy", async () => {
    const user = userEvent.setup();
    mockGetSecurityPolicy.mockResolvedValue({
      data: {
        version: "3",
        enabled: false,
        autoScanOnPublish: false,
        requireSignature: false,
        requireVerifiedSignature: false,
        requireSbom: true,
        requireProvenance: false,
        requireVulnerabilityScan: true,
        maxAllowedSeverity: "high",
        failOnScanError: true,
        allowedLicenses: ["MIT"],
      },
    } as never);
    mockReplaceSecurityPolicy.mockResolvedValue({
      data: {
        version: "4",
        enabled: true,
        autoScanOnPublish: true,
        requireSignature: false,
        requireVerifiedSignature: false,
        requireSbom: true,
        requireProvenance: false,
        requireVulnerabilityScan: true,
        maxAllowedSeverity: "high",
        failOnScanError: true,
        allowedLicenses: ["MIT"],
      },
    } as never);

    render(
      <TestQueryProvider>
        <PreferencesProvider>
          <RepositorySecurityTab repo={repo} publicationScanning />
        </PreferencesProvider>
      </TestQueryProvider>,
    );

    expect(await screen.findByText("Current version 3")).toBeInTheDocument();
    const saveButton = screen.getByRole("button", { name: "Save policy" });
    expect(saveButton).toBeDisabled();
    await user.click(
      screen.getByRole("switch", { name: "Scan after publication" }),
    );
    await user.click(
      screen.getByRole("switch", { name: "Enforce admission checks" }),
    );
    expect(screen.getByText("Unsaved changes")).toBeInTheDocument();
    expect(saveButton).toBeEnabled();
    await user.click(saveButton);

    await waitFor(() => expect(mockReplaceSecurityPolicy).toHaveBeenCalled());
    expect(mockReplaceSecurityPolicy).toHaveBeenCalledWith(
      expect.objectContaining({
        path: { repositoryId: repo.id },
        headers: { "If-Match": "3" },
        body: expect.objectContaining({
          version: "3",
          enabled: true,
          autoScanOnPublish: true,
          requireSbom: true,
          maxAllowedSeverity: "high",
          allowedLicenses: ["MIT"],
        }),
      }),
    );
    expect(await screen.findByText("Current version 4")).toBeInTheDocument();
    expect(
      screen.getByText("Repository security policy saved"),
    ).toBeInTheDocument();
    expect(saveButton).toBeDisabled();
  });

  it("shows automatic scanning as unavailable when no scanner is configured", async () => {
    mockGetSecurityPolicy.mockResolvedValue({
      data: { version: "1", autoScanOnPublish: false },
    } as never);

    render(
      <TestQueryProvider>
        <PreferencesProvider>
          <RepositorySecurityTab repo={repo} publicationScanning={false} />
        </PreferencesProvider>
      </TestQueryProvider>,
    );

    expect(
      await screen.findByRole("switch", { name: "Scan after publication" }),
    ).toBeDisabled();
    expect(screen.getByText("No scanner configured")).toBeInTheDocument();
  });

  it("loads and saves the independent quarantine read policy", async () => {
    const user = userEvent.setup();
    mockGetSecurityPolicy.mockResolvedValue({
      data: { version: "1", enabled: false },
    } as never);
    mockGetQuarantineReadPolicy.mockResolvedValue({
      data: { version: "7", enabled: false },
    } as never);
    mockReplaceQuarantineReadPolicy.mockResolvedValue({
      data: { version: "8", enabled: true },
    } as never);

    render(
      <TestQueryProvider>
        <PreferencesProvider>
          <RepositorySecurityTab repo={repo} publicationScanning />
        </PreferencesProvider>
      </TestQueryProvider>,
    );

    const readSwitch = await screen.findByRole("switch", {
      name: "Block quarantined artifact reads",
    });
    const readSaveButton = screen.getByRole("button", {
      name: "Save read policy",
    });
    expect(readSaveButton).toBeDisabled();
    await user.click(readSwitch);
    expect(screen.getByText("Unsaved changes")).toBeInTheDocument();
    expect(readSaveButton).toBeEnabled();
    await user.click(readSaveButton);

    await waitFor(() =>
      expect(mockReplaceQuarantineReadPolicy).toHaveBeenCalledWith({
        path: { repositoryId: repo.id },
        headers: { "If-Match": "7" },
        body: { version: "7", enabled: true },
      }),
    );
    expect(
      await screen.findByText("Read policy version 8"),
    ).toBeInTheDocument();
    expect(
      screen.getByText("Quarantine read policy saved"),
    ).toBeInTheDocument();
    expect(readSaveButton).toBeDisabled();
  });

  it("names the Go module version scope instead of the generic fallback", async () => {
    mockGetSecurityPolicy.mockResolvedValue({
      data: { version: "1", enabled: false },
    } as never);

    render(
      <TestQueryProvider>
        <PreferencesProvider>
          <RepositorySecurityTab
            repo={{ ...repo, format: "go", name: "go-releases" }}
            publicationScanning={false}
          />
        </PreferencesProvider>
      </TestQueryProvider>,
    );

    expect(
      await screen.findByText("Every module@version representation"),
    ).toBeInTheDocument();
    expect(
      screen.getByText("Hidden from @v/list and @latest"),
    ).toBeInTheDocument();
    expect(screen.queryByText("Artifact identity")).not.toBeInTheDocument();
  });

  it("keeps the security policy usable when the read policy request fails", async () => {
    mockGetSecurityPolicy.mockResolvedValue({
      data: { version: "3", enabled: false },
    } as never);
    mockGetQuarantineReadPolicy.mockResolvedValue({
      error: {
        code: "internal_error",
        message: "Failed to read policy",
        status: 500,
      },
    } as never);

    render(
      <TestQueryProvider>
        <PreferencesProvider>
          <RepositorySecurityTab repo={repo} publicationScanning />
        </PreferencesProvider>
      </TestQueryProvider>,
    );

    expect(
      await screen.findByText("Failed to read policy"),
    ).toBeInTheDocument();
    expect(screen.getByText("Current version 3")).toBeInTheDocument();
    expect(
      screen.getByRole("button", { name: "Save policy" }),
    ).toBeInTheDocument();
    expect(
      screen.queryByRole("button", { name: "Save read policy" }),
    ).not.toBeInTheDocument();
    expect(screen.getByText("Unavailable")).toBeInTheDocument();
    expect(screen.queryByText("Unsaved changes")).not.toBeInTheDocument();
  });
});
