import { cleanup, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, expect, it, vi } from "vitest";
import {
  createIdentityProvider,
  listIdentityProviders,
  replaceIdentityProvider,
  testIdentityProvider,
  type IdentityProvider,
} from "../../client";
import { PreferencesProvider } from "../../lib/preferences";
import { TestQueryProvider } from "../../test/queryClient";
import { AuthenticationPage } from "./Authentication";

vi.mock("../../client", async (importOriginal) => ({
  ...(await importOriginal<typeof import("../../client")>()),
  createIdentityProvider: vi.fn(),
  listIdentityProviders: vi.fn(),
  replaceIdentityProvider: vi.fn(),
  testIdentityProvider: vi.fn(),
}));

const saved: IdentityProvider = {
  id: "00000000-0000-0000-0000-000000000301",
  slug: "default",
  kind: "oidc",
  displayName: "Single sign-on",
  enabled: true,
  version: "4",
  attributeMapping: {},
  provisioningMode: "jit",
  emailLinkingEnabled: false,
  jitDefaultRole: "none",
  createdAt: "2026-10-01T08:00:00Z",
  updatedAt: "2026-10-12T08:00:00Z",
  oidc: {
    issuer: "https://id.example.com/realms/artifacts",
    audience: "notarx-api",
    clientId: "notarx-console",
    clientSecretConfigured: true,
    redirectUrl: "https://gateway.example.com/auth/oidc/default/callback",
    scopes: ["openid", "profile"],
    adminSubjects: [],
    memberRoles: ["artifact-members"],
    adminRoles: ["artifact-admins"],
    algorithms: ["RS256"],
  },
};
const listOf = (...items: IdentityProvider[]) => ({ data: { items } }) as never;

function renderPage() {
  render(
    <TestQueryProvider>
      <PreferencesProvider>
        <AuthenticationPage />
      </PreferencesProvider>
    </TestQueryProvider>,
  );
}

afterEach(() => {
  cleanup();
  vi.clearAllMocks();
});

it("saves edits against the loaded version and restarts the form on the new one", async () => {
  const user = userEvent.setup();
  vi.mocked(listIdentityProviders).mockResolvedValue(listOf(saved));
  vi.mocked(replaceIdentityProvider).mockResolvedValue({
    data: {
      ...saved,
      version: "5",
      oidc: { ...saved.oidc!, audience: "gateway-api-v2" },
    },
  } as never);
  renderPage();

  const audience = await screen.findByDisplayValue("notarx-api");
  await user.clear(audience);
  await user.type(audience, "gateway-api-v2");
  await user.click(screen.getByRole("button", { name: /Remove secret/ }));
  await user.click(screen.getByRole("button", { name: /Mark for removal/ }));
  expect(screen.getByText("Pending save")).toBeInTheDocument();
  await user.click(screen.getByRole("button", { name: /Save and apply/ }));

  await waitFor(() =>
    expect(replaceIdentityProvider).toHaveBeenCalledWith({
      path: { providerId: saved.id },
      body: expect.objectContaining({
        displayName: "Single sign-on",
        oidc: expect.objectContaining({
          audience: "gateway-api-v2",
          clearClientSecret: true,
        }),
      }),
      headers: { "If-Match": "4" },
    }),
  );
  expect(
    await screen.findByText("Settings saved and active on this node"),
  ).toBeInTheDocument();
  expect(screen.getByDisplayValue("gateway-api-v2")).toBeInTheDocument();
  // The new version starts clean: no pending removal and nothing to save.
  expect(screen.queryByText("Pending save")).not.toBeInTheDocument();
  expect(
    screen.getByRole("button", { name: /Settings synchronized/ }),
  ).toBeDisabled();
});

it("rejects an enabled provider without an issuer before saving", async () => {
  const user = userEvent.setup();
  vi.mocked(listIdentityProviders).mockResolvedValue(listOf(saved));
  renderPage();

  const issuer = await screen.findByDisplayValue(saved.oidc!.issuer);
  await user.clear(issuer);
  await user.click(screen.getByRole("button", { name: /Save and apply/ }));

  expect(
    await screen.findByText("Required when OIDC is enabled"),
  ).toBeInTheDocument();
  expect(replaceIdentityProvider).not.toHaveBeenCalled();
});

it("creates the default provider on the first save when none exists", async () => {
  const user = userEvent.setup();
  vi.mocked(listIdentityProviders).mockResolvedValue(listOf());
  vi.mocked(createIdentityProvider).mockResolvedValue({
    data: { ...saved, version: "1" },
  } as never);
  renderPage();

  const issuer = await screen.findByPlaceholderText(
    "https://id.example.com/realms/artifacts",
  );
  await user.type(issuer, "https://id.example.com/realms/artifacts");
  await user.click(screen.getByRole("button", { name: /Save and apply/ }));

  await waitFor(() =>
    expect(createIdentityProvider).toHaveBeenCalledWith({
      body: expect.objectContaining({
        slug: "default",
        kind: "oidc",
        enabled: false,
        oidc: expect.objectContaining({
          issuer: "https://id.example.com/realms/artifacts",
        }),
      }),
    }),
  );
  expect(replaceIdentityProvider).not.toHaveBeenCalled();
});

it("edits the default provider when several OIDC providers exist", async () => {
  const user = userEvent.setup();
  const other: IdentityProvider = {
    ...saved,
    id: "00000000-0000-0000-0000-000000000302",
    slug: "aaa-first",
    oidc: { ...saved.oidc!, audience: "other-audience" },
  };
  vi.mocked(listIdentityProviders).mockResolvedValue(listOf(other, saved));
  vi.mocked(testIdentityProvider).mockResolvedValue({
    data: {
      reachable: true,
      issuer: saved.oidc!.issuer,
      latencyMs: 12,
      checkedAt: "2026-10-12T08:00:00Z",
    },
  } as never);
  renderPage();

  expect(await screen.findByDisplayValue("notarx-api")).toBeInTheDocument();
  expect(screen.queryByDisplayValue("other-audience")).not.toBeInTheDocument();
  await user.click(screen.getByRole("button", { name: /Test saved settings/ }));
  await waitFor(() =>
    expect(testIdentityProvider).toHaveBeenCalledWith({
      path: { providerId: saved.id },
    }),
  );
});
