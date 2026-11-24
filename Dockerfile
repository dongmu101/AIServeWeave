# One Dockerfile for the three Go services, selected by SERVICE.
#
# Nothing is compiled here. scripts/build-go.sh compiles on the host into
# .image-build/<SERVICE>/ (or, for a release, takes build-release.sh's
# binaries), and this file only copies the binary for the target platform.
# The image build therefore needs neither the Go toolchain nor the module
# download, and a release image carries the same bytes as the GitHub Release.
# make images / make publish / make release and deploy/upgrade.sh --build run
# the script first; a bare docker build must too:
#
# 三个 Go 服务共用一个 Dockerfile，由 SERVICE 选择。
#
# 这里不编译任何东西。scripts/build-go.sh 在宿主机上编译到 .image-build/<SERVICE>/（发布时
# 则直接取 build-release.sh 的二进制），本文件只拷贝目标平台的二进制。镜像构建因此既不需要
# Go 工具链，也不下载模块，发布出去的镜像与 GitHub Release 是同一批字节。make images /
# make publish / make release 与 deploy/upgrade.sh --build 会先执行该脚本；直接 docker build
# 也要先执行：
#
#   scripts/build-go.sh aiServeWeaveGateway
#   docker build --build-arg SERVICE=aiServeWeaveGateway -t aisw-gateway .
FROM alpine:3.24
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
# The ARGs come after the shared layer above, so all three images reuse it.
# ARG 放在上面的公共层之后，三个镜像共用该层。
ARG SERVICE
ARG TARGETOS
ARG TARGETARCH
COPY .image-build/${SERVICE}/${TARGETOS}_${TARGETARCH}/service /usr/local/bin/service
# The Agent artifacts the Gateway serves under /agent/download/<VERSION>/;
# empty for every other service.
# Gateway 在 /agent/download/<VERSION>/ 下提供的 Agent 制品；其他服务为空目录。
COPY .image-build/${SERVICE}/agent-dist/ /opt/aisw/agent-dist/
ENTRYPOINT ["/usr/local/bin/service"]
