# Politique de sécurité

[English](SECURITY.md) | [Index de la documentation](docs/README.fr-CA.md)

notarx manipule des justificatifs d’authentification, des permissions
de dépôt, des métadonnées de paquets non fiables et des octets d’artefacts
exécutables. Les rapports de sécurité doivent donc être traités en privé jusqu’à
ce qu’un correctif et un plan de divulgation existent.

## Signaler une vulnérabilité

N’ouvrez pas de ticket public pour une vulnérabilité soupçonnée. Utilisez le canal
privé de signalement des vulnérabilités de l’hébergeur du dépôt lorsqu’il sera
offert, ou communiquez avec les mainteneurs par le canal privé du projet que
l’équipe principale utilise.

Incluez, si possible :

- la révision ou la version de déploiement touchée;
- le protocole, la route d’API ou le composant touché;
- les prérequis et les étapes de reproduction;
- l’incidence sur la sécurité et la violation attendue de la frontière de
  confiance;
- des journaux ou une preuve de concept minimale, après avoir retiré les
  justificatifs et le contenu des artefacts.

Ne faites pas d’essais sur des systèmes ou des données qui ne vous appartiennent
pas. N’incluez dans un rapport aucun jeton actif, mot de passe, contenu de paquet
privé ou renseignement personnel.

## Processus de réponse

Les mainteneurs valideront le rapport, détermineront les révisions touchées,
prépareront des tests et un correctif, puis coordonneront la divulgation une fois
que les exploitants disposeront d’une voie de mise à niveau réaliste. Les délais
d’accusé de réception et de correction ne sont pas encore publiés, car le projet
n’a pas lancé de programme de soutien public.

## Clé de signature des paquets

Les dépôts natifs apt et yum de `packages.mlops.ca` sont signés par la clé OpenPGP
`notarx packages <packages@mlops.ca>` (clé primaire RSA 4096 à certification
seule, conservée hors ligne, et sous-clés de signature RSA 4096 d’une validité de
deux ans). La cérémonie des clés, leur garde, leur rotation et leur révocation
sont décrites dans [docs/release/packages-signing.md](docs/release/packages-signing.md).
Vérifiez `https://packages.mlops.ca/gpg.key` à l’aide de l’empreinte ci-dessous
avant de lui faire confiance.

Empreinte de la clé primaire : pas encore émise. La clé de production n’a pas
été créée, de sorte qu’aucun dépôt signé n’est publié et qu’il ne faut faire
confiance à aucun `gpg.key` servi avant que cette ligne n’indique une empreinte de
40 caractères. La liste de vérification de la cérémonie remplace cette ligne par
l’empreinte dans le même changement qui publie `gpg.key`.

## Base de référence de sécurité actuelle

- Le trafic de production doit se terminer en TLS au Gateway ou à un mandataire
  inverse de confiance. Les exemples en HTTP simple de ce dépôt sont destinés au
  développement local.
- Remplacez chaque secret de remplacement de `.env.example` et des exemples
  Compose.
- Conservez `NOTARX_ADMIN_TOKEN`, les jetons du résolveur, les clés d’API, les
  justificatifs de la base de données, les justificatifs du stockage d’objets et
  `NOTARX_EGRESS_PROXY_KEY` dans un gestionnaire de secrets.
- Utilisez des comptes de service pour l’intégration continue et les applications
  tierces. Liez les autorisations de dépôt au principal stable du compte, faites
  la rotation de ses justificatifs à usage unique avec une fenêtre de
  chevauchement, et désactivez le compte pour rejeter tous les justificatifs
  pendant la réponse à un incident. Consultez `docs/service-account-operations.md`.
- Exécutez les migrations de base de données comme tâche de déploiement distincte
  et n’accordez pas aux nœuds Gateway ordinaires les justificatifs du
  propriétaire du schéma.
- Restreignez les hôtes amont des dépôts mandataires et l’accès réseau sortant.
  Les mots de passe de mandataire personnalisés par dépôt exigent
  `NOTARX_EGRESS_PROXY_KEY`.
- Traitez les paquets et les métadonnées téléversés comme des entrées non fiables.
  notarx n’offre pas d’analyse de maliciels. L’analyse des
  vulnérabilités, des licences et des SBOM n’est offerte que lorsqu’un
  exploitant active explicitement l’analyseur de référence fourni ou un analyseur
  externe conforme au contrat.
- N’exposez les routes d’exploitation et de gestion qu’aux réseaux et aux
  identités prévus. Les nœuds réservés aux workers et à l’ordonnanceur n’exposent
  volontairement que les points d’accès de vivacité, de disponibilité et de
  métriques.
- Examinez les enregistrements d’audit après les changements de comptes de
  service ou de justificatifs, de permissions, d’accès anonyme, de conservation,
  de suppression, de promotion et de réplication.

Exécutez l’audit des dépendances du dépôt avant un candidat de publication :

```sh
make dependency-audit
```
