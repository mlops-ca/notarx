export const providersEnCA = {
  "providers.title": "Identity providers",
  "providers.description":
    "Sign-in providers for the Console and the API. Users choose among the enabled providers on the sign-in page.",
  "providers.add": "Add provider",
  "providers.loading": "Loading identity providers",
  "providers.empty": "No identity provider yet",
  "providers.emptyHint":
    "Add an OpenID Connect or SAML provider such as Microsoft Entra ID, Okta or Keycloak. Local users and tokens keep working.",
  "providers.name": "Name",
  "providers.protocol": "Protocol",
  "providers.status": "Status",
  "providers.provisioning": "Provisioning",
  "providers.warnings": "Warnings",
  "providers.actions": "Actions",
  "providers.protocol.oidc": "OpenID Connect",
  "providers.protocol.saml": "SAML 2.0",
  "providers.mode.disabled": "No automatic accounts",
  "providers.mode.jit": "Create accounts at first sign-in",
  "providers.mode.scim_only": "SCIM only",
  "providers.jitRole.none": "Awaiting approval",
  "providers.jitRole.member": "Member",
  "providers.enabled": "Enabled",
  "providers.disabled": "Disabled",
  "providers.edit": "Edit",
  "providers.test": "Test connection",
  "providers.rotateKey": "Rotate signing key",
  "providers.delete": "Delete",
  "providers.cancel": "Cancel",
  "providers.next": "Next",
  "providers.back": "Back",
  "providers.create": "Create provider",
  "providers.save": "Save",
  "providers.none": "None",
  "providers.template.entra": "Microsoft Entra ID",
  "providers.template.okta": "Okta",
  "providers.template.keycloak": "Keycloak",
  "providers.template.generic": "Other provider",
  "providers.templateHint.entra":
    "Uses the tenant and object identifiers as the stable sign-in key.",
  "providers.templateHint.okta": "Reads the groups claim from the ID token.",
  "providers.templateHint.keycloak":
    "Reads realm groups from the groups claim.",
  "providers.templateHint.generic": "Fill in every setting yourself.",
  "providers.wizardTitle": "Add identity provider",
  "providers.editTitle": "Edit {name}",
  "providers.stepKind": "Provider",
  "providers.stepConnection": "Connection",
  "providers.stepPolicy": "Sign-in policy",
  "providers.chooseProtocol": "Protocol",
  "providers.chooseTemplate": "Provider template",
  "providers.displayName": "Display name",
  "providers.displayNameHint": "Shown on the sign-in button.",
  "providers.slug": "Slug",
  "providers.slugHint":
    "Lowercase letters, digits and hyphens. It is part of the callback URLs and cannot change later.",
  "providers.slugInvalid":
    "Use 2 to 63 lowercase letters, digits or hyphens, starting with a letter or digit.",
  "providers.required": "Required",
  "providers.issuer": "Issuer URL",
  "providers.issuerHint.entra":
    "https://login.microsoftonline.com/TENANT_ID/v2.0",
  "providers.issuerHint.okta":
    "https://ORGANIZATION.okta.com or the URL of its authorization server",
  "providers.issuerHint.keycloak": "https://HOST/realms/REALM",
  "providers.issuerHint.generic":
    "The issuer in the provider's discovery document",
  "providers.clientId": "Client ID",
  "providers.clientSecret": "Client secret",
  "providers.clientSecretKeep": "Leave empty to keep the stored secret.",
  "providers.clearSecret": "Remove the stored client secret",
  "providers.audience": "Audience",
  "providers.audienceHint":
    "The audience of ID tokens. Defaults to the client ID.",
  "providers.redirectUrl": "Redirect URI",
  "providers.redirectUrlHint":
    "Register this exact URL at the identity provider.",
  "providers.scopes": "Scopes",
  "providers.jwksUrl": "JWKS URL",
  "providers.jwksUrlHint":
    "Only when the provider does not publish it in its discovery document.",
  "providers.metadataUrl": "Metadata URL",
  "providers.metadataUrlHint.entra":
    "The App Federation Metadata URL of the enterprise application.",
  "providers.metadataUrlHint.okta": "The metadata URL of the SAML application.",
  "providers.metadataUrlHint.keycloak":
    "https://HOST/realms/REALM/protocol/saml/descriptor",
  "providers.metadataUrlHint.generic":
    "An HTTPS URL that serves the provider's SAML metadata.",
  "providers.metadataXml": "Metadata XML",
  "providers.metadataXmlHint": "Or paste the metadata instead of giving a URL.",
  "providers.metadataImportFailed":
    "The provider was created, but its metadata could not be imported. Import it again from the provider's Edit dialog.",
  "providers.metadataNeeded": "Give a metadata URL or paste the metadata XML.",
  "providers.samlSigning": "Sign authentication requests",
  "providers.samlNext":
    "After the provider is created, register the service provider metadata and the assertion consumer URL at your identity provider, then enable it.",
  "providers.provisioningMode": "Accounts",
  "providers.jitDefaultRole": "Role of new accounts",
  "providers.jitDefaultRoleHint":
    "A first sign-in never creates an administrator. Grant more through user groups.",
  "providers.emailLinking": "Link accounts by verified email",
  "providers.emailLinkingHint":
    "Links a first sign-in to an existing account whose email an administrator verified. Administrators and accounts with a local password are never linked.",
  "providers.enableNow": "Enable the provider",
  "providers.enableNowHint":
    "A disabled provider is hidden from the sign-in page.",
  "providers.created": "{name} was created.",
  "providers.saved": "{name} was saved.",
  "providers.testOk": "Connection succeeded in {latencyMs} ms",
  "providers.deleteTitle": "Delete identity provider",
  "providers.deleteMessage":
    "Delete {name}? People who sign in through it can no longer do so, and the groups it filled lose their provider.",
  "providers.rotated":
    "A new signing key for {name} was generated; register the updated service provider metadata at the identity provider.",
  "providers.spMetadataUrl": "Service provider metadata URL",
  "providers.acsUrl": "Assertion consumer service URL",
  "providers.certificates": "Pinned signing certificates",
  "providers.noCertificates": "No certificate is pinned yet.",
  "providers.certActive": "Active",
  "providers.certNext": "Next (rollover)",
  "providers.certExpires": "Expires {date}",
  "providers.certExpired":
    "The signing certificate of {name} expired on {date}. Sign-ins through this provider fail until a valid certificate is pinned.",
  "providers.certExpiresSoon":
    "The signing certificate of {name} expires in {days} days ({date}). Import the provider's metadata again, or pin the new certificate.",
  "providers.overageTitle": "{name}: too many groups for one sign-in",
  "providers.overage":
    "The provider did not send the groups of some users because they are in too many groups; they signed in without group memberships (last seen {date}, within the last 30 days). Use SCIM provisioning, or send app roles or only the groups assigned to the application.",
  "providers.adminSubjects": "Administrator subjects",
  "providers.adminSubjectsHint":
    "Exact subject values that sign in as administrators. No claim, role or group value raises an account level; use user groups for access.",
  "providers.sloTitle": "Single Logout is not supported",
  "providers.slo":
    "Signing out ends the notarx session only; the identity provider session stays open, and signing out there does not end the notarx session. A session lasts up to 12 hours. To cut access sooner, disable the user or end their sessions.",
  "providers.sloSignOut":
    "Signing out ends this notarx session only; your identity provider session stays open.",
} as const;
