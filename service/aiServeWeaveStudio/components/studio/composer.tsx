"use client";

import * as React from "react";
import { ArrowUpIcon, CheckIcon, SquareIcon, ChevronDownIcon, FileTextIcon, ImageIcon, PaperclipIcon, PlusIcon, XIcon } from "lucide-react";
import { toast } from "sonner";
import { cn } from "cn";

import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuGroup,
  DropdownMenuItem,
  DropdownMenuLabel,
  DropdownMenuShortcut,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";
import { ACCEPT, admit, checkAttachment, displayName, formatSize } from "@/lib/studio/attachments";
import type { StudioModel } from "@/lib/studio/contract";
import { chooseModel, MODEL_PREFERENCE_KEY } from "@/lib/studio/model-preference";
import { MAX_MESSAGE_CHARS } from "@/lib/studio/send-request";
import { useStoredValue } from "@/components/studio/use-stored-value";

interface Attachment {
  id: string;
  file: File;
  kind: "image" | "document";
}

const MAX_INPUT_HEIGHT = 240;

/** ComposerHandle lets the page put a refused message back for editing, and
 * read the chosen model.
 * ComposerHandle 让页面把被拒绝的消息放回输入框以便修改，并读取所选模型。 */
export interface ComposerHandle {
  restore(text: string): void;
  /** model is the model the picker shows. / model 是选择器当前显示的模型。 */
  model(): string;
}

export interface ComposerProps {
  models: StudioModel[];
  modelsError: string;
  /** onSubmit sends; returning true clears the input. / onSubmit 执行发送；返回 true 时清空输入框。 */
  onSubmit: (text: string, model: string) => boolean | Promise<boolean>;
  /** The conversation's own model, preferred over the stored choice. / 对话自身的模型，优先于已存的选择。 */
  conversationModel?: string;
  /** While a reply streams, the send button becomes stop. / 回复流式进行时，发送按钮变为停止。 */
  streaming?: boolean;
  onStop?: () => void;
  placeholder?: string;
  /** Where the model menu opens; "top" when the composer sits at the bottom of the screen. / 模型菜单的弹出方向；输入框位于屏幕底部时用 "top"。 */
  menuSide?: "top" | "bottom";
  ref?: React.Ref<ComposerHandle>;
}

/**
 * Composer is the message input: text, attachments, the "+" menu and the
 * model picker. Attachments are checked and listed, but not sent yet; a send
 * with attachments says so instead of dropping them silently.
 *
 * Composer 是消息输入区：文本、附件、「+」菜单与模型选择器。附件会被校验并列出，但暂不
 * 发送；带附件发送时会如实提示，而不是悄悄丢弃。
 */
