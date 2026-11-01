# 03 构建：Gateway 镜像内置 Agent 制品、Release 上传脚本

Status: ready-for-agent

依据：[spec.md「构建与发布」](../spec.md#构建与发布)。

## 范围

- 根 `Dockerfile` 为 Gateway 增加 Agent 交叉编译阶段（`AGENT_PLATFORMS`，同一 `VERSION`），产出 `/opt/aisw/agent-dist/<VERSION>/` 与 `SHA256SUMS.txt`。
- `deploy/docker-compose.yaml`、`.env.example`、`deploy/lib.sh`/`install.sh`/`upgrade.sh`：新 flag 与 `AGENT_PUBLIC_URL`、可选 `AGENT_PLATFORMS`。
- `scripts/build-release.sh`、`.github/workflows/release.yml`：上传 `agent.sh`、`agent.ps1`、`install.sh`、`install.ps1` 并纳入总清单。
- 评估镜像体积与服务器构建时长，写入部署文档。

## 验收

- [x] 本地 `docker build` Gateway 镜像，目录内容与清单校验通过（`AGENT_PLATFORMS=linux/amd64` 冒烟；另跑过六平台完整构建）
- [x] 其他服务镜像不变（Registry 冒烟：仅多一个空目录 `/opt/aisw/agent-dist`）
- [x] `build-release.sh` 单平台冒烟（注意它会清空 `dist/`）

## Comments

### 完成记录

- `Dockerfile`：新增 `agent-dist` 阶段（`FROM build`），仅 `SERVICE=aiServeWeaveGateway` 时交叉编译；最终镜像统一 `COPY --from=agent-dist`，其他服务得到空目录。Stage 名须小写，故未采用按 SERVICE 选最终阶段的方案。
- `docker-compose.yaml`：`-agent-dist-dir=${AGENT_PUBLIC_URL:+/opt/aisw/agent-dist}`，与 `-agent-public-url` 同空同设（Gateway 要求两者成对）；控制面新增环境变量 `AISW_AGENT_PUBLIC_URL`；构建参数 `AGENT_PLATFORMS`。
- `lib.sh` 新增 `ensure_agent_install`（给旧 `controlplane.yaml` 补 `AgentInstall` 段），`install.sh` 新增 `--agent-public-url`，`upgrade.sh` 与 `install.sh` 均调用补段函数。
- `build-release.sh` 把四个脚本复制进 `dist/`，随总 `SHA256SUMS.txt` 校验；`release.yml` 的 `gh release create dist/*` 已覆盖，仅更新注释。
- 实测（本机，依赖层已缓存）：六平台构建约 52 秒，`agent-dist` 目录 98 MB，Gateway 镜像 195 MB；单平台 76 MB。
- 未验证：`install.sh`/`install.ps1` 尚未存在（任务 04、05），冒烟时用临时空文件代替并已删除；Dockerfile 现在要求这两个文件存在，缺失会构建失败。CI 中 arm64 镜像在 QEMU 下的构建耗时未测。真实服务器构建时长未测。
