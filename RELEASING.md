# 发布指南 / Releasing

本文说明如何给 AIServeWeave 打一个版本、CI 在打 tag 后自动做什么，以及部署方如何验证
和升级。日常开发约定见 [AGENTS.md](AGENTS.md)；本文件只讲发布本身。

This document explains how to cut an AIServeWeave release, what CI does
automatically once a tag is pushed, and how a deployer verifies and upgrades.
Day-to-day development conventions live in [AGENTS.md](AGENTS.md); this file
is about releases only.

## 版本号策略 / Versioning

- 遵循 [语义化版本](https://semver.org/lang/zh-CN/)，tag 格式 `vMAJOR.MINOR.PATCH`
  （如 `v0.1.0`）。
- 项目目前在 `0.x`：**minor 版本升级仍可能包含破坏性变更**（协议、配置、数据库
  schema、CLI flag）。达到 `1.0.0` 前不作兼容承诺，参见 [STATUS.md](STATUS.md) 的
  `A03`（协议与配置升级兼容）——该项已交付协议演进规则、配置版本策略与滚动升级/
  回退边界的设计文档（[`docs/superpowers/specs/2026-09-15-a03-protocol-config-upgrade-design.md`](docs/superpowers/specs/2026-09-15-a03-protocol-config-upgrade-design.md)），
  但那份文档划的是"哪些方向字节层面不会崩"的边界，不是生产级兼容性承诺，本条的
  立场不因此改变。
- 每个版本的实际变更记在 [CHANGELOG.md](CHANGELOG.md)，`Added`/`Changed`/`Fixed`/
  `Removed` 分类；破坏性变更单独标注，不要淹没在普通条目里。

Follows [Semantic Versioning](https://semver.org/); tags look like `vMAJOR.MINOR.PATCH`.
While the project is on `0.x`, a minor bump may still break protocol, config,
database schema, or CLI flags — there is no compatibility promise before
`1.0.0` (see STATUS.md's `A03`, which now ships a design document covering
proto evolution rules, config versioning and rolling-upgrade/rollback
boundaries —
[`docs/superpowers/specs/2026-09-15-a03-protocol-config-upgrade-design.md`](docs/superpowers/specs/2026-09-15-a03-protocol-config-upgrade-design.md).
That document draws the line on which directions don't break at the byte
level; it is not a production-grade compatibility guarantee, and this
paragraph's stance is unchanged by it). Every release's actual changes live
in [CHANGELOG.md](CHANGELOG.md); breaking changes get their own callout, not
buried among routine entries.

## 支持平台 / Supported platforms

| 产物 | linux/amd64 | linux/arm64 | darwin/amd64 | darwin/arm64 | Windows |
| --- | --- | --- | --- | --- | --- |
| 二进制（Agent、Gateway、控制面、Registry） | ✅ | ✅ | ✅ | ✅ | ❌ 不支持 |
| 容器镜像（Gateway、控制面、Registry、Console） | ✅ | ✅ | — | — | ❌ 不支持 |

- Agent 交叉编译到 darwin 是因为它按设计跑在持有推理后端的宿主机上（README 与
  `deploy/docker-compose.yaml` 都把本机 Ollama/vLLM 当作常见场景），而不是因为另外
  三个服务被期望脱离容器运行；它们同样提供 darwin 二进制，纯粹是同一条构建流水线的
  副产品，没有为此单独验证过，容器镜像仍是三个数据面服务与 Registry 的推荐运行方式。
- Windows 不在支持范围内：仓库里没有任何针对 Windows 的路径处理或验证约定
  （证书文件权限、信号处理等均按 POSIX 假设编写），没有验证过就不列出来。

Agent gets darwin builds because it is designed to run on the machine that
hosts the inference backend (the README and `deploy/docker-compose.yaml` both
treat local Ollama/vLLM as the common case) — not because the other three
services are expected to run outside a container. They get darwin binaries
too only because it is a byproduct of one shared build pipeline; that has not
been separately verified, and containers remain the recommended way to run
the three data-plane/identity services. Windows is out of scope: nothing in
this codebase handles Windows paths or signals (certificate file permissions,
signal handling — all POSIX assumptions), and an unverified platform does not
get listed as supported.

## 打一个版本 / Cutting a release

1. 确认 `main` 分支的最新提交已通过 CI（`.github/workflows/ci.yml` 的三个作业全绿）。
2. 更新 [CHANGELOG.md](CHANGELOG.md)：把 `[Unreleased]` 下的条目移到新的
   `## [X.Y.Z] - YYYY-MM-DD` 小节下面，`[Unreleased]` 留空表头；补上文末的对比链接。
3. 如果这次发布也要更新 Console：把 `service/aiServeWeaveConsole/package.json` 的
   `"version"` 字段同步改成同一个版本号（Console 没有独立的版本号体系，也不需要有）。
4. 提交这两处改动（一般标题类似 `chore: release vX.Y.Z`）。
5. `git tag vX.Y.Z && git push origin vX.Y.Z`。
6. `.github/workflows/release.yml` 在 tag push 上自动触发：重跑一遍质量门禁、交叉编译
   四个二进制的四个平台产物、生成 `SHA256SUMS.txt`、构建并推送四个服务的多架构容器镜像
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
   quality gates, cross-compiles all four binaries for all four platforms,
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

**当前仍不对生产环境做跨版本滚动升级的承诺**——STATUS.md 的 `A03` 已交付协议演进
规则、配置版本策略与升级/回退顺序的设计文档（
[`docs/superpowers/specs/2026-09-15-a03-protocol-config-upgrade-design.md`](docs/superpowers/specs/2026-09-15-a03-protocol-config-upgrade-design.md)），
但那份文档给出的是字节层面"哪些方向不会崩"的边界和已知缺口，不是端到端演练验证过
的生产保证（没有真实多副本混合版本集群的滚动升级排练，见文档第九节缺口 1）。默认
仍按以下步骤停机升级，不要假设"新镜像原地替换旧镜像"总是安全的；确实需要滚动升级
时，先读一遍上述设计文档第六、七节的版本互操作范围声明和推荐顺序：

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
upgrades.** STATUS.md's `A03` now ships a design document covering proto
evolution rules, config versioning, and upgrade/rollback ordering
([`docs/superpowers/specs/2026-09-15-a03-protocol-config-upgrade-design.md`](docs/superpowers/specs/2026-09-15-a03-protocol-config-upgrade-design.md)),
but it draws byte-level "won't break" boundaries and known gaps, not an
end-to-end-rehearsed production guarantee (no real multi-replica
mixed-version rolling-upgrade rehearsal exists yet — see gap 1 in the
document's section 9). Default to the stop-the-world steps below rather than
assuming a new image can always replace the old one in place; if a rolling
upgrade is genuinely needed, read sections 6–7 of that document first for
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

`deploy/README.md` 的「起步」一节默认从源码构建（`docker compose up -d --build`）。
要改用已发布的镜像而不是本地构建，把 `deploy/docker-compose.yaml` 里每个
AIServeWeave 自己服务（`registry-init`、`registry`、`controlplane`、`console`、
`gateway`）的 `build: {...}` 块换成：

```yaml
image: ghcr.io/dongmu101/aiserveweave-<service>:<version>
```

（`<service>` 取值：`registry`、`controlplane`、`gateway`、`console`；`registry-init`
与 `registry` 用同一个 `aiserveweave-registry` 镜像。）换完后 `docker compose up -d`
不需要 `--build`。这是手工改几行 YAML 的做法，仓库里刻意不维护第二份
`docker-compose.image.yaml`，避免两份编排文件互相漂移。

`deploy/README.md`'s "起步" section defaults to a source build
(`docker compose up -d --build`). To use released images instead of a local
build, swap each of AIServeWeave's own service's `build: {...}` block in
`deploy/docker-compose.yaml` for `image: ghcr.io/dongmu101/aiserveweave-<service>:<version>`
as shown above, then `docker compose up -d` without `--build`. This is a
deliberate few-line manual edit — the repository does not keep a second
`docker-compose.image.yaml` to avoid two orchestration files drifting apart.
