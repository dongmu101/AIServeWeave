"use client";

import * as React from "react";
import type {} from "altcha/types/react";

import { useI18n } from "@/components/i18n/provider";
import type { Locale } from "@/lib/i18n/locale";

/**
 * CaptchaField is the human check on the tenant sign-in and sign-up forms:
 * ALTCHA's open-source widget, solving a proof-of-work challenge from
 * /api/captcha in the browser. It reports the payload to submit once
 * verified and null otherwise. A solution is spent by one submission, so the
 * form calls reset() after every attempt, successful or not.
 *
 * The widget touches `customElements`, so it is loaded only in the browser,
 * after mount.
 *
 * CaptchaField 是租户登录与注册表单上的人机验证：ALTCHA 的开源组件，在浏览器中求解
 * 来自 /api/captcha 的工作量证明挑战。校验通过时回报要提交的 payload，否则回报 null。
 * 一个解只能用于一次提交，因此表单在每次尝试之后（无论成败）都调用 reset()。
 *
 * 组件会访问 `customElements`，因此只在浏览器中、挂载之后才加载。
 */

/** CaptchaFieldHandle is what the form may ask of the field.
 * CaptchaFieldHandle 是表单可以对本字段调用的方法。 */
export interface CaptchaFieldHandle {
  reset(): void;
}

/** WIDGET_LANGUAGES maps Console locales onto ALTCHA's translation codes.
 * WIDGET_LANGUAGES 把 Console 的语言映射到 ALTCHA 的翻译代码。 */
const WIDGET_LANGUAGES: Record<Locale, string> = {
  "zh-CN": "zh-cn",
  "zh-TW": "zh-tw",
  "en-US": "en",
  "ja-JP": "ja",
  "ko-KR": "ko",
  "de-DE": "de",
  "fr-FR": "fr-fr",
  "es-ES": "es-es",
};

/** loadWidget registers the custom element and the eight translations once.
 * loadWidget 只注册一次自定义元素与八种翻译。 */
let loading: Promise<unknown> | null = null;
function loadWidget(): Promise<unknown> {
  loading ??= import("altcha").then(() =>
    Promise.all([
      import("altcha/i18n/zh-cn"),
      import("altcha/i18n/zh-tw"),
      import("altcha/i18n/en"),
      import("altcha/i18n/ja"),
      import("altcha/i18n/ko"),
      import("altcha/i18n/de"),
      import("altcha/i18n/fr-fr"),
      import("altcha/i18n/es-es"),
    ])
  );
  return loading;
}

/** WIDGET_CONFIGURATION drops the attribution footer and logo, leaving the
 * widget as a single row.
 * WIDGET_CONFIGURATION 去掉署名页脚与图标，使组件只占一行。 */
const WIDGET_CONFIGURATION = JSON.stringify({ hideFooter: true, hideLogo: true });

/** WIDGET_HEIGHT is the widget's rendered height, equal to the form's inputs
 * and buttons (h-10): 1px border and 10px padding on each side around the
 * 18px checkbox, which is taller than the 14px label's 17.5px line. The
 * placeholder shown while the widget loads uses it too, so nothing shifts
 * when the widget appears.
 *
 * WIDGET_HEIGHT 是组件渲染后的高度，与表单里的输入框和按钮（h-10）一致：
 * 18px 复选框上下各加 1px 边框与 10px 内边距；复选框高于 14px 标签的 17.5px 行高。
 * 组件加载期间的占位块也使用它，组件出现时版面不跳动。 */
const WIDGET_HEIGHT = "h-10";

/** WIDGET_STYLE ties the widget's colours, radius, border and type size to
 * the Console theme tokens and to the Input component, so it reads as one
 * more field of the form and follows light and dark mode with it.
 *
 * WIDGET_STYLE 把组件的颜色、圆角、边框与字号绑定到 Console 的主题变量和 Input
 * 组件的样式，使其看起来就是表单的又一个字段，并一起跟随明暗模式。 */
const WIDGET_STYLE = {
  fontSize: "0.875rem",
  "--altcha-max-width": "100%",
  "--altcha-padding": "10px",
  "--altcha-border-radius": "var(--radius)",
  "--altcha-border-color": "var(--input)",
  "--altcha-checkbox-size": "18px",
  "--altcha-checkbox-border-radius": "4px",
  "--altcha-checkbox-border-color": "var(--input)",
  "--altcha-checkbox-outline": "3px solid color-mix(in oklab, var(--ring) 50%, transparent)",
  "--altcha-checkbox-outline-offset": "1px",
  "--altcha-color-base": "var(--background)",
  "--altcha-color-base-content": "var(--foreground)",
  "--altcha-color-neutral": "var(--input)",
  "--altcha-color-neutral-content": "var(--muted-foreground)",
  "--altcha-color-primary": "var(--primary)",
  "--altcha-color-primary-content": "var(--primary-foreground)",
  "--altcha-color-success": "var(--primary)",
  "--altcha-color-success-content": "var(--primary-foreground)",
  "--altcha-color-error": "var(--destructive)",
  "--altcha-spinner-color": "var(--muted-foreground)",
  "--altcha-transition-duration": "0.2s",
} as React.CSSProperties;

export function CaptchaField({
  onChange,
  ref,
}: {
  onChange: (payload: string | null) => void;
  ref?: React.Ref<CaptchaFieldHandle>;
}) {
  const { locale } = useI18n();
  const widget = React.useRef<HTMLElement & { reset?: () => void }>(null);
  const [ready, setReady] = React.useState(false);
  const report = React.useEffectEvent((payload: string | null) => onChange(payload));

  React.useEffect(() => {
    let live = true;
    loadWidget().then(
      () => live && setReady(true),
      () => live && setReady(false)
    );
    return () => {
      live = false;
    };
  }, []);

  React.useEffect(() => {
    const element = widget.current;
    if (!ready || !element) return;
    const listener = (event: Event) => {
      const { state, payload } = (event as CustomEvent<{ state: string; payload: string | null }>).detail;
      report(state === "verified" && payload ? payload : null);
    };
    element.addEventListener("statechange", listener);
    return () => element.removeEventListener("statechange", listener);
  }, [ready]);

  React.useImperativeHandle(ref, () => ({
    reset() {
      widget.current?.reset?.();
      onChange(null);
    },
  }), [onChange]);

  if (!ready) {
    return <div className={`${WIDGET_HEIGHT} rounded-lg border border-input bg-background`} aria-hidden="true" />;
  }
  return (
    <altcha-widget
      ref={widget}
      challenge="/api/captcha"
      type="checkbox"
      auto="onfocus"
      configuration={WIDGET_CONFIGURATION}
      language={WIDGET_LANGUAGES[locale]}
      style={WIDGET_STYLE}
    />
  );
}
