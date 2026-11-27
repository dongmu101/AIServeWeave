服务器安装

初次安装
deploy/install.sh \
  --owner-email <控制台邮箱> \
  --operator-email <运维邮箱> \
  --https \
  --advertise-addr agent.open7x.com:8443 \
  --agent-public-url https://www.open7x.com

make release VERSION=v3.0.0

make publish VERSION=v3.1.1 PREBUILT=dist PUBLISH_TAGS="v3.1.1 latest" FORCE=1   # dist/ 须是 v3.1.1 的 build-release.sh 产物；不传 PREBUILT 则在本机编译

更新

```bash
deploy/upgrade.sh
```

升级收尾时，脚本默认在 Linux 宿主机执行 `sync`，再向 `/proc/sys/vm/drop_caches` 写入 `1`，释放整台宿主机可丢弃的文件页缓存。权限不足、接口受限或刷盘失败时会告警，升级仍会完成。清理后首次读取文件可能增加磁盘 I/O。

升级会重建 Gateway。Compose 给 Gateway 的 `stop_grace_period` 为 75 秒：Gateway 收到 SIGTERM 后先等进行中的请求结束，再关闭 Agent 隧道，每段最多 30 秒，空闲时立即退出。新容器开始监听并等 Agent 重新连上之前，单副本部署的新请求会短暂收到 502 或 503，客户端重试即可。

## 跨地域隧道网络调优

Agent 与 Gateway 分处不同地域时，隧道 RTT 常在 100–300ms，跨境链路还伴随丢包。请求体（Claude Code 一轮可达数 MB）由 Gateway 发往 Agent，Gateway 是发送方，以下调优以 Gateway 宿主机为主。

**1. 拥塞控制改为 BBR（宿主机）。** CUBIC 遇丢包即窗口减半，长距离有损链路上吞吐会塌陷。新建的网络命名空间继承宿主机的拥塞控制算法，因此只需在宿主机设置，Gateway 容器随之生效：

```bash
sudo modprobe tcp_bbr
echo tcp_bbr | sudo tee /etc/modules-load.d/bbr.conf
sudo tee /etc/sysctl.d/90-aisw-tunnel.conf <<'EOF'
net.core.default_qdisc = fq
net.ipv4.tcp_congestion_control = bbr
net.ipv4.tcp_slow_start_after_idle = 0
EOF
sudo sysctl --system
sysctl net.ipv4.tcp_congestion_control   # 应输出 bbr
```

**2. 关闭空闲后慢启动。** 隧道在请求之间空闲，Linux 默认空闲后重置拥塞窗口，每个大请求都重新慢启动。该参数不继承宿主机，[docker-compose.yaml](docker-compose.yaml) 已在 Gateway 容器上设置 `net.ipv4.tcp_slow_start_after_idle=0`；上面的宿主机配置覆盖经 docker-proxy 在宿主机终结的连接。

**3. 缓冲区。** 内核默认 `tcp_wmem`/`tcp_rmem` 上限为 4–6 MiB，足够覆盖 100 Mbps × 300ms 的 BDP。更高带宽时按 `带宽 × RTT` 调大 `net.ipv4.tcp_wmem`、`net.ipv4.tcp_rmem` 的第三个值以及 `net.core.wmem_max`、`net.core.rmem_max`。

**4. Gateway 放置。** 链路为 用户 → Gateway → Agent →（CLI 运行时的上游 API）。Gateway 应放在质量最差那一段的近端：用户或 Agent 在中国大陆而上游在海外时，带优质回程线路的香港、日本节点通常优于美国节点。

**5. 隧道自身已做的优化。** 大于 16 KiB 的请求 payload 由 Gateway 以 zstd 压缩后经隧道发送（Agent 在 Hello 中声明支持，旧版 Agent 收到原文）；流式响应不压缩，避免攒帧拖慢首字。调度器在同等条件下优先选择隧道 RTT 更低的节点。BULK 槽（产物上传下载）使用独立 TCP 连接，丢包重传不阻塞推理流。

### 测量基线

改动前后用同一组数字对比，不要只看 loopback 实测：

- `tunnel_control_heartbeat_rtt_seconds`（Agent 指标）与 `tunnel_server_node_rtt_seconds`（Gateway 指标）：隧道 RTT。
- `tunnel_server_stream_first_event_seconds` 与 Agent 侧 `tunnel_stream_first_event_seconds` 的差：隧道对首字的贡献。
- `tunnel_server_frame_bytes{direction="outbound"}`：压缩后实际发出的请求帧大小。
- `tunnel_reconnects_total`：链路是否频繁断开。

没有真实跨地域环境时，在一台 **非生产** Linux 机器上用 netem 给隧道端口注入延迟与丢包，再让 Agent 连接它：

