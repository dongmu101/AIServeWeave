# Docker Compose 部署

本目录部署单机 AIServeWeave：MySQL、Redis、Registry、ControlPlane、Gateway 与 Console。
Agent 在推理节点宿主机上运行，连接本机 Ollama / vLLM / ComfyUI。

下文默认保留 `.env.example` 中的端口。除明确标注外，命令均在仓库的 `deploy` 目录执行。

启用 `ComfyEditor.DistDir` 的部署，须先在仓库根目录使用
`scripts/fetch-comfyui-frontend.sh --localized <空目录>` 构建对应八语言制品，
再将目录挂载给控制面并升级服务。工具链与校验规则见
[本地化构建说明](../scripts/comfyui-localization/README.md)。已配置目录缺少
`aisw-i18n-manifest.json`、配置版本或逐文件摘要不符时，控制面启动失败；
不要将原始 release zip 与本地化制品混用。未配置 `DistDir` 时编辑器继续关闭。

## 环境要求

- Docker Engine 24+、Docker Compose v2 或更新版本，使用 `docker compose` 命令。
- 首次构建可访问镜像仓库、Go 模块源、npm registry 与 Google Fonts；拉取预构建镜像时只需访问 Docker Hub 与 GHCR。
- 运行整套服务需要 2 核、3 GiB 内存；在主机上从源码构建还需要内存加 swap 约 6 GiB。小内存主机见「小内存主机」。
- 宿主机安装 `openssl`、`curl`；从源码运行 Agent 时需要 Go 1.27。
- 下表端口空闲；冲突时在 `.env` 中设置对应变量。

| 用途 | 默认端口 | 覆盖变量 |
| --- | --- | --- |
| Console | 3000 | `CONSOLE_PORT` |
| Gateway API / 隧道 / 指标 | 8080 / 8443 / 9090 | `GATEWAY_PORT` / `GATEWAY_TUNNEL_PORT` / `GATEWAY_METRICS_PORT` |
| ControlPlane | 8090 | `CONTROLPLANE_PORT` |
| Registry / 指标 | 9443 / 9091 | `REGISTRY_PORT` / `REGISTRY_METRICS_PORT` |
| MySQL / Redis | 3306 / 6379 | `MYSQL_PORT` / `REDIS_PORT` |

## 脚本部署与升级

`install.sh` 自动完成下文「起步」中的手工步骤，`upgrade.sh` 负责之后每次拉取新代码后的重新部署。
两个脚本都可以在任意目录下调用，脚本会自己切换到 `deploy` 目录。

### 首次部署

```bash
deploy/install.sh --owner-email owner@example.com --operator-email ops@example.com
```

脚本按以下顺序执行：

1. 从 `.env.example` 创建 `.env`，权限设为 600，并去掉 CR 换行。
2. 补齐所有为空的密钥；已有值不改动。
3. MySQL 数据卷尚未创建时，为数据库账户生成独立密码。
4. 账户密码留空时自动生成，并在脚本结束时显示一次。
5. 本机没有 `controlplane.yaml` 时由 `controlplane.example.yaml` 复制一份。这份文件属于本机部署、不受 git 跟踪（已在 `.gitignore` 中），按需修改，已存在时脚本不会覆盖。
6. 在 `controlplane.yaml` 中关闭 go-zero 的访问日志中间件（原因见下文「对外部署与已知限制」），缺少 `TenantNodes` 与 `AgentInstall` 段时补上。
7. 逐个构建镜像（`.env` 设置了 `AISW_IMAGE_TAG` 时改为拉取该 release 的预构建镜像），启动服务，然后检查 Console、控制面与 Gateway 是否应答。

脚本可以重复执行，失败后重跑会从中断处继续。常用选项如下，完整列表见 `deploy/install.sh --help`：

| 选项 | 作用 |
| --- | --- |
| `--https` | 部署在 HTTPS 反向代理之后，启用 Secure Cookie |
| `--signup` | 开启租户自助注册：写入 `controlplane.yaml` 的 `Signup` 段与 `docker-compose.override.yaml` |
| `--image-tag vX.Y.Z` | 拉取该 release 的预构建镜像，主机上不构建；写入 `.env` 的 `AISW_IMAGE_TAG`，见「小内存主机」 |
| `--goproxy URL` | 指定源码构建时的 Go 模块代理，例如 `https://goproxy.cn,direct` |
| `--advertise-addr HOST:PORT` | Agent 拨号的隧道地址 |
| `--agent-public-url URL` | 平台对外的 HTTPS 站点地址（如 `https://open7x.com`），写入 `.env` 的 `AGENT_PUBLIC_URL`，开启 Agent 一行命令安装，见「对外部署与已知限制」 |
| `--local-discovery` | 保留对 `host.docker.internal` 的本机发现。Linux 原生 Docker 无法解析这个主机名，所以默认清空 |
| `--no-build` | 只准备配置，不构建或拉取镜像、不启动 |

### 重新部署

```bash
deploy/upgrade.sh                    # 拉取当前分支的上游提交，备份，重建有变化的服务
deploy/upgrade.sh gateway console    # 只重建指定服务
deploy/upgrade.sh --no-pull          # 不拉取代码，直接按当前工作区重建
deploy/upgrade.sh --image-tag v0.2.0 # 改为拉取该 release 的预构建镜像；--image-tag '' 切回源码构建
deploy/upgrade.sh --no-build           # 沿用已 load 到本机的镜像（make push），不构建也不拉取
```

升级按以下流程进行：