export function Composer({ models, modelsError, onSubmit, conversationModel, streaming = false, onStop, placeholder = "今天我能为您做些什么？", menuSide = "bottom", ref }: ComposerProps) {
  const [text, setText] = React.useState("");
  const [attachments, setAttachments] = React.useState<Attachment[]>([]);
  const [stored, storeModel] = useStoredValue(MODEL_PREFERENCE_KEY);
  const [picked, setPicked] = React.useState<string | null>(null);
  const [sending, setSending] = React.useState(false);
  const model = chooseModel(models, picked, conversationModel, stored);

  const [dragging, setDragging] = React.useState(false);
  const inputRef = React.useRef<HTMLTextAreaElement>(null);
  const fileRef = React.useRef<HTMLInputElement>(null);
  const composing = React.useRef(false);

  React.useImperativeHandle(ref, () => ({
    restore(value: string) {
      setText((current) => (current.trim() === "" ? value : current));
      inputRef.current?.focus();
    },
    model: () => model,
  }), [model]);

  React.useEffect(() => {
    inputRef.current?.focus();
  }, []);

  React.useEffect(() => {
    function onKey(event: KeyboardEvent) {
      if ((event.metaKey || event.ctrlKey) && !event.shiftKey && !event.altKey && event.key.toLowerCase() === "u") {
        event.preventDefault();
        fileRef.current?.click();
      }
    }
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, []);

  React.useLayoutEffect(() => {
    const input = inputRef.current;
    if (!input) return;
    input.style.height = "auto";
    input.style.height = `${Math.min(input.scrollHeight, MAX_INPUT_HEIGHT)}px`;
  }, [text]);

  function addFiles(files: File[]) {
    if (files.length === 0) return;
    const { accepted, rejected } = admit(attachments.length, files);
    for (const reason of rejected) toast.error(reason);
    if (accepted.length === 0) return;
    setAttachments((current) => [
      ...current,
      ...accepted.map((file) => {
        const verdict = checkAttachment(file);
        return { id: crypto.randomUUID(), file, kind: verdict.ok ? verdict.kind : "document" } as Attachment;
      }),
    ]);
  }

  const selected = models.find((candidate) => candidate.id === model);
  const canSend = text.trim() !== "" && model !== "" && !streaming && !sending;

  async function send() {
    if (!canSend) return;
    if (attachments.length > 0) {
      toast.info("附件发送即将支持，请先移除附件后发送文字消息。");
      return;
    }
    if (text.length > MAX_MESSAGE_CHARS) {
      toast.error(`消息太长，请缩短到 ${MAX_MESSAGE_CHARS.toLocaleString()} 字以内。`);
      return;
    }
    setSending(true);
    try {
      if (await onSubmit(text, model)) setText("");
    } finally {
      setSending(false);
    }
  }

  function selectModel(id: string) {
    setPicked(id);
    storeModel(id);
  }

  return (
    <div className="w-full">
      <div
        onDragOver={(event) => {
          if (event.dataTransfer.types.includes("Files")) {
            event.preventDefault();
            setDragging(true);
          }
        }}
        onDragLeave={() => setDragging(false)}
        onDrop={(event) => {
          event.preventDefault();
          setDragging(false);
          addFiles(Array.from(event.dataTransfer.files));
        }}
        className={cn(
          "rounded-3xl border bg-card shadow-sm transition-colors focus-within:border-foreground/25",
          dragging && "border-brand ring-2 ring-brand/30"
        )}
      >
        {attachments.length > 0 ? (
          <ul className="flex flex-wrap gap-2 px-4 pt-4" aria-label="附件">
            {attachments.map((attachment) => (
              <li key={attachment.id} className="flex max-w-60 items-center gap-2 rounded-xl border bg-muted/50 py-1.5 pl-2 pr-1 text-xs">
                {attachment.kind === "image" ? <ImageIcon className="size-4 shrink-0 text-muted-foreground" /> : <FileTextIcon className="size-4 shrink-0 text-muted-foreground" />}
                <span className="min-w-0 flex-1">
                  <span className="block truncate font-medium">{displayName(attachment.file.name)}</span>
                  <span className="block text-muted-foreground">{formatSize(attachment.file.size)}</span>
                </span>
                <button type="button" aria-label={`移除 ${attachment.file.name}`}
                  onClick={() => setAttachments((current) => current.filter((item) => item.id !== attachment.id))}
                  className="inline-flex size-6 items-center justify-center rounded-md text-muted-foreground hover:bg-muted hover:text-foreground">
                  <XIcon className="size-3.5" />
                </button>
              </li>
            ))}
          </ul>
        ) : null}

        <textarea
          ref={inputRef}
          value={text}
          rows={2}
          placeholder={placeholder}
          aria-label="输入消息"
          onChange={(event) => setText(event.target.value)}
          onCompositionStart={() => (composing.current = true)}
          onCompositionEnd={() => (composing.current = false)}
          onPaste={(event) => {
            const files = Array.from(event.clipboardData.files);
            if (files.length > 0) {
              event.preventDefault();
              addFiles(files);
            }
          }}
          onKeyDown={(event) => {
            if (event.key === "Enter" && !event.shiftKey && !composing.current && !event.nativeEvent.isComposing) {
              event.preventDefault();
              void send();
            }
          }}
          className="block w-full resize-none bg-transparent px-5 pt-4 text-base outline-none placeholder:text-muted-foreground/80"
        />

        <div className="flex items-center gap-2 px-3 pb-3 pt-1">
          <DropdownMenu>
            <DropdownMenuTrigger
              render={
                <button type="button" aria-label="添加内容"
                  className="inline-flex size-9 items-center justify-center rounded-xl text-muted-foreground hover:bg-muted hover:text-foreground data-popup-open:bg-muted data-popup-open:text-foreground" />
              }
            >
              <PlusIcon className="size-5" />
            </DropdownMenuTrigger>
            <DropdownMenuContent side="bottom" align="start" sideOffset={8} className="w-64 rounded-xl p-1.5">
              <DropdownMenuItem onClick={() => fileRef.current?.click()} className="h-9 gap-3 px-2.5">
                <PaperclipIcon />
                上传或添加文件
                <DropdownMenuShortcut>⌘U</DropdownMenuShortcut>
              </DropdownMenuItem>
            </DropdownMenuContent>
          </DropdownMenu>
          <input ref={fileRef} type="file" multiple accept={ACCEPT} className="hidden"
            onChange={(event) => {
              addFiles(Array.from(event.target.files ?? []));
              event.target.value = "";
            }} />

          <div className="flex-1" />

          {streaming ? (
            <button type="button" onClick={onStop} aria-label="停止生成"
              className="inline-flex size-9 items-center justify-center rounded-xl bg-foreground text-background">
              <SquareIcon className="size-3.5 fill-current" />
            </button>
          ) : (
            <button type="button" onClick={() => void send()} disabled={!canSend} aria-label="发送"
              className="inline-flex size-9 items-center justify-center rounded-xl bg-foreground text-background transition-opacity disabled:opacity-25">
              <ArrowUpIcon className="size-5" />
            </button>
          )}
        </div>
      </div>

      <div className="mt-3 flex items-center justify-end px-3 text-sm">
        {modelsError ? (
          <span className="text-destructive">模型列表加载失败：{modelsError}</span>
        ) : models.length === 0 ? (
          <span className="text-muted-foreground">当前没有可用的模型</span>
        ) : (
          <DropdownMenu>
            <DropdownMenuTrigger
              render={
                <button type="button" className="inline-flex max-w-72 items-center gap-1.5 rounded-lg px-2 py-1 font-medium hover:bg-muted data-popup-open:bg-muted" />
              }
            >
              <span className="truncate">{selected?.name ?? "选择模型"}</span>
              <ChevronDownIcon className="size-4 shrink-0 text-muted-foreground" />
            </DropdownMenuTrigger>
            <DropdownMenuContent side={menuSide} align="end" sideOffset={6} className="max-h-80 w-72 rounded-xl p-1.5">
              <DropdownMenuGroup>
              <DropdownMenuLabel>模型</DropdownMenuLabel>
              {models.map((candidate) => (
                <DropdownMenuItem key={candidate.id} onClick={() => selectModel(candidate.id)} className="min-h-9 gap-2 px-2.5 py-1.5">
                  <span className="min-w-0 flex-1">
                    <span className="block truncate">{candidate.name}</span>
                    {candidate.name !== candidate.id ? <span className="block truncate text-xs text-muted-foreground">{candidate.id}</span> : null}
                  </span>
                  {candidate.id === model ? <CheckIcon className="text-brand" /> : null}
                </DropdownMenuItem>
              ))}
              </DropdownMenuGroup>
            </DropdownMenuContent>
          </DropdownMenu>
        )}
      </div>
    </div>
  );
}
