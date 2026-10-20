import type { MessageKey } from "./messages.ts";
import type { MessageValues } from "./translate.ts";
import { ApiError } from "../console/errors.ts";

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

/** draftWriteProblem retains a safe failure and an unchanged-draft notice.
 * draftWriteProblem 保留安全的错误原因和草稿未丢失的提示。 */
export function draftWriteProblem(error: unknown): MessageError {
  return error instanceof ApiError && error.kind === "conflict"
    ? new MessageError("common.theCurrentVersionHasChangedYourDraftHas")
    : new MessageError("common.yourDraftHasBeenKeptRefreshTheCurrent", {}, error);
}
