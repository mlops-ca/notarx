import type { settingsEnCA } from "./settings.en-CA";

export const settingsFrCA: Record<keyof typeof settingsEnCA, string> = {
  "settings.requiredWhenOIDCIsEnabled": "Obligatoire lorsque OIDC est activé",
  "settings.clientIDAndRedirectURLMustBeConfigured":
    "L’ID client et l’URL de redirection doivent être configurés ensemble",
  "settings.clientIDIsRequiredWhenAClientSecret":
    "L’ID client est obligatoire lorsqu’un secret client est configuré",
  "settings.providerConnection": "Connexion au fournisseur",
  "settings.forExampleAKeycloakRealmOrGitLabOIDC":
    "Par exemple, un realm Keycloak ou un émetteur OIDC GitLab.",
  "settings.aPIAudience": "Audience de l’API",
  "settings.redirectURL": "URL de redirection",
  "settings.leaveEmptyToUseProviderDiscovery":
    "Laissez vide pour utiliser la découverte du fournisseur.",
  "settings.discoveredAutomatically": "Découvert automatiquement",
  "settings.roleMapping": "Correspondance des rôles",
  "settings.supportsCommonKeycloakRealmClientRolesTopLevel":
    "Prend en charge les rôles de realm et de client Keycloak courants, les rôles de premier niveau et les revendications de groupes. Une correspondance administrateur fait du compte un administrateur; une correspondance membre l’approuve sans accès aux dépôts, lequel provient toujours des autorisations par dépôt. L’administrateur l’emporte si les deux correspondent.",
  "settings.administratorSubjectAllowlist":
    "Liste d’autorisation des sujets administrateurs",
  "settings.useOnlyForAccountsThatRequireAdministratorAccess":
    "À utiliser uniquement pour les comptes qui exigent un accès administrateur, selon la revendication sub stable.",
  "settings.unlinkedIdentities": "Identités non liées",
  "settings.whenDisabledOnlyAdministratorLinkedIdentitiesCanEnter":
    "Lorsque désactivé, seules les identités liées par un administrateur peuvent accéder aux comptes locaux. JIT crée un compte à la première connexion.",
  "settings.keepExternalPrincipal": "Conserver le principal externe",
  "settings.provisionOnFirstSignIn": "Provisionner à la première connexion",
  "settings.jITDefaultRole": "Rôle JIT par défaut",
  "settings.usedOnlyWhenNoExternalRoleMappingMatches":
    "Utilisé uniquement lorsqu’aucune correspondance de rôle externe ne s’applique; aucun inscrit les nouveaux utilisateurs sans accès, membre les inscrit sans accès aux dépôts jusqu’à ce qu’une autorisation soit accordée.",
  "settings.noneAwaitingApproval": "aucun · en attente d’approbation",
  "settings.memberNoRepositoryAccess": "membre · aucun accès aux dépôts",
  "settings.verifiedEmailLinking": "Liaison par courriel vérifié",
  "settings.onlyEmailVerifiedTrueClaimsAreUsedMultiple":
    "Seules les revendications email_verified=true sont utilisées; si plusieurs comptes correspondent, la connexion est refusée.",
  "settings.on": "Activé",
  "settings.off": "Désactivé",
  "settings.enterpriseIdentityProvider": "Fournisseur d’identité d’entreprise",
  "settings.usesAuthorizationCodePKCESessionsRetainOnlyThe":
    "Utilise Authorization Code + PKCE; les sessions ne conservent que l’identité validée et bornée.",
  "settings.on2": "Activé",
  "settings.off2": "Désactivé",
  "settings.clientCredentials": "Justificatifs d’identité du client",
  "settings.theCurrentSecretWillBeRemovedOnSave":
    "Le secret actuel sera supprimé à l’enregistrement. Saisissez une nouvelle valeur pour le remplacer.",
  "settings.configuredLeaveEmptyToRetainTheCurrentSecret":
    "Configuré. Laissez vide pour conserver le secret actuel.",
  "settings.publicPKCEClientsMayOmitASecret":
    "Les clients PKCE publics peuvent omettre le secret.",
  "settings.keepExistingSecret": "Conserver le secret existant",
  "settings.optional": "Facultatif",
  "settings.undoRemoval": "Annuler la suppression",
  "settings.removeClientSecret": "Supprimer le secret client?",
  "settings.theSecretIsNotRemovedUntilSettingsAre":
    "Le secret n’est supprimé qu’une fois les paramètres enregistrés.",
  "settings.markForRemoval": "Marquer pour suppression",
  "settings.cancel": "Annuler",
  "settings.removeSecret": "Supprimer le secret",
  "settings.pendingSave": "Enregistrement en attente",
  "settings.encryptedAtRest": "Chiffré au repos",
  "settings.effectiveContext": "Contexte effectif",
  "settings.source": "Source",
  "settings.version": "Version",
  "settings.otherNodes": "Autres nœuds",
  "settings.convergeWithin5s": "Convergence en 5 s",
  "settings.existingSessions": "Sessions existantes",
  "settings.remainValidUntilLogoutOrExpiry":
    "Restent valides jusqu’à la déconnexion ou l’expiration",
  "settings.currentCallback": "Rappel actuel\u00a0: ",
  "settings.testSavedSettings": "Tester les paramètres enregistrés",
  "settings.saveAndApply": "Enregistrer et appliquer",
  "settings.settingsSynchronized": "Paramètres synchronisés",
  "settings.failedToLoadAuthenticationSettings":
    "Échec du chargement des paramètres d’authentification",
  "settings.failedToSaveAuthenticationSettings":
    "Échec de l’enregistrement des paramètres d’authentification",
  "settings.settingsSavedAndActiveOnThisNode":
    "Paramètres enregistrés et actifs sur ce nœud",
  "settings.identityProviderConnectionFailed":
    "Échec de la connexion au fournisseur d’identité",
  "settings.authentication": "Authentification",
  "settings.runtimeSettings": "Paramètres d’exécution",
  "settings.configureAnExternalOIDCProviderBrowserSignIn":
    "Configurez un fournisseur OIDC externe, la connexion par navigateur et la correspondance des rôles",
  "settings.runtimeStatus": "État d’exécution",
  "settings.enabled": "Activé",
  "settings.disabled": "Désactivé",
  "settings.configurationSource": "Source de configuration",
  "settings.version2": "Version {version}",
  "settings.versionUnknown": "Version inconnue",
  "settings.clientSecret": "Secret client",
  "settings.configured": "Configuré",
  "settings.notConfigured": "Non configuré",
  "settings.neverReturnedByTheAPI": "Jamais retourné par l’API",
  "settings.samlProviders": "Fournisseurs SAML",
  "settings.samlProvidersDescription":
    "Enregistrez les informations du fournisseur de services ci-dessous auprès de chaque fournisseur d’identité. Les certificats de signature épinglés vérifient les réponses de connexion; ajoutez un nouveau certificat comme suivant avant que le fournisseur d’identité ne bascule.",
  "settings.samlMetadataURL": "URL des métadonnées du fournisseur de services",
  "settings.samlAssertionConsumerURL":
    "URL du service de consommation d’assertions",
  "settings.samlPinnedCertificates": "Certificats de signature épinglés",
  "settings.samlNoCertificates": "Aucun certificat n’est encore épinglé.",
  "settings.samlCertificateActive": "Actif",
  "settings.samlCertificateNext": "Suivant (renouvellement)",
  "settings.samlCertificateExpires": "Expire le {date}",
  "settings.samlCertificateExpiresSoon":
    "Un certificat de {name} expire dans {days} jours ({date}). Ajoutez le nouveau certificat avant que le fournisseur d’identité ne bascule.",
  "settings.samlCertificateExpired":
    "Un certificat de {name} a expiré le {date}. Retirez-le ou remplacez-le par le certificat courant.",
  "settings.samlServiceProviderKeys":
    "Clés de signature du fournisseur de services publiées : {count}",
  "settings.authorizationCache": "Cache d’autorisation",
  "settings.authorizationCacheDescription":
    "Chaque nœud conserve en mémoire les justificatifs d’identité, les comptes et les données d’accès. Un justificatif révoqué, un compte désactivé, une autorisation retirée ou un changement de visibilité prend effet sur tous les nœuds dans cette fenêtre, même si une notification de changement est perdue.",
  "settings.authorizationCacheWindow": "Fenêtre de révocation",
  "settings.authorizationCacheSeconds": "{seconds} s",
  "settings.authorizationCacheDisabled":
    "Cache désactivé\u00a0: effet immédiat",
  "settings.authorizationCacheRegulatedProfile":
    "Profil réglementé\u00a0: définissez NOTARX_AUTHZ_CACHE_TTL=1s, ou 0 lorsqu’aucun délai n’est acceptable, au prix d’un plus grand nombre de requêtes à la base de données par requête. Le paramètre ne change qu’au redémarrage du nœud.",
  "settings.lastUpdated": "Dernière mise à jour",
  "settings.notPersisted": "Non persisté",
  "settings.databaseSettingsAreShared":
    "Les paramètres de la base de données sont partagés",
  "settings.connectionSucceededInMs": "Connexion réussie en {latencyMs} ms",
  "settings.brandPreview": "Aperçu de la marque",
  "settings.livePreview": "Aperçu en direct",
  "settings.untitledSite": "Site sans titre",
  "settings.thisPreviewShowsTheIdentityLockupOnlyOther":
    "Cet aperçu montre uniquement le bloc d’identité; les autres utilisateurs ne sont pas touchés tant que vous n’enregistrez pas.",
  "settings.dark": "Sombre",
  "settings.light": "Clair",
  "settings.uploaded": "Téléversé",
  "settings.directory": "Répertoire",
  "settings.builtIn": "Intégré",
  "settings.useAsDefault": "Utiliser par défaut",
  "settings.deleteThisUploadedTheme": "Supprimer ce thème téléversé?",
  "settings.thisCannotBeUndoneUploadThePackageAgain":
    "Cette action est irréversible; téléversez de nouveau le paquet pour le restaurer.",
  "settings.delete": "Supprimer",
  "settings.disableTheThemeAndSaveSiteSettingsFirst":
    "Désactivez d’abord le thème et enregistrez les paramètres du site",
  "settings.replaceUploadedTheme": "Remplacer le thème téléversé",
  "settings.installThemePackage": "Installer un paquet de thème",
  "settings.replaceTheme": "Remplacer le thème",
  "settings.iDUnavailable": "ID indisponible",
  "settings.installAndEnable": "Installer et activer",
  "settings.themePackageFailed": "Échec du paquet de thème",
  "settings.validationPassedReadyToInstall":
    "Validation réussie; prêt à installer",
  "settings.thisIDBelongsToAnUploadedThemeA":
    "Cet ID appartient à un thème téléversé; une nouvelle version le remplacera",
  "settings.thisIDBelongsToABuiltInOr":
    "Cet ID appartient à un thème intégré ou de répertoire et ne peut pas être écrasé",
  "settings.name": "Nom",
  "settings.mode": "Mode",
  "settings.file": "Fichier",
  "settings.theServerStrictlyParsesThePackageAgainstV1":
    "Le serveur analyse strictement le paquet selon la v1. Après l’installation d’un nouveau thème, enregistrez les paramètres du site pour le rendre disponible aux utilisateurs.",
  "settings.theThemePackageMustBeAJSONFile":
    "Le paquet de thème doit être un fichier JSON d’au moins 1 octet et d’au plus 256 Kio",
  "settings.theThemePackageIsNotValidJSON":
    "Le paquet de thème n’est pas un JSON valide",
  "settings.themeInstalledAndAddedToTheEnabledSet":
    "Thème installé et ajouté à l’ensemble activé; enregistrez et appliquez pour le rendre disponible aux utilisateurs",
  "settings.themeInstalledThe32ThemeEnabledLimitHas":
    "Thème installé; la limite de 32 thèmes activés est atteinte, désactivez donc d’abord un autre thème",
  "settings.themePackageReplacedEnabledThemesWillUseThe":
    "Paquet de thème remplacé; les thèmes activés utiliseront la nouvelle version au rechargement des paramètres",
  "settings.uploadedThemeDeleted": "Thème téléversé supprimé",
  "settings.siteSettingsAreLive": "Les paramètres du site sont en vigueur",
  "settings.onlyPNGJPEGOrWebPImagesAreSupported":
    "Seules les images PNG, JPEG ou WebP sont prises en charge",
  "settings.theLogoMustBeNoLargerThan192":
    "Le logo ne doit pas dépasser 192 Kio",
  "settings.couldNotReadTheLogo": "Impossible de lire le logo",
  "settings.siteSettings": "Paramètres du site",
  "settings.manageTheSiteNameAndBrandIdentityUsed":
    "Gérez le nom du site et l’identité de marque utilisés par la connexion, la consultation publique et la navigation de la console.",
  "settings.brandIdentity": "Identité de marque",
  "settings.changesApplyToThisSessionImmediatelyAndAre":
    "Les modifications s’appliquent immédiatement à cette session et sont lisibles publiquement avant la connexion.",
  "settings.siteName": "Nom du site",
  "settings.shownInNavigationSignInPublicBrowseAnd":
    "Affiché dans la navigation, la connexion, la consultation publique et le titre du navigateur.",
  "settings.brandMark": "Emblème de marque",
  "settings.18CharactersUsedWhenNoLogoIs":
    "De 1 à 8 caractères; utilisé lorsqu’aucun logo n’est défini ou que l’image ne se charge pas.",
  "settings.siteLogo": "Logo du site",
  "settings.uploadLogo": "Téléverser un logo",
  "settings.upload": "Téléverser",
  "settings.removeLogo": "Supprimer le logo",
  "settings.useASameOriginPathHTTPSURLOr":
    "Utilisez un chemin de même origine, une URL HTTPS ou téléversez une image PNG, JPEG ou WebP d’au plus 192 Kio. Une image carrée à fond transparent donne les meilleurs résultats.",
  "settings.consoleThemes": "Thèmes de la console",
  "settings.enableTheThemesUsersMayChooseAndSelect":
    "Activez les thèmes que les utilisateurs peuvent choisir et sélectionnez celui par défaut pour les nouveaux visiteurs. Toutes les couleurs des thèmes sont dérivées des jetons Ant Design.",
  "settings.uploadThemePackage": "Téléverser un paquet de thème",
  "settings.uploadAThemePackageV1MatchingThemesConsole":
    "Téléversez un paquet de thème v1 conforme à themes/console-theme.schema.json pour l’installer dynamiquement. Les paquets ne contiennent que des jetons de couleur bornés, sans CSS, script ni ressource externe. Les thèmes du répertoire de l’opérateur restent accessibles par les commandes de thème de la passerelle.",
  "settings.discardChanges": "Abandonner les modifications",
  "settings.logoUrlPlaceholder": "/assets/logo.webp ou https://…",
};
