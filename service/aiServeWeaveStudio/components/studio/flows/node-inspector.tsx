"use client";

import * as React from "react";
import { BracesIcon, PlusIcon, Trash2Icon, TriangleAlertIcon } from "lucide-react";
import { cn } from "cn";

import { NODE_KIND } from "@/components/studio/flows/flow-node";
import { Button } from "@/components/ui/button";
import { DropdownMenu, DropdownMenuContent, DropdownMenuItem, DropdownMenuTrigger } from "@/components/ui/dropdown-menu";
import type { StudioModel } from "@/lib/studio/contract";
import type { FlowCatalog, ImageModelCatalog, TemplateInput, TemplateInputType, WorkflowTemplate } from "@/lib/studio/flow-catalog";
import { artifactOutputs } from "@/lib/studio/artifacts";
import {
  comfyConfig,
  conditionConfig,
  imageConfig,
  isIdentifier,
  isImageSize,
  llmConfig,
  OPERATORS,
  referencesFor,
  setConfig,
  templateConfig,
  updateNode,
  type Reference,
} from "@/lib/studio/flow-graph";
import { templateRefs } from "@/lib/studio/flow-template";
import { isFileInput, issueText, type FlowDefinition, type FlowInput, type FlowIssue, type FlowOutput, type InputType, type JSONField, type ValueType } from "@/lib/studio/flows";

/**
 * NodeInspector edits the selected node: its display name and its config,
 * or — for the input and output nodes — the flow's run-time inputs and result
 * fields. Every change goes straight into the definition; nothing is saved
 * until the editor saves.
 *
 * NodeInspector 编辑选中的节点：显示名称与配置；对输入与输出节点，则是编排的运行时输入与
 * 结果字段。每次修改都直接写入定义；编辑器保存之前不会存储任何东西。
 */

const fieldClass =
  "w-full min-w-0 rounded-lg border border-input bg-background px-2.5 py-1.5 text-sm outline-none transition-colors placeholder:text-muted-foreground focus-visible:border-ring focus-visible:ring-3 focus-visible:ring-ring/50 aria-invalid:border-destructive";

const TYPE_LABEL: Record<ValueType, string> = { text: "文本", number: "数字", boolean: "是/否" };
const INPUT_TYPE_LABEL: Record<InputType, string> = { ...TYPE_LABEL, image: "图片", file: "文件" };

