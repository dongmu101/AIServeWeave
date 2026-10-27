import { createLocalizationPlugin, translate } from "./swagger-i18n.mjs";

const initial = JSON.parse(document.getElementById("aisw-docs-state").textContent);
let state = Object.freeze(initial), generation = 0;
const listeners = new Set(), cache = new Map([[state.locale,state.catalog]]);
const selector = document.getElementById("aisw-docs-locale");
const allowed = [...selector.options].map(option=>option.value);
const getLocaleState = () => state;
getLocaleState.subscribe = listener => { listeners.add(listener); return ()=>listeners.delete(listener); };
function applyShell() {
  document.documentElement.lang = state.locale;
  document.title = state.catalog["oas/info/title"];
  document.getElementById("aisw-language-label").textContent = state.catalog["ui.language"];
  document.documentElement.style.setProperty("--aisw-required",JSON.stringify(state.catalog["ui.required"] ?? "*"));
  document.documentElement.style.setProperty("--aisw-loading",JSON.stringify(state.catalog["ui.loading"]));
}
selector.addEventListener("change", async () => {
  const locale = selector.value, operation = ++generation;
  if (!allowed.includes(locale)) return;
  try {
    let catalog = cache.get(locale);
    if (!catalog) {
      const response = await fetch(`i18n/${locale}.json`,{cache:"no-cache"});
      if (!response.ok) throw new Error("Language resource unavailable");
      catalog = await response.json();
      if (Object.keys(initial.catalog).some(key=>typeof catalog[key]!=="string" || !catalog[key])) throw new Error("Incomplete language resource");
      cache.set(locale,catalog);
    }
    if (operation !== generation) return;
    state = Object.freeze({locale,catalog});
    document.cookie = `aisw_docs_locale=${locale}; Path=/; Max-Age=31536000; SameSite=Lax${location.protocol==="https:"?"; Secure":""}`;
    applyShell();
    document.getElementById("aisw-language-error").textContent = "";
    for (const listener of [...listeners]) listener();
  } catch {
    if (operation === generation) { selector.value = state.locale; document.getElementById("aisw-language-error").textContent = translate("ui.languageLoadFailed",{},state.catalog); }
  }
});
applyShell();
window.ui = SwaggerUIBundle({
  url:"/openapi.yaml",dom_id:"#swagger-ui",presets:[SwaggerUIBundle.presets.apis,SwaggerUIStandalonePreset],
  validatorUrl:null,plugins:[createLocalizationPlugin(getLocaleState)],layout:"StandaloneLayout",deepLinking:true,
});
