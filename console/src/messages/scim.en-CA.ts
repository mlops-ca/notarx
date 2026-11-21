export const scimEnCA = {
  "scim.title": "SCIM tokens",
  "scim.description":
    "A SCIM token lets an identity provider provision users and groups for one provider only. It is shown once when you create it, it expires after at most one year, and revoking it takes effect at once.",
  "scim.forProvider": "For {name}",
  "scim.newToken": "New token",
  "scim.newTokenFor": "New SCIM token for {name}",
  "scim.create": "Create",
  "scim.tokenName": "Token name",
  "scim.tokenNameHint":
    "Say where the token is used, for example entra-provisioning.",
  "scim.validity": "Validity",
  "scim.validityHint":
    "The token stops working at its expiry. Plan to replace it before then.",
  "scim.days30": "30 days",
  "scim.days90": "90 days (recommended)",
  "scim.days180": "180 days",
  "scim.year1": "1 year",
  "scim.shownOnce":
    "The token is shown once. Copy it into the identity provider before you close the dialog.",
  "scim.tokenCreated": "SCIM token created",
  "scim.copyNow": "Copy the token now",
  "scim.copyNowDescription":
    "This is the only time the token is shown. It is not stored and cannot be recovered; if you lose it, create a new one.",
  "scim.baseUrlHint":
    "Use it as the secret token of the {provider} provisioning app, with the SCIM base URL of this instance ending in /scim/v2.",
  "scim.done": "Done",
  "scim.noTokens": "No SCIM tokens for this provider.",
  "scim.name": "Name",
  "scim.status": "Status",
  "scim.created": "Created",
  "scim.expires": "Expires",
  "scim.actions": "Actions",
  "scim.revoke": "Revoke",
  "scim.revokeToken": "Revoke token {name}",
  "scim.revokeTitle": "Revoke SCIM token",
  "scim.revokeMessage":
    "{provider} can no longer provision with {name}. This cannot be undone; create a new token to resume provisioning.",
  "scim.tokenExpiresSoon":
    "The SCIM token {name} expires in {days} days ({date}). Create a new token and update the provisioning app before then.",
  "scim.tokenExpired":
    "The SCIM token {name} expired on {date}. Provisioning with it has stopped; create a new token.",
} as const;
