"use client";

import * as React from "react";
import { useRouter } from "next/navigation";
import { useChat } from "@ai-sdk/react";
import { DefaultChatTransport } from "ai";
import { BrainIcon, CheckIcon, ChevronRightIcon, CopyIcon, RotateCcwIcon, TriangleAlertIcon } from "lucide-react";
import { toast } from "sonner";
import { cn } from "cn";

import { BrandMark } from "@/components/studio/brand";
import { Composer, type ComposerHandle } from "@/components/studio/composer";
import { takeFirstMessage } from "@/components/studio/first-message";
import { Markdown } from "@/components/studio/markdown";
import { sendToSignIn } from "@/components/studio/session-expired";
import { errorMessage, type StudioModel } from "@/lib/studio/contract";
import { messageText, type Conversation, type StudioUIMessage } from "@/lib/studio/conversations";
import { buildSendBody } from "@/lib/studio/send-request";

/**
 * ChatView is one conversation: the messages, streaming replies through the
 * AI SDK's useChat, and the composer. The server is the source of truth; the
 * messages here are the stored history plus what this tab is streaming.
 *
 * ChatView 是一个对话：消息列表、经 AI SDK 的 useChat 流式接收的回复，以及输入框。
 * 服务端是唯一事实来源；这里的消息是已存储的历史加上本标签页正在接收的内容。
 */
