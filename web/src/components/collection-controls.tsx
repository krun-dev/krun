import { useEffect, useState } from "react";
import { useTranslation } from "react-i18next";
import { t } from "@/lib/i18n";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";

type CollectionPage = {
  search: string;
  pageSize: number;
  pageNumber: number;
  loading: boolean;
  hasPrevious: boolean;
  hasNext: boolean;
  searchFor: (value: string) => void;
  resize: (value: number) => void;
  previous: () => void;
  next: () => void;
};

export function CollectionToolbar({ page, label }: { page: CollectionPage; label: string }) {
  useTranslation();
  const [draft, setDraft] = useState(page.search);
  useEffect(() => setDraft(page.search), [page.search]);
  return (
    <div className="mb-4 flex flex-wrap items-center justify-between gap-3">
      <form
        className="flex w-full max-w-lg gap-2"
        role="search"
        onSubmit={(event) => {
          event.preventDefault();
          page.searchFor(draft);
        }}
      >
        <Input
          aria-label={label}
          placeholder={label}
          value={draft}
          onChange={(event) => setDraft(event.target.value)}
        />
        <Button type="submit" variant="outline">
          {t("搜索")}
        </Button>
        {draft || page.search ? (
          <Button
            variant="ghost"
            type="button"
            onClick={() => {
              setDraft("");
              page.searchFor("");
            }}
          >
            {t("清除")}
          </Button>
        ) : null}
      </form>
      <div className="flex items-center gap-2 text-sm text-muted-foreground">
        <span>{t("每页条数")}</span>
        <select
          aria-label={t("每页条数")}
          className="h-9 rounded-md border bg-background px-3"
          value={String(page.pageSize)}
          onChange={(event) => page.resize(Number(event.target.value))}
        >
          {[20, 50, 100].map((size) => (
            <option key={size} value={size}>
              {size}
            </option>
          ))}
        </select>
      </div>
    </div>
  );
}

export function CollectionPagination({ page }: { page: CollectionPage }) {
  useTranslation();
  return (
    <nav aria-label={t("列表分页")} className="my-4 flex flex-wrap items-center justify-end gap-3">
      <span className="text-sm text-muted-foreground">
        {t("第 {{page}} 页", { page: page.pageNumber })}
      </span>
      <Button
        variant="outline"
        disabled={page.loading || !page.hasPrevious}
        onClick={page.previous}
      >
        {t("上一页")}
      </Button>
      <Button variant="outline" disabled={page.loading || !page.hasNext} onClick={page.next}>
        {t("下一页")}
      </Button>
    </nav>
  );
}
