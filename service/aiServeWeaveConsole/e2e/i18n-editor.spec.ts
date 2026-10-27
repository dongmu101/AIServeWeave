import {type Frame, type Page} from "@playwright/test";
import {test, expect} from "./fixtures";
import {LOCALES} from "../lib/i18n/locale";

type ProbeNode = {id: string | number; title: string; getTitle(): string; clone(): ProbeNode; pos: number[]; size: number[]};
type Probe = typeof globalThis & {
  __aiswBridge?: {setLocale(locale: string): Promise<void>; exportGraph(): Promise<string>; loadApi(text: string): Promise<void>};
  __aiswApp?: {graph: { _nodes: ProbeNode[]}};
  __aiswChanges?: number;
};
const graph = {
  "1": {class_type: "CheckpointLoaderSimple", inputs: {ckpt_name: "fixture.safetensors"}},
  "2": {class_type: "CLIPTextEncode", inputs: {text: "用户输入 / custom prompt", clip: ["1", 1]}},
  "3": {class_type: "CLIPTextEncode", inputs: {text: "explicit title", clip: ["1", 1]}, _meta: {title: "User title 用户标题"}},
  "4": {class_type: "CLIPTextEncode", inputs: {text: "explicit default", clip: ["1", 1]}, _meta: {title: "CLIP Text Encode (Prompt)"}},
};
const displayTitles = {"zh-CN":"CLIP文本编码", "zh-TW":"CLIP 文字編碼（提示詞）", "en-US":"CLIP Text Encode (Prompt)", "ja-JP":"CLIPテキストエンコード（プロンプト）", "ko-KR":"CLIP 텍스트 인코딩 (프롬프트)", "de-DE":"CLIP-Textkodierung (Prompt)", "fr-FR":"CLIP Text Encode (Prompt)", "es-ES":"Codificar Texto CLIP (Prompt)"};

async function openEditor(page: Page, locale = "zh-CN"): Promise<Frame> {
  await page.context().addCookies([{name: "aisw_console_locale", value: locale, url: "http://127.0.0.1:3100"}]);
  // Observe the actual bridge; none of its methods or return values are replaced.
  // 只观察真实桥接，不替换任何方法及返回值。
  await page.route("**/extensions/aiserveweave/bridge.js", async route => {
    const response = await route.fetch();
    const observe = `const p=window.$wujie.props;const ready=p.onReady,change=p.onChange;p.onReady=b=>{window.__aiswBridge=b;ready(b)};p.onChange=()=>{window.__aiswChanges=(window.__aiswChanges||0)+1;change()};window.__aiswApp=app;\n`;
    await route.fulfill({response, body: observe + await response.text()});
  });
  await page.goto("/operator/workflow-templates");
  await page.getByRole("button", {name: /P10_.* · v1/}).click();
  await expect(page.locator("textarea").first()).toHaveValue(/synthetic input/);
  await page.locator("textarea").first().fill(JSON.stringify(graph));
  await page.locator("details summary").filter({hasText: /ComfyUI/}).first().click();
  let found: Frame | undefined;
  await expect.poll(async () => {
    for (const frame of page.frames()) if (await frame.evaluate(() => Boolean((globalThis as Probe).__aiswBridge)).catch(() => false)) {found=frame;return true;}
    return false;
  }, {timeout: 30_000}).toBe(true);
  const frame = found!;
  await expect.poll(() => frame.evaluate(() => (globalThis as Probe).__aiswApp?.graph._nodes.length)).toBe(4);
  await frame.evaluate(async () => { await (globalThis as Probe).__aiswBridge!.exportGraph(); });
  return frame;
}

test("real editor switches all eight languages without changing the graph or edits", async ({page}) => {
  let definitionReads = 0;
  page.on("request", request => { if (new URL(request.url()).pathname.endsWith("/api/object_info")) definitionReads++; });
  const frame = await openEditor(page);
  const initial = await frame.evaluate(async () => (globalThis as Probe).__aiswBridge!.exportGraph());
  const changes = await frame.evaluate(() => (globalThis as Probe).__aiswChanges ?? 0);
  const reads = definitionReads;
  const titles: string[] = [];
  for (const locale of LOCALES) {
    await page.locator("[data-language-switcher]").selectOption(locale);
    await expect(page.locator("html").first()).toHaveAttribute("lang", locale);
    const code = {"zh-CN":"zh", "zh-TW":"zh-TW", "en-US":"en", "ja-JP":"ja", "ko-KR":"ko", "de-DE":"de", "fr-FR":"fr", "es-ES":"es"}[locale];
    await expect(page.locator("html").nth(1)).toHaveAttribute("lang", code);
    await expect.poll(() => frame.evaluate(() => (globalThis as Probe).__aiswApp!.graph._nodes.find(n=>String(n.id)==="2")!.getTitle())).toBe(displayTitles[locale]);
    const state = await frame.evaluate(async () => {
      const probe=globalThis as Probe;
      const nodes=probe.__aiswApp!.graph._nodes;
      return {output:await probe.__aiswBridge!.exportGraph(), changes:probe.__aiswChanges ?? 0, titles:nodes.map(n=>n.getTitle()), cloneTitle:nodes.find(n=>String(n.id)==="2")!.clone().title};
    });
    const normalize = (text: string) => {const o=JSON.parse(text);if(o.workflow.extra) delete o.workflow.extra.ds;return o;};
    expect(normalize(state.output)).toEqual(normalize(initial));
    expect(state.changes).toBe(changes);
    expect(definitionReads).toBe(reads);
    expect(page.frames()).toContain(frame);
    expect(state.output).not.toContain("aiswTitleExplicit");
    expect(state.titles[2]).toBe("User title 用户标题");
    expect(state.titles[3]).toBe("CLIP Text Encode (Prompt)");
    expect(state.cloneTitle).toBe("CLIP Text Encode (Prompt)");
    expect(JSON.parse(state.output).workflow.nodes.find((n: {id:number})=>String(n.id)==="4").title).toBe("CLIP Text Encode (Prompt)");
    expect(JSON.parse(state.output).workflow.nodes.find((n: {id:number})=>String(n.id)==="2").title).toBeUndefined();
    titles.push(state.titles[1]);
  }
  expect(new Set(titles).size).toBeGreaterThan(4);
});

test("locale selected before mount and overlapping exports keep a single editor", async ({page}) => {
  const frame = await openEditor(page, "de-DE");
  await expect.poll(() => frame.evaluate(() => (globalThis as Probe).__aiswApp!.graph._nodes.find(n=>String(n.id)==="2")!.getTitle())).toBe(displayTitles["de-DE"]);
  const frames = page.frames().length;
  const result = await frame.evaluate(async () => {
    const b=(globalThis as Probe).__aiswBridge!;
    const [before,,during]=await Promise.all([b.exportGraph(), b.setLocale("fr"), b.exportGraph()]);
    return [before,during,await b.exportGraph()];
  });
  const normalize = (text: string) => {const o=JSON.parse(text);if(o.workflow.extra) delete o.workflow.extra.ds;return o;};
  expect(result.map(normalize)).toEqual([normalize(result[0]),normalize(result[0]),normalize(result[0])]);
  expect(page.frames().length).toBe(frames);
});