export function NodeInspector({
  def,
  nodeId,
  models,
  catalog,
  issues,
  warnings,
  onChange,
  onDelete,
}: {
  def: FlowDefinition;
  nodeId: string;
  models: StudioModel[];
  catalog: FlowCatalog;
  issues: FlowIssue[];
  /** why the node cannot run now, such as an unavailable model or template
   * 节点当前无法运行的原因，例如不可用的模型或模板 */
  warnings: FlowIssue[];
  onChange: (def: FlowDefinition) => void;
  onDelete: (id: string) => void;
}) {
  const node = def.nodes.find((n) => n.id === nodeId);
  if (!node) return null;
  const kind = NODE_KIND[node.type];
  // An artifact (a file input, or the image and files a node makes) may only be
  // the whole of an output's result field, or of a ComfyUI template's file
  // input; no other variable menu lists one.
  // 产物（文件输入，或节点产出的图片与文件）只能作为输出结果字段的全部内容，或 ComfyUI 模板
  // 文件型输入的全部内容；其他变量菜单都不列出它们。
  const references = referencesFor(def, node.id, node.type === "output" ? "output" : "text");
  const fileReferences = node.type === "comfyui_template" ? referencesFor(def, node.id, "file") : [];
  const config = (next: Record<string, unknown>) => onChange(setConfig(def, node.id, next));
  const invalid = (field: string) => issues.some((issue) => issue.field === field || issue.field.startsWith(`${field}.`));

  return (
    <div className="grid gap-4 p-4">
      <div className="flex items-center gap-2">
        <span className={cn("inline-flex size-7 items-center justify-center rounded-md [&_svg]:size-4", kind.tint)}>{kind.icon}</span>
        <div className="min-w-0 flex-1">
          <p className="text-sm font-medium">{kind.label}节点</p>
          <p className="font-mono text-xs text-muted-foreground">ID：{node.id}</p>
        </div>
        {node.type !== "input" && node.type !== "output" ? (
          <Button variant="ghost" size="icon-sm" aria-label="删除节点" onClick={() => onDelete(node.id)}>
            <Trash2Icon />
          </Button>
        ) : null}
      </div>

      {issues.length > 0 ? (
        <ul className="grid gap-1 rounded-lg bg-destructive/10 px-3 py-2 text-xs text-destructive" aria-label="这个节点的问题">
          {issues.map((issue, i) => (
            <li key={i}>{issueText(issue)}</li>
          ))}
        </ul>
      ) : null}

      {warnings.length > 0 ? (
        <ul className="grid gap-1 rounded-lg bg-amber-500/10 px-3 py-2 text-xs text-amber-700 dark:text-amber-300" aria-label="这个节点暂时无法运行">
          {warnings.map((warning, i) => (
            <li key={i} className="flex items-start gap-1.5">
              <TriangleAlertIcon className="mt-px size-3.5 shrink-0" />
              {issueText(warning)}
            </li>
          ))}
        </ul>
      ) : null}

      <Field label="显示名称">
        <input className={fieldClass} value={node.label ?? ""} maxLength={100} placeholder={kind.label}
          onChange={(event) => onChange(updateNode(def, node.id, (n) => ({ ...n, label: event.target.value })))} />
      </Field>

      {node.type === "input" ? <InputsEditor inputs={def.inputs} issues={issues} onChange={(inputs) => onChange({ ...def, inputs })} /> : null}
      {node.type === "output" ? <OutputsEditor def={def} outputs={def.outputs} references={references} issues={issues} onChange={(outputs) => onChange({ ...def, outputs })} /> : null}

      {node.type === "llm" ? (
        <LLMForm config={llmConfig(node)} models={models} references={references} invalid={invalid} onChange={config} />
      ) : null}

      {node.type === "image_generation" ? (
        <ImageForm config={imageConfig(node)} catalog={catalog.imageModels} references={references} invalid={invalid} onChange={config} />
      ) : null}

      {node.type === "comfyui_template" ? (
        <ComfyForm config={comfyConfig(node)} templates={catalog.templates} references={references} fileReferences={fileReferences} invalid={invalid} onChange={config} />
      ) : null}

      {node.type === "template" ? (
        <TemplateField label="模板" rows={8} value={templateConfig(node).template} references={references} invalid={invalid("config.template")}
          placeholder="例如：标题：{{draft.text}}" onChange={(template) => config({ template })} />
      ) : null}

      {node.type === "condition" ? (
        <ConditionForm node={conditionConfig(node)} references={references} invalid={invalid} onChange={config} />
      ) : null}
    </div>
  );
}

function Field({ label, hint, children }: { label: string; hint?: string; children: React.ReactNode }) {
  return (
    <label className="grid gap-1.5">
      <span className="text-xs font-medium text-muted-foreground">{label}</span>
      {children}
      {hint ? <span className="text-xs text-muted-foreground/80">{hint}</span> : null}
    </label>
  );
}

/** TemplateField is a template input with a menu of the variables this node
 * may use, inserted at the caret.
 *
 * TemplateField 是一个模板输入框，附带该节点可用变量的菜单，选中后插入到光标处。 */
