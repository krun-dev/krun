import i18next from "i18next";
import { initReactI18next } from "react-i18next";
import zh from "../locales/zh-CN.json" with { type: "json" };
import en from "../locales/en.json" with { type: "json" };
import runtimeMessages from "../../../internal/localize/zh-CN.json" with { type: "json" };

export type Language = "zh-CN" | "en";
export function savedLanguage(): Language {
  try {
    return localStorage.getItem("krun.language") === "en" ? "en" : "zh-CN";
  } catch {
    return "zh-CN";
  }
}

void i18next.use(initReactI18next).init({
  resources: {
    "zh-CN": { translation: { ...zh, ...runtimeMessages } },
    en: {
      translation: {
        ...en,
        ...Object.fromEntries(
          Object.keys(runtimeMessages).map((key) => [key, key]),
        ),
      },
    },
  },
  lng: savedLanguage(),
  fallbackLng: "zh-CN",
  supportedLngs: ["zh-CN", "en"],
  keySeparator: false,
  nsSeparator: false,
  interpolation: { escapeValue: false },
  react: { useSuspense: false },
});

export function locale(): Language {
  return i18next.resolvedLanguage === "en" ? "en" : "zh-CN";
}
export function t(message: string, values?: Record<string, unknown>): string {
  const key = i18next.exists(message)
    ? message
    : (originalMessages.get(message) ?? message);
  return i18next.t(key, values);
}
const originalMessages = new Map(
  [...Object.entries(en), ...Object.entries(runtimeMessages)].map(
    ([key, value]) => [value, key],
  ),
);
function updateDocument() {
  if (typeof document === "undefined") return;
  document.documentElement.lang = locale();
  document.title = `krun · ${t("控制台")}`;
}
export async function setLanguage(language: Language) {
  await i18next.changeLanguage(language);
  try {
    localStorage.setItem("krun.language", language);
  } catch {
    // Switching still works when storage is unavailable.
  }
}
i18next.on("languageChanged", updateDocument);
updateDocument();

export function environmentLabel(value: string): string {
  const labels: Record<string, string> = {
    global: "全局",
    development: "开发",
    staging: "预发布",
    production: "生产",
  };
  return t(labels[value] ?? value);
}
export function roleLabel(value: string): string {
  const labels: Record<string, string> = {
    system_admin: "系统管理员",
    org_admin: "组织管理员",
    developer: "开发者",
    publisher: "发布者",
    viewer: "查看者",
  };
  return t(labels[value] ?? value);
}

export function resourceLabel(value: string): string {
  const labels: Record<string, string> = {
    system: "系统",
    organization: "组织",
    service: "服务",
    model: "模型",
    api_key: "密钥",
  };
  return t(labels[value] ?? value);
}

export function credentialLabel(value: string): string {
  const labels: Record<string, string> = {
    session: "浏览器会话",
    cli: "CLI 会话",
    api_key: "API 密钥",
    bootstrap: "管理员初始化",
  };
  return t(labels[value] ?? value);
}

export function authError(code: string): string {
  const labels: Record<string, string> = {
    AUTH_STATE_INVALID: "登录已过期或已被新的请求替代，请重新登录。",
    AUTH_UPSTREAM_DENIED: "身份来源拒绝了登录请求。",
    AUTH_UPSTREAM_FAILED: "身份来源登录失败，请重试或联系管理员。",
    AUTH_PROVIDER_UNAVAILABLE: "Provider unavailable",
    USER_DISABLED: "User disabled",
    INVITATION_REQUIRED: "Invitation required",
    RATE_LIMITED: "Try again later",
  };
  return `${t(labels[code] ?? "登录失败")} (${code})`;
}

export function errorMessage(error: unknown): string {
  if (error instanceof SyntaxError) return t("JSON 格式无效");
  if (error instanceof TypeError) return t("网络连接失败，请检查网络后重试。");
  return error instanceof Error ? t(error.message) : t("操作失败");
}
