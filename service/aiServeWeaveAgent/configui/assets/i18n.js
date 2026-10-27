/* Static-key UI translation; runtime identities and form values stay untouched.
 * 按静态键翻译界面，保留运行时标识与表单值。 */
(() => {
  const bootstrap = JSON.parse(document.getElementById("aisw-i18n-state").textContent);
  const allowed = ["zh-CN", "zh-TW", "en-US", "ja-JP", "ko-KR", "de-DE", "fr-FR", "es-ES"];
  const states = { registering: "a0097", unknown: "a0098", healthy: "a0099", unhealthy: "a0100", closed: "a0101" };
  let locale = bootstrap.locale, messages = bootstrap.messages, generation = 0;
  const cache = new Map([[locale, messages]]);
  function text(key, values = {}) {
    const phrase = messages[key];
    if (typeof phrase !== "string") throw new Error("Unknown UI translation key");
    return phrase.replace(/\{([a-zA-Z]\w*)\}/g, (_, name) => Object.hasOwn(values, name) ? String(values[name]) : "");
  }
  function apply(root) {
    const elements = [...(root.matches?.("[data-i18n]") ? [root] : []), ...root.querySelectorAll("[data-i18n]")];
    for (const element of elements) {
      const values = JSON.parse(element.dataset.i18nValues || "{}");
      if (element.dataset.runtimeState) values.state = states[element.dataset.runtimeState] ? text(states[element.dataset.runtimeState]) : element.dataset.runtimeState;
      if (element.dataset.i18n === "a0083") values.error = text("a0102");
      element.textContent = text(element.dataset.i18n, values);
    }
    for (const attr of ["aria-label", "placeholder", "label"]) {
      for (const element of root.querySelectorAll(`[data-i18n-${attr}]`)) element.setAttribute(attr, text(element.getAttribute(`data-i18n-${attr}`)));
    }
    for (const template of root.querySelectorAll("template")) apply(template.content);
  }
  function message(element, key, values = {}) {
    element.dataset.i18n = key;
    element.dataset.i18nValues = JSON.stringify(values);
    element.textContent = text(key, values);
  }
  async function select(next) {
    if (!allowed.includes(next)) return;
    const operation = ++generation;
    const selector = document.getElementById("aisw-locale");
    try {
      let nextMessages = cache.get(next);
      if (!nextMessages) {
        const response = await fetch(`/i18n/${next}.json`, { cache: "no-cache" });
        if (!response.ok) throw new Error("Language resource unavailable");
        nextMessages = await response.json();
        if (Object.keys(messages).some(key => typeof nextMessages[key] !== "string" || !nextMessages[key])) throw new Error("Language resource incomplete");
        cache.set(next, nextMessages);
      }
      if (operation !== generation) return;
      locale = next; messages = nextMessages;
      document.cookie = `aisw_agent_locale=${locale}; Path=/; Max-Age=31536000; SameSite=Lax${location.protocol === "https:" ? "; Secure" : ""}`;
      document.documentElement.lang = locale;
      apply(document);
      document.getElementById("locale-status").textContent = "";
      document.dispatchEvent(new Event("aisw:locale"));
    } catch {
      if (operation === generation) {
        selector.value = locale;
        message(document.getElementById("locale-status"), "a0105");
      }
    }
  }
  window.AISWI18n = { text, apply, message, select };
  document.getElementById("aisw-locale").addEventListener("change", event => { void select(event.target.value); });
  document.querySelector("form").addEventListener("submit", event => {
    if (event.submitter?.value === "locale") { event.preventDefault(); void select(document.getElementById("aisw-locale").value); }
  });
})();
