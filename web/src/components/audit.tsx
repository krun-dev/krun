import { errorMessage, t, locale, credentialLabel } from "@/lib/i18n";
import { useTranslation } from "react-i18next";
import { useEffect, useState } from "react";
import {
  CalendarDays,
  ChevronLeft,
  ChevronRight,
  ChevronsLeft,
  RefreshCw,
} from "lucide-react";
import { api, type AuditPageResponse } from "@/lib/api";
import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
import { Empty, Loading, PageHeader, SelectField } from "./common";
import { DataTable, ErrorState, ID } from "./management";

export function AuditPage({
  base,
  refresh,
}: {
  base: string;
  refresh: number;
}) {
  useTranslation();
  const [limit, setLimit] = useState("50");
  const [cursors, setCursors] = useState([""]);
  const [page, setPage] = useState(0);
  const [reload, setReload] = useState(0);
  const [result, setResult] = useState<{
    key: string;
    data?: AuditPageResponse;
    error?: string;
  }>({ key: "" });
  const query = new URLSearchParams({
    limit,
    ...(cursors[page] ? { cursor: cursors[page] } : {}),
  });
  const path = `${base}/audit?${query}`;
  const requestKey = `${path}@${refresh}:${reload}`;
  const loading = result.key !== requestKey;
  const data = result.data;
  useEffect(() => {
    let active = true;
    api<AuditPageResponse>(path)
      .then((data) => {
        if (active) setResult({ key: requestKey, data });
      })
      .catch((error) => {
        if (active)
          setResult({
            key: requestKey,
            error: errorMessage(error),
          });
      });
    return () => {
      active = false;
    };
  }, [path, requestKey]);
  function reset() {
    setCursors([""]);
    setPage(0);
    setReload((value) => value + 1);
  }
  function next() {
    if (!data?.next_cursor || loading) return;
    const history = cursors.slice(0, page + 1);
    // Save the canonical cursor for the first page, so returning to it uses
    // the same snapshot rather than including newly arrived audit records.
    history[page] = data.cursor;
    setCursors([...history, data.next_cursor]);
    setPage((value) => value + 1);
  }
  const start = data?.items.length ? page * Number(limit) + 1 : 0;
  const end = data?.items.length ? page * Number(limit) + data.items.length : 0;
  return (
    <>
      <PageHeader
        eyebrow={t("Activity log")}
        title={t("审计记录")}
        description={t(
          "查看最近 7 天的授权与管理记录，超过保留期的记录会自动清理。",
        )}
        action={
          <Button variant="outline" onClick={reset} disabled={loading}>
            <RefreshCw size={15} className={loading ? "animate-spin" : ""} />
            {t("刷新")}
          </Button>
        }
      />
      <div className="mb-5 flex flex-wrap items-center gap-3 border-b pb-4">
        <Badge
          variant="outline"
          className="gap-1.5 font-normal text-muted-foreground"
        >
          <CalendarDays size={13} />
          {t("最近 7 天")}
        </Badge>
        <span className="text-xs text-muted-foreground">
          {!loading && data
            ? t("共 {{v0}} 条记录", { v0: data.total.toLocaleString(locale()) })
            : t("正在加载…")}
        </span>
        <div className="ml-auto flex items-center gap-2 text-xs text-muted-foreground">
          <span>{t("每页")}</span>
          <div className="w-24">
            <SelectField
              label={t("审计每页条数")}
              value={limit}
              onChange={(value) => {
                setLimit(value);
                reset();
              }}
            >
              {[25, 50, 100, 200, 500].map((size) => (
                <option key={size} value={size}>
                  {t("{{count}} 条", { count: size })}
                </option>
              ))}
            </SelectField>
          </div>
        </div>
      </div>
      {loading ? (
        <Loading />
      ) : result.error ? (
        <div className="space-y-3">
          <ErrorState error={result.error} />
          <Button variant="outline" onClick={reset}>
            {t("重新加载首页")}
          </Button>
        </div>
      ) : !data?.items.length ? (
        <Empty
          title={t("没有更多审计记录")}
          description={t(
            "仅展示最近 7 天内且当前身份有权访问的记录，可返回上一页或刷新。",
          )}
        />
      ) : (
        <DataTable
          className="[&_[data-slot=table-container]]:max-h-[max(240px,min(680px,calc(100dvh_-_380px)))] [&_[data-slot=table-header]]:sticky [&_[data-slot=table-header]]:top-0 [&_[data-slot=table-header]]:z-10 [&_[data-slot=table-header]]:bg-card"
          rows={data.items}
          getKey={(entry) => entry.id}
          columns={[
            {
              title: t("时间"),
              render: (entry) => (
                <span className="whitespace-nowrap text-xs">
                  {new Date(entry.time).toLocaleString(locale())}
                </span>
              ),
            },
            {
              title: t("动作"),
              render: (entry) => (
                <code className="text-xs">{entry.action}</code>
              ),
            },
            {
              title: t("主体"),
              render: (entry) => (
                <div>
                  <ID value={entry.actor} />
                  <p className="mt-1 text-[11px] text-muted-foreground">
                    {credentialLabel(entry.credential)}
                  </p>
                </div>
              ),
            },
            {
              title: t("资源"),
              render: (entry) => <ID value={entry.resource} />,
            },
            {
              title: t("决策"),
              render: (entry) => (
                <Badge
                  variant="outline"
                  className={
                    entry.allowed
                      ? "border-primary/25 bg-primary/5 font-normal text-primary"
                      : "border-destructive/25 bg-destructive/5 font-normal text-destructive"
                  }
                >
                  {entry.allowed ? t("允许") : t("拒绝")}
                </Badge>
              ),
            },
            {
              title: t("请求"),
              render: (entry) => <ID value={entry.request_id} />,
            },
          ]}
        />
      )}
      <div className="mt-4 flex flex-wrap items-center justify-between gap-3">
        <span className="text-xs text-muted-foreground">
          {!loading && data
            ? t("{{v0}}–{{v1}} / {{v2}} 条", {
                v0: start,
                v1: end,
                v2: data.total.toLocaleString(locale()),
              })
            : t("加载中…")}
        </span>
        <div className="flex items-center gap-2">
          <Button
            variant="outline"
            size="icon-sm"
            aria-label={t("审计首页")}
            disabled={loading || page === 0}
            onClick={() => setPage(0)}
          >
            <ChevronsLeft size={15} />
          </Button>
          <Button
            variant="outline"
            size="sm"
            disabled={loading || page === 0}
            onClick={() => setPage((value) => value - 1)}
          >
            <ChevronLeft size={15} />
            {t("上一页")}
          </Button>
          <span className="px-1 text-xs tabular-nums text-muted-foreground">
            {t("第 {{page}} 页", { page: page + 1 })}
            {data && !loading
              ? ` / ${Math.max(1, Math.ceil(data.total / Number(limit)))}`
              : ""}
          </span>
          <Button
            variant="outline"
            size="sm"
            disabled={loading || !data?.has_more || !!result.error}
            onClick={next}
          >
            {t("下一页")}
            <ChevronRight size={15} />
          </Button>
        </div>
      </div>
      {data && !loading && (
        <p className="mt-4 text-[11px] leading-5 text-muted-foreground">
          {t(
            "按 {{date}} 的快照浏览；刷新可查看新记录。超过 7 天的记录不再保留。",
            { date: new Date(data.as_of).toLocaleString(locale()) },
          )}
        </p>
      )}
    </>
  );
}
