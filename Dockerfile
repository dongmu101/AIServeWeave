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

FROM golang:1.27-alpine AS build
ARG SERVICE
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
RUN test -n "$SERVICE" || (echo "SERVICE build-arg is required" >&2; exit 1) && \
    CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/service ./service/${SERVICE}

FROM alpine:3.22
# ca-certificates is for outbound TLS — the Gateway calls the control plane
# over HTTPS in a real deployment. The tunnel's own trust comes from the
# Registry CA, which is mounted, not baked in.
#
# ca-certificates 用于出站 TLS——真实部署中 Gateway 通过 HTTPS 调用控制面。隧道自身的
# 信任来自 Registry 的 CA，那是挂载进来的，不是打进镜像的。
RUN apk add --no-cache ca-certificates && adduser -D -u 10001 aisw
USER aisw
COPY --from=build /out/service /usr/local/bin/service
ENTRYPOINT ["/usr/local/bin/service"]
