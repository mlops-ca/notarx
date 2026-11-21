import {
  cleanup,
  render,
  screen,
  waitFor,
  within,
} from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import {
  createScimToken,
  listScimTokens,
  revokeScimToken,
  type IdentityProvider,
  type ScimToken,
} from "../../client";
import { AntdProvider } from "../../app/AntdProvider";
import { PreferencesProvider } from "../../lib/preferences";
import {
  SCIM_TOKEN_WARNING_DAYS,
  ScimTokensCard,
  daysUntilExpiry,
  expiringTokens,
} from "./ScimTokens";

vi.mock("../../client", async (importOriginal) => ({
  ...(await importOriginal<typeof import("../../client")>()),
  listScimTokens: vi.fn(),
  createScimToken: vi.fn(),
  revokeScimToken: vi.fn(),
}));

const DAY = 24 * 60 * 60 * 1000;
const PROVIDER_ID = "00000000-0000-4000-8000-000000000501";
const PLAINTEXT = "nxscim_SECRET_PLAINTEXT_TOKEN_VALUE_0123456789";

function provider(overrides: Partial<IdentityProvider> = {}): IdentityProvider {
  return {
    id: PROVIDER_ID,
    slug: "entra",
    kind: "oidc",
    displayName: "Entra ID",
    enabled: true,
    version: "1",
    attributeMapping: {},
    provisioningMode: "scim_only",
    emailLinkingEnabled: false,
    jitDefaultRole: "none",
    createdAt: "2026-10-01T08:00:00Z",
    updatedAt: "2026-10-01T08:00:00Z",
    ...overrides,
  };
}

function token(
  name: string,
  daysLeft: number,
  state: ScimToken["state"] = "active",
): ScimToken {
  return {
    id:
      "00000000-0000-4000-8000-0000000005" +
      name.length.toString().padStart(2, "0"),
    providerId: PROVIDER_ID,
    name,
    state,
    createdAt: "2026-10-01T08:00:00Z",
    expiresAt: new Date(Date.now() + daysLeft * DAY).toISOString(),
    ...(state === "revoked" ? { revokedAt: "2026-10-02T08:00:00Z" } : {}),
  };
}

let client: QueryClient;

function renderCard(providers: IdentityProvider[], tokens: ScimToken[]) {
  vi.mocked(listScimTokens).mockResolvedValue({
    data: { items: tokens },
  } as never);
  return render(
    <QueryClientProvider client={client}>
      <PreferencesProvider>
        <AntdProvider>
          <ScimTokensCard providers={providers} />
        </AntdProvider>
      </PreferencesProvider>
    </QueryClientProvider>,
  );
}

beforeEach(() => {
  client = new QueryClient({
    defaultOptions: {
      queries: { retry: false, staleTime: 0, gcTime: Infinity },
      mutations: { retry: false },
    },
  });
});

afterEach(() => {
  cleanup();
  vi.clearAllMocks();
  window.localStorage.clear();
  window.sessionStorage.clear();
});

describe("expiry arithmetic", () => {
  it("counts whole days until a token expires", () => {
    const now = Date.parse("2026-11-19T00:00:00Z");
    expect(daysUntilExpiry("2026-11-29T00:00:00Z", now)).toBe(10);
    expect(daysUntilExpiry("2026-11-18T00:00:00Z", now)).toBe(-1);
  });

  it("flags active tokens inside the warning window, soonest first, and never revoked ones", () => {
    const flagged = expiringTokens([
      token("far", 200),
      token("soon", SCIM_TOKEN_WARNING_DAYS - 1),
      token("expired", -3, "expired"),
      token("gone", 2, "revoked"),
    ]);
    expect(flagged.map((entry) => entry.name)).toEqual(["expired", "soon"]);
  });
});

