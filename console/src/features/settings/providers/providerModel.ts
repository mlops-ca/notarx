import type {
  CreateIdentityProviderWritable,
  IdentityProvider,
  IdentityProviderAttributeMapping,
  IdentityProviderOidcInputWritable,
  IdentityProviderSamlInput,
  ReplaceIdentityProviderWritable,
} from "../../../client";
import type { MessageKey } from "../../../messages";
import type { Translate } from "../../../lib/preferences";

export type ProviderKind = "oidc" | "saml";
export type ProviderTemplate = "entra" | "okta" | "keycloak" | "generic";
export type ProvisioningMode = "disabled" | "jit" | "scim_only";

export const PROVIDER_TEMPLATES: ProviderTemplate[] = [
  "entra",
  "okta",
  "keycloak",
  "generic",
];

/** Slugs appear in callback URLs; the server enforces the same pattern. */
export const SLUG_PATTERN = /^[a-z0-9][a-z0-9-]{1,62}$/;

/** What the wizard edits; one flat shape for both protocols. */
export interface ProviderDraft {
  kind: ProviderKind;
  template: ProviderTemplate;
  slug: string;
  displayName: string;
  enabled: boolean;
  // OpenID Connect
  issuer: string;
  audience: string;
  jwksUrl: string;
  clientId: string;
  clientSecret: string;
  clearSecret: boolean;
  redirectUrl: string;
  scopes: string[];
  // SAML
  metadataUrl: string;
  metadataXml: string;
  signRequests: boolean;
  // Both
  provisioningMode: ProvisioningMode;
  jitDefaultRole: "none" | "member";
  emailLinkingEnabled: boolean;
  attributeMapping: IdentityProviderAttributeMapping;
}

/** Template defaults that differ between providers (docs/identity-provider-guides.md). */
const OIDC_SCOPES: Record<ProviderTemplate, string[]> = {
  entra: ["openid", "profile", "email"],
  okta: ["openid", "profile", "email", "groups"],
  keycloak: ["openid", "profile", "email"],
  generic: ["openid", "profile", "email"],
};

/** Entra's stable key is the tenant and object id; sub is pairwise per application. */
const OIDC_MAPPING: Record<ProviderTemplate, IdentityProviderAttributeMapping> =
  {
    entra: { subject: "tid:oid" },
    okta: {},
    keycloak: {},
    generic: {},
  };

export function issuerHintKey(template: ProviderTemplate): MessageKey {
  return `providers.issuerHint.${template}`;
}

export function metadataHintKey(template: ProviderTemplate): MessageKey {
  return `providers.metadataUrlHint.${template}`;
}

export function redirectUrlFor(slug: string, origin: string): string {
  return slug ? `${origin}/auth/oidc/${slug}/callback` : "";
}

export function newDraft(
  kind: ProviderKind,
  template: ProviderTemplate,
  origin: string,
): ProviderDraft {
  return {
    kind,
    template,
    slug: "",
    displayName: "",
    enabled: false,
    issuer: "",
    audience: "",
    jwksUrl: "",
    clientId: "",
    clientSecret: "",
    clearSecret: false,
    redirectUrl: redirectUrlFor("", origin),
    scopes: [...OIDC_SCOPES[template]],
    metadataUrl: "",
    metadataXml: "",
    signRequests: false,
    provisioningMode: "jit",
    jitDefaultRole: "none",
    emailLinkingEnabled: false,
    attributeMapping: kind === "oidc" ? { ...OIDC_MAPPING[template] } : {},
  };
}

export function draftFromProvider(provider: IdentityProvider): ProviderDraft {
  const oidc = provider.oidc;
  const saml = provider.saml;
  return {
    kind: provider.kind,
    template: "generic",
    slug: provider.slug,
    displayName: provider.displayName,
    enabled: provider.enabled,
    issuer: oidc?.issuer ?? "",
    audience: oidc?.audience ?? "",
    jwksUrl: oidc?.jwksUrl ?? "",
    clientId: oidc?.clientId ?? "",
    clientSecret: "",
    clearSecret: false,
    redirectUrl: oidc?.redirectUrl ?? "",
    scopes: oidc?.scopes ?? [],
    metadataUrl: saml?.metadataUrl ?? "",
    metadataXml: "",
    signRequests: saml?.signRequests ?? false,
    provisioningMode: provider.provisioningMode,
    jitDefaultRole: provider.jitDefaultRole,
    emailLinkingEnabled: provider.emailLinkingEnabled,
    attributeMapping: provider.attributeMapping,
  };
}

