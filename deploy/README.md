服务器安装

初次安装
deploy/install.sh \
  --owner-email <控制台邮箱> \
  --operator-email <运维邮箱> \
  --https \
  --advertise-addr agent.open7x.com:8443 \
  --agent-public-url https://www.open7x.com

make release VERSION=v3.0.0

make publish VERSION=v3.1.1 AGENT_DIST=dist/agent-dist PUBLISH_TAGS="v3.1.1 latest" FORCE=1

更新
make  upgrade.sh

## 公开站点与计费检查

- 7x 的公开 Console 入口为 `https://www.open7x.com`。从公网检查 `/`、`/signup`、`/console/docs/clients` 和 `/pricing`，再对照源站 Console 与 HTTPS 反向代理的响应。源站返回 200 无法证明公网 DNS 与 TLS 正常。
- `/billing/v1/catalog` 返回实际目录。四张列表为空时，检查定价、商品发布和支付配置。准备接收真实付款后，在所用渠道验收通知、到账、逐笔用量和退款。Dodo 的通知 URL 为 `https://www.open7x.com/billing/v1/dodo/webhook`，易支付为 `https://www.open7x.com/billing/v1/ezfp/notify`。使用 [Nginx 配置示例](nginx.example.conf) 对应的精确路径。
- Compose 已启用 Redis AOF。当 Gateway 配置了控制面和 Redis 时，待上报用量跨 Gateway 重启保留。监控部署将 [用量账本告警](usage-ledger-alerts.yml) 加入 Prometheus 的 `rule_files`，抓取各 Gateway 的内部 `/metrics` 并连接告警通知。先检查 Redis 和控制面故障，隔离记录由运维核对后处理。
