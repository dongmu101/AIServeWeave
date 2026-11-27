import { studioErrorCode } from "@/lib/studio/contract";
import { parseIssues } from "@/lib/studio/flows";
import { jsonResponse, errorResponse, rawJsonResponse } from "@/lib/server/responses";

/**
 * gatewayAnswer turns a Gateway answer that has been read into the browser's:
 * a success is forwarded as it is; a failure is reduced to its stable code,
 * never the Gateway's own message, and a refused flow or run keeps its
 * issues, which carry only node, field and code.
 *
 * gatewayAnswer 把读到的 Gateway 应答转换为给浏览器的应答：成功原样转发；失败只保留稳定
 * 错误码，绝不带 Gateway 自己的 message；被拒绝的编排或运行保留其问题列表，其中只有节点、
 * 字段与错误码。
 */
export function gatewayAnswer(status: number, text: string): Response {
  if (status >= 200 && status < 300) return rawJsonResponse(status, text);
  const code = studioErrorCode(text) || "upstream";
  if (status === 400) {
    let issues: ReturnType<typeof parseIssues> = [];
    try {
      issues = parseIssues(JSON.parse(text));
    } catch {
      issues = [];
    }
    if (issues.length > 0) return jsonResponse(400, { error: code, issues });
  }
  return errorResponse(status, code);
}