function TemplateField({
  label,
  value,
  onChange,
  references,
  rows = 4,
  placeholder,
  invalid,
  single,
}: {
  label: string;
  value: string;
  onChange: (value: string) => void;
  references: Reference[];
  rows?: number;
  placeholder?: string;
  invalid?: boolean;
  single?: boolean;
}) {
  const ref = React.useRef<HTMLTextAreaElement & HTMLInputElement>(null);
  function insert(text: string) {
    const element = ref.current;
    const start = element?.selectionStart ?? value.length;
    const end = element?.selectionEnd ?? value.length;
    onChange(value.slice(0, start) + text + value.slice(end));
    requestAnimationFrame(() => {
      element?.focus();
      element?.setSelectionRange(start + text.length, start + text.length);
    });
  }
  return (
    <div className="grid gap-1.5">
      <div className="flex items-center justify-between gap-2">
        <span className="text-xs font-medium text-muted-foreground">{label}</span>
        <DropdownMenu>
          <DropdownMenuTrigger
            disabled={references.length === 0}
            render={<button type="button" className="inline-flex items-center gap-1 rounded px-1.5 py-0.5 text-xs text-muted-foreground hover:bg-muted hover:text-foreground disabled:opacity-50" />}
          >
            <BracesIcon className="size-3.5" />
            插入变量
          </DropdownMenuTrigger>
          <DropdownMenuContent side="bottom" align="end" className="max-h-72 w-64 overflow-y-auto">
            {references.map((reference) => (
              <DropdownMenuItem key={reference.ref} onClick={() => insert(reference.ref)}>
                <span className="min-w-0 flex-1 truncate">{reference.label}</span>
                <span className="font-mono text-[10px] text-muted-foreground">{reference.ref}</span>
              </DropdownMenuItem>
            ))}
          </DropdownMenuContent>
        </DropdownMenu>
      </div>
      {single ? (
        <input ref={ref} className={cn(fieldClass, "font-mono")} value={value} placeholder={placeholder} aria-label={label} aria-invalid={invalid || undefined}
          onChange={(event) => onChange(event.target.value)} />
      ) : (
        <textarea ref={ref} className={cn(fieldClass, "resize-y leading-relaxed")} rows={rows} value={value} placeholder={placeholder} aria-label={label}
          aria-invalid={invalid || undefined} onChange={(event) => onChange(event.target.value)} />
      )}
    </div>
  );
}

function numberOrUndefined(text: string): number | undefined {
  if (text.trim() === "") return undefined;
  const value = Number(text);
  return Number.isFinite(value) ? value : undefined;
}

function LLMForm({
  config,
  models,
  references,
  invalid,
  onChange,
}: {
  config: ReturnType<typeof llmConfig>;
  models: StudioModel[];
  references: Reference[];
  invalid: (field: string) => boolean;
  onChange: (config: Record<string, unknown>) => void;
}) {
  const offered = models.some((m) => m.id === config.model);
  const set = (patch: Record<string, unknown>) => {
    const next: Record<string, unknown> = { ...config, ...patch };
    for (const key of Object.keys(next)) if (next[key] === undefined) delete next[key];
    onChange(next);
  };
  const fields = config.json_fields ?? [];
  return (
    <>
      <Field label="模型">
        <select className={fieldClass} value={config.model} aria-invalid={invalid("config.model") || !offered || undefined} onChange={(event) => set({ model: event.target.value })}>
          {config.model === "" ? <option value="">请选择模型</option> : null}
          {!offered && config.model !== "" ? <option value={config.model}>{config.model}（当前不可用）</option> : null}
          {models.map((m) => (
            <option key={m.id} value={m.id}>
              {m.name}
            </option>
          ))}
        </select>
      </Field>
      {!offered && config.model !== "" ? (
        <p className="flex items-center gap-1.5 text-xs text-amber-600">
          <TriangleAlertIcon className="size-3.5" />
          这个模型当前不可用，运行前请换一个。
        </p>
      ) : null}
      <TemplateField label="系统提示词（可选）" rows={3} value={config.system ?? ""} references={references} invalid={invalid("config.system")}
        placeholder="模型扮演的角色或回答要求" onChange={(system) => set({ system: system === "" ? undefined : system })} />
      <TemplateField label="提示词" rows={6} value={config.prompt} references={references} invalid={invalid("config.prompt")}
        placeholder="例如：用三句话总结：{{inputs.topic}}" onChange={(prompt) => set({ prompt })} />
      <div className="grid grid-cols-2 gap-3">
        <Field label="温度（可选）" hint="0–2，越高越发散">
          <input className={fieldClass} type="number" min={0} max={2} step={0.1} value={config.temperature ?? ""} aria-invalid={invalid("config.temperature") || undefined}
            onChange={(event) => set({ temperature: numberOrUndefined(event.target.value) })} />
        </Field>
        <Field label="最大输出长度（可选）" hint="单位为 token">
          <input className={fieldClass} type="number" min={1} step={1} value={config.max_tokens ?? ""} aria-invalid={invalid("config.max_tokens") || undefined}
            onChange={(event) => set({ max_tokens: numberOrUndefined(event.target.value) })} />
        </Field>
      </div>
      <div className="grid gap-2 rounded-lg border p-3">
        <label className="flex items-center gap-2 text-sm">
          <input type="checkbox" checked={fields.length > 0}
            onChange={(event) => set({ json_fields: event.target.checked ? [{ name: "result", type: "text" }] : undefined })} />
          要求按 JSON 返回指定字段
        </label>
        {fields.length > 0 ? (
          <>
            <p className="text-xs text-muted-foreground">下游节点可以用 {"{{节点ID.字段名}}"} 取到每个字段；回复全文仍是 {"{{节点ID.text}}"}。</p>
            {fields.map((field, i) => (
              <JSONFieldRow key={i} field={field} invalid={invalid(`config.json_fields[${i}]`)}
                onChange={(next) => set({ json_fields: fields.map((f, j) => (j === i ? next : f)) })}
                onRemove={() => set({ json_fields: fields.length > 1 ? fields.filter((_, j) => j !== i) : undefined })} />
            ))}
            <Button variant="outline" size="sm" className="justify-self-start" onClick={() => set({ json_fields: [...fields, { name: `field_${fields.length + 1}`, type: "text" }] })}>
              <PlusIcon />
              添加字段
            </Button>
          </>
        ) : null}
      </div>
    </>
  );
}

