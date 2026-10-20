import assert from "node:assert/strict";
import fs from "node:fs/promises";
import test from "node:test";

async function fixture() {
  const source=(await fs.readFile(new URL("./bridge.js",import.meta.url),"utf8")).replace(/^import .*;$/gm,"");
  const calls=[];let extension,bridge;
  const app={registerExtension(value){extension=value;},ui:{settings:{async setSettingValueAsync(key,value){calls.push([key,value]);}}},async graphToPrompt(){calls.push(["export"]);return {workflow:{nodes:[]},output:{}};}};
  const api={addEventListener(){},async getNodeDefs(){return {};}};
  const window={$wujie:{props:{locale:"de",onReady(value){bridge=value;}}}};
  const setEditorLocale=async locale=>{calls.push(["activate",locale]);};
  new Function("app","api","window","setEditorLocale",source)(app,api,window,setEditorLocale);
  await extension.setup();return {bridge,calls};
}

test("bridge waits for initial locale, rejects unknown tags and serializes exports",async()=>{
  const {bridge,calls}=await fixture();
  assert.equal(typeof bridge.setLocale,"function");
  assert.deepEqual(calls,[["Comfy.Locale","de"],["activate","de"]]);
  calls.length=0;
  await Promise.all([bridge.setLocale("ja"),bridge.exportGraph(),bridge.setLocale("fr")]);
  assert.deepEqual(calls,[["Comfy.Locale","ja"],["activate","ja"],["export"],["Comfy.Locale","fr"],["activate","fr"]]);
  await assert.rejects(bridge.setLocale("zh-CN"));
  await assert.rejects(bridge.setLocale({}));
});
