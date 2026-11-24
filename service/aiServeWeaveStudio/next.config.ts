import type { NextConfig } from "next";

const nextConfig: NextConfig = {
  // Emit the minimal Node.js server used by the container image.
  // 输出容器镜像使用的最小 Node.js 服务端。
  output: "standalone",
  // Compression belongs to the reverse proxy (deploy/nginx.example.conf gzips
  // this site, but not the text/event-stream answers), so the Node process
  // spends no CPU on it.
  // 压缩交给反向代理（deploy/nginx.example.conf 为本站点开启 gzip，但不压缩 text/event-stream
  // 回答），Node 进程不再为此耗 CPU。
  compress: false,
  // The image copies the standalone output built on the host (scripts/build-web.sh),
  // so it must hold no native binaries: sharp's would be the host's platform, not
  // the image's. No page uses next/image, so image optimization is off and sharp
  // stays out of the trace.
  // 镜像直接拷贝在宿主机构建的 standalone 产物（scripts/build-web.sh），其中不能有原生
  // 二进制：sharp 的二进制属于宿主机平台，而不是镜像平台。没有页面使用 next/image，因此
  // 关闭图片优化，并把 sharp 排除在追踪之外。
  images: { unoptimized: true },
  outputFileTracingExcludes: {
    "*": ["**/node_modules/sharp/**", "**/node_modules/@img/**"],
  },
};

export default nextConfig;
