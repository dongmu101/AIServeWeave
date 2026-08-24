// Package workflow is the Gateway's catalogue of admin-registered ComfyUI
// workflow templates, and the binding of caller-supplied inputs into them.
//
// It exists because of one line in README.md's 工作流模板 section: 平台不应允许
// 普通 API 调用者随意修改整个节点图. A caller names a template and supplies
// values for the inputs that template declares; nothing else about the graph
// is reachable from the public API. A template that binds an input to a node
// or field it does not have is rejected when the catalogue is loaded, so a
// misdeclared template fails on the operator's terminal rather than on a
// caller's request.
//
// Package workflow 是 Gateway 这一侧由管理员注册的 ComfyUI 工作流模板目录，以及把
// 调用方给出的输入绑定进模板的那一步。
//
// 它的存在源于 README.md「工作流模板」一节的一句话：平台不应允许普通 API 调用者随意
// 修改整个节点图。调用方只能指名一个模板，并为该模板声明过的输入提供取值；图的其余
// 部分从公开 API 完全够不着。模板若把某个输入绑到并不存在的节点或字段上，会在加载目录
// 时就被拒绝，因此声明错误的模板挂在运维的终端上，而不是挂在调用方的请求上。
package workflow
