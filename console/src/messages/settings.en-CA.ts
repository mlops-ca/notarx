export const settingsEnCA = {
  "settings.cancel": "Cancel",
  "settings.saveAndApply": "Save and apply",
  "settings.authorizationCache": "Authorization cache",
  "settings.authorizationCacheDescription":
    "Each node caches API keys and service-account state in memory. A revoked API key, a disabled service account or a revoked service-account credential is effective on every node within this window, even when a change notification is lost.",
  "settings.authorizationCacheWindow": "Revocation window",
  "settings.authorizationCacheSeconds": "{seconds} s",
  "settings.authorizationCacheDisabled":
    "Caching disabled: effective immediately",
  "settings.authorizationCacheRegulatedProfile":
    "Regulated profile: set NOTARX_AUTHZ_CACHE_TTL=1s, or 0 where no staleness is acceptable, at the cost of more database queries per request. The setting changes only by restarting the node.",
  "settings.brandPreview": "Brand preview",
  "settings.livePreview": "Live preview",
  "settings.untitledSite": "Untitled site",
  "settings.thisPreviewShowsTheIdentityLockupOnlyOther":
    "This preview shows the identity lockup only; other users are unaffected until you save.",
  "settings.dark": "Dark",
  "settings.light": "Light",
  "settings.uploaded": "Uploaded",
  "settings.directory": "Directory",
  "settings.builtIn": "Built in",
  "settings.useAsDefault": "Use as default",
  "settings.deleteThisUploadedTheme": "Delete this uploaded theme?",
  "settings.thisCannotBeUndoneUploadThePackageAgain":
    "This cannot be undone; upload the package again to restore it.",
  "settings.delete": "Delete",
  "settings.disableTheThemeAndSaveSiteSettingsFirst":
    "Disable the theme and save site settings first",
  "settings.replaceUploadedTheme": "Replace uploaded theme",
  "settings.installThemePackage": "Install theme package",
  "settings.replaceTheme": "Replace theme",
  "settings.iDUnavailable": "ID unavailable",
  "settings.installAndEnable": "Install and enable",
  "settings.themePackageFailed": "Theme package failed",
  "settings.validationPassedReadyToInstall":
    "Validation passed; ready to install",
  "settings.thisIDBelongsToAnUploadedThemeA":
    "This ID belongs to an uploaded theme; a new version will replace it",
  "settings.thisIDBelongsToABuiltInOr":
    "This ID belongs to a built-in or directory theme and cannot be overwritten",
  "settings.name": "Name",
  "settings.mode": "Mode",
  "settings.file": "File",
  "settings.theServerStrictlyParsesThePackageAgainstV1":
    "The server strictly parses the package against v1. After installing a new theme, save site settings to make it available to users.",
  "settings.theThemePackageMustBeAJSONFile":
    "The theme package must be a JSON file between 1 byte and 256 KiB",
  "settings.theThemePackageIsNotValidJSON":
    "The theme package is not valid JSON",
  "settings.themeInstalledAndAddedToTheEnabledSet":
    "Theme installed and added to the enabled set; save and apply to make it available to users",
  "settings.themeInstalledThe32ThemeEnabledLimitHas":
    "Theme installed; the 32-theme enabled limit has been reached, so disable another theme first",
  "settings.themePackageReplacedEnabledThemesWillUseThe":
    "Theme package replaced; enabled themes will use the new version when settings reload",
  "settings.uploadedThemeDeleted": "Uploaded theme deleted",
  "settings.siteSettingsAreLive": "Site settings are live",
  "settings.onlyPNGJPEGOrWebPImagesAreSupported":
    "Only PNG, JPEG, or WebP images are supported",
  "settings.theLogoMustBeNoLargerThan192":
    "The logo must be no larger than 192 KiB",
  "settings.couldNotReadTheLogo": "Could not read the logo",
  "settings.siteSettings": "Site settings",
  "settings.manageTheSiteNameAndBrandIdentityUsed":
    "Manage the site name and brand identity used by sign-in, public browse, and Console navigation.",
  "settings.brandIdentity": "Brand identity",
  "settings.changesApplyToThisSessionImmediatelyAndAre":
    "Changes apply to this session immediately and are publicly readable before sign-in.",
  "settings.siteName": "Site name",
  "settings.shownInNavigationSignInPublicBrowseAnd":
    "Shown in navigation, sign-in, public browse, and the browser title.",
  "settings.brandMark": "Brand mark",
  "settings.18CharactersUsedWhenNoLogoIs":
    "1–8 characters; used when no logo is set or the image cannot load.",
  "settings.siteLogo": "Site logo",
  "settings.uploadLogo": "Upload logo",
  "settings.upload": "Upload",
  "settings.removeLogo": "Remove logo",
  "settings.useASameOriginPathHTTPSURLOr":
    "Use a same-origin path, HTTPS URL, or upload a PNG, JPEG, or WebP up to 192 KiB. A square image with a transparent background works best.",
  "settings.consoleThemes": "Console themes",
  "settings.enableTheThemesUsersMayChooseAndSelect":
    "Enable the themes users may choose and select the default for first-time visitors. All theme colours are resolved from Ant Design tokens.",
  "settings.uploadThemePackage": "Upload theme package",
  "settings.uploadAThemePackageV1MatchingThemesConsole":
    "Upload a Theme Package v1 matching themes/console-theme.schema.json to install it dynamically. Packages contain bounded colour tokens only—no CSS, scripts, or external assets. Operator directory themes remain available through gateway theme commands.",
  "settings.discardChanges": "Discard changes",
  "settings.logoUrlPlaceholder": "/assets/logo.webp or https://…",
};

export type SettingsMessageKey = keyof typeof settingsEnCA;