function JSONFieldRow({ field, invalid, onChange, onRemove }: { field: JSONField; invalid: boolean; onChange: (field: JSONField) => void; onRemove: () => void }) {
  return (
    <div className="grid grid-cols-[1fr_auto_auto] gap-2">
      <input className={cn(fieldClass, "font-mono")} value={field.name} aria-label="字段名" aria-invalid={invalid || !isIdentifier(field.name) || undefined}
        onChange={(event) => onChange({ ...field, name: event.target.value })} />
      <select className={cn(fieldClass, "w-24")} value={field.type} aria-label="字段类型" onChange={(event) => onChange({ ...field, type: event.target.value as ValueType })}>
        {(Object.keys(TYPE_LABEL) as ValueType[]).map((type) => (
          <option key={type} value={type}>
            {TYPE_LABEL[type]}
          </option>
        ))}
      </select>
      <Button variant="ghost" size="icon-sm" aria-label="删除字段" onClick={onRemove}>
        <Trash2Icon />
      </Button>
      <input className={cn(fieldClass, "col-span-3")} value={field.description ?? ""} placeholder="字段说明（可选，会告诉模型）" aria-label="字段说明"
        onChange={(event) => onChange({ ...field, description: event.target.value })} />
    </div>
  );
}

const TEMPLATE_TYPE_LABEL: Record<TemplateInputType, string> = { string: "文本", integer: "整数", number: "数字", boolean: "是/否", file: "文件" };

function ImageForm({
  config,
  catalog,
  references,
  invalid,
  onChange,
}: {
  config: ReturnType<typeof imageConfig>;
  catalog: ImageModelCatalog | null;
  references: Reference[];
  invalid: (field: string) => boolean;
  onChange: (config: Record<string, unknown>) => void;
}) {
  const set = (patch: Record<string, unknown>) => {
    const next: Record<string, unknown> = { ...config, ...patch };
    for (const key of Object.keys(next)) if (next[key] === undefined) delete next[key];
    onChange(next);
  };
  const known = catalog?.models.some((m) => m.id === config.model) ?? true;
  const noDefault = catalog !== null && !catalog.defaultAvailable;
  const gone = config.model !== "" && !known;
  const sizeBad = config.size !== undefined && !isImageSize(config.size);
  return (
    <>
      <Field label="图像模型">
        <select className={fieldClass} value={config.model} aria-invalid={invalid("config.model") || gone || (config.model === "" && noDefault) || undefined}
          onChange={(event) => set({ model: event.target.value })}>
          {catalog === null ? (
            <option value={config.model}>{config.model || "部署默认"}</option>
          ) : (
            <>
              {catalog.defaultAvailable ? <option value="">部署默认</option> : <option value="" disabled>请选择模型</option>}
              {gone ? <option value={config.model}>{config.model}（当前不可用）</option> : null}
              {catalog.models.map((m) => (
                <option key={m.id} value={m.id}>
                  {m.name}
                </option>
              ))}
            </>
          )}
        </select>
      </Field>
      {gone ? (
        <p className="flex items-center gap-1.5 text-xs text-amber-600">
          <TriangleAlertIcon className="size-3.5" />
          这个图像模型当前不可用，运行前请换一个。
        </p>
      ) : null}
      {config.model === "" && noDefault ? (
        <p className="flex items-center gap-1.5 text-xs text-amber-600">
          <TriangleAlertIcon className="size-3.5" />
          部署没有默认的图像生成，运行前请选择一个图像模型。
        </p>
      ) : null}
      <TemplateField label="提示词" rows={6} value={config.prompt} references={references} invalid={invalid("config.prompt")}
        placeholder="例如：一只在雨中打伞的猫，{{inputs.style}} 风格" onChange={(prompt) => set({ prompt })} />
      <Field label="尺寸（可选）" hint="宽x高，每边 1–16384，例如 1024x1024；留空由模型决定">
        <input className={cn(fieldClass, "font-mono")} value={config.size ?? ""} placeholder="1024x1024" aria-invalid={invalid("config.size") || sizeBad || undefined}
          onChange={(event) => set({ size: event.target.value.trim() === "" ? undefined : event.target.value.trim() })} />
      </Field>
      <p className="text-xs text-muted-foreground">
        产出的图片是 {"{{节点ID.image}}"}：可以在输出节点的结果字段里整个引用，或整个作为后续 ComfyUI 模板节点的文件型输入；不能放进提示词、模板或条件。
      </p>
    </>
  );
}