/** Field problems the server would reject; empty when the step can proceed. */
export function connectionErrors(
  draft: ProviderDraft,
  editing: boolean,
  t: Translate,
): Partial<
  Record<
    "slug" | "displayName" | "issuer" | "clientId" | "redirectUrl" | "metadata",
    string
  >
> {
  const errors: ReturnType<typeof connectionErrors> = {};
  if (!editing && !SLUG_PATTERN.test(draft.slug))
    errors.slug = t("providers.slugInvalid");
  if (!draft.displayName.trim()) errors.displayName = t("providers.required");
  if (draft.kind === "oidc") {
    if (!draft.issuer.trim()) errors.issuer = t("providers.required");
    if (!draft.clientId.trim()) errors.clientId = t("providers.required");
    if (!draft.redirectUrl.trim()) errors.redirectUrl = t("providers.required");
  } else if (
    !editing &&
    !draft.metadataUrl.trim() &&
    !draft.metadataXml.trim()
  ) {
    errors.metadata = t("providers.metadataNeeded");
  }
  return errors;
}

function oidcBody(
  draft: ProviderDraft,
  current?: IdentityProvider,
): IdentityProviderOidcInputWritable {
  const body: IdentityProviderOidcInputWritable = {
    issuer: draft.issuer.trim(),
    audience: draft.audience.trim() || draft.clientId.trim(),
    clientId: draft.clientId.trim(),
    redirectUrl: draft.redirectUrl.trim(),
    scopes: draft.scopes,
    // Settings that this wizard no longer offers are kept as they are stored.
    adminSubjects: current?.oidc?.adminSubjects ?? [],
    memberRoles: current?.oidc?.memberRoles ?? [],
    adminRoles: current?.oidc?.adminRoles ?? [],
  };
  if (current?.oidc?.algorithms) body.algorithms = current.oidc.algorithms;
  if (draft.jwksUrl.trim()) body.jwksUrl = draft.jwksUrl.trim();
  if (draft.clientSecret.trim()) body.clientSecret = draft.clientSecret.trim();
  if (draft.clearSecret) body.clearClientSecret = true;
  return body;
}

/** A new SAML provider starts empty; the metadata import fills in the rest. */
function samlBody(
  draft: ProviderDraft,
  current?: IdentityProvider,
): IdentityProviderSamlInput {
  const saml = current?.saml;
  return {
    entityId: saml?.entityId ?? "",
    ssoUrl: saml?.ssoUrl ?? "",
    certificates: (saml?.certificates ?? []).map(({ pem, state }) => ({
      pem,
      state,
    })),
    nameIdFormat: saml?.nameIdFormat ?? "persistent",
    allowIdpInitiated: saml?.allowIdpInitiated ?? false,
    relayStateAllowlist: saml?.relayStateAllowlist ?? [],
    signRequests: draft.signRequests,
    metadataUrl: saml?.metadataUrl ?? (draft.metadataUrl.trim() || undefined),
    metadataRefresh: saml?.metadataRefresh ?? false,
  };
}

/** The create request; a SAML provider is created disabled until its metadata is imported. */
export function toCreateBody(
  draft: ProviderDraft,
): CreateIdentityProviderWritable {
  return {
    slug: draft.slug,
    kind: draft.kind,
    displayName: draft.displayName.trim(),
    enabled: draft.kind === "oidc" ? draft.enabled : false,
    oidc: draft.kind === "oidc" ? oidcBody(draft) : undefined,
    saml: draft.kind === "saml" ? samlBody(draft) : undefined,
    attributeMapping: draft.attributeMapping,
    provisioningMode: draft.provisioningMode,
    emailLinkingEnabled: draft.emailLinkingEnabled,
    jitDefaultRole: draft.jitDefaultRole,
  };
}

/** The replace request for a saved provider; slug and kind never change. */
export function toReplaceBody(
  draft: ProviderDraft,
  current: IdentityProvider,
): ReplaceIdentityProviderWritable {
  return {
    displayName: draft.displayName.trim(),
    enabled: draft.enabled,
    oidc: current.kind === "oidc" ? oidcBody(draft, current) : undefined,
    saml: current.kind === "saml" ? samlBody(draft, current) : undefined,
    attributeMapping: draft.attributeMapping,
    provisioningMode: draft.provisioningMode,
    emailLinkingEnabled: draft.emailLinkingEnabled,
    jitDefaultRole: draft.jitDefaultRole,
  };
}

/** Whole days until a certificate expires; negative once it has. */
export const SAML_CERTIFICATE_WARNING_DAYS = 30;
const DAY_MS = 24 * 60 * 60 * 1000;
export function daysUntil(notAfter: string, now: number = Date.now()): number {
  return Math.ceil((Date.parse(notAfter) - now) / DAY_MS);
}
