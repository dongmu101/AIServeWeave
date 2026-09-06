import type { NextConfig } from "next";

const nextConfig: NextConfig = {
  // Emit the minimal Node.js server used by the container image.
  // 输出容器镜像使用的最小 Node.js 服务端。
  output: "standalone",
};

export default nextConfig;
