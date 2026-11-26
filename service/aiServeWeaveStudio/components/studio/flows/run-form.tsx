"use client";

import * as React from "react";
import { PlayIcon, XIcon } from "lucide-react";

import { Button } from "@/components/ui/button";
import type { FlowInput } from "@/lib/studio/flows";
import { buildRunInputs, initialFormValues, type FormValue } from "@/lib/studio/run-inputs";

/**
 * RunForm asks for a run's input values. Values are checked here first; the
 * Gateway's own refusals come back as errors per input.
 *
 * RunForm 收集一次运行的输入值。取值先在这里检查；Gateway 自己的拒绝以逐个输入的错误返回。
 */
export function RunForm({
  inputs,
  previous,
  busy,
  serverErrors,
  onSubmit,
  onClose,
}: {
  inputs: FlowInput[];
  previous?: Record<string, unknown>;
  busy: boolean;
  serverErrors: Record<string, string>;
  onSubmit: (inputs: Record<string, string | number | boolean>) => void;
  onClose: () => void;
}) {
  const [values, setValues] = React.useState<Record<string, FormValue>>(() => initialFormValues(inputs, previous));
  const [errors, setErrors] = React.useState<Record<string, string>>({});
  const first = React.useRef<HTMLElement | null>(null);

  React.useEffect(() => {
    first.current?.focus();
    function onKey(event: KeyboardEvent) {
      if (event.key === "Escape") onClose();
    }
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [onClose]);

  function submit(event: React.FormEvent) {
    event.preventDefault();
    const built = buildRunInputs(inputs, values);
    if (!built.ok) {
      setErrors(built.errors);
      return;
    }
    setErrors({});
    onSubmit(built.inputs);
  }

  const shown = { ...serverErrors, ...errors };
  return (
    <div className="fixed inset-0 z-50 flex items-center justify-center p-4">
      <button type="button" aria-label="关闭" className="absolute inset-0 bg-black/40" onClick={onClose} />
      <form role="dialog" aria-modal="true" aria-labelledby="run-form-title" onSubmit={submit}
        className="relative grid max-h-[85svh] w-full max-w-lg gap-4 overflow-y-auto rounded-2xl border bg-background p-5 shadow-xl">
        <div className="flex items-center justify-between">
          <h2 id="run-form-title" className="font-heading text-lg font-medium">运行编排</h2>
          <Button variant="ghost" size="icon-sm" aria-label="关闭" onClick={onClose}>
            <XIcon />
          </Button>
        </div>
        {inputs.length === 0 ? <p className="text-sm text-muted-foreground">这个编排没有输入变量，直接运行即可。</p> : null}
        {inputs.map((input, i) => {
          const id = `run-input-${input.name}`;
          const error = shown[input.name];
          const label = (
            <span className="text-sm font-medium">
              {input.label || input.name}
              {input.required && input.default === undefined ? <span className="text-destructive"> *</span> : null}
              {input.label ? <span className="ml-1.5 font-mono text-xs font-normal text-muted-foreground">{input.name}</span> : null}
            </span>
          );
          if (input.type === "boolean") {
            return (
              <label key={input.name} className="flex items-center gap-2">
                <input id={id} type="checkbox" checked={values[input.name] === true}
                  ref={i === 0 ? (element) => { first.current = element; } : undefined}
                  onChange={(event) => setValues({ ...values, [input.name]: event.target.checked })} />
                {label}
              </label>
            );
          }
          return (
            <div key={input.name} className="grid gap-1.5">
              <label htmlFor={id}>{label}</label>
              {input.type === "number" ? (
                <input id={id} type="text" inputMode="decimal" value={String(values[input.name] ?? "")} aria-invalid={error ? true : undefined}
                  ref={i === 0 ? (element) => { first.current = element; } : undefined}
                  className="h-9 rounded-lg border border-input bg-background px-2.5 text-sm outline-none focus-visible:border-ring focus-visible:ring-3 focus-visible:ring-ring/50 aria-invalid:border-destructive"
                  onChange={(event) => setValues({ ...values, [input.name]: event.target.value })} />
              ) : (
                <textarea id={id} rows={3} value={String(values[input.name] ?? "")} aria-invalid={error ? true : undefined}
                  ref={i === 0 ? (element) => { first.current = element; } : undefined}
                  className="resize-y rounded-lg border border-input bg-background px-2.5 py-1.5 text-sm leading-relaxed outline-none focus-visible:border-ring focus-visible:ring-3 focus-visible:ring-ring/50 aria-invalid:border-destructive"
                  onChange={(event) => setValues({ ...values, [input.name]: event.target.value })} />
              )}
              {error ? <p className="text-xs text-destructive">{error}</p> : null}
            </div>
          );
        })}
        {shown[""] ? <p className="text-sm text-destructive">{shown[""]}</p> : null}
        <div className="flex justify-end gap-2">
          <Button variant="outline" onClick={onClose}>
            取消
          </Button>
          <Button type="submit" disabled={busy}>
            <PlayIcon />
            {busy ? "正在启动…" : "开始运行"}
          </Button>
        </div>
      </form>
    </div>
  );
}
