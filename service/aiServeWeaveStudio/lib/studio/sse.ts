/**
 * An incremental parser for the Gateway's Studio SSE stream. Bytes arrive in
 * arbitrary pieces; push returns the frames each piece completes. Only the
 * "event" and "data" fields are read; a frame whose data is not JSON is
 * dropped rather than guessed at.
 *
 * Gateway Studio SSE 流的增量解析器。字节以任意分片到达；push 返回每个分片补全的帧。
 * 只读取 "event" 与 "data" 字段；data 不是 JSON 的帧直接丢弃，不做猜测。
 */

export interface GatewayFrame {
  event: string;
  data: Record<string, unknown>;
}

export class SSEParser {
  private buffer = "";

  push(text: string): GatewayFrame[] {
    this.buffer += text.replace(/\r\n?/g, "\n");
    const frames: GatewayFrame[] = [];
    let end: number;
    while ((end = this.buffer.indexOf("\n\n")) >= 0) {
      const block = this.buffer.slice(0, end);
      this.buffer = this.buffer.slice(end + 2);
      const frame = parseBlock(block);
      if (frame) frames.push(frame);
    }
    return frames;
  }
}

function parseBlock(block: string): GatewayFrame | null {
  let event = "message";
  const data: string[] = [];
  for (const line of block.split("\n")) {
    if (line.startsWith("event:")) event = line.slice(6).trim();
    else if (line.startsWith("data:")) data.push(line.slice(5).replace(/^ /, ""));
  }
  if (data.length === 0) return null;
  try {
    const parsed: unknown = JSON.parse(data.join("\n"));
    if (typeof parsed !== "object" || parsed === null || Array.isArray(parsed)) return null;
    return { event, data: parsed as Record<string, unknown> };
  } catch {
    return null;
  }
}
