# 03 构建：Gateway 镜像内置 Agent 制品、Release 上传脚本

Status: needs-triage

依据：[spec.md「构建与发布」](../spec.md#构建与发布)。

## 范围

- 根 `Dockerfile` 为 Gateway 增加 Agent 交叉编译阶段（`AGENT_PLATFORMS`，同一 `VERSION`），产出 `/opt/aisw/agent-dist/<VERSION>/` 与 `SHA256SUMS.txt`。
- `deploy/docker-compose.yaml`、`.env.example`、`deploy/lib.sh`/`install.sh`/`upgrade.sh`：新 flag 与 `AGENT_PUBLIC_URL`、可选 `AGENT_PLATFORMS`。
- `scripts/build-release.sh`、`.github/workflows/release.yml`：上传 `agent.sh`、`agent.ps1`、`install.sh`、`install.ps1` 并纳入总清单。
- 评估镜像体积与服务器构建时长，写入部署文档。

## 验收

- [ ] 本地 `docker build` Gateway 镜像，目录内容与清单校验通过（`AGENT_PLATFORMS=linux/amd64` 冒烟）
- [ ] 其他服务镜像不变
- [ ] `build-release.sh` 单平台冒烟（注意它会清空 `dist/`）

## Comments