- 代码已是最新时照常重建，不会提前退出：手动 `git pull` 之后容器仍是旧版本，所以不能用「代码有没有新提交」判断要不要部署。未变化的镜像走构建缓存，容器只在镜像或配置有变化时重建；`--force` 只为兼容旧命令保留，不再有作用。
- 只做快进合并。`controlplane.yaml` 是本机文件，拉取不会动它；从仍跟踪该文件的旧版本第一次升级上来时，脚本在拉取期间把它挪开、拉取后原样放回，并改为不受跟踪。上游若重新跟踪该文件，脚本拒绝升级，避免覆盖本机配置。其他已跟踪文件有本地修改时直接拒绝升级。
- 拉取后改用新版本的脚本继续执行，补齐新版本要求的空密钥。
- 如果 `.env.example` 新增了变量，或 `controlplane.example.yaml` 有上游改动，给出提示和查看差异的命令，由你决定是否同步到本机的 `controlplane.yaml`。
- 先构建或拉取镜像，这一步不改动正在运行的服务；之后、重建容器之前，把 MySQL 转储和 Registry 身份材料备份到 `~/aiserveweave-backups`（可用 `--backup-dir` 修改）。任何一项备份失败都会中止升级。
- 拉取模式下总是拉取全部服务的镜像，即使命令只点名了部分服务；镜像没有变化的容器不会重建。
- 结束时打印回滚命令（切换过镜像版本时还有回滚镜像的命令），以及本次备份的位置。
- `--prune` 清理悬空镜像、旧版本以 `deploy-<服务>` 命名的镜像，并把构建缓存压到 2 GB 以内。

两个脚本都逐个构建镜像，不会并行构建。源码构建时内存加 swap 低于 6 GiB 脚本会给出警告；
这类主机应改为拉取预构建镜像，见下一节。

## 小内存主机

下面以 2 核、3 GiB 内存的主机为例。服务本身跑得下，主机上的 Go 与 Next.js 构建却跑不下。

### 拉取预构建镜像，不在主机上构建

打 `vX.Y.Z` tag 时，Release 流水线会把四个服务的多架构镜像推送到
`ghcr.io/dongmu101/aiserveweave-<服务>:<版本>`（见 [RELEASING.md](../RELEASING.md)）。
把 `.env` 的 `AISW_IMAGE_TAG` 设为某个已发布版本，脚本就改为拉取镜像：

```bash
git fetch --tags
deploy/install.sh --owner-email owner@example.com --image-tag v0.2.0   # 首次部署
deploy/upgrade.sh --image-tag v0.2.0                                   # 已有部署切换或升级版本
```

- 镜像版本与代码版本应当一致。compose 文件来自当前检出，镜像来自 release；两者的
  `deploy/docker-compose.yaml` 不同时脚本会警告，因为该版本之后新增的启动参数旧二进制不认识。
  最稳妥的做法是 `git checkout v0.2.0` 后再部署。
- 镜像只在打 tag 时发布。main 上尚未发布的提交，仍须源码构建，或先打一个 release。
- GHCR 包为私有时，先用带 `read:packages` 权限的 token 执行 `docker login ghcr.io`。
- 拉取模式下 `docker compose up` 带 `--no-build`，缺少镜像会直接报错，不会悄悄在主机上构建。
  手工操作时使用 `docker compose pull && docker compose up -d --no-build`。

### 推送到 Docker Hub，主机拉取

在开发机执行 `make publish`：构建 amd64 与 arm64 两种架构的镜像，以 `git describe` 版本（如
`v0.1.0-107-g3625aaf`）为 tag，推送到 `docker.io/dongmu101/aiserveweave-<服务>`。要求本机工作区干净且已推送。

```bash
docker login                              # 首次
make publish                              # 结束时打印主机上要执行的命令
make publish PUBLISH_TAGS="v0.2.0 latest" # 指定 tag
```

主机上第一次在 `deploy/.env` 设置 `AISW_IMAGE_REPO=docker.io/dongmu101`，之后每次：

```bash
git pull && deploy/upgrade.sh --image-tag v0.1.0-107-g3625aaf
```

脚本会从 tag 里的 `g<提交>` 找出镜像构建时的提交，用它核对本机的 compose 文件。
首次多平台构建会创建一个名为 `aisw-multiarch` 的 docker-container builder，不会改动默认 builder。
Docker Hub 新建的仓库默认公开，可在 Docker Hub 设置里调整。

### 在别的机器上构建，再送到主机

main 上尚未发布的提交，可以在开发机上构建，再经 ssh 送到主机。仓库根目录的 `Makefile` 包好了这几步：

```bash
make images                                   # 为 linux/amd64 构建四个镜像（服务器是 arm64 时加 PLATFORM=linux/arm64）
make push SERVER=user@host                    # docker save | ssh | docker load，不重启服务
make deploy SERVER=user@host                  # 以上两步，再在主机上执行 deploy/upgrade.sh --no-build
make deploy SERVER=user@host SERVICES=gateway # 只更新 Gateway
make help                                     # 全部目标与变量
```

- 镜像标为 `ghcr.io/dongmu101/aiserveweave-<服务>:local`，正是主机 `.env` 中 `AISW_IMAGE_TAG` 留空时启动的镜像。
- `upgrade.sh --no-build` 照常拉取代码、补配置、备份，但不构建也不拉取镜像，缺少镜像时直接中止。
  主机上的代码会快进到上游提交，因此 `make deploy` 要求本机工作区干净且已推送，镜像与主机代码才出自同一个提交。
- Go 编译在构建机原生平台上交叉编译，不经过 QEMU；Console 的 `pnpm build` 仍在目标平台模拟下运行，
  在 Apple Silicon 上较慢，Docker Desktop 开启 Rosetta 可以加快。
- `make push` 会先核对主机架构与镜像架构，避免把 arm64 镜像送到 amd64 主机。主机用户须能直接执行 `docker`（在 docker 组中）。
- 无法直连 ssh 时用 `make save`，把压缩包拷到主机后 `gunzip -c <文件> | docker load`，再执行 `deploy/upgrade.sh --no-build`。

### 必须在主机上构建时

