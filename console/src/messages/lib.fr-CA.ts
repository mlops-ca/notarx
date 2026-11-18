import type { libEnCA } from "./lib.en-CA";

export const libFrCA: Record<keyof typeof libEnCA, string> = {
  "lib.mavenRepositoryURL": "URL du dépôt Maven",
  "lib.oCIRegistryAddress": "Adresse du registre OCI",
  "lib.dockerRegistrySetup": "Configuration du registre Docker",
  "lib.conanRemoteAddress": "Adresse du remote Conan",
  "lib.npmRegistryURL": "URL du registre npm",
  "lib.oneOffUsage": "Utilisation ponctuelle",
  "lib.cargoSparseIndex": "Index sparse Cargo",
  "lib.searchAndInstallACrate": "Rechercher et installer une crate",
  "lib.aPTSourceURL": "URL de la source APT",
  "lib.downloadAnArtifact": "Télécharger un artefact",
  "lib.dockerLoginImagePrefix":
    "docker login {host}\n# Préfixe d’image\u00a0: {host}/{repositoryName}/",
  "lib.mavenDependencyDynamicSnapshot": "Dépendance Maven (SNAPSHOT dynamique)",
  "lib.mavenDependencyPinnedBuild": "Dépendance Maven (build épinglé)",
  "lib.buildPom": "POM de build",
  "lib.gradlePinnedBuild": "Gradle (build épinglé)",
  "lib.mavenDependencyPomXml": "Dépendance Maven (pom.xml)",
  "lib.gradleDependency": "Dépendance Gradle",
  "lib.repositoryPath": "Chemin du dépôt",
  "lib.dockerPull": "Docker pull",
  "lib.dockerRun": "Docker run",
  "lib.imageReference": "Référence de l’image",
  "lib.addRemote": "Ajouter un remote",
  "lib.installPackage": "Installer le paquet",
  "lib.downloadUrl": "URL de téléchargement",
  "lib.curlDownload": "Téléchargement avec curl",
  "lib.rawRepositoryAddress": "Adresse du dépôt Raw",
  "lib.failedToReadOciManifestStatus":
    "Échec de lecture du manifest OCI ({status})",
};
