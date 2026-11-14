import { SendIcon } from "lucide-react";

import { buttonVariants } from "@/components/ui/button";
import { cn } from "@/lib/utils";

/** TELEGRAM_SUPPORT is the Telegram account that answers customer questions.
 *
 * TELEGRAM_SUPPORT 是回答客户问题的 Telegram 客服账号。 */
const TELEGRAM_SUPPORT = "xiuming142857";

/** SupportLink opens the Telegram support chat in a new tab. It takes the label
 * already translated so both server and client headers can render it.
 *
 * SupportLink 在新分页打开 Telegram 客服对话。它接收已翻译的文字，使服务端与客户端页首都能渲染。 */
export function SupportLink({ label }: { label: string }) {
  return (
    <a
      href={`https://t.me/${TELEGRAM_SUPPORT}`}
      target="_blank"
      rel="noopener noreferrer"
      aria-label={`${label} @${TELEGRAM_SUPPORT}`}
      title={`@${TELEGRAM_SUPPORT}`}
      className={cn(buttonVariants({ variant: "ghost", size: "sm" }), "shrink-0")}
    >
      <SendIcon aria-hidden="true" className="text-sky-500" />
      <span className="hidden sm:inline">{label}</span>
    </a>
  );
}
