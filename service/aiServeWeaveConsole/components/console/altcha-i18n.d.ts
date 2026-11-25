// altcha 3.7.0 ships these translations without matching declarations (its
// .d.ts files are named cn/tw/fr/es). They are side-effect imports that
// register strings with the widget and export nothing.
// altcha 3.7.0 发布的这几个翻译没有对应的类型声明（它的 .d.ts 文件名是 cn/tw/fr/es）。
// 它们是只向组件注册文案的副作用导入，不导出任何东西。
declare module "altcha/i18n/zh-cn";
declare module "altcha/i18n/zh-tw";
declare module "altcha/i18n/fr-fr";
declare module "altcha/i18n/es-es";
