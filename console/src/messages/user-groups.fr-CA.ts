import type { userGroupsEnCA } from "./user-groups.en-CA";

export const userGroupsFrCA: Record<keyof typeof userGroupsEnCA, string> = {
  "userGroups.title": "Groupes d’utilisateurs",
  "userGroups.description":
    "Ensembles de comptes que les droits d’accès aux dépôts peuvent désigner comme principal. Un groupe doté d’un rôle global relève le niveau de ses membres.",
  "userGroups.new": "Nouveau groupe",
  "userGroups.loading": "Chargement des groupes d’utilisateurs",
  "userGroups.empty": "Aucun groupe d’utilisateurs pour l’instant",
  "userGroups.emptyHint":
    "Créez un groupe local, ou un groupe qu’un fournisseur d’ouverture de session remplit à partir de sa revendication de groupes.",
  "userGroups.loadMore": "Charger plus",
  "userGroups.name": "Nom",
  "userGroups.source": "Source",
  "userGroups.source.local": "Local",
  "userGroups.source.scim": "SCIM",
  "userGroups.source.claims": "Revendications de connexion",
  "userGroups.globalRole": "Rôle global",
  "userGroups.role.none": "Aucun",
  "userGroups.role.member": "Membre",
  "userGroups.role.admin": "Administrateur",
  "userGroups.actions": "Actions",
  "userGroups.members": "Membres",
  "userGroups.rename": "Renommer",
  "userGroups.setRole": "Rôle global",
  "userGroups.delete": "Supprimer",
  "userGroups.cancel": "Annuler",
  "userGroups.close": "Fermer",
  "userGroups.create": "Créer",
  "userGroups.save": "Enregistrer",
  "userGroups.createTitle": "Nouveau groupe d’utilisateurs",
  "userGroups.nameHint":
    "Unique parmi les groupes locaux, 255 caractères au plus.",
  "userGroups.sourceHint":
    "Un groupe issu des revendications est repéré par l’identifiant externe de son fournisseur et ne porte jamais de rôle global.",
  "userGroups.provider": "Fournisseur d’ouverture de session",
  "userGroups.providerPlaceholder": "Sélectionnez un fournisseur",
  "userGroups.externalId": "Identifiant externe",
  "userGroups.externalIdHint":
    "La valeur de la revendication de groupes qui désigne ce groupe.",
  "userGroups.renameTitle": "Renommer {name}",
  "userGroups.roleTitle": "Rôle global de {name}",
  "userGroups.roleHint":
    "Le niveau d’un membre est le plus élevé entre le rôle propre du compte et les rôles globaux de ses groupes, évalué à l’authentification du compte.",
  "userGroups.claimsRoleNotice":
    "Un groupe rempli à partir des revendications de connexion n’a jamais que le rôle global aucun, afin qu’un fournisseur d’identité ne puisse pas relever un niveau.",
  "userGroups.adminWarningTitle": "Chaque membre devient administrateur",
  "userGroups.adminWarning":
    "Attribuer le rôle d’administrateur à {name} confère l’administration de la plateforme à chaque membre actuel et futur du groupe. Ajoutez-y des membres avec prudence.",
  "userGroups.deleteTitle": "Supprimer le groupe d’utilisateurs",
  "userGroups.deleteMessage":
    "Supprimer le groupe «\u00a0{name}\u00a0»? Ses appartenances sont retirées\u00a0: les membres perdent ce que les droits accordés au groupe leur donnaient. Un groupe recréé sous le même nom repart avec le rôle global aucun.",
  "userGroups.membersTitle": "Membres de {name}",
  "userGroups.addMember": "Ajouter un membre",
  "userGroups.userPlaceholder": "Sélectionnez un utilisateur à ajouter",
  "userGroups.noMembers": "Ce groupe n’a aucun membre",
  "userGroups.loadingMembers": "Chargement des membres",
  "userGroups.removeMember": "Retirer",
  "userGroups.removeMemberTitle": "Retirer le membre",
  "userGroups.removeMemberMessage":
    "Retirer l’utilisateur «\u00a0{user}\u00a0» du groupe «\u00a0{group}\u00a0»? Le compte perd ce que le groupe lui accordait dès sa prochaine requête.",
  "userGroups.memberUser": "Utilisateur",
  "userGroups.memberAdded": "Ajouté le",
  "userGroups.principalGroup": "Groupe d’utilisateurs",
  "userGroups.principalGroups": "Groupes d’utilisateurs",
  "userGroups.identityGroups": "Groupes d’utilisateurs",
  "userGroups.identityNoGroups": "Aucun",
};
