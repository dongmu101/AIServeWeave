import assert from "node:assert/strict";
import test from "node:test";

import { MAX_BODY_BYTES, readBoundedText } from "./responses.ts";

/**
 * countingBody builds a chunked request body and reports how much of it was
 * actually pulled.
 *
 * The counting is the point of these tests. Whether an oversized body is
 * refused is easy to get right and easy to get right for the wrong reason —
 * `request.text()` refuses it too, after buffering all of it. What has to be
 * asserted is that the reading stopped, and that is only visible from the
 * source's side.
 *
 * countingBody 构造一个分块的请求体，并报告它实际被拉取了多少。
 *
 * 这个计数正是这些测试的要点。「超大请求体会被拒绝」既容易做对，也容易因为错误的理由
 * 而做对——`request.text()` 同样会拒绝它，只不过是在把它整个缓冲下来之后。必须断言的是
 * 读取停止了，而那只能从源头一侧看到。
 */
function countingBody(chunkSize: number, chunks: number) {
  const state = { pulled: 0, cancelled: false };
  const stream = new ReadableStream<Uint8Array>(
    {
      pull(controller) {
        if (state.pulled >= chunks) {
          controller.close();
          return;
        }
        state.pulled += 1;
        controller.enqueue(new Uint8Array(chunkSize).fill(0x61));
      },
      cancel() {
        state.cancelled = true;
      },
    },
    // A queue of zero, so a chunk is produced only when somebody is reading.
    // With the default strategy the stream pulls one chunk ahead to fill its
    // queue, and the count would then include a chunk nobody asked for —
    // which would make "was anything read at all" untestable.
    //
    // 队列长度为零，因此只有在有人读取时才生产一个分块。用默认策略时，流会预先拉取一块
    // 来填满自己的队列，计数里就会包含一块没人索取的分块——那会让「到底有没有被读过」
    // 变得无法测试。
    new CountQueuingStrategy({ highWaterMark: 0 })
  );
  return { stream, state };
}

/**
 * chunked builds a request whose body is a stream and which declares no
 * Content-Length — the shape the bound has to survive.
 *
 * It is a stand-in rather than a real `Request`, and the reason matters.
 * Constructing an undici `Request` from a stream starts pumping that stream
 * into an internal buffer of its own, because that object models an outgoing
 * request being prepared for a socket. A route handler's `Request` is the
 * opposite: its body is the incoming socket, read only when somebody reads it.
 * Wrapping the stream in an undici Request here would measure undici's pump
 * rather than this function, and would report a body as fully drained no
 * matter what this function did.
 *
 * The stand-in provides exactly what readBoundedText consumes — the headers
 * and the body — so what is asserted is this function's own behaviour.
 *
 * chunked 构造一个请求体为流、且不声明 Content-Length 的请求——上限必须能在这种形态下
 * 站得住。
 *
 * 它是一个替身而不是真正的 `Request`，而理由是要紧的。用一个流去构造 undici 的
 * `Request`，会让它开始把那个流抽进自己的内部缓冲区，因为那个对象模拟的是一个正在为
 * socket 准备的出站请求。route handler 拿到的 `Request` 恰好相反：它的请求体就是入站的
 * socket，只有在有人读它时才被读。在这里用 undici Request 包住这个流，度量到的会是
 * undici 的抽取而不是本函数，而且无论本函数怎么做，都会报告请求体已被完全抽干。
 *
 * 这个替身只提供 readBoundedText 真正消费的东西——请求头与请求体——因此被断言的是本函数
 * 自己的行为。
 */
function chunked(
  stream: ReadableStream<Uint8Array>,
  headers: Record<string, string> = {}
): Request {
  return {
    headers: new Headers(headers),
    body: stream,
  } as unknown as Request;
}