/** inputHint is the one line of facts about a workflow template input.
 * inputHint 是关于工作流模板某个输入的一行说明。 */
function inputHint(input: TemplateInput): string {
  const parts: string[] = [TEMPLATE_TYPE_LABEL[input.type]];
  parts.push(input.required ? "必填" : "选填");
  if (input.default !== undefined) parts.push(`默认 ${String(input.default)}`);
  if (input.maxLength !== undefined) parts.push(`最长 ${input.maxLength} 字符`);
  if (input.min !== undefined || input.max !== undefined) parts.push(`范围 ${input.min ?? "…"} – ${input.max ?? "…"}`);
  return parts.join(" · ");
}

function ComfyForm({
  config,
  templates,
  references,
  fileReferences,
  invalid,
  onChange,
}: {
  config: ReturnType<typeof comfyConfig>;
  templates: WorkflowTemplate[] | null;
  references: Reference[];
  /** the sources a file input may take: image and file inputs of the flow
   * and the image of an upstream node
   * 文件型输入可选的来源：编排的 image 与 file 输入，以及上游节点的 image */
  fileReferences: Reference[];
  invalid: (field: string) => boolean;
  onChange: (config: Record<string, unknown>) => void;
}) {
  const template = templates?.find((t) => t.id === config.template);
  const gone = templates !== null && config.template !== "" && !template;
  const declared = new Set(template?.inputs.map((input) => input.name));
  const stale = template ? Object.keys(config.inputs).filter((name) => !declared.has(name)) : [];
  const setInput = (name: string, value: string) => {
    const inputs = { ...config.inputs };
    if (value === "") delete inputs[name];
    else inputs[name] = value;
    onChange({ ...config, inputs });
  };
  function pick(id: string) {
    // Inputs the new template also declares keep their values; the rest would
    // only be refused at run time.
    // 新模板同样声明的输入保留取值；其余的只会在运行时被拒绝。
    const next = templates?.find((t) => t.id === id);
    const keep = new Set(next?.inputs.map((input) => input.name));
    onChange({ template: id, inputs: Object.fromEntries(Object.entries(config.inputs).filter(([name]) => keep.has(name))) });
  }
  return (
    <>
      <Field label="工作流模板">
        <select className={fieldClass} value={config.template} aria-invalid={invalid("config.template") || gone || undefined} onChange={(event) => pick(event.target.value)}>
          {templates === null ? <option value={config.template}>{config.template || "（模板列表加载失败）"}</option> : null}
          {templates !== null && config.template === "" ? <option value="">请选择模板</option> : null}
          {gone ? <option value={config.template}>{config.template}（当前不可用）</option> : null}
          {templates?.map((t) => (
            <option key={t.id} value={t.id}>
              {t.id}
            </option>
          ))}
        </select>
        {template?.description ? <span className="text-xs text-muted-foreground/80">{template.description}</span> : null}
      </Field>
      {gone ? (
        <p className="flex items-center gap-1.5 text-xs text-amber-600">
          <TriangleAlertIcon className="size-3.5" />
          这个模板当前不可用（未发布或对你不可见），运行前请换一个。
        </p>
      ) : null}
      {template ? (
        <div className="grid gap-3">
          <div>
            <p className="text-xs font-medium text-muted-foreground">模板输入</p>
            <p className="mt-0.5 text-xs text-muted-foreground/80">留空则使用模板自己的默认值；可以用「插入变量」引用输入或上游结果。</p>
          </div>
          {template.inputs.length === 0 ? <p className="text-xs text-muted-foreground">这个模板没有输入。</p> : null}
          {template.inputs.map((input) =>
            input.type === "file" ? (
              <FileSource key={input.name} input={input} value={config.inputs[input.name] ?? ""} sources={fileReferences} invalid={invalid(`config.inputs.${input.name}`)}
                onChange={(value) => setInput(input.name, value)} />
            ) : (
              <TemplateField key={input.name} label={`${input.name}（${inputHint(input)}）`} rows={input.type === "string" ? 3 : 1} single={input.type !== "string"}
                value={config.inputs[input.name] ?? ""} references={references} invalid={invalid(`config.inputs.${input.name}`)}
                placeholder={input.default !== undefined ? String(input.default) : undefined} onChange={(value) => setInput(input.name, value)} />
            )
          )}
          {stale.map((name) => (
            <div key={name} className="flex items-center gap-2 rounded-lg border border-amber-500 p-2 text-xs">
              <TriangleAlertIcon className="size-3.5 shrink-0 text-amber-600" />
              <span className="min-w-0 flex-1 truncate">
                <span className="font-mono">{name}</span>：模板没有这个输入
              </span>
              <Button variant="ghost" size="icon-sm" aria-label={`移除输入 ${name}`} onClick={() => setInput(name, "")}>
                <Trash2Icon />
              </Button>
            </div>
          ))}
          {template.outputs.length > 0 ? (
            <p className="text-xs text-muted-foreground">模板产出：{template.outputs.map((o) => o.name).join("、")}。</p>
          ) : null}
        </div>
      ) : templates !== null && config.template === "" ? (
        <p className="text-xs text-muted-foreground">选择模板后，在这里为它的输入赋值。</p>
      ) : null}
      <p className="text-xs text-muted-foreground">
        第一张图片是 {"{{节点ID.image}}"}，全部产物是 {"{{节点ID.artifacts}}"}。它们可以在输出节点的结果字段里整个引用；{"{{节点ID.image}}"} 还可以整个作为后续
        ComfyUI 模板节点的文件型输入，{"{{节点ID.artifacts}}"} 不行。
      </p>
    </>
  );
}

