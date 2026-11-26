import { parseDefinition, type FlowDefinition, type FlowInput } from "./flows.ts";

/**
 * The run form and the flow file: turning form values into a run's inputs,
 * and a flow into a file and back.
 *
 * 运行表单与编排文件：把表单值转换为一次运行的输入，以及把编排导出为文件并导回。
 */

export type FormValue = string | boolean;

/** initialFormValues fills the form from the inputs' defaults, or from a
 * previous run's values when repeating it.
 *
 * initialFormValues 用输入的默认值填充表单；重跑时改用上一次运行的取值。 */
export function initialFormValues(inputs: FlowInput[], previous?: Record<string, unknown>): Record<string, FormValue> {
  const out: Record<string, FormValue> = {};
  for (const input of inputs) {
    const value = previous && input.name in previous ? previous[input.name] : input.default;
    if (input.type === "boolean") out[input.name] = value === true;
    else out[input.name] = value === undefined || value === null ? "" : String(value);
  }
  return out;
}

export type BuiltInputs =
  | { ok: true; inputs: Record<string, string | number | boolean> }
  | { ok: false; errors: Record<string, string> };

/** buildRunInputs checks the form and returns the inputs to send. A blank
 * optional field is left out so its default applies.
 *
 * buildRunInputs 检查表单并返回要发送的输入。留空的选填项不发送，以便采用其默认值。 */
export function buildRunInputs(inputs: FlowInput[], values: Record<string, FormValue>): BuiltInputs {
  const out: Record<string, string | number | boolean> = {};
  const errors: Record<string, string> = {};
  for (const input of inputs) {
    const value = values[input.name];
    if (input.type === "boolean") {
      out[input.name] = value === true;
      continue;
    }
    const text = typeof value === "string" ? value : "";
    if (text.trim() === "") {
      if (input.required && input.default === undefined) errors[input.name] = "必填。";
      continue;
    }
    if (input.type === "number") {
      const parsed = Number(text.trim());
      if (!Number.isFinite(parsed)) {
        errors[input.name] = "请输入数字。";
        continue;
      }
      out[input.name] = parsed;
      continue;
    }
    out[input.name] = text;
  }
  return Object.keys(errors).length > 0 ? { ok: false, errors } : { ok: true, inputs: out };
}

const FILE_FORMAT = "aiserveweave-flow";

/** flowFile is the export of a flow: its name and definition, tagged so an
 * import can tell it from any other JSON.
 *
 * flowFile 是编排的导出：名称与定义，并带标记，使导入时能把它与其他 JSON 区分开。 */
export function flowFile(name: string, definition: FlowDefinition): string {
  return `${JSON.stringify({ format: FILE_FORMAT, version: 1, name, definition }, null, 2)}\n`;
}

/** readFlowFile reads an exported flow, or a bare definition. It returns null
 * for anything else.
 *
 * readFlowFile 读取导出的编排或单独的定义，其他内容返回 null。 */
export function readFlowFile(text: string): { name: string; definition: FlowDefinition } | null {
  try {
    const parsed: unknown = JSON.parse(text);
    if (typeof parsed !== "object" || parsed === null || Array.isArray(parsed)) return null;
    const source = parsed as Record<string, unknown>;
    if (source.format === FILE_FORMAT) {
      return { name: typeof source.name === "string" ? source.name : "", definition: parseDefinition(source.definition) };
    }
    if (Array.isArray(source.nodes)) return { name: "", definition: parseDefinition(source) };
    return null;
  } catch {
    return null;
  }
}
