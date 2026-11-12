# 发布指南 / Releasing

本文说明如何给 AIServeWeave 打一个版本、CI 在打 tag 后自动做什么，以及部署方如何验证
和升级。日常开发约定见 [CONTRIBUTING.md](CONTRIBUTING.md)；本文件只讲发布本身。

This document explains how to cut an AIServeWeave release, what CI does
automatically once a tag is pushed, and how a deployer verifies and upgrades.
Day-to-day development conventions live in [CONTRIBUTING.md](CONTRIBUTING.md); this file
is about releases only.

## 版本号策略 / Versioning

- 遵循 [语义化版本](https://semver.org/lang/zh-CN/)，tag 格式 `vMAJOR.MINOR.PATCH`
  （如 `v0.1.0`）。
- 项目目前在 `0.x`：**minor 版本升级仍可能包含破坏性变更**（协议、配置、数据库
  schema、CLI flag）。达到 `1.0.0` 前不作兼容承诺。`A03`（协议与配置升级兼容）的
  设计文档给出了协议演进规则、配置版本策略与滚动升级/回退边界，但它划的只是"哪些
  方向字节层面不会崩"，不是生产级兼容性承诺，本条的立场不因此改变。该文档已从仓库
  删除，需要时在提交 b810671 之前的历史中查看 `docs/superpowers/specs/2026-09-15-a03-protocol-config-upgrade-design.md`。
- 每个版本的实际变更记在 [CHANGELOG.md](CHANGELOG.md)，`Added`/`Changed`/`Fixed`/
  `Removed` 分类；破坏性变更单独标注，不要淹没在普通条目里。

Follows [Semantic Versioning](https://semver.org/); tags look like `vMAJOR.MINOR.PATCH`.
While the project is on `0.x`, a minor bump may still break protocol, config,
database schema, or CLI flags — there is no compatibility promise before
`1.0.0`. The `A03` design document covers proto evolution rules, config
versioning and rolling-upgrade/rollback boundaries, but it only draws the line
on which directions don't break at the byte level; it is not a
production-grade compatibility guarantee, and this paragraph's stance is
unchanged by it. The document has been removed from the repository; see
`docs/superpowers/specs/2026-09-15-a03-protocol-config-upgrade-design.md` in history before commit b810671. Every release's actual changes live
in [CHANGELOG.md](CHANGELOG.md); breaking changes get their own callout, not
buried among routine entries.

## Agent 修复的发布与分发 / Shipping Agent fixes

Agent 的修复进入 `main` 后，源码部署者重新构建并重启 Agent 即可获得修复；预编译安装用户需要安装包含修复的正式版本。平台默认的一行命令安装从 Gateway 镜像内置的 Agent 制品下载，因此发布 Agent 修复时也要从相同源码、以相同版本构建并发布 Gateway 镜像。`.github/workflows/release.yml` 的二进制作业与 Gateway 镜像构建已经分别覆盖这两种产物。

以 Claude 长工具链 400 修复（`ca16c33`）为例：

1. 选定包含修复的提交作为新 Release 的源码，按下文流程发布新版本及校验和；旧版本的既有下载文件不会因此改变。
2. 平台维护者更新 Gateway 镜像后，确认 `/agent/v1/manifest` 报告预期版本且 `installable: true`，并抽查 `/agent/download/<version>/SHA256SUMS.txt` 和目标系统 Agent 文件。
3. 已有节点通过安装脚本 `--upgrade` / `-Upgrade` 升级并重启 Agent，确认节点自身版本。平台提供新版下载包不会自动替换已有节点程序。
4. 运行真实 Claude Code 长工具任务验收。CI 已通过不等同于目标节点或生产环境已验收；本次修复的回归包含连续工具回合、迟到回调、beta 变化和双 Gateway mTLS 路径，由现有 `go test ./...` 与数据面 race 门禁执行。

针对这个修复，Gateway 的推理转发与隧道协议未变；重建 Gateway 镜像的目的包括更新其内置的 Agent 下载制品。自行分发 Agent 的部署者按自己的制品来源更新程序，范围见 [Agent 升级说明](deploy/agent/README.md#claude-code-长工具链-400-修复)。

## 支持平台 / Supported platforms

| 产物 | linux/amd64 | linux/arm64 | darwin/amd64 | darwin/arm64 | windows/amd64 | windows/arm64 |
| --- | --- | --- | --- | --- | --- | --- |
| Agent 二进制与安装包 | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ |
| 二进制（Gateway、控制面、Registry） | ✅ | ✅ | ✅ | ✅ | ❌ 不支持 | ❌ 不支持 |
| 容器镜像（Gateway、控制面、Registry、Console） | ✅ | ✅ | — | — | — | — |

✅ 表示发布流水线产出该制品，不代表已在该系统上完成实机验收；Agent 各系统的验收状态见
[Agent 验收矩阵](deploy/agent/acceptance.md)。

✅ means the release pipeline produces that artifact, not that it has been
accepted on real hardware for that system; see the
[Agent acceptance matrix](deploy/agent/acceptance.md) for per-system status.

- Agent 交叉编译到 darwin 是因为它按设计跑在持有推理后端的宿主机上（README 与
  `deploy/docker-compose.yaml` 都把本机 Ollama/vLLM 当作常见场景），而不是因为另外
  三个服务被期望脱离容器运行；它们同样提供 darwin 二进制，纯粹是同一条构建流水线的
  副产品，没有为此单独验证过，容器镜像仍是三个数据面服务与 Registry 的推荐运行方式。
- Windows 只发布 Agent 的原生 `.exe` 与 `.zip` 安装包：Agent 已有 Windows 适配（身份
  ACL、named pipe 控制、计划任务部署等），部署入口与支持的 Windows 版本见
  [Agent 部署](deploy/agent/README.md)。Gateway、控制面与 Registry 仍按 POSIX 假设编写
  （证书文件权限、信号处理等），没有验证过，不出 Windows 制品。

Agent gets darwin builds because it is designed to run on the machine that
hosts the inference backend (the README and `deploy/docker-compose.yaml` both
treat local Ollama/vLLM as the common case) — not because the other three
services are expected to run outside a container. They get darwin binaries
too only because it is a byproduct of one shared build pipeline; that has not
been separately verified, and containers remain the recommended way to run
the three data-plane/identity services. On Windows only the Agent ships, as
a native `.exe` plus a `.zip` package: the Agent has Windows-specific
handling (identity ACLs, named-pipe control, scheduled-task deployment), and
its deployment entry point and supported Windows versions are in
[Agent deployment](deploy/agent/README.md). Gateway, the control plane and
Registry still rely on POSIX assumptions (certificate file permissions,
signal handling), are unverified on Windows, and get no Windows artifacts.

## 打一个版本 / Cutting a release

1. 确认 `main` 分支的最新提交已通过 CI（`.github/workflows/ci.yml` 的三个作业全绿）。
2. 更新 [CHANGELOG.md](CHANGELOG.md)：把 `[Unreleased]` 下的条目移到新的
   `## [X.Y.Z] - YYYY-MM-DD` 小节下面，`[Unreleased]` 留空表头；补上文末的对比链接。
3. 如果这次发布也要更新 Console：把 `service/aiServeWeaveConsole/package.json` 的
   `"version"` 字段同步改成同一个版本号（Console 没有独立的版本号体系，也不需要有）。
4. 提交这两处改动（一般标题类似 `chore: release vX.Y.Z`）。
5. `git tag vX.Y.Z && git push origin vX.Y.Z`。
6. `.github/workflows/release.yml` 在 tag push 上自动触发：重跑一遍质量门禁、按上表交叉
   编译四个二进制（Agent 另含 Windows 与安装包）、生成 `SHA256SUMS.txt`、构建并推送四个服务的多架构容器镜像
   到 GHCR（`ghcr.io/dongmu101/aiserveweave-<service>:<version>` 与 `:latest`）、创建
   GitHub Release 并附上全部二进制、校验和文件，release notes 取自 CHANGELOG 对应小节。
7. 发布流水线跑完后，去 Releases 页面确认产物齐全，用下面「校验和验证」抽查一个平台。

1. Confirm the latest commit on `main` has passed CI (all three jobs in
   `.github/workflows/ci.yml` green).
2. Update `CHANGELOG.md`: move the `[Unreleased]` entries under a new
   `## [X.Y.Z] - YYYY-MM-DD` heading, leave `[Unreleased]` empty, and add the
   comparison links at the bottom.
3. If this release also ships a Console update, bump
   `service/aiServeWeaveConsole/package.json`'s `"version"` to match — Console
   has no independent version scheme and does not need one.
4. Commit those two changes (typically `chore: release vX.Y.Z`).
5. `git tag vX.Y.Z && git push origin vX.Y.Z`.
6. `.github/workflows/release.yml` fires on the tag push: it reruns the
   quality gates, cross-compiles all four binaries per the table above (the
   Agent also gets Windows builds and install packages),
   generates `SHA256SUMS.txt`, builds and pushes multi-arch images for all
   four services to GHCR
   (`ghcr.io/dongmu101/aiserveweave-<service>:<version>` and `:latest`), and
   creates a GitHub Release with every binary, the checksum file, and release
   notes pulled from the matching CHANGELOG section.
7. Once the pipeline finishes, check the Releases page for a complete set of
   artifacts and spot-check one platform with the checksum verification below.

## 校验和验证 / Verifying checksums

```bash
# 下载某个版本的全部产物到同一目录，包含 SHA256SUMS.txt
sha256sum -c SHA256SUMS.txt
# macOS 用
shasum -a 256 -c SHA256SUMS.txt
```

任何一行显示 `FAILED` 就不要运行那个二进制，去 Issues 报告。

Download a release's artifacts into one directory including
`SHA256SUMS.txt`, then run the command above. Any line reporting `FAILED`
means don't run that binary — report it as an issue.

## 升级说明 / Upgrading

**当前仍不对生产环境做跨版本滚动升级的承诺**——`A03` 设计文档给出了协议演进规则、
配置版本策略与升级/回退顺序，但只是字节层面"哪些方向不会崩"的边界和已知缺口，不是
端到端演练验证过的生产保证（没有真实多副本混合版本集群的滚动升级排练，见文档第九节
缺口 1）。该文档已从仓库删除，在提交 b810671 之前的历史中查看 `docs/superpowers/specs/2026-09-15-a03-protocol-config-upgrade-design.md`。默认仍按以下
步骤停机升级，不要假设"新镜像原地替换旧镜像"总是安全的；确实需要滚动升级时，先从
历史中读该文档第六、七节的版本互操作范围声明和推荐顺序：

1. 读目标版本 CHANGELOG 里的 Breaking Changes / Known limitations，确认没有需要手动
   处理的 schema 变更或配置项改名。
2. 备份数据库（PostgreSQL 或 MySQL，取决于部署选择的是哪个）。控制面的迁移是 P07
   交付的带版本 SQL 迁移（`-migrate up/status/resume`），仅追加式的迁移允许回退二
   进制，但没有 DOWN 脚本——一旦某次迁移是破坏性的（删列/改列/改名），备份仍是唯一
   的回退手段，见 [数据库升级与恢复](deploy/database-recovery.md)。
3. 停止全部服务（`docker compose down`，不加 `-v`，保留数据卷）。
4. 拉取新版本镜像或替换新二进制。
5. 重新启动，观察日志确认迁移与启动无误，再对外恢复流量。

不要在生产环境上尝试未经演练的不停机滚动升级——多副本混合版本运行只验证到字段级
wire 兼容性（A03 设计文档第二节），没有真实多副本集群的端到端验证。

**There is still no production commitment to cross-version rolling
upgrades.** The `A03` design document covers proto evolution rules, config
versioning, and upgrade/rollback ordering (removed from the repository; see
`docs/superpowers/specs/2026-09-15-a03-protocol-config-upgrade-design.md` in history before commit b810671),
but it draws byte-level "won't break" boundaries and known gaps, not an
end-to-end-rehearsed production guarantee (no real multi-replica
mixed-version rolling-upgrade rehearsal exists yet — see gap 1 in the
document's section 9). Default to the stop-the-world steps below rather than
assuming a new image can always replace the old one in place; if a rolling
upgrade is genuinely needed, read sections 6–7 of that document (from history) first for
the interoperability matrix and recommended ordering:

1. Read the target version's Breaking Changes / Known limitations in the
   CHANGELOG to confirm there's no schema change or renamed config item that
   needs manual handling.
2. Back up the database (PostgreSQL or MySQL, whichever the deployment
   uses). The control plane's migrations are `P07`'s versioned SQL
   migrations (`-migrate up/status/resume`) — append-only migrations allow
   rolling the binary back, but there is no DOWN script, so once a migration
   is destructive (dropped/renamed/retyped column), the backup is still the
   only rollback path; see [Database Upgrade and Recovery](deploy/database-recovery.md).
3. Stop every service (`docker compose down`, without `-v`, keeping data
   volumes).
4. Pull the new images or swap in the new binaries.
5. Restart, check logs for a clean migration and startup, then restore
   traffic.

Do not attempt an unrehearsed no-downtime rolling upgrade in production —
mixed versions across replicas are only verified at the field-level wire
compatibility (A03 design document, section 2); there is no rehearsed
evidence yet for a real multi-replica mixed-version cluster.

## 使用已发布产物做全新部署 / Deploying a released version from scratch

`deploy/docker-compose.yaml` 里每个 AIServeWeave 自己的服务（`registry-init`、`registry`、
`controlplane`、`console`、`gateway`）同时带 `build:` 与
`image: ${AISW_IMAGE_REPO:-ghcr.io/dongmu101}/aiserveweave-<service>:${AISW_IMAGE_TAG:-local}`。
`AISW_IMAGE_TAG` 留空时从源码构建，镜像标为 `:local`；设为已发布的 `vX.Y.Z` 时就是本节流水线
推送的镜像（`registry-init` 与 `registry` 共用 `aiserveweave-registry`）。因此只需要一份编排
文件，不必手工改 YAML：

```bash
git checkout vX.Y.Z           # compose 文件与镜像同版本
deploy/install.sh --image-tag vX.Y.Z ...   # 或已有部署：deploy/upgrade.sh --image-tag vX.Y.Z
# 手工：在 deploy/.env 设置 AISW_IMAGE_TAG=vX.Y.Z，再
# docker compose pull && docker compose up -d --no-build
```

细节（版本核对、私有包登录、小内存主机）见 [deploy/README.md](deploy/README.md)「小内存主机」。

Each of AIServeWeave's own services in `deploy/docker-compose.yaml` carries
both `build:` and
`image: ${AISW_IMAGE_REPO:-ghcr.io/dongmu101}/aiserveweave-<service>:${AISW_IMAGE_TAG:-local}`.
With `AISW_IMAGE_TAG` empty the images are built from source and tagged
`:local`; set to a released `vX.Y.Z`, they are the images this workflow
pushed. One orchestration file serves both, with no hand-edited YAML: check
out the tag, then run `deploy/install.sh --image-tag vX.Y.Z` (or
`deploy/upgrade.sh --image-tag vX.Y.Z` for an existing deployment), or set
`AISW_IMAGE_TAG` in `deploy/.env` and run
`docker compose pull && docker compose up -d --no-build`.

## Agent 桌面与独立安装制品

`build-release.sh` 为 Agent 增加 windows/amd64、windows/arm64 原生 `.exe`；其他服务不新增 Windows 发布目标。保留现有 Linux 与 macOS（含 Intel）裸二进制命名，新增 Agent `.tar.gz`（Unix）和 `.zip`（Windows），包含对应二进制、维护脚本、README 与选定文件的 SHA256 清单。Release 总清单同时覆盖裸文件和压缩包。内部首版不承诺签名/公证；不使用平台安全策略绕过步骤。部署及逐系统验收见 [Agent 部署](deploy/agent/README.md)。

### 安装脚本资产 / Install script assets

`build-release.sh` 另把 `agent.sh`、`agent.ps1`、`install.sh`、`install.ps1` 四个脚本原样复制进 `dist/`（先清空 `dist/`），随 Release 作为独立资产上传，并纳入总 `SHA256SUMS.txt`。`install.sh`/`install.ps1` 里的 `__AISW_PLATFORM_URL__` 占位符在发布资产里保持原样，只有平台（Gateway 的 `/agent/*`）下发时才替换为自己的站点地址；直接从 Release 运行这两个脚本须传 `--platform`/`-Platform`，或改用平台下发的版本。

Gateway 镜像构建时按同一 `VERSION` 交叉编译 Agent 并内置 `/opt/aisw/agent-dist/<VERSION>/`，与 Release 的 Agent 二进制来自同一份源码，但是各自构建，不保证字节一致；用户通过 `--source github` 安装的是 Release 资产，通过平台安装的是镜像内置的制品，两者都由各自的 `SHA256SUMS.txt` 校验。`--source github` 只对正式 `vX.Y.Z` 版本可用，所以打 tag 后才能在 Release 页面验证这一路。镜像体积与构建时间的影响见 [部署说明](deploy/README.md)。启用方式与验收见 [部署说明](deploy/README.md#已有部署启用一行命令安装) 和 [Agent 验收矩阵](deploy/agent/acceptance.md)。

`build-release.sh` also copies the four scripts `agent.sh`, `agent.ps1`, `install.sh` and `install.ps1` into `dist/` verbatim (it empties `dist/` first); they ship as standalone release assets under the overall `SHA256SUMS.txt`. The `__AISW_PLATFORM_URL__` placeholder in `install.sh`/`install.ps1` stays unreplaced in release assets and is substituted only when the platform (the Gateway's `/agent/*`) serves the script, so running these two straight from a release needs `--platform`/`-Platform`, or the platform-served copy instead.

The Gateway image cross-compiles the Agent for the same `VERSION` at build time and bakes it into `/opt/aisw/agent-dist/<VERSION>/`. It comes from the same source as the release's Agent binaries but is built separately, so the bytes need not match; `--source github` installs a release asset, the platform path installs the image's artifacts, and each is verified against its own `SHA256SUMS.txt`. `--source github` works only for official `vX.Y.Z` versions, so that path can only be verified after a tag is pushed. See [deployment](deploy/README.md#已有部署启用一行命令安装) and the [Agent acceptance matrix](deploy/agent/acceptance.md) for enabling and acceptance.
