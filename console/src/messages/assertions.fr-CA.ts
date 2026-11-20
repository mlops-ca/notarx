import type { assertionsEnCA } from "./assertions.en-CA";

export const assertionsFrCA: Record<keyof typeof assertionsEnCA, string> = {
  "assertions.title": "Audiences des assertions",
  "assertions.description":
    "Ce compte peut émettre des assertions signées de courte durée seulement pour les audiences énumérées ici. Rien n’est autorisé tant que vous n’ajoutez pas d’audience, et les caractères génériques ne sont pas pris en charge.",
  "assertions.subject": "Sujet des assertions",
  "assertions.subjectHint":
    "La revendication sub de chaque assertion émise par ce compte. Elle est fixée à la création du compte et ne change pas si le compte est renommé. Utilisez-la, avec l’URL de l’émetteur, pour créer l’identifiant fédéré dans Microsoft Entra ID.",
  "assertions.noSubjectTitle": "Ce compte n’a pas de sujet d’assertion",
  "assertions.noSubjectDescription":
    "Son nom contient des caractères qu’un sujet d’assertion ne peut pas contenir, donc il ne peut pas émettre d’assertions. Créez de nouveau le compte avec un sujet d’assertion personnalisé pour les activer.",
  "assertions.allowAudience": "Autoriser une audience",
  "assertions.audience": "Audience",
  "assertions.audienceHint":
    "Comparée exactement, par exemple api://AzureADTokenExchange pour Microsoft Entra ID.",
  "assertions.maxLifetime": "Durée de vie maximale",
  "assertions.added": "Ajoutée",
  "assertions.lifetime1m": "1 minute",
  "assertions.lifetime2m": "2 minutes",
  "assertions.lifetime5m": "5 minutes",
  "assertions.lifetime10m": "10 minutes",
  "assertions.allow": "Autoriser",
  "assertions.cancel": "Annuler",
  "assertions.remove": "Retirer",
  "assertions.removeAudience": "Retirer l’audience {audience}",
  "assertions.removeTitle": "Retirer l’audience",
  "assertions.removeMessage":
    "Ce compte ne peut plus émettre d’assertions pour {audience}. Les assertions déjà émises restent valides jusqu’à leur expiration.",
  "assertions.loading": "Chargement des audiences",
  "assertions.emptyTitle": "Aucune audience autorisée",
  "assertions.emptyHint":
    "Ajoutez une audience avant que ce compte puisse émettre une assertion.",
  "assertions.saveFailed": "Impossible d’enregistrer l’audience",
  "assertions.customSubject": "Sujet d’assertion (facultatif)",
  "assertions.customSubjectHint":
    "Laissez vide pour utiliser notarx:service-account: suivi du nom. Ne le définissez que pour conserver le fonctionnement d’un identifiant fédéré Microsoft Entra existant. Il ne peut pas être modifié par la suite.",
};
