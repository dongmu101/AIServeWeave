import { controlledComponents } from "./controlled-components.mjs";
import { SOURCES } from "./source.mjs";

const controls = SOURCES.filter(entry => entry.kind === "control");
const explanations = SOURCES.filter(entry => entry.kind === "oas");

/** translate replaces named values as plain text. / translate 以纯文本替换具名参数。 */
export function translate(key, values, catalog) {
  const phrase = catalog[key];
  if (typeof phrase !== "string") throw new Error("Unknown UI translation key");
  return phrase.replace(/\{([a-zA-Z]\w*)\}/g, (_, name) => {
    if (!Object.hasOwn(values, name)) throw new Error("Missing UI translation parameter");
    return String(values[name]);
  });
}
function pointerParts(pointer) { return pointer.slice(1).split("/").map(part => part.replace(/~1/g,"/").replace(/~0/g,"~")); }
function at(object, parts) { return parts.reduce((value, key) => value?.[key], object); }

/** localizeSpecText projects only exact, allowlisted explanatory fields.
 * localizeSpecText 仅投影原文匹配且列入允许列表的说明字段。 */
export function localizeSpecText(spec, catalog) {
  const result = structuredClone(spec);
  for (const { id, source, pointer } of explanations) {
    const parts = pointerParts(pointer);
    if (at(spec, parts) !== source) continue;
    const parent = at(result, parts.slice(0,-1));
    parent[parts.at(-1)] = catalog[id];
  }
  return result;
}

const escaped = text => text.replace(/[.*+?^${}()|[\]\\]/g,"\\$&");
const patterns = controls.map(entry => {
  const names = [...entry.source.matchAll(/\{([a-zA-Z]\w*)\}/g)].map(match => match[1]);
  const expression = entry.source.split(/\{[a-zA-Z]\w*\}/).map(escaped).join("([\\s\\S]+?)");
  return { ...entry, names, pattern: new RegExp("^"+expression+"$") };
});
function fixedText(value, state, allowExplanation) {
  for (const entry of patterns) {
    const match = value.match(entry.pattern);
    if (match) return translate(entry.id, Object.fromEntries(entry.names.map((name,index)=>[name,match[index+1]])), state.catalog);
  }
  if (allowExplanation) {
    const entry = explanations.find(entry => entry.source === value);
    if (entry) return state.catalog[entry.id];
  }
  return value;
}

const displayClasses = new Set(["Models","schemes","JsonSchema_array","App","authorizationPopup","authorizeBtn","authorizeOperationBtn","auths","AuthItem","oauth2","apiKeyAuth","basicAuth","clear","liveResponse","info","InfoBasePath","Contact","CopyToClipboardBtn","operations","operation","responses","response","responseBody","parameters","parameterRow","execute","headers","errors","overview","FilterContainer","ParamBody","curl","TryItOutButton","BaseLayout","VersionPragmaFilter","ParameterIncludeEmpty","OperationTag","ExamplesSelect","HttpAuth","ServersContainer","OperationServers","operationLink","OAS31Auths","Topbar","onlineValidatorBadge","License","OperationSummary"]);
const attributeClasses = new Set(["JsonSchemaArrayItemFile","JsonSchemaArrayItemText","JsonSchema_boolean","JsonSchema_object","JsonSchema_string","RequestBodyEditor"]);
const protectedClasses = /(?:^|\s)(?:parameter__name|prop-name|property-name|model-title|opblock-tag|opblock-summary-path|base-url|version|microlight)(?:\s|$)/;

/** localizeElementTree preserves element identity, values, handlers and code subtrees.
 * localizeElementTree 保留元素身份、输入值、事件处理器与代码子树。 */
export function localizeElementTree(React, tree, state, allowExplanation = false, insideCopy = false, protectText = false, componentName = "") {
  if (typeof tree === "string") return protectText ? tree : fixedText(tree,state,allowExplanation);
  if (Array.isArray(tree)) {
    if (!protectText && tree.every(child=>typeof child === "string" || typeof child === "number")) {
      const phrase = tree.join("");
      const translated = fixedText(phrase,state,allowExplanation);
      if (translated !== phrase) return translated;
    }
    return tree.map(child=>localizeElementTree(React,child,state,allowExplanation,insideCopy,protectText,componentName));
  }
  if (!React.isValidElement(tree)) return tree;
  const props = tree.props;
  if (["pre","code"].includes(tree.type)) return tree;
  protectText ||= protectedClasses.test(props.className ?? "") || (tree.type === "option" && props.children === props.value);
  const next = {};
  for (const attr of ["title","aria-label","placeholder","ariaLabel"]) if (typeof props[attr] === "string") next[attr] = fixedText(props[attr],state,false);
  if (tree.type === "button" && insideCopy) { next.title = translate("ui.copyClipboard",{},state.catalog); next["aria-label"] = next.title; }
  const narrative = (props.className ?? "").split(" ").includes("opblock-summary-description") || (componentName === "info" && props.className === "title");
  if (narrative) {
    const project = child => {
      if (typeof child === "string") {
        const entry = allowExplanation && explanations.find(entry=>entry.source === child);
        return entry ? state.catalog[entry.id] : child;
      }
      return Array.isArray(child) ? child.map(project) : child;
    };
    return React.cloneElement(tree,{...next,children:project(props.children)});
  }
  if (props.children !== undefined && tree.type !== "textarea") next.children = localizeElementTree(React,props.children,state,allowExplanation,insideCopy || props.className === "copy-to-clipboard",protectText,componentName);
  return React.cloneElement(tree,next);
}

