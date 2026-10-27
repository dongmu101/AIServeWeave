import assert from "node:assert/strict";
import fs from "node:fs";
import test from "node:test";
import { translate, localizeSpecText, createLocalizationPlugin } from "./swagger-i18n.mjs";

const read = locale => JSON.parse(fs.readFileSync(new URL(`./i18n/${locale}.json`, import.meta.url)));
test("fixed labels and named values translate without interpreting HTML", () => {
 assert.equal(translate("ui.authorize", {}, read("zh-CN")), "授权");
 assert.throws(() => translate("missing", {}, read("en-US")));
});
test("OAS display projection preserves the original object and all machine fields", () => {
 const spec={openapi:"3.0.3",info:{title:"AIServeWeave Gateway API"},servers:[{url:"http://localhost:8080",description:"公开推理前门（默认端口，可通过 -addr 配置）"}],paths:{"/v1/models":{get:{summary:"列出当前至少一个已连接节点声明支持 chat 能力的模型",responses:{"200":{description:"模型列表"}}}}},components:{schemas:{Machine:{type:"string",enum:["Authorize"],default:"Try it out",example:"Execute"}}}};
 const copy=structuredClone(spec),localized=localizeSpecText(spec,read("en-US"));
 assert.deepEqual(spec,copy);
 assert.notEqual(localized.paths["/v1/models"].get.summary,spec.paths["/v1/models"].get.summary);
 assert.equal(localized.servers[0].url,spec.servers[0].url);
 assert.deepEqual(localized.components,spec.components);
 const custom=structuredClone(spec);custom.paths["/v1/models"].get.summary="作者说明";
 assert.equal(localizeSpecText(custom,read("de-DE")).paths["/v1/models"].get.summary,"作者说明");
});
test("the plugin has one stable installation independent of display changes", () => {
 const plugin=createLocalizationPlugin(()=>({locale:"en-US",catalog:read("en-US")}));
 assert.equal(typeof plugin,"function");
});

test("catalogues cover every allowlisted phrase with identical named parameters", async () => {
 const { SOURCES } = await import("./source.mjs");
 const parameters = value => [...value.matchAll(/\{([a-zA-Z]\w*)\}/g)].map(match=>match[1]).sort();
 for (const locale of ["zh-CN","zh-TW","en-US","ja-JP","ko-KR","de-DE","fr-FR","es-ES"]) {
  const messages = read(locale);
  for (const entry of SOURCES) {
   assert.equal(typeof messages[entry.id],"string",`${locale}:${entry.id}`);
   assert.ok(messages[entry.id].trim(),`${locale}:${entry.id}`);
   assert.deepEqual(parameters(messages[entry.id]),parameters(entry.source),`${locale}:${entry.id}`);
  }
 }
});

test("class adapters preserve their type, draft state, handlers and machine/code values across locales", async () => {
 const { localizeElementTree } = await import("./swagger-i18n.mjs");
 const React = {
  isValidElement: element => !!element?.props,
  cloneElement: (element,props) => ({...element,props:{...element.props,...props}}),
  createElement: (type,props,...children) => ({type,props:{...props,children},key:props?.key,ref:props?.ref}),
 };
 class Original {
  constructor() { this.state={draft:"kept"}; }
  render() { return React.createElement("button",{onClick:handler,key:"stable",ref:"stable-ref"},"Authorize"); }
  forceUpdate() { this.updated=true; }
 }
 Original.prototype.isReactComponent = true;
 const handler = ()=>{};
 let state={locale:"en-US",catalog:read("en-US")},listener;
 const get=()=>state;get.subscribe=fn=>{listener=fn;return()=>{listener=null;};};
 const plugin=createLocalizationPlugin(get)({React,getComponents:()=>({authorizeBtn:[Original]}),specSelectors:{}});
 const Type=plugin.components.authorizeBtn,instance=new Type();instance.componentDidMount();
 state={locale:"zh-CN",catalog:read("zh-CN")};listener();
 const rendered=instance.render();assert.equal([].concat(rendered.props.children).join(""),"授权");
 assert.deepEqual(instance.state,{draft:"kept"});assert.equal(instance.constructor,Type);assert.equal(rendered.props.onClick,handler);assert.equal(rendered.key,"stable");assert.equal(rendered.ref,"stable-ref");
 for (const tree of [React.createElement("input",{value:"Authorize",defaultValue:"Execute"}),React.createElement("code",null,"Authorize"),React.createElement("span",{className:"parameter__name"},"Authorize")]) {
  assert.deepEqual(localizeElementTree(React,tree,state),tree);
 }
 instance.componentWillUnmount();assert.equal(listener,null);
});

