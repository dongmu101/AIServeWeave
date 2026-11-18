"use client";

import * as React from "react";
import { useRouter } from "next/navigation";
import { toast } from "sonner";

import { Composer } from "@/components/studio/composer";
import { holdFirstMessage } from "@/components/studio/first-message";
import { sendToSignIn } from "@/components/studio/session-expired";
import { errorMessage, type StudioModel } from "@/lib/studio/contract";
import { parseConversation } from "@/lib/studio/conversations";

/**
 * NewConversation is the home page composer: the first send creates the
 * conversation, hands the message to its page, and goes there.
 *
 * NewConversation 是首页的输入框：首次发送时创建对话，把消息交给对话页，然后跳转过去。
 */
export function NewConversation({ models, modelsError }: { models: StudioModel[]; modelsError: string }) {
  const router = useRouter();

  async function start(text: string, model: string): Promise<boolean> {
    let response: Response;
    try {
      response = await fetch("/api/studio/conversations", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ model }),
      });
    } catch {
      toast.error("网络连接失败，请稍后重试。");
      return false;
    }
    if (response.status === 401) {
      sendToSignIn("/");
      return false;
    }
    if (!response.ok) {
      const body = (await response.json().catch(() => ({}))) as { error?: string };
      toast.error(errorMessage(body.error ?? ""));
      return false;
    }
    let id: string;
    try {
      id = parseConversation(await response.json()).id;
    } catch {
      toast.error(errorMessage("upstream_contract"));
      return false;
    }
    holdFirstMessage(id, text, model);
    router.push(`/c/${id}`);
    return true;
  }

  return <Composer models={models} modelsError={modelsError} onSubmit={start} />;
}