- 先加 swap，内存加 swap 至少 6 GiB：

  ```bash
  sudo fallocate -l 4G /swapfile && sudo chmod 600 /swapfile
  sudo mkswap /swapfile && sudo swapon /swapfile
  echo '/swapfile none swap sw 0 0' | sudo tee -a /etc/fstab
  echo 'vm.swappiness=10' | sudo tee /etc/sysctl.d/99-swappiness.conf && sudo sysctl --system
  ```

- 在 `.env` 设置 `AGENT_PLATFORMS`，只编译实际要发的平台。交叉编译六个平台的 Agent 是 Gateway 镜像里最慢的一步。
- Dockerfile 的 Go 编译使用 BuildKit 缓存挂载，第二次以后只重编改动的包；只改 Console 不会让 Go 镜像重建。
  缓存在 `docker builder prune` 之后才会清空。

### 内存上限

每个常驻服务都有容器内存上限。Go 服务配套 `GOMEMLIMIT`（约为上限的 80%），堆接近它时 GC
更积极地回收，不会一路涨到被内核 OOM 杀掉；Console 用 `--max-old-space-size` 限制 V8 堆。
没有这些上限时，内存一紧，内核往往先杀掉占用最大的 mysqld。

| 服务 | 默认上限 | 配套参数 | 覆盖变量 |
| --- | --- | --- | --- |
| mysql | 768m | buffer pool 256M | `MYSQL_MEM_LIMIT`、`MYSQL_INNODB_BUFFER_POOL_SIZE` |
| gateway | 512m | `GOMEMLIMIT=400MiB` | `GATEWAY_MEM_LIMIT`、`GATEWAY_GOMEMLIMIT` |
| controlplane | 384m | `GOMEMLIMIT=300MiB` | `CONTROLPLANE_MEM_LIMIT`、`CONTROLPLANE_GOMEMLIMIT` |
| console | 384m | V8 堆 256 MB | `CONSOLE_MEM_LIMIT`、`CONSOLE_NODE_HEAP_MB` |
| redis | 256m | 不设 `maxmemory`，吊销状态不能被淘汰 | `REDIS_MEM_LIMIT` |
| registry | 128m | `GOMEMLIMIT=100MiB` | `REGISTRY_MEM_LIMIT`、`REGISTRY_GOMEMLIMIT` |

上限合计约 2.4 GiB，是上限而不是预留。上线后用 `docker stats --no-stream` 观察实际占用；
某个服务因超限被杀时，`docker inspect -f '{{.State.OOMKilled}}' <容器>` 显示 `true`，按需调高并同步配套参数。
Gateway 刻意不限制请求体大小，并发大请求（如 Claude Code）占内存最多，因此上限给得最高。

### MySQL 与日志

- MySQL 关闭 performance_schema 与 binlog，buffer pool 256M，最大连接数 60。这套单实例部署
  不做主从复制，也不做按时间点恢复，备份走 mysqldump（见 [database-recovery.md](database-recovery.md)）。
  旧部署升级后，数据卷里已有的 `binlog.*` 文件不会再被使用，确认无用后可以删除：

  ```bash
  docker compose exec mysql sh -c 'rm -f /var/lib/mysql/binlog.[0-9]* /var/lib/mysql/binlog.index'
  ```

- 所有容器日志使用 json-file 驱动轮转，每个容器最多保留 3 个 10 MB 文件。
  `docker compose logs` 只能看到轮转后保留的部分。

## 起步

### 1. 创建配置

首次部署在仓库根目录执行；已有 `.env` 时直接编辑原文件，不覆盖已有密钥：

```bash
cd deploy
umask 077
test -e .env || cp .env.example .env
chmod 600 .env
```

执行以下命令填充八个独立密钥的空值，已有值保持不变：

```bash
for var in AISW_ACCESS_SECRET AISW_INTERNAL_TOKEN AISW_BOOTSTRAP_TOKEN AISW_REGISTRY_ADMIN_TOKEN AISW_GATEWAY_ADMIN_TOKEN AISW_GATEWAY_MODEL_PULL_TOKEN AISW_GATEWAY_COMFYUI_MANAGED_TOKEN; do
  sed -i.bak "s#^${var}=\$#${var}=$(openssl rand -base64 32)#" .env
done
sed -i.bak "s#^AISW_CONSOLE_SESSION_SECRET=\$#AISW_CONSOLE_SESSION_SECRET=$(openssl rand -base64 48)#" .env
rm -f .env.bak
```

编辑 `.env`，填写首次登录账户和独立的数据库密码：

```dotenv
AISW_DEFAULT_TENANT_NAME=Default
AISW_DEFAULT_OWNER_EMAIL=owner@example.com
AISW_DEFAULT_OWNER_PASSWORD=<随机密码>
MYSQL_PASSWORD=<独立随机密码>
MYSQL_ROOT_PASSWORD=<另一独立随机密码>
```

每个密码可分别用 `openssl rand -hex 24` 生成，替换上面的占位符并保存到密码管理器。
当前 bootstrap 手工拼接 JSON，自动建号使用的名称和密码应避免双引号、反斜杠与换行。

需要管理机群时，再填写独立的平台运维账户：

```dotenv
AISW_DEFAULT_OPERATOR_EMAIL=ops@example.com
AISW_DEFAULT_OPERATOR_PASSWORD=<独立随机密码>
AISW_DEFAULT_OPERATOR_NAME=Admin
```

租户账户与平台运维账户各自登录。对应邮箱留空则跳过建号；再次执行 bootstrap
不会修改已有账户或重置密码。数据库密码变量只在新数据卷初始化时创建数据库账户，
已有数据库改密需同步修改数据库内的账户密码与 `.env`。

### 2. 构建与启动

```bash
docker compose config --quiet
docker compose up -d --build
docker compose ps --all
docker compose logs --tail=50 bootstrap
```

