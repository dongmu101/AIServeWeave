/**
 * The model a new conversation starts with: the user's last choice while it
 * is still offered, else the first model in catalog order. The choice is a
 * per-browser convenience kept in localStorage; losing it only resets the
 * picker.
 *
 * 新对话默认使用的模型：用户上次的选择（只要它仍在列表里），否则取目录顺序中的第一个。
 * 这个选择是保存在 localStorage 里的单浏览器便利项，丢失只会让选择器回到默认。
 */

export const MODEL_PREFERENCE_KEY = "aisw-studio:model";

export function pickModel(models: { id: string }[], stored: string | null): string {
  if (stored && models.some((model) => model.id === stored)) return stored;
  return models[0]?.id ?? "";
}

/** chooseModel picks the first candidate still offered, else the first
 * model. A conversation passes its own model before the stored preference,
 * so continuing it keeps the model it was using.
 *
 * chooseModel 取第一个仍在列表中的候选，否则取第一个模型。对话会把自己的模型放在已存
 * 偏好之前，因此继续对话时沿用它原来的模型。 */
export function chooseModel(models: { id: string }[], ...candidates: Array<string | null | undefined>): string {
  for (const candidate of candidates) {
    if (candidate && models.some((model) => model.id === candidate)) return candidate;
  }
  return models[0]?.id ?? "";
}
