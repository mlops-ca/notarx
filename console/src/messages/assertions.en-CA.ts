export const assertionsEnCA = {
  "assertions.title": "Assertion audiences",
  "assertions.description":
    "This account can mint short-lived signed assertions only for the audiences listed here. Nothing is allowed until you add an audience, and there are no wildcards.",
  "assertions.subject": "Assertion subject",
  "assertions.subjectHint":
    "The sub claim of every assertion this account mints. It is fixed when the account is created and does not change if the account is renamed. Use it, with the issuer URL, when you create the federated credential in Microsoft Entra ID.",
  "assertions.noSubjectTitle": "This account has no assertion subject",
  "assertions.noSubjectDescription":
    "Its name contains characters that an assertion subject cannot hold, so it cannot mint assertions. Create the account again with a custom assertion subject to enable them.",
  "assertions.allowAudience": "Allow audience",
  "assertions.audience": "Audience",
  "assertions.audienceHint":
    "Matched exactly, for example api://AzureADTokenExchange for Microsoft Entra ID.",
  "assertions.maxLifetime": "Longest lifetime",
  "assertions.added": "Added",
  "assertions.lifetime1m": "1 minute",
  "assertions.lifetime2m": "2 minutes",
  "assertions.lifetime5m": "5 minutes",
  "assertions.lifetime10m": "10 minutes",
  "assertions.allow": "Allow",
  "assertions.cancel": "Cancel",
  "assertions.remove": "Remove",
  "assertions.removeAudience": "Remove audience {audience}",
  "assertions.removeTitle": "Remove audience",
  "assertions.removeMessage":
    "This account can no longer mint assertions for {audience}. Assertions that were already issued stay valid until they expire.",
  "assertions.loading": "Loading audiences",
  "assertions.emptyTitle": "No audiences allowed",
  "assertions.emptyHint":
    "Add an audience before this account can mint an assertion.",
  "assertions.saveFailed": "The audience could not be saved",
  "assertions.customSubject": "Assertion subject (optional)",
  "assertions.customSubjectHint":
    "Leave empty to use notarx:service-account: followed by the name. Set it only to keep an existing Microsoft Entra federated credential working. It cannot be changed later.",
} as const;