`.env` 设置了 `AISW_IMAGE_TAG` 时，把第二行换成
`docker compose pull && docker compose up -d --no-build`，见「小内存主机」。

`mysql`、`redis`、`console` 应最终显示 `healthy`；`registry`、`controlplane`、`gateway`
应持续运行。`registry-init` 与 `bootstrap` 成功结束后显示 `Exited (0)`，属于正常状态。
`bootstrap` 日志确认建号成功后，再登录 Console。

Compose 的 `service_started` 只保证容器已启动，Console 健康检查也只检查登录页；
两者都不证明控制面业务可用。启动后通过实际登录与下面的 API 请求验证。
参见 [Compose 启动顺序](https://docs.docker.com/compose/how-tos/startup-order/)。

### 3. 登录与验证 API

打开 `http://127.0.0.1:3000`，使用上面配置的租户邮箱和密码登录，在 API Key 页面创建
并保存 Key。明文仅在创建时显示一次。

在终端将 `AISW_API_KEY` 设置为刚创建的 Key，再执行：

```bash
curl --fail-with-body --connect-timeout 5 --max-time 15 \
  http://127.0.0.1:8080/v1/models \
  -H "Authorization: Bearer $AISW_API_KEY"
unset AISW_API_KEY
```

没有 Agent 时模型列表为空。接入带模型的 Agent 后再次查询，应能看到模型目录。

租户控制台的“AI 服务与模型”（`/console/models`）展示 **Agent 连接上来后上报的 AI 服务和模型**，
包括 Agent 在线/离线状态、服务类型、健康状态、后端版本、模型 ID 与能力；不展示 Gateway
本机发现结果。租户页面无需平台运维登录，详细内部地址仅在运维页面展示。

平台运维 `/operator/models` 还保留独立的 Gateway 本机发现区域。Compose 默认通过
`host.docker.internal:11434` 和 `host.docker.internal:8000` 读取宿主机 Ollama/vLLM 清单，
无需 Agent 即可显示服务地址、健康状态、版本与模型；这份发现清单不会直接加入推理调度。
可在 `deploy/.env` 设置 `GATEWAY_LOCAL_OLLAMA_URL` / `GATEWAY_LOCAL_VLLM_URL` 为容器
可达地址，设为空字符串跳过该类型。Docker Desktop 提供 `host.docker.internal`；其他
Docker 环境需自行配置该名称或覆盖地址。通过 Gateway API 调用模型仍按下一节接入 Agent。

## 接一个 Agent

### 一行命令安装（推荐）

前提：平台已开启一行命令安装（`.env` 设置了 `AGENT_PUBLIC_URL`，启用步骤见下文「已有部署启用一行命令安装」）。未开启时 Console 提示「未启用」，改用后面的 ZIP 接入包。

1. 平台运维在 **Agent 接入**（`/operator/agent-enrollment`）、商户 owner/admin 在 **节点管理**（`/console/nodes`）的「添加节点」面板选择系统（Ubuntu、macOS、Windows）、凭据方式与下载来源，点击生成。运维先填好 Registry 与 Gateway 隧道地址；商户的地址取自 `TenantNodes` 配置。
2. 复制页面显示的一行命令，在节点机上执行：Ubuntu 用带 `sudo` 的 `curl … | sudo bash`，macOS 不用 sudo，Windows 在普通（非管理员）PowerShell 中运行。命令包含一次性兑换码，有效期 15 分钟至 24 小时，只能兑换一次。
3. 脚本从平台（`/agent/*`）取得与平台同版本的 Agent 与 `SHA256SUMS.txt`，校验通过后才兑换并安装服务；也可选择从 GitHub Release 下载（仅正式 `vX.Y.Z` 版本）。兑换码只在页面内存中出现一次，离开页面后无法找回，过期或已用须重新生成。
4. 安装完成后在「节点管理」检查连接与运行时，再验收普通请求和 SSE。

凭据方式有三种：兑换码（命令内含码）、交互输入（命令不含码，脚本隐藏输入提示，页面另显示码）、令牌直写（命令内含一次性 Bootstrap 令牌与 CA 指纹）。兑换码只在兑换时才铸造令牌，数据库不保存明文码与令牌。已有节点升级用脚本的 `--upgrade`（Windows 为 `-Upgrade`），不使用兑换码。详细参数与排错见 [Agent 独立部署](agent/README.md#一行命令安装)。

### ZIP 接入包（高级）

也可使用运维控制台的 **Agent 接入** 页面（`/operator/agent-enrollment`，「高级」折叠区）手动生成 ZIP：

1. 用平台运维账户登录，填写节点可达的 Registry 和 Gateway 隧道 `host:port`，选择令牌有效期（15 分钟、1 小时或 24 小时）。当前远程部署为 `registry.open7x.com:9443` 与 `agent.open7x.com:8443`；地址必须与服务端证书和名册通告一致。页面填写地址不会修改部署配置。
2. 点击「生成新节点接入包」，然后下载 ZIP，经可信私密渠道交给节点提供者。每次生成分配随机新节点 ID，令牌只绑定该 ID；此入口不用于覆盖既有节点或重装已有身份。
3. 对方另行取得与平台同一版本、匹配本机系统/架构的 Agent 发行包，将接入包解压到发行包目录，按包内 `README.txt` 安装。对方不需要平台账户、Registry 管理密钥或平台服务器权限。
4. 注册成功后在「节点管理」检查连接与运行时，再验收普通请求和 SSE。删除下载的令牌及 ZIP 副本；安装服务成功不等于模型已接入。

ZIP 包含 `ca.pem`、`bootstrap-token`、`connection.json`、`README.txt`，不包含 Agent 二进制、CA 私钥、平台管理令牌或 CLI 登录。CA 也可从该页单独下载，页面显示规范化 CA 文件的 SHA256。接入包只保留在当前页面内存；离开后无法找回，过期/已使用须生成新包。响应丢失不会自动重试；再次手动生成会授权另一个节点，先前可能签发的令牌仍有效至到期。

此入口沿用控制面已有 `Registry.Addr`、`Registry.AdminToken`、`Registry.CACertFile` 配置，默认 Compose 已配置，无需新增环境变量。控制面只读取 CA 公共证书，CA 文件缺失、过期或混有非证书 PEM 块时拒绝下载和签发。租户账户无权访问。接入包生成记录在平台审计 `node.enrollment.create`，只记录节点 ID、操作者及到期时间。

命令行接入仍可用于管理员维护：

以下示例适用于 Agent 与 Compose 位于同一台机器，且本机 Ollama 已在 `11434` 端口运行。
每个节点使用不同的 `node-id`，示例中的 `local-ollama-01` 需按实际节点修改。

先在 `deploy` 目录执行，为该节点签发有效十五分钟的一次性绑定令牌并复制公共 CA：

```bash
umask 077
mkdir -p ../data/agent
docker compose exec -T registry /usr/local/bin/service \
  -data-dir=/data -admin-token-file=/data/admin-token \
  -registry-addr=127.0.0.1:9443 \
  -mint-token -bind-node-id=local-ollama-01 > ../data/agent/bootstrap-token
docker compose cp registry:/data/ca/ca-cert.pem ../data/agent/ca.pem
```

绑定令牌代表运维对该节点的授权，可直接注册；未绑定令牌需要先审批节点。
随后切回仓库根目录运行 Agent：

```bash
cd ..
go run ./service/aiServeWeaveAgent \
  -node-id=local-ollama-01 \
  -registry=127.0.0.1:9443 -ca-file=./data/agent/ca.pem \
  -bootstrap-token-file=./data/agent/bootstrap-token \
  -cert-file=./data/agent/cert.pem -key-file=./data/agent/key.pem \
  -gateway=127.0.0.1:8443 \
  -ollama-url=http://127.0.0.1:11434 \
  -metrics-addr=127.0.0.1:9092 \
  -labels=region=local
```

Agent 指标使用 `9092`，避免与宿主机上的 Registry 指标端口冲突。注册成功后令牌文件由
Agent 删除；重启沿用节点证书和私钥。长期运行的本地 YAML 配置见
[Agent README](../service/aiServeWeaveAgent/README.md)。

## 日常维护

返回 `deploy` 目录后执行以下命令：

| 操作 | 命令 |
| --- | --- |
| 拉取新代码并重新部署 | `./upgrade.sh` |
| 查看全部容器与一次性任务 | `docker compose ps --all` |
| 查看某服务日志 | `docker compose logs --tail=100 <service>` |
| 修改 `.env` 后应用配置 | `docker compose up -d` |
| 修改挂载的 `controlplane.yaml` 后加载 | `docker compose restart controlplane` |
| 重建单个业务服务 | `docker compose up -d --no-deps --build <service>` |
| 重跑账户初始化 | `docker compose up -d --no-deps --force-recreate bootstrap` |
| 停止并保留容器与数据 | `docker compose stop -t 45` |
| 移除容器并保留数据卷 | `docker compose down -t 45` |

Compose 会检测服务配置或镜像变化并重建容器，`restart` 不会重新读取 `.env`；
只有需要强制重建时才加 `--force-recreate`。
参见 [docker compose up](https://docs.docker.com/reference/cli/docker/compose/up/)。

数据库、Redis 会话状态和 Registry 身份材料分别持久化在 `mysql-data`、`redis-data`、
`registry-data`。长期部署固定目录及 Compose 项目名，避免换目录后连接到新的空数据卷。
升级前备份配置、数据库与 Registry 身份材料；数据库升级与恢复步骤见
[数据库升级与恢复](database-recovery.md)。不要将删除数据卷作为常规故障处理方式。

专属节点（`/operator/node-access`）要求所有 Gateway 副本已经升级到支持它的版本：
旧版 Gateway 会忽略控制面下发的 `nodes` 字段，绑定在那些副本上不生效。分批升级时，
先升级全部 Gateway，再绑定任何节点；见
[ControlPlane STATUS「专属节点」](../service/aiServeWeaveControlPlane/STATUS.md#专属节点租户节点池)。

商户自有节点（租户控制台「我的节点」`/console/nodes`）默认开启，每个商户最多同时 10 台。
参考部署的 `controlplane.example.yaml` 已带上配置块，`install.sh` 与 `upgrade.sh` 会给缺少它的
`controlplane.yaml` 补上：

```yaml
TenantNodes:
  Enabled: true                                      # 设为 false 即关闭
  RegistryAddr: "${AISW_TENANT_NODE_REGISTRY_ADDR}"  # compose 取自 .env 的 REGISTRY_ADVERTISE_ADDR
  GatewayAddr: "${AISW_TENANT_NODE_GATEWAY_ADDR}"    # compose 取自 .env 的 GATEWAY_ADVERTISE_ADDR
  MaxPerTenant: 10       # 每商户同时有效的节点数，1–100
  MaxCreatesPerDay: 10   # 每商户 24 小时内可生成的接入包数，1–1000
```

两个地址写进接入包，域名须分别包含在 Registry 证书与 Gateway 隧道证书中；compose 默认值为
`registry.open7x.com:9443` 与 `agent.open7x.com:8443`。它还沿用 `Registry.Addr`、`Registry.AdminToken`、
`Registry.CACertFile`：地址或 Registry 任一侧为空时功能不生效（接口 404，启动日志说明），只配了一半时
控制面拒绝启动。
**分批升级时先把 `Enabled` 设为 `false`，等 Registry、控制面与全部 Gateway 都升级、数据库迁移
`0012_tenant_nodes` 执行完，再改回 `true`**（单机 compose 一次升级全部服务，无需此步）：
旧版 Gateway 不认识 `tnode.` 归属，会把商户自有节点当作共享节点派给所有商户；旧版 Registry
不会拒绝未绑定令牌自报 `tnode.` id。商户的 owner/admin 生成接入包后，按上面 Agent 接入的同一套
发行包与 `README.txt` 安装；见
[ControlPlane STATUS「商户自有节点」](../service/aiServeWeaveControlPlane/STATUS.md#商户自有节点)。

### 证书链是怎么闭合的

Registry 首次生成 CA；`registry-init` 用同一个 CA 为 Gateway 签发服务端证书。
Agent 用公共 `ca-cert.pem` 验证 Registry 与 Gateway，Gateway 用该 CA 验证节点证书。

### 服务端证书有效期与旧部署升级

Registry 默认自签证书和 Gateway 服务端证书最长有效十年，且不超过 CA 的剩余有效期。
CA 首次生成时有效十年；Agent 节点证书有效三十天并自动续期。

已有的三十天服务端证书需重新签发。在维护窗口执行以下命令，保留原 CA：

```bash
docker compose build registry registry-init gateway
docker compose up -d --no-deps --force-recreate registry
docker compose run --rm --no-deps registry-init
docker compose up -d --no-deps --force-recreate -t 45 gateway
```

Gateway 重建会断开现有隧道，Agent 自动重连，无需重新注册。
自行提供的证书文件按签发方的有效期更新。证书细节见
[Registry STATUS](../service/aiServeWeaveRegistry/STATUS.md)。

## 对外部署与已知限制

- go-zero 默认开启访问日志中间件。控制面返回任何 5xx 时，它会用 `httputil.DumpRequest`
  把完整请求写进 error 日志，包括 `Authorization` 头与请求体，内部令牌和登录密码因此会进入日志。
  在代码修复之前，`controlplane.yaml` 必须包含 `Middlewares: {Log: false}`；
  `install.sh` 与 `upgrade.sh` 会自动补上这一段。已经泄露进日志的令牌要轮换，并用
  `docker compose up -d --force-recreate` 重建容器，旧容器的日志随之删除。
- Registry gRPC `9443` 与 Gateway Agent 隧道 `8443` 默认绑定 `0.0.0.0`，供远程节点
  直接建立 TLS/mTLS 连接；在云安全组或防火墙中限制为 Agent 的出口 IP。其他宿主机端口
  继续绑定 `127.0.0.1`。Console 和 Gateway HTTP API 通过 HTTPS 反向代理接入；设置
  `AISW_CONSOLE_COOKIE_SECURE=true`，并让代理保留外部 `Host`。Nginx 配置见下文
  [HTTPS 反向代理](#https-反向代理)。
- 当前远程 Agent 入口为 `registry.open7x.com:9443`、`agent.open7x.com:8443`，两条 DNS
  记录使用 DNS only／仅 DNS，指向平台源站。Registry 与 Gateway 证书分别包含对应域名；
  `GATEWAY_ADVERTISE_ADDR` 默认使用 `agent.open7x.com:8443`。使用其他域名时同步修改
  两处 `-tls-host` 与通告地址；修改 `GATEWAY_TUNNEL_PORT` 时同步通告端口。
- Gateway 镜像内置 Agent 制品，供 `/agent/*` 一行命令安装使用。构建时按 `.env` 的
  `AGENT_PLATFORMS`（默认 linux、darwin、windows 的 amd64 与 arm64 共六个平台）以同一
  `VERSION` 交叉编译 Agent，落到镜像内的 `/opt/aisw/agent-dist/<VERSION>/`，并附带
  `agent.sh`、`agent.ps1`、`install.sh`、`install.ps1` 与覆盖全部文件的 `SHA256SUMS.txt`。
  未设置 `AGENT_PUBLIC_URL` 时 Gateway 不挂载 `/agent/*`，控制面不提供签发。其他服务镜像只多一个空目录。
  本地实测（依赖层已缓存）：六平台约增加 98 MB 镜像体积（Gateway 镜像约 195 MB，单平台约 76 MB），
  构建约 52 秒；首次构建要另外下载 Go 模块。只需要部分平台时设置 `AGENT_PLATFORMS="linux/amd64"` 缩短构建。
  公开入口需要反向代理把 `/agent/` 前缀转给 Gateway HTTP 端口。
  **Agent 修复的分发**：Agent 源码改动也需要重建并部署 Gateway 镜像，平台才会在
  `/agent/v1/manifest` 和 `/agent/download/<VERSION>/` 提供新的 Agent。现有运行节点仍需
  各自升级并重启；更新平台制品不会自动替换节点程序。2026-11-04 的 Claude 长工具链
  400 修复已在 `ca16c33` 进入源码，发行包需包含该修复，见 [Agent 升级说明](agent/README.md#claude-code-长工具链-400-修复)。
- 当前为单 Registry、单 Gateway 编排。多副本方案见对应服务 README。
- 默认数据库为 MySQL。`postgres` profile 只额外启动 PostgreSQL，不切换控制面数据源；
  Job 持久化目前仅支持 MySQL。
- `controlplane.yaml` 是容器配置。长期部署在独立迁移完成后关闭 `Database.AutoMigrate`，
  按 [数据库升级与恢复](database-recovery.md) 操作。
- Redis 是会话的权威状态，已启用 AOF 和数据卷；不可用时管理请求返回 `503`。
  不要恢复早于吊销或改密操作的 Redis 快照。

### HTTPS 反向代理

完整示例见 [nginx.example.conf](nginx.example.conf)，复制为 `/etc/nginx/conf.d/aiserveweave.conf`
后替换域名与证书路径。示例拓扑：

| 域名 | 后端 | Cloudflare | 证书 |
|---|---|---|---|
| `open7x.com`、`www` | Console `:3000`，`/agent/` 转 Gateway `:8080` | 代理（橙色云） | 可用 Cloudflare Origin CA |
| `api.open7x.com` | Gateway `:8080` | 仅 DNS（灰色云） | 必须公开受信，如 Let's Encrypt |

API 不经过 Cloudflare 代理：非 Enterprise 方案的请求体上限为 100 MB，低于 Gateway 工作流上传的
200 MiB；源站 100 秒内未返回响应头即返回 524，且无法调整。流式请求有心跳不受影响，非流式长请求会失败。
`registry`、`agent` 两个子域名本就仅 DNS 指向源站，API 改为仅 DNS 不会额外暴露源站 IP；但 443 不能再在
防火墙中只放行 Cloudflare 网段。

Origin CA 证书只受 Cloudflare 信任，API 切换为仅 DNS **之前**先换证书。DNS-01 验证不受代理状态影响，
可在橙色云期间完成：

1. Cloudflare → My Profile → API Tokens → Create Token，选 **Edit zone DNS** 模板，Zone Resources 限定
   该域名，可加 Client IP 过滤为服务器 IP，TTL 留空（设置过期会导致自动续期失败）。不要使用 Global API Key。
2. 在服务器上签发并安装（`--fullchain-file` 不能换成只含叶证书的文件，否则部分 SDK 校验失败）：

   ```bash
   curl https://get.acme.sh | sh -s email=<联系邮箱>
   source ~/.bashrc
   read -rs CF_Token && export CF_Token     # 输入不回显，也不进 shell 历史
   acme.sh --issue --dns dns_cf -d api.open7x.com --server letsencrypt --keylength ec-256
   acme.sh --install-cert -d api.open7x.com --ecc \
     --key-file       /etc/nginx/ssl/api.open7x.com.key \
     --fullchain-file /etc/nginx/ssl/api.open7x.com.pem \
     --reloadcmd      "nginx -t && systemctl reload nginx"
   acme.sh --upgrade --auto-upgrade
   unset CF_Token
   ```

   acme.sh 把 token 保存在 `/root/.acme.sh/account.conf`（`SAVED_CF_Token`），按 CA 建议的时段自动续期并
   执行 `--reloadcmd`。轮换 token 后同步修改该值。
3. 修改 Nginx 的 api 证书路径后执行 `nginx -t && systemctl reload nginx`。`nginx -T` 读取的是磁盘配置，
   未 reload 时正在运行的进程仍发旧证书。
4. 仍是橙色云时绕过 Cloudflare 验证源站证书，看到 `Let's Encrypt` 与 `SSL certificate verify ok` 再切换为仅 DNS：

   ```bash
   curl -sv --resolve api.open7x.com:443:<源站IP> https://api.open7x.com/ -o /dev/null 2>&1 \
     | grep -E "subject:|issuer:|verify"
   ```

   校验失败时 curl 不打印证书信息，加 `-k` 查看实际发出的证书。

橙色云站点的 Cloudflare 设置：SSL/TLS 模式用 **Full (strict)**（Flexible 会以 80 端口回源，被 301 重定向成循环）；
关闭 **Bot Fight Mode**（会拦截 `curl … | bash` 一行命令安装，Free 方案无法按路径豁免）、**Rocket Loader**
与 **Email Address Obfuscation**（改写 HTML，导致 Next.js hydration 不一致）；不要为 Console 添加 Cache Everything 规则。

### 已有部署启用一行命令安装

在**平台服务器**的仓库里一步完成（以 `https://open7x.com` 为例，改成实际站点地址）：

```bash
git pull
deploy/enable-agent-install.sh https://open7x.com
# 验收只需要部分平台时可缩短约 10 分钟的构建：
# deploy/enable-agent-install.sh https://open7x.com --platforms "linux/amd64 darwin/arm64 windows/amd64"
```

脚本依次做：检查版本（工作区必须干净，否则版本带 `-dirty`，安装脚本会拒绝）、写 `.env` 的 `AGENT_PUBLIC_URL`、补 `controlplane.yaml` 的 `AgentInstall` 段、**带版本**构建并重建 `gateway` 与 `controlplane`，最后自检 Gateway 本机入口、安装脚本和公网入口。哪一步没通过，会直接说明原因。公网入口不通时，会打印要加到 Nginx 的内容：

```nginx
location /agent/ {
    proxy_pass http://127.0.0.1:8080;   # Gateway HTTP 端口，即 GATEWAY_PORT
    proxy_set_header Host $host;
    proxy_buffering off;                # 制品较大，不落盘缓冲
    client_max_body_size 4k;            # 兑换请求体不超过 1 KiB
}
```

`/agent/` 是公开入口，不带 Cookie，不要放进 Console 的 `location`，也不要对它启用登录校验。加好后 `nginx -t && nginx -s reload`，再运行 `deploy/enable-agent-install.sh https://open7x.com --no-build` 复查。

通过后，在 Console 生成命令，到**节点机器**（不是平台服务器）上执行。Console 在生成命令后，会读取本站 `/agent/v1/manifest`，平台版本是 `dev`/`-dirty` 或入口不通时在命令上方显示红色警告。

两处配置必须一致：`AGENT_PUBLIC_URL` 决定 Gateway 是否挂载 `/agent/*`，`AgentInstall.PublicURL` 决定控制面是否签发并写进命令。`deploy/install.sh --agent-public-url`、`deploy/upgrade.sh` 与上面的脚本都按 `.env` 的值写入；`controlplane.yaml` 已有 `AgentInstall` 段时不会改写，不一致时脚本会拒绝并说明怎么改。

**手动步骤**（不用脚本时）：Gateway 与控制面必须带版本构建。直接 `docker compose up --build` 不传 `VERSION`，版本变成 `dev`，安装脚本会拒绝。

```bash
VERSION=$(git -C .. describe --tags --always --dirty); echo "$VERSION"   # 不能含 dev 或 dirty
docker compose build --build-arg VERSION="$VERSION" controlplane gateway &&
docker compose up -d --no-build --force-recreate controlplane gateway
curl -fsS https://open7x.com/agent/v1/manifest   # version 不是 dev，"installable":true
```

关闭时清空 `AGENT_PUBLIC_URL` 和 `AgentInstall.PublicURL` 后重建，`/agent/*` 不再挂载，已签发的兑换码到期失效。

### 已有部署启用远程 Agent

在原来的 `deploy` 目录拉取新配置后执行。已有 `.env` 中的旧通告值会覆盖 Compose 默认值，
因此先备份并更新该项；重新签发服务端证书沿用原有 `registry-data` 中的 CA。
以下命令会短暂重启 Registry 和 Gateway，Nginx 继续使用现有网站与 API 配置。

```bash
cp -p .env ".env.before-remote-agent.$(date +%Y%m%d-%H%M%S)"
sed -i '/^GATEWAY_ADVERTISE_ADDR=/d' .env
printf '\nGATEWAY_ADVERTISE_ADDR=agent.open7x.com:8443\n' >> .env

docker compose config --quiet &&
docker compose run --rm --no-deps registry-init &&
docker compose up -d --no-deps --no-build --force-recreate registry &&
docker compose up -d --no-deps --no-build --force-recreate --timeout 45 gateway

docker compose ps registry gateway
docker compose exec -T gateway /usr/local/bin/service -version
```

确认端口映射为 `0.0.0.0:9443`、`0.0.0.0:8443`，并在 Agent 所在网络验证 DNS、端口和
证书，再按「接一个 Agent」签发绑定令牌、复制公共 CA；预编译安装见
[Agent 独立部署](agent/README.md)。配置解析通过不代表远程系统已完成接入验收。

历史指标为可选项：在 `controlplane.yaml` 添加以下配置，再重启控制面。
Fleet、模型拉取和 ComfyUI Managed 转发已在默认配置中接好。

```yaml
MetricsHistory:
  GatewayAddrs: ["http://gateway:9090"]
  RegistryAddr: "http://registry:9091"
```

### 托管供应商远程下发

商户在 `/console/nodes`、平台运维在 `/operator/node-providers` 为节点填写 OpenAI 等托管
接口与 Key，由控制面下发到 Agent。**默认开启，无需手动配置**：`install.sh` 与 `upgrade.sh`
会在 `.env` 中生成 `AISW_GATEWAY_REMOTE_PROVIDERS_TOKEN` 与 `AISW_PROVIDER_MASTER_KEY`，并给
`controlplane.yaml` 补上 `RemoteProviders` 配置块。已有部署执行一次 `deploy/upgrade.sh` 即可开启。

`AISW_PROVIDER_MASTER_KEY` 静态加密已保存的 Key，丢失或更换后所有已保存的 Key 都读不出来，
需要重新填写，请备份 `deploy/.env`。要关闭时把 `controlplane.yaml` 里整个 `RemoteProviders` 配置块
（含 `RemoteProviders:` 这一行）注释掉再重启控制面；脚本看到 `# RemoteProviders:` 就不会再补回。
不要只留一行空的 `RemoteProviders:`，控制面会因类型不匹配拒绝启动。

Agent 侧不需要任何配置，商户节点与平台节点升级到 2026-11-02 之后的版本即可接收。Key 只以封装给节点证书公钥的密文
经过 Gateway，规则见[托管供应商说明「控制面远程下发」](../common/runtime/hosted/README.md#控制面远程下发)。
已完成默认测试，未经真实环境验收。

## 常见问题

| 现象 | 处理 |
| --- | --- |
| `config --quiet` 提示 `set it in deploy/.env` | 补齐八个密钥；旧部署保留已有值，只补缺失项 |
| 端口已被占用 | 修改 `.env` 对应端口后执行 `docker compose up -d` |
| `bootstrap` 为 `Exited (1)` | 查看其日志；检查邮箱、密码和控制面状态，修正后单独重跑 |
| Console 可打开但登录失败 | 查看 `controlplane` 日志及 `bootstrap` 创建结果；确认使用对应账户的登录入口 |
| Registry 数据目录权限错误 | 核对卷挂载与属主，镜像运行 UID 为 `10001`；先备份，再修复目录权限 |
| Agent 注册失败 | 检查绑定令牌的节点 ID、有效期和 CA；令牌已用或过期时重新签发 |
| Agent 连接失败或证书主机名不匹配 | 同时检查端口绑定、通告地址及证书 SAN |

### 第一个租户与第一个 key

主流程使用 bootstrap 建号、Console 创建 Key。选择手动建号时，留空相应
`AISW_DEFAULT_*_EMAIL`，在 `deploy` 目录执行下面的租户创建请求。
示例适用于本指南生成的 `.env`；自行编辑的值需使用兼容 shell 的引号格式。

```bash
source .env
curl --fail-with-body --connect-timeout 5 --max-time 15 \
  -X POST http://127.0.0.1:8090/admin/v1/tenants \
  -H "Authorization: Bearer $AISW_BOOTSTRAP_TOKEN" \
  -H 'Content-Type: application/json' \
  -d '{"name":"Default","owner_email":"owner@example.com","owner_password":"replace-with-a-random-password"}'
```

手动创建平台运维使用 `POST /admin/v1/platform/operators`，同样携带 bootstrap token，
JSON 字段为 `email`、`password`、`name`。其余接口见
[ControlPlane STATUS](../service/aiServeWeaveControlPlane/STATUS.md)。

## Agent 独立部署

Ubuntu systemd、Apple Silicon macOS LaunchAgent、Windows 11 原生用户计划任务的安装与维护见 [Agent 部署指南](agent/README.md)。包含固定版本在线/本地安装、状态/停止/重载、显式升级/回退与保留身份卸载。目标系统实机验收状态另列，不能由构建通过推定。
