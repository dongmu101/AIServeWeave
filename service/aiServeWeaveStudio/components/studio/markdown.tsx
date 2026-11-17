"use client";

import * as React from "react";
import ReactMarkdown from "react-markdown";
import remarkGfm from "remark-gfm";

/**
 * Markdown renders an assistant answer. Raw HTML in the answer is not
 * rendered (react-markdown's default), and links open in a new tab without
 * an opener, so a reply cannot script or steer this page.
 *
 * Markdown 渲染助手的回答。回答中的原始 HTML 不渲染（react-markdown 的默认行为），链接在
 * 新标签页打开且不带 opener，因此回复无法在本页执行脚本或操控本页。
 */
export const Markdown = React.memo(function Markdown({ text }: { text: string }) {
  return (
    <div className="studio-markdown">
      <ReactMarkdown
        remarkPlugins={[remarkGfm]}
        components={{
          a: ({ href, children }) => (
            <a href={href} target="_blank" rel="noopener noreferrer nofollow">
              {children}
            </a>
          ),
        }}
      >
        {text}
      </ReactMarkdown>
    </div>
  );
});
