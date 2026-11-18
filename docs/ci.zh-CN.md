# CI 路径分类与 ci-ok 门禁

[English](ci.md) | [文档索引](README.zh-CN.md)

CI 只运行变更所需的作业。`.github/workflows/ci.yml` 中的 `changes` 作业对拉取请求
涉及的文件运行 `scripts/ci-changes.sh`，得出五个类别；只有对应类别变更时才运行重型作业。

| 类别 | 路径 | 启用的作业 |
| --- | --- | --- |
| `go` | `*.go`、`go.mod`、`go.sum`、`migrations/`、`internal/`、`cmd/`、`contracts/`、`examples/`、`api/`，以及 `release-version-check` 读取的文件（`CHANGELOG*.md`、`docs/cli*.md`、`VERSION`） | `quality`、`contract-core`、`contract-native`、`contract-and-test`，以及 `console-e2e`（它运行真实网关） |
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

`contract-core` 是由四个通道（`core-checks`、`core-email`、`core-native-e2e`、`core-backup`）组成的矩阵，
与十一个 `contract-native` 通道并行运行（不再设置 `max-parallel`，受托管运行器并发上限约束）。
原先串行的 `core` 通道按耗时拆分，完整运行时间由最慢的通道（`core-email`，约 10 分钟）决定，而不再是 34 分钟的串行总和。
原通道的每个步骤都恰好在一个 core 通道中运行；拆分记录在 `scripts/ci-contract-lanes.json` 的 `coreOrder`，
每个通道保留各自的证据与专用运行器 Docker 缓存归属。每个通道带重试地预热 Go 模块缓存，
并通过 `GOPROXY=https://proxy.golang.org|direct` 解析模块，单次代理流错误不再使通道失败。聚合检查仍要求每个通道的证据。

Dockerfile 在构建镜像时不再运行 `go test`：测试套件在 `core-checks`（`make test`）与 `quality` 中只运行一次。
测试位于可选的 `test` 阶段（`make docker-test`），镜像构建时 BuildKit 会跳过它。

CI 不再运行上游的 `upgrade-readiness` 步骤和上游 v0.5.0 标签获取：ADR 0015 不提供来自上游的升级路径，
[#39](https://github.com/mlops-ca/notarx/issues/39) 会把 `scripts/upgrade-readiness.sh` 改为基于 notarx 发布标签。
notarx 发布标签出现后再恢复该步骤。

运行 `bash scripts/ci-changes-test.sh` 可检查分类器用例。
