/**
 * Model families group model aliases by the vendor line they belong to, for
 * display only: billing never depends on them. A family is inferred from the
 * alias name; an alias nothing matches falls into "other".
 *
 * 模型系列按所属厂商产品线对模型别名分组，只用于展示，计费从不依赖它。系列由别名推断；
 * 什么都匹配不上的别名归入 "other"。
 */

/** ModelFamily is one display group. `mark` is the short glyph drawn in its
 * icon and `color` the icon's background.
 *
 * ModelFamily 是一个展示分组。`mark` 是图标里的短字符，`color` 是图标底色。 */
export interface ModelFamily {
  id: string;
  name: string;
  vendor: string;
  mark: string;
  color: string;
  /** Matches the alias's last path segment, lower-cased. / 匹配别名小写后的最后一段。 */
  model: RegExp;
  /** Matches a leading provider segment such as `anthropic/`. / 匹配 `anthropic/` 这类前缀段。 */
  provider: RegExp;
}

/** OTHER_FAMILY is the id of the group for aliases no family matches.
 * OTHER_FAMILY 是匹配不上任何系列的别名所在分组的 id。 */
export const OTHER_FAMILY = "other";

/** MODEL_FAMILIES are the known families, in display order.
 * MODEL_FAMILIES 是已知系列，按展示顺序排列。 */
export const MODEL_FAMILIES: readonly ModelFamily[] = [
  { id: "claude", name: "Claude", vendor: "Anthropic", mark: "A", color: "#D97757", model: /^claude/, provider: /^anthropic$/ },
  { id: "openai", name: "GPT", vendor: "OpenAI", mark: "O", color: "#10A37F", model: /^(chatgpt|gpt|o\d+(-|$)|codex|dall-e|whisper|tts-|text-embedding|omni-moderation|sora)/, provider: /^(openai|azure)$/ },
  { id: "gemini", name: "Gemini", vendor: "Google", mark: "G", color: "#4285F4", model: /^(gemini|gemma|imagen|veo)/, provider: /^(google|gemini|vertex(_ai)?)$/ },
  { id: "deepseek", name: "DeepSeek", vendor: "DeepSeek", mark: "D", color: "#4D6BFE", model: /^deepseek/, provider: /^deepseek$/ },
  { id: "qwen", name: "Qwen", vendor: "Alibaba", mark: "Q", color: "#615CED", model: /^(qwen|qwq|qvq|wan\d)/, provider: /^(qwen|alibaba|dashscope)$/ },
  { id: "glm", name: "GLM", vendor: "Zhipu AI", mark: "Z", color: "#3859FF", model: /^(glm|chatglm|cogview|cogvideo)/, provider: /^(zhipu(ai)?|z-?ai|bigmodel)$/ },
  { id: "kimi", name: "Kimi", vendor: "Moonshot AI", mark: "K", color: "#16191E", model: /^(kimi|moonshot)/, provider: /^moonshot(ai)?$/ },
  { id: "doubao", name: "Doubao", vendor: "ByteDance", mark: "D", color: "#1E37FC", model: /^(doubao|seed)/, provider: /^(bytedance|volcengine|doubao)$/ },
  { id: "grok", name: "Grok", vendor: "xAI", mark: "X", color: "#111111", model: /^grok/, provider: /^x-?ai$/ },
  { id: "minimax", name: "MiniMax", vendor: "MiniMax", mark: "M", color: "#E2167E", model: /^(minimax|abab)/, provider: /^minimax$/ },
  { id: "llama", name: "Llama", vendor: "Meta", mark: "M", color: "#0866FF", model: /^(llama|codellama)/, provider: /^(meta|meta-llama)$/ },
  { id: "mistral", name: "Mistral", vendor: "Mistral AI", mark: "M", color: "#FA520F", model: /^(mistral|mixtral|ministral|magistral|codestral|devstral|pixtral)/, provider: /^mistral(ai)?$/ },
  { id: "hunyuan", name: "Hunyuan", vendor: "Tencent", mark: "H", color: "#0052D9", model: /^hunyuan/, provider: /^(tencent|hunyuan)$/ },
  { id: "ernie", name: "ERNIE", vendor: "Baidu", mark: "E", color: "#2932E1", model: /^ernie/, provider: /^(baidu|qianfan)$/ },
];

/** modelFamily returns the family an alias belongs to, or null for "other".
 * The last path segment is matched first, so `openrouter/claude-x` is Claude;
 * the leading segments are tried only when it matches nothing.
 *
 * modelFamily 返回别名所属系列；归入「其他」时返回 null。先匹配最后一段，所以
 * `openrouter/claude-x` 属于 Claude；最后一段匹配不上时才尝试前面的段。 */
export function modelFamily(model: string): ModelFamily | null {
  const segments = model.toLowerCase().split("/");
  const last = segments[segments.length - 1];
  const byModel = MODEL_FAMILIES.find((family) => family.model.test(last));
  if (byModel) return byModel;
  for (const segment of segments.slice(0, -1)) {
    const byProvider = MODEL_FAMILIES.find((family) => family.provider.test(segment));
    if (byProvider) return byProvider;
  }
  return null;
}

/** FamilyGroup is one family's items; `family` is null for "other".
 * FamilyGroup 是一个系列的条目；「其他」的 `family` 为 null。 */
export interface FamilyGroup<T> {
  id: string;
  family: ModelFamily | null;
  items: T[];
}

const collator = new Intl.Collator("en", { numeric: true, sensitivity: "base" });

/** groupByFamily groups items by the family of `modelOf(item)`: known
 * families in MODEL_FAMILIES order, "other" last, empty groups omitted; items
 * within a group in natural order of their model, so gpt-5 comes before
 * gpt-10.
 *
 * groupByFamily 按 `modelOf(item)` 的系列分组：已知系列按 MODEL_FAMILIES 顺序，「其他」
 * 放最后，空组省略；组内按模型名自然序排列，所以 gpt-5 在 gpt-10 之前。 */
export function groupByFamily<T>(items: readonly T[], modelOf: (item: T) => string): FamilyGroup<T>[] {
  const groups = new Map<string, FamilyGroup<T>>();
  for (const item of items) {
    const family = modelFamily(modelOf(item));
    const id = family?.id ?? OTHER_FAMILY;
    let group = groups.get(id);
    if (!group) {
      group = { id, family, items: [] };
      groups.set(id, group);
    }
    group.items.push(item);
  }
  const order = (id: string) => {
    const index = MODEL_FAMILIES.findIndex((family) => family.id === id);
    return index < 0 ? MODEL_FAMILIES.length : index;
  };
  const out = [...groups.values()].sort((a, b) => order(a.id) - order(b.id));
  for (const group of out) group.items.sort((a, b) => collator.compare(modelOf(a), modelOf(b)));
  return out;
}
