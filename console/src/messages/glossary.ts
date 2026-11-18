/**
 * zh-CN labels, kept as they were when zh-CN was the key source (frozen with the
 * zh-CN dictionaries; ADR 0014). They only feed the zh-CN column of `glossary`.
 *
 * CONTEXT.zh-CN.md writes Promotion as 晋级; the Console has always shown
 * 晋升, and keeps it.
 */
const zhCNTerms: readonly {
  en: string;
  zh: string;
  avoid?: readonly string[];
}[] = [
  { en: "Repository", zh: "仓库", avoid: ["存储库"] },
  { en: "Hosted repository", zh: "Hosted 仓库" },
  { en: "Proxy repository", zh: "Proxy 仓库", avoid: ["镜像仓库"] },
  { en: "Group", zh: "分组", avoid: ["群组", "仓库组"] },
  { en: "Artifact", zh: "制品", avoid: ["工件", "构件"] },
  { en: "Asset", zh: "资产" },
  { en: "Upstream", zh: "上游" },
  { en: "Publication", zh: "发布" },
  { en: "Promotion", zh: "晋升", avoid: ["晋级"] },
  { en: "Replication", zh: "复制" },
  { en: "Tombstone", zh: "墓碑" },
  { en: "Quarantine", zh: "隔离" },
  { en: "Retention policy", zh: "保留策略" },
  { en: "Grant", zh: "授权" },
  { en: "Service account", zh: "服务账号", avoid: ["服务账户"] },
  { en: "Credential", zh: "凭据", avoid: ["凭证"] },
  { en: "API key", zh: "API 密钥" },
  { en: "Audit", zh: "审计" },
  { en: "Webhook delivery", zh: "Webhook 投递" },
];

export type GlossaryLocale = "en-CA" | "fr-CA" | "zh-CN";

export interface GlossaryEntry {
  /** The term as CONTEXT.md names it. */
  term: string;
  labels: { "en-CA": string; "fr-CA"?: string; "zh-CN"?: string };
  /** Synonyms the message test rejects in that locale. */
  avoid?: { "fr-CA"?: readonly string[]; "zh-CN"?: readonly string[] };
}

/** fr-CA labels agreed in ADR 0014; the other terms get one with the fr-CA dictionaries. */
const frCATerms: Record<string, { label: string; avoid?: readonly string[] }> =
  {
    Repository: { label: "dépôt" },
    Artifact: { label: "artefact" },
    Promotion: { label: "promotion" },
    Group: { label: "groupe" },
    Tombstone: { label: "pierre tombale", avoid: ["épitaphe"] },
    Quarantine: { label: "quarantaine" },
    Upstream: { label: "amont" },
    "Proxy repository": { label: "dépôt mandataire" },
    "Service account": { label: "compte de service" },
    Grant: { label: "autorisation", avoid: ["subvention"] },
    "Audit event": { label: "événement d’audit" },
    "Hosted repository": { label: "dépôt hébergé" },
    Asset: { label: "ressource" },
    Publication: { label: "publication" },
    Replication: { label: "réplication" },
    "Retention policy": { label: "politique de conservation" },
    Credential: { label: "justificatif d’identité" },
    "API key": { label: "clé d’API" },
    Audit: { label: "audit" },
    "Webhook delivery": { label: "livraison de webhook" },
  };

/** Terms in en-CA that have no zh-CN counterpart. */
const extraTerms = ["Audit event"];

/**
 * The Console's names for the domain terms in CONTEXT.md, per locale. The
 * message test rejects a message in a locale that uses one of that locale's
 * `avoid` synonyms, so one concept keeps one name across pages.
 */
export const glossary: readonly GlossaryEntry[] = [
  ...zhCNTerms.map(({ en, zh, avoid }): GlossaryEntry => {
    const fr = frCATerms[en];
    return {
      term: en,
      labels: { "en-CA": en, "fr-CA": fr?.label, "zh-CN": zh },
      avoid: {
        ...(fr?.avoid ? { "fr-CA": fr.avoid } : {}),
        ...(avoid ? { "zh-CN": avoid } : {}),
      },
    };
  }),
  ...extraTerms.map((term): GlossaryEntry => {
    const fr = frCATerms[term];
    return {
      term,
      labels: { "en-CA": term, "fr-CA": fr?.label },
      avoid: fr?.avoid ? { "fr-CA": fr.avoid } : {},
    };
  }),
];
