import type { settingsEnCA } from "./settings.en-CA";

export const settingsFrCA: Record<keyof typeof settingsEnCA, string> = {
  "settings.cancel": "Annuler",
  "settings.saveAndApply": "Enregistrer et appliquer",
  "settings.authorizationCache": "Cache d’autorisation",
  "settings.authorizationCacheDescription":
    "Chaque nœud conserve en mémoire les clés d’API et l’état des comptes de service. Une clé d’API révoquée, un compte de service désactivé ou un justificatif de compte de service révoqué prend effet sur tous les nœuds dans cette fenêtre, même si une notification de changement est perdue.",
  "settings.authorizationCacheWindow": "Fenêtre de révocation",
  "settings.authorizationCacheSeconds": "{seconds} s",
  "settings.authorizationCacheDisabled":
    "Cache désactivé\u00a0: effet immédiat",
  "settings.authorizationCacheRegulatedProfile":
    "Profil réglementé\u00a0: définissez NOTARX_AUTHZ_CACHE_TTL=1s, ou 0 lorsqu’aucun délai n’est acceptable, au prix d’un plus grand nombre de requêtes à la base de données par requête. Le paramètre ne change qu’au redémarrage du nœud.",
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