test("a body over the bound is refused without being read to the end", async () => {
  // Sixteen kibibyte chunks against a 64 KiB bound: four chunks reach it and
  // the fifth goes over, so a correct implementation pulls five of the 512 on
  // offer. The old one pulled all 512 — eight mebibytes into memory — and then
  // measured what it had.
  //
  // 16 KiB 的分块对 64 KiB 的上限：四块到达上限，第五块越过它，因此正确的实现会从
  // 提供的 512 块中拉取 5 块。旧实现会把 512 块全部拉完——八兆字节进内存——然后再去
  // 度量自己手上有多少。
  const chunkSize = 16 * 1024;
  const chunks = 512;
  const { stream, state } = countingBody(chunkSize, chunks);

  const result = await readBoundedText(chunked(stream));

  assert.equal(result, null, "an oversized body must be refused");
  const expectedPulls = Math.floor(MAX_BODY_BYTES / chunkSize) + 1;
  assert.equal(
    state.pulled,
    expectedPulls,
    `reading stopped after ${state.pulled} chunks, want ${expectedPulls}`
  );
  assert.ok(
    state.pulled * chunkSize < chunkSize * chunks,
    "the whole body was pulled despite the bound"
  );
  assert.equal(state.cancelled, true, "the source was not told to stop");
});

test("one oversized chunk is refused on its own", async () => {
  // The bound must hold when a single chunk is larger than it, which is the
  // case a per-chunk check written as "stop after N chunks" would miss.
  //
  // 当单个分块本身就大于上限时，这条限制也必须成立——那正是写成「N 块之后停止」的
  // 逐块检查会漏掉的情形。
  const { stream, state } = countingBody(MAX_BODY_BYTES * 4, 8);

  assert.equal(await readBoundedText(chunked(stream)), null);
  assert.equal(state.pulled, 1, "more than one oversized chunk was pulled");
  assert.equal(state.cancelled, true);
});

test("a declared Content-Length over the bound is refused before any read", async () => {
  const { stream, state } = countingBody(1024, 8);
  const request = chunked(stream, { "content-length": String(MAX_BODY_BYTES + 1) });

  assert.equal(await readBoundedText(request), null);
  assert.equal(state.pulled, 0, "the body was read despite an oversized Content-Length");
});

test("a body within the bound is returned whole", async () => {
  const cases: { name: string; size: number }[] = [
    { name: "an ordinary body", size: 32 },
    { name: "one byte under the bound", size: MAX_BODY_BYTES - 1 },
    // Exactly at the bound is accepted: the limit is what may be sent, not
    // what must be undershot.
    //
    // 恰好等于上限是接受的：这条限制说的是「可以发送多少」，不是「必须少于多少」。
    { name: "exactly at the bound", size: MAX_BODY_BYTES },
  ];

  for (const item of cases) {
    const encoded = new TextEncoder().encode("a".repeat(item.size));
    const stream = new ReadableStream<Uint8Array>({
      start(controller) {
        controller.enqueue(encoded);
        controller.close();
      },
    });
    assert.equal((await readBoundedText(chunked(stream)))?.length, item.size, item.name);
  }
});

test("a multi-byte character split across chunks survives", async () => {
  // The decoder is streaming for this reason. Decoding each chunk on its own
  // would turn one character into two replacement characters, and the JSON
  // parse downstream would fail on a body that was perfectly valid.
  //
  // 解码器之所以是流式的正是因为这个。各自独立解码每个分块，会把一个字符变成两个
  // 替换字符，而下游的 JSON 解析会在一个本来完全合法的请求体上失败。
  const encoded = new TextEncoder().encode('{"name":"工作流"}');
  const stream = new ReadableStream<Uint8Array>({
    start(controller) {
      // Split mid-character: the first Chinese character starts at byte 10.
      //
      // 在字符中间切开：第一个中文字符从第 10 字节开始。
      controller.enqueue(encoded.slice(0, 11));
      controller.enqueue(encoded.slice(11));
      controller.close();
    },
  });

  assert.equal(await readBoundedText(chunked(stream)), '{"name":"工作流"}');
});

test("a request with no body reads as empty rather than failing", async () => {
  // A real DELETE arrives with a null body, which is not the same as an empty
  // one: reading it as a failure would break sign-out.
  //
  // 真实的 DELETE 到达时 body 为 null，那与「空请求体」不是一回事：把它读成失败会让
  // 退出登录失效。
  const request = { headers: new Headers(), body: null } as unknown as Request;
  assert.equal(await readBoundedText(request), "");
});