/** FileSource picks where a workflow template's file input gets its file: a
 * file input of the flow, or the image an upstream node makes. It is a choice
 * among those and nothing typed, since only one whole file may go there. A
 * value that is no longer one of them (a removed input, a hand-written text)
 * stays visible, marked, so it can be replaced.
 *
 * FileSource 选择工作流模板的文件型输入从哪里取文件：编排的文件输入，或上游节点产出的图片。
 * 只能在这些里选而不能键入，因为那里只能整个放一个文件。不再属于它们的取值（已删除的输入、
 * 手写的文本）仍保持可见并加以标注，以便替换。 */
function FileSource({ input, value, sources, invalid, onChange }: { input: TemplateInput; value: string; sources: Reference[]; invalid: boolean; onChange: (value: string) => void }) {
  const { refs, sole } = templateRefs(value);
  // Written as {{ inputs.photo }}, it is still the source {{inputs.photo}}.
  // 写成 {{ inputs.photo }} 时，它仍是来源 {{inputs.photo}}。
  const canonical = sole ? `{{${refs[0].scope}.${refs[0].field}}}` : "";
  const known = sources.some((source) => source.ref === canonical);
  const chosen = known ? canonical : value;
  const stale = value.trim() !== "" && !known;
  const bad = invalid || stale || (input.required && input.default === undefined && value.trim() === "");
  return (
    <div className="grid gap-1.5">
      <label className="grid gap-1.5">
        <span className="text-xs font-medium text-muted-foreground">
          <span className="font-mono">{input.name}</span>（{inputHint(input)}）
        </span>
        <select className={fieldClass} value={chosen} aria-invalid={bad || undefined} aria-label={`${input.name} 的文件来源`} onChange={(event) => onChange(event.target.value)}>
          <option value="">{input.required && input.default === undefined ? "请选择文件来源" : "不设置（使用模板默认值）"}</option>
          {stale ? <option value={value}>{value}（不是可用的文件来源）</option> : null}
          {sources.map((source) => (
            <option key={source.ref} value={source.ref}>
              {source.label}
            </option>
          ))}
        </select>
      </label>
      {stale ? (
        <p className="flex items-center gap-1.5 text-xs text-amber-600">
          <TriangleAlertIcon className="size-3.5 shrink-0" />
          这里只能选一个文件来源，请重新选择。
        </p>
      ) : null}
      {sources.length === 0 ? (
        <p className="text-xs text-muted-foreground">
          还没有可选的文件来源：在输入节点添加一个图片或文件输入，或让图像生成、ComfyUI 模板节点连到这个节点之前。
        </p>
      ) : null}
    </div>
  );
}

