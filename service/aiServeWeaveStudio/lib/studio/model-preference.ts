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
