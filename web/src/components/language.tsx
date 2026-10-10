import { Languages } from "lucide-react";
import { useTranslation } from "react-i18next";
import { locale, setLanguage, t, type Language } from "@/lib/i18n";

export function LanguageSwitch() {
  useTranslation();
  return (
    <label className="inline-flex items-center gap-1.5 text-xs text-muted-foreground">
      <Languages size={15} aria-hidden="true" />
      <select
        aria-label={t("语言")}
        className="h-8 rounded-md border bg-card px-2 text-foreground"
        value={locale()}
        onChange={(event) => void setLanguage(event.target.value as Language)}
      >
        <option value="zh-CN">中文</option>
        <option value="en">English</option>
      </select>
    </label>
  );
}
