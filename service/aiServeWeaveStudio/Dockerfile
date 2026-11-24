# The Studio is built on the host with scripts/build-web.sh (pnpm build); this
# image only copies that standalone output, so nothing is compiled in Docker.
# The output is plain JavaScript, so the same files serve amd64 and arm64.
# Build with the Studio directory as context; no deployment secrets enter it.
#
# Studio 先在宿主机用 scripts/build-web.sh（pnpm build）构建；本镜像只拷贝其 standalone
# 产物，Docker 里不编译任何东西。产物是纯 JavaScript，同一份文件适用于 amd64 与 arm64。
# 以 Studio 目录为构建上下文，不将部署密钥放入其中。
#
# Alpine for the same reason as the Console: busybox wget serves the
# healthcheck without a second node process; the output is plain JavaScript.
# 与 Console 相同的理由用 Alpine：busybox wget 承担健康检查，不另起 node 进程；产物是纯 JavaScript。
FROM node:26-alpine
WORKDIR /app
ENV NODE_ENV=production \
    NEXT_TELEMETRY_DISABLED=1 \
    HOSTNAME=0.0.0.0 \
    PORT=3100
# Standalone excludes static assets, so copy both asset directories explicitly.
# standalone 不包含静态资源，因此显式复制两个资源目录。
COPY --chown=node:node .next/standalone ./
COPY --chown=node:node .next/static ./.next/static
COPY --chown=node:node public ./public
USER node
EXPOSE 3100
CMD ["node", "server.js"]
