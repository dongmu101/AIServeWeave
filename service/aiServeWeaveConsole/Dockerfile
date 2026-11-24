# The Console is built on the host with scripts/build-web.sh (pnpm build); this
# image only copies that standalone output, so nothing is compiled in Docker.
# The output is plain JavaScript, so the same files serve amd64 and arm64.
# Build with the Console directory as context; no deployment secrets enter it.
#
# Console 先在宿主机用 scripts/build-web.sh（pnpm build）构建；本镜像只拷贝其 standalone
# 产物，Docker 里不编译任何东西。产物是纯 JavaScript，同一份文件适用于 amd64 与 arm64。
# 以 Console 目录为构建上下文，不将部署密钥放入其中。
#
# Alpine because its busybox wget lets the compose healthcheck probe without
# starting a second node process each time. musl is no concern: the output is
# plain JavaScript, and build-web.sh rejects native *.node modules.
#
# 用 Alpine，是因为它自带的 busybox wget 让 compose 健康检查不必每次另起一个 node 进程。
# musl 不构成问题：产物是纯 JavaScript，build-web.sh 会拒绝原生 *.node 模块。
FROM node:26-alpine
WORKDIR /app
ENV NODE_ENV=production \
    NEXT_TELEMETRY_DISABLED=1 \
    HOSTNAME=0.0.0.0 \
    PORT=3000
# Standalone excludes static assets, so copy both asset directories explicitly.
# standalone 不包含静态资源，因此显式复制两个资源目录。
COPY --chown=node:node .next/standalone ./
COPY --chown=node:node .next/static ./.next/static
COPY --chown=node:node public ./public
USER node
EXPOSE 3000
CMD ["node", "server.js"]
