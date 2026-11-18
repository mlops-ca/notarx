import type { publicBrowseEnCA } from "./public-browse.en-CA";

export const publicBrowseFrCA: Record<keyof typeof publicBrowseEnCA, string> = {
  "public.browseTitle": "Navigateur d’artefacts publics",
  "public.managementLogin": "Connexion à la gestion",
  "public.managementConsole": "Ouvrir la console de gestion",
  "publicBrowse.loadingPublicRepositories": "Chargement des dépôts publics…",
  "publicBrowse.globalAnonymousReadsAreDisabled":
    "Les lectures anonymes globales sont désactivées",
  "publicBrowse.anonymousDisabledHint":
    "Un administrateur doit activer les lectures anonymes globales pour que les paramètres des dépôts prennent effet.",
  "publicBrowse.openAccessControl": "Ouvrir le contrôle d’accès",
  "publicBrowse.publicRepositoryNotFoundOrVisible":
    "Dépôt public introuvable ou non visible",
  "publicBrowse.repositoryMissingHint":
    "Revenez au catalogue et choisissez un dépôt pour lequel les lectures anonymes sont activées.",
  "publicBrowse.backToCatalog": "Retour au catalogue",
  "publicBrowse.publicReadOnly": "Public en lecture seule",
  "publicBrowse.catalogHeading":
    "Découvrez et consommez des artefacts publics de confiance",
  "publicBrowse.catalogDescription":
    "Recherchez des images, des dépendances et des paquets dans les dépôts explicitement publiés par votre équipe. La lecture ne requiert aucune connexion; la publication, les autorisations et l’administration des dépôts exigent toujours une authentification.",
  "publicBrowse.searchRepositoryNameOrFormat":
    "Rechercher un nom de dépôt ou un format",
  "publicBrowse.publicSources": "sources publiques",
  "publicBrowse.artifactFormats": "formats d’artefacts",
  "publicBrowse.managementActionsRequireSignIn":
    "Les actions de gestion exigent une connexion",
  "publicBrowse.catalogAccessHint":
    "Le catalogue public n’accorde jamais d’accès en écriture ni d’administration",
  "publicBrowse.noPublicRepositories": "Aucun dépôt public",
  "publicBrowse.emptyCatalogHint":
    "Un administrateur doit activer l’accès anonyme global et autoriser les lectures sur un dépôt. Les sources et formats publics apparaîtront ici dès qu’ils seront disponibles.",
  "publicBrowse.chooseAnArtifactSource": "Choisir une source d’artefacts",
  "publicBrowse.publicSourcesArtifactFormats":
    "{sourceCount} sources publiques · {formatCount} formats d’artefacts",
  "publicBrowse.filterByArtifactFormat": "Filtrer par format d’artefact",
  "publicBrowse.all": "Tous",
  "publicBrowse.noMatchingPublicRepositories":
    "Aucun dépôt public correspondant",
  "publicBrowse.catalogFilterHint":
    "Modifiez le terme de recherche ou le filtre de format et réessayez.",
  "publicBrowse.clearFilters": "Effacer les filtres",
  "publicBrowse.proxyDescription":
    "Met en cache et accélère les artefacts d’un amont approuvé par un administrateur.",
  "publicBrowse.groupDescription":
    "Regroupe plusieurs dépôts publics derrière un point d’accès stable unique.",
  "publicBrowse.hostedDescription":
    "Artefacts publics publiés et maintenus directement par l’équipe.",
  "publicBrowse.noSignInToRead": "Lecture sans connexion",
  "publicBrowse.openRepository": "Ouvrir le dépôt",
  "publicBrowse.publicRepositories": "Dépôts publics",
  "publicBrowse.unknownRepository": "Dépôt inconnu",
  "publicBrowse.coordinatePathOrNamePrefixOptional":
    "Coordonnée, chemin ou préfixe de nom (facultatif)",
  "publicBrowse.coordinatePathOrNameKeywordOptional":
    "Coordonnée, paquet ou mot-clé de chemin (facultatif)",
  "publicBrowse.search": "Rechercher",
  "publicBrowse.loadingPublicArtifacts": "Chargement des artefacts publics…",
  "publicBrowse.noMatchingPublicArtifacts":
    "Aucun artefact public correspondant",
  "publicBrowse.emptyArtifactsHint":
    "Vérifiez que les lectures anonymes sont activées ou modifiez la requête ci-dessus.",
  "publicBrowse.found": "Trouvé",
  "publicBrowse.artifacts": "artefacts",
  "publicBrowse.versions": " ({versionCount} versions)",
  "publicBrowse.anonymousReadOnly": "Anonyme en lecture seule",
  "publicBrowse.image": "Image",
  "publicBrowse.aptPath": "Chemin APT",
  "publicBrowse.artifactCoordinate": "Coordonnée de l’artefact",
  "publicBrowse.loadedVersions": "Versions chargées",
  "publicBrowse.loading": "Chargement…",
  "publicBrowse.loadWhenExpanded": "Charger au développement",
  "publicBrowse.versionCount": "{versionCount}{more}",
  "publicBrowse.selectedVersion": "Version sélectionnée",
  "publicBrowse.imageDigest": "Digest de l’image",
  "publicBrowse.digest": "Digest",
  "publicBrowse.size": "Taille",
  "publicBrowse.contentType": "Type de contenu",
  "publicBrowse.firstCached": "Première mise en cache",
  "publicBrowse.created": "Créé",
  "publicBrowse.actions": "Actions",
  "publicBrowse.collapse": "Réduire",
  "publicBrowse.selectVersion": "Sélectionner la version",
  "publicBrowse.usage": "Utilisation",
  "publicBrowse.openVersion": "Ouvrir la version",
  "publicBrowse.open": "Ouvrir",
  "publicBrowse.copied": "Copié",
  "publicBrowse.copyCoordinate": "Copier la coordonnée",
  "publicBrowse.copy": "Copier {coordinate}",
  "publicBrowse.repository": "Dépôt",
  "publicBrowse.published": "Publié",
  "publicBrowse.publisher": "Éditeur",
  "publicBrowse.notRecorded": "Non enregistré",
  "publicBrowse.selectImageVersion": "Sélectionner la version de l’image",
  "publicBrowse.loaded": "Chargé",
  "publicBrowse.retry": "Réessayer",
  "publicBrowse.loadingVersions": "Chargement des versions…",
  "publicBrowse.noMatchingVersions": "Aucune version correspondante",
  "publicBrowse.searchImageVersions": "Rechercher des versions d’image",
  "publicBrowse.loadingVersionPage": "Chargement…",
  "publicBrowse.loadMoreVersions": "Charger {pageSize} versions de plus",
  "publicBrowse.versionPageHint":
    "Jusqu’à {pageSize} versions sont chargées à la fois; sélectionnez-en une pour voir les détails et l’utilisation.",
  "publicBrowse.openVersionPage": "Ouvrir la version",
  "publicBrowse.linkCopied": "Lien copié",
  "publicBrowse.copyLink": "Copier le lien",
  "publicBrowse.version": "Version",
  "publicBrowse.checksum": "Digest",
  "publicBrowse.imageSize": "Taille de l’image",
  "publicBrowse.selectLoadedVersionHint":
    "Une fois les versions chargées, recherchez-en une à gauche et sélectionnez-la.",
  "publicBrowse.forRepository": "Pour le dépôt",
  "publicBrowse.usageAccessHint":
    "La consultation anonyme ne requiert aucun jeton. La publication, les dépôts privés et la gestion exigent toujours une connexion.",
  "publicBrowse.registerAnOciRegistry": "Enregistrer un registre OCI",
  "publicBrowse.registerAMavenRepository": "Enregistrer un dépôt Maven",
  "publicBrowse.registerAConanRemote": "Enregistrer un remote Conan",
  "publicBrowse.registerAnNpmRegistry": "Enregistrer un registre npm",
  "publicBrowse.registerAPypiRepository": "Enregistrer un dépôt PyPI",
  "publicBrowse.registerAGoModuleProxy": "Enregistrer un proxy de modules Go",
  "publicBrowse.registerACargoRegistry": "Enregistrer un registre Cargo",
  "publicBrowse.registerAnAptSource": "Enregistrer une source APT",
  "publicBrowse.registerARawSource": "Enregistrer une source Raw",
  "publicBrowse.selectPackageVersion": "Sélectionner la version du paquet",
  "publicBrowse.searchConanPackageVersions":
    "Rechercher des versions de paquet Conan",
  "publicBrowse.versionsAreGroupedUnderTheSameNameUser":
    "Les versions sont regroupées sous le même name@user/channel; sélectionnez-en une pour examiner sa révision de recette.",
  "publicBrowse.enterRevisionOrDigest": "Saisissez une révision ou un digest",
  "publicBrowse.loadingRevisions": "Chargement des révisions…",
  "publicBrowse.noMatchingRevisions": "Aucune révision correspondante",
  "publicBrowse.searchRecipeRevisions": "Rechercher des révisions de recette",
  "publicBrowse.loadMoreRevisions":
    "Charger {VERSION_PAGE_SIZE} révisions de plus",
  "publicBrowse.selectARecipeRevisionToViewDetailsAnd":
    "Sélectionnez une révision de recette pour voir les détails et l’utilisation.",
  "publicBrowse.conanPackage": "Paquet Conan",
  "publicBrowse.latestVersion": "Dernière version",
  "publicBrowse.versions2": "Versions",
  "publicBrowse.currentRevision": "Révision actuelle",
  "publicBrowse.copyConanPackageIdentifier":
    "Copier l’identifiant du paquet Conan",
  "publicBrowse.copy2": "Copier",
  "publicBrowse.artifact": "Artefact",
  "publicBrowse.searchMavenVersions": "Rechercher des versions Maven",
  "publicBrowse.searchByVersionOrSNAPSHOTBuildNumberWithout":
    "Recherchez par version ou par numéro de build SNAPSHOT sans développer la liste complète.",
  "publicBrowse.buildType": "Type de build",
  "publicBrowse.release": "Release",
  "publicBrowse.artifactBrowser": "Navigateur d’artefacts",
  "publicBrowse.failedToReadPublicRepositories":
    "Échec de la lecture des dépôts publics ({status})",
  "publicBrowse.failedToReadOciVersions":
    "Échec de la lecture des versions OCI",
  "publicBrowse.failedToReadOciTags": "Échec de la lecture des tags OCI",
  "publicBrowse.failedToReadOciTagsStatus":
    "Échec de la lecture des tags OCI ({status})",
  "publicBrowse.failedToReadOciManifest": "Échec de la lecture du manifest OCI",
  "publicBrowse.failedToReadConanRevisions":
    "Échec de la lecture des révisions Conan",
  "publicBrowse.untaggedDigest": "Sans tag · {digest}",
};
