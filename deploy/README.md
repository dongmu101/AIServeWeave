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

## 公开站点与计费检查

- 7x 的公开 Console 入口为 `https://www.open7x.com`。从公网检查 `/`、`/signup`、`/console/docs/clients` 和 `/pricing`，再对照源站 Console 与 HTTPS 反向代理的响应。源站返回 200 无法证明公网 DNS 与 TLS 正常。
- `/billing/v1/catalog` 返回实际目录。四张列表为空时，检查定价、商品发布和支付配置。准备接收真实付款后，在所用渠道验收通知、到账、逐笔用量和退款。Dodo 的通知 URL 为 `https://www.open7x.com/billing/v1/dodo/webhook`，易支付为 `https://www.open7x.com/billing/v1/ezfp/notify`。使用 [Nginx 配置示例](nginx.example.conf) 对应的精确路径。
- Compose 已启用 Redis AOF。当 Gateway 配置了控制面和 Redis 时，待上报用量跨 Gateway 重启保留。监控部署将 [用量账本告警](usage-ledger-alerts.yml) 加入 Prometheus 的 `rule_files`，抓取各 Gateway 的内部 `/metrics` 并连接告警通知。先检查 Redis 和控制面故障，隔离记录由运维核对后处理。
