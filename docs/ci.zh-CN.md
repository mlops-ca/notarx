# CI 路径分类与 ci-ok 门禁

[English](ci.md) | [文档索引](README.zh-CN.md)

CI 只运行变更所需的作业。`.github/workflows/ci.yml` 中的 `changes` 作业对拉取请求
涉及的文件运行 `scripts/ci-changes.sh`，得出五个类别；只有对应类别变更时才运行重型作业。

| 类别 | 路径 | 启用的作业 |
| --- | --- | --- |
| `go` | `*.go`、`go.mod`、`go.sum`、`migrations/`、`internal/`、`cmd/`、`contracts/`、`examples/`、`api/`，以及 `release-version-check` 读取的文件（`CHANGELOG*.md`、`docs/cli*.md`、`VERSION`） | `quality`、`contract-plan`、`contract`、`contract-and-test`，以及 `console-e2e`（它运行真实网关） |
| `console` | `console/`、`api/`（生成的客户端） | `console`（单元测试、构建、视觉）与 `console-e2e` |
| `docs` | `docs/`、`*.md`、`LICENSE`、`NOTICE` | `docs`（中英文对应、链接、能力声明） |
| `images` | `Dockerfile*`、`compose*.yml`、`deploy/` | 所有执行产品的作业（含 `go` 与 `console`） |
| `ci` | `.github/`、`Makefile`、`scripts/` | 全部 |

不属于任何类别的路径同样运行全部作业，因此新增顶层目录不会悄悄跳过作业。推送到
`main`、打标签以及无法解析范围时运行全部作业，包括主线制品与镜像发布。
`pr-title`（Conventional Commit 标题，ADR 0024）在每个拉取请求上运行。

因此仅文档的拉取请求只运行 `changes`、`pr-title`、`docs` 与 `ci-ok`；仅 Go 的拉取请求跳过
`console`（单元、构建、视觉）但仍运行 `console-e2e`；仅控制台的拉取请求跳过所有契约通道。

## ci-ok 门禁

`ci-ok` 依赖所有其他作业并始终运行：每个被依赖的作业成功或因变更检测被跳过时通过，任何失败或取消则失败。
请把 `ci-ok` 设为唯一必需的分支保护检查，其背后的作业集合变化时无需修改保护规则。

## 契约通道

`scripts/ci-contract-lanes.json` 是唯一给出 lane 名称、所需工具与超时的位置。`contract-plan` 作业把它转换为矩阵
（`scripts/ci-contract.py matrix`），`contract` 为每个条目并行运行一个可复用的 `contract-lane.yml` 作业
（不设置 `max-parallel`，受托管运行器并发上限约束）。原先约 34 分钟的串行 `core` lane 按耗时拆为四条，
完整运行时间由最慢的 lane 决定而不是总和。原 lane 的每个步骤都恰好在一条 lane 中运行，唯一移除的是 `upgrade-readiness`（见下）。
`contract-and-test` 保留为发布门禁引用的稳定汇总，要求所有 lane 成功。

每条 lane 使用默认浅克隆，带重试地预热一次 Go 模块缓存（`GOPROXY=https://proxy.golang.org|direct`），
并只安装它声明需要的工具：Rust 取自 `rust-toolchain.toml`，Node 取自 `.nvmrc`，Go 取自 `go.mod`，
仅运行 OpenAPI 或 Console 代码生成的 lane 才安装 npm 依赖。npm 超时设置位于 `console/.npmrc` 与 `tools/openapi/.npmrc`。
作业运行在 `ubuntu-24.04`，`timeout-minutes` 取自计划。失败的 lane 会上传步骤日志与原始报告。

Dockerfile 在构建镜像时不再运行 `go test`：测试套件在 `core-checks`（`make test`）与 `quality` 中只运行一次。
测试位于可选的 `test` 阶段（`make docker-test`；Dockerfile 或 compose 变更时由 `image-test-stages` 运行），镜像构建时 BuildKit 会跳过它。

CI 不再获取上游 v0.5.0 标签，也不再运行 `upgrade-readiness`：ADR 0015 不提供来自上游的升级路径，
[#39](https://github.com/mlops-ca/notarx/issues/39) 会把 `scripts/upgrade-readiness.sh`（及其 make 目标，予以保留）改为基于 notarx 发布标签。
notarx 发布标签出现后再恢复该步骤。

运行 `bash scripts/ci-changes-test.sh` 可检查分类器用例。
