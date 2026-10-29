# Issue tracker: Local Markdown

本仓库的任务和规格以 Markdown 文件保存在 `.scratch/`。

## 约定

- 每项功能使用一个目录：`.scratch/<feature-slug>/`。
- 规格文件：`.scratch/<feature-slug>/spec.md`。
- 实施任务：`.scratch/<feature-slug>/issues/<NN>-<slug>.md`，
  从 `01` 开始编号，每个任务独立成文件。
- 普通任务的 triage 状态写在文件顶部附近的 `Status:` 行，
  状态字符串见 `triage-labels.md`。
- 评论与讨论历史追加到文件末尾的 `## Comments` 下。

## 技能要求“发布到任务系统”时

规格写入 `spec.md`；实施任务写入对应的 `issues/` 文件。
目录不存在时创建目录。

## 技能要求“获取相关任务”时

读取用户提供的文件路径；只有编号时，在对应功能目录中定位任务。

## Wayfinding 操作

供 `/wayfinder` 使用：

- 地图：`.scratch/<effort>/map.md`，
  保存 Notes、Decisions-so-far 和 Fog。
- 子任务：`.scratch/<effort>/issues/<NN>-<slug>.md`，
  从 `01` 编号，正文记录待解问题。
- 类型：用 `Type:` 行记录 research、prototype、grilling 或 task。
- 生命周期：Wayfinder 子任务使用 `Status: open`、
  `Status: claimed`、`Status: resolved`。
- 阻塞：在顶部附近用 `Blocked by: NN, NN` 记录；
  所有被依赖任务均为 resolved 后解除阻塞。
- 前沿任务：按编号选择首个 open、无阻塞且未认领的任务。
- 认领：开始工作前，将状态保存为 claimed。
- 完成：在 `## Answer` 下追加答案，将状态改为 resolved，
  并在地图的 Decisions-so-far 中追加结论摘要和相对链接。
