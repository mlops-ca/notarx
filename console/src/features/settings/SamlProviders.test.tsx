import { cleanup, render, screen } from "@testing-library/react";
import { afterEach, expect, it, vi } from "vitest";
import { listIdentityProviders, type IdentityProvider } from "../../client";
import { PreferencesProvider } from "../../lib/preferences";
import { TestQueryProvider } from "../../test/queryClient";
import { SamlProvidersCard, daysUntil } from "./SamlProviders";

vi.mock("../../client", async (importOriginal) => ({
  ...(await importOriginal<typeof import("../../client")>()),
  listIdentityProviders: vi.fn(),
}));

const DAY = 24 * 60 * 60 * 1000;

function samlProvider(notAfters: string[], state = "active"): IdentityProvider {
  return {
    id: "00000000-0000-0000-0000-000000000401",
    slug: "corp",
    kind: "saml",
    displayName: "Corp SAML",
    enabled: true,
    version: "2",
    attributeMapping: {},
    provisioningMode: "jit",
    emailLinkingEnabled: false,
    jitDefaultRole: "none",
    createdAt: "2026-10-01T08:00:00Z",
    updatedAt: "2026-10-12T08:00:00Z",
    saml: {
      entityId: "https://idp.example.test/m",
      ssoUrl: "https://idp.example.test/sso",
      certificates: notAfters.map((notAfter, index) => ({
        pem: "pem-" + index,
        notAfter,
        state: index === 0 ? state : "next",
      })) as never,
      nameIdFormat: "persistent",
      allowIdpInitiated: false,
      relayStateAllowlist: [],
      signRequests: true,
      metadataRefresh: false,
      spEntityId: "https://notarx.example.test/auth/saml/corp/metadata",
      acsUrl: "https://notarx.example.test/auth/saml/corp/acs",
      metadataEndpoint: "https://notarx.example.test/auth/saml/corp/metadata",
      spKeys: [{ id: "k1", notAfter: "2031-01-01T00:00:00Z" }] as never,
    },
  };
}

function renderCard(...providers: IdentityProvider[]) {
  vi.mocked(listIdentityProviders).mockResolvedValue({
    data: { items: providers },
  } as never);
  render(
    <TestQueryProvider>
      <PreferencesProvider>
        <SamlProvidersCard />
      </PreferencesProvider>
    </TestQueryProvider>,
  );
}

afterEach(() => {
  cleanup();
  vi.clearAllMocks();
});

it("counts whole days until a certificate expires", () => {
  const now = Date.parse("2026-11-19T00:00:00Z");
  expect(daysUntil("2026-11-29T00:00:00Z", now)).toBe(10);
  expect(daysUntil("2026-11-18T00:00:00Z", now)).toBe(-1);
});

it("warns when a pinned certificate expires within 30 days", async () => {
  renderCard(
    samlProvider([
      new Date(Date.now() + 10 * DAY).toISOString(),
      new Date(Date.now() + 400 * DAY).toISOString(),
    ]),
  );
  expect(await screen.findByText(/Corp SAML expires in/i)).toBeTruthy();
  expect(screen.getAllByText(/Expires /)).toHaveLength(2);
  expect(screen.getByText("Next (rollover)", { exact: false })).toBeTruthy();
  expect(screen.getByText(/auth\/saml\/corp\/acs/)).toBeTruthy();
});

it("reports an expired certificate and stays quiet when all are far from expiry", async () => {
  renderCard(samlProvider([new Date(Date.now() - 2 * DAY).toISOString()]));
  expect(await screen.findByText(/Corp SAML expired on/i)).toBeTruthy();
  cleanup();
  renderCard(samlProvider([new Date(Date.now() + 200 * DAY).toISOString()]));
  expect(await screen.findByText("Corp SAML")).toBeTruthy();
  expect(screen.queryByText(/expires in/i)).toBeNull();
});

it("renders nothing without SAML providers", async () => {
  renderCard({ ...samlProvider([]), kind: "oidc" });
  await vi.waitFor(() => expect(listIdentityProviders).toHaveBeenCalled());
  expect(screen.queryByText("SAML providers")).toBeNull();
});
