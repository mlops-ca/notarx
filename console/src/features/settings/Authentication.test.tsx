import { cleanup, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, expect, it, vi } from "vitest";
import {
  getOidcSettings,
  replaceOidcSettings,
  type OidcSettings,
} from "../../client";
import { PreferencesProvider } from "../../lib/preferences";
import { TestQueryProvider } from "../../test/queryClient";
import { AuthenticationPage } from "./Authentication";

vi.mock("../../client", async (importOriginal) => ({
  ...(await importOriginal<typeof import("../../client")>()),
  getOidcSettings: vi.fn(),
  replaceOidcSettings: vi.fn(),
  testOidcSettings: vi.fn(),
}));

const saved: OidcSettings = {
  version: "4",
  source: "database",
  enabled: true,
  issuer: "https://id.example.com/realms/artifacts",
  audience: "artifact-gateway-api",
  clientId: "artifact-gateway-console",
  clientSecretConfigured: true,
  redirectUrl: "https://gateway.example.com/auth/oidc/callback",
  scopes: ["openid", "profile"],
  adminSubjects: [],
  memberRoles: ["artifact-members"],
  adminRoles: ["artifact-admins"],
  provisioningMode: "jit",
  emailLinkingEnabled: false,
  jitDefaultRole: "none",
  updatedAt: "2026-10-12T08:00:00Z",
} as OidcSettings;

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
  vi.mocked(getOidcSettings).mockResolvedValue({ data: saved } as never);
  vi.mocked(replaceOidcSettings).mockResolvedValue({
    data: { ...saved, version: "5", audience: "gateway-api-v2" },
  } as never);
  renderPage();

  const audience = await screen.findByDisplayValue("artifact-gateway-api");
  await user.clear(audience);
  await user.type(audience, "gateway-api-v2");
  await user.click(screen.getByRole("button", { name: /Remove secret/ }));
  await user.click(screen.getByRole("button", { name: /Mark for removal/ }));
  expect(screen.getByText("Pending save")).toBeInTheDocument();
  await user.click(screen.getByRole("button", { name: /Save and apply/ }));

  await waitFor(() =>
    expect(replaceOidcSettings).toHaveBeenCalledWith({
      body: expect.objectContaining({
        audience: "gateway-api-v2",
        clearClientSecret: true,
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
  vi.mocked(getOidcSettings).mockResolvedValue({ data: saved } as never);
  renderPage();

  const issuer = await screen.findByDisplayValue(saved.issuer);
  await user.clear(issuer);
  await user.click(screen.getByRole("button", { name: /Save and apply/ }));

  expect(
    await screen.findByText("Required when OIDC is enabled"),
  ).toBeInTheDocument();
  expect(replaceOidcSettings).not.toHaveBeenCalled();
});
