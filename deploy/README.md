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

## 公开站点与计费检查

- 7x 的公开 Console 入口为 `https://www.open7x.com`。从公网检查 `/`、`/signup`、`/console/docs/clients` 和 `/pricing`，再对照源站 Console 与 HTTPS 反向代理的响应。源站返回 200 无法证明公网 DNS 与 TLS 正常。
- `/billing/v1/catalog` 返回实际目录。四张列表为空时，检查定价、商品发布和支付配置。准备接收真实付款后，在所用渠道验收通知、到账、逐笔用量和退款。Dodo 的通知 URL 为 `https://www.open7x.com/billing/v1/dodo/webhook`，易支付为 `https://www.open7x.com/billing/v1/ezfp/notify`。使用 [Nginx 配置示例](nginx.example.conf) 对应的精确路径。
- Compose 已启用 Redis AOF。当 Gateway 配置了控制面和 Redis 时，待上报用量跨 Gateway 重启保留。监控部署将 [用量账本告警](usage-ledger-alerts.yml) 加入 Prometheus 的 `rule_files`，抓取各 Gateway 的内部 `/metrics` 并连接告警通知。先检查 Redis 和控制面故障，隔离记录由运维核对后处理。
