import type { Translate } from "../../lib/preferences";
import type {
  CreateIdentityProviderWritable,
  IdentityProvider,
  ReplaceIdentityProviderWritable,
} from "../../client";

export interface AuthenticationFormValues {
  enabled: boolean;
  issuer: string;
  audience: string;
  jwksUrl: string;
  clientId: string;
  clientSecret: string;
  redirectUrl: string;
  scopes: string[];
  adminSubjects: string[];
  memberRoles: string[];
  adminRoles: string[];
  provisioningMode: "disabled" | "jit" | "scim_only";
  emailLinkingEnabled: boolean;
  jitDefaultRole: "none" | "member";
}

/**
 * The provider as the form edits it. A provider that does not exist yet has
 * an empty id and version; its first save creates it with the slug "default".
 */
export interface AuthenticationSettings extends AuthenticationFormValues {
  id: string;
  slug: string;
  displayName: string;
  version: string;
  clientSecretConfigured: boolean;
  algorithms: ("RS256" | "ES256" | "EdDSA")[];
  attributeMapping: IdentityProvider["attributeMapping"];
  updatedAt?: string;
}

export const DEFAULT_PROVIDER_SLUG = "default";

export function emptySettings(displayName: string): AuthenticationSettings {
  return {
    id: "",
    slug: DEFAULT_PROVIDER_SLUG,
    displayName,
    version: "",
    enabled: false,
    issuer: "",
    audience: "",
    jwksUrl: "",
    clientId: "",
    clientSecret: "",
    clientSecretConfigured: false,
    redirectUrl: "",
    scopes: ["openid", "profile", "email"],
    adminSubjects: [],
    memberRoles: [],
    adminRoles: [],
    algorithms: ["RS256"],
    attributeMapping: {},
    provisioningMode: "disabled",
    emailLinkingEnabled: false,
    jitDefaultRole: "none",
  };
}

export function settingsFromProvider(
  provider: IdentityProvider,
): AuthenticationSettings {
  const oidc = provider.oidc;
  return {
    id: provider.id,
    slug: provider.slug,
    displayName: provider.displayName,
    version: provider.version,
    enabled: provider.enabled,
    issuer: oidc?.issuer ?? "",
    audience: oidc?.audience ?? "",
    jwksUrl: oidc?.jwksUrl ?? "",
    clientId: oidc?.clientId ?? "",
    clientSecret: "",
    clientSecretConfigured: oidc?.clientSecretConfigured ?? false,
    redirectUrl: oidc?.redirectUrl ?? "",
    scopes: oidc?.scopes ?? [],
    adminSubjects: oidc?.adminSubjects ?? [],
    memberRoles: oidc?.memberRoles ?? [],
    adminRoles: oidc?.adminRoles ?? [],
    algorithms: oidc?.algorithms ?? ["RS256"],
    attributeMapping: provider.attributeMapping,
    provisioningMode: provider.provisioningMode,
    emailLinkingEnabled: provider.emailLinkingEnabled,
    jitDefaultRole: provider.jitDefaultRole,
    updatedAt: provider.updatedAt,
  };
}

/** The form's starting values; the client secret is never read back. */
export function toFormValues(
  settings: AuthenticationSettings,
): AuthenticationFormValues {
  return {
    enabled: settings.enabled,
    issuer: settings.issuer,
    audience: settings.audience,
    jwksUrl: settings.jwksUrl,
    clientId: settings.clientId,
    clientSecret: "",
    redirectUrl: settings.redirectUrl,
    scopes: settings.scopes,
    adminSubjects: settings.adminSubjects,
    memberRoles: settings.memberRoles,
    adminRoles: settings.adminRoles,
    provisioningMode: settings.provisioningMode,
    emailLinkingEnabled: settings.emailLinkingEnabled,
    jitDefaultRole: settings.jitDefaultRole,
  };
}

/** Field errors the server would otherwise reject; empty when valid. */
export function authenticationFieldErrors(
  values: AuthenticationFormValues,
  t: Translate,
) {
  if (!values.enabled) return [];
  const required: (keyof AuthenticationFormValues)[] = ["issuer", "audience"];
  const missing = required.filter((name) => !String(values[name] ?? "").trim());
  const fields = missing.map((name) => ({
    name,
    errors: [t("settings.requiredWhenOIDCIsEnabled")],
  }));
  const hasClientId = Boolean(values.clientId.trim());
  const hasRedirectUrl = Boolean(values.redirectUrl.trim());
  if (hasClientId !== hasRedirectUrl) {
    fields.push({
      name: hasClientId ? "redirectUrl" : "clientId",
      errors: [t("settings.clientIDAndRedirectURLMustBeConfigured")],
    });
  }
  if (values.clientSecret.trim() && !hasClientId) {
    fields.push({
      name: "clientId",
      errors: [t("settings.clientIDIsRequiredWhenAClientSecret")],
    });
  }
  return fields;
}

function oidcBody(
  values: AuthenticationFormValues,
  settings: AuthenticationSettings,
  clearSecret: boolean,
) {
  const oidc: ReplaceIdentityProviderWritable["oidc"] = {
    issuer: values.issuer.trim(),
    audience: values.audience.trim(),
    clientId: values.clientId.trim(),
    redirectUrl: values.redirectUrl.trim(),
    scopes: values.scopes ?? [],
    adminSubjects: values.adminSubjects ?? [],
    memberRoles: values.memberRoles ?? [],
    adminRoles: values.adminRoles ?? [],
    algorithms: settings.algorithms,
  };
  if (values.jwksUrl.trim()) oidc.jwksUrl = values.jwksUrl.trim();
  if (values.clientSecret.trim())
    oidc.clientSecret = values.clientSecret.trim();
  if (clearSecret) oidc.clearClientSecret = true;
  return oidc;
}

/** The replace request for a saved provider; slug and kind never change. */
export function toReplaceBody(
  values: AuthenticationFormValues,
  settings: AuthenticationSettings,
  clearSecret: boolean,
): ReplaceIdentityProviderWritable {
  return {
    displayName: settings.displayName,
    enabled: values.enabled,
    oidc: oidcBody(values, settings, clearSecret),
    attributeMapping: settings.attributeMapping,
    provisioningMode: values.provisioningMode,
    emailLinkingEnabled: values.emailLinkingEnabled,
    jitDefaultRole: values.jitDefaultRole,
  };
}

/** The create request for the first save, when no provider exists yet. */
export function toCreateBody(
  values: AuthenticationFormValues,
  settings: AuthenticationSettings,
  clearSecret: boolean,
): CreateIdentityProviderWritable {
  return {
    slug: settings.slug,
    kind: "oidc",
    ...toReplaceBody(values, settings, clearSecret),
  };
}
