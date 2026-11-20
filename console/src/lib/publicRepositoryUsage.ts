import type { Translate } from "./preferences";
import type { UsageSnippet } from "./usage";

export type PublicRepositoryFormat =
  "oci" | "maven" | "conan" | "raw" | "npm" | "pypi" | "go" | "cargo" | "apt";

export interface PublicRepositoryUsageContext {
  /** Hugging Face has usage guidance but no public catalogue card yet. */
  format: PublicRepositoryFormat | "huggingface";
  repositoryName: string;
  origin: string;
  host: string;
  t: Translate;
}

export function publicRepositoryUsage({
  format,
  repositoryName,
  origin,
  host,
  t,
}: PublicRepositoryUsageContext): UsageSnippet[] {
  if (format === "maven") {
    const url = `${origin}/maven/${repositoryName}`;
    return [
      { label: t("lib.mavenRepositoryURL"), code: url },
      {
        label: "settings.xml",
        code: `<repository>\n  <id>${repositoryName}</id>\n  <url>${url}</url>\n</repository>`,
      },
      { label: "Gradle repositories", code: `maven { url = uri("${url}") }` },
    ];
  }
  if (format === "oci") {
    return [
      {
        label: t("lib.oCIRegistryAddress"),
        code: `${host}/${repositoryName}`,
      },
      {
        label: t("lib.dockerRegistrySetup"),
        code: t("lib.dockerLoginImagePrefix", { host, repositoryName }),
      },
    ];
  }
  if (format === "conan") {
    return [
      {
        label: t("lib.conanRemoteAddress"),
        code: `conan remote add ${repositoryName} ${origin}/conan/${repositoryName}`,
      },
    ];
  }
  if (format === "npm") {
    const registry = `${origin}/npm/${repositoryName}/`;
    return [
      { label: t("lib.npmRegistryURL"), code: registry },
      { label: ".npmrc", code: `registry=${registry}` },
    ];
  }
  if (format === "pypi") {
    const index = `${origin}/pypi/${repositoryName}/simple/`;
    return [
      { label: "PyPI Simple API", code: index },
      { label: "pip", code: `pip config set global.index-url ${index}` },
    ];
  }
  if (format === "go") {
    const proxy = `${origin}/go/${repositoryName}`;
    return [
      { label: "GOPROXY", code: `go env -w GOPROXY=${proxy}` },
      {
        label: t("lib.oneOffUsage"),
        code: `GOPROXY=${proxy} go mod download`,
      },
    ];
  }
  if (format === "cargo") {
    const index = `sparse+${origin}/cargo/${repositoryName}/`;
    return [
      { label: t("lib.cargoSparseIndex"), code: index },
      {
        label: ".cargo/config.toml",
        code: `[registries.notarx]\nindex = "${index}"\ncredential-provider = "cargo:token"`,
      },
      {
        label: t("lib.searchAndInstallACrate"),
        code: "cargo search <crate> --registry notarx\ncargo add <crate> --registry notarx\ncargo install <crate> --registry notarx",
      },
    ];
  }
  if (format === "apt") {
    const source = `${origin}/apt/${repositoryName}`;
    return [
      { label: t("lib.aPTSourceURL"), code: source },
      { label: "sources.list", code: `deb ${source} <suite> <component>` },
      {
        label: t("lib.downloadAnArtifact"),
        code: `curl -fsSL ${source}/pool/<component>/<path>/<package>.deb -o package.deb`,
      },
    ];
  }
  if (format === "huggingface") {
    const endpoint = `${origin}/hf/${repositoryName}`;
    return [
      { label: "HF_ENDPOINT", code: `export HF_ENDPOINT=${endpoint}` },
      { label: "HF_TOKEN", code: "export HF_TOKEN=<token>" },
    ];
  }
  return [
    {
      label: t("lib.rawRepositoryAddress"),
      code: `${origin}/raw/${repositoryName}/`,
    },
  ];
}
