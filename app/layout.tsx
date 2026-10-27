import type { Metadata } from "next";
import "./globals.css";

export const metadata: Metadata = {
  title: "剧栈 · CCTV 自动追剧",
  description: "搜索、订阅并自动下载央视网更新的剧集。",
};

export default function RootLayout({ children }: Readonly<{ children: React.ReactNode }>) {
  return (
    <html lang="zh-CN">
      <body>{children}</body>
    </html>
  );
}