test("enum options keep literal values even when they equal a translated control label", async () => {
 const { localizeElementTree } = await import("./swagger-i18n.mjs");
 const React={isValidElement:element=>!!element?.props,cloneElement:(element,props)=>({...element,props:{...element.props,...props}})};
 const option={type:"option",props:{value:"Schema",children:"Schema"}};
 assert.deepEqual(localizeElementTree(React,option,{locale:"zh-CN",catalog:read("zh-CN")}),option);
});

test("unknown operation summaries retain author text that resembles a UI label", async () => {
 const { localizeElementTree } = await import("./swagger-i18n.mjs");
 const React={isValidElement:element=>!!element?.props,cloneElement:(element,props)=>({...element,props:{...element.props,...props}})};
 const summary={type:"div",props:{className:"opblock-summary-description",children:"Authorize"}};
 assert.deepEqual(localizeElementTree(React,summary,{locale:"zh-CN",catalog:read("zh-CN")}),summary);
});

test("fixed bundle and source hashes require an explicit localization audit on update", async () => {
 const { createHash } = await import("node:crypto");
 const { SPEC_SHA256,BUNDLE_SHA256 } = await import("./source.mjs");
 for (const [path,expected] of [["../openapi.yaml",SPEC_SHA256],["./swagger-ui-bundle.js",BUNDLE_SHA256]]) assert.equal(createHash("sha256").update(fs.readFileSync(new URL(path,import.meta.url))).digest("hex"),expected,path);
});

test("bare registered components receive the same stable adapter as wrapped registration stacks", () => {
 const React={isValidElement:element=>!!element?.props,cloneElement:(element,props)=>({...element,props:{...element.props,...props}})};
 class Original { render(){return {type:"button",props:{children:"Authorize"}};} }
 Original.prototype.isReactComponent=true;
 const installed=createLocalizationPlugin(()=>({locale:"zh-CN",catalog:read("zh-CN")}))({React,getComponents:()=>({authorizeBtn:Original}),specSelectors:{}});
 assert.equal(typeof installed.components.authorizeBtn,"function");
 assert.equal(new installed.components.authorizeBtn().render().props.children,"授权");
});

test("response header data and OAuth scopes retain literal control-like names", async () => {
 const {localizeElementTree}=await import("./swagger-i18n.mjs");
 const React={isValidElement:element=>!!element?.props,cloneElement:(element,props)=>({...element,props:{...element.props,...props}})};
 const state={locale:"zh-CN",catalog:read("zh-CN")};
 for (const name of ["Name","Type","Authorize"]) {
  const header={type:"td",props:{className:"header-col",children:name}};
  assert.deepEqual(localizeElementTree(React,header,state,false,false,false,"headers"),header,`header ${name}`);
  for(const className of ["name","description"]){
   const scope={type:"p",props:{className,children:name}};
   assert.deepEqual(localizeElementTree(React,scope,state,false,false,false,"oauth2"),scope,`scope ${className} ${name}`);
  }
 }
 const button={type:"button",props:{children:"Authorize"}};
 assert.equal(localizeElementTree(React,button,state,false,false,false,"oauth2").props.children,"授权");
 const heading={type:"th",props:{className:"header-col",children:"Name"}};
 assert.notEqual(localizeElementTree(React,heading,state,false,false,false,"headers").props.children,"Name");
});
