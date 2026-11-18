import type { repositoryGrantsEnCA } from "./repository-grants.en-CA";

export const repositoryGrantsFrCA: Record<
  keyof typeof repositoryGrantsEnCA,
  string
> = {
  "repositoryGrants.couldNotLoadUsersApiKeysServiceAccountsOr":
    "Impossible de charger les utilisateurs, les clés d’API, les comptes de service ou les rôles personnalisés. Vous pouvez tout de même utiliser une identité personnalisée et les permissions intégrées.",
  "repositoryGrants.selectOrEnterAPrincipalForTheGrant":
    "Sélectionnez ou saisissez un principal pour l’autorisation.",
  "repositoryGrants.thePreviousRowCouldNotBeRemovedSoThe":
    "La ligne précédente n’a pas pu être supprimée; la nouvelle autorisation n’a donc pas été enregistrée. Veuillez réessayer.",
  "repositoryGrants.thePreviousRowWasRemovedButSavingTheNew":
    "La ligne précédente a été supprimée, mais l’enregistrement de la nouvelle autorisation a échoué. Enregistrez de nouveau pour l’écrire.",
  "repositoryGrants.grantRemoved": "Autorisation supprimée",
  "repositoryGrants.accessGrants": "Autorisations d’accès",
  "repositoryGrants.principal": "Principal",
  "repositoryGrants.grantedCapabilities": "Capacités accordées",
  "repositoryGrants.resourceScope": "Portée de la ressource",
  "repositoryGrants.entireRepository": "Dépôt entier",
  "repositoryGrants.actions": "Actions",
  "repositoryGrants.edit": "Modifier",
  "repositoryGrants.removeThisGrant": "Supprimer cette autorisation?",
  "repositoryGrants.onlyThisRowIsRemovedEveryOtherPrincipalKeeps":
    "Seule cette ligne est supprimée; chaque autre principal conserve ses autorisations.",
  "repositoryGrants.remove": "Supprimer",
  "repositoryGrants.cancel": "Annuler",
  "repositoryGrants.removeThisGrantRow": "Supprimer cette ligne d’autorisation",
  "repositoryGrants.addGrant": "Ajouter une autorisation",
  "repositoryGrants.noAccessGrants": "Aucune autorisation d’accès",
  "repositoryGrants.chooseAUserApiKeyOrServiceAccountVia":
    "Choisissez un utilisateur, une clé d’API ou un compte de service avec Ajouter une autorisation, ou saisissez un sujet OIDC ou un acteur personnalisé.",
  "repositoryGrants.editGrant": "Modifier l’autorisation",
  "repositoryGrants.save": "Enregistrer",
  "repositoryGrants.thisPrincipalAlreadyHoldsAGrantAtThisResource":
    "Ce principal détient déjà une autorisation pour cette portée de ressource; l’enregistrement la remplace.",
  "repositoryGrants.repositoryRulesAddPermissionsTheyCannotRevokeAnExisting":
    "Les règles de dépôt ajoutent des permissions; elles ne peuvent pas révoquer le rôle global existant d’un utilisateur ou d’une clé d’API. Les comptes de service n’ont aucun rôle global.",
  "repositoryGrants.usersApiKeysOrServiceAccountsAreTemporarilyUnavailable":
    "Les utilisateurs, les clés d’API ou les comptes de service sont temporairement indisponibles. Vous pouvez tout de même choisir OIDC ou acteur personnalisé et saisir son identifiant.",
};