/** createLocalizationPlugin installs stable adapters before Swagger UI first mounts.
 * createLocalizationPlugin 在 Swagger UI 首次挂载前安装稳定的组件适配。 */
export function createLocalizationPlugin(getLocaleState) {
  return system => {
    const React = system.React;
    const subscribe = getLocaleState.subscribe ?? (()=>()=>{});
    const registry = system.getComponents();
    const components = {};
    const explanationText = value => {
      const entry = explanations.find(entry=>entry.source===value);
      return entry && ownedSpec() ? getLocaleState().catalog[entry.id] : value;
    };
    let lastSpec, owned = false;
    function ownedSpec() {
      const current = system.specSelectors?.specJson?.();
      if (current === lastSpec) return owned;
      lastSpec = current;
      const spec = current?.toJS?.() ?? current;
      owned = !!spec && explanations.every(entry => at(spec,pointerParts(entry.pointer)) === entry.source);
      return owned;
    }
    const directFunctions = new Set(["HighlightCode","RequestSnippets","Input","TextArea","Example","OperationExt","Callbacks","RequestBody","Fallback","MutualTLSAuth"]);
    const privateTypes = new WeakMap();
    function privateFragments(tree, parent) {
      if (Array.isArray(tree)) return tree.map(child=>privateFragments(child,parent));
      if (!React.isValidElement(tree)) return tree;
      const privateName = tree.type?.name;
      const allowed = parent === "liveResponse" ? ["WC","JC"] : parent === "errors" ? ["w3","E3"] : [];
      if (typeof tree.type === "function" && allowed.includes(privateName)) {
        const Original = tree.type;
        if (!privateTypes.has(Original)) privateTypes.set(Original,function LocalizedPrivate(props) {
          React.useSyncExternalStore(subscribe,getLocaleState,getLocaleState);
          return localizeElementTree(React,privateFragments(Original(props),parent),getLocaleState());
        });
        return React.createElement(privateTypes.get(Original),{...tree.props,key:tree.key,ref:tree.ref});
      }
      if (["pre","code","textarea"].includes(tree.type)) return tree;
      if (typeof tree.props.source === "string") {
        const source = tree.props.source;
        if (parent === "liveResponse") {
          const entry = controls.find(entry=>["ui.failedFetch","ui.failedFetchReasons"].includes(entry.id)&&entry.source===source);
          if (entry) return React.cloneElement(tree,{source:getLocaleState().catalog[entry.id]});
        }
        if (parent === "parameterRow") for (const [prefix,key] of [["Available values","ui.availableValues"],["Default value","ui.defaultValue"],["Example","ui.example"]]) {
          const before = `<i>${prefix}</i> : `;
          if (source.startsWith(before)) return React.cloneElement(tree,{source:`<i>${translate(key,{},getLocaleState().catalog)}</i> : `+source.slice(before.length)});
        }
      }
      const children = tree.props.children;
      if ((tree.type === "a" || tree.type === "small") && Array.isArray(children)) {
        const label = typeof children[0] === "string" ? children[0].trim() : "";
        const key = label === "Jump to line" ? "ui.jumpToLine" : label === "at" ? "ui.atPath" : label === "on line" ? "ui.onLine" : null;
        if (key) return React.cloneElement(tree,{children:translate(key,{line:children[1],path:children[1]},getLocaleState().catalog)});
      }
      return children === undefined ? tree : React.cloneElement(tree,{children:privateFragments(children,parent)});
    }
    const controlled = controlledComponents(system,getLocaleState,key=>translate(key,{},getLocaleState().catalog),explanationText);
    const explainComponents = new Set(["info","OperationSummary","operation","OperationTag","response","ResponseExtension","Model","Property"]);
    for (const [name, entry] of Object.entries(registry)) {
      const stack = Array.isArray(entry) ? entry : [entry];
      if (!stack.length) continue;
      const Original = stack[0];
      let Localized;
      if (controlled[name]) {
        Localized = controlled[name];
      } else if (Original?.prototype?.isReactComponent && typeof Original.prototype.render === "function" && (displayClasses.has(name)||attributeClasses.has(name))) {
        Localized = class extends Original {
          componentDidMount() { super.componentDidMount?.(); this.aiswUnsubscribe = subscribe(()=>this.forceUpdate()); }
          componentWillUnmount() { this.aiswUnsubscribe?.(); super.componentWillUnmount?.(); }
          render() { return localizeElementTree(React,privateFragments(super.render(),name),getLocaleState(),explainComponents.has(name)&&ownedSpec(),false,attributeClasses.has(name),name); }
        };
      } else if (directFunctions.has(name) && typeof Original === "function") {
        Localized = function LocalizedFixedFunction(props) {
          React.useSyncExternalStore(subscribe,getLocaleState,getLocaleState);
          const rendered = Original(props);
          if (name === "RequestBody" && rendered?.type === "i" && Array.isArray(rendered.props.children) && rendered.props.children[1]?.type === "code") {
            const marker = "\uE000";
            const [before,after] = translate("ui.noMediaExamples",{mediaType:marker},getLocaleState().catalog).split(marker);
            return React.cloneElement(rendered,{children:[before,rendered.props.children[1],after]});
          }
          return localizeElementTree(React,rendered,getLocaleState());
        };
      } else continue;
      components[name] = stack.slice(1).reduce((component,wrapper)=>wrapper(component,system),Localized);
    }
    return {
      components,
      wrapComponents: {
        Markdown: Original => function LocalizedMarkdown(props) {
          React.useSyncExternalStore(subscribe,getLocaleState,getLocaleState);
          return React.createElement(Original,{...props,source: typeof props.source === "string" ? explanationText(props.source) : props.source});
        },
      },
    };
  };
}