```bash
IFACE=eth0   # Agent 连接进来的网卡
sudo tc qdisc add dev "$IFACE" root handle 1: prio
sudo tc qdisc add dev "$IFACE" parent 1:3 handle 30: netem delay 100ms 20ms loss 1%
sudo tc filter add dev "$IFACE" parent 1: protocol ip u32 match ip sport 8443 0xffff flowid 1:3
# 测完删除
sudo tc qdisc del dev "$IFACE" root
```

过滤器只作用于隧道端口（默认 8443）发出的包；单向 100ms 加上回程即约 200ms RTT。切勿在承载线上流量的网卡上执行。

## 服务日志文件

Compose 自动为 Gateway、Registry、ControlPlane、Console 和 Studio 创建 `deploy/logs/<服务>/`。在 `/opt/AIServeWeave` 部署时，文件路径为 `/opt/AIServeWeave/deploy/logs/<服务>/<服务>.log`，例如 `logs/controlplane/controlplane.log`。

各服务的标准输出和标准错误会追加到对应文件，并继续出现在 `docker compose logs`。文件按 10 MiB 轮转，保留 `.log.1` 和 `.log.2` 两份归档。文件留在宿主机，容器重建后继续追加。多个副本须分别挂载独立目录。

数据库迁移等通过 `docker compose run` 覆盖命令的一次性操作直接输出到终端。若用 `docker-compose.override.yaml` 覆盖常驻服务的 `command`，首个参数须保留 `--aisw-capture-logs` 才会启用文件日志。

在部署目录下查看或检索日志：

```bash
tail -F logs/gateway/gateway.log
grep -F '<请求 ID>' logs/gateway/*.log*
```

同步 `docker-compose.yaml` 和 `service-logs.sh` 后，在部署目录运行 `docker compose up -d --no-build --pull never`，用现有镜像按配置重建容器。部署目录里的文件从启用后开始记录。旧容器现存日志可在重建前用 `docker compose logs` 导出。

## 对象存储

文件有两类去处，都在 `deploy/.env` 里开启，留空即关闭：

- **部署存储**：由 `AISW_ARTIFACT_STORAGE` 开启，取 `s3` 或 `webdav`。存放 job 产物，以及未配置自有存储的租户的 Studio 文件。不开启时产物只能从节点实时拉取，Studio 也无法为这些租户保存推理生图与上传的文件。
- **租户自有存储**：由 `AISW_TENANT_STORAGE_MASTER_KEY` 开启，还需要把 `controlplane.example.yaml` 的 `TenantStorage` 块加进 `controlplane.yaml`。之后租户的 owner/admin 可以在 Console 为本租户填写自己的 S3 或 WebDAV。租户的 Access Key 在 Console 里填写，不写进 `.env`。

以 Cloudflare R2 作为部署存储为例：

```bash
AISW_ARTIFACT_STORAGE=s3
AISW_ARTIFACT_STORAGE_S3_ENDPOINT=https://<账户 ID>.r2.cloudflarestorage.com
AISW_ARTIFACT_STORAGE_S3_REGION=auto
AISW_ARTIFACT_STORAGE_S3_BUCKET=<bucket>
AISW_ARTIFACT_STORAGE_S3_PREFIX=platform
AISW_ARTIFACT_STORAGE_S3_PATH_STYLE=true
AISW_ARTIFACT_STORAGE_S3_ACCESS_KEY_ID=<R2 访问密钥 ID>
AISW_ARTIFACT_STORAGE_S3_SECRET_ACCESS_KEY=<R2 机密访问密钥>
```

- 修改后执行 `docker compose up -d gateway`。
- 凭据只经环境变量传给 Gateway，不出现在进程参数里。`deploy/.env` 请设为 `chmod 600`。
- 保留期用 Gateway 的 `-artifact-retention`、`-artifact-preview-retention` 与 `-studio-artifact-retention` 调整，默认值见 `gateway -h`。
- 部署存储与租户存储可以用同一个 bucket 下的不同 prefix，但建议分开 bucket 与 token。

## 公开站点与计费检查

- 7x 的公开 Console 入口为 `https://www.open7x.com`。从公网检查 `/`、`/signup`、`/console/docs/clients` 和 `/pricing`，再对照源站 Console 与 HTTPS 反向代理的响应。源站返回 200 无法证明公网 DNS 与 TLS 正常。
- `/billing/v1/catalog` 返回实际目录。四张列表为空时，检查定价、商品发布和支付配置。准备接收真实付款后，在所用渠道验收通知、到账、逐笔用量和退款。Dodo 的通知 URL 为 `https://www.open7x.com/billing/v1/dodo/webhook`，易支付为 `https://www.open7x.com/billing/v1/ezfp/notify`。使用 [Nginx 配置示例](nginx.example.conf) 对应的精确路径。
- Compose 已启用 Redis AOF。当 Gateway 配置了控制面和 Redis 时，待上报用量跨 Gateway 重启保留。监控部署将 [用量账本告警](usage-ledger-alerts.yml) 加入 Prometheus 的 `rule_files`，抓取各 Gateway 的内部 `/metrics` 并连接告警通知。先检查 Redis 和控制面故障，隔离记录由运维核对后处理。
