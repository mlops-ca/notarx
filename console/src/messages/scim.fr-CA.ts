import type { scimEnCA } from "./scim.en-CA";

export const scimFrCA: Record<keyof typeof scimEnCA, string> = {
  "scim.title": "Jetons SCIM",
  "scim.description":
    "Un jeton SCIM permet à un fournisseur d’identité de provisionner des utilisateurs et des groupes pour un seul fournisseur. Il est affiché une seule fois à sa création, il expire au plus tard après un an et sa révocation prend effet immédiatement.",
  "scim.forProvider": "Pour {name}",
  "scim.newToken": "Nouveau jeton",
  "scim.newTokenFor": "Nouveau jeton SCIM pour {name}",
  "scim.create": "Créer",
  "scim.tokenName": "Nom du jeton",
  "scim.tokenNameHint":
    "Indiquez où le jeton est utilisé, par exemple entra-provisioning.",
  "scim.validity": "Validité",
  "scim.validityHint":
    "Le jeton cesse de fonctionner à son expiration. Prévoyez de le remplacer avant cette date.",
  "scim.days30": "30 jours",
  "scim.days90": "90 jours (recommandé)",
  "scim.days180": "180 jours",
  "scim.year1": "1 an",
  "scim.shownOnce":
    "Le jeton est affiché une seule fois. Copiez-le dans le fournisseur d’identité avant de fermer la boîte de dialogue.",
  "scim.tokenCreated": "Jeton SCIM créé",
  "scim.copyNow": "Copiez le jeton maintenant",
  "scim.copyNowDescription":
    "C’est la seule fois que le jeton est affiché. Il n’est pas conservé et ne peut pas être récupéré; si vous le perdez, créez-en un nouveau.",
  "scim.baseUrlHint":
    "Utilisez-le comme jeton secret de l’application de provisionnement de {provider}, avec l’URL de base SCIM de cette instance, qui se termine par /scim/v2.",
  "scim.done": "Terminé",
  "scim.noTokens": "Aucun jeton SCIM pour ce fournisseur.",
  "scim.name": "Nom",
  "scim.status": "État",
  "scim.created": "Créé",
  "scim.expires": "Expire",
  "scim.actions": "Actions",
  "scim.revoke": "Révoquer",
  "scim.revokeToken": "Révoquer le jeton {name}",
  "scim.revokeTitle": "Révoquer le jeton SCIM",
  "scim.revokeMessage":
    "{provider} ne peut plus provisionner avec {name}. Cette action est irréversible; créez un nouveau jeton pour reprendre le provisionnement.",
  "scim.tokenExpiresSoon":
    "Le jeton SCIM {name} expire dans {days} jours ({date}). Créez un nouveau jeton et mettez à jour l’application de provisionnement avant cette date.",
  "scim.tokenExpired":
    "Le jeton SCIM {name} a expiré le {date}. Le provisionnement avec ce jeton est arrêté; créez un nouveau jeton.",
};
