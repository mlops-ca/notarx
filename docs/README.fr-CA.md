# Documentation d’Artifact Gateway

[English](README.md) · [Fichier README du projet](../README.fr-CA.md)

Artifact Gateway est un dépôt d’artefacts léger et natif au protocole. Son plan de
contrôle s’appuie sur PostgreSQL comme seule dépendance de coordination et de
base de données; les octets immuables résident dans un stockage d’objets
compatible S3.

Cet index suit le contrat de navigation indépendant des cadriciels défini dans
[`site-map.json`](site-map.json). Un énoncé de capacité n’est à jour que s’il
concorde avec les tests exécutables et avec la
[base de référence de compatibilité des protocoles](protocol-compatibility.md).

*Le noyau de la documentation (ce fichier, le guide de démarrage, la politique de
sécurité et le guide de contribution) est offert en français. Les autres guides
sont rédigés en anglais; leur titre ci-dessous est traduit pour faciliter la
recherche.*

- [Découverte des sources Nexus et correspondance native](nexus-source-discovery.md)
- [Lots de migration Nexus durables](nexus-migration-batches.md)
- [Migration Nexus par la Console et observations d’acceptation](nexus-migration-console.md)

## Pour commencer

- [Aperçu du projet](../README.fr-CA.md) — positionnement, capacités, démarrage rapide et frontière de déploiement léger.
- [Classes de chemins de l’intégration continue et barrière ci-ok](ci.md) — les tâches que déclenche un changement et la vérification obligatoire unique.
- [Constructions natives, matrice des exécuteurs et constructions locales](native-builds.md) — aucune compilation croisée ni émulation, la matrice des exécuteurs, les constructions locales sur Apple Silicon et la garde des constructions natives.
- [Guide du site de documentation](documentation-site-guide.md) — contrat de langue, de route, de navigation et de futur générateur de site.
- [Guide de démarrage](getting-started.fr-CA.md) — pile locale complète et premier dépôt.
- [Orientation du produit](../PRODUCT.md) — utilisateurs, cas d’usage, frontière du produit et priorités.
- [Contribuer](../CONTRIBUTING.fr-CA.md) — flux de changement, conventions de documentation et vérifications obligatoires.
- [Guide d’extension des formats](format-extension-guide.md) — exigences d’admission d’un nouvel écosystème de paquets.

## Architecture et conception

