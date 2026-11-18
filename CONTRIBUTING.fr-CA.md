# Contribuer à Artifact Gateway

[English](CONTRIBUTING.md)

Artifact Gateway est actuellement développé par son équipe principale. Ce guide
consigne le flux de travail d’ingénierie utilisé dans le dépôt afin que les
changements demeurent révisables et reproductibles pendant que le projet se
prépare à accueillir davantage de contributions.

## Prérequis de développement

- Docker avec la prise en charge de Compose
- `kubectl`, `jq` et une grappe Kubernetes locale pour modifier les ressources
  de déploiement
- Go à la version déclarée dans `go.mod`
- Rust/Cargo 1.96.0 pour modifier la base de protocole Cargo en préparation
- Node.js 26 et npm
- GNU Make
- OpenSSL

Démarrez la pile locale complète à partir d’une copie de travail :

```sh
make dev-bootstrap
make dev
```

`make dev-bootstrap` crée un fichier `.env` privé au besoin et génère seulement
les justificatifs requis par la pile locale Gateway, PostgreSQL et RustFS. Il
préserve les vraies valeurs existantes et peut être relancé sans risque.
Consultez le [guide de démarrage](docs/getting-started.fr-CA.md) pour le flux
complet.

`make dev-status` vérifie la Console, son mandataire d’API et l’état de santé de
Gateway. `make dev-down` arrête seulement la Console gérée par la copie de
travail; `make down` arrête la pile Compose sans supprimer ses volumes PostgreSQL
et RustFS.

## Flux de changement

1. Gardez chaque changement centré sur un seul comportement ou une seule
   préoccupation d’ingénierie.
2. Ajoutez ou mettez à jour les tests à la frontière publique touchée par le
   changement.
3. Mettez à jour la source OpenAPI avant les clients générés ou les interfaces de
   serveur.
4. Ajoutez une migration pour les changements de schéma; ne modifiez jamais une
   migration déjà appliquée.
5. Décrivez les changements de comportement visibles pour l’utilisateur dans le
   titre de la demande de tirage (un sujet de commit conventionnel);
   `CHANGELOG.md` en est généré au moment de la publication et ne se modifie pas
   à la main.
6. Exécutez la plus petite vérification pertinente pendant le développement, puis
   les vérifications obligatoires ci-dessous avant de valider.

Les fichiers générés ne doivent pas être modifiés à la main. Les racines OpenAPI
modifiables sont `api/openapi/native-hosted.yaml` pour le contrat natif et de la
Console, et `api/openapi/management-runtime.yaml` pour la frontière de gestion
en Go. Gardez les chemins et les schémas de gestion partagés cohérents dans les
deux racines. `make openapi-check` régénère et vérifie les deux paquets JSON, le
client de la Console et le contrat de gestion Go.

Installez les générateurs épinglés avant d’exécuter la vérification du contrat :

```sh
npm --prefix tools/openapi ci --ignore-scripts --no-audit --no-fund
npm --prefix console ci --ignore-scripts --no-audit --no-fund
```

Ne réinstallez pas les dépendances de la Console pendant que `make dev` sert
Vite. Arrêtez la Console gérée avec `make dev-down`, installez les dépendances,
puis redémarrez-la avec `make dev`; remplacer `node_modules` sous un processus
Vite actif invalide les URL de dépendances optimisées.

Les outils facultatifs de lint, d’usage et de diagnostic de la CLI Ant Design ont
été retirés, avec leur chaîne de dépendances de développement `fast-glob` →
`micromatch` → `braces`. GHSA-vfj7-8cjw-p6xm ne fait l’objet d’aucune exception
d’audit; réintroduire l’avis fait échouer la barrière. Utilisez
`make console-check` pour ESLint et le formatage, `make console-typecheck` pour
les types et `make console-test` pour la barrière de couverture complète. La
génération de l’API utilise toujours les outils OpenAPI épinglés ci-dessus. Les
thèmes utilisent les définitions de thèmes de la Console et le
`ConfigProvider` d’Ant Design; Vite et Tailwind construisent les styles avec
`make console-build`. Aucun de ces flux n’exige la CLI Ant Design. Les autres
décisions d’audit demeurent dans `scripts/npm-audit-policy.mjs`; les nouveaux
avis, les rapports invalides, les pannes de registre et les exceptions expirées
font échouer l’audit des dépendances.

