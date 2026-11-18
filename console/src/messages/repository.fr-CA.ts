import type { repositoryEnCA } from "./repository.en-CA";

export const repositoryFrCA: Record<keyof typeof repositoryEnCA, string> = {
  "repository.aptFirstTitle": "Publier le premier snapshot signé",
  "repository.aptFirstManagementAfterPublish":
    "Après la première publication, gérez ici les paquets visibles.",
  "repository.aptFirstScope":
    "Le premier snapshot contient uniquement ce paquet. Préparez-le, vérifiez son identité, puis publiez-le explicitement avant que les clients APT puissent le lire.",
  "repository.aptFirstHostedOnly":
    "La première publication exige un dépôt APT hébergé",
  "repository.aptFirstFile": "Paquet Debian (.deb)",
  "repository.aptFirstComponentHint":
    "Par exemple, main. Cette valeur reste fixe après la création de la session de préparation.",
  "repository.aptFirstInvalidComponent":
    "Utilisez des lettres minuscules, des chiffres et + . -, en commençant par une lettre ou un chiffre, jusqu’à 128 caractères.",
  "repository.aptFirstInvalidFile":
    "Choisissez un seul fichier .deb non vide, sans chemin dans son nom, d’au plus 255 caractères et de 1 Gio maximum.",
  "repository.aptFirstHashUnavailable":
    "Le SHA-256 du fichier n’a pas pu être calculé. Sélectionnez de nouveau le fichier sur une page HTTPS ou localhost.",
  "repository.aptFirstHashing": "Calcul du SHA-256 du fichier…",
  "repository.aptFirstExactSize": "{size} octets",
  "repository.aptFirstStage": "Préparer et examiner le paquet",
  "repository.aptFirstRetryStage": "Réessayer la préparation et l’examen",
  "repository.aptFirstSession": "Session de préparation\u00a0: ",
  "repository.aptFirstReviewTitle":
    "Préparé\u00a0: examinez le paquet identifié par le serveur",
  "repository.aptFirstPackage": "Nom du paquet",
  "repository.aptFirstArchitecture": "Architecture",
  "repository.aptFirstIdentity": "Identité du paquet",
  "repository.aptFirstReviewed":
    "J’ai vérifié le nom, la version et l’architecture du paquet",
  "repository.aptFirstPublish": "Confirmer et signer le premier snapshot",
  "repository.aptFirstRetryPublish": "Réessayer la première publication",
  "repository.aptFirstRetryHint":
    "Votre sélection et tout enregistrement de préparation reçu sont conservés. Une nouvelle tentative réutilise la même requête.",
  "repository.aptFirstSessionMismatch":
    "La session de préparation ne correspond pas à ce fichier ni à cette cible. Le téléversement n’a pas été poursuivi.",
  "repository.aptFirstRevisionMismatch":
    "L’identité du paquet côté serveur est incomplète, ou son digest ou sa taille diffère de ce fichier. La publication n’a pas été activée.",
  "repository.aptFirstSnapshotMismatch":
    "Aucun premier snapshot visible n’a été reçu pour ce dépôt et cette suite. Conservez cette requête et réessayez.",
  "repository.aptFirstPublishedRefresh":
    "La publication est confirmée. Si le snapshot courant ne s’est pas mis à jour, utilisez Actualiser les snapshots ci-dessus.",
  "repository.aptFirstDiscardTitle":
    "Effacer cette sélection et cet enregistrement de nouvelle tentative?",
  "repository.aptFirstDiscardHint":
    "Les données de préparation téléversées expirent sur le serveur et ne seront pas publiées automatiquement.",
  "repository.aptFirstDiscard": "Effacer cette sélection",
  "repository.copied": "Copié",
  "repository.copy": "Copier",
  "repository.publishWithDockerOrPodman": "Publier avec Docker ou Podman",
  "repository.askAnAdministratorForAServiceAccountCredential":
    "Demandez à un administrateur un justificatif d’identité de compte de service avec accès en écriture à ce dépôt, transmis par un canal sécurisé. La session de navigateur SSO n’est pas un justificatif Docker.",
  "repository.signInToRegistry": "Se connecter au registre",
  "repository.tagAndPush": "Étiqueter et pousser",
  "repository.configureNpmRegistry": "Configurer le registre npm",
  "repository.useAGatewayAPIKeyOrResolverToken":
    "Utilisez une clé d’API Gateway ou un token de résolution pour l’authentification.",
  "repository.publish": "Publier",
  "repository.configurePyPIRepository": "Configurer le dépôt PyPI",
  "repository.anonymousPipReadsNeedNoCredentialsTwineUses":
    "Les lectures pip anonymes n’exigent aucun justificatif; twine accepte tout nom d’utilisateur non vide avec le token de résolution.",
  "repository.configureCargoRegistry": "Configurer le registre Cargo",
  "repository.askAnAdministratorForRepositoryWriteAccessFor":
    "Demandez à un administrateur l’accès en écriture au dépôt. Pour les lectures privées et la publication, injectez CARGO_REGISTRIES_NOTARX_TOKEN sous la forme Bearer suivi du token Gateway au moyen d’un gestionnaire de secrets; ne l’inscrivez jamais dans le fichier de configuration. Les autorisations du dépôt gèrent l’accès; cargo owner n’est pas pris en charge.",
  "repository.publishAndRead": "Publier et lire",
  "repository.yankAndRestoreAVersion": "Retirer et rétablir une version (yank)",
  "repository.fileName": "Nom du fichier",
  "repository.size": "Taille",
  "repository.actions": "Actions",
  "repository.remove": "Retirer",
  "repository.status": "Statut",
  "repository.uploaded": "Téléversé",
  "repository.pending": "En attente",
  "repository.declareCoordinateAndObjects":
    "Déclarer la coordonnée et les objets",
  "repository.uploadFiles": "Téléverser les fichiers",
  "repository.complete": "Terminé",
  "repository.mavenCoordinate": "Coordonnée Maven",
  "repository.groupArtifactVersionForExampleComAcmeWidget":
    "groupe:artefact:version, par exemple com.acme:widget:1.0.0",
  "repository.pOMObjectName": "Nom de l’objet POM",
  "repository.theStagedFileUsedAsThePOM":
    "Le fichier préparé utilisé comme POM",
  "repository.selectAPOMFileFirst": "Sélectionnez d’abord un fichier POM",
  "repository.releaseFiles": "Fichiers de la version",
  "repository.selectThePOMJARSourceArchiveAndOther":
    "Sélectionnez le POM, le JAR, l’archive des sources et les autres fichiers; le SHA-256 est calculé dans le navigateur",
  "repository.chooseReleaseFiles": "Choisir les fichiers de la version",
  "repository.createPublishSession": "Créer une session de publication",
  "repository.session": "Session",
  "repository.expires": "Expire\u00a0: ",
  "repository.allFilesUploaded": "Tous les fichiers sont téléversés",
  "repository.uploadAllFiles": "Téléverser tous les fichiers",
  "repository.commitRelease": "Valider la version",
  "repository.discardThisRelease": "Abandonner cette version?",
  "repository.localSelectionsAndSessionDetailsWillBeCleared":
    "Les sélections locales et les détails de la session seront effacés. Les objets temporaires téléversés expirent sur le serveur.",
  "repository.discardRelease": "Abandonner la version",
  "repository.continueEditing": "Continuer la modification",
  "repository.discardAndStartOver": "Abandonner et recommencer",
  "repository.releasePublished": "Version publiée",
  "repository.publishAnother": "Publier une autre version",
  "repository.isUnavailable": "{feature} n’est pas disponible",
  "repository.thisConsoleDoesNotSupportTheFormatYet":
    "Cette Console ne prend pas encore en charge le format {unsupportedFormat}",
  "repository.theCurrentBackendDoesNotExposeThisEndpoint":
    "Le serveur actuel n’expose pas ce point de terminaison (404)",
  "repository.newRepository": "Nouveau dépôt",
  "repository.cancel": "Annuler",
  "repository.create": "Créer",
  "repository.type": "Type",
  "repository.hosted": "Hébergé",
  "repository.proxy": "Proxy",
  "repository.hostAndPublishArtifacts": "Héberger et publier des artefacts",
  "repository.fetchAndCacheFromUpstream":
    "Récupérer et mettre en cache depuis l’amont",
  "repository.repositoryName": "Nom du dépôt",
  "repository.lowercaseLettersNumbersAndHyphensForExampleTeam":
    "Lettres minuscules, chiffres et traits d’union, par exemple team-images",
  "repository.format": "Format",
  "repository.strictPublication": "Publication stricte",
  "repository.offByDefaultSuccessfulStandardMavenGradleUploads":
    "Désactivée par défaut\u00a0: les téléversements Maven/Gradle standard réussis sont lisibles immédiatement, pour la migration compatible Nexus. Lorsqu’elle est activée, une coordonnée reste masquée tant que sa validation Gateway n’a pas réussi.",
  "repository.upstreamEndpoint": "Point de terminaison amont",
  "repository.externalProxySource": "Source de proxy externe",
  "repository.commaSeparatedHostnamesRequiredForRawConanNpm":
    "Noms d’hôte séparés par des virgules; obligatoires pour les proxys Raw, Conan, npm, PyPI, Go, Cargo et APT. PyPI exige normalement aussi files.pythonhosted.org; Cargo exige normalement aussi static.crates.io.",
  "repository.name": "Nom",
  "repository.capacity": "Capacité",
  "repository.deleting": "Suppression en cours",
  "repository.repositories": "Dépôts",
  "repository.aUnifiedViewOfHostedAndProxyRepositories":
    "Une vue unifiée des dépôts hébergés et mandataires",
  "repository.repositories2": "Dépôts",
  "repository.archived": "{deletedCount} archivés",
  "repository.active": "{activeCount} actifs",
  "repository.proxyRepositories": "Dépôts mandataires",
  "repository.upstreamCachesAndMirrors": "Caches et miroirs amont",
  "repository.storageUsed": "Stockage utilisé",
  "repository.objects": "{number} objets",
  "repository.capacityUnavailable": "Capacité non disponible",
  "repository.clearFilters": "Effacer les filtres",
  "repository.search": "Rechercher",
  "repository.nameFormatOrRepositoryType": "Nom, format ou type de dépôt…",
  "repository.allFormats": "Tous les formats",
  "repository.activeAndDeleting": "Actifs et en suppression",
  "repository.allStatuses": "Tous les statuts",
  "repository.active2": "Actif",
  "repository.deleted": "Supprimé",
  "repository.noRepositories": "Aucun dépôt",
  "repository.noMatchingRepositories": "Aucun dépôt correspondant",
  "repository.aRepositoryDefinesAFormatAndPolicyBoundary":
    "Un dépôt définit une frontière de format et de politique pour la publication, le mandat (proxy) et la gouvernance des artefacts.",
  "repository.youHaveNoRepositoryGrantsYetAPlatform":
    "Vous n’avez encore aucune autorisation sur un dépôt. Un administrateur de la plateforme attribue l’accès dépôt par dépôt; demandez-lui de vous en accorder une.",
  "repository.deletedRepositoriesAreArchivedAndAvailableThroughThe":
    "Les dépôts supprimés sont archivés et accessibles au moyen du filtre de statut",
  "repository.adjustTheFiltersAndTryAgain": "Ajustez les filtres et réessayez",
  "repository.noMatchingRepositories2": "Aucun dépôt correspondant",
  "repository.adjustTheFiltersOrLoadMoreRepositories":
    "Ajustez les filtres ou chargez d’autres dépôts",
  "repository.deleteRepository": "Supprimer le dépôt",
  "repository.deleteRepository2": "Supprimer le dépôt",
  "repository.readsAndWritesStopImmediatelyWhileTheRepository":
    "? Les lectures et les écritures s’arrêtent immédiatement pendant que le dépôt passe à l’état de suppression. Le nettoyage en arrière-plan se termine normalement en moins d’une minute. Cette action est irréversible; les métadonnées d’audit sont conservées.",
  "repository.delete2": "Supprimer",
  "repository.aFormatNamespaceContainingAccessPolicyArtifactsAnd":
    "Un namespace de format contenant la politique d’accès, les artefacts et la configuration amont.",
  "repository.fetchesAndCachesUpstreamResponsesOnDemandPublishing":
    "Récupère et met en cache les réponses amont à la demande; la publication est désactivée.",
  "repository.storesVerifiedPublishedArtifactsAndSupportsDeletionRestore":
    "Stocke les artefacts publiés vérifiés et prend en charge la suppression, la restauration et la conservation.",
  "repository.aUserVisibleLogicalIdentitySuchAsA":
    "Une identité logique visible par l’utilisateur, comme une coordonnée Maven ou une image OCI.",
  "repository.anImmutableFileOrBlobUnderAnArtifact":
    "Un fichier ou blob immuable sous un artefact, comme un JAR, un POM ou une couche d’image.",
  "repository.anIndexAndBytesForAnUpstreamResponse":
    "Un index et des octets pour une réponse amont; ce n’est pas un artefact hébergé.",
  "repository.turnsCompleteValidatedStagedContentIntoAVisible":
    "Transforme un contenu préparé complet et validé en artefact visible.",
  "repository.aRestorableDeletionRecordBytesAreReclaimedAfter":
    "Un enregistrement de suppression restaurable; les octets sont récupérés une fois les références disparues.",
  "repository.selectsExpiredVersionsOrPathsByFormatRules":
    "Sélectionne les versions ou chemins expirés selon les règles du format et crée une tâche de récupération vérifiable.",
  "repository.concepts": "Concepts",
  "repository.viewConcepts": "Voir les concepts",
  "repository.repositorySummary": "Résumé du dépôt",
  "repository.anonymousReads": "Lectures anonymes",
  "repository.privateReads": "Lectures privées",
  "repository.protocolEndpoint": "Point de terminaison du protocole",
  "repository.openPublicBrowser": "Ouvrir le navigateur public",
  "repository.objects2": "{bytes} · {number} objets",
  "repository.viewCapacity": "Voir la capacité",
  "repository.effectiveAccess": "Accès effectif",
  "repository.ociTokenExchange": "Échange de token OCI",
  "repository.ociTokenBindingHint":
    "Liez un service de token et une audience pour les défis Bearer du registre. Cela n’accorde aucun accès réseau; autorisez l’hôte émetteur séparément. Modifier la liaison exige un nouveau secret amont ou l’effacement du justificatif d’identité stocké.",
  "repository.ociTokenServiceURL": "URL du service de token",
  "repository.ociTokenServiceURLHint":
    "Le realm HTTPS exact du défi amont. Sans nom d’utilisateur, mot de passe, requête ni fragment.",
  "repository.ociTokenAudience": "Audience du token",
  "repository.ociTokenAudienceHint":
    "Le service exact du défi amont, sans espaces ni caractères de contrôle.",
  "repository.upstreamCredentialToBoundIssuer":
    "Envoyez ce justificatif d’identité uniquement au service de token lié. Le contenu du registre utilise le token de téléchargement échangé; les redirections entre origines retirent les justificatifs d’identité.",
  "repository.upstreamCredentialRebindRequired":
    "Le point de terminaison amont ou la liaison du token OCI a changé. Saisissez un nouveau secret, ou choisissez Anonyme pour effacer le justificatif d’identité stocké, puis enregistrez de nouveau.",
  "repository.upstreamAuthentication": "Authentification amont",
  "repository.attachACredentialToUpstreamFetchesWhenThe":
    "S’authentifier auprès de l’amont configuré. Les justificatifs d’identité sont envoyés uniquement à la source liée; les redirections entre origines les retirent.",
  "repository.anonymous": "Anonyme",
  "repository.sendNoCredentialAndClearTheStoredOne":
    "N’envoyer aucun justificatif d’identité et effacer celui qui est stocké",
  "repository.sendNoCredential": "N’envoyer aucun justificatif d’identité",
  "repository.basic": "Basic",
  "repository.sendAnHTTPBasicUsernameAndPassword":
    "Utiliser un nom d’utilisateur et un mot de passe amont",
  "repository.bearerToken": "Token Bearer",
  "repository.sendAuthorizationBearerToken":
    "Envoyer Authorization: Bearer <token>",
  "repository.upstreamUsername": "Nom d’utilisateur amont",
  "repository.upstreamPassword": "Mot de passe amont",
  "repository.upstreamToken": "Token amont",
  "repository.storedEncryptedWithAES256GCMLeaveBlank":
    "En écriture seule et chiffré au repos. Chaque enregistrement efface ce champ. Laissez-le vide pour conserver le justificatif d’identité seulement si sa liaison et son schéma sont inchangés; sinon, saisissez-le de nouveau.",
  "repository.configuredEnterAValueToReplaceIt":
    "Configuré; saisissez une valeur pour le remplacer",
  "repository.notConfigured": "Non configuré",
  "repository.upstreamURL": "URL amont",
  "repository.hTTPSBaseURLChangesTakeEffectImmediatelyOn":
    "URL de base HTTPS. La modifier exige un nouveau secret amont ou l’effacement du justificatif d’identité stocké. Les modifications enregistrées s’appliquent à la requête suivante.",
  "repository.allowedHosts": "Hôtes autorisés",
  "repository.commaSeparatedRequiredForRawConanNpmPyPI":
    "Séparés par des virgules. Obligatoires pour les proxys Raw, Conan, npm, PyPI, Go, Cargo et APT. Cargo exige normalement index.crates.io et static.crates.io.",
  "repository.commaSeparatedOptionalForOCIAndMavenProxies":
    "Séparés par des virgules. Facultatifs pour les proxys OCI et Maven.",
  "repository.egressProxy": "Proxy de sortie",
  "repository.configureTheEgressProxyUsedWhenThisProxy":
    "Configurez le proxy de sortie utilisé lorsque ce dépôt mandataire joint son amont, pour les réseaux privés ou restreints.",
  "repository.direct": "Direct",
  "repository.doNotUseAProxyRetainPrivateAddress":
    "Ne pas utiliser de proxy; conserver la protection des adresses privées",
  "repository.useEnvironmentVariables":
    "Utiliser les variables d’environnement",
  "repository.useProcessLevelHTTPSPROXYAndNO":
    "Utiliser HTTP(S)_PROXY et NO_PROXY au niveau du processus",
  "repository.customProxy": "Proxy personnalisé",
  "repository.setAnHTTPOrSOCKS5ProxySpecificallyFor":
    "Définir un proxy HTTP ou SOCKS5 propre à ce dépôt",
  "repository.protocol": "Protocole",
  "repository.hTTPCONNECT": "HTTP (CONNECT)",
  "repository.proxyHost": "Hôte du proxy",
  "repository.port": "Port",
  "repository.remoteDNSSocks5h": "DNS distant (socks5h)",
  "repository.whenEnabledTheProxyResolvesTheUpstreamHostname":
    "Lorsque cette option est activée, le proxy résout le nom d’hôte amont. Utilisez-la lorsque le DNS local ne peut pas joindre le réseau amont.",
  "repository.proxyUsernameOptional": "Nom d’utilisateur du proxy (facultatif)",
  "repository.proxyPasswordOptional": "Mot de passe du proxy (facultatif)",
  "repository.storedEncryptedWithAES256GCMLeaveBlank2":
    "En écriture seule et chiffré au repos. Chaque enregistrement efface ce champ. Laissez-le vide pour conserver le justificatif d’identité actuel.",
  "repository.clearStoredProxyCredentials":
    "Effacer les justificatifs d’identité du proxy stockés",
  "repository.bypassListNoProxy": "Liste de contournement (noProxy)",
  "repository.commaSeparatedHostnameSuffixesOrCIDRsMatchingUpstreams":
    "Suffixes de noms d’hôte ou CIDR séparés par des virgules. Les amonts correspondants contournent le proxy.",
  "repository.testConnection": "Tester la connexion",
  "repository.theTestUsesTheSavedConfiguration":
    "Le test utilise la configuration enregistrée",
  "repository.proxyReachable": "Proxy joignable",
  "repository.upstreamReturned": "l’amont a répondu {upstreamStatus}",
  "repository.latencyMs": "latence de {latencyMs} ms",
  "repository.connectionFailed": "Échec de la connexion\u00a0: ",
  "repository.unknownError": "Erreur inconnue",
  "repository.repositorySettingsSaved": "Paramètres du dépôt enregistrés",
  "repository.repositorySettings": "Paramètres du dépôt",
  "repository.manageReadAccessAndUpstreamConnectivityForProxy":
    "Gérez l’accès en lecture et la connectivité amont des dépôts mandataires. Le nom, le format et le type du dépôt ne peuvent pas être modifiés après la création.",
  "repository.reset": "Réinitialiser",
  "repository.saveChanges": "Enregistrer les modifications",
  "repository.allowAnonymousReads": "Autoriser les lectures anonymes",
  "repository.whenEnabledProtocolGETHEADRequestsCanRead":
    "Lorsque cette option est activée, les requêtes GET/HEAD du protocole peuvent lire ce dépôt sans justificatif d’identité.",
  "repository.whenOffNexusStyleDirectVisibilityLetsStandard":
    "Lorsqu’elle est désactivée, la visibilité directe de style Nexus permet aux clients Maven/Gradle standard de publier sans intégration supplémentaire. Lorsqu’elle est activée, les téléversements restent masqués tant que la validation de la coordonnée Gateway n’a pas réussi. Ne modifiez pas cette politique pendant une publication en cours.",
  "repository.supportedOperations": "Opérations prises en charge",
  "repository.invalidTargetPathEmptyDotDotDotBackslash":
    "Chemin cible non valide\u00a0: les chemins vides, les points, les doubles points, les barres obliques inverses, les caractères de contrôle et les caractères de mise en forme bidirectionnelle ne sont pas autorisés. Unicode, les espaces et les parenthèses sont pris en charge.",
  "repository.theTargetPathContainsCharactersThatCannotBe":
    "Le chemin cible contient des caractères qui ne peuvent pas être encodés. Modifiez le nom du fichier ou le chemin cible et réessayez.",
  "repository.theEncodedTargetPathCannotExceed4096Bytes":
    "Le chemin cible encodé ne peut pas dépasser 4096 octets. Raccourcissez les répertoires ou le nom du fichier.",
  "repository.uploadFailed400TheTargetDoesNotFollow":
    "Échec du téléversement (400)\u00a0: la cible ne respecte pas les règles de chemin Raw. Unicode, les espaces et les parenthèses sont pris en charge; retirez les segments vides, les points, les doubles points, les barres obliques inverses ou les NUL, puis réessayez.",
  "repository.uploadFailed": "Échec du téléversement",
  "repository.upload": "Téléverser",
  "repository.uploadTo": "Téléverser vers",
  "repository.rawFileUploadFailed": "Échec du téléversement du fichier Raw",
  "repository.file": "Fichier",
  "repository.chooseFile": "Choisir un fichier",
  "repository.noFileSelected": "Aucun fichier sélectionné",
  "repository.targetPath": "Chemin cible",
  "repository.relativeToTheRepositoryRootOneOptionalLeading":
    "Relatif à la racine du dépôt. Une barre oblique initiale facultative peut représenter explicitement la racine du dépôt et est retirée au téléversement. Laissez vide pour utiliser le nom du fichier. Unicode, les espaces et les parenthèses sont pris en charge jusqu’à 4096 octets encodés; les chemins vides, les points, les doubles points, les barres obliques inverses, les caractères de contrôle et les caractères de mise en forme bidirectionnelle ne le sont pas.",
  "repository.theServerValidatesSHA256AnExistingPath":
    "Le serveur valide le SHA-256. Un chemin existant portant le même nom sera écrasé.",
  "repository.artifactBrowseView": "Vue de navigation des artefacts",
  "repository.directory": "Répertoire",
  "repository.list": "Liste",
  "repository.namespace": "Namespace",
  "repository.component": "Composant",
  "repository.version": "Version",
  "repository.asset": "Ressource",
  "repository.nodeDetails": "Détails du nœud",
  "repository.openInList": "Ouvrir dans la liste",
  "repository.canonicalLocation": "Emplacement canonique",
  "repository.localSources": "Sources locales",
  "repository.orderedByVisibleMemberPriorityNotAnActual":
    "Classées par priorité des membres visibles, et non selon un résultat de téléchargement réel.",
  "repository.differentContentAtThisPath": "Contenu différent à ce chemin",
  "repository.sourceRepository": "Dépôt source",
  "repository.cacheState": "État du cache",
  "repository.cached": "En cache",
  "repository.buildNumber": "Numéro de build",
  "repository.contentType": "Type de contenu",
  "repository.cacheUpdated": "Cache mis à jour",
  "repository.updated": "Mis à jour",
  "repository.assetPath": "Chemin de la ressource",
  "repository.selectAnItemInTheDirectory":
    "Sélectionnez un élément dans le répertoire",
  "repository.inspectItsFullNameSourcesAndVerificationDetails":
    "Consultez ici son nom complet, ses sources et les détails de vérification.",
  "repository.arrowKeysToBrowseEnterToSelect":
    "Flèches pour naviguer · Entrée pour sélectionner",
  "repository.published": "Publié",
  "repository.cacheScope": "Portée du cache",
  "repository.multipleLocalSources": "Plusieurs sources locales",
  "repository.loadMore": "Charger plus",
  "repository.failedToLoadDirectory": "Échec du chargement du répertoire",
  "repository.loadingDirectory": "Chargement du répertoire…",
  "repository.noBrowseableArtifacts": "Aucun artefact navigable",
  "repository.readableMembersHaveNoPublishedArtifactsOrLive":
    "Les membres lisibles n’ont encore aucun artefact publié ni entrée de cache active.",
  "repository.proxyDirectoriesShowOnlyFetchedAndRecordedCache":
    "Les répertoires proxy n’affichent que les ressources de cache récupérées et enregistrées.",
  "repository.publishMavenArtifactsOrUploadRawFilesTo":
    "Publiez des artefacts Maven ou téléversez des fichiers Raw pour alimenter le répertoire adapté au format.",
  "repository.repositoryDirectory": "Répertoire du dépôt",
  "repository.artifactDirectory": "Répertoire des artefacts",
  "repository.memberPublicationsAndKnownCache":
    "Publications des membres et cache connu",
  "repository.cachedAssetsOnly": "Ressources en cache seulement",
  "repository.exploreArtifactsByDirectory":
    "Explorer les artefacts par répertoire",
  "repository.collapseAllDirectories": "Réduire tous les répertoires",
  "repository.refresh": "Actualiser",
  "repository.artifactDirectoryTree":
    "Arborescence du répertoire des artefacts",
  "repository.entriesLoaded": "{loadedNodeCount} entrées chargées",
  "repository.expandDirectoriesToLoadMore":
    "Développez les répertoires pour en charger davantage",
  "repository.enterAnInvalidationTarget": "Saisissez une cible d’invalidation.",
  "repository.failedToInvalidateCache": "Échec de l’invalidation du cache",
  "repository.enterAMavenGAVOrCachePath":
    "Saisissez un GAV Maven ou un chemin de cache.",
  "repository.failedToClearNegativeCache":
    "Échec de l’effacement du cache négatif",
  "repository.invalidateCache": "Invalider le cache",
  "repository.path": "Chemin",
  "repository.repository": "Dépôt",
  "repository.byPrefix": "Par préfixe",
  "repository.invalidate": "Invalider",
  "repository.clearNegativeCache": "Effacer le cache négatif",
  "repository.versionComponentAndRepositoryInvalidationsUseTheirMaven":
    "Les invalidations de version, de composant et de dépôt utilisent leurs préfixes de cache Maven. Seuls les index de cache sont supprimés; les objets d’octets sont récupérés plus tard par le collecteur d’orphelins.",
  "repository.cacheEntriesInvalidated":
    "{invalidateResult} entrées de cache invalidées.",
  "repository.negativeCacheEntriesCleared":
    "{negativeResult} entrées de cache négatif effacées.",
  "repository.enterAMavenGAVGroupIdArtifactIdVersionExtension":
    "Saisissez un GAV Maven (groupId:artifactId:version[:extension[:classifier]]) ou un chemin de dépôt.",
  "repository.cacheWarmUpRequestFailed":
    "Échec de la requête de préchauffage du cache",
  "repository.failedToRefreshCache": "Échec de l’actualisation du cache",
  "repository.warmCache": "Préchauffer le cache",
  "repository.warm": "Préchauffer",
  "repository.forceRefresh": "Forcer l’actualisation",
  "repository.checkUpstream": "Vérifier l’amont",
  "repository.primarySize": "Taille principale",
  "repository.files": "Fichiers",
  "repository.member": "Membre",
  "repository.primaryFiles": "Fichiers principaux\u00a0: ",
  "repository.mavenCoordinateUsage": "Utilisation de la coordonnée Maven",
  "repository.expand": "(développer)",
  "repository.fileDetails": "Détails du fichier",
  "repository.checksumsSignatures":
    "Sommes de contrôle / signatures ({length})",
  "repository.failedToLoadUpstreamStatus":
    "Échec du chargement de l’état de l’amont",
  "repository.upstream": "Amont",
  "repository.checking": "Vérification…",
  "repository.health": "Santé",
  "repository.reachable": "Joignable",
  "repository.unreachable": "Injoignable",
  "repository.cache": "Cache",
  "repository.enabled": "Activé",
  "repository.disabled": "Désactivé",
  "repository.usage": "Utilisation",
  "repository.theMavenProxyRequiresBasicAuthenticationUseAny":
    "Le proxy Maven exige l’authentification Basic\u00a0: utilisez n’importe quel nom d’utilisateur non vide et le token de résolution comme mot de passe.",
  "repository.directDownload": "Téléchargement direct",
  "repository.security": "Sécurité",
  "repository.files2": "Fichiers",
  "repository.files3": "{count} fichiers",
  "repository.aPTPath": "Chemin APT",
  "repository.digest": "Digest",
  "repository.firstCached": "Première mise en cache",
  "repository.versions": "Versions",
  "repository.versions2": "{versionCount} versions",
  "repository.openToInspect": "Ouvrir pour inspecter",
  "repository.latestVersion": "Dernière version",
  "repository.filePath": "Chemin du fichier",
  "repository.lastUpdated": "Dernière mise à jour",
  "repository.noMatchingArtifacts": "Aucun artefact correspondant",
  "repository.noArtifacts": "Aucun artefact",
  "repository.tryADifferentSearchTerm": "Essayez un autre terme de recherche",
  "repository.failedToLoadProxyCache": "Échec du chargement du cache du proxy",
  "repository.artifactBrowser": "Navigateur d’artefacts",
  "repository.search2": "Rechercher…",
  "repository.primaryAssets": "Ressources principales",
  "repository.allFiles": "Tous les fichiers",
  "repository.jAROnly": "JAR seulement",
  "repository.pOMOnly": "POM seulement",
  "repository.mavenVersionsShowing":
    "{number} versions Maven, {number2} affichées",
  "repository.returnToFullList": "Retourner à la liste complète",
  "repository.pageWithMavenVersionsPerPage":
    "Page {number} avec {number2} versions Maven par page",
  "repository.quotaUpdated": "Quota mis à jour",
  "repository.capacityManagement": "Gestion de la capacité",
  "repository.cacheUsage": "Utilisation du cache",
  "repository.usedSpace": "Espace utilisé",
  "repository.cachedObjects": "Objets en cache",
  "repository.objectCount": "Nombre d’objets",
  "repository.quota": "Quota",
  "repository.unlimited": "Illimité",
  "repository.utilization": "Taux d’utilisation",
  "repository.quotaGiB0ForUnlimited": "Quota (Gio, 0 pour illimité)",
  "repository.updateQuota": "Mettre à jour le quota",
  "repository.artifactAddress": "Adresse de l’artefact",
  "repository.downloads": "Téléchargements",
  "repository.totalBytes": "Total d’octets",
  "repository.firstDownload": "Premier téléchargement",
  "repository.lastDownload": "Dernier téléchargement",
  "repository.lastDownloadedBy": "Dernier téléchargement par",
  "repository.searchArtifactAddress": "Rechercher une adresse d’artefact",
  "repository.searchAddressCaseSensitive":
    "Rechercher une adresse (sensible à la casse)",
  "repository.wholeRepositoryAddressesDownloads":
    "Dépôt entier\u00a0: {number} adresses · {number2} téléchargements · {bytes}",
  "repository.noMatchingArtifactAddresses":
    "Aucune adresse d’artefact correspondante",
  "repository.noDownloadsRecordedYet":
    "Aucun téléchargement enregistré pour l’instant",
  "repository.tryADifferentAddressSearch":
    "Essayez une autre recherche d’adresse.",
  "repository.noDownloadsFromThisRepositoryHaveBeenRecorded":
    "Aucun téléchargement de ce dépôt n’a encore été enregistré.",
  "repository.searchByPackagePathOrCoordinate":
    "Rechercher par paquet, chemin ou coordonnée",
  "repository.searching": "Recherche…",
  "repository.noSelectableArtifacts": "Aucun artefact sélectionnable",
  "repository.artifactSearchFailedChangeTheQueryAndTry":
    "La recherche d’artefacts a échoué. Modifiez la requête et réessayez.",
  "repository.artifactIsQuarantinedReleaseItFirst":
    "L’artefact est en quarantaine; libérez-le d’abord",
  "repository.policyIsDisabled": "La politique est désactivée",
  "repository.signatureIsRequired": "Une signature est requise",
  "repository.aVerifiedSignatureIsRequired":
    "Une signature vérifiée est requise",
  "repository.anSBOMIsRequired": "Un SBOM est requis",
  "repository.provenanceIsRequired": "Une provenance est requise",
  "repository.aVulnerabilityScanIsRequired":
    "Une analyse de vulnérabilités est requise",
  "repository.licenseInformationIsRequired":
    "Les informations de licence sont requises",
  "repository.aLicenseIsNotAllowListed":
    "Une licence ne figure pas dans la liste autorisée",
  "repository.theVulnerabilityScanFailed":
    "L’analyse de vulnérabilités a échoué",
  "repository.lowVulnerabilitiesFound": "Vulnérabilités faibles détectées",
  "repository.mediumVulnerabilitiesFound": "Vulnérabilités moyennes détectées",
  "repository.highVulnerabilitiesFound": "Vulnérabilités élevées détectées",
  "repository.criticalVulnerabilitiesFound":
    "Vulnérabilités critiques détectées",
  "repository.unknownSeverityVulnerabilitiesFound":
    "Vulnérabilités de gravité inconnue détectées",
  "repository.artifactIsQuarantinedAndCannotBePromotedOr":
    "L’artefact est en quarantaine et ne peut être ni promu ni répliqué",
  "repository.securityPolicyAllowsPromotion":
    "La politique de sécurité autorise la promotion",
  "repository.securityPolicyBlocksPromotion":
    "La politique de sécurité bloque la promotion",
  "repository.releaseTheArtifactFromItsDetailsThenEvaluate":
    "Libérez l’artefact depuis ses détails, puis évaluez de nouveau l’admission.",
  "repository.policyVersion": "Version de la politique",
  "repository.enforcement": "Application",
  "repository.enabled2": "activée",
  "repository.disabled2": "désactivée",
  "repository.artifactIntelligence": "Renseignements sur l’artefact",
  "repository.found": "trouvé",
  "repository.notFound": "introuvable",
  "repository.promotionSubmittedTrackItOnTheTargetRepository":
    "Promotion soumise. Suivez-la dans l’onglet Tâches de cycle de vie du dépôt cible.",
  "repository.replicationPlanCreatedTrackItsProgressBelow":
    "Plan de réplication créé. Suivez sa progression ci-dessous.",
  "repository.startPromotionReplication": "Lancer une promotion / réplication",
  "repository.selectAnImmutableArtifactAndATargetRepository":
    "Sélectionnez un artefact immuable et un dépôt cible du même format. La promotion publie une copie après l’admission par la cible; la réplication crée un plan reprenable sans approbation de sécurité.",
  "repository.sourceArtifact": "Artefact source",
  "repository.visiblePackagesInTheCurrentSuiteTypeA":
    "Paquets visibles dans la suite courante; saisissez un nom de paquet pour filtrer.",
  "repository.upTo50ItemsAreShownTypeA":
    "Jusqu’à 50 éléments sont affichés. Saisissez un préfixe de paquet, de chemin ou de coordonnée pour restreindre les résultats.",
  "repository.searchAndSelectASourceArtifact":
    "Rechercher et sélectionner un artefact source",
  "repository.selectAPackageInThisSuite":
    "Sélectionnez un paquet dans cette suite",
  "repository.targetHostedRepository": "Dépôt hébergé cible",
  "repository.selectTargetRepository": "Sélectionner le dépôt cible",
  "repository.selectARepositoryWithTheSameFormat":
    "Sélectionnez un dépôt du même format…",
  "repository.noCompatibleHostedRepositoryIsAvailable":
    "Aucun dépôt hébergé compatible n’est disponible",
  "repository.targetSuite": "Suite cible",
  "repository.theTargetRepositorySignsANewSnapshotWith":
    "Le dépôt cible signe un nouveau snapshot avec son propre signataire et conserve les membres existants.",
  "repository.hideAdvancedManualInput": "Masquer la saisie manuelle avancée",
  "repository.advancedManualInput": "Saisie manuelle avancée",
  "repository.artifactCoordinate": "Coordonnée de l’artefact",
  "repository.immutableVersionIdentityUsedByLifecycleOperations":
    "Identité de version immuable utilisée par les opérations de cycle de vie {format}",
  "repository.digest2": "Digest",
  "repository.evaluateAdmission": "Évaluer l’admission",
  "repository.promote": "Promouvoir",
  "repository.replicate": "Répliquer",
  "repository.targetSuite2": "Suite cible",
  "repository.targetRepository": "Dépôt cible",
  "repository.created": "Créé",
  "repository.completed": "Terminé",
  "repository.progress": "Progression",
  "repository.cancelReplicationPlan": "Annuler le plan de réplication?",
  "repository.workersWillNotRetryAfterCancellationBytesAlready":
    "Les workers ne réessaieront pas après l’annulation. Les octets déjà copiés ne sont pas supprimés automatiquement.",
  "repository.cancelPlan": "Annuler le plan",
  "repository.back": "Retour",
  "repository.object": "Objet",
  "repository.attempts": "Tentatives",
  "repository.replicationPlanCanceledWorkersWillNotRetryIt":
    "Plan de réplication annulé. Les workers ne le réessaieront pas.",
  "repository.replicationPlans": "Plans de réplication ({count})",
  "repository.noReplicationPlans": "Aucun plan de réplication",
  "repository.replicationProgress": "Progression de la réplication",
  "repository.status2": "Statut\u00a0: ",
  "repository.target": "Cible\u00a0: ",
  "repository.created2": "Créé\u00a0: ",
  "repository.noCheckpoints": "Aucun checkpoint",
  "repository.requeuedIntelligenceSyncJobS":
    "{count} tâche(s) de synchronisation des renseignements remise(s) en file",
  "repository.inspectedArtifactsQueuedRetried":
    "{inspected} artefacts inspectés, {enqueued} mis en file, {retried} réessayés",
  "repository.reconcileScans": "Réconcilier les analyses",
  "repository.lifecycleJobs": "Tâches de cycle de vie",
  "repository.noLifecycleJobs": "Aucune tâche de cycle de vie",
  "repository.retentionCleanupPromotionAndReplicationTasksAppearHere":
    "Les tâches de nettoyage de conservation, de promotion et de réplication apparaissent ici.",
  "repository.kind": "Genre",
  "repository.error": "Erreur",
  "repository.reconciliationFillsPublicationScanGapsAndRetriesFailed":
    "La réconciliation comble les lacunes d’analyse de publication et réessaie les tâches en échec.",
  "repository.intelligenceSyncJobSNeedReconciliation":
    "{length} tâche(s) de synchronisation des renseignements à réconcilier",
  "repository.requeueFailedIntelligenceSyncs":
    "Remettre en file les synchronisations de renseignements en échec?",
  "repository.onlyFailedOrCancelledSyncJobsAreReset":
    "Seules les tâches de synchronisation en échec ou annulées sont réinitialisées; le contenu des artefacts reste inchangé.",
  "repository.requeue": "Remettre en file",
  "repository.reconcileIntelligence": "Réconcilier les renseignements",
  "repository.restored": "{coordinate} restauré",
  "repository.tombstoneManagement": "Gestion des pierres tombales",
  "repository.coordinate": "Coordonnée",
  "repository.deleted2": "Supprimé",
  "repository.restoreThisArtifact": "Restaurer cet artefact?",
  "repository.afterRestorationTheArtifactIsAvailableAgainIn":
    "Après la restauration, l’artefact est de nouveau disponible dans la navigation du dépôt et les lectures de protocole.",
  "repository.restore": "Restaurer",
  "repository.noTombstones": "Aucune pierre tombale",
  "repository.deletedArtifactsRetainTombstoneRecordsAndCanBe":
    "Les artefacts supprimés conservent des enregistrements de pierre tombale et peuvent être restaurés ici.",
  "repository.mavenReleaseRetentionDays":
    "Jours de conservation des versions Maven",
  "repository.aReleaseBecomesEligibleAfterThisManyDays":
    "Une version devient admissible après ce nombre de jours. Les builds SNAPSHOT utilisent la période distincte ci-dessous.",
  "repository.minimumVersionsPerMavenModule":
    "Nombre minimal de versions par module Maven",
  "repository.groupByGroupIdArtifactIdAndAlwaysProtectThe":
    "Regroupe par groupId:artifactId et protège toujours les versions les plus récentes.",
  "repository.maximumVersionsPerMavenModule":
    "Nombre maximal de versions par module Maven",
  "repository.use0ForNoLimitOlderReleasesOr":
    "Utilisez 0 pour aucune limite. Les versions plus anciennes ou les builds snapshot au-delà de la limite deviennent admissibles.",
  "repository.onlyCleanMatchingMavenCoordinates":
    "Nettoyer seulement les coordonnées Maven correspondantes",
  "repository.optionalRE2CoordinateRegexLeaveEmptyToMatch":
    "Expression régulière RE2 facultative pour les coordonnées. Laissez vide pour correspondre à toutes les coordonnées Maven.",
  "repository.protectMavenCoordinates": "Protéger des coordonnées Maven",
  "repository.coordinatesMatchingAnyRegexNeverBecomeEligible":
    "Les coordonnées correspondant à une expression régulière ne deviennent jamais admissibles.",
  "repository.eGComExample": "p. ex. ^com\\.example:",
  "repository.eGComExamplePlatform": "p. ex. ^com\\.example:platform:",
  "repository.mavenArtifactVersions": "Versions d’artefacts Maven",
  "repository.mavenArtifactVersion": "Version d’artefact Maven",
  "repository.imageVersionRetentionDays":
    "Jours de conservation des versions d’image",
  "repository.aManifestBecomesEligibleAfterThisManyDays":
    "Un manifest devient admissible après ce nombre de jours.",
  "repository.minimumVersionsPerImage": "Nombre minimal de versions par image",
  "repository.groupByImageNameAndAlwaysProtectThese":
    "Regroupe par nom d’image et protège toujours ces manifests les plus récents.",
  "repository.maximumVersionsPerImage": "Nombre maximal de versions par image",
  "repository.use0ForNoLimitOlderManifestsBeyond":
    "Utilisez 0 pour aucune limite. Les manifests plus anciens au-delà de la limite deviennent admissibles.",
  "repository.onlyCleanMatchingImages":
    "Nettoyer seulement les images correspondantes",
  "repository.matchesImageNameNameDigestOrNameTag":
    "Correspond au nom d’image, à nom@digest ou à nom:tag. Laissez vide pour toutes les images.",
  "repository.protectImageVersions": "Protéger des versions d’image",
  "repository.useAnImageNameToProtectAllVersions":
    "Utilisez un nom d’image pour protéger toutes les versions, ou un digest ou un tag pour une version exacte.",
  "repository.eGTeamBackend": "p. ex. ^team/backend(@|:)",
  "repository.eGTeamBackendStable": "p. ex. ^team/backend:stable$",
  "repository.imageVersions": "versions d’image",
  "repository.manifestVersion": "Version du manifest",
  "repository.recipeRevisionRetentionDays":
    "Jours de conservation des révisions de recette",
  "repository.aRecipeRevisionBecomesEligibleAfterThisMany":
    "Une révision de recette devient admissible après ce nombre de jours.",
  "repository.minimumVersionsPerReference":
    "Nombre minimal de versions par référence",
  "repository.groupByFullConanReferenceAndProtectThe":
    "Regroupe par référence Conan complète et protège les révisions de recette les plus récentes.",
  "repository.maximumVersionsPerReference":
    "Nombre maximal de versions par référence",
  "repository.use0ForNoLimitCleaningARecipe":
    "Utilisez 0 pour aucune limite. Le nettoyage d’une révision de recette masque aussi ses paquets binaires.",
  "repository.onlyCleanMatchingReferences":
    "Nettoyer seulement les références correspondantes",
  "repository.matchesAFullReferenceOrReferenceRecipeRevision":
    "Correspond à une référence complète ou à référence#révision-de-recette.",
  "repository.protectConanVersions": "Protéger des versions Conan",
  "repository.aMatchingReferenceProtectsAllRevisionsAnExact":
    "Une référence correspondante protège toutes les révisions; une coordonnée exacte protège une seule version.",
  "repository.eGOpenssl3": "p. ex. ^openssl/3\\.",
  "repository.eGReleaseStable": "p. ex. @release/stable(#|$)",
  "repository.assetInactivityRetentionDays":
    "Jours de conservation en cas d’inactivité des ressources",
  "repository.aPathAssetBecomesEligibleAfterItHas":
    "Une ressource de chemin devient admissible lorsqu’elle n’a pas été mise à jour depuis ce nombre de jours.",
  "repository.onlyCleanMatchingPaths":
    "Nettoyer seulement les chemins correspondants",
  "repository.optionalRE2PathRegexLeaveEmptyToMatch":
    "Expression régulière RE2 facultative pour les chemins. Laissez vide pour correspondre à toutes les ressources du dépôt.",
  "repository.protectPaths": "Protéger des chemins",
  "repository.pathsMatchingAnyRegexNeverBecomeEligible":
    "Les chemins correspondant à une expression régulière ne deviennent jamais admissibles.",
  "repository.eGReleasesNightly": "p. ex. ^releases/nightly/",
  "repository.eGReleasesStable": "p. ex. ^releases/stable/",
  "repository.pathAssets": "ressources de chemin",
  "repository.pathAsset": "Ressource de chemin",
  "repository.npmPackageVersionRetentionDays":
    "Jours de conservation des versions de paquets npm",
  "repository.aPackageVersionBecomesEligibleAfterThisMany":
    "Une version de paquet devient admissible après ce nombre de jours.",
  "repository.minimumVersionsPerNpmPackage":
    "Nombre minimal de versions par paquet npm",
  "repository.groupByPackageNameAndAlwaysProtectThese":
    "Regroupe par nom de paquet et protège toujours ces versions publiées le plus récemment.",
  "repository.maximumVersionsPerNpmPackage":
    "Nombre maximal de versions par paquet npm",
  "repository.use0ForNoLimitEarlierPublicationsBeyond":
    "Utilisez 0 pour aucune limite. Les publications antérieures au-delà de la limite deviennent admissibles.",
  "repository.onlyCleanMatchingNpmPackages":
    "Nettoyer seulement les paquets npm correspondants",
  "repository.matchesAPackageNameOrPackageVersionLeave":
    "Correspond à un nom de paquet ou à paquet@version. Laissez vide pour tous les paquets npm.",
  "repository.protectNpmPackageVersions":
    "Protéger des versions de paquets npm",
  "repository.matchAPackageNameToProtectEveryVersion":
    "Indiquez un nom de paquet pour protéger toutes les versions, ou paquet@version pour une seule version.",
  "repository.eGTeam": "p. ex. ^@team/",
  "repository.eGTeamPlatform": "p. ex. ^@team/platform(@|$)",
  "repository.npmPackageVersions": "Versions de paquets npm",
  "repository.npmPackageVersion": "Version de paquet npm",
  "repository.pyPIProjectVersionRetentionDays":
    "Jours de conservation des versions de projets PyPI",
  "repository.aProjectVersionBecomesEligibleAfterThisMany":
    "Une version de projet devient admissible après ce nombre de jours.",
  "repository.minimumVersionsPerPyPIProject":
    "Nombre minimal de versions par projet PyPI",
  "repository.groupByNormalizedProjectNameAndAlwaysProtect":
    "Regroupe par nom de projet normalisé et protège toujours ces versions publiées le plus récemment.",
  "repository.maximumVersionsPerPyPIProject":
    "Nombre maximal de versions par projet PyPI",
  "repository.use0ForNoLimitEarlierPublicationsBeyond2":
    "Utilisez 0 pour aucune limite. Les publications antérieures au-delà de la limite deviennent admissibles avec tous les fichiers de cette version.",
  "repository.onlyCleanMatchingPyPIProjects":
    "Nettoyer seulement les projets PyPI correspondants",
  "repository.matchesAProjectNameOrProjectVersionLeave":
    "Correspond à un nom de projet ou à projet@version. Laissez vide pour tous les projets PyPI.",
  "repository.protectPyPIProjectVersions":
    "Protéger des versions de projets PyPI",
  "repository.matchAProjectNameToProtectEveryVersion":
    "Indiquez un nom de projet pour protéger toutes les versions, ou projet@version pour une seule version.",
  "repository.eGInternal": "p. ex. ^internal-",
  "repository.eGPlatformCore": "p. ex. ^platform-core(@|$)",
  "repository.pyPIProjectVersions": "Versions de projets PyPI",
  "repository.pyPIProjectVersion": "Version de projet PyPI",
  "repository.goModuleVersionRetentionDays":
    "Jours de conservation des versions de modules Go",
  "repository.aModuleVersionBecomesEligibleAfterThisMany":
    "Une version de module devient admissible après ce nombre de jours.",
  "repository.minimumVersionsPerGoModule":
    "Nombre minimal de versions par module Go",
  "repository.groupByModulePathAndAlwaysProtectThese":
    "Regroupe par chemin de module et protège toujours ces versions publiées le plus récemment.",
  "repository.maximumVersionsPerGoModule":
    "Nombre maximal de versions par module Go",
  "repository.onlyCleanMatchingGoModules":
    "Nettoyer seulement les modules Go correspondants",
  "repository.matchesAModulePathOrModuleVersionLeave":
    "Correspond à un chemin de module ou à module@version. Laissez vide pour tous les modules Go.",
  "repository.protectGoModuleVersions": "Protéger des versions de modules Go",
  "repository.matchAModulePathToProtectEveryVersion":
    "Indiquez un chemin de module pour protéger toutes les versions, ou module@version pour une seule version.",
  "repository.eGExampleComTeam": "p. ex. ^example\\.com/team/",
  "repository.eGExampleComTeamWidget": "p. ex. ^example\\.com/team/widget(@|$)",
  "repository.goModuleVersions": "Versions de modules Go",
  "repository.goModuleVersion": "Version de module Go",
  "repository.artifactRetentionDays": "Jours de conservation des artefacts",
  "repository.anArtifactBecomesEligibleAfterThisManyDays":
    "Un artefact devient admissible après ce nombre de jours.",
  "repository.minimumVersionsPerGroup": "Nombre minimal de versions par groupe",
  "repository.alwaysProtectTheseNewestVersionsInEachGroup":
    "Protège toujours ces versions les plus récentes dans chaque groupe.",
  "repository.maximumVersionsPerGroup": "Nombre maximal de versions par groupe",
  "repository.use0ForNoLimitOlderVersionsBeyond":
    "Utilisez 0 pour aucune limite. Les versions plus anciennes au-delà de la limite deviennent admissibles.",
  "repository.onlyCleanMatchingArtifacts":
    "Nettoyer seulement les artefacts correspondants",
  "repository.optionalRE2RegexLeaveEmptyToMatchAll":
    "Expression régulière RE2 facultative. Laissez vide pour correspondre à tous les artefacts.",
  "repository.protectArtifacts": "Protéger des artefacts",
  "repository.artifactsMatchingAnyRegexNeverBecomeEligible":
    "Les artefacts correspondant à une expression régulière ne deviennent jamais admissibles.",
  "repository.eGTeam2": "p. ex. ^team/",
  "repository.eGStable": "p. ex. /stable$",
  "repository.artifacts": "artefacts",
  "repository.artifactVersion": "Version d’artefact",
  "repository.snapshotBuild": "Build snapshot",
  "repository.releaseVersion": "Version de livraison",
  "repository.cleanupUnit": "Unité de nettoyage",
  "repository.reason": "Motif",
  "repository.exceededVersionLimit": "Limite de versions dépassée",
  "repository.notUpdatedForDays": "Non mis à jour depuis {ageDays} jours",
  "repository.retainedForDays": "Conservé depuis {ageDays} jours",
  "repository.downloadEvidence": "Preuve de téléchargement",
  "repository.last": "{downloadCount} · dernier {date}",
  "repository.neverDownloaded": "Jamais téléchargé",
  "repository.dryRunResultsLoadedOfCandidatePolicyVersion":
    "Résultats de la simulation\u00a0: {length} sur {totalCandidates} {candidateName} candidat(s) chargé(s) (version de la politique {policyVersion})",
  "repository.exportTheCompleteCandidateSetIndependentOfThe":
    "Exporter l’ensemble complet des candidats, indépendamment de la page courante",
  "repository.exportCSV": "Exporter en CSV",
  "repository.byAge": "Par ancienneté",
  "repository.types": "Types\u00a0: ",
  "repository.release": "Version de livraison",
  "repository.snapshot": "Snapshot",
  "repository.none": "Aucun",
  "repository.oldestCandidate": "Candidat le plus ancien",
  "repository.noToClean": "Aucun {candidateName} à nettoyer",
  "repository.loadMoreCandidates": "Charger plus de candidats",
  "repository.theDryRunResultExpiredOrThePolicy":
    "Le résultat de la simulation a expiré ou la politique a changé. Relancez-la.",
  "repository.theRetentionPolicyChangedSoThisPreviewIs":
    "La politique de conservation a changé; cet aperçu n’est donc plus valide. Relancez-le.",
  "repository.theRetentionTaskWasSubmittedTrackItOn":
    "La tâche de conservation a été soumise. Suivez-la dans l’onglet Tâches de cycle de vie.",
  "repository.failedToExportDryRunResults":
    "Échec de l’exportation des résultats de la simulation",
  "repository.maximumVersionsMustBe0OrGreaterThan":
    "Le nombre maximal de versions doit être 0 ou supérieur ou égal au nombre minimal de versions.",
  "repository.policySaved": "Politique enregistrée",
  "repository.rawCleanupByPathAsset": "Nettoyage Raw par ressource de chemin",
  "repository.rawAssetsAreNotVersionGroupedSoMinimum":
    "Les ressources Raw ne sont pas regroupées par version; le nombre minimal et le nombre maximal de versions ne s’appliquent donc pas. L’ancienneté est calculée à partir de la dernière mise à jour.",
  "repository.automaticCleanup": "Nettoyage automatique",
  "repository.whenDisabledNoScheduledOrManualCleanupTask":
    "Lorsqu’il est désactivé, aucune tâche de nettoyage planifiée ou manuelle n’est créée. Les pierres tombales existantes ne sont pas touchées.",
  "repository.days": "jours",
  "repository.snapshotRetentionDays": "Jours de conservation des snapshots",
  "repository.mavenSNAPSHOTVersionsCanUseARetentionPeriod":
    "Les versions Maven SNAPSHOT peuvent utiliser une période de conservation distincte de celle des versions de livraison.",
  "repository.items": "éléments",
  "repository.downloadProtectionDays":
    "Jours de protection après téléchargement",
  "repository.artifactsDownloadedWithinTheWindowSkipCleanupEven":
    "Les artefacts téléchargés dans cette fenêtre échappent au nettoyage même s’ils sont expirés; 0 désactive cette protection. Basé sur l’utilisation des téléchargements du cycle de vie.",
  "repository.savePolicy": "Enregistrer la politique",
  "repository.dryRun": "Simulation",
  "repository.runRetentionCleanup": "Exécuter le nettoyage de conservation?",
  "repository.thisWillCleanAllCandidateThePolicyVersion":
    "Cette opération nettoiera tous les {totalCandidates} {candidateName} candidat(s). La version de la politique est vérifiée de nouveau avant l’exécution.",
  "repository.runCleanup": "Exécuter le nettoyage",
  "repository.runCleanup2": "Exécuter le nettoyage ({totalCandidates})",
  "repository.retentionPolicy": "Politique de conservation",
  "repository.hostedRepositoryRetention": "Conservation du dépôt hébergé",
  "repository.artifact": "Artefact",
  "repository.unknownArtifact": "Artefact inconnu",
  "repository.lastError": "Dernière erreur",
  "repository.recentScanJobs": "Tâches d’analyse récentes",
  "repository.reconcileHistoricalArtifacts":
    "Réconcilier les artefacts historiques",
  "repository.showsScansAmongTheLatest100LifecycleJobs":
    "Affiche les analyses parmi les 100 dernières tâches de cycle de vie; ce n’est pas l’historique complet des analyses.",
  "repository.recentJobsAreVisibleToRepositoryAdministrators":
    "Les tâches récentes sont visibles par les administrateurs du dépôt",
  "repository.aSuccessfulSubmissionStillReturnsItsJobID":
    "Une soumission réussie renvoie tout de même l’ID de sa tâche ci-dessus.",
  "repository.noScansInRecentJobs": "Aucune analyse dans les tâches récentes",
  "repository.checksTheLatest100LifecycleJobsOnlyJobs":
    "Vérifie seulement les 100 dernières tâches de cycle de vie. Les tâches apparaissent ici après une soumission manuelle ou une réconciliation historique.",
  "repository.scanCompleted": "Analyse terminée",
  "repository.scanning": "Analyse en cours",
  "repository.scanFailed": "Échec de l’analyse",
  "repository.scanned": "Analysé",
  "repository.affected": "Touché",
  "repository.scanError": "Erreur d’analyse",
  "repository.hasEvidence": "Avec preuves",
  "repository.notScanned": "Non analysé",
  "repository.scanJobQueued": "Tâche d’analyse mise en file",
  "repository.jobID": "ID de la tâche\u00a0: ",
  "repository.selectAndScanAnImmutableArtifact":
    "Sélectionner et analyser un artefact immuable",
  "repository.searchAndSelectAnArtifact":
    "Rechercher et sélectionner un artefact",
  "repository.selectionLocksTheCanonicalCoordinateAndFullDigest":
    "La sélection verrouille la coordonnée canonique et le digest complet. Jusqu’à 50 correspondances sont affichées; recherchez les versions historiques et les révisions Conan par mot-clé. N’utilisez la saisie manuelle avancée que comme moyen de reprise.",
  "repository.enterAnArtifactCoordinate": "Saisissez une coordonnée d’artefact",
  "repository.artifactCoordinateIsTooLong":
    "La coordonnée de l’artefact est trop longue",
  "repository.canonicalArtifactCoordinate":
    "Coordonnée canonique de l’artefact",
  "repository.sHA256Digest": "Digest SHA-256",
  "repository.enterASHA256Digest": "Saisissez un digest SHA-256",
  "repository.enterSha256FollowedBy64LowercaseHexadecimalCharacters":
    "Saisissez sha256: suivi de 64 caractères hexadécimaux minuscules",
  "repository.advancedModeRequiresAnExactCoordinateAndDigest":
    "Le mode avancé exige une coordonnée et un digest qui correspondent exactement.",
  "repository.theScanOnlyReadsTheSelectedArtifactAnd":
    "L’analyse lit uniquement l’artefact sélectionné et ne récupère ni ne modifie jamais le contenu amont.",
  "repository.queueScan": "Mettre l’analyse en file",
  "repository.checkingScanCapability": "Vérification de la capacité d’analyse…",
  "repository.artifactScanning": "Analyse des artefacts",
  "repository.scanningWritesSBOMLicenseAndVulnerabilityResultsTo":
    "L’analyse écrit les résultats SBOM, de licence et de vulnérabilités dans les renseignements sur l’artefact. Les échecs n’annulent pas les téléversements, et les résultats ne mettent pas automatiquement les artefacts en quarantaine ni ne bloquent les lectures.",
  "repository.manualScan": "Analyse manuelle",
  "repository.available": "Disponible",
  "repository.unavailable": "Non disponible",
  "repository.scanOnPublishCapability": "Capacité d’analyse à la publication",
  "repository.noScannerIsConfiguredForThisRepository":
    "Aucun analyseur n’est configuré pour ce dépôt",
  "repository.askAPlatformAdministratorToConfigure":
    "Demandez à un administrateur de la plateforme de configurer ",
  "repository.and": " et ",
  "repository.inTheGatewayDeploymentAndEnableScanJobs":
    " dans le déploiement Gateway et d’activer les tâches d’analyse sur les nœuds worker. Le point de terminaison et le token ne sont pas exposés dans la Console.",
  "repository.thisIdentityCannotManageArtifactScans":
    "Cette identité ne peut pas gérer les analyses d’artefacts",
  "repository.queueingScansAndHistoricalReconciliationRequireRepositoriesIntelligence":
    "La mise en file d’analyses et la réconciliation historique exigent repositories:intelligence.",
  "repository.aPTPublicationTrust": "Confiance de publication APT",
  "repository.signingTrustAndCurrentSnapshot":
    "Confiance de signature et snapshot courant",
  "repository.verifyTheSigningKeysCurrentlyTrustedByGateway":
    "Vérifiez les clés de signature actuellement approuvées par Gateway, la fenêtre de rotation et les preuves immuables de la dernière publication atomique. Les points de terminaison du signataire, les tokens et le matériel de clé privée ne sont jamais affichés ici.",
  "repository.publicationRootOfTrust": "Racine de confiance de publication",
  "repository.aPTReleaseSigning": "Signature de Release APT",
  "repository.remoteModeRequiresPinnedPublicKeyFingerprintsTwo":
    "Le mode distant exige des empreintes de clés publiques épinglées. Deux empreintes représentent une fenêtre de chevauchement contrôlée entre l’ancienne et la nouvelle clé.",
  "repository.signerMode": "Mode du signataire",
  "repository.trustedKeyCount": "Nombre de clés approuvées",
  "repository.currentKeyRole": "Rôle de la clé actuelle",
  "repository.latestPublication": "Dernière publication",
  "repository.trustedFingerprintWindow": "Fenêtre des empreintes approuvées",
  "repository.theFirstFingerprintIsPrimaryAndTheOptional":
    "La première empreinte est la principale et la seconde, facultative, est la prochaine clé acceptée pendant la rotation. La distribution des clés aux clients reste une action explicite de l’opérateur.",
  "repository.noRemoteTrustedFingerprintsConfigured":
    "Aucune empreinte approuvée distante n’est configurée.",
  "repository.primary": "Principale",
  "repository.next": "Suivante",
  "repository.inUse": "En cours d’utilisation",
  "repository.latestImmutableEvidence": "Dernières preuves immuables",
  "repository.theseValuesComeFromTheCommittedVisibleSnapshot":
    "Ces valeurs proviennent du snapshot visible validé, et non de l’état que le signataire déclare actuellement.",
  "repository.suiteSequence": "Suite / séquence",
  "repository.publishedAt": "Publié le",
  "repository.signerIdentity": "Identité du signataire",
  "repository.signatureAlgorithm": "Algorithme de signature",
  "repository.keyFingerprint": "Empreinte de la clé",
  "repository.releaseDigest": "Digest de Release",
  "repository.thisRepositoryHasNoVisibleSnapshotYet":
    "Ce dépôt n’a encore aucun snapshot visible.",
  "repository.productionTrustPinned": "Confiance de production épinglée",
  "repository.keyRotationOverlap": "Chevauchement de rotation des clés",
  "repository.bothOldAndNewKeysAreAcceptedConfirm":
    "L’ancienne et la nouvelle clés sont toutes deux acceptées. Confirmez qu’un nouveau snapshot utilise la clé suivante, puis retirez l’ancienne clé seulement après la fermeture de la fenêtre de confiance des clients.",
  "repository.referenceSignerOnly": "Signataire de référence seulement",
  "repository.theLocalReferenceSignerIsSuitableOnlyFor":
    "Le signataire de référence local ne convient qu’à la validation H2 et ne doit pas devenir une racine de confiance de production.",
  "repository.trustPolicyMismatch": "Politique de confiance non conforme",
  "repository.theLatestVisibleSnapshotUsesAKeyOutside":
    "Le dernier snapshot visible utilise une clé extérieure à la politique de confiance active, ou la configuration de confiance distante est incomplète. Corrigez la politique avant de publier.",
  "repository.notConfigured2": "Non configuré",
  "repository.noUsableSignerIsConfiguredSoNewAPT":
    "Aucun signataire utilisable n’est configuré; de nouveaux snapshots APT ne peuvent donc pas être publiés.",
  "repository.unknownState": "État inconnu",
  "repository.remotePinnedTrust": "Confiance distante épinglée",
  "repository.localReferenceSigner": "Signataire de référence local",
  "repository.disabled3": "Désactivé",
  "repository.activeTrustKey": "Clé de confiance active",
  "repository.fixture": "Fixture",
  "repository.outsidePolicy": "Hors politique",
  "repository.pathAndChecksumSidecars":
    "Chemin et fichiers annexes de somme de contrôle",
  "repository.hiddenFromDirectoryListings":
    "Masqué dans les listes de répertoires",
  "repository.assetsWithinTheCoordinate": "Ressources de la coordonnée",
  "repository.hiddenFromGeneratedMetadata":
    "Masqué dans les métadonnées générées",
  "repository.manifestAndDescriptorClosure":
    "Fermeture du manifest et des descripteurs",
  "repository.hiddenFromTagsCatalogAndReferrers":
    "Masqué dans les tags, le catalogue et les referrers",
  "repository.hiddenFromPackumentsAndDistTags":
    "Masqué dans les packuments et les dist-tags",
  "repository.hiddenFromSimpleMetadata": "Masqué dans les métadonnées Simple",
  "repository.recipeRevisionClosure": "Fermeture des révisions de recette",
  "repository.hiddenFromRevisionMetadata":
    "Masqué dans les métadonnées de révision",
  "repository.everyModuleVersionRepresentation":
    "Chaque représentation module@version",
  "repository.hiddenFromVListAndLatest": "Masqué dans @v/list et @latest",
  "repository.artifactIdentity": "Identité de l’artefact",
  "repository.hiddenFromDiscoveryMetadata":
    "Masqué dans les métadonnées de découverte",
  "repository.repositorySecurityPolicySaved":
    "Politique de sécurité du dépôt enregistrée",
  "repository.noVulnerabilities": "Aucune vulnérabilité",
  "repository.upToLow": "Jusqu’à faible",
  "repository.upToMedium": "Jusqu’à moyenne",
  "repository.upToHigh": "Jusqu’à élevée",
  "repository.allowCritical": "Autoriser critique",
  "repository.noScannerConfigured": "Aucun analyseur configuré",
  "repository.unavailableForThisFormat": "Non disponible pour ce format",
  "repository.promotionGuardrail": "Garde-fou de promotion",
  "repository.promotionAdmission": "Admission à la promotion",
  "repository.evaluatesSignaturesSBOMsProvenanceAndVulnerabilityIntelligenceBefore":
    "Évalue les signatures, les SBOM, la provenance et les renseignements sur les vulnérabilités avant la création d’une tâche de promotion. Les lectures ordinaires restent inchangées.",
  "repository.enforced": "Appliquée",
  "repository.notEnforced": "Non appliquée",
  "repository.unsavedChanges": "Modifications non enregistrées",
  "repository.enforceAdmissionChecks":
    "Appliquer les vérifications d’admission",
  "repository.securityIntelligence": "Renseignements de sécurité",
  "repository.controlsWhetherNewlyPublishedArtifactsAutomaticallyProduceIntelligence":
    "Détermine si les artefacts nouvellement publiés produisent automatiquement les renseignements utilisés par l’admission.",
  "repository.scanAfterPublication": "Analyser après la publication",
  "repository.appliesOnlyToArtifactsPublishedAfterThisPolicy":
    "S’applique uniquement aux artefacts publiés après l’enregistrement de cette politique. Les analyses s’exécutent de façon asynchrone et les résultats apparaissent dans les détails de l’artefact.",
  "repository.evidenceRequirements": "Exigences de preuves",
  "repository.selectedRequirementsRunBeforeEveryPromotion":
    "Les exigences sélectionnées s’exécutent avant chaque promotion.",
  "repository.requirementsMayBeConfiguredInAdvanceAndTake":
    "Les exigences peuvent être configurées à l’avance et ne prennent effet qu’une fois l’admission activée.",
  "repository.requireASignature": "Exiger une signature",
  "repository.rejectArtifactsWithoutSignatureEvidence":
    "Rejeter les artefacts sans preuve de signature.",
  "repository.requireAVerifiedSignature": "Exiger une signature vérifiée",
  "repository.atLeastOneSignatureMustBeVerifiedUseful":
    "Au moins une signature doit être vérifiée; utile pour les dépôts de versions protégés.",
  "repository.requireAnSBOM": "Exiger un SBOM",
  "repository.rejectArtifactsWithoutASoftwareBillOfMaterials":
    "Rejeter les artefacts sans nomenclature logicielle (SBOM).",
  "repository.requireProvenance": "Exiger la provenance",
  "repository.requireBuildSourceBuilderAndCommitProvenance":
    "Exiger la provenance de la source de build, du builder et du commit.",
  "repository.requireVulnerabilityScanning":
    "Exiger l’analyse de vulnérabilités",
  "repository.doNotAdmitArtifactsThatHaveNotBeen":
    "Ne pas admettre les artefacts qui n’ont pas été analysés.",
  "repository.blockOnScanErrors": "Bloquer en cas d’erreur d’analyse",
  "repository.blockWhenAScannerReportsAnErrorDisable":
    "Bloquer lorsqu’un analyseur signale une erreur; désactivez cette option pour un repli en mode fail-open.",
  "repository.riskThresholds": "Seuils de risque",
  "repository.furtherRestrictTheVulnerabilitySeverityAndLicenseSet":
    "Restreindre davantage la gravité des vulnérabilités et l’ensemble des licences autorisés par l’admission.",
  "repository.maximumAllowedVulnerabilitySeverity":
    "Gravité maximale autorisée des vulnérabilités",
  "repository.affectedResultsAboveThisLevelAreRejectedUnknown":
    "Les résultats touchés au-delà de ce niveau sont rejetés. Les constats inconnus sont toujours traités comme le risque le plus élevé.",
  "repository.allowedSPDXLicenses": "Licences SPDX autorisées",
  "repository.leaveEmptyForNoRestrictionWhenSetEvery":
    "Laissez vide pour aucune restriction. Lorsque défini, chaque licence doit figurer dans cette liste d’ID SPDX.",
  "repository.eGApache20MIT": "p. ex. Apache-2.0, MIT",
  "repository.currentVersion": "Version actuelle {version}",
  "repository.quarantineReadPolicySaved":
    "Politique de lecture en quarantaine enregistrée",
  "repository.readGuardrail": "Garde-fou de lecture",
  "repository.quarantinedArtifactReads": "Lectures d’artefacts en quarantaine",
  "repository.controlsWhetherClientsMayContinueReadingQuarantinedArtifacts":
    "Détermine si les clients peuvent continuer à lire les artefacts en quarantaine par les protocoles du dépôt. Les lectures reprennent immédiatement après la libération.",
  "repository.readsBlocked": "Lectures bloquées",
  "repository.readsAllowed": "Lectures autorisées",
  "repository.unavailable2": "Non disponible",
  "repository.blockQuarantinedArtifactReads":
    "Bloquer les lectures d’artefacts en quarantaine",
  "repository.affectedRequests": "Requêtes touchées",
  "repository.currentFormatBoundary": "Frontière de format actuelle",
  "repository.discoveryMetadata": "Métadonnées de découverte",
  "repository.whenUsedByAGroup": "Lorsqu’utilisé par un groupe",
  "repository.noLowerPriorityFallback": "Aucun repli de priorité inférieure",
  "repository.readsRemainAllowedByDefaultForUpgradeCompatibility":
    "Les lectures restent autorisées par défaut pour la compatibilité de mise à niveau. Examinez les enregistrements de quarantaine existants avant d’activer l’application.",
  "repository.readPolicyVersion":
    "Version de la politique de lecture {version}",
  "repository.saveReadPolicy": "Enregistrer la politique de lecture",
  "repository.aPTSigningState": "État de la signature APT",
  "repository.securityAdmissionPolicy": "Politique d’admission de sécurité",
  "repository.repositoryGuardrails": "Garde-fous du dépôt",
  "repository.admissionAndQuarantinedReads":
    "Admission et lectures en quarantaine",
  "repository.twoIndependentGuardrailsLiveHereTheReadPolicy":
    "Deux garde-fous indépendants se trouvent ici\u00a0: la politique de lecture détermine si les artefacts en quarantaine peuvent être téléchargés, tandis que l’admission détermine si des artefacts peuvent entrer dans ce dépôt.",
  "repository.exportResponseIsEmpty": "La réponse d’exportation est vide",
  "repository.restoreResponseIsEmpty": "La réponse de restauration est vide",
  "repository.restoredSnapshotFromTheArchive":
    "Snapshot {suite} n° {sequence} restauré depuis l’archive",
  "repository.exportAPortableDisasterRecoveryArchiveOrRestore":
    "Exportez une archive portable de reprise après sinistre, ou restaurez un snapshot historique de cette suite après un sinistre à l’aide du reçu stocké séparément. Une archive ne contient que des signatures publiques et des octets de paquets, jamais de matériel de clé privée.",
  "repository.archiveDownloadStarted": "Téléchargement de l’archive démarré",
  "repository.fileStoreItSeparatelyFromTheReceiptRestore":
    "Fichier {filename}. Stockez-le séparément du reçu {digest}, car la restauration exige ce reçu exact.",
  "repository.fileThisBrowserCannotComputeTheDigestRun":
    "Fichier {filename}. Ce navigateur ne peut pas calculer le digest; exécutez gateway apt-snapshot verify sur l’archive et conservez le reçu sha256 qu’il affiche.",
  "repository.exportDisasterRecoveryArchive":
    "Exporter l’archive de reprise après sinistre",
  "repository.state": "État",
  "repository.published2": "Publié",
  "repository.action": "Action",
  "repository.exportArchive": "Exporter l’archive",
  "repository.restoreFromArchive": "Restaurer depuis une archive",
  "repository.onlyAGatewayV1ArchiveBelongingToThis":
    "Seule une archive Gateway v1 appartenant à ce dépôt est acceptée. La restauration vérifie d’abord le reçu de sauvegarde, les deux signatures OpenPGP et les index canoniques; rejouer la même archive est idempotent et ne re-signe ni ne promeut jamais un snapshot retiré.",
  "repository.chooseArchiveFile": "Choisir le fichier d’archive",
  "repository.backupReceiptSha256": "Reçu de sauvegarde (sha256)",
  "repository.mustBeTheSha25664LowercaseHexReceipt":
    "Doit être le reçu sha256:<64 caractères hexadécimaux minuscules> stocké séparément au moment de la sauvegarde. Ne le dérivez pas de ce téléversement; le serveur vérifie qu’ils correspondent.",
  "repository.restoreFromThisArchive": "Restaurer depuis cette archive?",
  "repository.restoreWritesPackagesAndSignedIndexesAConflicting":
    "La restauration écrit des paquets et des index signés. Un snapshot ou une coordonnée en conflit dans la suite cible est rejeté.",
  "repository.restoreArchive": "Restaurer l’archive",
  "repository.currentSignedSnapshot": "Snapshot signé courant",
  "repository.currentSnapshot": "Snapshot courant n° {sequence}",
  "repository.visible": "Visible",
  "repository.snapshotID": "ID du snapshot",
  "repository.signingFingerprint": "Empreinte de signature",
  "repository.signer": "Signataire",
  "repository.noVisibleSnapshotInThisSuite":
    "Aucun snapshot visible dans cette suite",
  "repository.checkTheSuiteNameOrCreateTheFirst":
    "Vérifiez le nom de la suite, ou créez le premier snapshot signé au moyen de l’API de publication.",
  "repository.prunableAfter": "Éligible à l’élagage après",
  "repository.pruneThisRetiredSnapshotSReferences":
    "Élaguer les références de ce snapshot retiré?",
  "repository.clientsUsingThisOldIndexCanNoLonger":
    "Les clients qui utilisent cet ancien index ne pourront plus le lire. D’autres références protègent toujours les objets partagés.",
  "repository.pruneReferences": "Élaguer les références",
  "repository.package": "Paquet",
  "repository.recoverUntil": "Récupérer jusqu’au",
  "repository.previewRecovery": "Prévisualiser la récupération",
  "repository.canonicalPathSHA256": "Chemin canonique / SHA-256",
  "repository.selectPackagesToPreviewRemovalFromTheCurrent":
    "Sélectionnez des paquets pour prévisualiser leur retrait de la suite courante.",
  "repository.previewDeletion": "Prévisualiser la suppression",
  "repository.retentionPreview": "Aperçu de la conservation",
  "repository.retainByUploadTime": "Conserver selon la date de téléversement",
  "repository.groupedByComponentPackageAndArchitectureOnlyUploads":
    "Regroupés par composant, paquet et architecture. Seuls les téléversements au-delà des deux limites sont admissibles; il ne s’agit pas de l’ordre des versions Debian.",
  "repository.keepLatestUploads": "Conserver les derniers téléversements",
  "repository.olderThanDays": "Plus anciens que (jours)",
  "repository.previewRetention": "Prévisualiser la conservation",
  "repository.reviewSnapshotChange": "Examiner la modification du snapshot",
  "repository.applyAndSignNewSnapshot":
    "Appliquer et signer le nouveau snapshot",
  "repository.basedOnSnapshot": "Basé sur le snapshot",
  "repository.removeRestorePackagesRemain":
    "Retirer {length}, restaurer {length2} ({remainingPackages} paquets restent)",
  "repository.deletedPackagesCanBeRecoveredForDaysSubmission":
    "Les paquets supprimés peuvent être récupérés pendant {recoveryDays} jours. La soumission crée de nouveaux index et de nouvelles signatures; un snapshot courant modifié exige un nouvel aperçu.",
  "repository.noMatchingChangesThereIsNothingToApply":
    "Aucune modification correspondante; il n’y a rien à appliquer.",
  "repository.snapshotResponseIsEmpty": "La réponse du snapshot est vide",
  "repository.previewResponseIsEmpty": "La réponse de l’aperçu est vide",
  "repository.theSnapshotChangedRefreshAndPreviewAgain":
    "Le snapshot a changé. Actualisez et prévisualisez de nouveau.",
  "repository.theSnapshotOrCandidatesChangedOrGovernanceBlocked":
    "Le snapshot ou les candidats ont changé, ou la gouvernance a bloqué un membre. Actualisez et prévisualisez de nouveau; aucune nouvelle tentative automatique n’a été faite.",
  "repository.noApplyResultWasReceivedRetryTheSame":
    "Aucun résultat d’application n’a été reçu. Réessayez la même requête.",
  "repository.publishedSnapshot": "Snapshot {suite} n° {sequence} publié",
  "repository.snapshotReferencesPrunedUnreferencedObjectsAreReclaimedAsynchronously":
    "Références du snapshot élaguées; les objets non référencés sont récupérés de façon asynchrone.",
  "repository.refreshSnapshots": "Actualiser les snapshots",
  "repository.packages": "Paquets ({length})",
  "repository.recovery": "Récupération ({length})",
  "repository.snapshots": "Snapshots ({length})",
  "repository.disasterRecovery": "Reprise après sinistre",
  "repository.promoteReplicate": "Promouvoir / répliquer",
  "repository.repositoryAdministratorAccessRequired":
    "L’accès administrateur du dépôt est requis",
  "repository.aPTSignedSnapshotsOperatorPreview":
    "Snapshots signés APT · Aperçu opérateur",
  "repository.membershipChangesRebuildAndSignACompleteSnapshot":
    "Les changements d’appartenance reconstruisent et signent un snapshot complet. L’application exige un signataire configuré; la garde des clés de production n’a pas été acceptée.",
  "repository.suite": "Suite",
  "repository.enterTheExactSuiteForExampleStableOr":
    "Saisissez la suite exacte, par exemple stable ou bookworm.",
  "repository.view": "Afficher",
  "repository.repositoryDetails": "Détails du dépôt",
  "repository.repositoryTasks": "Tâches du dépôt",
  "repository.tabArtifacts": "Artefacts",
  "repository.tabGovernance": "Gouvernance",
  "repository.tabDistribution": "Distribution",
  "repository.tabSettings": "Paramètres",
  "repository.tabBrowse": "Parcourir",
  "repository.tabUsage": "Utilisation",
  "repository.tabPublish": "Publier",
  "repository.tabAccessGrants": "Autorisations d’accès",
  "repository.tabRetention": "Conservation",
  "repository.tabScanning": "Analyse",
  "repository.tabSecurityAdmission": "Admission de sécurité",
  "repository.tabTombstones": "Pierres tombales",
  "repository.tabPromoteReplicate": "Promouvoir / répliquer",
  "repository.tabSignedSnapshots": "Snapshots signés",
  "repository.tabGeneral": "Général",
  "repository.tabCapacity": "Capacité",
  "repository.tabLifecycleJobs": "Tâches de cycle de vie",
  "repository.pushArtifactsWithTheMatchingClient":
    "Poussez des artefacts avec le client {format} pour les afficher ici.",
  "repository.allowedHostsRequired": "Hôtes autorisés (obligatoire)",
  "repository.allowedHostsOptional": "Hôtes autorisés (facultatif)",
  "repository.moreActionsFor": "Plus d’actions pour {name}",
  "repository.copyRepositoryId": "Copier l’ID du dépôt",
  "repository.deleteRepositoryAction": "Supprimer le dépôt",
};
