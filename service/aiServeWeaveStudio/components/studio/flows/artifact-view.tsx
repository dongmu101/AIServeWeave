"use client";

import * as React from "react";
import { DownloadIcon, FileIcon, FilmIcon, ImageOffIcon } from "lucide-react";
import { cn } from "cn";

import { artifactURL, downloadName, parseArtifactList, type ArtifactItem, type ArtifactShape } from "@/lib/studio/artifacts";

/**
 * How a run shows what image and ComfyUI nodes made: an image as a picture,
 * a list of artifacts as thumbnails and download links. The bytes come from
 * the BFF's artifact route, which serves them as attachments; an <img> still
 * renders them, and a link downloads them.
 *
 * 运行如何展示图像与 ComfyUI 节点的产出：图片显示为图，产物列表显示为缩略图与下载链接。
 * 字节来自 BFF 的产物路由，它以 attachment 提供；<img> 仍能渲染，链接则是下载。
 */

const buttonClass =
  "inline-flex h-7 shrink-0 items-center gap-1 rounded-md border px-2 text-xs text-muted-foreground hover:bg-muted hover:text-foreground";

function Thumbnail({ src, alt }: { src: string; alt: string }) {
  const [failed, setFailed] = React.useState(false);
  const [open, setOpen] = React.useState(false);
  if (failed) {
    return (
      <div className="flex items-center gap-2 rounded-lg bg-muted px-3 py-2 text-xs text-muted-foreground">
        <ImageOffIcon className="size-4 shrink-0" />
        图片无法加载，可能已被清理。
      </div>
    );
  }
  return (
    <button type="button" aria-label={open ? `收起 ${alt}` : `放大 ${alt}`} onClick={() => setOpen((value) => !value)}
      className="block max-w-full overflow-hidden rounded-lg border bg-muted">
      {/* eslint-disable-next-line @next/next/no-img-element -- bytes stream through the BFF route, not the image optimiser */}
      <img src={src} alt={alt} loading="lazy" onError={() => setFailed(true)} className={cn("block h-auto w-auto max-w-full object-contain", open ? "max-h-none" : "max-h-48")} />
    </button>
  );
}

function DownloadLink({ href, filename, label }: { href: string; filename: string; label: string }) {
  return (
    <a href={href} download={filename} aria-label={label} className={buttonClass}>
      <DownloadIcon className="size-3.5" />
      下载
    </a>
  );
}

function Row({ runId, item }: { runId: string; item: ArtifactItem }) {
  const href = artifactURL(runId, item.id);
  const name = downloadName(item);
  return (
    <li className="grid gap-2 rounded-lg border p-2">
      <div className="flex items-center gap-2">
        {item.kind === "video" ? <FilmIcon className="size-4 shrink-0 text-muted-foreground" /> : item.kind === "image" ? null : <FileIcon className="size-4 shrink-0 text-muted-foreground" />}
        <span className="min-w-0 flex-1 truncate text-xs" title={name}>
          {name}
        </span>
        {href ? <DownloadLink href={href} filename={name} label={`下载 ${name}`} /> : null}
      </div>
      {item.kind === "image" && href ? <Thumbnail src={href} alt={name} /> : null}
    </li>
  );
}

/** ArtifactValue shows the value of an artifact output: label, then the
 * picture or the list. A value that is not what the shape says is shown as
 * the text it is.
 *
 * ArtifactValue 展示产物输出的值：标签，然后是图片或列表。与形式不符的值按原文展示。 */
export function ArtifactValue({ runId, label, value, shape }: { runId: string; label: string; value: string; shape: ArtifactShape }) {
  const items = shape === "list" ? parseArtifactList(value) : null;
  const id = shape === "image" && value !== "" ? value.trim() : "";
  const href = id ? artifactURL(runId, id) : "";
  let body: React.ReactNode;
  if (shape === "image") {
    if (value === "") body = <Muted>没有生成图片。</Muted>;
    else if (!href) body = <Muted>{value}</Muted>;
    else body = <Thumbnail src={href} alt={label} />;
  } else if (items === null) {
    body = <Muted>{value || "没有产物。"}</Muted>;
  } else if (items.length === 0) {
    body = <Muted>没有产物。</Muted>;
  } else {
    body = (
      <ul className="grid gap-2" aria-label={`${label} 的产物`}>
        {items.map((item) => (
          <Row key={item.id} runId={runId} item={item} />
        ))}
      </ul>
    );
  }
  return (
    <div className="grid gap-1">
      <div className="flex items-center justify-between gap-2">
        <span className="font-mono text-[11px] text-muted-foreground">{label}</span>
        {href ? <DownloadLink href={href} filename={label} label={`下载 ${label}`} /> : null}
      </div>
      {body}
    </div>
  );
}

function Muted({ children }: { children: React.ReactNode }) {
  return <p className="break-all rounded-lg bg-muted px-3 py-2 text-xs text-muted-foreground">{children}</p>;
}
