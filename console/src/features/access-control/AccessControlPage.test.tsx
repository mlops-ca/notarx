import {
  cleanup,
  render,
  screen,
  waitFor,
  within,
} from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { getLabelledButton } from "../../test/buttons";
import { afterEach, describe, expect, it, vi } from "vitest";
import { MemoryRouter } from "react-router-dom";
import {
  getAnonymousAccessPolicy,
  getRepositoryEffectiveAccess,
  listApiKeys,
  listRepositories,
  listRepositoryGrants,
  listServiceAccounts,
  listUsers,
  replaceAnonymousAccessPolicy,
} from "../../client";
import { AntdProvider } from "../../app/AntdProvider";
import { PreferencesProvider } from "../../lib/preferences";
import { TestQueryProvider } from "../../test/queryClient";
import { AccessControlPage } from "./AccessControl";

vi.mock("../../client", async () => {
  const actual =
    await vi.importActual<typeof import("../../client")>("../../client");
  return {
    ...actual,
    getAnonymousAccessPolicy: vi.fn(),
    getRepositoryEffectiveAccess: vi.fn(),
    listApiKeys: vi.fn(),
    listRepositories: vi.fn(),
    listRepositoryGrants: vi.fn(),
    listServiceAccounts: vi.fn(),
    listUsers: vi.fn(),
    replaceAnonymousAccessPolicy: vi.fn(),
  };
});

vi.mock("../../lib/auth", () => ({
  useAuth: () => ({
    identity: {
      actor: "user:operator",
      kind: "local",
      role: "admin",
      administrator: true,
    },
    identityLoading: false,
  }),
}));

const mockGetAnonymousAccessPolicy = vi.mocked(getAnonymousAccessPolicy);
const mockGetRepositoryEffectiveAccess = vi.mocked(
  getRepositoryEffectiveAccess,
);
const mockListApiKeys = vi.mocked(listApiKeys);
const mockListRepositories = vi.mocked(listRepositories);
const mockListRepositoryGrants = vi.mocked(listRepositoryGrants);
const mockListServiceAccounts = vi.mocked(listServiceAccounts);
const mockListUsers = vi.mocked(listUsers);
const mockReplaceAnonymousAccessPolicy = vi.mocked(
  replaceAnonymousAccessPolicy,
);

afterEach(() => {
  cleanup();
  vi.clearAllMocks();
});

