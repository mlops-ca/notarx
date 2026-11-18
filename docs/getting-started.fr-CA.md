# Démarrage

[English](getting-started.md) · [Retour au README](../README.fr-CA.md)

Ce guide met en place la pile de développement complète : un Gateway autonome,
PostgreSQL pour les métadonnées et la coordination, RustFS pour le plan des
octets compatible S3, et la Console Vite. Aucun composant Redis, Kafka,
Elasticsearch, file externe ou de découverte de services n’est nécessaire.

## Prérequis

- Docker avec la prise en charge de Compose
- Node.js 24 ou plus et npm
- GNU Make
- OpenSSL
- Environ 4 Gio de mémoire libre pour la pile de développement par défaut

Go est requis pour le développement du moteur, et il faut utiliser la version
déclarée dans `go.mod`. `kubectl` n’est nécessaire que pour le flux de travail
Kubernetes.

## Installer une version empaquetée

La version 0.1.0 est offerte sur [GitHub Releases](https://github.com/mlops-ca/notarx/releases/tag/v0.1.0)
sous forme d’archives Gateway Linux et macOS pour amd64 et arm64, d’un paquet
statique de la Console, des contrats OpenAPI résolus et de sommes de contrôle.
Vérifiez `SHA256SUMS` avant de décompresser, puis exécutez `notarx version` pour
confirmer la version intégrée et la révision Git.

Chaque archive Gateway contient aussi `migrations/`, `run-migrations.sh`, un
exemple d’environnement et un guide d’installation compact. Une installation à
partir des binaires exige `psql`; exportez `PGHOST`, `PGUSER`, `PGDATABASE` et
`PGPASSWORD`, puis appliquez le schéma avant de démarrer Gateway :

```sh
MIGRATION_DIR=./migrations ./run-migrations.sh
```

Les conteneurs de la version stable sont `ghcr.io/mlops-ca/notarx:0.1.0` et
`ghcr.io/mlops-ca/notarx-console:0.1.0`. Les étiquettes `main` correspondantes
sont des instantanés mobiles qualifiés par l’intégration continue; elles ne
doivent pas remplacer une étiquette de version ou un condensé (digest) épinglé
dans un déploiement contrôlé.

## 1. Préparer l’environnement

```sh
make dev-bootstrap
```

La commande crée `.env` à partir de `.env.example` lorsque le fichier est
absent, fixe ses permissions à `0600` et génère ces valeurs requises, propres au
poste local :

- `NOTARX_POSTGRES_PASSWORD`
- `NOTARX_ADMIN_TOKEN`
- `NOTARX_RESOLVER_TOKEN`
- `RUSTFS_ACCESS_KEY`
- `RUSTFS_SECRET_KEY`
- `RUSTFS_RPC_SECRET`

Les valeurs existantes qui ne sont pas des valeurs de remplacement ne sont jamais
écrasées. Si un `.env` existant doit être modifié, l’outil écrit de façon
atomique et conserve une copie de retour arrière sous
`.local-dev/environment-backups/`. Les justificatifs générés ne sont pas
affichés.

Examinez `.env` si vous avez besoin d’autres ports ou de paramètres facultatifs
pour OIDC, l’analyseur, le mandataire de sortie, le signataire APT ou les workers.

## 2. Démarrer la pile

```sh
make dev
```

La commande construit et démarre PostgreSQL, RustFS, les migrations et Gateway
avec Docker Compose. Elle installe aussi, au besoin, les dépendances de la
Console épinglées par le fichier de verrouillage, démarre Vite sous un
superviseur propre à la copie de travail et attend que la pile complète soit
prête.

Points d’accès locaux par défaut :

| Surface | Adresse |
| --- | --- |
| Console | <http://127.0.0.1:4173> |
| API de Gateway | <http://127.0.0.1:8080> |
| Vivacité | <http://127.0.0.1:8080/livez> |
| Disponibilité | <http://127.0.0.1:8080/readyz> |
| API S3 de RustFS | <http://127.0.0.1:9000> |
| Console de RustFS | <http://127.0.0.1:9001> |

Les remplacements définis dans `.env` sont reflétés par `make dev-status`.

## 3. Créer le premier dépôt

1. Ouvrez la Console.
2. Lisez `NOTARX_ADMIN_TOKEN` dans le fichier `.env` local et utilisez-le à
   l’entrée de connexion de l’administrateur.
3. Ouvrez **Dépôts**, choisissez **Créer un dépôt**, puis sélectionnez hébergé,
   mandataire ou groupe, ainsi qu’un format actuellement pris en charge.
4. Après la création, utilisez l’extrait de client de la page de détail du dépôt
   pour obtenir la bonne racine de protocole et la bonne forme d’authentification.

Utilisez un dépôt hébergé pour les artefacts publiés dans Gateway, un dépôt
mandataire pour un amont figurant sur la liste d’autorisation, et un groupe pour
exposer des membres hébergés et mandataires ordonnés par un seul point d’accès
client. La [base de référence de compatibilité des protocoles](protocol-compatibility.md)
consigne le comportement et les limites propres à chaque format.

## Commandes courantes

```sh
make dev-status       # vérifie la Console, le mandataire d'API, la vivacité et la disponibilité
make dev-down         # arrête seulement la Console gérée par cette copie de travail
make down             # arrête les services Compose; conserve les volumes
make dev              # redémarre la pile complète
make test             # exécute la barrière de régression locale partagée
```

`make dev-down` laisse volontairement Gateway et ses services de données en
marche. Utilisez `make down` lorsque toute la pile Compose doit s’arrêter.
Aucune des deux commandes ne supprime les volumes de PostgreSQL ou de RustFS.

## Dépannage

### Le fichier `.env` est absent ou contient encore des valeurs de remplacement

Exécutez `make dev-bootstrap`. La commande est idempotente et ne renouvelle pas
les justificatifs existants valides.

### Les dépendances de la Console sont périmées

Arrêtez la Console gérée avec `make dev-down`, exécutez
`npm --prefix console ci`, puis redémarrez-la avec `make dev`. Ne remplacez pas
`console/node_modules` pendant que Vite fonctionne.

### Un port local est déjà utilisé

Modifiez `NOTARX_HTTP_PORT`, `NOTARX_CONSOLE_PORT`, `NOTARX_POSTGRES_PORT` ou les
ports de RustFS dans `.env`, puis redémarrez les services touchés. Confirmez les
points d’accès effectifs avec `make dev-status`.

### Des ressources MinIO héritées sont détectées

L’environnement d’exécution local actuel n’utilise que RustFS. La garde échoue
de façon sûre lorsqu’elle trouve d’anciens conteneurs ou volumes MinIO, et elle
ne les supprime ni ne les monte jamais. Examinez et conservez explicitement les
anciennes données avant de supprimer ou de renommer les ressources héritées;
l’outil ne comporte aucun contournement de migration sur place.

### Gateway est vivant mais pas prêt

Exécutez `docker compose --env-file .env -f compose.yml ps` et examinez la
disponibilité de Gateway, de PostgreSQL, des migrations et de RustFS. La
disponibilité exige les dépendances de stockage du rôle configuré; la vivacité ne
rend compte que de l’état du processus.

## Pour poursuivre

- [Architecture](../ARCHITECTURE.md)
- [Index de la documentation](README.fr-CA.md)
- [Flux de contribution](../CONTRIBUTING.fr-CA.md)
- [Déploiement de développement Kubernetes](kubernetes-deployment.md)
- [Guide de reprise](recovery-runbook.md)
