## 开始工作

1. 涉及交付范围、待办或验收时，再读 [STATUS.md](STATUS.md) 与目标服务的 `STATUS.md`。区分代码已交付、默认测试通过和真实环境已验收。
2. 按下表读取当前改动需要的文档。已有实现以当前代码为准；发现文档不一致时，在本次涉及范围内同步修正。公共 API、配置与行为变更须同步对应文档。
3. 修改前确认目标包已有的实现、测试与调用边界；尚未实现的能力以 STATUS 为依据，不根据目录名或历史设计假定已经可用。

## Agent skills

### Issue tracker

任务与规格以 Markdown 文件保存在 `.scratch/<feature-slug>/`。见 `docs/agents/issue-tracker.md`。

### Triage labels

使用默认五个角色字符串：`needs-triage`、`needs-info`、`ready-for-agent`、`ready-for-human`、`wontfix`。见 `docs/agents/triage-labels.md`。

### Domain docs

single-context：根目录 `GLOSSARY.md` 与 `docs/adr/`。见 `docs/agents/domain.md`。