## Langue

Les tickets, les demandes de tirage, les messages de commit, les commentaires de
code et la documentation sont rédigés en anglais (orthographe canadienne, selon
l'[ADR 0014](docs/adr/0014-locale-policy.md)). N’ouvrez pas de tickets ni de
demandes de tirage dans une autre langue. Les langues du produit et la
documentation traduite suivent la politique de langues de l’ADR 0014, et non ce
fichier.

## Traçabilité du ticket jusqu’à la fusion

Suivez chaque changement visible pour l’utilisateur ou chaque défaut dans un
ticket GitHub qui indique le problème observé, le comportement visé, la portée
explicite, les critères d’acceptation et les dépendances. Gardez chaque ticket
assez petit pour être validé et fusionné indépendamment; scindez les sémantiques
de protocole ou de cache sans lien en tickets de suivi liés.

Utilisez une branche comme `codex/issue-24-group-resolution`. Liez le ticket dans
le corps de la demande de tirage avec `Closes #24`, décrivez le comportement
final, et consignez les commandes et les résultats pour le SHA de tête de la
demande. Ne fusionnez qu’une fois les vérifications obligatoires réussies pour ce
SHA. Consignez la demande de tirage, le SHA de fusion et les limites restantes
dans le ticket, puis supprimez la branche de fonctionnalité fusionnée après avoir
vérifié qu’aucun arbre de travail ne l’utilise encore. Ne supprimez pas des
branches non fusionnées uniquement parce qu’elles sont anciennes.

L’intégration continue, les bancs d’essai de protocole isolés, les vérifications
d’exécution locales et l’acceptation de la publication sont des preuves
distinctes. Fermer un ticket de mise en œuvre ne déclare pas une version prête;
suivez explicitement l’acceptation de la mise à niveau, de la restauration et du
déploiement.

## Classification des tickets

Chaque ticket porte exactement une étiquette de type et une étiquette de domaine
principal; les tickets sur un format de paquets précis portent aussi l’étiquette
de ce format. Le vocabulaire des étiquettes est la classification : filtrer sur
`label:"area: cli"` retourne exactement ce volet de travail.

| Axe | Étiquettes | Signification |
| --- | --- | --- |
| Type | `type: feature`, `type: bug`, `type: design`, `type: docs`, `type: chore` | `feature` étend une capacité visible pour l’utilisateur; `bug` est un comportement contraire au résultat visé ou au contrat; `design` produit une décision consignée dans un ADR ou un document plutôt que du code; `docs` ne change que la documentation; `chore` couvre l’intégration continue, les dépendances, les refactorisations et l’outillage sans changement de comportement. |
| Domaine | `area: console`, `area: api`, `area: cli`, `area: auth`, `area: audit`, `area: platform`, `area: ops` | `console` est l’interface Web; `api` est le contrat de gestion, ses sources OpenAPI et les clients générés; `cli` est `notarxctl`; `auth` couvre l’identité, l’autorisation, les comptes et les justificatifs de service; `audit` couvre les enregistrements d’audit, la conservation et l’analyse judiciaire; `platform` est le plan de données côté serveur : artefacts et dépôts, cycle de vie et conservation, promotion et réplication, capacité, cache, quarantaine, analyse; `ops` couvre le déploiement, l’exécution, les diagnostics, les journaux, la sauvegarde et la mise à niveau. |
| Format | `format: maven`, `format: oci`, `format: npm`, `format: pypi`, `format: go`, `format: apt`, `format: conan`, `format: cargo`, `format: nuget`, `format: raw` | Orthogonal au domaine; appliqué aux tickets portant sur le comportement d’un seul protocole. |

Les titres commencent par le domaine : `Console:`, `API:` et `CLI:` pour les
surfaces techniques, et `Auth:`, `Audit:`, `Platform:` ou `Ops:` pour les
domaines métier, suivi de la constatation ou du résultat requis plutôt que de
l’activité. Le travail transversal (processus, outillage) peut omettre le mot de
domaine et l’étiquette de domaine.

Chaque ticket énonce son contexte, sa portée, ses critères d’acceptation et une
exclusion explicite; une série garde un ticket parent dont le corps énumère ses
enfants sous forme de liste de tâches. Gardez un ticket par changement pouvant
être fusionné indépendamment et référencez-le avec `Closes #N`. Les formulaires
de tickets sous `.github/ISSUE_TEMPLATE/` utilisent des champs propres à chaque
type pour ces éléments et appliquent l’étiquette de type; ajoutez les étiquettes
de domaine et de format applicables au moment de l’ouverture. Pour les défauts, le
comportement attendu et les preuves de contrat définissent l’acceptation, et
l’incidence énonce la portée et les exclusions. Pour les conceptions, le problème
et les contraintes énoncent la portée et les exclusions, et la décision avec son
résultat consigné définit l’acceptation. Les formulaires de fonctionnalité
nomment directement ces éléments.

## Vérifications obligatoires

Exécutez la matrice localement avant de pousser, afin qu’un envoi ne consomme
l’intégration continue qu’une fois plutôt qu’à chaque itération :

```sh
make ci-local                        # l'ensemble rapide, quelques minutes
make ci-local-full                   # chaque tâche de demande de tirage, y compris les suites E2E natives
./scripts/ci-local.sh --job console  # une seule tâche d'intégration continue
./scripts/ci-local.sh --list         # ce que chaque sélection exécute
```

`scripts/ci-local.sh` appelle les mêmes cibles et scripts que le flux de travail
plutôt que de les réimplémenter, regroupe sa sortie par tâche d’intégration
continue et affiche chaque vérification avec sa durée. Il s’arrête à la première
erreur par défaut; passez `--keep-going` pour réunir toutes les erreurs en une
seule exécution. `mainline-assets` et `mainline-images` ne s’exécutent qu’à
l’envoi vers main et sont signalés comme hors périmètre. L’intégration continue
reste la source de vérité.

Pour un changement limité au moteur :

```sh
go test ./path/to/changed/package
make lint
make vet
make coverage
```

Pour un changement de la Console :

```sh
npm --prefix console ci
make console-check
make console-test
make console-build
```

`make lint` exécute aussi `make spellcheck`, qui vérifie les messages de la
Console et la documentation anglaise selon le dictionnaire de l’anglais canadien
(`en-CA`) (`colour`, `-ize`); `make architecture-check` rejette le texte CJK
dans les fichiers Go hors tests, car les serveurs retournent des messages en
anglais avec des codes stables et la Console les localise selon le code (ADR
0014). Installez d’abord les dépendances de la Console
(`npm --prefix console ci`); les termes propres au projet vont dans
`.cspell/project-words.txt`.

`make coverage` applique à la fois le seuil de base Go de tout le dépôt et des
planchers plus stricts pour les paquets stables de sécurité, de cycle de vie, de
réplication et d’analyse. `make console-test` mesure tout le TypeScript et le TSX
écrits à la main de la Console, en n’excluant que le client d’API généré et la
configuration des tests. Les planchers de couverture sont des garde-fous de
non-régression : relevez-les avec des tests significatifs à la frontière publique
et ne les abaissez pas pour faire passer un changement.

Pour les changements de contrat, de persistance ou de protocole, exécutez aussi
les vérifications correspondantes :

```sh
make openapi-check
make integration-test
make native-oci-e2e
make native-raw-e2e
make native-maven-e2e
make conan-e2e
make cargo-contract
```

Pour les manifestes Kubernetes, le routage du conteneur de la Console ou les
outils de déploiement local, exécutez la barrière de rendu hors ligne avant
d’utiliser une grappe réelle :

```sh
make kubernetes-local-check
make console-docker-build
```

Lorsque Kubernetes de Docker Desktop est disponible, exécutez aussi
`make kubernetes-local-up`, `make kubernetes-local-status` et
`make kubernetes-local-verify`. La vérification publie un objet Raw unique,
redémarre Gateway et relit à la fois son enregistrement de dépôt dans PostgreSQL
et les octets de l’objet. Consignez les preuves en direct et n’utilisez
`make kubernetes-local-down` que lorsque la suppression de l’espace de noms et de
ses données locales de PVC est voulue.

Les constructions sont natives : notarx ne fait jamais de compilation croisée ni
d’émulation (ADR 0016). `make build`, `make docker-build` et `make cli-build`
produisent l’architecture de votre machine (arm64 sur Apple Silicon, amd64 sur
Linux x86_64), et l’autre architecture provient des artefacts de l’intégration
continue, et non d’une construction locale avec `--platform`. Exécutez
`make native-build-check` après avoir touché un Dockerfile, le Makefile,
`scripts/` ou un flux de travail; consultez les
[constructions natives](docs/native-builds.md).

Exécutez `make test` avant de soumettre un changement qui touche le comportement
partagé du moteur. Exécutez `make docs-check` lorsque vous modifiez des points
d’entrée Markdown, des énoncés de capacité ou des ressources locales; la commande
valide les références locales de la documentation et la carte du site de
documentation. Le flux de travail d’intégration continue est la source de vérité
finale pour la matrice obligatoire complète.

### Voies de contrat et preuves de performance

`scripts/ci-contract-lanes.json` est la source unique des voies de contrat : leurs
noms et leur ordre, les outils dont chacune a besoin (`rust`, `node`, `npm`), les
délais des tâches et des étapes, et les artefacts d’échec.
`scripts/ci-contract.py matrix` le transforme en la matrice que `ci.yml` fournit
au flux réutilisable `contract-lane.yml`; aucun des deux flux de travail ne
nomme donc de voie. Ajoutez ou déplacez une voie en modifiant seulement le JSON.

Les tâches `contract` s’exécutent en parallèle avec `fail-fast: false`. La
vérification stable `contract-and-test` (nommée par la barrière de publication,
ADR 0024) exige que chaque voie réussisse; la publication attend aussi la
qualité et la Console. L’ancienne voie `core` en série est scindée en
`core-checks`, `core-email`, `core-native-e2e` et `core-backup`, suivies des voies
natives (clients de migration, inventaire, NuGet, migration et découverte OCI,
authentification en amont privé, migration npm, Helm, RubyGems, migration PyPI).
Chaque voie n’installe que les chaînes d’outils qu’elle déclare, lues dans
`go.mod`, `rust-toolchain.toml` et `.nvmrc`; les réglages de nouvelle tentative et
de délai de npm se trouvent dans `console/.npmrc` et `tools/openapi/.npmrc`.

Exécutez une voie avec `./scripts/ci-local.sh --lane inventory`; `--job contract`
exécute chaque voie ainsi que la vérification locale de la pile CLI. La
vérification locale de fraîcheur d’OpenAPI régénère et compare les empreintes de
fichiers afin que les changements générés non validés restent révisables. Les
contrôles de l’exécuteur de voies s’exécutent avec
`python3 -B -m unittest discover -s scripts/tests -p test_ci_contract.py`. Une
voie en échec téléverse `contract-<lane>-<attempt>` avec les journaux de ses
étapes et les rapports bruts des producteurs. `upgrade-readiness` ne s’exécute pas
à l’intégration continue tant qu’aucune étiquette de version stable de notarx
n’existe (ADR 0015); entre-temps, `make backup-transfer-test` exécute le banc
d’essai de mise à niveau « base de référence plus `000004` synthétique ».

## Conventions de documentation

Chaque document Markdown publié a une route canonique en anglais (orthographe
en-CA) qui utilise le nom de fichier `.md` sans suffixe. Les versions localisées
suivent l'[ADR 0014](docs/adr/0014-locale-policy.md) :

- **en-CA** est la langue de référence et la seule obligatoire.
- **fr-CA** (`.fr-CA.md`, préfixe de route `/fr-CA/`) est de première classe. Le
  noyau `README`, `docs/README`, `docs/getting-started`, `SECURITY` et
  `CONTRIBUTING` exige une version française; les autres pages en français sont
  facultatives.
- **zh-CN** (`.zh-CN.md`, préfixe de route `/zh-CN/`) est maintenu par la
  communauté. Il est figé à l’instantané d’origine : les fichiers existants sont
  conservés et leur structure est vérifiée, les fichiers manquants passent sous
  silence, personne n’y est affecté, la Console se rabat sur en-CA et l’outillage
  d’élagage est conservé. Du nouveau contenu zh-CN est bienvenu de la part d’un
  contributeur de la communauté, mais n’est jamais exigé et ne bloque jamais un
  changement.

Une version localisée renvoie à sa page anglaise et à l’index de la
documentation, et la page anglaise renvoie à elle en retour. Le sélecteur de
langue ne liste que les langues qui existent pour la page.

Les changements fr-CA sont revus par une étape automatisée à l’aide d’un grand
modèle de langage plutôt que par une personne établie au Québec
([#257](https://github.com/mlops-ca/notarx/issues/257)). Elle compare les chaînes
fr-CA modifiées au glossaire aligné sur l’OQLF et aux règles d’écriture, puis
signale les problèmes sur la demande de tirage; le fournisseur se choisit avec
`I18N_REVIEW_PROVIDER` (Claude, Mistral ou Cohere). Un mainteneur approuve quand
même la demande de tirage (CODEOWNERS dirige `*.fr-CA.md` et `*.fr-CA.ts` vers
les mainteneurs). Les ébauches sont marquées dans
`console/src/messages/.review/fr-CA.json`, et un candidat de publication échoue à
`make i18n-release-check` tant qu’une clé fr-CA est `draft` ou qu’une clé
`critical` n’a pas passé la revue. zh-CN n’a pas de barrière de publication.

La revue est `.github/workflows/i18n-review.yml`, qui exécute
`scripts/i18n-review.mjs` sur les demandes de tirage qui touchent `*.fr-CA.ts`,
`*.fr-CA.md` ou `console/src/messages/glossary.ts`. Seuls les éléments suivants
sont envoyés au fournisseur : les chaînes modifiées (avec leur texte en-CA) ou la
prose des documents fr-CA modifiés, le glossaire et les règles d’écriture, jamais
des données du dépôt. Définissez la variable de dépôt `I18N_REVIEW_PROVIDER` à
`anthropic`, `mistral` ou `cohere` et ajoutez sa clé comme secret
`ANTHROPIC_API_KEY`, `MISTRAL_API_KEY` ou `COHERE_API_KEY`; une valeur inconnue
échoue avec une erreur claire. Les constats (termes du glossaire à éviter,
tournures du français de France, typographie, dérive de sens par rapport à en-CA)
apparaissent sous forme d’annotations de vérification et d’un commentaire sur la
demande de tirage, et la vérification échoue lorsqu’une clé `critical` fait
l’objet d’un constat. Les clés qui passent sont consignées comme `reviewed` dans
le fichier annexe, que le flux de travail valide sur la branche de la demande de
tirage. Les demandes de tirage issues d’une bifurcation ne reçoivent aucun
secret : un mainteneur exécute donc le flux de travail avec `workflow_dispatch`
et le numéro de la demande, et le résultat est publié comme statut de commit
`i18n-review`. Le script qui détient la clé provient toujours de la révision de
base, et la tête n’est lue que comme données.

Ajoutez chaque document anglais, et chaque version localisée qui existe, à
`docs/site-map.json` sous l’une des six sections stables. La carte est le contrat
de navigation indépendant des cadriciels d’un futur Docusaurus, VitePress ou
MkDocs; ne maintenez pas de métadonnées propres à un cadriciel comme seconde
autorité de navigation.

Gardez les pages traduites équivalentes sur le plan du comportement (fr-CA et
tout zh-CN existant). Les clés de configuration, les routes, les codes d’état,
les commandes, les limites de compatibilité, l’état d’aperçu et les frontières de
sécurité ne doivent pas disparaître dans une langue. Un court texte de renvoi ou
un lien vers l’autre langue n’est pas un corps traduit.

Exécutez `make docs-check` après avoir ajouté, renommé ou déplacé une page. Il
valide les liens locaux, les paires de langues, les liens réciproques, les
contrôles du corps propres à chaque langue, les titres localisés et la
couverture complète de la carte du site. Consultez le
[guide du site de documentation](docs/documentation-site-guide.md).

## Attentes en matière de tests

- Testez le comportement observable de l’extérieur par une interface HTTP, de
  stockage, de protocole ou de paquet exporté.
- Préférez un comportement ciblé par test et utilisez des attentes littérales
  connues.
- Ajoutez un test de régression avant de corriger un défaut lorsqu’une frontière
  publique stable est disponible.
- Ne couplez pas les tests aux appels d’assistants privés dans le seul but
  d’augmenter la couverture.
- Ne réduisez pas la collecte de couverture à une courte liste de fichiers bien
  testés; les exclusions doivent être du code généré, des bancs d’essai ou de
  l’infrastructure de test.
- Utilisez des tests d’intégration pour les transactions, les migrations, les
  verrous et le comportement du stockage d’objets de PostgreSQL que les magasins
  en mémoire ne peuvent pas démontrer.

## Clés et identifiants typés

Règles (ADR 0023, section 4), appliquées par `make lint` (`sqllint-check`, qui
exécute `go run ./tools/sqllint/cmd`) et par `TestSQLTypedKeyLint` dans
`make test` :

- Ne comparez jamais une colonne uuid convertie en texte à un paramètre
  (`col::text = $1`, `$1::text = col`, `id::text` après `WHERE`) : cela neutralise
  l’index btree. Liez le paramètre comme `uuid.UUID` ou `$n::uuid`, et lisez les
  colonnes uuid dans `uuid.UUID` ou sélectionnez-les avec `col::text` seulement
  dans la liste de sélection.
- Créez les nouveaux identifiants avec `ids.New()` (`internal/ids`, UUIDv7), jamais
  avec `uuid.New()`. Le lint rejette `uuid.New()` et `uuid.NewRandom()` (même sous
  un alias d’importation) partout dans `internal/` hors de `internal/ids`.
  `uuid.NewString()`, aussi un UUIDv4 aléatoire, est compté par fichier dans le
  code hors tests dans `tools/sqllint/allowlist-uuid.txt` et plafonné de la même
  façon : un nouvel appel dans un fichier sans allocation fait échouer le lint;
  utilisez donc `ids.New()` pour les identifiants de ligne. Les appels qui ne sont
  pas des identifiants de ligne (jetons de bail, identifiants de requête et de
  trace, noms temporaires) peuvent rester et conservent leur compte. Rien ne doit
  dépendre de l’ordre des identifiants.
- `$n::uuid` lève SQLSTATE 22P02 pour une chaîne qui n’est pas un uuid, là où
  `col::text = $n` ne trouvait aucune correspondance. Analysez un identifiant
  provenant d’un client (segment de chemin, paramètre de requête ou corps de
  requête) avec `uuid.Parse` à la frontière de l’API et répondez 400 ou 404 à cet
  endroit; ne le laissez pas atteindre une requête de magasin sous forme de
  chaîne libre. Les identifiants émis par le serveur ou lus dans la base de
  données sont déjà valides.
- Les violations existantes sont comptées par fichier dans
  `tools/sqllint/allowlist.txt` et `tools/sqllint/allowlist-uuid.txt`. Les comptes
  ne peuvent que diminuer : une nouvelle violation, ou un compte supérieur à celui
  du fichier, fait échouer le lint, et un compte devenu trop élevé le fait aussi
  échouer; exécutez donc `go run ./tools/sqllint/cmd -update` après avoir retiré
  des conversions.

## Migrations de base de données

Les migrations sont à sens unique et sont appliquées avant le démarrage des
nœuds Gateway. La base de données enregistre à la fois les noms de fichiers des
migrations et leurs sommes de contrôle SHA-256 dans
`notarx_schema_migrations`; un fichier enregistré dont la somme de contrôle a
changé interrompt l’exécution. Validez une migration avec :

```sh
make migration-check
make integration-test
```

La suite d’intégration applique toutes les migrations à une base PostgreSQL
vierge et vérifie qu’une seconde exécution ne change rien.

Règles (ADR 0015) :

- Le schéma part de `migrations/000001_baseline.sql`, un `pg_dump` normalisé. Ne
  le modifiez jamais sur place; les changements de schéma sont de nouveaux
  fichiers `000002_<snake_case>.sql`, `000003_...`, avec un préfixe unique à
  6 chiffres, sans trou.
- À sens unique, par expansion puis contraction : une version peut ajouter des
  objets et remplir des données. Elle ne peut supprimer ou renommer que des objets
  que la version mineure précédente ne lit plus. Il n’y a aucune section Down.
- Les remplissages de données vivent dans des migrations numérotées, jamais dans
  la base de référence. La base de référence n’amorce que les quatre lignes
  singleton qu’attend le code.
- `pg_trgm` est requis (index trigrammes). `make preflight` et l’exécuteur de
  migrations vérifient que l’extension est installée ou peut être créée avant la
  première instruction.
- Avant la première version stable (`vX.Y.Z`, ADR 0024), les bases de données
  sont jetables : la base de référence est régénérée une fois avant l’étiquette,
  et vous recréez votre base de développement. Après cette étiquette, les
  migrations publiées sont immuables.

`make migration-check` (`scripts/migration-immutability-check.sh`) s’exécute à
l’intégration continue et dans `make preflight`. Il rejette toujours les préfixes
en double, hors séquence ou avec des trous, ainsi que les noms qui ne respectent
pas `NNNNNN_<snake_case>.sql`. Une fois qu’une étiquette stable existe, il échoue
aussi lorsqu’un fichier présent à la dernière étiquette stable (l’étiquette la
plus élevée correspondant à `^v[0-9]+\.[0-9]+\.[0-9]+$`; les préversions
nocturnes sont ignorées) diffère de celle-ci. Il a besoin des étiquettes; utilisez
donc un clone complet. `bash scripts/migration-immutability-check-test.sh` exécute
ses bancs d’essai.

Les commandes de cycle de vie de l’intégration utilisent explicitement le projet
Compose `artifact-gateway-integration` et `/dev/null` comme fichier
d’environnement. Elles ignorent le `.env` de la copie de travail et tout
`COMPOSE_PROJECT_NAME` exporté; le nettoyage des tests ne peut donc pas
sélectionner la pile de développement normale. Utilisez
`make integration-down` pour nettoyer ce projet de test.

## Messages de commit

Les demandes de tirage sont fusionnées par écrasement (squash); le titre de la
demande devient donc le sujet du commit. Utilisez un sujet de commit conventionnel
à l’impératif, `type(scope)!: subject` :

```text
feat(oci): add range reads for blobs
fix(console): preserve selected artifact version
test(storage): cover expired worker leases
docs: describe clustered runtime roles
```

Les types, les types qui déclenchent une publication (`feat`, `fix`, `perf`,
`revert`; un `!` ou un pied de page `BREAKING CHANGE:` en déclenche aussi une) et
les portées permises se trouvent dans `.github/commit-policy.json`, la source
unique de la politique
([ADR 0024](docs/adr/0024-release-versioning-and-conventional-commits.md)). La
portée est facultative, on peut en lister plusieurs séparées par des virgules
(`feat(oci,npm): ...`), et en ajouter une revient à modifier une ligne de ce
fichier. Le flux de travail `pr-title` vérifie le titre lorsqu’une demande de
tirage est ouverte, modifiée ou mise à jour; exécutez
`TITLE='feat(oci): x' make commit-policy-check` pour en vérifier un localement.
`fix(security)` et `fix(deps)` publient un correctif; les titres de Dependabot
suivent la politique par `.github/dependabot.yml`.

Choisissez la portée utile la plus étroite. Gardez la sortie générée dans le même
commit que le changement de contrat ou de source qui l’a produite. Un changement
au Trivy épinglé ou à l’image de l’analyseur est un `build(scanner)` et exige un
réviseur issu de `CODEOWNERS`
([ADR 0030](docs/adr/0030-sbom-generation-and-vulnerability-scanning.md)).
