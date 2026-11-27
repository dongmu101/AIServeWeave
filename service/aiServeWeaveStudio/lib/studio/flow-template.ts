/**
 * Template references as the editor reads them: a client-side mirror of
 * common/flowdef's ParseTemplate, used to tell whether a text is exactly one
 * reference (the only place an artifact field may appear) and which
 * references a text holds. The authority is common/flowdef, applied when the
 * flow is saved; where the two disagree the server's answer stands.
 *
 * 编辑器读取模板引用的方式：common/flowdef 的 ParseTemplate 在客户端的镜像，用来判断
 * 一段文本是否恰好是一个引用（产物字段只允许出现在这里），以及文本中有哪些引用。权威是
 * 保存时执行的 common/flowdef；两者不一致时以服务端的结论为准。
 */

export interface TemplateRef {
  scope: string;
  field: string;
}

export interface ParsedRefs {
  refs: TemplateRef[];
  /** the text is one reference with nothing but white space around it
   * 文本是一个引用，两侧只有空白 */
  sole: boolean;
}

const IDENT = /^[A-Za-z_][A-Za-z0-9_]{0,63}$/;

/** templateRefs lists a text's references in order of appearance. Text that
 * merely looks like a reference ({{name}}, {{ a | b }}, an unclosed "{{")
 * stays literal, exactly as on the server. A reference with more than two
 * parts is unsupported there; here it yields no references at all.
 *
 * templateRefs 按出现顺序列出文本中的引用。只是看起来像引用的文本（{{name}}、
 * {{ a | b }}、未闭合的「{{」）与服务端一样按字面文本处理。超过两段的引用在服务端不受
 * 支持；这里对它不返回任何引用。 */
export function templateRefs(text: string): ParsedRefs {
  const refs: TemplateRef[] = [];
  let literal = false;
  for (let i = 0; i < text.length; ) {
    if (!text.startsWith("{{", i)) {
      if (text[i].trim() !== "") literal = true;
      i++;
      continue;
    }
    const end = text.indexOf("}}", i + 2);
    if (end < 0) {
      if (text.slice(i).trim() !== "") literal = true;
      break;
    }
    const parts = text.slice(i + 2, end).trim().split(".");
    if (parts.length < 2 || !parts.every((part) => IDENT.test(part))) {
      literal = true;
      i++;
      continue;
    }
    if (parts.length > 2) return { refs: [], sole: false };
    refs.push({ scope: parts[0], field: parts[1] });
    i = end + 2;
  }
  return { refs, sole: refs.length === 1 && !literal };
}
