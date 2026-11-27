import { ARTIFACT_ID_PATTERN, RUN_ID_PATTERN, parseFileValue } from "./artifacts.ts";
import { isFileInput, issueText, parseDefinition, type FlowDefinition, type FlowInput, type FlowIssue } from "./flows.ts";

/**
 * The run form and the flow file: turning form values into a run's inputs,
 * and a flow into a file and back.
 *
 * A run with files is a multipart/form-data request: the usual JSON body in a
 * "request" field and one file part per image or file input, named after the
 * input. A run without files stays JSON. A rerun keeps an earlier run's file
 * by naming it, {"run_id","artifact_id"}, as the input's JSON value; the
 * Gateway copies it. Which files are acceptable (type, size) is the
 * Gateway's call and its refusal is shown; nothing is limited here.
 *
 * 运行表单与编排文件：把表单值转换为一次运行的输入，以及把编排导出为文件并导回。
 *
 * 带文件的运行是 multipart/form-data 请求：通常的 JSON 请求体放在「request」字段，每个
 * image 或 file 输入一个以输入名命名的文件分片。不带文件的运行仍是 JSON。重跑通过把更早
 * 运行的文件命名为 {"run_id","artifact_id"}、作为该输入的 JSON 取值来保留它；Gateway 负责
 * 复制。哪些文件可接受（类型、大小）由 Gateway 决定，它的拒绝会展示出来；这里不设任何限制。
 */

/** FileChoice is what a file input holds in the form: a file picked now, or
 * one an earlier run was given, kept by name.
 * FileChoice 是文件输入在表单里持有的内容：现在选的文件，或更早的运行收到的、按名沿用的文件。 */
export type FileChoice =
  | { kind: "upload"; file: File }
  | { kind: "reuse"; runId: string; artifactId: string; filename: string };

/** FormValue is one input's value in the form; a file input holds a
 * FileChoice, or null while nothing is chosen.
 * FormValue 是表单中一个输入的取值；文件输入持有 FileChoice，未选择时为 null。 */
export type FormValue = string | boolean | FileChoice | null;

/** formText is a text or number input's text. / formText 是文本或数字输入的文本。 */
export function formText(value: FormValue | undefined): string {
  return typeof value === "string" ? value : "";
}

/** formFile is a file input's choice, or null. / formFile 是文件输入的选择，没有则为 null。 */
export function formFile(value: FormValue | undefined): FileChoice | null {
  return typeof value === "object" && value !== null ? value : null;
}

/** initialFormValues fills the form from the inputs' defaults, or from a
 * previous run's values when repeating it. A file the previous run was given
 * is kept by name, which needs that run's id; without it the file input
 * starts empty.
 *
 * initialFormValues 用输入的默认值填充表单；重跑时改用上一次运行的取值。上一次运行收到的
 * 文件按名沿用，这需要该运行的 id；没有它时文件输入为空。 */
export function initialFormValues(inputs: FlowInput[], previous?: Record<string, unknown>, previousRunId = ""): Record<string, FormValue> {
  const out: Record<string, FormValue> = {};
  for (const input of inputs) {
    const value = previous && input.name in previous ? previous[input.name] : input.default;
    if (isFileInput(input)) {
      const file = RUN_ID_PATTERN.test(previousRunId) ? parseFileValue(value) : null;
      out[input.name] = file ? { kind: "reuse", runId: previousRunId, artifactId: file.artifactId, filename: file.filename } : null;
    } else if (input.type === "boolean") out[input.name] = value === true;
    else out[input.name] = value === undefined || value === null ? "" : String(value);
  }
  return out;
}

/** ReusedFile is the JSON value of an input that keeps an earlier run's file.
 * ReusedFile 是沿用更早运行的文件的输入的 JSON 取值。 */
export interface ReusedFile {
  run_id: string;
  artifact_id: string;
}

export type RunInputValue = string | number | boolean | ReusedFile;

