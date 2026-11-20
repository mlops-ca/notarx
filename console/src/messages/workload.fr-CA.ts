import type { workloadEnCA } from "./workload.en-CA";

export const workloadFrCA: Record<keyof typeof workloadEnCA, string> = {
  "workload.title": "Identité de charge de travail",
  "workload.description":
    "Permettez aux tâches CI et aux charges de travail de grappe de s’authentifier avec des jetons de courte durée émis par leur propre plateforme, sans secret stocké.",
  "workload.disabledNotice":
    "Vérifiez que NOTARX_WORKLOAD_ENABLED vaut true sur le serveur. Tant qu’il n’est pas activé, les émetteurs et les politiques sont modifiables, mais aucun jeton de charge de travail n’est accepté.",
  "workload.disabledNoticeTitle":
    "L’ouverture de session par charge de travail reste inactive tant que le serveur ne l’active pas",
  "workload.newIssuer": "Nouvel émetteur",
  "workload.newPolicy": "Nouvelle politique",
  "workload.issuers": "Émetteurs de confiance",
  "workload.issuersHint": "Plateformes dont notarx vérifie les jetons signés.",
  "workload.policies": "Politiques de confiance",
  "workload.policiesHint":
    "Conditions sur les revendications qui associent un jeton vérifié à un compte de service.",
  "workload.noIssuers": "Aucun émetteur de confiance pour l’instant",
  "workload.noIssuersHint":
    "Enregistrez d’abord GitHub Actions, GitLab CI, une grappe Kubernetes ou un autre émetteur OIDC.",
  "workload.noPolicies": "Aucune politique de confiance pour l’instant",
  "workload.noPoliciesHint":
    "Une politique associe les jetons d’un émetteur à un compte de service.",
  "workload.loadingIssuers": "Chargement des émetteurs de confiance",
  "workload.loadingPolicies": "Chargement des politiques de confiance",
  "workload.loadMore": "Charger plus",
  "workload.name": "Nom",
  "workload.kind": "Type",
  "workload.issuerUrl": "URL de l’émetteur",
  "workload.issuer": "Émetteur",
  "workload.state": "État",
  "workload.actions": "Actions",
  "workload.serviceAccount": "Compte de service",
  "workload.conditions": "Conditions",
  "workload.singleUse": "Usage unique",
  "workload.tokenLifetime": "Durée du jeton",
  "workload.expires": "Expiration",
  "workload.never": "Jamais",
  "workload.yes": "Oui",
  "workload.no": "Non",
  "workload.seconds": "{count} s",
  "workload.conditionCount": "{count} conditions",
  "workload.kind.github_actions": "GitHub Actions",
  "workload.kind.gitlab_ci": "GitLab CI",
  "workload.kind.kubernetes": "Kubernetes",
  "workload.kind.generic": "OIDC générique",
  "workload.audiences": "Audiences acceptées",
  "workload.audiencesHint": "Une par ligne. Le jeton doit en porter une.",
  "workload.keySource": "Source des clés",
  "workload.keySourceDiscovery":
    "Découverte (récupérer les clés de l’émetteur)",
  "workload.keySourceStatic": "Jeu de clés statique (coller le JWKS)",
  "workload.jwksUri": "URL du jeu de clés",
  "workload.jwksUriHint":
    "Facultatif. Laissez vide pour utiliser le document de découverte.",
  "workload.staticJwks": "Jeu de clés (JWKS)",
  "workload.staticJwksHint":
    "Pour les grappes dont le serveur d’API est privé : kubectl get --raw /openid/v1/jwks",
  "workload.invalidJson": "La valeur n’est pas du JSON valide.",
  "workload.allowPrivateNetwork": "Autoriser les adresses de réseau privé",
  "workload.allowPrivateNetworkHint":
    "Seulement pour les émetteurs auto-hébergés sur un réseau privé.",
  "workload.issuerNameHint": "Un identifiant court comme github-acme.",
  "workload.create": "Créer",
  "workload.cancel": "Annuler",
  "workload.close": "Fermer",
  "workload.refresh": "Actualiser",
  "workload.probe": "Vérifier l’accessibilité",
  "workload.probeTitle": "Accessibilité de l’émetteur",
  "workload.probeReachable": "L’émetteur a répondu et publie des clés.",
  "workload.probeUnreachable": "L’émetteur n’a pas pu être lu.",
  "workload.probeKeys": "Clés",
  "workload.probeAlgorithms": "Algorithmes",
  "workload.disable": "Désactiver",
  "workload.enable": "Activer",
  "workload.delete": "Supprimer",
  "workload.disableIssuerTitle": "Désactiver cet émetteur?",
  "workload.disableIssuerMessage":
    "Les jetons de {name} cessent de s’authentifier dans la fenêtre de révocation.",
  "workload.deleteIssuerTitle": "Supprimer cet émetteur?",
  "workload.deleteIssuerMessage":
    "{name} est supprimé. Il ne peut pas l’être tant que des politiques de confiance l’utilisent.",
  "workload.disablePolicyTitle": "Désactiver cette politique?",
  "workload.disablePolicyMessage":
    "Les jetons que seule la politique {name} accepte cessent de s’authentifier dans la fenêtre de révocation.",
  "workload.deletePolicyTitle": "Supprimer cette politique?",
  "workload.deletePolicyMessage":
    "La politique {name} est supprimée définitivement.",
  "workload.policyName": "Nom de la politique",
  "workload.policyNameHint":
    "C’est aussi le nom d’utilisateur Docker et Maven qui épingle cette politique.",
  "workload.issuerRequired": "Choisissez un émetteur",
  "workload.serviceAccountRequired": "Choisissez un compte de service",
  "workload.template": "Modèle de départ",
  "workload.templateHint":
    "Remplit les conditions qu’exige ce type d’émetteur. Remplacez chaque valeur d’exemple.",
  "workload.conditionsJson": "Conditions (JSON)",
  "workload.conditionsHint":
    "Une liste de revendication, opérateur et valeurs. Toutes les conditions doivent être vraies.",
  "workload.conditionsInvalid": "Les conditions doivent former une liste JSON.",
  "workload.maxTtl": "Durée maximale du jeton en secondes",
  "workload.maxTtlHint":
    "Plafonne le jeton notarx que renvoie un échange de jeton.",
  "workload.singleUseLabel":
    "Accepter chaque jeton une seule fois (usage unique)",
  "workload.singleUseHint":
    "Exige une revendication jti dans le jeton. Recommandé pour les politiques utilisées seulement par l’échange de jeton.",
  "workload.exchangeOnly": "Utilisée seulement par l’échange de jeton",
  "workload.exchangeOnlyHint":
    "Active l’usage unique, car un échange n’a besoin du jeton qu’une fois.",
  "workload.sampleToken": "Jeton d’exemple",
  "workload.sampleTokenHint":
    "Requis pour un émetteur générique afin de vérifier que ses jetons portent un jti. Il n’est pas conservé.",
  "workload.allowAnyRef":
    "Autoriser toute branche, étiquette ou tout environnement",
  "workload.allowAnyRefHint":
    "Confirme qu’aucune condition de réf., d’environnement ou de flux de travail n’est voulue.",
  "workload.allowAdmin": "Autoriser un compte de service administrateur",
  "workload.allowAdminHint":
    "Requis lorsque le compte de service détient une autorisation d’administrateur.",
  "workload.template.github_actions": "GitHub Actions : dépôt et branche",
  "workload.template.gitlab_ci": "GitLab CI : projet et branche protégée",
  "workload.template.kubernetes": "Kubernetes : sujet du compte de service",
  "workload.template.generic": "Générique : sujet",
  "workload.tester": "Testeur de politique",
  "workload.testerHint":
    "Collez un jeton ou un ensemble de revendications pour voir quelle politique correspond et quelle condition échoue. Rien n’est enregistré ni consommé.",
  "workload.testerInput": "Jeton ou revendications",
  "workload.testerInputHint": "Un JWT, ou un objet JSON de revendications.",
  "workload.testerIssuer": "Émetteur",
  "workload.testerAnyIssuer": "D’après la revendication iss",
  "workload.run": "Lancer le test",
  "workload.testerMatched": "Politique correspondante : {name}",
  "workload.testerNoMatch": "Aucune politique ne correspond.",
  "workload.testerAmbiguous":
    "Des politiques de comptes de service différents correspondent, donc une vraie ouverture de session est refusée.",
  "workload.testerVerified":
    "La signature a été vérifiée avec les clés de l’émetteur.",
  "workload.testerUnverified": "La signature n’a pas été vérifiée.",
  "workload.testerFailure":
    "{name} : la condition {index} sur {claim} échoue. {reason}",
  "workload.testerPass": "{name} : toutes les conditions sont vraies.",
  "workload.testerWarnings": "Avertissements",
};
