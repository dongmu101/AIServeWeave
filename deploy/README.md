服务器安装

初次安装
deploy/install.sh \
  --owner-email <控制台邮箱> \
  --operator-email <运维邮箱> \
  --https \
  --advertise-addr agent.open7x.com:8443 \
  --agent-public-url https://open7x.com

make release VERSION=v3.0.0

make publish VERSION=v3.0.0 AGENT_DIST=dist/agent-dist PUBLISH_TAGS="v3.0.0 latest" FORCE=1

更新
make  upgrade.sh