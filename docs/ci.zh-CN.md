# CI 路径分类与 ci-ok 门禁

[English](ci.md) | [文档索引](README.zh-CN.md)

CI 只运行变更所需的作业。`.github/workflows/ci.yml` 中的 `changes` 作业对拉取请求
涉及的文件运行 `scripts/ci-changes.sh`，得出五个类别；只有对应类别变更时才运行重型作业。

| 类别 | 路径 | 启用的作业 |
| --- | --- | --- |
| `go` | `*.go`、`go.mod`、`go.sum`、`migrations/`、`internal/`、`cmd/`、`contracts/`、`examples/`、`api/` | `quality`、`contract-core`、`contract-native`、`contract-and-test` |
| `console` | `console/`、`api/`（生成的客户端） | `console`（单元测试、构建、视觉、E2E） |
| `docs` | `docs/`、`*.md`、`LICENSE`、`NOTICE` | `docs`（中英文对应、链接、能力声明） |
| `images` | `Dockerfile*`、`compose*.yml`、`deploy/` | 所有执行产品的作业（含 `go` 与 `console`） |
| `ci` | `.github/`、`Makefile`、`scripts/` | 全部 |

不属于任何类别的路径同样运行全部作业，因此新增顶层目录不会悄悄跳过作业。推送到
`main`、打标签以及无法解析范围时运行全部作业，包括主线制品与镜像发布。
`pr-title`（Conventional Commit 标题，ADR 0024）在每个拉取请求上运行。

因此仅文档的拉取请求只运行 `changes`、`pr-title`、`docs` 与 `ci-ok`；仅 Go 的拉取请求跳过
`console`；仅控制台的拉取请求跳过所有契约通道。

## ci-ok 门禁

`ci-ok` 依赖所有其他作业并始终运行：每个被依赖的作业成功或因变更检测被跳过时通过，任何失败或取消则失败。
请把 `ci-ok` 设为唯一必需的分支保护检查，其背后的作业集合变化时无需修改保护规则。

## 契约通道

`contract-core` 与十一个 `contract-native` 通道并行运行（不再设置 `max-parallel`，受托管运行器并发上限约束）。
每个通道带重试地预热 Go 模块缓存，并通过 `GOPROXY=https://proxy.golang.org|direct` 解析模块，
单次代理流错误不再使通道失败。通道证据与聚合检查保持不变。

运行 `bash scripts/ci-changes-test.sh` 可检查分类器用例。