describe("AccessControlPage", () => {
  it("keeps permission evaluation usable when service accounts are unavailable", async () => {
    mockListRepositoryGrants.mockResolvedValue({ data: [] } as never);
    mockGetAnonymousAccessPolicy.mockResolvedValue({
      data: { enabled: false, version: "1" },
    } as never);
    mockListRepositories.mockResolvedValue({
      data: {
        items: [
          {
            id: "00000000-0000-4000-8000-000000000001",
            name: "releases",
            format: "raw",
            type: "hosted",
            state: "active",
          },
        ],
      },
    } as never);
    mockListUsers.mockResolvedValue({
      data: {
        items: [{ name: "active-user", role: "member", state: "active" }],
      },
    } as never);
    mockListApiKeys.mockResolvedValue({ data: { items: [] } } as never);
    mockListServiceAccounts.mockResolvedValue({
      error: "404 page not found",
    } as never);
    mockGetRepositoryEffectiveAccess.mockResolvedValue({
      data: {
        actor: "user:active-user",
        resource: "",
        simulated: true,
        repository: {
          id: "00000000-0000-4000-8000-000000000001",
          name: "releases",
          format: "raw",
          type: "hosted",
          state: "active",
        },
        identity: {
          kind: "local_session",
          subject: "active-user",
          displayName: "active-user",
        },
        anonymousRead: {
          allowed: false,
          source: "repository",
          reason: "private",
        },
        permissions: {
          read: {
            allowed: true,
            source: "repository_grants",
            reason: "scope_granted",
          },
          write: { allowed: false, source: "none", reason: "not granted" },
          admin: { allowed: false, source: "none", reason: "not granted" },
          intelligence: {
            allowed: false,
            source: "none",
            reason: "not granted",
          },
        },
      },
    } as never);

    const user = userEvent.setup();
    render(
      <TestQueryProvider>
        <PreferencesProvider>
          <AntdProvider>
            <MemoryRouter initialEntries={["/access?tab=evaluate"]}>
              <AccessControlPage />
            </MemoryRouter>
          </AntdProvider>
        </PreferencesProvider>
      </TestQueryProvider>,
    );

    expect(
      await screen.findByText(
        "Service accounts are temporarily unavailable. You can still evaluate the current identity, users, API keys, and custom actors.",
      ),
    ).toBeVisible();

    const principalPicker = screen.getByRole("combobox", {
      name: "Principal",
    });
    await user.click(principalPicker);
    await user.click(screen.getByText(/User · active-user/));
    await user.click(screen.getByRole("button", { name: /Evaluate/ }));

    await waitFor(() =>
      expect(mockGetRepositoryEffectiveAccess).toHaveBeenCalledWith({
        // The evaluation runs as a query, so a superseded one is cancelled.
        signal: expect.any(AbortSignal),
        path: { repositoryId: "00000000-0000-4000-8000-000000000001" },
        query: {
          actor: "user:active-user",
          role: "member",
          resource: undefined,
        },
      }),
    );
    expect(await screen.findByText("Simulated")).toBeInTheDocument();
  });

  it("never shows an earlier evaluation after its inputs changed", async () => {
    mockListRepositoryGrants.mockResolvedValue({ data: [] } as never);
    mockGetAnonymousAccessPolicy.mockResolvedValue({
      data: { enabled: false, version: "1" },
    } as never);
    mockListRepositories.mockResolvedValue({
      data: {
        items: [
          {
            id: "00000000-0000-4000-8000-000000000001",
            name: "releases",
            format: "raw",
            type: "hosted",
            state: "active",
          },
        ],
      },
    } as never);
    mockListUsers.mockResolvedValue({
      data: {
        items: [{ name: "active-user", role: "member", state: "active" }],
      },
    } as never);
    mockListApiKeys.mockResolvedValue({ data: { items: [] } } as never);
    mockListServiceAccounts.mockResolvedValue({
      error: "404 page not found",
    } as never);
    const answer = (resource: string) => {
      const response = {
        data: {
          actor: "user:active-user",
          resource: "",
          simulated: true,
          repository: {
            id: "00000000-0000-4000-8000-000000000001",
            name: "releases",
            format: "raw",
            type: "hosted",
            state: "active",
          },
          identity: {
            kind: "local_session",
            subject: "active-user",
            displayName: "active-user",
          },
          anonymousRead: {
            allowed: false,
            source: "repository",
            reason: "private",
          },
          permissions: {
            read: {
              allowed: true,
              source: "repository_grants",
              reason: "scope_granted",
            },
            write: { allowed: false, source: "none", reason: "not granted" },
            admin: { allowed: false, source: "none", reason: "not granted" },
            intelligence: {
              allowed: false,
              source: "none",
              reason: "not granted",
            },
          },
        },
      } as { data: { resource: string } };
      return { data: { ...response.data, resource } } as never;
    };
    let finishFirst: (value: never) => void = () => {};
    mockGetRepositoryEffectiveAccess
      .mockImplementationOnce(
        () =>
          new Promise<never>((resolve) => {
            finishFirst = resolve;
          }),
      )
      .mockResolvedValue(answer("docs/"));

    const user = userEvent.setup();
    render(
      <TestQueryProvider>
        <PreferencesProvider>
          <AntdProvider>
            <MemoryRouter initialEntries={["/access?tab=evaluate"]}>
              <AccessControlPage />
            </MemoryRouter>
          </AntdProvider>
        </PreferencesProvider>
      </TestQueryProvider>,
    );

    expect(
      await screen.findByText(
        "Service accounts are temporarily unavailable. You can still evaluate the current identity, users, API keys, and custom actors.",
      ),
    ).toBeVisible();

    const principalPicker = screen.getByRole("combobox", {
      name: "Principal",
    });
    await user.click(principalPicker);
    await user.click(screen.getByText(/User · active-user/));
    await user.click(screen.getByRole("button", { name: /Evaluate/ }));

    await waitFor(() =>
      expect(mockGetRepositoryEffectiveAccess).toHaveBeenCalledTimes(1),
    );
    // Changing an input withdraws the evaluation in flight.
    await user.type(
      screen.getByPlaceholderText("releases/2026/app.tar.gz"),
      "docs/",
    );
    await user.click(screen.getByRole("button", { name: /Evaluate/ }));
    expect(await screen.findByText("docs/")).toBeInTheDocument();
    finishFirst(answer(""));
    await new Promise((resolve) => setTimeout(resolve, 20));
    expect(screen.getByText("docs/")).toBeInTheDocument();
    expect(screen.queryByText("Entire repository")).not.toBeInTheDocument();
  });

  it("omits disabled users and unusable API keys from the principal picker", async () => {
    mockListRepositoryGrants.mockResolvedValue({ data: [] } as never);
    mockGetAnonymousAccessPolicy.mockResolvedValue({
      data: { enabled: false, version: "1" },
    } as never);
    mockListRepositories.mockResolvedValue({
      data: {
        items: [
          {
            id: "00000000-0000-4000-8000-000000000001",
            name: "releases",
            format: "raw",
            type: "hosted",
            state: "active",
          },
        ],
      },
    } as never);
    mockListUsers.mockResolvedValue({
      data: {
        items: [
          { name: "active-user", role: "member", state: "active" },
          { name: "disabled-user", role: "member", state: "disabled" },
        ],
      },
    } as never);
    mockListApiKeys.mockResolvedValue({
      data: {
        items: [
          { id: "active-key", name: "Active key", roles: ["member"] },
          {
            id: "revoked-key",
            name: "Revoked key",
            roles: ["member"],
            revokedAt: "2026-08-12T00:00:00Z",
          },
          {
            id: "expired-key",
            name: "Expired key",
            roles: ["member"],
            expiresAt: "2000-01-01T00:00:00Z",
          },
        ],
      },
    } as never);
    mockListServiceAccounts.mockResolvedValue({
      data: {
        items: [
          {
            id: "active-service-account",
            name: "pipeone-ci",
            description: "CI publisher",
            state: "active",
            createdAt: "2026-08-18T00:00:00Z",
            updatedAt: "2026-08-18T00:00:00Z",
            version: "1",
          },
          {
            id: "disabled-service-account",
            name: "old-ci",
            description: "disabled",
            state: "disabled",
            createdAt: "2026-08-18T00:00:00Z",
            updatedAt: "2026-08-18T00:00:00Z",
            version: "1",
          },
        ],
      },
    } as never);

    const user = userEvent.setup();
    render(
      <TestQueryProvider>
        <PreferencesProvider>
          <AntdProvider>
            <MemoryRouter initialEntries={["/access?tab=evaluate"]}>
              <AccessControlPage />
            </MemoryRouter>
          </AntdProvider>
        </PreferencesProvider>
      </TestQueryProvider>,
    );

    const principalPicker = await screen.findByRole("combobox", {
      name: "Principal",
    });
    await user.click(principalPicker);

    expect(screen.getByText(/User · active-user/)).toBeInTheDocument();
    expect(screen.getByText(/API Key · Active key/)).toBeInTheDocument();
    expect(
      screen.getByText(/Service account · pipeone-ci/),
    ).toBeInTheDocument();
    expect(screen.getAllByText(/Current identity/)).not.toHaveLength(0);
    expect(screen.getByText(/OIDC \/ custom actor/)).toBeInTheDocument();
    expect(screen.queryByText(/disabled-user/)).not.toBeInTheDocument();
    expect(screen.queryByText(/Revoked key/)).not.toBeInTheDocument();
    expect(screen.queryByText(/Expired key/)).not.toBeInTheDocument();
    expect(screen.queryByText(/old-ci/)).not.toBeInTheDocument();

    await user.click(screen.getByText(/Service account · pipeone-ci/));
    mockGetRepositoryEffectiveAccess.mockResolvedValue({
      data: {
        actor: "service-account:active-service-account",
        resource: "",
        simulated: true,
        repository: {
          id: "00000000-0000-4000-8000-000000000001",
          name: "releases",
          format: "raw",
          type: "hosted",
          state: "active",
        },
        identity: {
          kind: "service_account_credential",
          subject: "active-service-account",
          displayName: "pipeone-ci",
        },
        anonymousRead: {
          allowed: false,
          source: "repository",
          reason: "private",
        },
        permissions: {
          read: { allowed: true, source: "grant", reason: "matched" },
          write: { allowed: true, source: "grant", reason: "matched" },
          admin: { allowed: false, source: "none", reason: "not granted" },
          intelligence: {
            allowed: false,
            source: "none",
            reason: "not granted",
          },
        },
      },
    } as never);
    await user.click(screen.getByRole("button", { name: /Evaluate/ }));
    await waitFor(() =>
      expect(mockGetRepositoryEffectiveAccess).toHaveBeenCalledWith({
        // The evaluation runs as a query, so a superseded one is cancelled.
        signal: expect.any(AbortSignal),
        path: { repositoryId: "00000000-0000-4000-8000-000000000001" },
        query: {
          actor: "service-account:active-service-account",
          role: undefined,
          resource: undefined,
        },
      }),
    );
    expect(await screen.findByText("Simulated")).toBeInTheDocument();
  });

  it.each(["updated", "update-error", "load-error"])(
    "keeps the compact anonymous boundary and policy state (%s)",
    async (outcome) => {
      const user = userEvent.setup();
      mockListRepositoryGrants.mockResolvedValue({ data: [] } as never);
      mockGetAnonymousAccessPolicy.mockResolvedValue(
        (outcome === "load-error"
          ? { error: { message: "synthetic policy unavailable", status: 503 } }
          : { data: { enabled: true, version: "7" } }) as never,
      );
      mockListRepositories.mockResolvedValue({
        data: {
          items: [
            {
              id: "00000000-0000-4000-8000-000000000001",
              name: "public-maven",
              format: "maven",
              type: "hosted",
              state: "active",
              anonymousRead: true,
            },
            {
              id: "00000000-0000-4000-8000-000000000002",
              name: "private-npm",
              format: "npm",
              type: "hosted",
              state: "active",
              anonymousRead: false,
            },
          ],
        },
      } as never);
      mockListUsers.mockResolvedValue({ data: { items: [] } } as never);
      mockListApiKeys.mockResolvedValue({ data: { items: [] } } as never);
      mockListServiceAccounts.mockResolvedValue({
        data: { items: [] },
      } as never);
      mockReplaceAnonymousAccessPolicy.mockResolvedValue(
        (outcome === "update-error"
          ? { error: { message: "synthetic policy conflict", status: 409 } }
          : { data: { enabled: false, version: "8" } }) as never,
      );

      render(
        <TestQueryProvider>
          <PreferencesProvider>
            <AntdProvider>
              <MemoryRouter initialEntries={["/access?tab=policies"]}>
                <AccessControlPage />
              </MemoryRouter>
            </AntdProvider>
          </PreferencesProvider>
        </TestQueryProvider>,
      );

      expect(
        await screen.findByText("Public access boundary"),
      ).toBeInTheDocument();
      expect(
        screen.getByText(
          "Anonymous reads require global, repository, and applicable group opt-in; writes, deletes, and management still require authentication.",
        ),
      ).toBeInTheDocument();
      expect(screen.queryByText("Group dual consent")).not.toBeInTheDocument();
      expect(
        await screen.findByText("1 / 2 repositories public"),
      ).toBeInTheDocument();
      if (outcome === "load-error") {
        expect(
          await screen.findByText("synthetic policy unavailable"),
        ).toBeInTheDocument();
        const card = within(
          screen
            .getByRole("heading", { name: "Public access boundary" })
            .closest(".ag-public-access-card")! as HTMLElement,
        );
        expect(card.queryByText("Loading…")).not.toBeInTheDocument();
        expect(card.queryByRole("switch")).not.toBeInTheDocument();
        expect(mockReplaceAnonymousAccessPolicy).not.toHaveBeenCalled();
        return;
      }
      expect(
        screen.getByRole("switch", { name: "Toggle global anonymous reads" }),
      ).toBeChecked();
      await user.click(
        screen.getByRole("switch", { name: "Toggle global anonymous reads" }),
      );
      await user.click(
        within(await screen.findByRole("tooltip")).getByRole("button", {
          name: /Continue/,
        }),
      );
      await waitFor(() =>
        expect(mockReplaceAnonymousAccessPolicy).toHaveBeenCalledWith({
          body: { enabled: false, version: "7" },
          headers: { "If-Match": "7" },
        }),
      );
      if (outcome === "update-error") {
        expect(
          await screen.findByText("synthetic policy conflict"),
        ).toBeInTheDocument();
        expect(
          screen.getByRole("switch", { name: "Toggle global anonymous reads" }),
        ).toBeChecked();
      } else {
        await waitFor(() =>
          expect(
            screen.getByRole("switch", {
              name: "Toggle global anonymous reads",
            }),
          ).not.toBeChecked(),
        );
      }
    },
  );

  it("renders and filters user, service-account, API key, and custom repository grants", async () => {
    const user = userEvent.setup();
    mockListRepositoryGrants.mockResolvedValue({
      data: [
        {
          repositoryId: "00000000-0000-4000-8000-000000000001",
          repositoryName: "release-repository",
          format: "maven",
          principal: "user:alice",
          scopes: ["repositories:read"],
        },
        {
          repositoryId: "00000000-0000-4000-8000-000000000001",
          repositoryName: "release-repository",
          format: "maven",
          principal: "service-account:11111111-1111-4111-8111-111111111111",
          scopes: ["repositories:write"],
          resourcePrefix: "org/example",
        },
        {
          repositoryId: "00000000-0000-4000-8000-000000000001",
          repositoryName: "release-repository",
          format: "maven",
          principal: "api-key:legacy-publisher",
          scopes: ["repositories:admin"],
        },
        {
          repositoryId: "00000000-0000-4000-8000-000000000002",
          repositoryName: "raw-assets",
          format: "raw",
          principal: "oidc:gitlab:release-team",
          scopes: ["repositories:intelligence"],
        },
      ],
    } as never);
    mockGetAnonymousAccessPolicy.mockResolvedValue({
      data: { enabled: false, version: "1" },
    } as never);
    mockListRepositories.mockResolvedValue({ data: { items: [] } } as never);
    mockListUsers.mockResolvedValue({ data: { items: [] } } as never);
    mockListApiKeys.mockResolvedValue({ data: { items: [] } } as never);
    mockListServiceAccounts.mockResolvedValue({ data: { items: [] } } as never);

    render(
      <TestQueryProvider>
        <PreferencesProvider>
          <AntdProvider>
            <MemoryRouter initialEntries={["/access"]}>
              <AccessControlPage />
            </MemoryRouter>
          </AntdProvider>
        </PreferencesProvider>
      </TestQueryProvider>,
    );

    expect(await screen.findByText("User · alice")).toBeInTheDocument();
    // Identifiers are shown whole under their kind, not cut to a prefix.
    expect(
      screen.getByText("11111111-1111-4111-8111-111111111111"),
    ).toBeInTheDocument();
    expect(screen.getByText("legacy-publisher")).toBeInTheDocument();
    expect(screen.getByText("OIDC / custom")).toBeInTheDocument();
    expect(screen.getByText("Artifact intelligence")).toBeInTheDocument();
    expect(screen.getByText("Elevated")).toBeInTheDocument();

    await user.type(
      screen.getByPlaceholderText("Username, API key, or actor"),
      "service-account",
    );
    expect(await screen.findByText("1 repository grants")).toBeInTheDocument();
    await user.click(getLabelledButton(/Clear/));
    expect(await screen.findByText("4 repository grants")).toBeInTheDocument();
    await user.type(
      screen.getByPlaceholderText("Repository name"),
      "raw-assets",
    );
    expect(await screen.findByText("1 repository grants")).toBeInTheDocument();
  });
});
