import type { migrationEnCA } from "./migration.en-CA";

export const migrationFrCA: Record<keyof typeof migrationEnCA, string> = {
  "migration.tab": "Sources de migration",
  "migration.sources": "Sources de migration Nexus",
  "migration.scope": "Connecter une source et prévisualiser sa portée",
  "migration.scopeHint":
    "Lit la configuration de la source et les métadonnées des ressources. La découverte et la prévisualisation du mappage ne copient aucun octet et ne créent aucun dépôt cible.",
  "migration.newSource": "Ajouter une source de migration",
  "migration.editSource": "Modifier la source de migration",
  "migration.sourceName": "Nom de la source",
  "migration.baseUrl": "Adresse HTTPS de Nexus",
  "migration.baseUrlHint":
    "Saisissez l’adresse de l’instance et, au besoin, le chemin de contexte, sans justificatifs, paramètres de requête ni fragment. Les réseaux privés exigent une liste d’autorisation explicite sur le serveur.",
  "migration.credentials": "Justificatifs d’identité de la source",
  "migration.noCredentials": "Ne pas utiliser de justificatifs",
  "migration.keepCredentials": "Conserver les justificatifs actuels",
  "migration.replaceCredentials": "Définir de nouveaux justificatifs",
  "migration.clearCredentials": "Effacer les justificatifs actuels",
  "migration.credentialsConfigured": "Justificatifs configurés",
  "migration.credentialsAbsent": "Aucun justificatif configuré",
  "migration.credentialHint":
    "Les justificatifs ne sont jamais relus ni enregistrés dans le navigateur. Les champs sont vidés après chaque envoi; saisissez-les de nouveau après un échec. Modifier l’adresse de la source exige de remplacer ou d’effacer les justificatifs existants.",
  "migration.username": "Nom d’utilisateur de la source",
  "migration.password": "Mot de passe de la source",
  "migration.save": "Enregistrer la source de migration",
  "migration.create": "Créer la source de migration",
  "migration.cancel": "Annuler",
  "migration.edit": "Modifier la source",
  "migration.delete": "Supprimer la source",
  "migration.deleteTitle": "Supprimer la source de migration?",
  "migration.deleteHint":
    "Supprime la connexion et les justificatifs chiffrés de {name}. La configuration et le contenu du Nexus source ne sont pas touchés.",
  "migration.refresh": "Actualiser la source",
  "migration.reloadLatest": "Lire la dernière version",
  "migration.moreSources": "Charger plus de sources de migration",
  "migration.noSources": "Aucune source de migration configurée",
  "migration.noSourcesHint":
    "Ajoutez une source Nexus 3 pour inspecter les dépôts visibles avec les justificatifs et prévisualiser les mappages vers les cibles natives.",
  "migration.selectSource": "Choisir une source de migration à inspecter",
  "migration.selectSourceHint":
    "Les connexions sont enregistrées sur le serveur. Une vérification préalable est une observation en lecture seule; elle ne prouve ni la migration ni la disponibilité à retirer Nexus.",
  "migration.version": "Version de la connexion {version}",
  "migration.preflight": "Lancer la vérification préalable",
  "migration.preflightTitle": "Portée des dépôts et mappage explicite",
  "migration.preflightEmpty": "La vérification préalable n’a pas été lancée",
  "migration.preflightEmptyHint":
    "Lisez les dépôts visibles avec les justificatifs, leurs formats et leurs limites, puis sélectionnez des dépôts pour la découverte ou la prévisualisation du mappage.",
  "migration.visibleScope":
    "Seulement la portée actuellement visible avec les justificatifs; l’exhaustivité de l’inventaire global n’est pas vérifiée. Snapshot de la version de connexion {version}.",
  "migration.noRepositories": "Aucun dépôt visible avec ces justificatifs",
  "migration.sourceRepository": "Dépôt source",
  "migration.targetName": "Nom de la cible native",
  "migration.targetNameHint":
    "Lettres minuscules, chiffres et traits d’union. Saisissez des noms explicites; les anciennes URL ne sont pas conservées. Sélectionnez jusqu’à 10 dépôts sources.",
  "migration.chooseRepository": "Sélectionner {name}",
  "migration.targetFor": "Nom de la cible pour {name}",
  "migration.members": "Ordre des membres source",
  "migration.groupHint":
    "Sélectionnez les membres et saisissez leurs noms cibles. La prévisualisation conserve cet ordre des membres.",
  "migration.supported": "Découverte hébergée prise en charge",
  "migration.previewOnly": "Prévisualisation de la configuration seulement",
  "migration.unsupported": "Non pris en charge",
  "migration.discover": "Découvrir le contenu hébergé sélectionné ({count})",
  "migration.preview": "Prévisualiser les mappages explicites",
  "migration.discoveryTitle": "Découverte des ressources sources",
  "migration.discoverySummary":
    "{assets} ressources sources, taille totale des métadonnées\u00a0: {bytes}. Chemins explicitement non pris en charge\u00a0: {unsupported}. Aucun octet n’a été téléchargé ni vérifié.",
  "migration.path": "Chemin source",
  "migration.size": "Taille des métadonnées source",
  "migration.reason": "Notes de traitement",
  "migration.noAssets":
    "Aucune ressource prise en charge dans la portée sélectionnée",
  "migration.unsupportedAssets":
    "Ressources explicitement non prises en charge",
  "migration.mappingTitle": "Résultats de la prévisualisation du mappage",
  "migration.mappingHint":
    "La prévisualisation valide les noms, les formats, les conflits et l’ordre des membres sans appliquer les mappages. Les permissions, les justificatifs et les champs manuels exigent une configuration explicite.",
  "migration.mappingValid":
    "Les mappages sélectionnés satisfont la validation actuelle. Aucune cible n’a été créée et aucun contenu n’a été migré.",
  "migration.mappingInvalid":
    "Les mappages contiennent des conflits ou des éléments non pris en charge. Ajustez-les et prévisualisez de nouveau.",
  "migration.ready": "Satisfait la validation actuelle",
  "migration.manual": "Configuration manuelle requise",
  "migration.invalid": "Mappage non valide",
  "migration.targetMembers": "Ordre des membres cibles\u00a0: {members}",
  "migration.observation":
    "Observation en lecture seule\u00a0: {requests} requêtes, {pages} pages, {bytes} reçus.",
  "migration.previousResult":
    "Le dernier résultat réussi est conservé. La dernière requête a échoué; ce résultat n’a pas été mis à jour.",
  "migration.failed":
    "La requête ne s’est pas terminée. Vérifiez le réseau et l’état du service, puis réessayez. Les erreurs n’affichent jamais les justificatifs de la source.",
  "migration.denied":
    "Cette source de migration exige l’identité d’un administrateur de la plateforme.",
  "migration.notFound":
    "La source de migration n’existe plus. Actualisez la liste des sources.",
  "migration.versionConflict":
    "Une autre opération a mis à jour cette connexion. Lisez la dernière version avant de soumettre de nouveau; saisissez de nouveau les justificatifs.",
  "migration.invalidSource":
    "Connexion non valide. Vérifiez son adresse HTTPS, son nom et ses justificatifs, et saisissez de nouveau les justificatifs à définir.",
  "migration.replaceRequired":
    "Modifier l’adresse de la source exige de remplacer ou d’effacer explicitement les justificatifs configurés.",
  "migration.sourceChanged":
    "La version de la connexion source a changé. Actualisez la connexion et relancez la vérification préalable.",
  "migration.upstreamDenied":
    "L’accès à la source a été refusé. Vérifiez les justificatifs, les permissions en lecture seule et la liste d’autorisation de sortie du serveur.",
  "migration.sourceUnavailable":
    "La lecture de la source a échoué. Vérifiez la connectivité à Nexus, TLS et les permissions de l’API en lecture seule, puis réessayez.",
  "migration.budget":
    "La découverte a dépassé son budget de sécurité ou la pagination était invalide. Sélectionnez moins de dépôts et réessayez. Aucun succès partiel n’a été renvoyé.",
  "migration.keyUnavailable":
    "Le chiffrement des justificatifs côté serveur est indisponible. Communiquez avec l’administrateur de la plateforme.",
  "migration.lookupUnavailable":
    "Les conflits de cibles n’ont pas pu être vérifiés. Rétablissez l’accès aux métadonnées des cibles et prévisualisez de nouveau.",
  "migration.reasonUnsupported":
    "Ce format, ce type de dépôt ou cette configuration de source n’est pas pris en charge. Inspectez la configuration de la source avant de décider de la suite.",
  "migration.reasonHostedInventory":
    "La configuration de la source et les métadonnées des ressources sont lisibles. Aucun contenu n’a été migré.",
  "migration.reasonReleasePolicy":
    "Seules les versions release des dépôts Maven RELEASE/MIXED peuvent être découvertes. Les politiques SNAPSHOT ou inconnues exigent un traitement distinct.",
  "migration.reasonSnapshot":
    "Le contenu Maven SNAPSHOT est hors de la portée de cette migration.",
  "migration.reasonMetadata":
    "Les métadonnées Maven partagées ou les chemins sans version release unique exigent un traitement distinct.",
  "migration.reasonManual":
    "Configurez explicitement les politiques natives et les justificatifs.",
  "migration.reasonFieldManual":
    "Ce champ de la source exige une configuration manuelle.",
  "migration.reasonFieldExpressible": "Exprimable par la configuration native.",
  "migration.reasonFieldUnsupported":
    "Ce champ de la source n’est pas pris en charge.",
  "migration.reasonConflict":
    "Une cible existante est en conflit sur le format, le type ou l’état.",
  "migration.reasonName": "Le nom de la cible est invalide ou en double.",
  "migration.reasonFormat":
    "Le format de la cible doit correspondre à celui du dépôt source.",
  "migration.reasonType":
    "Le type de la cible doit correspondre à celui du dépôt source.",
  "migration.reasonMembers":
    "Mappez explicitement tous les membres du groupe, en conservant leur ordre et leur format source.",
  "migration.reasonExistingGroup":
    "Le groupe existant exige une configuration explicite.",
  "migration.reasonMembersOnly": "Seul un groupe peut avoir des membres.",
};
