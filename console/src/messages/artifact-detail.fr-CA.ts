import type { artifactDetailEnCA } from "./artifact-detail.en-CA";

export const artifactDetailFrCA: Record<
  keyof typeof artifactDetailEnCA,
  string
> = {
  "artifactDetail.yankState": "État du retrait",
  "artifactDetail.yanked": "Retiré",
  "artifactDetail.notYanked": "Disponible pour la sélection",
  "artifactDetail.yankManage": "Gérer le retrait",
  "artifactDetail.yankSummary": "{yanked} / {total} fichiers retirés",
  "artifactDetail.yankTarget": "Portée",
  "artifactDetail.yankAllFiles": "Les {total} fichiers de cette version",
  "artifactDetail.yankAction": "Action",
  "artifactDetail.yankFiles": "Retirer les fichiers",
  "artifactDetail.unyankFiles": "Restaurer les fichiers",
  "artifactDetail.yankReason": "Motif du retrait (facultatif)",
  "artifactDetail.yankReasonLimit":
    "1024 octets au maximum, sans caractères de contrôle. La restauration efface le motif.",
  "artifactDetail.yankExplanation":
    "Le retrait (yank) influence la sélection de version par pip; une version exacte épinglée peut tout de même télécharger le fichier. Les octets du fichier, la quarantaine et les pierres tombales restent distincts.",
  "artifactDetail.yankConfirm": "Confirmer le retrait",
  "artifactDetail.unyankConfirm": "Confirmer la restauration",
  "artifactDetail.yankRefresh": "Actualiser et confirmer de nouveau",
  "artifactDetail.yankMissingResponse":
    "La réponse de retrait est incomplète. Actualisez pour vérifier le résultat.",
  "artifactDetail.yankFailed": "Échec du chargement de l’état du retrait",
  "artifactDetail.npmSaveDeprecation": "Enregistrer le message",
  "artifactDetail.npmDeprecated": "Cette version est dépréciée",
  "artifactDetail.npmEditDeprecation": "Modifier la dépréciation",
  "artifactDetail.npmUnpublishVersion":
    "Dépublier la version «\u00a0{version}\u00a0»?",
  "artifactDetail.npmUnpublishExplanation":
    "La version et les tags qui y pointent deviennent invisibles pour les clients natifs. La conservation contrôle la récupération des octets; restaurez depuis la page Pierres tombales du dépôt avant la récupération.",
  "artifactDetail.npmUnpublish": "Dépublier",
  "artifactDetail.npmGovernanceFailed":
    "Échec de la mise à jour (HTTP {status}). Lisez l’état actuel du paquet avant de décider de soumettre à nouveau.",
  "artifactDetail.npmGovernanceUnknown":
    "Le résultat de la mise à jour est inconnu. Lisez l’état du paquet avant de décider de soumettre à nouveau.",
  "artifactDetail.npmDeprecationMessage": "Message pour la version {version}",
  "artifactDetail.npmDeprecationExplanation":
    "Le message ne modifie pas les octets du paquet. Maximum de 4096 octets. Enregistrez un message vide pour l’effacer.",
  "artifactDetail.severity": "Gravité",
  "artifactDetail.vulnerability": "Vulnérabilité",
  "artifactDetail.affectedComponent": "Composant touché",
  "artifactDetail.version": "Version",
  "artifactDetail.fixedIn": "Corrigée dans",
  "artifactDetail.none": "Aucune",
  "artifactDetail.action": "Action",
  "artifactDetail.view": "Afficher",
  "artifactDetail.allSeverities": "Toutes les gravités",
  "artifactDetail.vulnerabilityFindings": "Résultats de vulnérabilités",
  "artifactDetail.searchVulnerabilities": "Rechercher des vulnérabilités",
  "artifactDetail.searchIDComponentOrLocation":
    "Rechercher un ID, un composant ou un emplacement…",
  "artifactDetail.filterBySeverity": "Filtrer par gravité",
  "artifactDetail.noMatchingVulnerabilityFindings":
    "Aucun résultat de vulnérabilité correspondant",
  "artifactDetail.iD": "ID",
  "artifactDetail.source": "Source",
  "artifactDetail.affectedVersion": "Version touchée",
  "artifactDetail.fixedVersion": "Version corrigée",
  "artifactDetail.noKnownFix": "Aucun correctif connu",
  "artifactDetail.location": "Emplacement",
  "artifactDetail.description": "Description",
  "artifactDetail.openAdvisory": "Ouvrir l’avis",
  "artifactDetail.failedToLoadArtifactIntelligence":
    "Échec du chargement de l’intelligence de l’artefact",
  "artifactDetail.affected": "Touché",
  "artifactDetail.clean": "Sain",
  "artifactDetail.scanFailed": "Échec de l’analyse",
  "artifactDetail.notScanned": "Non analysé",
  "artifactDetail.noKnownVulnerabilities": "Aucune vulnérabilité connue",
  "artifactDetail.scanDidNotCompleteCheckTheJobDetails":
    "L’analyse ne s’est pas terminée; consultez les détails de la tâche",
  "artifactDetail.noScanResultYet": "Aucun résultat d’analyse pour l’instant",
  "artifactDetail.artifactIntelligence": "Intelligence de l’artefact",
  "artifactDetail.signatures": "Signatures",
  "artifactDetail.recordS": "{length} enregistrement(s)",
  "artifactDetail.noneLinked": "Aucun lié",
  "artifactDetail.documentS": "{length} document(s)",
  "artifactDetail.provenance": "Provenance",
  "artifactDetail.vulnerabilities": "Vulnérabilités",
  "artifactDetail.licenses": "Licences",
  "artifactDetail.updated": "Mis à jour",
  "artifactDetail.failedToLoadScanStatus":
    "Échec du chargement de l’état de l’analyse",
  "artifactDetail.failedToQueueARescan":
    "Échec de la mise en file d’une nouvelle analyse",
  "artifactDetail.scanStatus": "État de l’analyse",
  "artifactDetail.attempt": "Tentative {attempts}/{maxAttempts}",
  "artifactDetail.rescan": "Réanalyser",
  "artifactDetail.artifactQuarantined": "Artefact en quarantaine",
  "artifactDetail.promotionAndReplicationAreBlockedOrdinaryReadsAre":
    "La promotion et la réplication sont bloquées; les lectures ordinaires ne sont pas touchées.",
  "artifactDetail.release": "Libérer",
  "artifactDetail.notQuarantined": "Pas en quarantaine",
  "artifactDetail.artifactNotQuarantined": "Artefact pas en quarantaine",
  "artifactDetail.lastReleased": "Dernière libération",
  "artifactDetail.quarantine": "Mettre en quarantaine",
  "artifactDetail.quarantineArtifact": "Mettre l’artefact en quarantaine",
  "artifactDetail.releaseArtifact": "Libérer l’artefact",
  "artifactDetail.cancel": "Annuler",
  "artifactDetail.quarantine2": "Mettre en quarantaine",
  "artifactDetail.release2": "Libérer",
  "artifactDetail.quarantineBlocksPromotionAndReplicationForThisArtifact":
    "La quarantaine bloque la promotion et la réplication de cet artefact, mais ne bloque pas les lectures ordinaires.",
  "artifactDetail.releasePermitsPromotionAndReplicationAgainOtherSecurity":
    "La libération permet de nouveau la promotion et la réplication; les autres règles d’admission de sécurité s’appliquent toujours.",
  "artifactDetail.quarantineReason": "Motif de la quarantaine",
  "artifactDetail.releaseReason": "Motif de la libération",
  "artifactDetail.enterAReasonForTheAuditTrail":
    "Saisissez un motif pour la piste d’audit",
  "artifactDetail.imageSize": "Taille de l’image",
  "artifactDetail.noLayerData": "Aucune donnée de couche",
  "artifactDetail.manifestMetadataOnly": "Métadonnées du manifest seulement",
  "artifactDetail.layers": "Couches",
  "artifactDetail.architectureOS": "Architecture / SE",
  "artifactDetail.created": "Créée",
  "artifactDetail.runtimeConfiguration": "Configuration d’exécution",
  "artifactDetail.layers2": "Couches",
  "artifactDetail.type": "Type",
  "artifactDetail.size": "Taille",
  "artifactDetail.loadingImageDetails": "Chargement des détails de l’image…",
  "artifactDetail.thisImageHasNoVisibleManifests":
    "Cette image n’a aucun manifest visible",
  "artifactDetail.searchTagsOrDigests": "Rechercher des tags ou des digests",
  "artifactDetail.unlinkThisImageTag": "Dissocier ce tag d’image?",
  "artifactDetail.deleteThisUntaggedManifest":
    "Supprimer ce manifest sans tag?",
  "artifactDetail.onlyTheTagIsRemovedTheManifestRemains":
    "Seul le tag est retiré. Le manifest reste gérable par digest quand il n’existe aucun autre tag.",
  "artifactDetail.theManifestMovesToTombstonesAndCanBe":
    "Le manifest passe aux pierres tombales et peut y être restauré.",
  "artifactDetail.delete": "Supprimer",
  "artifactDetail.unlinkTag": "Dissocier le tag",
  "artifactDetail.deleteManifest": "Supprimer le manifest",
  "artifactDetail.usage": "Utilisation",
  "artifactDetail.useTheSelectedTagOrDigestToPull":
    "Utilisez le tag ou le digest sélectionné pour tirer cette image.",
  "artifactDetail.loadingManifest": "Chargement du manifest…",
  "artifactDetail.publisher": "Éditeur",
  "artifactDetail.published": "Publié",
  "artifactDetail.contentType": "Type de contenu",
  "artifactDetail.state": "État",
  "artifactDetail.digest": "Digest",
  "artifactDetail.usage2": "Utilisation",
  "artifactDetail.searchAndSelectAVersion":
    "Recherchez et sélectionnez une version",
  "artifactDetail.noMatchingVersions": "Aucune version correspondante",
  "artifactDetail.deleteFailed": "Échec de la suppression",
  "artifactDetail.currentVersion": "Version actuelle",
  "artifactDetail.deleteThisMavenVersion": "Supprimer cette version Maven?",
  "artifactDetail.thisVersionAndItsRecoverableReferencesWillNo":
    "Cette version et ses références récupérables ne seront plus visibles.",
  "artifactDetail.deleteCurrentVersion": "Supprimer la version actuelle",
  "artifactDetail.releaseVersions": "Versions finales",
  "artifactDetail.snapshotBuilds": "Builds snapshot",
  "artifactDetail.failedToLoadNpmPackage":
    "Échec du chargement du paquet npm ({status})",
  "artifactDetail.failedToLoadNpmPackage2": "Échec du chargement du paquet npm",
  "artifactDetail.selectVersion": "Sélectionner la version",
  "artifactDetail.searchNpmVersions": "Rechercher des versions npm",
  "artifactDetail.metadataOnly": "Métadonnées seulement",
  "artifactDetail.cached": "En cache",
  "artifactDetail.notRecorded": "Non enregistré",
  "artifactDetail.packageSize": "Taille du paquet",
  "artifactDetail.availableAfterDownload": "Disponible après le téléchargement",
  "artifactDetail.dependenciesLicense": "Dépendances / licence",
  "artifactDetail.computedAfterDownload": "Calculé après le téléchargement",
  "artifactDetail.thisVersionWasDiscoveredUpstreamTheFirstInstall":
    "Cette version a été découverte en amont. La première installation vérifie son tarball et le met en cache.",
  "artifactDetail.artifactCached": "Artefact mis en cache{cachedAt}",
  "artifactDetail.failedToLoadPyPIProject":
    "Échec du chargement du projet PyPI ({status})",
  "artifactDetail.failedToLoadPyPIProject2":
    "Échec du chargement du projet PyPI",
  "artifactDetail.distributionFile": "Fichier de distribution",
  "artifactDetail.python": "Python",
  "artifactDetail.afterDownload": "Après le téléchargement",
  "artifactDetail.searchPyPIVersions": "Rechercher des versions PyPI",
  "artifactDetail.cached2": "{cachedCount}/{length} en cache",
  "artifactDetail.distributionFiles": "Fichiers de distribution",
  "artifactDetail.totalSize": "Taille totale",
  "artifactDetail.failedToLoadGoModuleVersions":
    "Échec du chargement des versions du module Go ({status})",
  "artifactDetail.failedToLoadGoVersions":
    "Échec du chargement des versions Go",
  "artifactDetail.failedToLoadVersionInfo":
    "Échec du chargement des informations de version ({status})",
  "artifactDetail.failedToLoadVersionInfo2":
    "Échec du chargement des informations de version",
  "artifactDetail.selectModuleVersion": "Sélectionner la version du module",
  "artifactDetail.searchVersions": "Rechercher des versions",
  "artifactDetail.module": "Module",
  "artifactDetail.source2": "Source",
  "artifactDetail.upstreamProxy": "Proxy en amont",
  "artifactDetail.knownSize": "Taille connue",
  "artifactDetail.protocolAssetsCachedOnFirstAccess":
    "Ressources du protocole (mises en cache au premier accès)",
  "artifactDetail.copy": "Copier",
  "artifactDetail.download": "Télécharger",
  "artifactDetail.failedToLoadCargoVersions":
    "Échec du chargement des versions Cargo ({status})",
  "artifactDetail.failedToLoadCargoVersions2":
    "Échec du chargement des versions Cargo",
  "artifactDetail.downloadFailed": "Échec du téléchargement",
  "artifactDetail.selectCrateVersion": "Sélectionner la version du crate",
  "artifactDetail.indexStatus": "État de l’index",
  "artifactDetail.available": "Disponible",
  "artifactDetail.linkCopied": "Lien copié",
  "artifactDetail.copyDownloadLink": "Copier le lien de téléchargement",
  "artifactDetail.downloadCrate": "Télécharger le .crate",
  "artifactDetail.failedToLoadConanRecipeRevisions":
    "Échec du chargement des révisions de recette Conan",
  "artifactDetail.failedToLoadConanPackageRevisions":
    "Échec du chargement des révisions de paquet Conan",
  "artifactDetail.failedToDeleteConanPackageRevision":
    "Échec de la suppression de la révision de paquet Conan",
  "artifactDetail.recipeRevisions": "Révisions de recette",
  "artifactDetail.packageRevisions": "Révisions de paquet",
  "artifactDetail.viewRecipe": "Afficher la recette",
  "artifactDetail.inspect": "Inspecter",
  "artifactDetail.deleteThisPackageRevision":
    "Supprimer cette révision de paquet?",
  "artifactDetail.thisBinaryPackageRevisionWillNoLongerBe":
    "Cette révision de paquet binaire ne sera plus visible.",
  "artifactDetail.failedToDeleteRawFile":
    "Échec de la suppression du fichier Raw",
  "artifactDetail.rawFile": "Fichier Raw",
  "artifactDetail.deleteThisRawFile": "Supprimer ce fichier Raw?",
  "artifactDetail.thisPathWillNoLongerBeVisible":
    "Ce chemin ne sera plus visible.",
  "artifactDetail.deleteFile": "Supprimer le fichier",
  "artifactDetail.packageObject": "Objet de paquet",
  "artifactDetail.byHashRepositoryMetadata": "Métadonnées de dépôt par hash",
  "artifactDetail.repositoryMetadata": "Métadonnées du dépôt",
  "artifactDetail.published2": "Publié",
  "artifactDetail.firstCached": "Première mise en cache",
  "artifactDetail.aPTAssetType": "Type de ressource APT",
  "artifactDetail.lastCached": "Dernière mise en cache",
  "artifactDetail.upstreamURL": "URL en amont",
  "artifactDetail.affected2": "Touché",
  "artifactDetail.clean2": "Sain",
  "artifactDetail.scanError": "Erreur d’analyse",
  "artifactDetail.evidence": "preuve",
  "artifactDetail.failedToReadOciManifestList":
    "Échec de la lecture de la liste de manifests OCI",
  "artifactDetail.ociManifestPageCursorRepeated":
    "Le curseur de page des manifests OCI s’est répété",
  "artifactDetail.failedToReadMavenArtifacts":
    "Échec de la lecture des artefacts Maven",
  "artifactDetail.noDeletableMavenArtifact":
    "Aucun ID d’artefact supprimable n’a été trouvé pour cette version Maven",
  "artifactDetail.failedToDeleteMavenArtifact":
    "Échec de la suppression de l’artefact Maven",
  "artifactDetail.untagged": "Sans tag",
  "artifactDetail.untaggedDigest": "Sans tag · {digest}",
};
