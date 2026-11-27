"use client";

import * as React from "react";
import { PlayIcon, XIcon } from "lucide-react";

import { Button } from "@/components/ui/button";
import { isFileInput, type FlowInput } from "@/lib/studio/flows";
import { buildRunInputs, formFile, formText, initialFormValues, type BuiltRun, type FormValue } from "@/lib/studio/run-inputs";

/**
 * RunForm asks for a run's input values. Values are checked here first; the
 * Gateway's own refusals come back as errors per input.
 *
 * RunForm 收集一次运行的输入值。取值先在这里检查；Gateway 自己的拒绝以逐个输入的错误返回。
 *
 * An image or file input is a file picker. A rerun starts such an input with the
 * file the earlier run was given, shown by name and kept unless another file is
 * picked or it is removed. Which files are accepted is the Gateway's decision.
 * 图片或文件输入是文件选择器。重跑时这类输入以更早的运行收到的文件开头，按文件名展示，除非
 * 选了另一个文件或移除，否则沿用。接受哪些文件由 Gateway 决定。
 */
export function RunForm({
  inputs,
  previous,
  previousRunId,
  busy,
  serverErrors,
  onSubmit,
  onClose,
}: {
  inputs: FlowInput[];
  previous?: Record<string, unknown>;
  /** the run that `previous` came from, so its files can be kept
   * `previous` 所属的运行，使其文件可以沿用 */
  previousRunId?: string;
  busy: boolean;
  serverErrors: Record<string, string>;
  onSubmit: (built: BuiltRun) => void;
  onClose: () => void;
}) {
  const [values, setValues] = React.useState<Record<string, FormValue>>(() => initialFormValues(inputs, previous, previousRunId));
  const [errors, setErrors] = React.useState<Record<string, string>>({});
  // Bumping a file input's key empties the browser's own file box.
  // 改变文件输入的 key 会清空浏览器自带的文件框。
  const [pickerKeys, setPickerKeys] = React.useState<Record<string, number>>({});
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
    onSubmit({ inputs: built.inputs, files: built.files });
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
          if (isFileInput(input)) {
            const choice = formFile(values[input.name]);
            return (
              <div key={input.name} className="grid gap-1.5">
                <label htmlFor={id}>{label}</label>
                <input id={id} key={pickerKeys[input.name] ?? 0} type="file" accept={input.type === "image" ? "image/*" : undefined} aria-invalid={error ? true : undefined}
                  ref={i === 0 ? (element) => { first.current = element; } : undefined}
                  className="w-full min-w-0 rounded-lg border border-input bg-background text-sm file:mr-3 file:h-9 file:cursor-pointer file:border-0 file:border-r file:bg-muted file:px-3 file:text-sm file:text-foreground aria-invalid:border-destructive"
                  onChange={(event) => {
                    const file = event.target.files?.[0];
                    if (file) setValues({ ...values, [input.name]: { kind: "upload", file } });
                  }} />
                {choice ? (
                  <div className="flex items-center gap-2 text-xs text-muted-foreground">
                    <span className="min-w-0 flex-1 truncate">
                      {choice.kind === "reuse" ? `沿用上次的文件：${choice.filename || choice.artifactId}（选择新文件可替换）` : `将上传：${choice.file.name}（${fileSize(choice.file.size)}）`}
                    </span>
                    <Button variant="ghost" size="xs" aria-label={`移除 ${input.name} 的文件`}
                      onClick={() => {
                        setValues({ ...values, [input.name]: null });
                        setPickerKeys({ ...pickerKeys, [input.name]: (pickerKeys[input.name] ?? 0) + 1 });
                      }}>
                      移除
                    </Button>
                  </div>
                ) : null}
                {error ? <p className="text-xs text-destructive">{error}</p> : null}
              </div>
            );
          }
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
                <input id={id} type="text" inputMode="decimal" value={formText(values[input.name])} aria-invalid={error ? true : undefined}
                  ref={i === 0 ? (element) => { first.current = element; } : undefined}
                  className="h-9 rounded-lg border border-input bg-background px-2.5 text-sm outline-none focus-visible:border-ring focus-visible:ring-3 focus-visible:ring-ring/50 aria-invalid:border-destructive"
                  onChange={(event) => setValues({ ...values, [input.name]: event.target.value })} />
              ) : (
                <textarea id={id} rows={3} value={formText(values[input.name])} aria-invalid={error ? true : undefined}
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

/** fileSize is a byte count as people read it. / fileSize 是人读的字节数。 */
function fileSize(bytes: number): string {
  if (bytes < 1024) return `${bytes} B`;
  if (bytes < 1024 * 1024) return `${(bytes / 1024).toFixed(1)} KiB`;
  return `${(bytes / (1024 * 1024)).toFixed(1)} MiB`;
}
