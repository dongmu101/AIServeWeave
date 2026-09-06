"use client";

import * as React from "react";

import { Button } from "@/components/ui/button";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { FormError } from "@/components/console/states";
import { SubmitButton } from "@/components/console/submit-button";

/**
 * ConfirmDialog is the shared confirmation for an action that cannot be
 * undone. It names the target rather than asking "are you sure": revoking a
 * key is irreversible, and a dialog that does not say which key is being
 * revoked pushes that check onto the person's memory of what they clicked.
 *
 * The failure is shown inside the dialog and the dialog stays open, so a
 * refused action does not look like a completed one.
 *
 * ConfirmDialog 是无法撤销的操作所共用的确认框。它点出操作对象，而不是只问「确定吗」：
 * 吊销一个 key 不可逆，而一个不说清正在吊销哪个 key 的对话框，等于把这道核对推给了
 * 当事人对自己刚才点了什么的记忆。
 *
 * 失败信息显示在对话框内部，且对话框保持打开，这样被拒绝的操作不会看起来像已完成的操作。
 */
export function ConfirmDialog({
  open,
  onOpenChange,
  title,
  description,
  confirmLabel = "确认",
  destructive = false,
  onConfirm,
}: {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  title: string;
  description: React.ReactNode;
  confirmLabel?: string;
  destructive?: boolean;
  /** onConfirm runs the action and resolves when it is settled. Rejecting
   * keeps the dialog open and shows the message.
   *
   * onConfirm 执行该操作，并在其尘埃落定时 resolve。抛出错误则保持对话框打开并展示
   * 相应文案。 */
  onConfirm: () => Promise<void>;
}) {
  const [pending, setPending] = React.useState(false);
  const [error, setError] = React.useState<string | null>(null);

  // Reset during render rather than in an effect: the failure of a previous
  // attempt must be gone the first time the reopened dialog paints, not one
  // render later, or it briefly accuses the new attempt of the old failure.
  //
  // 在渲染期间重置而不是放进 effect：上一次尝试的失败信息，必须在对话框重新打开后
  // 首次绘制时就已消失，而不是晚一次渲染，否则它会短暂地把旧的失败扣在新的尝试头上。
  const [wasOpen, setWasOpen] = React.useState(open);
  if (open !== wasOpen) {
    setWasOpen(open);
    setError(null);
  }

  async function confirm(event: React.FormEvent) {
    event.preventDefault();
    setPending(true);
    setError(null);
    try {
      await onConfirm();
      onOpenChange(false);
    } catch (failure) {
      setError(
        failure instanceof Error ? failure.message : "操作失败，请稍后重试。"
      );
    } finally {
      setPending(false);
    }
  }

  return (
    <Dialog
      open={open}
      onOpenChange={(next) => {
        // A request in flight has an outcome the person needs to see; closing
        // now would hide it without stopping it.
        //
        // 在途的请求有一个当事人需要看到的结果；此刻关闭只会藏起它，并不能让它停下。
        if (!pending) {
          onOpenChange(next);
        }
      }}
    >
      <DialogContent>
        <form onSubmit={confirm} className="grid gap-4">
          <DialogHeader>
            <DialogTitle>{title}</DialogTitle>
            <DialogDescription>{description}</DialogDescription>
          </DialogHeader>
          <FormError message={error} />
          <DialogFooter>
            <Button
              type="button"
              variant="outline"
              disabled={pending}
              onClick={() => onOpenChange(false)}
            >
              取消
            </Button>
            <SubmitButton
              pending={pending}
              variant={destructive ? "destructive" : "default"}
            >
              {confirmLabel}
            </SubmitButton>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
}
