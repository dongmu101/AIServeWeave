import assert from "node:assert/strict";
import fs from "node:fs/promises";
import os from "node:os";
import path from "node:path";
import {createHash} from "node:crypto";
import test from "node:test";
import {applyLocalization,writeArtifactManifest} from "./apply.mjs";

const sha=value=>createHash("sha256").update(value).digest("hex");
const production=JSON.parse(await fs.readFile(new URL("./manifest.json",import.meta.url)));
async function fixture(t){
 const root=await fs.mkdtemp(path.join(os.tmpdir(),"aisw-i18n-source-test-"));t.after(()=>fs.rm(root,{recursive:true,force:true}));
 const source=path.join(root,"source"),resources=path.join(root,"resources");await fs.mkdir(resources,{recursive:true});
 const config=structuredClone(production);config.source.files={};config.resources={};config.supplements=[];config.patches=["locale-config.patch"];
 const write=async(base,file,data)=>{await fs.mkdir(path.dirname(path.join(base,file)),{recursive:true});await fs.writeFile(path.join(base,file),data);};
 const originals={"package.json":JSON.stringify({version:production.frontend_version}),"src/locales/localeConfig.ts":"export const localeDefinitions = {\n  en: { text: 'English', loaders: null },\n}\n"};
 for(const[file,data]of Object.entries(originals)){await write(source,file,data);config.source.files[file]=sha(data);}
 for(const locale of Object.values(config.locales))for(const group of config.groups){
  const data=JSON.stringify({label:"Hello {name}",empty:""});await write(source,`src/locales/${locale}/${group}.json`,data);
  if(locale==="de"){const file=`de/${group}.json`;await write(resources,file,data);config.resources[file]=sha(data);}
 }
 const patch="diff --git a/src/locales/localeConfig.ts b/src/locales/localeConfig.ts\n--- a/src/locales/localeConfig.ts\n+++ b/src/locales/localeConfig.ts\n@@ -1,3 +1,4 @@\n export const localeDefinitions = {\n   en: { text: 'English', loaders: null },\n+  de: { text: 'Deutsch', loaders: loadersFor('de') },\n }\n";
 await write(resources,"locale-config.patch",patch);config.resources["locale-config.patch"]=sha(patch);
 const save=()=>write(resources,"manifest.json",JSON.stringify(config));await save();return {source,resources,config,save,write};
}
test("fixed source and complete resources apply only once",async t=>{
 const f=await fixture(t);await applyLocalization(f.source,f.resources);
 assert.match(await fs.readFile(path.join(f.source,"src/locales/localeConfig.ts"),"utf8"),/Deutsch/);
 await assert.rejects(applyLocalization(f.source,f.resources),/source|hash|digest/i);
});
for(const name of ["version","source digest","missing German bundle","interpolation","tag commit"]){test(`rejects ${name} before patching`,async t=>{
 const f=await fixture(t);
 if(name==="version")await f.write(f.source,"package.json",JSON.stringify({version:"99.0.0"}));
 if(name==="source digest")await f.write(f.source,"src/locales/localeConfig.ts","changed");
 if(name==="missing German bundle")await fs.unlink(path.join(f.resources,"de/main.json"));
 if(name==="interpolation"){const text=JSON.stringify({label:"Hallo {wrong}",empty:""});await f.write(f.resources,"de/main.json",text);f.config.resources["de/main.json"]=sha(text);await f.save();}
 if(name==="tag commit"){f.config.source.commit="0".repeat(40);await f.save();}
 await assert.rejects(applyLocalization(f.source,f.resources));
 assert.doesNotMatch(await fs.readFile(path.join(f.source,"src/locales/localeConfig.ts"),"utf8"),/Deutsch/);
});}

test("artifact manifest hashes every output and excludes itself",async t=>{
 const f=await fixture(t);const dist=path.join(f.source,"dist");
 await f.write(dist,"index.html","<html>editor</html>");await f.write(dist,"assets/de.js","export default 'Deutsch'");
 await writeArtifactManifest(dist);
 const manifest=JSON.parse(await fs.readFile(path.join(dist,"aisw-i18n-manifest.json")));
 assert.equal(manifest.source_commit,production.source.commit);
 assert.deepEqual(manifest.locales,production.locales);
 assert.deepEqual(manifest.files,{"assets/de.js":sha("export default 'Deutsch'"),"index.html":sha("<html>editor</html>")});
 await writeArtifactManifest(dist);
 assert.deepEqual(JSON.parse(await fs.readFile(path.join(dist,"aisw-i18n-manifest.json"))),manifest);
 await f.write(dist,"assets/de.js.map","{}");
 await assert.rejects(writeArtifactManifest(dist),/Source maps/);
});