- [Architecture](../ARCHITECTURE.md) — exécution, stockage, cohérence et responsabilité du code.
- [Diagrammes d’architecture](architecture-diagrams.md) — flux du système, du déploiement, de la publication et des workers.
- [Système de conception](../DESIGN.md) — langage visuel, composants, mise en page et règles d’interaction de la Console.
- [Langage du domaine](../CONTEXT.md) — terminologie canonique des dépôts, des artefacts, du cycle de vie et de la distribution.
- [Configuration du stockage d’objets](object-storage.md) — pilotes, clés `NOTARX_OBJECT_STORE_*`, modes de sommes de contrôle et paramètres OCI.
- [Capacités PostgreSQL](postgresql-capabilities.md) — verrous, baux, files, notifications, JSONB, recherche, observabilité, l’empreinte du code dans PostgreSQL et la matrice de prise en charge de PostgreSQL géré.
- [Contrat Native Hosted](native-hosted-contract.md) — autorité des métadonnées, cycle de vie des objets, transactions et idempotence.
- [Contrat V2](v2-contract.md) — contexte historique de Raw, de Conan 2, de la politique anonyme et de la migration.
- [Contrat du cycle de vie des artefacts](artifact-lifecycle-contract.md) — visibilité, pierre tombale, restauration, collecte, promotion et réplication.
- [Contrat de suppression de dépôt](repository-deletion-contract.md) — suppression logique sûre et sémantique de reprise.
- [Déploiement distribué](distributed-deployment.md) — topologie de l’API, de l’ordonnanceur, des workers, de PostgreSQL et du stockage d’objets.
- [ADR 0001 : dépôt d’artefacts complet](adr/0001-full-artifact-repository.md) (en anglais)
- [ADR 0002 : instantanés de promotion](adr/0002-promotion-snapshots.md) (en anglais)
- [ADR 0003 : formats uniquement par protocole](adr/0003-protocol-only-formats.md) (en anglais)
- [ADR 0004 : publication Go hébergée](adr/0004-go-hosted-publication.md) (en anglais)
- [ADR 0005 : système de thèmes sémantiques de la Console](adr/0005-console-semantic-theme-system.md) (en anglais)
- [ADR 0006 : niveaux de compte et autorité par dépôt](adr/0006-account-levels-and-repository-grants.md) (en anglais)
- [ADR 0007 : forme de la CLI notarxctl et contrat transversal](adr/0007-agctl-cli-shape.md) (en anglais)
- [ADR 0008 : le chemin du module Go suit le dépôt source](adr/0008-go-module-path.md) (en anglais)
- [ADR 0009 : architecture de gestion native](adr/0009-native-architecture.md) (en anglais)
- [ADR 0010 : contrats du magasin en mémoire](adr/0010-memory-store-contracts.md) (en anglais)
- [ADR 0011 : modules de format explicites et traceur Raw](adr/0011-format-modules.md) (en anglais)
- [ADR 0012 : composition des domaines de l’API de gestion](adr/0012-management-domain-composition.md) (en anglais)
- [ADR 0013 : charte de la bifurcation notarx](adr/0013-notarx-fork-charter.md) (en anglais)
- [ADR 0014 : politique de langues, en-CA comme référence, fr-CA de première classe, zh-CN secondaire](adr/0014-locale-policy.md) (en anglais)
- [ADR 0015 : migration de base unique](adr/0015-single-baseline-migration.md) (en anglais)
- [ADR 0016 : constructions natives par architecture](adr/0016-native-architecture-builds.md) (en anglais)
- [ADR 0017 : moteurs d’exécution et déploiement de référence sur l’offre gratuite d’Oracle Cloud](adr/0017-runtime-backends-and-oci-free-tier.md) (en anglais)
- [ADR 0018 : fournisseurs d’identité humaine, registre OIDC et SAML, approvisionnement SCIM, groupes](adr/0018-identity-providers-saml-scim.md) (en anglais)
- [ADR 0019 : fédération d’identité des charges de travail (entrante)](adr/0019-workload-identity-federation.md) (en anglais)
- [ADR 0020 : notarx comme émetteur de jetons OIDC](adr/0020-notarx-oidc-issuer.md) (en anglais)
- [ADR 0021 : promotion par règles et chaînes de dépôts](adr/0021-promotion-rules-and-repository-chains.md) (en anglais)
- [ADR 0022 : portabilité de la base de données, MySQL 8.4 (HeatWave) comme second moteur](adr/0022-database-portability-mysql-heatwave.md) (en anglais)
- [ADR 0023 : extensibilité du modèle de données, découplage des écritures, partitionnement, conservation, compteurs et caches](adr/0023-data-model-scalability.md) (en anglais)
- [ADR 0024 : versionnement des versions, commits conventionnels et promotion de la version nocturne à la version stable](adr/0024-release-versioning-and-conventional-commits.md) (en anglais)
- [ADR 0025 : canaux de distribution, images, archives, paquets natifs et dépôts signés](adr/0025-distribution-channels.md) (en anglais)
- [ADR 0026 : dépôts Debian et RPM](adr/0026-debian-and-rpm-repositories.md) (en anglais)
- [ADR 0027 : dépôts de modèles et de jeux de données Hugging Face](adr/0027-hugging-face-repositories.md) (en anglais)
- [ADR 0028 : paquets génériques versionnés sur Raw](adr/0028-generic-versioned-packages.md) (en anglais)
- [ADR 0029 : visibilité des dépôts, flux publics et privés](adr/0029-repository-visibility-public-private-feeds.md) (en anglais)
- [ADR 0030 : génération de SBOM et analyse des vulnérabilités](adr/0030-sbom-generation-and-vulnerability-scanning.md) (en anglais)
- [Point de bifurcation en amont et registre des cherry-picks](upstream.md) — origine de la bifurcation, procédure de cherry-pick et commits en amont retenus ou refusés.
- [Ressources de marque notarx](brand-assets.md) — emblème, déclinaisons, icônes et jetons de couleur.

## Protocoles et formats

