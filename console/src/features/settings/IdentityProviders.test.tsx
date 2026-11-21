import { MemoryRouter } from "react-router-dom";
import {
  cleanup,
  render,
  screen,
  waitFor,
  within,
} from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";
import {
  createIdentityProvider,
  deleteIdentityProvider,
  getSecuritySettings,
  importIdentityProviderMetadata,
  listAudits,
  listIdentityProviders,
  replaceIdentityProvider,
  rotateIdentityProviderSpKey,
  testIdentityProvider,
  type IdentityProvider,
} from "../../client";
import { AntdProvider } from "../../app/AntdProvider";
import { PreferencesProvider } from "../../lib/preferences";
import { TestQueryProvider } from "../../test/queryClient";
import { IdentityProvidersPage } from "./IdentityProviders";

vi.mock("../../client", async () => {
  const actual =
    await vi.importActual<typeof import("../../client")>("../../client");
  return {
    ...actual,
    createIdentityProvider: vi.fn(),
    deleteIdentityProvider: vi.fn(),
    getSecuritySettings: vi.fn(),
    importIdentityProviderMetadata: vi.fn(),
    listAudits: vi.fn(),
    listIdentityProviders: vi.fn(),
    replaceIdentityProvider: vi.fn(),
    rotateIdentityProviderSpKey: vi.fn(),
    testIdentityProvider: vi.fn(),
  };
});

const DAY = 24 * 60 * 60 * 1000;
const stamp = "2026-11-01T00:00:00Z";
const base = {
  enabled: true,
  version: "2",
  attributeMapping: {},
  provisioningMode: "jit" as const,
  emailLinkingEnabled: false,
  jitDefaultRole: "none" as const,
  createdAt: stamp,
  updatedAt: stamp,
};
const entra: IdentityProvider = {
  ...base,
  id: "00000000-0000-4000-8000-000000000001",
  slug: "entra",
  kind: "oidc",
  displayName: "Entra ID",
  oidc: {
    issuer: "https://login.microsoftonline.com/t/v2.0",
    audience: "client",
    clientId: "client",
    clientSecretConfigured: true,
    redirectUrl: "https://notarx.example.test/auth/oidc/entra/callback",
    scopes: ["openid"],
    adminSubjects: [],
    memberRoles: [],
    adminRoles: [],
    algorithms: ["RS256"],
  },
};
function saml(notAfter: string): IdentityProvider {
  return {
    ...base,
    id: "00000000-0000-4000-8000-000000000002",
    slug: "okta",
    kind: "saml",
    displayName: "Okta SAML",
    saml: {
      entityId: "https://idp.example.test/m",
      ssoUrl: "https://idp.example.test/sso",
      certificates: [{ pem: "pem-1", notAfter, state: "active" }],
      nameIdFormat: "persistent",
      allowIdpInitiated: false,
      relayStateAllowlist: [],
      signRequests: true,
      metadataRefresh: false,
      spEntityId: "https://notarx.example.test/auth/saml/okta/metadata",
      acsUrl: "https://notarx.example.test/auth/saml/okta/acs",
      metadataEndpoint: "https://notarx.example.test/auth/saml/okta/metadata",
      spKeys: [],
    },
  };
}

function mockReads(providers: IdentityProvider[], overage: object[] = []) {
  vi.mocked(listIdentityProviders).mockResolvedValue({
    data: { items: providers },
  } as never);
  vi.mocked(listAudits).mockResolvedValue({ data: overage } as never);
  vi.mocked(getSecuritySettings).mockResolvedValue({
    data: { authorizationCacheEnabled: true, authorizationCacheTtlSeconds: 5 },
  } as never);
}

function renderPage() {
  return render(
    <MemoryRouter>
      <TestQueryProvider>
        <PreferencesProvider>
          <AntdProvider>
            <IdentityProvidersPage />
          </AntdProvider>
        </PreferencesProvider>
      </TestQueryProvider>
    </MemoryRouter>,
  );
}

afterEach(() => {
  cleanup();
  vi.clearAllMocks();
});

async function rowOf(name: string) {
  const cell = await screen.findByText(name);
  return within(cell.closest("tr") as HTMLElement);
}

