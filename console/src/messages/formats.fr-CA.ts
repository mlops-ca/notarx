import type { formatsEnCA } from "./formats.en-CA";

export const formatsFrCA: Record<keyof typeof formatsEnCA, string> = {
  "formats.ociDiscovery": "Catalogue OCI et artefacts associés",
  "formats.ociCatalogCoverage":
    "Le catalogue natif liste les noms et les sources d’images autorisés, et indique la couverture de l’amont. Le cache ci-dessous n’est pas un inventaire complet.",
  "formats.ociReferrerAuthority":
    "Les artefacts associés suivent la source du digest de l’image, même après le réordonnancement des membres du groupe. Un changement de configuration de la source exige une nouvelle vérification.",
  "formats.ociDiscoveryTrust":
    "Trouver un SBOM ou une signature confirme le lien de contenu; cela ne vérifie ni l’identité du signataire ni la politique de confiance.",
  "formats.ociReferrersApiRequired":
    "Les amonts doivent prendre en charge l’API Referrers. Les sources non compatibles renvoient une erreur explicite; le repli par tag est indisponible.",
  "formats.name": "Nom",
  "formats.filterByImageNamePrefix": "Filtrer par préfixe de nom d’image…",
  "formats.artifact": "Artefact",
  "formats.searchGAVCoordinates": "Rechercher des coordonnées GAV…",
  "formats.theProxyCacheAppearsAfterAMavenClient":
    "Le cache du proxy apparaît après qu’un client Maven a récupéré des dépendances.",
  "formats.package": "Paquet",
  "formats.filterByPackageNamePrefix": "Filtrer par préfixe de nom de paquet…",
  "formats.upstreamMetadataAndCacheStatusAppearAfterNpm":
    "Les métadonnées de l’amont et l’état du cache apparaissent après que npm install a récupéré un paquet.",
  "formats.project": "Projet",
  "formats.filterByProjectNamePrefix": "Filtrer par préfixe de nom de projet…",
  "formats.upstreamFilesAndCacheStatusAppearAfterPip":
    "Les fichiers de l’amont et l’état du cache apparaissent après que pip install a récupéré un projet.",
  "formats.module": "Module",
  "formats.filterByModulePathPrefix":
    "Filtrer par préfixe de chemin de module…",
  "formats.upstreamVersionsAndCachedAssetsAppearAfterGo":
    "Les versions de l’amont et les ressources en cache apparaissent après que go mod download a récupéré un module.",
  "formats.searchCrateNames": "Rechercher des noms de crate…",
  "formats.filterByReference": "Filtrer par référence…",
  "formats.filePath": "Chemin du fichier",
  "formats.searchPaths": "Rechercher des chemins…",
  "formats.aPTPath": "Chemin APT",
  "formats.filterByDistsOrPoolPath": "Filtrer par chemin dists/ ou pool/…",
  "formats.theProxyCacheAppearsAfterAptUpdateApt":
    "Le cache du proxy apparaît après apt update, apt install ou une requête directe vers un chemin APT.",
  "formats.uploadDebFilesThroughPublicationSessionsAndPublish":
    "Téléversez des fichiers .deb dans des sessions de publication et publiez un snapshot signé pour afficher la version visible actuelle.",
  "formats.crate": "Crate",
  "formats.filterByImageNameKeyword": "Filtrer par mot-clé de nom d’image…",
  "formats.filterByPackageNameKeyword": "Filtrer par mot-clé de nom de paquet…",
  "formats.filterByProjectNameKeyword": "Filtrer par mot-clé de nom de projet…",
  "formats.filterByModulePathKeyword":
    "Filtrer par mot-clé de chemin de module…",
};
