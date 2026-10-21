# ComfyUI 本地化构建

上游固定为 ComfyUI_frontend `v1.55.17`，commit `7ab461f170c2bba8916faae4cda606278fa367a4`。`manifest.json` 是源码归档、输入文件、补丁、词典和工具链版本的唯一来源。核心节点目录仍为 ComfyUI `0.41.2` 的 678 个节点。

部署使用 `scripts/fetch-comfyui-frontend.sh --localized <空目录>`，要求 Node `v26.8.2` 和 pnpm `11.13.1`。脚本核对官方归档摘要、应用补丁、使用上游锁文件安装依赖、构建 localhost 版本、删除 source map，再生成 `aisw-i18n-manifest.json`。Sentry 上传令牌被清空。`AISW_COMFY_SOURCE_ARCHIVE` 和 `AISW_COMFY_PNPM_STORE` 可复用下载缓存，归档仍须通过摘要校验。旧的不带 `--localized` 模式只用于获取原始发布包，当前控制面不接受它作为八语言编辑器资源。

构建命令为 `pnpm exec vite build --config vite.config.mts`，不包含上游整个 monorepo 的类型检查。2026-10-20 本机执行上游 `pnpm build` 的 `vue-tsc` 阶段被系统终止（137）；本地补丁由离线测试和实际编辑器浏览器验收覆盖，不能将构建成功表述成上游全仓类型检查通过。

德文四组资源在 `de/`；其余语言只在 `supplements/` 记录需要补齐的叶节点。`apply.mjs` 在写入任何源文件前检查八语言结构、非空值、数组、null 和插值参数；未知键、版本或摘要直接失败。Vue-i18n 复数分支数允许随语言变化，参数集合保持一致。

`editor-state.patch` 限于嵌入场景的语言就绪协调、跳过换语时重载图，以及节点默认持久化标题与显示标题分离。显式 `title` / `_meta.title`（即使等于默认值）始终保留为作者值；仅运行时来源标记不进入工作流 JSON。Canvas 与 Vue 头部共用显示解析。初始显示设置使用上游默认的 `Comfy.UseNewMenu=Top`，避免启用未本地化的旧菜单。

修改资源后重新计算 `manifest.json` 对应资源摘要，执行 `node scripts/comfyui-localization/sync-metadata.mjs` 和 `gofmt`。`--check` 检查控制面接收的配置摘要与此清单一致。升级上游版本需要同时审查源补丁、归档、锁文件和核心节点目录，重新完成真实构建及编辑器浏览器验收。不要提交下载源码、node_modules 或 dist。

语言 JSON 与上游补丁派生于 GPL-3.0-only 资源，适用本目录的 `LICENSE`；新增及修订译文随相同许可提供。官方来源为 <https://github.com/Comfy-Org/ComfyUI_frontend/tree/v1.55.17/src/locales>。分发修改后的编辑器时，同时保留上游许可、归档来源及本目录的补丁和语言资源，以提供对应源码。