- [Compatibilité des protocoles](protocol-compatibility.md) — comportement mis en œuvre et limites explicites par format.
- [Catalogue et référents OCI](oci-discovery.md) — énumération autorisée, couverture, fermetures vérifiées et liaisons de source stables.
- [Publication native Conan 2](conan-native-publication.md) — distant canonique, visibilité du manifeste et acceptation par les clients.
- [Retrait de fichiers PyPI (yank)](pypi-yank.md) — sélection des fichiers, gestion atomique, pip et preuves de reprise.
- [Plages HTTP de Raw](raw-http-ranges.md) — lectures multiparties bornées et validateurs immuables.
- [Publication Maven hébergée](maven-hosted-publication.md) — mode direct compatible Nexus et engagement strict facultatif.
- [Gouvernance des versions npm](npm-version-governance.md) — dépréciation native, retrait de publication, barrières d’autorité, reprise et octets immuables.
- [APT mandataire et groupe](apt-proxy.md) — lectures qui préservent les octets, cache, autorisation et limites.
- [Cérémonie des clés de signature des paquets et environnements](release/packages-signing.md) — disposition des clés, garde, certification S2, rotation, révocation et environnements protégés de `packages.mlops.ca`.
- [Signature APT hébergée](apt-hosted-signing.md) — aperçu H2, signataire externe, rotation H3 et frontière de production.
- [Première publication APT](apt-first-publication.md) — mise en place explicite des paquets, revue d’identité et signature reprenable pour une suite vide.
- [Archives d’instantanés APT](apt-snapshot-archive.md) — export des octets signés, vérifications d’intégrité hors ligne, restauration de confiance dans le même dépôt et migration progressive de l’existant.
- [Cycle de vie APT hébergé](apt-hosted-lifecycle.md) — suppression signée, conservation, fenêtres de reprise et élagage sûr pour les références.
- [Distribution APT hébergée](apt-hosted-distribution.md) — promotion signée par la cible, réplication vérifiée et reprise par les workers.
- [Feuille de route APT hébergée](apt-hosted-roadmap.md) — barrières d’acceptation ordonnées H1 à H4.
- [Configuration du client Cargo](cargo-usage.md) — configuration et commandes des registres hébergés, mandataires et de groupe.
- [Recherche sur le dépôt Cargo](cargo-repository-research.md) — décisions sur le registre sparse et registre de mise en œuvre C0 à C4.
- [Aperçu HTTP de Helm](helm-hosted-preview.md) — publication hébergée désactivée par défaut, cache mandataire, liaison de source de groupe et consommation à froid vérifiée.
- [Aperçu NuGet hébergé](nuget-hosted-preview.md) — publication native désactivée par défaut et restauration vérifiée.
- [Aperçu NuGet mandataire et groupe](nuget-proxy-group-preview.md) — découverte de sources V3, autorité persistante et caches privés vérifiés.
- [Feuille de route NuGet](nuget-roadmap.md) — acceptation de l’aperçu et travaux restants de cycle de vie et d’admission.
- [Aperçu de la réplication durable NuGet](nuget-replication-preview.md) — plans hébergés fixes, workers à barrière, paquets vérifiés appartenant à la cible et reprise exacte par élément.
- [Aperçu RubyGems hébergé](rubygems-hosted-preview.md) — publication native désactivée par défaut, index de dépendances compacts et hérités, et gems originaux vérifiés.
- [Aperçu de la réplication RubyGems hébergée](rubygems-replication-preview.md) — plans durables désactivés par défaut, variantes immuables, publication à barrière et vérification à froid par le client.
- [Migration des groupes hérités](legacy-group-migration.md) — comportement de compatibilité et conseils de migration.
- [Référence du moteur Gitea](gitea-backend-reference.md) — référence de conception du contrôle préalable et de la boucle de preuves.

## Exploitation et sécurité

- [Envoi de courriels de test par l’administrateur](email-notification-delivery.md) — SMTP TLS sur demande, cibles chiffrées, tests synthétiques durables et limites de livraison.
- [Alertes de quota logique des dépôts](repository-quota-alerts.md) — seuils soutenus explicites, preuves durables et livraison par le worker de courriels existant.
- [Sources de signaux opérationnels](operational-signals.md) — signaux existants et diagnostics facultatifs de capacité locale pour l’administrateur, avec espace de noms, fraîcheur et frontières de défaillance.
- [Conception du produit de journaux d’exécution](runtime-log-product-design.md) — proposition de classification, de recherche, de stockage durable et d’acceptation du déploiement pour les journaux de l’API et des workers.
- [Déploiement Kubernetes](kubernetes-deployment.md) — base exécutable locale et exigences de production.
- [Guide de reprise](recovery-runbook.md) — sauvegarde, restauration, preuves RPO/RTO et retour arrière.
- [Import historique de Maven SNAPSHOT](maven-snapshot-import.md) — admission contrôlée d’un lot figé, identités historiques exactes et sélecteurs courants de la source, capacité et reprise par points de contrôle.
- [Contrôle préalable de capacité de migration](migration-capacity-preflight.md) — calcul hors ligne du budget logique et physique à partir de preuves d’inventaire fixes.
- [Vérification hors ligne du manifeste de sauvegarde](backup-manifest-verification.md) — vérification locale privée des octets et cohérence d’instantané explicitement non démontrée.
- [Opérations d’accès anonyme](anonymous-access-operations.md) — barrières globale, de groupe et de dépôt, refus par défaut.
- [Gouvernance des utilisateurs locaux](user-governance.md) — comptes, sessions, verrouillage, liaison OIDC et audit.
- [SSO de navigateur OIDC](oidc-sso.md) — Code avec PKCE, configuration à l’exécution, correspondance des rôles et comportement des sessions.
- [Keycloak sur Kubernetes](oidc-keycloak-k8s.md) — acceptation du rappel réel du navigateur.
- [Autorisation par octroi de dépôt](repository-grant-authorization-plan.md) — décisions d’exécution délimitées et déploiement progressif.
- [Opérations des comptes de service](service-account-operations.md) — principaux machine stables et rotation sans interruption.
- [CLI notarxctl](cli.md) — contrat du client pour l’intégration continue, l’administration de la plateforme et les agents : priorité de configuration, codes de sortie, enveloppe JSON et commandes d’introspection actuelles.
- [Carte des commandes notarxctl](cli-command-map.md) — chaque opération de gestion et de protocole natif classée comme première version, ultérieure ou explicitement sans objet, avec permissions et codes d’acceptation; protégée par `make cli-contract-check`.
- [Politique d’admission de sécurité](security-admission-policy.md) — preuves de promotion, quarantaine et application facultative en lecture.
- [Contrat de l’analyseur d’artefacts](artifact-scanner-contract.md) — analyse externe bornée, santé, preuves et réconciliation.
- [Conception de la sortie du mandataire](proxy-egress-design.md) — routage direct, par environnement, HTTP et SOCKS5 par dépôt.
- [Contrat de livraison des webhooks](webhook-delivery-contract.md) — événements transactionnels, HMAC, nouvelles tentatives et rejeu.
- [Politique de sécurité](../SECURITY.fr-CA.md) — signalement privé et base de référence de déploiement.

