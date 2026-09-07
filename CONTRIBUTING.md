# 贡献指南

感谢你对 AIServeWeave 感兴趣。本文说明如何提交贡献；跨仓库的开发约定（代码地图、契约唯一源、质量门禁、编码约定、安全红线、分层职责）见 [AGENTS.md](AGENTS.md)，提交前请先读一遍，本文不重复其内容。

## 开始之前

- 提交较大改动（新功能、架构调整）前，先开一个 issue 讨论方案，避免实现完成后因方向不符被拒。
- 小的 bug 修复、文档修正可以直接提 PR。
- 动手前确认目标服务的当前状态：[STATUS.md](STATUS.md) 是跨服务任务安排的唯一入口，标注了哪些能力已完成、哪些还是骨架；不要在骨架服务里假设已有的包。

## 开发环境

- Go 1.27。
- 修改 `api/proto/tunnel/v1/tunnel.proto` 需要重新生成代码：`go generate ./api/...`（需先安装 `protoc-gen-go` 与 `protoc-gen-go-grpc`，见 [generate.go](api/proto/tunnel/v1/generate.go)）。生成代码（`*.pb.go`）不手工编辑。
- Console 子项目（`service/aiServeWeaveConsole/`）有独立的依赖安装与测试方式，见其 [AGENTS.md](service/aiServeWeaveConsole/AGENTS.md)。

## 提交前必须跑通的检查

Go 代码改动：

```bash
gofmt -l ./service ./api      # 必须无输出
go vet ./...
go build ./...                # 三个服务入口都必须能链接
go generate ./api/...         # 结果须与仓库一致，有 diff 说明没重新生成或工具版本不对
go test ./...
go test -race ./service/...
```

Console 改动额外执行其专属门禁（lint、typecheck、test、build），见 Console AGENTS.md。

默认测试不依赖真实 Gateway、GPU、外部网络或真实数据库；需要真实后端的测试按仓库约定独立隔离（例如按环境变量启用），不要让它们拖累默认 `go test ./...`。

## 提交规范

- Commit message 说明改动的原因（why），不只是做了什么。
- 改公共 API（proto 契约、控制面/Gateway 对外接口、Console 白名单转发）时同步更新对应文档，不要留到"以后补"——README 与代码不一致视为缺陷。
- 涉及编码约定的部分（导出标识符的双语 doc comment、表驱动测试、注入的 `runtime.Clock`、协程泄漏检查等）见 AGENTS.md「编码约定」一节。
- 涉及 API Key、鉴权头、Prompt、工作流 JSON 等敏感信息处理，遵守 AGENTS.md「安全红线」一节；不确定是否触及安全边界时，在 PR 描述里说明。

## Pull Request 流程

1. Fork 仓库，基于 `main` 建分支。
2. 完成改动并跑通上述质量门禁。
3. 提交 PR，描述改动动机、影响范围和测试证据（哪些命令跑过、结果如何）。
4. 涉及路线图任务（`STATUS.md` 中的编号，如 J01、S01、R01）时在 PR 中关联对应编号，方便追踪验收进度。
5. 维护者会做代码评审；请求的改动请在原分支上追加提交，不要强推覆盖评审历史（除非维护者明确要求）。

## 报告 Bug 与提功能建议

在 GitHub Issues 提交，说明复现步骤、期望行为与实际行为，以及相关服务（Agent/Gateway/Registry/ControlPlane/Console）。安全漏洞不要走公开 issue，见 [SECURITY.md](SECURITY.md)。

## 行为准则

保持讨论专业、尊重不同背景的贡献者。以事实和技术依据为基础提出异议，不做人身攻击。
