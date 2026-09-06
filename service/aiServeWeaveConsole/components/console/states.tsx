"use client";

import type * as React from "react";

import { Button } from "@/components/ui/button";
import { Skeleton } from "@/components/ui/skeleton";

/**
 * These are the four things a Console view can be showing: loading, empty,
 * failed, or a form that rejected an input. They are one module because the
 * distinction between them is the point.
 *
 * A read that failed must never be drawn as "no records": the two look alike
 * in a table and mean opposite things — one says the tenant has nothing, the
 * other says the Console does not know. A page that blurs them will one day
 * reassure somebody that they have no API keys while the control plane is
 * simply unreachable.
 *
 * 这四种就是一个 Console 视图可能呈现的状态：加载中、空、失败，以及一个拒绝了输入的
 * 表单。它们放在同一个模块里，因为它们之间的区别正是要点。
 *
 * 读取失败绝不能画成「没有记录」：这两者在表格里长得一样，含义却相反——一个说这个租户
 * 什么都没有，另一个说 Console 并不知道。混淆它们的页面，总有一天会在控制面根本连不上的
 * 时候，安慰某个人说他没有 API Key。
 */

/**
 * LoadingState marks a region as loading, without pretending to know how much
 * will arrive.
 *
 * LoadingState 把一块区域标记为加载中，而不假装知道最终会到达多少内容。
 */
export function LoadingState({
  label = "正在加载",
  rows = 3,
}: {
  label?: string;
  rows?: number;
}) {
  return (
    <div
      className="flex flex-col gap-2 py-2"
      role="status"
      aria-live="polite"
      aria-busy="true"
    >
      <span className="sr-only">{label}</span>
      {Array.from({ length: rows }, (_, index) => (
        <Skeleton key={index} className="h-8 w-full" />
      ))}
    </div>
  );
}

/**
 * EmptyState says a query succeeded and returned nothing.
 *
 * EmptyState 表示一次查询成功了，且没有返回任何内容。
 */
export function EmptyState({
  title,
  description,
  action,
}: {
  title: string;
  description?: string;
  action?: React.ReactNode;
}) {
  return (
    <div className="flex flex-col items-center gap-2 rounded-xl border border-dashed px-6 py-10 text-center">
      <p className="text-sm font-medium">{title}</p>
      {description ? (
        <p className="max-w-md text-sm text-muted-foreground">{description}</p>
      ) : null}
      {action}
    </div>
  );
}

/**
 * ErrorState says a read failed, and offers the only useful next step.
 *
 * ErrorState 表示一次读取失败了，并给出唯一有用的下一步。
 */
export function ErrorState({
  message,
  onRetry,
}: {
  message: string;
  onRetry?: () => void;
}) {
  return (
    <div
      className="flex flex-col items-center gap-3 rounded-xl border border-destructive/30 bg-destructive/5 px-6 py-8 text-center"
      role="alert"
    >
      <p className="text-sm text-destructive">{message}</p>
      {onRetry ? (
        <Button variant="outline" size="sm" onClick={onRetry}>
          重试
        </Button>
      ) : null}
    </div>
  );
}

/**
 * FormError reports why a submission was refused. It is announced rather than
 * merely colored, so the reason reaches somebody who is not looking at it.
 *
 * FormError 说明一次提交为何被拒绝。它会被朗读而不只是被着色，好让原因也能传达给
 * 并未在看着它的人。
 */
export function FormError({ message }: { message: string | null }) {
  if (!message) {
    return null;
  }
  return (
    <p className="text-sm text-destructive" role="alert">
      {message}
    </p>
  );
}