function ConditionForm({
  node,
  references,
  invalid,
  onChange,
}: {
  node: ReturnType<typeof conditionConfig>;
  references: Reference[];
  invalid: (field: string) => boolean;
  onChange: (config: Record<string, unknown>) => void;
}) {
  const operator = OPERATORS.find((o) => o.value === node.op);
  return (
    <>
      <TemplateField label="比较的值" rows={2} single value={node.left} references={references} invalid={invalid("config.left")}
        placeholder="{{draft.text}}" onChange={(left) => onChange({ ...node, left })} />
      <Field label="比较方式">
        <select className={fieldClass} value={node.op} onChange={(event) => onChange({ ...node, op: event.target.value })}>
          {OPERATORS.map((o) => (
            <option key={o.value} value={o.value}>
              {o.label}
            </option>
          ))}
        </select>
      </Field>
      {operator?.unary ? null : (
        <TemplateField label={operator?.numeric ? "与之比较的数字" : "与之比较的文本"} single value={node.right ?? ""} references={references}
          invalid={invalid("config.right")} placeholder={operator?.numeric ? "7" : "TODO"} onChange={(right) => onChange({ ...node, right })} />
      )}
      <p className="text-xs text-muted-foreground">两侧会先去掉首尾空白。结果为「是」时走右侧上方的连接点，「否」时走下方的连接点。</p>
    </>
  );
}