describe("ScimTokensCard", () => {
  it("renders nothing without identity providers", async () => {
    renderCard([], []);
    expect(screen.queryByText("SCIM tokens")).toBeNull();
  });

  it("lists tokens with their state and warns before one expires", async () => {
    renderCard(
      [provider()],
      [
        token("okta-sync", 400),
        token("entra-sync", 9),
        token("old", 1, "revoked"),
      ],
    );
    expect(await screen.findByText("entra-sync")).toBeInTheDocument();
    expect(
      screen.getByText(/The SCIM token entra-sync expires in 9 days/),
    ).toBeInTheDocument();
    expect(screen.queryByText(/The SCIM token okta-sync expires/)).toBeNull();
    expect(screen.queryByText(/The SCIM token old expires/)).toBeNull();
    // Only a token that still authenticates can be revoked.
    expect(
      screen.getByRole("button", { name: "Revoke token entra-sync" }),
    ).toBeInTheDocument();
    expect(
      screen.queryByRole("button", { name: "Revoke token old" }),
    ).toBeNull();
  });

  it("reports an expired token", async () => {
    renderCard([provider()], [token("lapsed", -2, "expired")]);
    expect(
      await screen.findByText(/The SCIM token lapsed expired on/),
    ).toBeInTheDocument();
  });

  it("reports a token that lapsed within the last day as expired, not as expiring in 0 days", async () => {
    renderCard([provider()], [token("hours-ago", -5 / 24)]);
    expect(
      await screen.findByText(/The SCIM token hours-ago expired on/),
    ).toBeInTheDocument();
    expect(screen.queryByText(/expires in 0 days/)).toBeNull();
  });

  it("shows an empty state for a provider without tokens", async () => {
    renderCard([provider()], []);
    expect(
      await screen.findByText("No SCIM tokens for this provider."),
    ).toBeInTheDocument();
  });

  it("reveals the plaintext once and keeps it out of the query cache and storage", async () => {
    const user = userEvent.setup();
    renderCard([provider()], []);
    vi.mocked(createScimToken).mockResolvedValueOnce({
      data: { ...token("entra-provisioning", 90), token: PLAINTEXT },
    } as never);

    await user.click(await screen.findByRole("button", { name: /New token/ }));
    const form = await screen.findByRole("dialog");
    await user.type(
      within(form).getByPlaceholderText("entra-provisioning"),
      " entra-provisioning ",
    );
    await user.click(within(form).getByRole("button", { name: /Create/ }));

    expect(await screen.findByText(PLAINTEXT)).toBeInTheDocument();
    expect(screen.getByText("Copy the token now")).toBeInTheDocument();
    const request = vi.mocked(createScimToken).mock.calls[0]?.[0] as {
      path: { providerId: string };
      body: { name: string; expiresAt: string };
    };
    expect(request.path.providerId).toBe(PROVIDER_ID);
    expect(request.body.name).toBe("entra-provisioning");
    const lifetime = Date.parse(request.body.expiresAt) - Date.now();
    expect(lifetime).toBeGreaterThan(89 * DAY);
    expect(lifetime).toBeLessThanOrEqual(90 * DAY);
    // The list is refreshed after the create, and it never carries a secret.
    await waitFor(() => expect(listScimTokens).toHaveBeenCalledTimes(2));

    await user.click(screen.getByRole("button", { name: "Done" }));
    await waitFor(() => expect(screen.queryByText(PLAINTEXT)).toBeNull());
    expect(document.body.innerHTML).not.toContain(PLAINTEXT);
    const cached = JSON.stringify(
      client
        .getQueryCache()
        .getAll()
        .map((query) => query.state.data),
    );
    expect(cached).not.toContain(PLAINTEXT);
    expect(client.getMutationCache().getAll()).toHaveLength(0);
    expect(JSON.stringify({ ...window.localStorage })).not.toContain(PLAINTEXT);
    expect(JSON.stringify({ ...window.sessionStorage })).not.toContain(
      PLAINTEXT,
    );
  });

  it("offers at most a 364 day lifetime, below the server's 365 day cap", async () => {
    const user = userEvent.setup();
    renderCard([provider()], []);
    vi.mocked(createScimToken).mockResolvedValueOnce({
      data: { ...token("yearly", 364), token: PLAINTEXT },
    } as never);
    await user.click(await screen.findByRole("button", { name: /New token/ }));
    const form = await screen.findByRole("dialog");
    await user.type(
      within(form).getByPlaceholderText("entra-provisioning"),
      "yearly",
    );
    await user.click(within(form).getByRole("combobox"));
    await user.click(await screen.findByText("1 year"));
    await user.click(within(form).getByRole("button", { name: /Create/ }));
    await screen.findByText(PLAINTEXT);
    const request = vi.mocked(createScimToken).mock.calls[0]?.[0] as {
      body: { expiresAt: string };
    };
    const lifetime = Date.parse(request.body.expiresAt) - Date.now();
    expect(lifetime).toBeGreaterThan(363 * DAY);
    expect(lifetime).toBeLessThan(365 * DAY);
  });

  it("keeps the dialog open with the error when the create fails and never reveals anything", async () => {
    const user = userEvent.setup();
    renderCard([provider()], []);
    vi.mocked(createScimToken).mockRejectedValueOnce(new Error("network down"));
    await user.click(await screen.findByRole("button", { name: /New token/ }));
    const form = await screen.findByRole("dialog");
    await user.type(
      within(form).getByPlaceholderText("entra-provisioning"),
      "t",
    );
    await user.click(within(form).getByRole("button", { name: /Create/ }));
    expect(await within(form).findByText("network down")).toBeInTheDocument();
    expect(screen.queryByText("Copy the token now")).toBeNull();
  });

  it("revokes after confirmation and refreshes the list", async () => {
    const user = userEvent.setup();
    renderCard([provider()], [token("entra-sync", 60)]);
    vi.mocked(revokeScimToken).mockResolvedValueOnce({
      data: token("entra-sync", 60, "revoked"),
    } as never);
    await user.click(
      await screen.findByRole("button", { name: "Revoke token entra-sync" }),
    );
    const dialog = await screen.findByRole("dialog");
    expect(
      within(dialog).getByText(/can no longer provision with entra-sync/),
    ).toBeInTheDocument();
    await user.click(within(dialog).getByRole("button", { name: /^Revoke$/ }));
    await waitFor(() =>
      expect(revokeScimToken).toHaveBeenCalledWith({
        path: { providerId: PROVIDER_ID, tokenId: token("entra-sync", 60).id },
      }),
    );
    await waitFor(() => expect(listScimTokens).toHaveBeenCalledTimes(2));
  });

  it("shows the error and frees the dialog when a revoke is rejected", async () => {
    const user = userEvent.setup();
    renderCard([provider()], [token("entra-sync", 60)]);
    vi.mocked(revokeScimToken).mockRejectedValueOnce(
      new Error("revoke failed"),
    );
    await user.click(
      await screen.findByRole("button", { name: "Revoke token entra-sync" }),
    );
    const dialog = await screen.findByRole("dialog");
    await user.click(within(dialog).getByRole("button", { name: /^Revoke$/ }));
    expect(await screen.findByText("revoke failed")).toBeInTheDocument();
  });
});
