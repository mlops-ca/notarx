import type { providersEnCA } from "./providers.en-CA";

export const providersFrCA: Record<keyof typeof providersEnCA, string> = {
  "providers.title": "Fournisseurs d’identité",
  "providers.description":
    "Fournisseurs d’ouverture de session pour la console et l’API. Les personnes choisissent parmi les fournisseurs activés sur la page de connexion.",
  "providers.add": "Ajouter un fournisseur",
  "providers.loading": "Chargement des fournisseurs d’identité",
  "providers.empty": "Aucun fournisseur d’identité pour l’instant",
  "providers.emptyHint":
    "Ajoutez un fournisseur OpenID Connect ou SAML comme Microsoft Entra ID, Okta ou Keycloak. Les utilisateurs locaux et les jetons continuent de fonctionner.",
  "providers.name": "Nom",
  "providers.protocol": "Protocole",
  "providers.status": "État",
  "providers.provisioning": "Provisionnement",
  "providers.warnings": "Avertissements",
  "providers.actions": "Actions",
  "providers.protocol.oidc": "OpenID Connect",
  "providers.protocol.saml": "SAML 2.0",
  "providers.mode.disabled": "Aucun compte automatique",
  "providers.mode.jit": "Créer les comptes à la première connexion",
  "providers.mode.scim_only": "SCIM seulement",
  "providers.jitRole.none": "En attente d’approbation",
  "providers.jitRole.member": "Membre",
  "providers.enabled": "Activé",
  "providers.disabled": "Désactivé",
  "providers.edit": "Modifier",
  "providers.test": "Tester la connexion",
  "providers.rotateKey": "Renouveler la clé de signature",
  "providers.delete": "Supprimer",
  "providers.cancel": "Annuler",
  "providers.next": "Suivant",
  "providers.back": "Retour",
  "providers.create": "Créer le fournisseur",
  "providers.save": "Enregistrer",
  "providers.none": "Aucun",
  "providers.template.entra": "Microsoft Entra ID",
  "providers.template.okta": "Okta",
  "providers.template.keycloak": "Keycloak",
  "providers.template.generic": "Autre fournisseur",
  "providers.templateHint.entra":
    "Utilise les identifiants du locataire et de l’objet comme clé de connexion stable.",
  "providers.templateHint.okta":
    "Lit la revendication de groupes du jeton d’identité.",
  "providers.templateHint.keycloak":
    "Lit les groupes du domaine dans la revendication de groupes.",
  "providers.templateHint.generic": "Renseignez vous-même chaque paramètre.",
  "providers.wizardTitle": "Ajouter un fournisseur d’identité",
  "providers.editTitle": "Modifier {name}",
  "providers.stepKind": "Fournisseur",
  "providers.stepConnection": "Connexion",
  "providers.stepPolicy": "Politique de connexion",
  "providers.chooseProtocol": "Protocole",
  "providers.chooseTemplate": "Modèle de fournisseur",
  "providers.displayName": "Nom d’affichage",
  "providers.displayNameHint": "Affiché sur le bouton de connexion.",
  "providers.slug": "Identifiant court",
  "providers.slugHint":
    "Lettres minuscules, chiffres et traits d’union. Il fait partie des URL de rappel et ne peut plus changer.",
  "providers.slugInvalid":
    "Utilisez de 2 à 63 lettres minuscules, chiffres ou traits d’union, en commençant par une lettre ou un chiffre.",
  "providers.required": "Obligatoire",
  "providers.issuer": "URL de l’émetteur",
  "providers.issuerHint.entra":
    "https://login.microsoftonline.com/TENANT_ID/v2.0",
  "providers.issuerHint.okta":
    "https://ORGANIZATION.okta.com ou l’URL de son serveur d’autorisation",
  "providers.issuerHint.keycloak": "https://HOST/realms/REALM",
  "providers.issuerHint.generic":
    "L’émetteur du document de découverte du fournisseur",
  "providers.clientId": "Identifiant client",
  "providers.clientSecret": "Secret client",
  "providers.clientSecretKeep":
    "Laissez vide pour conserver le secret enregistré.",
  "providers.clearSecret": "Supprimer le secret client enregistré",
  "providers.audience": "Audience",
  "providers.audienceHint":
    "L’audience des jetons d’identité. Par défaut, l’identifiant client.",
  "providers.redirectUrl": "URI de redirection",
  "providers.redirectUrlHint":
    "Enregistrez cette URL exacte auprès du fournisseur d’identité.",
  "providers.scopes": "Portées",
  "providers.jwksUrl": "URL JWKS",
  "providers.jwksUrlHint":
    "Seulement si le fournisseur ne la publie pas dans son document de découverte.",
  "providers.metadataUrl": "URL des métadonnées",
  "providers.metadataUrlHint.entra":
    "L’URL des métadonnées de fédération de l’application d’entreprise.",
  "providers.metadataUrlHint.okta":
    "L’URL des métadonnées de l’application SAML.",
  "providers.metadataUrlHint.keycloak":
    "https://HOST/realms/REALM/protocol/saml/descriptor",
  "providers.metadataUrlHint.generic":
    "Une URL HTTPS qui sert les métadonnées SAML du fournisseur.",
  "providers.metadataXml": "XML des métadonnées",
  "providers.metadataXmlHint":
    "Ou collez les métadonnées au lieu de donner une URL.",
  "providers.metadataImportFailed":
    "Le fournisseur a été créé, mais ses métadonnées n’ont pas pu être importées. Importez-les de nouveau depuis la boîte de modification du fournisseur.",
  "providers.metadataNeeded":
    "Donnez une URL de métadonnées ou collez le XML des métadonnées.",
  "providers.samlSigning": "Signer les demandes d’authentification",
  "providers.samlNext":
    "Une fois le fournisseur créé, enregistrez les métadonnées du fournisseur de service et l’URL du service consommateur d’assertions auprès de votre fournisseur d’identité, puis activez-le.",
  "providers.provisioningMode": "Comptes",
  "providers.jitDefaultRole": "Rôle des nouveaux comptes",
  "providers.jitDefaultRoleHint":
    "Une première connexion ne crée jamais d’administrateur. Accordez davantage par les groupes d’utilisateurs.",
  "providers.emailLinking": "Lier les comptes par courriel vérifié",
  "providers.emailLinkingHint":
    "Lie une première connexion à un compte existant dont un administrateur a vérifié le courriel. Les administrateurs et les comptes dotés d’un mot de passe local ne sont jamais liés.",
  "providers.enableNow": "Activer le fournisseur",
  "providers.enableNowHint":
    "Un fournisseur désactivé est masqué sur la page de connexion.",
  "providers.created": "{name} a été créé.",
  "providers.saved": "{name} a été enregistré.",
  "providers.testOk": "Connexion réussie en {latencyMs} ms",
  "providers.deleteTitle": "Supprimer le fournisseur d’identité",
  "providers.deleteMessage":
    "Supprimer le fournisseur «\u00a0{name}\u00a0»? Les personnes qui se connectent par son entremise ne le pourront plus, et les groupes qu’il alimentait perdent leur fournisseur.",
  "providers.rotated":
    "Une nouvelle clé de signature a été générée pour {name}. Enregistrez les métadonnées à jour du fournisseur de service auprès du fournisseur d’identité.",
  "providers.spMetadataUrl": "URL des métadonnées du fournisseur de service",
  "providers.acsUrl": "URL du service consommateur d’assertions",
  "providers.certificates": "Certificats de signature épinglés",
  "providers.noCertificates": "Aucun certificat n’est encore épinglé.",
  "providers.certActive": "Actif",
  "providers.certNext": "Suivant (renouvellement)",
  "providers.certExpires": "Expire le {date}",
  "providers.certExpired":
    "Le certificat de signature de {name} a expiré le {date}. Les connexions par ce fournisseur échouent tant qu’un certificat valide n’est pas épinglé.",
  "providers.certExpiresSoon":
    "Le certificat de signature de {name} expire dans {days} jours ({date}). Importez de nouveau les métadonnées du fournisseur, ou épinglez le nouveau certificat.",
  "providers.overageTitle": "{name}\u00a0: revendication de groupes dépassée",
  "providers.overage":
    "Microsoft Entra ID n’a pas envoyé les groupes de certains utilisateurs, car ils appartiennent à trop de groupes; ils se sont connectés sans appartenance à des groupes (dernière occurrence le {date}). Utilisez le provisionnement SCIM, ou émettez des rôles d’application ou seulement les groupes affectés à l’application.",
  "providers.sloTitle": "La déconnexion unique n’est pas prise en charge",
  "providers.slo":
    "La déconnexion met fin à la session notarx seulement; la session du fournisseur d’identité reste ouverte, et s’y déconnecter ne met pas fin à la session notarx. Une session dure jusqu’à 12 heures. Pour couper l’accès plus tôt, désactivez l’utilisateur ou mettez fin à ses sessions.",
  "providers.sloSignOut":
    "La déconnexion met fin à cette session notarx seulement; la session de votre fournisseur d’identité reste ouverte.",
};