## Qualité, performance et publication

- [Base de référence de performance](performance-baseline.md) — taille du binaire et de l’image, mémoire au repos, concurrence et preuves sur les gros objets.
- [Preuves de recherche « artefact contient »](reports/artifact-contains-search-2026-10-12.md) — plans de requête PostgreSQL isolés, coût des fragments courts et budgets de pagination.
- [Évaluation de la qualité du projet](project-quality-assessment.md) — forces, risques et priorités d’amélioration.
- [Liste de vérification de l’achèvement du moteur](backend-completion-checklist.md) — état de la mise en œuvre de V1 et prochaines tranches.
- [Préparation à la publication](release-readiness.md) — barrières exécutables de protocole, d’intégration, de mise à niveau et de reprise.
- [Modèle de fiche de publication](release-record-template.md) — champs de preuves, d’approbation et de retour arrière.
- [Fiche de préparation : 2026-08-11](release-records/2026-08-11-d738d4ed.md) — preuves historiques d’un candidat local, pas une approbation de production.
- [Fiche de candidat : 2026-09-08](release-records/2026-09-08-cb59b227.md) — candidat de groupe figé, preuves de mise à niveau et de restauration de la distribution, et barrière d’accès au registre restante.
- [Journal des changements](../CHANGELOG.md) — versions stables, généré à partir des commits conventionnels.

## Stratégie et référence

- [Objectif du dépôt complet](full-artifact-repository-goal.md) — définition de l’achèvement de V1.
- [Feuille de route du dépôt complet](full-artifact-repository-roadmap.md) — séquence d’architecture et état actuel.
- [Analyse d’écart avec Nexus](nexus-gap-analysis.md) — comparaison actuelle des capacités et de l’expérience entre les produits.
- [Revue d’écart avec Nexus, août 2026](nexus-gap-review-2026-08.md) — revue historique à un instant donné.
- [Recherche sur la publication Maven de Nexus](nexus-maven-publication-research.md) — comportement direct et par étapes, d’après les sources primaires.
- [Feuille de route de la Console des dépôts](repository-console-experience-roadmap.md) — navigation, capacité, opérations des mandataires et expérience des politiques.
- [Gouvernance OpenAPI](openapi-governance-plan.md) — frontières de source, de génération, d’exécution et de revue.

## Règles de documentation

- L’anglais utilise le `.md` sans suffixe; le français canadien utilise `.fr-CA.md` et le chinois simplifié `.zh-CN.md`.
- Seul l’anglais est obligatoire. Le noyau français (`README`, `docs/README`, `docs/getting-started`, `SECURITY` et `CONTRIBUTING`) est vérifié à l’intégration continue; les autres pages en français sont facultatives, et un fichier `.zh-CN.md` existant est conservé tel quel.
- Chaque paire de pages se lie dans les deux sens et figure une seule fois dans `site-map.json` avec des titres localisés.
- Les versions localisées préservent les commandes, les routes, les codes d’état, les limites de compatibilité, les frontières de sécurité et l’état de livraison.
- Les travaux de recherche, d’aperçu, de preuves historiques et de feuille de route restent explicitement étiquetés.
- Exécutez `make docs-check` avant de soumettre des changements de documentation.