/** BuiltRun is a run ready to send: the JSON values, and the files to upload
 * by input name.
 * BuiltRun 是准备好发送的运行：JSON 取值，以及按输入名索引的待上传文件。 */
export interface BuiltRun {
  inputs: Record<string, RunInputValue>;
  files: Record<string, File>;
}

export type BuiltInputs = ({ ok: true } & BuiltRun) | { ok: false; errors: Record<string, string> };

/** buildRunInputs checks the form and returns what to send. A blank
 * optional field is left out so its default applies; an uploaded file goes
 * in `files` and not in `inputs`, as the Gateway refuses an input given both
 * ways.
 *
 * buildRunInputs 检查表单并返回要发送的内容。留空的选填项不发送，以便采用其默认值；上传的
 * 文件放进 files 而不放进 inputs，因为同一个输入两种方式都给，Gateway 会拒绝。 */
export function buildRunInputs(inputs: FlowInput[], values: Record<string, FormValue>): BuiltInputs {
  const out: Record<string, RunInputValue> = {};
  const files: Record<string, File> = {};
  const errors: Record<string, string> = {};
  for (const input of inputs) {
    const value = values[input.name];
    if (isFileInput(input)) {
      const choice = formFile(value);
      if (!choice) {
        if (input.required) errors[input.name] = "请选择文件。";
      } else if (choice.kind === "upload") {
        files[input.name] = choice.file;
      } else if (RUN_ID_PATTERN.test(choice.runId) && ARTIFACT_ID_PATTERN.test(choice.artifactId)) {
        out[input.name] = { run_id: choice.runId, artifact_id: choice.artifactId };
      } else {
        errors[input.name] = "上次的文件无法沿用，请重新选择文件。";
      }
      continue;
    }
    if (input.type === "boolean") {
      out[input.name] = value === true;
      continue;
    }
    const text = formText(value);
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
  return Object.keys(errors).length > 0 ? { ok: false, errors } : { ok: true, inputs: out, files };
}

/** RunStartBody is the body of a run start: JSON, or multipart form data
 * when files go with it. / RunStartBody 是启动运行的请求体：JSON，带文件时为 multipart 表单。 */
export type RunStartBody = { inputs: Record<string, RunInputValue>; version?: number } | FormData;

/** runStartBody makes the request body for a built run. Without files it is
 * the JSON body as ever; with files it is a form whose "request" field holds
 * that same JSON and whose file parts are named after their inputs.
 *
 * runStartBody 为构建好的运行生成请求体。没有文件时是一如既往的 JSON 请求体；有文件时是一个
 * 表单，其「request」字段放同样的 JSON，文件分片以各自的输入名命名。 */
export function runStartBody(built: BuiltRun, version?: number): RunStartBody {
  const body: { inputs: Record<string, RunInputValue>; version?: number } = { inputs: built.inputs };
  if (version !== undefined) body.version = version;
  const names = Object.keys(built.files);
  if (names.length === 0) return body;
  const form = new FormData();
  form.append("request", JSON.stringify(body));
  for (const name of names) form.append(name, built.files[name], built.files[name].name);
  return form;
}

/** serverInputErrors turns the issues of a refused run start into the text
 * to show under each input; an issue about no input in particular goes under
 * "". The first issue of an input is the one shown.
 *
 * serverInputErrors 把被拒绝的运行启动的问题列表转换为各输入下要展示的文字；不针对某个具体
 * 输入的问题归到 ""。每个输入展示它的第一个问题。 */
export function serverInputErrors(issues: FlowIssue[]): Record<string, string> {
  const errors: Record<string, string> = {};
  for (const issue of issues) {
    const key = /^inputs\.([A-Za-z_][A-Za-z0-9_]*)$/.exec(issue.field)?.[1] ?? "";
    if (!(key in errors)) errors[key] = issueText({ ...issue, field: "" });
  }
  return errors;
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