export function ChatView({
  conversation,
  initialMessages,
  models,
  modelsError,
}: {
  conversation: Conversation;
  initialMessages: StudioUIMessage[];
  models: StudioModel[];
  modelsError: string;
}) {
  const router = useRouter();
  const composer = React.useRef<ComposerHandle>(null);
  const [title, setTitle] = React.useState(conversation.title);
  const [stopped, setStopped] = React.useState<ReadonlySet<string>>(new Set());
  // A refusal before the turn was stored: retrying would regenerate the
  // previous answer instead, so no retry is offered.
  // 轮次写入前的拒绝：此时重试会重新生成上一条回答，因此不提供重试。
  const [refused, setRefused] = React.useState(false);
  const latest = React.useRef<StudioUIMessage[]>(initialMessages);
  const returnTo = `/c/${conversation.id}`;

  const transport = React.useMemo(
    () =>
      new DefaultChatTransport<StudioUIMessage>({
        api: `/api/studio/conversations/${conversation.id}/stream`,
        fetch: async (input, init) => {
          const response = await fetch(input, init);
          if (response.status === 401) sendToSignIn(returnTo);
          return response;
        },
        // The model rides in each call's body: the one chosen for that send.
        // 模型随每次调用的 body 传入：即这次发送所选的模型。
        prepareSendMessagesRequest: ({ messages, trigger, body }) => ({
          body: buildSendBody({ trigger, messages, model: typeof body?.model === "string" ? body.model : "" }),
        }),
      }),
    [conversation.id, returnTo]
  );

  const { messages, setMessages, sendMessage, regenerate, stop, status, error, clearError } = useChat<StudioUIMessage>({
    id: conversation.id,
    messages: initialMessages,
    transport,
    onFinish: ({ message }) => {
      if (message.metadata?.title) setTitle(message.metadata.title);
      router.refresh();
    },
    onError: () => {
      // A send refused before it was stored leaves only the optimistic user
      // message; put its text back in the composer instead of showing a
      // question the server never saw.
      // 在写入前就被拒绝的发送只留下乐观添加的用户消息；把文字放回输入框，而不是展示一条
      // 服务端从未收到的提问。
      const last = latest.current.at(-1);
      const beforeStored = last?.role === "user";
      setRefused(beforeStored);
      if (last && beforeStored) {
        composer.current?.restore(messageText(last));
        setMessages((current) => (current.at(-1)?.id === last.id ? current.slice(0, -1) : current));
      }
    },
  });

  const streaming = status === "submitted" || status === "streaming";

  React.useEffect(() => {
    latest.current = messages;
  }, [messages]);

  React.useEffect(() => {
    const first = takeFirstMessage(conversation.id);
    if (first) {
      void sendMessage({ text: first.text }, { body: { model: first.model } });
    }
  }, [conversation.id, sendMessage]);

  const scroller = useStickToBottom(messages);

  function submit(text: string, chosen: string): boolean {
    if (streaming) return false;
    clearError();
    void sendMessage({ text }, { body: { model: chosen } });
    return true;
  }

  function stopReply() {
    const last = messages.at(-1);
    if (last?.role === "assistant") setStopped((current) => new Set(current).add(last.id));
    void stop();
  }

  function retry() {
    clearError();
    // Regenerate with the model the picker shows now.
    // 使用选择器当前显示的模型重新生成。
    void regenerate({ body: { model: composer.current?.model() ?? conversation.model } });
  }

  const lastAssistant = messages.findLast((message) => message.role === "assistant");

  return (
    <div className="flex min-h-0 flex-1 flex-col">
      <header className="flex h-12 shrink-0 items-center gap-2 px-4 md:px-6">
        <h1 className="min-w-0 truncate text-sm font-medium">{title || "新对话"}</h1>
      </header>

      <div ref={scroller} className="min-h-0 flex-1 overflow-y-auto">
        <div className="mx-auto flex w-full max-w-3xl flex-col gap-6 px-4 pb-8 pt-2 sm:px-6">
          {messages.map((message) =>
            message.role === "user" ? (
              <UserMessage key={message.id} message={message} />
            ) : (
              <AssistantMessage
                key={message.id}
                message={message}
                streaming={streaming && message.id === messages.at(-1)?.id}
                interrupted={message.metadata?.status === "incomplete" || stopped.has(message.id)}
                canRegenerate={!streaming && message.id === lastAssistant?.id && messages.at(-1)?.id === message.id}
                onRegenerate={retry}
              />
            )
          )}
          {status === "submitted" && messages.at(-1)?.role === "user" ? <Thinking /> : null}
          {error ? (
            <div role="alert" className="flex items-start gap-2 rounded-xl border border-destructive/30 bg-destructive/5 px-4 py-3 text-sm">
              <TriangleAlertIcon className="mt-0.5 size-4 shrink-0 text-destructive" />
              <span className="flex-1">{error.message || errorMessage("")}</span>
              {!refused && messages.at(-1)?.role === "assistant" ? (
                <button type="button" onClick={retry} className="shrink-0 font-medium text-foreground underline-offset-2 hover:underline">
                  重试
                </button>
              ) : null}
            </div>
          ) : null}
        </div>
      </div>

      <div className="shrink-0 px-4 pb-4 sm:px-6">
        <div className="mx-auto w-full max-w-3xl">
          <Composer
            ref={composer}
            models={models}
            modelsError={modelsError}
            conversationModel={conversation.model}
            onSubmit={submit}
            streaming={streaming}
            onStop={stopReply}
            placeholder="继续提问…"
            menuSide="top"
          />
        </div>
      </div>
    </div>
  );
}

/** useStickToBottom keeps the list scrolled to the newest content while the
 * reader is at the bottom, and leaves them alone once they scroll up.
 *
 * useStickToBottom 在读者位于底部时让列表跟随最新内容滚动；读者向上滚动后不再打扰。 */
function useStickToBottom(dependency: unknown) {
  const ref = React.useRef<HTMLDivElement>(null);
  const pinned = React.useRef(true);

  React.useEffect(() => {
    const element = ref.current;
    if (!element) return;
    const onScroll = () => {
      pinned.current = element.scrollHeight - element.scrollTop - element.clientHeight < 80;
    };
    element.addEventListener("scroll", onScroll, { passive: true });
    return () => element.removeEventListener("scroll", onScroll);
  }, []);

  React.useLayoutEffect(() => {
    const element = ref.current;
    if (element && pinned.current) element.scrollTop = element.scrollHeight;
  }, [dependency]);

  return ref;
}

