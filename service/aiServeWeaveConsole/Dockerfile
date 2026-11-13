# Build with the Console directory as context; no deployment secrets enter it.
# 以 Console 目录为构建上下文，不将部署密钥放入其中。
FROM node:26-bookworm-slim AS base
WORKDIR /app
ENV NEXT_TELEMETRY_DISABLED=1

FROM base AS dependencies
COPY package.json pnpm-lock.yaml pnpm-workspace.yaml ./
# Use the package manager version declared by the project and its lockfile.
# 使用项目声明的包管理器版本与锁文件。
# Cache mounts keep the npm cache and pnpm store across builds; they never enter the image.
# 缓存挂载让 npm 缓存与 pnpm store 跨构建保留，不进入镜像。
RUN --mount=type=cache,target=/root/.npm \
    --mount=type=cache,target=/pnpm/store \
    npm install --global "$(node -p 'require("./package.json").packageManager')" && \
    pnpm install --frozen-lockfile --store-dir /pnpm/store

FROM dependencies AS build
COPY . .
# Reuse the Next.js compilation cache across builds.
# 跨构建复用 Next.js 编译缓存。
RUN --mount=type=cache,target=/app/.next/cache pnpm build

FROM base AS runtime
ENV NODE_ENV=production \
    HOSTNAME=0.0.0.0 \
    PORT=3000
# Standalone excludes static assets, so copy both asset directories explicitly.
# standalone 不包含静态资源，因此显式复制两个资源目录。
COPY --from=build --chown=node:node /app/.next/standalone ./
COPY --from=build --chown=node:node /app/.next/static ./.next/static
COPY --from=build --chown=node:node /app/public ./public
USER node
EXPOSE 3000
CMD ["node", "server.js"]
