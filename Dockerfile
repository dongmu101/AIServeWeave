# One Dockerfile for all three binaries, selected by SERVICE.
#
# They share a module, a dependency set and a build command, so three files
# would be three copies of one recipe that drift apart the first time somebody
# bumps the Go version in two of them.
#
# 三个二进制共用一个 Dockerfile，由 SERVICE 选择。
#
# 它们共用一个 module、一套依赖与同一条构建命令，因此三个文件就是同一份配方的三份
# 拷贝——等哪天有人只在其中两个里升了 Go 版本，它们就开始漂移了。
#
#   docker build --build-arg SERVICE=aiServeWeaveGateway -t aisw-gateway .

# The build stage runs on the builder's own platform and cross-compiles for the
# target (CGO is off, so no target toolchain is needed). Building a linux/amd64
# image on an arm64 Mac, or the arm64 image in release CI, would otherwise run
# the whole Go compile under QEMU, several times slower.
#
# 构建阶段跑在构建机自己的平台上，交叉编译到目标平台（CGO 关闭，不需要目标平台工具链）。
# 否则在 arm64 Mac 上构建 linux/amd64 镜像、或在 release CI 里构建 arm64 镜像时，整个 Go
# 编译都要在 QEMU 模拟下跑，慢好几倍。
FROM --platform=$BUILDPLATFORM golang:1.27.4-alpine AS build
ARG SERVICE
ARG TARGETOS
ARG TARGETARCH
# VERSION is stamped into the binary's `main.version` (see each service's
# `-version` flag) so a running container reports the tag it was built from
# instead of always saying "dev". Release CI passes the tag; a plain
# docker build with no --build-arg VERSION keeps the "dev" default.
#
# VERSION 被打进二进制的 `main.version`（见各服务的 `-version` flag），好让一个正在
# 跑的容器报告它是从哪个 tag 构建的，而不是永远显示 "dev"。发布流水线会传入 tag；不带
# --build-arg VERSION 的普通 docker build 保留 "dev" 默认值。
ARG VERSION=dev
# GOPROXY lets a network that cannot reach proxy.golang.org point module
# downloads at a mirror: docker compose build --build-arg GOPROXY=https://goproxy.cn,direct
#
# GOPROXY 让访问不了 proxy.golang.org 的网络把模块下载指向镜像：
# docker compose build --build-arg GOPROXY=https://goproxy.cn,direct
ARG GOPROXY=https://proxy.golang.org,direct
ENV GOPROXY=${GOPROXY}
WORKDIR /src

# Dependencies are fetched in their own layer so a source-only change does not
# re-download the module cache.
#
# 依赖在自己的层里拉取，这样只改源码时不会重新下载模块缓存。
COPY go.mod go.sum ./
RUN go mod download

COPY . .
# CGO is off so the result runs on a distroless-style base with no libc to
# match. -trimpath keeps build machine paths out of the binary.
#
# 关闭 CGO，好让产物能跑在没有 libc 需要匹配的精简基础镜像上。-trimpath 让构建机的
# 路径不进入二进制。
#
# The go-build cache mount persists compiled packages across builds and across
# the services: COPY . . invalidates this layer on any source change, and
# without the mount every rebuild recompiles the whole dependency tree from
# scratch, which is most of the build time on a 2-core host.
#
# go-build 缓存挂载让已编译的包在多次构建之间、在各服务之间复用：任何源码改动都会让
# COPY . . 之后的这一层失效，没有这个挂载，每次重建都要从头编译整棵依赖树——这在 2 核主机上
# 占了构建时间的大头。
RUN --mount=type=cache,target=/root/.cache/go-build \
    test -n "$SERVICE" || (echo "SERVICE build-arg is required" >&2; exit 1) && \
    CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build -trimpath -ldflags="-s -w -X main.version=${VERSION}" -o /out/service ./service/${SERVICE}

# agent-dist cross-compiles the Agent for every platform in AGENT_PLATFORMS and
# lays out the directory the Gateway serves under /agent/. It does the work only
# when SERVICE is the Gateway; for every other service it leaves an empty
# directory, so their images differ by nothing but that empty directory.
#
# agent-dist 为 AGENT_PLATFORMS 中的每个平台交叉编译 Agent，并摆出 Gateway 在 /agent/
# 下提供的目录。只有 SERVICE 是 Gateway 时才真正干活；其他服务只留下一个空目录，镜像之间
# 的差别仅此而已。
FROM build AS agent-dist
ARG VERSION=dev
ARG AGENT_PLATFORMS="linux/amd64 linux/arm64 darwin/amd64 darwin/arm64 windows/amd64 windows/arm64"
ARG SERVICE
RUN --mount=type=cache,target=/root/.cache/go-build \
    set -eu; \
    mkdir -p /opt/aisw/agent-dist; \
    if [ "$SERVICE" != aiServeWeaveGateway ]; then exit 0; fi; \
    dir="/opt/aisw/agent-dist/${VERSION}"; \
    mkdir -p "$dir"; \
    for platform in $AGENT_PLATFORMS; do \
      goos="${platform%/*}"; goarch="${platform#*/}"; ext=""; \
      if [ "$goos" = windows ]; then ext=.exe; fi; \
      CGO_ENABLED=0 GOOS="$goos" GOARCH="$goarch" \
        go build -trimpath -ldflags="-s -w -X main.version=${VERSION}" \
        -o "$dir/aiserveweave-agent_${VERSION}_${goos}_${goarch}${ext}" ./service/aiServeWeaveAgent; \
    done; \
    for f in agent.sh agent.ps1 install.sh install.ps1; do cp "deploy/agent/$f" "$dir/$f"; done; \
    cd "$dir" && sha256sum -- * > SHA256SUMS.txt

FROM alpine:3.22
# ca-certificates is for outbound TLS — the Gateway calls the control plane
# over HTTPS in a real deployment. The tunnel's own trust comes from the
# Registry CA, which is mounted, not baked in.
#
# ca-certificates 用于出站 TLS——真实部署中 Gateway 通过 HTTPS 调用控制面。隧道自身的
# 信任来自 Registry 的 CA，那是挂载进来的，不是打进镜像的。

# /data is only the Registry's, for its CA and issued certificates
# (-data-dir), but it costs nothing to create in the other two images. It has
# to exist here, owned by aisw, rather than being created by the entrypoint at
# runtime: a fresh named volume mounted over an empty path in the image comes
# out owned by root, and USER below means the process can never be root to fix
# that itself. Docker seeds a new volume's ownership from the image directory
# it is mounted over, which is what this line is for.
#
# /data 只属于 Registry，装它的 CA 与签发的证书（-data-dir），但在另外两个镜像里建它
# 不费什么代价。它必须在这里建好、属主是 aisw，而不是留给运行时的入口进程去建：一个全新
# 的具名卷挂载到镜像里一个空路径上时，落地的属主是 root，而下面的 USER 意味着进程永远
# 不可能是 root 去自己修正这一点。Docker 用镜像里被挂载路径的属主来初始化一个新卷，这
# 一行就是为了让它能提供这个属主。
RUN apk add --no-cache ca-certificates && adduser -D -u 10001 aisw && \
    mkdir -p /data && chown aisw:aisw /data
USER aisw
COPY --from=build /out/service /usr/local/bin/service
# Empty for every service but the Gateway, whose agent-dist stage fills it.
# 除 Gateway 外都是空目录，由 Gateway 的 agent-dist 阶段填充。
COPY --from=agent-dist /opt/aisw/agent-dist /opt/aisw/agent-dist
ENTRYPOINT ["/usr/local/bin/service"]