function UserMessage({ message }: { message: StudioUIMessage }) {
  return (
    <div className="flex justify-end">
      <div className="max-w-[85%] whitespace-pre-wrap break-words rounded-2xl bg-muted px-4 py-2.5 text-[15px] leading-relaxed">
        {messageText(message)}
      </div>
    </div>
  );
}

function AssistantMessage({
  message,
  streaming,
  interrupted,
  canRegenerate,
  onRegenerate,
}: {
  message: StudioUIMessage;
  streaming: boolean;
  interrupted: boolean;
  canRegenerate: boolean;
  onRegenerate: () => void;
}) {
  const text = messageText(message);
  const reasoning = message.parts
    .filter((part) => part.type === "reasoning")
    .map((part) => part.text)
    .join("");
  const thinking = streaming && text === "";
  const failed = message.metadata?.status === "error";

  return (
    <div className="group flex gap-3">
      <BrandMark className="mt-1 size-6 shrink-0" />
      <div className="min-w-0 flex-1">
        {reasoning ? <Reasoning text={reasoning} active={thinking} /> : null}
        {text ? <Markdown text={text} /> : null}
        {!text && !reasoning && streaming ? <Dots /> : null}
        {failed && !text ? <p className="text-sm text-muted-foreground">{errorMessage(message.metadata?.errorCode ?? "upstream_error")}</p> : null}
        {!streaming ? (
          <div className="mt-2 flex items-center gap-1 text-muted-foreground">
            {interrupted ? <span className="mr-1 rounded bg-muted px-1.5 py-0.5 text-xs">已中断</span> : null}
            {text ? <CopyButton text={text} /> : null}
            {canRegenerate ? (
              <button type="button" onClick={onRegenerate} aria-label="重新生成" title="重新生成"
                className="inline-flex size-7 items-center justify-center rounded-md hover:bg-muted hover:text-foreground">
                <RotateCcwIcon className="size-3.5" />
              </button>
            ) : null}
          </div>
        ) : null}
      </div>
    </div>
  );
}

function Reasoning({ text, active }: { text: string; active: boolean }) {
  const [open, setOpen] = React.useState(false);
  return (
    <div className="mb-3">
      <button type="button" onClick={() => setOpen((value) => !value)} aria-expanded={open}
        className="inline-flex items-center gap-1.5 text-sm text-muted-foreground hover:text-foreground">
        <BrainIcon className={cn("size-4", active && "animate-pulse")} />
        {active ? "思考中…" : "思考过程"}
        <ChevronRightIcon className={cn("size-3.5 transition-transform", open && "rotate-90")} />
      </button>
      {open ? (
        <div className="mt-2 whitespace-pre-wrap border-l-2 pl-3 text-sm leading-relaxed text-muted-foreground">{text}</div>
      ) : null}
    </div>
  );
}

function CopyButton({ text }: { text: string }) {
  const [copied, setCopied] = React.useState(false);
  async function copy() {
    try {
      await navigator.clipboard.writeText(text);
      setCopied(true);
      setTimeout(() => setCopied(false), 1500);
    } catch {
      toast.error("复制失败。");
    }
  }
  return (
    <button type="button" onClick={copy} aria-label="复制" title="复制"
      className="inline-flex size-7 items-center justify-center rounded-md hover:bg-muted hover:text-foreground">
      {copied ? <CheckIcon className="size-3.5" /> : <CopyIcon className="size-3.5" />}
    </button>
  );
}

function Thinking() {
  return (
    <div className="flex gap-3">
      <BrandMark className="mt-1 size-6 shrink-0" />
      <Dots />
    </div>
  );
}

function Dots() {
  return (
    <span className="mt-2.5 inline-flex gap-1" aria-label="正在生成">
      {[0, 150, 300].map((delay) => (
        <span key={delay} className="size-1.5 animate-bounce rounded-full bg-muted-foreground/60" style={{ animationDelay: `${delay}ms` }} />
      ))}
    </span>
  );
}
