// Display adapters derived from Swagger UI 5.17.14 (Apache-2.0).
// 展示适配基于 Swagger UI 5.17.14（Apache-2.0），保留原有状态和 effect 依赖。
export function controlledComponents(system, getLocaleState, text, explanationText) {
  const React = system.React;
  const h = React.createElement;
  const subscribe = getLocaleState.subscribe ?? (()=>()=>{});
  function useLocale() { React.useSyncExternalStore(subscribe,getLocaleState,getLocaleState); }

  function ModelExample({schema,example,isExecute=false,specPath,includeWriteOnly=false,includeReadOnly=false,getComponent,getConfigs,specSelectors}) {
    useLocale();
    const {defaultModelRendering,defaultModelExpandDepth} = getConfigs();
    const initialTab = ["example","model"].includes(defaultModelRendering) && schema && !isExecute ? defaultModelRendering : "example";
    const previous = React.useRef();
    const previousExecute = previous.current;
    React.useEffect(()=>{ previous.current = isExecute; });
    const [activeTab,setActiveTab] = React.useState(initialTab);
    const onTabChange = React.useCallback(event=>setActiveTab(event.target.dataset.name),[]);
    React.useEffect(()=>{ if (previousExecute && !isExecute && example) setActiveTab("example"); },[previousExecute,isExecute,example]);
    const id = React.useId(), exampleTab=id+"-example-tab", examplePanel=id+"-example-panel", modelTab=id+"-model-tab", modelPanel=id+"-model-panel";
    const ModelWrapper = getComponent("ModelWrapper"), HighlightCode = getComponent("HighlightCode",true);
    return h("div",{className:"model-example"},
      h("ul",{className:"tab",role:"tablist"},
        h("li",{className:"tabitem"+(activeTab==="example"?" active":""),role:"presentation"},h("button",{"aria-controls":examplePanel,"aria-selected":activeTab==="example",className:"tablinks","data-name":"example",id:exampleTab,onClick:onTabChange,role:"tab"},text(isExecute?"ui.editValue":"ui.exampleValue"))),
        schema && h("li",{className:"tabitem"+(activeTab==="model"?" active":""),role:"presentation"},h("button",{"aria-controls":modelPanel,"aria-selected":activeTab==="model",className:"tablinks"+(isExecute?" inactive":""),"data-name":"model",id:modelTab,onClick:onTabChange,role:"tab"},text(specSelectors.isOAS3()?"ui.schema":"ui.model")))),
      activeTab==="example" && h("div",{"aria-hidden":false,"aria-labelledby":exampleTab,"data-name":"examplePanel",id:examplePanel,role:"tabpanel",tabIndex:"0"},example||h(HighlightCode,null,text("ui.noExample"))),
      activeTab==="model" && h("div",{"aria-hidden":false,"aria-labelledby":modelTab,"data-name":"modelPanel",id:modelPanel,role:"tabpanel",tabIndex:"0"},h(ModelWrapper,{schema,getComponent,getConfigs,specSelectors,expandDepth:defaultModelExpandDepth,specPath,includeReadOnly,includeWriteOnly})));
  }

  function Servers({servers,currentServer,setSelectedServer,setServerVariableValue,getServerVariable,getEffectiveServerValue}) {
    useLocale();
    const variables = servers.find(server=>server.get("url")===currentServer)?.get("variables");
    React.useEffect(()=>{ if (!currentServer) setSelectedServer(servers.first()?.get("url")); },[]);
    React.useEffect(()=>{
      const server = servers.find(server=>server.get("url")===currentServer);
      if (server) server.get("variables")?.map((variable,key)=>{ setServerVariableValue({server:currentServer,key,val:variable.get("default")||""}); });
      else setSelectedServer(servers.first().get("url"));
    },[currentServer,servers]);
    const onSelect = React.useCallback(event=>setSelectedServer(event.target.value),[setSelectedServer]);
    const onVariable = React.useCallback(event=>setServerVariableValue({server:currentServer,key:event.target.getAttribute("data-variable"),val:event.target.value}),[setServerVariableValue,currentServer]);
    return h("div",{className:"servers"},
      h("label",{htmlFor:"servers"},h("select",{onChange:onSelect,value:currentServer,id:"servers"},servers.valueSeq().map(server=>h("option",{value:server.get("url"),key:server.get("url")},server.get("url"),server.get("description")&&" - "+explanationText(server.get("description")))).toArray())),
      !!variables?.size && h("div",null,h("div",{className:"computed-url"},text("ui.computedUrl"),h("code",null,getEffectiveServerValue(currentServer))),h("h4",null,text("ui.serverVariables")),h("table",null,h("tbody",null,variables.entrySeq().map(([key,variable])=>h("tr",{key},h("td",null,key),h("td",null,variable.get("enum")?h("select",{"data-variable":key,onChange:onVariable},variable.get("enum").map(value=>h("option",{selected:value===getServerVariable(currentServer,key),key:value,value},value))):h("input",{type:"text",value:getServerVariable(currentServer,key)||"",onChange:onVariable,"data-variable":key}))))))));
  }
  function EnumModel({value,getComponent}) {
    useLocale();
    const ModelCollapse = getComponent("ModelCollapse");
    const collapsedContent = h("span",null,"Array [ ",value.count()," ]");
    return h("span",{className:"prop-enum"},text("ui.enum"),h("br"),h(ModelCollapse,{collapsedContent},"[ ",value.map(String).join(", ")," ]"));
  }
  return {modelExample:ModelExample,Servers,EnumModel};
}
