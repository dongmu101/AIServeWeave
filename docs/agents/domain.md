# Domain Docs

本仓库采用 single-context 领域文档布局。

## 探索代码前读取

- 根目录 `GLOSSARY.md`：领域术语与定义。
- `docs/adr/`：与当前工作相关的架构决策。

文件尚不存在时，直接继续工作。
领域术语或决策明确后，由 `/domain-modeling` 按需创建。

## 文件布局

- `GLOSSARY.md`
- `docs/adr/0001-<decision-slug>.md`
- `docs/adr/0002-<decision-slug>.md`

## 使用统一术语

任务标题、重构建议、诊断假设与测试名称中的领域概念，
使用词汇表定义的名称。

词汇表尚未收录某个概念时，先判断是否有现成术语；
确有缺口时，将其记录为 `/domain-modeling` 的待处理项。

## 明示决策冲突

方案与已有 ADR 冲突时，明确指出对应 ADR、
冲突内容以及建议重新讨论的理由。
