import type { MessageKey } from "./messages.ts";
import type { MessageValues } from "./translate.ts";

/** MessageError carries a fixed UI reason and named values, never upstream text.
 * MessageError 保存固定的界面原因及具名参数，不携带上游文本。 */
export class MessageError extends Error {
  readonly key: MessageKey;
  readonly values: MessageValues;
  readonly failure: unknown;

  constructor(key: MessageKey, values: MessageValues = {}, failure?: unknown) {
    super("A local display operation failed");
    this.name = "MessageError";
    this.key = key;
    this.values = values;
    this.failure = failure;
  }
}