function InputsEditor({ inputs, issues, onChange }: { inputs: FlowInput[]; issues: FlowIssue[]; onChange: (inputs: FlowInput[]) => void }) {
  const update = (i: number, next: FlowInput) => onChange(inputs.map((input, j) => (j === i ? next : input)));
  return (
    <div className="grid gap-3">
      <div>
        <p className="text-xs font-medium text-muted-foreground">运行时输入</p>
        <p className="mt-0.5 text-xs text-muted-foreground/80">
          每次运行时填写，在任何节点中以 {"{{inputs.名称}}"} 引用。图片与文件输入在运行时上传，只能整个作为结果字段的取值，或整个填入 ComfyUI 模板的文件型输入。
        </p>
      </div>
      {inputs.map((input, i) => {
        const invalid = issues.some((issue) => issue.field === `inputs.${input.name}` || issue.field.startsWith(`inputs.${input.name}.`) || issue.field.startsWith(`inputs[${i}]`));
        return (
          <div key={i} className={cn("grid gap-2 rounded-lg border p-3", invalid && "border-destructive")}>
            <div className="grid grid-cols-[1fr_auto_auto] gap-2">
              <input className={cn(fieldClass, "font-mono")} value={input.name} aria-label="变量名" aria-invalid={!isIdentifier(input.name) || undefined}
                onChange={(event) => update(i, { ...input, name: event.target.value })} />
              <select className={cn(fieldClass, "w-20")} value={input.type} aria-label="类型"
                onChange={(event) => update(i, { ...input, type: event.target.value as InputType, default: undefined })}>
                {(Object.keys(INPUT_TYPE_LABEL) as InputType[]).map((type) => (
                  <option key={type} value={type}>
                    {INPUT_TYPE_LABEL[type]}
                  </option>
                ))}
              </select>
              <Button variant="ghost" size="icon-sm" aria-label="删除输入" onClick={() => onChange(inputs.filter((_, j) => j !== i))}>
                <Trash2Icon />
              </Button>
            </div>
            <input className={fieldClass} value={input.label ?? ""} maxLength={100} placeholder="显示名称（可选）" aria-label="显示名称"
              onChange={(event) => update(i, { ...input, label: event.target.value })} />
            <div className="flex items-center gap-3">
              <label className="flex items-center gap-1.5 text-sm">
                <input type="checkbox" checked={input.required} onChange={(event) => update(i, { ...input, required: event.target.checked })} />
                必填
              </label>
              {isFileInput(input) ? (
                <span className="text-xs text-muted-foreground">运行时上传{input.type === "image" ? "图片" : "文件"}，不能设默认值。</span>
              ) : input.type === "boolean" ? (
                <label className="flex items-center gap-1.5 text-sm">
                  <input type="checkbox" checked={input.default === true} onChange={(event) => update(i, { ...input, default: event.target.checked })} />
                  默认为「是」
                </label>
              ) : (
                <input className={fieldClass} type={input.type === "number" ? "number" : "text"} value={input.default === undefined ? "" : String(input.default)}
                  placeholder="默认值（可选）" aria-label="默认值"
                  onChange={(event) => {
                    const text = event.target.value;
                    update(i, { ...input, default: text === "" ? undefined : input.type === "number" ? numberOrUndefined(text) : text });
                  }} />
              )}
            </div>
          </div>
        );
      })}
      <Button variant="outline" size="sm" className="justify-self-start"
        onClick={() => onChange([...inputs, { name: `input_${inputs.length + 1}`, type: "text", required: false }])}>
        <PlusIcon />
        添加输入
      </Button>
    </div>
  );
}

function OutputsEditor({ def, outputs, references, issues, onChange }: { def: FlowDefinition; outputs: FlowOutput[]; references: Reference[]; issues: FlowIssue[]; onChange: (outputs: FlowOutput[]) => void }) {
  const artifacts = artifactOutputs(def);
  const update = (i: number, next: FlowOutput) => onChange(outputs.map((output, j) => (j === i ? next : output)));
  return (
    <div className="grid gap-3">
      <div>
        <p className="text-xs font-medium text-muted-foreground">结果字段</p>
        <p className="mt-0.5 text-xs text-muted-foreground/80">运行结束后展示的内容，通常引用某个上游节点的结果。图片与文件结果要在这里整个引用，不能与其他文字混写。</p>
      </div>
      {outputs.map((output, i) => {
        const invalid = issues.some((issue) => issue.field.startsWith(`outputs.${output.name}`) || issue.field.startsWith(`outputs[${i}]`));
        return (
          <div key={i} className={cn("grid gap-2 rounded-lg border p-3", invalid && "border-destructive")}>
            <div className="flex gap-2">
              <input className={cn(fieldClass, "font-mono")} value={output.name} aria-label="字段名" aria-invalid={!isIdentifier(output.name) || undefined}
                onChange={(event) => update(i, { ...output, name: event.target.value })} />
              <Button variant="ghost" size="icon-sm" aria-label="删除结果字段" onClick={() => onChange(outputs.filter((_, j) => j !== i))}>
                <Trash2Icon />
              </Button>
            </div>
            <TemplateField label="取值" single value={output.ref} references={references} invalid={invalid} placeholder="{{draft.text}}"
              onChange={(ref) => update(i, { ...output, ref })} />
            {artifacts[output.name] ? (
              <p className="text-xs text-muted-foreground">
                {artifacts[output.name] === "image" ? "这是一张图片" : artifacts[output.name] === "file" ? "这是一个文件" : "这是一组产物文件"}，运行结果里会直接展示并可下载。需要整个引用，不能和其他文字混写。
              </p>
            ) : null}
          </div>
        );
      })}
      <Button variant="outline" size="sm" className="justify-self-start"
        onClick={() => onChange([...outputs, { name: `result_${outputs.length + 1}`, ref: references.find((r) => !r.ref.startsWith("{{inputs."))?.ref ?? "" }])}>
        <PlusIcon />
        添加结果字段
      </Button>
    </div>
  );
}
