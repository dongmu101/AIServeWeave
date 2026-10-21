"use client";

import { Message } from "@/components/i18n/message";

import * as React from "react";

import { useDocs } from "@/app/console/docs/docs-shell";
import { Input } from "@/components/ui/input";

/** ExampleModelField lets a reader put the model they will call into every
 * example on every page. / ExampleModelField 让读者把自己要调用的模型填进每个页面
 * 的每个示例。 */
export function ExampleModelField() {
  const { model, setModel } = useDocs();
  const id = React.useId();
  return (
    <div className="flex flex-wrap items-center gap-2 text-sm">
      <label htmlFor={id} className="text-muted-foreground">
        <Message id="tenant.exampleModel" /></label>
      <Input
        id={id}
        value={model}
        maxLength={128}
        onChange={(event) => setModel(event.target.value)}
        className="h-8 w-56 font-mono text-xs"
        spellCheck={false}
      />
      <span className="text-xs text-muted-foreground">
        <Message id="tenant.enterAModelNameToReplaceItIn" /></span>
    </div>
  );
}
