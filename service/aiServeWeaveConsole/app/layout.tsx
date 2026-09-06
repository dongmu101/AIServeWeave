import type { Metadata } from "next";
import { Geist, Geist_Mono } from "next/font/google";
import "./globals.css";

import { Toaster } from "@/components/ui/sonner";

const geistSans = Geist({
  variable: "--font-geist-sans",
  subsets: ["latin"],
});

const geistMono = Geist_Mono({
  variable: "--font-geist-mono",
  subsets: ["latin"],
});

/**
 * metadata names the product rather than the framework. `robots` is set
 * because every page behind this layout is an administrative view of one
 * tenant's data: there is nothing here for an index to hold.
 *
 * metadata 点出的是产品而不是框架。这里设置 `robots`，是因为本布局之下的每个页面都是
 * 对某一个租户数据的管理视图：没有任何内容值得被索引收录。
 */
export const metadata: Metadata = {
  title: "AIServeWeave 控制台",
  description: "AIServeWeave 租户管理控制台：用户、API Key 与管理审计。",
  robots: { index: false, follow: false },
};

export default function RootLayout({ children }: LayoutProps<"/">) {
  return (
    <html
      lang="zh-CN"
      className={`${geistSans.variable} ${geistMono.variable} h-full antialiased`}
    >
      <body className="flex min-h-full flex-col bg-background text-foreground">
        {children}
        <Toaster position="top-center" />
      </body>
    </html>
  );
}
