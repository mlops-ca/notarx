import type { remoteInstancesEnCA } from "./remote-instances.en-CA";

export const remoteInstancesFrCA: Record<
  keyof typeof remoteInstancesEnCA,
  string
> = {
  "remoteInstances.title": "Instances distantes",
  "remoteInstances.description":
    "Autres instances notarx vers lesquelles cette instance peut promouvoir. Chaque enregistrement est vérifié auprès du document d’instance connu de l’instance distante.",
  "remoteInstances.add": "Ajouter une instance distante",
  "remoteInstances.edit": "Modifier l’instance distante",
  "remoteInstances.editRow": "Modifier {name}",
  "remoteInstances.testRow": "Tester {name}",
  "remoteInstances.deleteRow": "Supprimer {name}",
  "remoteInstances.name": "Nom",
  "remoteInstances.baseUrl": "URL de base",
  "remoteInstances.baseUrlHint":
    "L’adresse https de l’instance notarx distante. Les réseaux privés sont refusés sauf si l’exploitant les autorise.",
  "remoteInstances.instanceId": "Identifiant d’instance",
  "remoteInstances.auth": "Authentification",
  "remoteInstances.auth.workload_identity": "Identité de charge de travail",
  "remoteInstances.auth.static_token": "Jeton statique",
  "remoteInstances.audience": "Audience",
  "remoteInstances.audienceHint":
    "L’audience de l’assertion de charge de travail émise pour cette instance distante.",
  "remoteInstances.token": "Jeton",
  "remoteInstances.tokenHint": "Stocké chiffré et jamais affiché de nouveau.",
  "remoteInstances.tokenReplaceHint":
    "Un jeton est configuré. Saisissez une nouvelle valeur pour le remplacer, ou laissez vide pour le conserver.",
  "remoteInstances.tokenConfigured": "Jeton configuré",
  "remoteInstances.tokenNotConfigured": "Aucun jeton",
  "remoteInstances.trustEvidence":
    "Faire confiance aux preuves de provenance de cette instance distante",
  "remoteInstances.enabled": "Activée",
  "remoteInstances.stateUnreachable": "Injoignable",
  "remoteInstances.state": "État",
  "remoteInstances.lastChecked": "Dernière vérification",
  "remoteInstances.actions": "Actions",
  "remoteInstances.editAction": "Modifier",
  "remoteInstances.test": "Tester",
  "remoteInstances.delete": "Supprimer",
  "remoteInstances.create": "Ajouter",
  "remoteInstances.save": "Enregistrer",
  "remoteInstances.deleteTitle": "Supprimer l’instance distante",
  "remoteInstances.deleteMessage":
    "Supprimer {name} ? Les règles de promotion qui la ciblent cessent de fonctionner.",
  "remoteInstances.loading": "Chargement des instances distantes",
  "remoteInstances.emptyTitle": "Aucune instance distante",
  "remoteInstances.emptyHint":
    "Ajoutez une instance distante pour promouvoir des artefacts vers un autre notarx.",
  "remoteInstances.loadMore": "Charger plus",
  "remoteInstances.testReachable": "{name} est joignable",
  "remoteInstances.testUnreachable": "{name} est injoignable",
  "remoteInstances.testDetail":
    "Identifiant d’instance {instanceId}, API {apiVersion}, formats : {formats}.",
  "remoteInstances.testFailed": "Raison : {error}",
};
