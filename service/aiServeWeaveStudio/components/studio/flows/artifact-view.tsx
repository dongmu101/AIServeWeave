"use client";

import * as React from "react";
import { DownloadIcon, FileIcon, FilmIcon, ImageOffIcon, VideoOffIcon } from "lucide-react";
import { toast } from "sonner";
import { cn } from "cn";

import { sendToSignIn } from "@/components/studio/session-expired";
import { artifactURL, downloadFailureText, downloadName, kindOfFilename, parseArtifactList, type ArtifactItem, type ArtifactShape } from "@/lib/studio/artifacts";

/**
 * How a run shows what image, video and ComfyUI nodes made: an image as a
 * picture, a video as a player, a list of artifacts as thumbnails and
 * download links. The bytes come from
 * the BFF's artifact route, which serves them as attachments; an <img> still
 * renders them, and a link downloads them.
 *
 * 运行如何展示图像、视频与 ComfyUI 节点的产出：图片显示为图，视频显示为播放器，产物列表显示为
 * 缩略图与下载链接。
 * 字节来自 BFF 的产物路由，它以 attachment 提供；<img> 仍能渲染，链接则是下载。
 *
 * The same shows the files a run was started with. Stored files are kept for a
 * limited time, so one can be gone: a picture says so where it would be, and
 * a download is first asked for its headers, so a missing file is a message
 * and not a saved error page.
 * 同样用来展示运行启动时收到的文件。存放的文件只保留一段时间，所以可能已经不在：图片在其
 * 位置上说明这一点；下载先请求响应头，因此缺失的文件是一条提示，而不是存下来的错误页。
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
        图片无法加载，可能已超过保留期被清理。
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

/** Player plays a video in place. The BFF serves it as an attachment, which
 * a <video> element still plays; preload="metadata" fetches only the start
 * until the user presses play.
 * Player 在原处播放视频。BFF 以 attachment 提供，<video> 元素仍可播放；
 * preload="metadata" 在用户点击播放之前只取开头部分。 */
function Player({ src, label }: { src: string; label: string }) {
  const [failed, setFailed] = React.useState(false);
  if (failed) {
    return (
      <div className="flex items-center gap-2 rounded-lg bg-muted px-3 py-2 text-xs text-muted-foreground">
        <VideoOffIcon className="size-4 shrink-0" />
        视频无法在这里播放，请下载后观看；也可能已超过保留期被清理。
      </div>
    );
  }
  return (
    <video src={src} controls preload="metadata" playsInline aria-label={label} onError={() => setFailed(true)}
      className="block max-h-72 w-full rounded-lg border bg-black" />
  );
}

/** download saves an artifact. It asks for the headers first and drops the
 * body, so a refusal (the file is gone, the session ended) is reported
 * instead of a JSON error being saved under the file's name.
 * download 保存一个产物。它先请求响应头并丢弃响应体，因此拒绝（文件已不存在、会话结束）会被
 * 报告出来，而不是把一份 JSON 错误以该文件名存下来。 */
async function download(href: string, filename: string): Promise<void> {
  const probe = new AbortController();
  try {
    const response = await fetch(href, { cache: "no-store", signal: probe.signal });
    probe.abort();
    if (response.status === 401) {
      sendToSignIn(window.location.pathname);
      return;
    }
    if (!response.ok) {
      toast.error(downloadFailureText(response.status));
      return;
    }
  } catch {
    toast.error(downloadFailureText(0));
    return;
  }
  const link = document.createElement("a");
  link.href = href;
  link.download = filename;
  document.body.append(link);
  link.click();
  link.remove();
}

function DownloadLink({ href, filename, label }: { href: string; filename: string; label: string }) {
  return (
    <button type="button" aria-label={label} className={buttonClass} onClick={() => void download(href, filename)}>
      <DownloadIcon className="size-3.5" />
      下载
    </button>
  );
}

/** ArtifactRow is one file: its name with a download, and a picture of it
 * when it is an image, a player when it is a video.
 * ArtifactRow 是一个文件：文件名加下载，是图片时再加图片预览，是视频时再加播放器。 */
export function ArtifactRow({ runId, item }: { runId: string; item: ArtifactItem }) {
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
      {item.kind === "video" && href ? <Player src={href} label={name} /> : null}
    </li>
  );
}

/** ArtifactValue shows the value of an artifact output: label, then the
 * picture or the list. A value that is not what the shape says is shown as
 * the text it is.
 *
 * ArtifactValue 展示产物输出的值：标签，然后是图片或列表。与形式不符的值按原文展示。 */
export function ArtifactValue({ runId, label, value, shape, names }: { runId: string; label: string; value: string; shape: ArtifactShape; names?: Record<string, string> }) {
  const items = shape === "list" ? parseArtifactList(value) : null;
  const id = shape !== "list" && value.trim() !== "" ? value.trim() : "";
  const href = id ? artifactURL(runId, id) : "";
  let body: React.ReactNode;
  if (shape === "file") {
    // A file input's value is its artifact id; the name is the run's own record.
    // 文件输入的值是其产物 id；文件名取自运行自己的记录。
    const filename = names?.[id] ?? "";
    if (id === "") body = <Muted>没有文件。</Muted>;
    else if (!href) body = <Muted>{value}</Muted>;
    else body = (
      <ul className="grid">
        <ArtifactRow runId={runId} item={{ id, filename, kind: kindOfFilename(filename) }} />
      </ul>
    );
  } else if (shape === "image") {
    if (value === "") body = <Muted>没有图片。</Muted>;
    else if (!href) body = <Muted>{value}</Muted>;
    else body = <Thumbnail src={href} alt={label} />;
  } else if (shape === "video") {
    if (value === "") body = <Muted>没有视频。</Muted>;
    else if (!href) body = <Muted>{value}</Muted>;
    else body = <Player src={href} label={label} />;
  } else if (items === null) {
    body = <Muted>{value || "没有产物。"}</Muted>;
  } else if (items.length === 0) {
    body = <Muted>没有产物。</Muted>;
  } else {
    body = (
      <ul className="grid gap-2" aria-label={`${label} 的产物`}>
        {items.map((item) => (
          <ArtifactRow key={item.id} runId={runId} item={item} />
        ))}
      </ul>
    );
  }
  return (
    <div className="grid gap-1">
      <div className="flex items-center justify-between gap-2">
        <span className="font-mono text-[11px] text-muted-foreground">{label}</span>
        {href && shape !== "file" ? <DownloadLink href={href} filename={names?.[id] || label} label={`下载 ${label}`} /> : null}
      </div>
      {body}
    </div>
  );
}

function Muted({ children }: { children: React.ReactNode }) {
  return <p className="break-all rounded-lg bg-muted px-3 py-2 text-xs text-muted-foreground">{children}</p>;
}
