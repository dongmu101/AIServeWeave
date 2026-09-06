"use client";

import type * as React from "react";
import type {
  ReactTable,
  RowData,
  TableFeatures,
  TableState,
} from "@tanstack/react-table";

import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";

/**
 * DataTable renders a TanStack table instance with the Console's markup.
 *
 * It takes the instance rather than columns and data on purpose: in v9 the
 * registered features are part of a table's type, so a wrapper that built the
 * instance would have to fix one feature set for every table in the Console —
 * and the audit view needs sorting and virtualization that the user list does
 * not. Each view declares its own features and columns; this component owns
 * only the markup, the empty row, and the fact that a table scrolls inside its
 * own container rather than widening the page.
 *
 * DataTable 用 Console 的标记渲染一个 TanStack 表格实例。
 *
 * 它刻意接收实例而不是 columns 与 data：在 v9 中，已注册的特性属于表格类型的一部分，
 * 因此若由包装组件来构造实例，就得为 Console 里所有表格固定同一套特性集——而审计视图
 * 需要的排序与虚拟滚动，用户列表并不需要。各视图声明自己的特性与列；本组件只负责标记、
 * 空行，以及「表格在自己的容器内横向滚动而不是把页面撑宽」这件事。
 */
export function DataTable<
  TFeatures extends TableFeatures,
  TData extends RowData,
  TSelected = TableState<TFeatures>,
>({
  table,
  emptyMessage = "没有记录",
  caption,
}: {
  table: ReactTable<TFeatures, TData, TSelected>;
  /** emptyMessage is shown when a successful read returned no rows. A failed
   * read must use ErrorState instead — see components/console/states.
   *
   * emptyMessage 在一次成功的读取没有返回任何行时展示。读取失败必须改用 ErrorState
   * —— 见 components/console/states。 */
  emptyMessage?: React.ReactNode;
  caption?: string;
}) {
  const rows = table.getRowModel().rows;
  const columnCount = table.getAllLeafColumns().length;

  return (
    <div className="rounded-xl border">
      <Table>
        {caption ? <caption className="sr-only">{caption}</caption> : null}
        <TableHeader>
          {table.getHeaderGroups().map((group) => (
            <TableRow key={group.id}>
              {group.headers.map((header) => (
                <TableHead key={header.id}>
                  {header.isPlaceholder ? null : (
                    <table.FlexRender header={header} />
                  )}
                </TableHead>
              ))}
            </TableRow>
          ))}
        </TableHeader>
        <TableBody>
          {rows.length === 0 ? (
            <TableRow>
              <TableCell
                colSpan={columnCount}
                className="py-8 text-center text-muted-foreground"
              >
                {emptyMessage}
              </TableCell>
            </TableRow>
          ) : (
            rows.map((row) => (
              <TableRow key={row.id}>
                {row.getAllCells().map((cell) => (
                  <TableCell key={cell.id}>
                    <table.FlexRender cell={cell} />
                  </TableCell>
                ))}
              </TableRow>
            ))
          )}
        </TableBody>
      </Table>
    </div>
  );
}
