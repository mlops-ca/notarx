import type { groupsEnCA } from "./groups.en-CA";

export const groupsFrCA: Record<keyof typeof groupsEnCA, string> = {
  "groups.available": "Disponibles ({length})",
  "groups.allAdded": "Tous ajoutés",
  "groups.configuredOrderWithinEachType":
    "Ordre configuré ({length}, au sein de chaque type)",
  "groups.addMembersFromTheLeft": "Ajoutez des membres depuis la gauche",
  "groups.moveUp": "Monter",
  "groups.moveDown": "Descendre",
  "groups.remove": "Retirer",
  "groups.newGroup": "Nouveau groupe",
  "groups.cancel": "Annuler",
  "groups.create": "Créer",
  "groups.groupName": "Nom du groupe",
  "groups.format": "Format",
  "groups.memberRepositories": "Dépôts membres",
  "groups.hostedMembersComeFirstMembersOfTheSame":
    "Les membres hébergés passent en premier; les membres du même type suivent l’ordre configuré",
  "groups.noActiveRepositoriesForThisFormat":
    "Aucun dépôt actif pour ce format",
  "groups.allowAnonymousReads": "Autoriser les lectures anonymes",
  "groups.anonymousRequestsResolveAMemberOnlyWhenBoth":
    "Une requête anonyme ne résout un membre que si le groupe et le dépôt membre autorisent tous deux les lectures anonymes.",
  "groups.allowAnonymousGroupReads":
    "Autoriser les lectures anonymes du groupe",
  "groups.resolutionOrder": "Ordre de résolution",
  "groups.resolutionOrder2": "Ordre de résolution\u00a0: {name}",
  "groups.refresh": "Actualiser",
  "groups.close": "Fermer",
  "groups.hostedMembersComeFirstMembersOfTheSame2":
    "Les membres hébergés passent en premier; les membres du même type suivent leurs positions configurées. Ce sont des candidats; les correspondances réelles dépendent de l’artefact, des permissions de lecture et des règles du protocole.",
  "groups.configuredMemberSAreCurrentlyExcludedFromResolution":
    "{excludedMemberCount} membre(s) configuré(s) sont actuellement exclus de la résolution.",
  "groups.noEligibleMembers": "Aucun membre admissible",
  "groups.effectiveCandidateOrder": "Ordre effectif des candidats",
  "groups.configuredPosition": "Position configurée {position}",
  "groups.position": "Position",
  "groups.member": "Membre",
  "groups.type": "Type",
  "groups.used": "Utilisé",
  "groups.objects": "Objets",
  "groups.quota": "Quota",
  "groups.unlimited": "Illimité",
  "groups.capacity": "Capacité",
  "groups.capacity2": "Capacité · {name}",
  "groups.editMembers": "Modifier les membres",
  "groups.editMembers2": "Modifier les membres\u00a0: {name}",
  "groups.save": "Enregistrer",
  "groups.setTheOrderWithinEachTypeHostedMembers":
    "Définissez l’ordre au sein de chaque type; les membres hébergés sont essayés avant les membres proxy.",
  "groups.settings": "Paramètres",
  "groups.groupSettings": "Paramètres du groupe\u00a0: {name}",
  "groups.eachMemberRepositoryMustAlsoAllowAnonymousReads":
    "Chaque dépôt membre doit aussi autoriser les lectures anonymes.",
  "groups.name": "Nom",
  "groups.access": "Accès",
  "groups.anonymousRead": "Lecture anonyme",
  "groups.private": "Privé",
  "groups.publicGroups": "Groupes publics",
  "groups.visibilityConflict":
    "Un groupe ne peut pas être plus visible que son membre le moins visible ({members}). Abaissez d’abord la visibilité du groupe ou relevez celle du membre.",
  "groups.lessVisibleMembersWarning":
    "Les utilisateurs de ce groupe ne voient pas {members} : la visibilité propre de ce membre est inférieure à celle du groupe. Relevez la visibilité du membre ou abaissez celle du groupe.",
  "groups.visibilityCeilingHelp":
    "Un groupe est un plafond de visibilité : les appelants ne voient que les membres dont la visibilité propre les admet.",
  "groups.membersConfiguredOrder": "Membres (ordre configuré)",
  "groups.unknown": "inconnu",
  "groups.noMembers": "Aucun membre",
  "groups.actions": "Actions",
  "groups.browseDirectory": "Parcourir le répertoire",
  "groups.groups": "Groupes",
  "groups.aggregateRepositoriesOfTheSameFormatBehindOne":
    "Regroupez des dépôts du même format derrière un seul point d’entrée",
  "groups.groupsAreUnavailable": "Les groupes sont indisponibles",
  "groups.theCurrentBackendDoesNotExposeTheGroups":
    "Le serveur actuel n’expose pas le point de terminaison des groupes (404)",
  "groups.noGroups": "Aucun groupe",
  "groups.createAGroupToAggregateRepositories":
    "Créez un groupe pour regrouper des dépôts",
  "groups.groups2": "Groupes",
  "groups.memberReferences": "{memberCount} références de membres",
  "groups.anonymousReadable": "Lisible anonymement",
  "groups.memberRepositoriesMustAllowAnonymousReads":
    "Les dépôts membres doivent autoriser les lectures anonymes",
  "groups.allEntryPointsArePrivate": "Tous les points d’entrée sont privés",
  "groups.formats": "Formats",
  "groups.resolvedByMatchingFormat": "Résolu selon le format correspondant",
  "groups.search": "Rechercher",
  "groups.searchGroupNames": "Rechercher des noms de groupes…",
  "groups.allFormats": "Tous les formats",
  "groups.noMatchingGroups": "Aucun groupe correspondant",
  "groups.adjustTheFiltersAndTryAgain": "Ajustez les filtres et réessayez",
  "groups.deleteGroup2": "Supprimer le groupe",
  "groups.deleteGroup3": "Supprimer le groupe",
  "groups.memberRepositoriesAreNotDeleted":
    "? Les dépôts membres ne sont pas supprimés.",
  "groups.delete": "Supprimer",
  "groups.backToGroups": "Retour aux groupes",
  "groups.groupDirectory": "Répertoire du groupe",
  "groups.exploreMemberArtifactsAndTraceEachLocalSource":
    "Explorez les artefacts des membres et retracez chaque source locale.",
  "groups.readOnlyView": "Vue en lecture seule",
  "groups.thisDirectoryContainsPublicationsAndKnownCacheAn":
    "Ce répertoire contient des publications et le cache connu. Un ancien dépôt proxy sans données en cache peut encore servir l’artefact; les téléchargements suivent la résolution du protocole.",
  "groups.visibleMemberCandidateOrder": "Ordre des candidats membres visibles",
  "groups.moreActionsFor": "Plus d’actions pour {name}",
  "groups.deleteGroupAction": "Supprimer le groupe",
};
