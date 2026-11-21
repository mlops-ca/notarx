import { describe, expect, it } from "vitest";
import type { IdentityProvider } from "../../../client";
import { createTranslator } from "../../../messages/translate";
import {
  connectionErrors,
  daysUntil,
  draftFromProvider,
  newDraft,
  toCreateBody,
  toReplaceBody,
} from "./providerModel";

const t = createTranslator("en-CA");
const origin = "https://notarx.example.test";

const oidcProvider: IdentityProvider = {
  id: "00000000-0000-4000-8000-000000000001",
  slug: "entra",
  kind: "oidc",
  displayName: "Entra",
  enabled: true,
  version: "3",
  attributeMapping: { subject: "tid:oid" },
  provisioningMode: "jit",
  emailLinkingEnabled: false,
  jitDefaultRole: "none",
  createdAt: "2026-10-01T00:00:00Z",
  updatedAt: "2026-10-01T00:00:00Z",
  oidc: {
    issuer: "https://login.microsoftonline.com/t/v2.0",
    audience: "client",
    clientId: "client",
    clientSecretConfigured: true,
    redirectUrl: "https://notarx.example.test/auth/oidc/entra/callback",
    scopes: ["openid"],
    adminSubjects: ["legacy-admin"],
    algorithms: ["ES256"],
  },
};

describe("provider templates", () => {
  it("keys Entra ID sign-ins on the tenant and object id", () => {
    expect(newDraft("oidc", "entra", origin).attributeMapping).toEqual({
      subject: "tid:oid",
    });
    expect(newDraft("oidc", "generic", origin).attributeMapping).toEqual({});
    expect(newDraft("oidc", "okta", origin).scopes).toContain("groups");
  });

  it("validates the connection step", () => {
    const draft = newDraft("oidc", "keycloak", origin);
    expect(Object.keys(connectionErrors(draft, false, t)).sort()).toEqual([
      "clientId",
      "displayName",
      "issuer",
      "redirectUrl",
      "slug",
    ]);
    const saml = newDraft("saml", "okta", origin);
    saml.slug = "okta-saml";
    saml.displayName = "Okta";
    expect(Object.keys(connectionErrors(saml, false, t))).toEqual(["metadata"]);
    saml.metadataUrl = "https://idp.example.test/metadata";
    expect(connectionErrors(saml, false, t)).toEqual({});
    expect(
      Object.keys(connectionErrors(newDraft("saml", "okta", origin), true, t)),
    ).toEqual(["displayName"]);
  });
});

describe("provider request bodies", () => {
  it("creates an OIDC provider with the client id as default audience", () => {
    const draft = {
      ...newDraft("oidc", "entra", origin),
      slug: "entra",
      displayName: " Entra ",
      issuer: " https://login.microsoftonline.com/t/v2.0 ",
      clientId: "client",
      clientSecret: "secret",
      redirectUrl: "https://notarx.example.test/auth/oidc/entra/callback",
      enabled: true,
    };
    const body = toCreateBody(draft);
    expect(body).toMatchObject({
      slug: "entra",
      kind: "oidc",
      displayName: "Entra",
      enabled: true,
      attributeMapping: { subject: "tid:oid" },
      oidc: {
        issuer: "https://login.microsoftonline.com/t/v2.0",
        audience: "client",
        clientSecret: "secret",
      },
    });
    expect(body.saml).toBeUndefined();
  });

  it("creates a SAML provider disabled and empty for the metadata import", () => {
    const draft = {
      ...newDraft("saml", "keycloak", origin),
      slug: "kc",
      displayName: "Keycloak",
      metadataUrl: "https://kc.example.test/descriptor",
      enabled: true,
    };
    const body = toCreateBody(draft);
    expect(body.enabled).toBe(false);
    expect(body.saml).toMatchObject({
      entityId: "",
      ssoUrl: "",
      certificates: [],
      metadataUrl: "https://kc.example.test/descriptor",
    });
    expect(body.oidc).toBeUndefined();
  });

  it("keeps settings the wizard does not offer when replacing", () => {
    const draft = { ...draftFromProvider(oidcProvider), clearSecret: true };
    const body = toReplaceBody(draft, oidcProvider);
    expect(body.oidc).toMatchObject({
      adminSubjects: ["legacy-admin"],
      algorithms: ["ES256"],
      clearClientSecret: true,
    });
    expect(body.oidc?.clientSecret).toBeUndefined();
  });

  it("keeps the pinned certificates of a SAML provider when replacing", () => {
    const saml: IdentityProvider = {
      ...oidcProvider,
      kind: "saml",
      oidc: undefined,
      saml: {
        entityId: "e",
        ssoUrl: "https://idp.example.test/sso",
        certificates: [
          { pem: "pem", notAfter: "2030-01-01T00:00:00Z", state: "next" },
        ],
        nameIdFormat: "emailAddress",
        allowIdpInitiated: false,
        relayStateAllowlist: [],
        signRequests: true,
        metadataRefresh: false,
        spEntityId: "",
        acsUrl: "",
        metadataEndpoint: "",
        spKeys: [],
      },
    };
    const body = toReplaceBody(draftFromProvider(saml), saml);
    expect(body.saml).toMatchObject({
      entityId: "e",
      certificates: [{ pem: "pem", state: "next" }],
      nameIdFormat: "emailAddress",
      signRequests: true,
    });
  });
});

describe("certificate expiry", () => {
  it("counts whole days", () => {
    const now = Date.parse("2026-11-19T00:00:00Z");
    expect(daysUntil("2026-11-29T00:00:00Z", now)).toBe(10);
    expect(daysUntil("2026-11-18T00:00:00Z", now)).toBe(-1);
  });
});
