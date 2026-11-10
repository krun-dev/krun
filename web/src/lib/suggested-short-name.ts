import { useEffect, useState } from "react";

export function useSuggestedShortName(name: string, maxLength: number) {
  const [edited, setEdited] = useState<string | null>(null);
  const [slugify, setSlugify] = useState<typeof import("transliteration").slugify>();
  const normalized = name.normalize("NFKC");
  const needsTransliteration = /[^\x00-\x7f]/.test(normalized);
  const loadTransliteration = edited === null && needsTransliteration && !slugify;

  useEffect(() => {
    if (!loadTransliteration) return;

    let active = true;
    void import("transliteration")
      .then((module) => {
        if (active) setSlugify(() => module.slugify);
      })
      .catch(() => {
        // An empty field still lets the server generate the name on submit.
      });
    return () => {
      active = false;
    };
  }, [loadTransliteration]);

  const suggestion = needsTransliteration
    ? (slugify?.(normalized, { allowedChars: "a-zA-Z0-9" }) ?? "")
    : normalized.toLowerCase().replace(/[^a-z0-9]+/g, "-");
  const value = edited ?? suggestion.replace(/^-+/, "").slice(0, maxLength).replace(/-+$/, "");

  return { value, edit: setEdited, reset: () => setEdited(null) };
}
