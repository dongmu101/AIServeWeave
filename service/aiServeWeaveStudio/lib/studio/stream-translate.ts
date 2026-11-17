import type { UIMessageChunk } from "ai";

import { errorMessage } from "./contract.ts";
import type { MessageStatus, StudioMetadata } from "./conversations.ts";
import type { GatewayFrame } from "./sse.ts";

/**
 * The translation from the Gateway's own Studio frames to AI SDK UI message
 * chunks. The Gateway protocol stays independent of the SDK; this is the only
 * place that knows both.
 *
 *   message_start   → start (message id, model, conversation title)
 *   thinking_delta  → reasoning-start / reasoning-delta
 *   text_delta      → text-start / text-delta (closing any open reasoning)
 *   usage           → kept for the finish metadata
 *   error           → error, with Studio's fixed wording for the code
 *   message_stop    → finish (status and usage as metadata)
 *
 * A stream that ends without message_stop was cut between the Gateway and
 * here; it ends with an error so the UI does not wait for more.
 *
 * Gateway 自有 Studio 帧到 AI SDK UI message chunk 的转换。Gateway 协议与 SDK 保持
 * 无关；只有这里同时了解两者。没有 message_stop 就结束的流是在 Gateway 与这里之间被
 * 截断的，以 error 结束，界面不会一直等待。
 */

type Chunk = UIMessageChunk<StudioMetadata>;

export interface Translator {
  frame(frame: GatewayFrame): Chunk[];
  end(): Chunk[];
}

const FINISH_REASON: Record<MessageStatus, "stop" | "interrupted" | "error"> = {
  complete: "stop",
  incomplete: "interrupted",
  error: "error",
};

function text(data: Record<string, unknown>, key: string): string {
  const value = data[key];
  return typeof value === "string" ? value : "";
}

function count(data: Record<string, unknown>, key: string): number {
  const value = data[key];
  return typeof value === "number" && Number.isFinite(value) && value >= 0 ? value : 0;
}

export function createTranslator(): Translator {
  let parts = 0;
  let reasoning: string | null = null;
  let answer: string | null = null;
  let usage: StudioMetadata["usage"];
  let started = false;
  let finished = false;

  function closeReasoning(out: Chunk[]) {
    if (reasoning !== null) {
      out.push({ type: "reasoning-end", id: reasoning });
      reasoning = null;
    }
  }

  function closeAll(out: Chunk[]) {
    closeReasoning(out);
    if (answer !== null) {
      out.push({ type: "text-end", id: answer });
      answer = null;
    }
  }

  return {
    frame({ event, data }) {
      if (finished) return [];
      const out: Chunk[] = [];
      switch (event) {
        case "message_start": {
          if (started) break;
          started = true;
          const metadata: StudioMetadata = {};
          const model = text(data, "model");
          if (model) metadata.model = model;
          const conversation = data.conversation;
          if (typeof conversation === "object" && conversation !== null) {
            const title = text(conversation as Record<string, unknown>, "title");
            if (title) metadata.title = title;
          }
          const messageId = text(data, "message_id");
          out.push(messageId ? { type: "start", messageId, messageMetadata: metadata } : { type: "start", messageMetadata: metadata });
          break;
        }
        case "thinking_delta": {
          const delta = text(data, "delta");
          if (!delta) break;
          if (reasoning === null) {
            reasoning = `reasoning-${++parts}`;
            out.push({ type: "reasoning-start", id: reasoning });
          }
          out.push({ type: "reasoning-delta", id: reasoning, delta });
          break;
        }
        case "text_delta": {
          const delta = text(data, "delta");
          if (!delta) break;
          closeReasoning(out);
          if (answer === null) {
            answer = `text-${++parts}`;
            out.push({ type: "text-start", id: answer });
          }
          out.push({ type: "text-delta", id: answer, delta });
          break;
        }
        case "usage":
          usage = { promptTokens: count(data, "prompt_tokens"), completionTokens: count(data, "completion_tokens") };
          break;
        case "error":
          closeAll(out);
          out.push({ type: "error", errorText: errorMessage(text(data, "code")) });
          finished = true;
          break;
        case "message_stop": {
          closeAll(out);
          const raw = text(data, "status");
          const status: MessageStatus = raw === "complete" || raw === "incomplete" ? raw : "error";
          const metadata: StudioMetadata = { status };
          if (usage) metadata.usage = usage;
          const code = text(data, "error_code");
          if (code) metadata.errorCode = code;
          out.push({ type: "finish", finishReason: FINISH_REASON[status], messageMetadata: metadata });
          finished = true;
          break;
        }
      }
      return out;
    },
    end() {
      if (finished) return [];
      finished = true;
      const out: Chunk[] = [];
      closeAll(out);
      out.push({ type: "error", errorText: errorMessage("stream_interrupted") });
      return out;
    },
  };
}