describe("Identity providers page", () => {
  it("lists providers of both protocols and states the Single Logout limit", async () => {
    mockReads([entra, saml(new Date(Date.now() + 400 * DAY).toISOString())]);
    renderPage();
    expect(
      (await rowOf("Entra ID")).getByText("OpenID Connect"),
    ).toBeInTheDocument();
    expect(
      (await rowOf("Okta SAML")).getByText("SAML 2.0"),
    ).toBeInTheDocument();
    expect(
      screen.getByText("Single Logout is not supported"),
    ).toBeInTheDocument();
  });

  it("shows the empty state", async () => {
    mockReads([]);
    renderPage();
    expect(
      await screen.findByText("No identity provider yet"),
    ).toBeInTheDocument();
  });

  it("warns 30 days before a SAML certificate expires, and after", async () => {
    mockReads([saml(new Date(Date.now() + 10 * DAY).toISOString())]);
    renderPage();
    expect(
      await screen.findByText(
        /signing certificate of Okta SAML expires in 10 days/,
      ),
    ).toBeInTheDocument();
    cleanup();
    mockReads([saml(new Date(Date.now() - 2 * DAY).toISOString())]);
    renderPage();
    expect(
      await screen.findByText(/signing certificate of Okta SAML expired on/),
    ).toBeInTheDocument();
  });

  it("does not warn about a certificate that is far from expiry", async () => {
    mockReads([saml(new Date(Date.now() + 90 * DAY).toISOString())]);
    renderPage();
    await rowOf("Okta SAML");
    expect(screen.queryByText(/signing certificate of Okta SAML/)).toBeNull();
  });

  it("warns about a recent groups overage recorded for the provider only", async () => {
    const recent = new Date(Date.now() - 2 * DAY).toISOString();
    mockReads(
      [entra],
      [
        {
          operation: "auth.oidc.groups_overage",
          resource: "auth/oidc/entra",
          occurredAt: recent,
          outcome: "success",
        },
        {
          operation: "auth.oidc.groups_overage",
          resource: "auth/oidc/other",
          occurredAt: recent,
          outcome: "success",
        },
      ],
    );
    renderPage();
    expect(
      await screen.findByText("Entra ID: too many groups for one sign-in"),
    ).toBeInTheDocument();
    expect(listAudits).toHaveBeenCalledWith(
      expect.objectContaining({
        query: expect.objectContaining({
          operation: "auth.oidc.groups_overage",
        }),
      }),
    );
    expect(listAudits).toHaveBeenCalledWith(
      expect.objectContaining({
        query: expect.objectContaining({
          operation: "auth.saml.groups_overage",
        }),
      }),
    );
  });

  it("warns about a SAML groups overage and drops one older than 30 days", async () => {
    const okta = saml(new Date(Date.now() + 400 * DAY).toISOString());
    mockReads(
      [entra, okta],
      [
        {
          operation: "auth.saml.groups_overage",
          resource: "auth/saml/okta",
          occurredAt: new Date(Date.now() - DAY).toISOString(),
          outcome: "success",
        },
        {
          operation: "auth.oidc.groups_overage",
          resource: "auth/oidc/entra",
          occurredAt: new Date(Date.now() - 45 * DAY).toISOString(),
          outcome: "success",
        },
      ],
    );
    renderPage();
    expect(
      await screen.findByText("Okta SAML: too many groups for one sign-in"),
    ).toBeInTheDocument();
    expect(
      screen.queryByText("Entra ID: too many groups for one sign-in"),
    ).toBeNull();
  });

  it("opens the wizard on a fresh form each time", async () => {
    mockReads([entra]);
    vi.mocked(createIdentityProvider).mockResolvedValue({
      data: entra,
    } as never);
    renderPage();
    await userEvent.click(
      await screen.findByRole("button", { name: /Add provider/ }),
    );
    await userEvent.click(
      within(screen.getByRole("dialog")).getByRole("button", { name: "Next" }),
    );
    await userEvent.type(
      within(screen.getByRole("dialog")).getAllByRole("textbox")[0],
      "Draft name",
    );
    await userEvent.click(
      within(screen.getByRole("dialog")).getByRole("button", {
        name: "Cancel",
      }),
    );
    await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
    await userEvent.click(screen.getByRole("button", { name: /Add provider/ }));
    const dialog = screen.getByRole("dialog");
    // Back on the first step, with nothing left over from the abandoned draft.
    expect(within(dialog).queryByRole("button", { name: "Back" })).toBeNull();
    expect(within(dialog).getAllByRole("radio")[0]).toBeChecked();
    expect(within(dialog).queryByDisplayValue("Draft name")).toBeNull();
  });

  it("shows why a delete failed", async () => {
    mockReads([entra]);
    vi.mocked(deleteIdentityProvider).mockResolvedValue({
      error: { code: "version_conflict", message: "changed" },
    } as never);
    renderPage();
    await userEvent.click(
      (await rowOf("Entra ID")).getByRole("button", { name: "Delete" }),
    );
    await userEvent.click(
      within(screen.getByRole("dialog")).getByRole("button", {
        name: "Delete",
      }),
    );
    expect(await screen.findByText(/changed/)).toBeInTheDocument();
  });

  it("disables a provider with the version it read", async () => {
    mockReads([entra]);
    vi.mocked(replaceIdentityProvider).mockResolvedValue({
      data: entra,
    } as never);
    renderPage();
    await userEvent.click(await screen.findByRole("switch"));
    await waitFor(() =>
      expect(replaceIdentityProvider).toHaveBeenCalledWith(
        expect.objectContaining({
          path: { providerId: entra.id },
          headers: { "If-Match": "2" },
          body: expect.objectContaining({ enabled: false }),
        }),
      ),
    );
  });

  it("tests an OIDC provider and rotates a SAML signing key", async () => {
    mockReads([entra, saml(new Date(Date.now() + 400 * DAY).toISOString())]);
    vi.mocked(testIdentityProvider).mockResolvedValue({
      data: { latencyMs: 12 },
    } as never);
    vi.mocked(rotateIdentityProviderSpKey).mockResolvedValue({
      data: {},
    } as never);
    renderPage();
    await userEvent.click(
      (await rowOf("Entra ID")).getByRole("button", {
        name: "Test connection",
      }),
    );
    expect(
      await screen.findByText("Connection succeeded in 12 ms"),
    ).toBeInTheDocument();
    await userEvent.click(
      (await rowOf("Okta SAML")).getByRole("button", {
        name: "Rotate signing key",
      }),
    );
    await waitFor(() => expect(rotateIdentityProviderSpKey).toHaveBeenCalled());
  });

  it("deletes after confirmation", async () => {
    mockReads([entra]);
    vi.mocked(deleteIdentityProvider).mockResolvedValue({
      data: undefined,
    } as never);
    renderPage();
    await userEvent.click(
      (await rowOf("Entra ID")).getByRole("button", { name: "Delete" }),
    );
    await userEvent.click(
      within(screen.getByRole("dialog")).getByRole("button", {
        name: "Delete",
      }),
    );
    await waitFor(() =>
      expect(deleteIdentityProvider).toHaveBeenCalledWith({
        path: { providerId: entra.id },
        headers: { "If-Match": "2" },
      }),
    );
  });

  it("walks the wizard to create an Entra ID provider with its template", async () => {
    mockReads([]);
    vi.mocked(createIdentityProvider).mockResolvedValue({
      data: entra,
    } as never);
    renderPage();
    await userEvent.click(
      await screen.findByRole("button", { name: /Add provider/ }),
    );
    const dialog = screen.getByRole("dialog");
    await userEvent.click(within(dialog).getByRole("button", { name: "Next" }));
    const inputs = () =>
      within(screen.getByRole("dialog")).getAllByRole("textbox");
    // A step with a missing field does not advance.
    await userEvent.click(within(dialog).getByRole("button", { name: "Next" }));
    expect(within(dialog).getAllByText("Required").length).toBeGreaterThan(0);
    await userEvent.type(inputs()[0], "Entra ID");
    await userEvent.type(inputs()[1], "entra");
    await userEvent.type(
      inputs()[2],
      "https://login.microsoftonline.com/t/v2.0",
    );
    await userEvent.type(inputs()[3], "client");
    await userEvent.click(within(dialog).getByRole("button", { name: "Next" }));
    await userEvent.click(
      within(dialog).getByRole("button", { name: "Create provider" }),
    );
    await waitFor(() =>
      expect(createIdentityProvider).toHaveBeenCalledWith({
        body: expect.objectContaining({
          slug: "entra",
          kind: "oidc",
          attributeMapping: { subject: "tid:oid" },
          oidc: expect.objectContaining({
            clientId: "client",
            audience: "client",
            redirectUrl: expect.stringMatching(
              /\/auth\/oidc\/entra\/callback$/,
            ),
          }),
        }),
      }),
    );
  });

  it("creates a SAML provider and imports its metadata", async () => {
    mockReads([]);
    vi.mocked(createIdentityProvider).mockResolvedValue({
      data: saml(stamp),
    } as never);
    vi.mocked(importIdentityProviderMetadata).mockResolvedValue({
      data: saml(stamp),
    } as never);
    renderPage();
    await userEvent.click(
      await screen.findByRole("button", { name: /Add provider/ }),
    );
    const dialog = screen.getByRole("dialog");
    await userEvent.click(
      within(dialog).getByRole("radio", { name: "SAML 2.0" }),
    );
    await userEvent.click(within(dialog).getByRole("radio", { name: "Okta" }));
    await userEvent.click(within(dialog).getByRole("button", { name: "Next" }));
    const inputs = within(dialog).getAllByRole("textbox");
    await userEvent.type(inputs[0], "Okta SAML");
    await userEvent.type(inputs[1], "okta");
    await userEvent.type(inputs[2], "https://idp.example.test/metadata");
    await userEvent.click(within(dialog).getByRole("button", { name: "Next" }));
    await userEvent.click(
      within(dialog).getByRole("button", { name: "Create provider" }),
    );
    await waitFor(() =>
      expect(importIdentityProviderMetadata).toHaveBeenCalledWith({
        path: { providerId: saml(stamp).id },
        body: { url: "https://idp.example.test/metadata" },
      }),
    );
    expect(createIdentityProvider).toHaveBeenCalledWith({
      body: expect.objectContaining({ kind: "saml", enabled: false }),
    });
  });

  it("reports a failed metadata import without losing the provider", async () => {
    mockReads([]);
    vi.mocked(createIdentityProvider).mockResolvedValue({
      data: saml(stamp),
    } as never);
    vi.mocked(importIdentityProviderMetadata).mockResolvedValue({
      error: { code: "bad_metadata", message: "unreadable" },
    } as never);
    renderPage();
    await userEvent.click(
      await screen.findByRole("button", { name: /Add provider/ }),
    );
    const dialog = screen.getByRole("dialog");
    await userEvent.click(
      within(dialog).getByRole("radio", { name: "SAML 2.0" }),
    );
    await userEvent.click(within(dialog).getByRole("button", { name: "Next" }));
    const inputs = within(dialog).getAllByRole("textbox");
    await userEvent.type(inputs[0], "Okta SAML");
    await userEvent.type(inputs[1], "okta");
    await userEvent.type(inputs[2], "https://idp.example.test/metadata");
    await userEvent.click(within(dialog).getByRole("button", { name: "Next" }));
    await userEvent.click(
      within(dialog).getByRole("button", { name: "Create provider" }),
    );
    expect(
      await screen.findByText(/could not be imported/),
    ).toBeInTheDocument();
  });

  it("edits a provider and keeps the stored secret unless asked to clear it", async () => {
    mockReads([entra]);
    vi.mocked(replaceIdentityProvider).mockResolvedValue({
      data: entra,
    } as never);
    renderPage();
    await userEvent.click(
      (await rowOf("Entra ID")).getByRole("button", { name: "Edit" }),
    );
    const dialog = screen.getByRole("dialog");
    expect(within(dialog).getByDisplayValue("entra")).toBeDisabled();
    await userEvent.click(within(dialog).getByRole("button", { name: "Next" }));
    await userEvent.click(within(dialog).getByRole("button", { name: "Save" }));
    await waitFor(() =>
      expect(replaceIdentityProvider).toHaveBeenCalledWith(
        expect.objectContaining({
          headers: { "If-Match": "2" },
          body: expect.objectContaining({
            oidc: expect.not.objectContaining({
              clientSecret: expect.anything(),
            }),
          }),
        }),
      ),
    );
  });
});
