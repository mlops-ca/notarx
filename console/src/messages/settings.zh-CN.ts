export const settingsZhCN = {
  "settings.cancel": "取消",
  "settings.saveAndApply": "保存并应用",
  "settings.brandPreview": "品牌预览",
  "settings.livePreview": "实时预览",
  "settings.untitledSite": "未命名站点",
  "settings.thisPreviewShowsTheIdentityLockupOnlyOther":
    "预览仅展示身份组合；保存前不会影响其他用户。",
  "settings.dark": "深色",
  "settings.light": "浅色",
  "settings.uploaded": "已上传",
  "settings.directory": "目录",
  "settings.builtIn": "内置",
  "settings.useAsDefault": "设为默认",
  "settings.deleteThisUploadedTheme": "删除这个上传主题？",
  "settings.thisCannotBeUndoneUploadThePackageAgain":
    "删除后无法恢复；若仍需使用，需要重新上传主题包。",
  "settings.delete": "删除",
  "settings.disableTheThemeAndSaveSiteSettingsFirst":
    "请先取消启用并保存站点设置",
  "settings.replaceUploadedTheme": "替换上传主题",
  "settings.installThemePackage": "安装主题包",
  "settings.replaceTheme": "确认替换",
  "settings.iDUnavailable": "ID 不可用",
  "settings.installAndEnable": "安装并启用",
  "settings.themePackageFailed": "主题包处理失败",
  "settings.validationPassedReadyToInstall": "校验通过，可以安装",
  "settings.thisIDBelongsToAnUploadedThemeA":
    "该 ID 已由上传主题使用，将创建新版本",
  "settings.thisIDBelongsToABuiltInOr":
    "该 ID 属于内置或目录主题，不能通过上传覆盖",
  "settings.name": "名称",
  "settings.mode": "模式",
  "settings.file": "文件",
  "settings.theServerStrictlyParsesThePackageAgainstV1":
    "主题包将在服务端按严格 v1 规范再次解析；安装新主题后，还需要保存站点设置才会向用户开放。",
  "settings.theThemePackageMustBeAJSONFile":
    "主题包必须是 1 字节到 256 KiB 的 JSON 文件",
  "settings.theThemePackageIsNotValidJSON": "主题包不是有效的 JSON 文件",
  "settings.themeInstalledAndAddedToTheEnabledSet":
    "主题已安装并加入启用列表；请保存并应用后对用户生效",
  "settings.themeInstalledThe32ThemeEnabledLimitHas":
    "主题已安装；启用主题已达到 32 个上限，请先停用其他主题",
  "settings.themePackageReplacedEnabledThemesWillUseThe":
    "主题包已替换；已启用主题将在重新载入设置时使用新版本",
  "settings.uploadedThemeDeleted": "已删除上传主题",
  "settings.siteSettingsAreLive": "站点设置已生效",
  "settings.onlyPNGJPEGOrWebPImagesAreSupported":
    "仅支持 PNG、JPEG 或 WebP 图片",
  "settings.theLogoMustBeNoLargerThan192": "Logo 文件不能超过 192 KiB",
  "settings.couldNotReadTheLogo": "读取 Logo 失败",
  "settings.siteSettings": "站点设置",
  "settings.manageTheSiteNameAndBrandIdentityUsed":
    "统一管理登录页、公开目录和控制台导航中的站点名称与品牌标识。",
  "settings.brandIdentity": "品牌信息",
  "settings.changesApplyToThisSessionImmediatelyAndAre":
    "保存后将立即同步到当前会话，并供未登录页面公开读取。",
  "settings.siteName": "站点名称",
  "settings.shownInNavigationSignInPublicBrowseAnd":
    "显示在导航、登录页、公开目录和浏览器标题中。",
  "settings.brandMark": "品牌标识",
  "settings.18CharactersUsedWhenNoLogoIs":
    "1–8 个字符；未设置 Logo 或 Logo 加载失败时作为回退。",
  "settings.siteLogo": "站点 Logo",
  "settings.uploadLogo": "上传 Logo",
  "settings.upload": "上传",
  "settings.removeLogo": "移除 Logo",
  "settings.useASameOriginPathHTTPSURLOr":
    "支持同源路径、HTTPS 地址，或上传不超过 192 KiB 的 PNG、JPEG、WebP。建议使用透明背景的方形图。",
  "settings.consoleThemes": "Console 主题",
  "settings.enableTheThemesUsersMayChooseAndSelect":
    "启用用户可以选择的主题，并指定首次访问时使用的默认主题。主题颜色统一由 Ant Design token 解析。",
  "settings.uploadThemePackage": "上传主题包",
  "settings.uploadAThemePackageV1MatchingThemesConsole":
    "上传符合 themes/console-theme.schema.json 的 Theme Package v1 即可动态安装；主题包只允许受限颜色 token，不会执行 CSS、脚本或外部资源。运维目录主题仍可通过 gateway theme 命令管理。",
  "settings.discardChanges": "撤销更改",
  "settings.logoUrlPlaceholder": "/assets/logo.webp 或 https://…",
};

export type SettingsMessageKey = keyof typeof settingsZhCN;
