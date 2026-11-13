import { useEffect, useState } from "react";
import { Plus, Trash2 } from "lucide-react";
import { APIError, api } from "@/lib/api";
import { errorMessage, t } from "@/lib/i18n";
import { Field, Loading, SelectField } from "@/components/common";
import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
import { Card, CardContent } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";

type Route = {
  match: "prefix" | "exact";
  path: string;
  worker_id: string;
  worker_locator?: string;
  enabled: boolean;
};
type Hostname = { host: string; suffix_id: string; revision: number; routes: Route[] };
type Suffix = { id: string; domain: string; label: string };
type Catalog = { items: Hostname[]; suffixes: Suffix[]; can_manage: boolean };
type Bindings = { items: Hostname[]; can_read_catalog: boolean };

export function WorkerHostnameBindings({
  base,
  openCatalog,
}: {
  base: string;
  openCatalog: () => void;
}) {
  const [data, setData] = useState<Bindings>();
  const [error, setError] = useState("");
  useEffect(() => {
    const controller = new AbortController();
    void api<Bindings>(base + "/hostnames", "GET", undefined, controller.signal)
      .then(setData)
      .catch((cause) => {
        if (!controller.signal.aborted) setError(errorMessage(cause));
      });
    return () => controller.abort();
  }, [base]);

  return (
    <Card className="py-0 shadow-none">
      <CardContent className="grid gap-3 p-5">
        <div className="flex flex-wrap items-center justify-between gap-3">
          <h3 className="font-medium">{t("自定义子域名")}</h3>
          {data?.can_read_catalog ? (
            <Button
              variant="outline"
              onClick={openCatalog}
            >
              {t("管理域名与路由")}
            </Button>
          ) : null}
        </div>
        {error ? (
          <p
            role="alert"
            className="text-sm text-destructive"
          >
            {error}
          </p>
        ) : !data ? (
          <Loading />
        ) : data.items.length === 0 ? (
          <p className="text-sm text-muted-foreground">{t("此 Worker 尚未绑定自定义子域名。")}</p>
        ) : (
          data.items.map((item) => (
            <HostnameCard
              key={item.host}
              item={item}
            />
          ))
        )}
      </CardContent>
    </Card>
  );
}

function HostnameCard({ item, edit }: { item: Hostname; edit?: () => void }) {
  return (
    <div className="rounded-lg border p-4">
      <div className="flex flex-wrap items-center justify-between gap-3">
        <a
          className="break-all font-mono text-sm font-medium hover:underline"
          href={"https://" + item.host}
          target="_blank"
          rel="noreferrer"
        >
          {item.host}
        </a>
        {edit ? (
          <Button
            variant="outline"
            size="sm"
            onClick={edit}
          >
            {t("编辑路由")}
          </Button>
        ) : null}
      </div>
      {item.routes.length > 0 ? (
        <ul className="mt-3 divide-y">
          {item.routes.map((route) => (
            <li
              key={route.match + ":" + route.path}
              className="flex flex-wrap items-center gap-2 py-2 text-sm"
            >
              <Badge variant="outline">{matchLabel(route.match)}</Badge>
              <code className="break-all">{route.path}</code>
              <span
                aria-hidden="true"
                className="text-muted-foreground"
              >
                →
              </span>
              <code className="break-all">{route.worker_locator || route.worker_id}</code>
              {!route.enabled ? <Badge variant="secondary">{t("已停用")}</Badge> : null}
            </li>
          ))}
        </ul>
      ) : (
        <p className="mt-2 text-sm text-muted-foreground">{t("尚未配置路由。")}</p>
      )}
    </div>
  );
}

function matchLabel(match: Route["match"]) {
  return match === "exact" ? t("精确路径") : t("路径段前缀");
}

export function ManagedHostnames({ base }: { base: string }) {
  const path = base + "/worker-hostnames";
  const [catalog, setCatalog] = useState<Catalog>();
  const [error, setError] = useState("");
  const [revision, setRevision] = useState(0);
  const [editor, setEditor] = useState<Hostname | "new" | null>(null);
  const [notice, setNotice] = useState("");

  useEffect(() => {
    const controller = new AbortController();
    setError("");
    void api<Catalog>(path, "GET", undefined, controller.signal)
      .then(setCatalog)
      .catch((cause) => {
        if (!controller.signal.aborted) setError(errorMessage(cause));
      });
    return () => controller.abort();
  }, [path, revision]);

  function refresh() {
    setRevision((value) => value + 1);
  }

  return (
    <Card className="py-0 shadow-none">
      <CardContent className="grid gap-4 p-5">
        <div className="flex flex-wrap items-start justify-between gap-3">
          <div>
            <h2 className="font-semibold">{t("自定义子域名")}</h2>
            <p className="mt-1 text-sm text-muted-foreground">
              {t("从平台后缀中申请子域名，将同一域名的不同路径交给不同的 Worker。")}
            </p>
          </div>
          {catalog?.can_manage && catalog.suffixes.length > 0 ? (
            <Button onClick={() => setEditor("new")}>
              <Plus size={16} />
              {t("申请域名")}
            </Button>
          ) : null}
        </div>
        {notice ? (
          <p
            role="status"
            className="text-sm text-muted-foreground"
          >
            {notice}
          </p>
        ) : null}
        {error ? (
          <div
            role="alert"
            className="flex items-center gap-3 text-sm text-destructive"
          >
            {error}
            <Button
              variant="outline"
              size="sm"
              onClick={refresh}
            >
              {t("重试")}
            </Button>
          </div>
        ) : !catalog ? (
          <Loading />
        ) : catalog.items.length === 0 ? (
          <p className="text-sm text-muted-foreground">
            {catalog.suffixes.length
              ? t("还没有自定义子域名。")
              : t("平台尚未开放可申请的域名后缀。")}
          </p>
        ) : (
          catalog.items.map((item) => (
            <HostnameCard
              key={item.host}
              item={item}
              edit={catalog.can_manage ? () => setEditor(item) : undefined}
            />
          ))
        )}
      </CardContent>
      {editor && catalog ? (
        <HostnameEditor
          path={path}
          item={editor === "new" ? undefined : editor}
          suffixes={catalog.suffixes}
          close={() => setEditor(null)}
          saved={(retired) => {
            setEditor(null);
            setNotice(retired ? t("域名已退役，路由正在撤除。") : t("配置已保存，路由正在同步。"));
            refresh();
          }}
        />
      ) : null}
    </Card>
  );
}

function HostnameEditor({
  path,
  item,
  suffixes,
  close,
  saved,
}: {
  path: string;
  item?: Hostname;
  suffixes: Suffix[];
  close: () => void;
  saved: (retired: boolean) => void;
}) {
  const [suffix, setSuffix] = useState(suffixes[0]?.id || "");
  const [label, setLabel] = useState("");
  const [routes, setRoutes] = useState(() =>
    (item?.routes || []).map((route, key) => ({ ...route, key })),
  );
  const [nextKey, setNextKey] = useState(routes.length);
  const [retiring, setRetiring] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const preview = suffixes.find((value) => value.id === suffix)?.domain;
  const changesFallback = item?.routes.some(
    (previous) =>
      !routes.some((route) => route.match === previous.match && route.path === previous.path),
  );

  function changeRoute(key: number, update: Partial<Route>) {
    setRoutes((current) =>
      current.map((route) => (route.key === key ? { ...route, ...update } : route)),
    );
  }

  async function submit(retire: boolean) {
    setBusy(true);
    setError("");
    try {
      const input = routes.map(({ match, path, worker_id, enabled }) => ({
        match,
        path,
        worker_id,
        enabled,
      }));
      if (item && retire) {
        await api(
          path + "/" + encodeURIComponent(item.host) + "?revision=" + item.revision,
          "DELETE",
        );
      } else if (item) {
        await api(path + "/" + encodeURIComponent(item.host), "PUT", {
          expected_revision: item.revision,
          routes: input,
        });
      } else {
        await api(path, "POST", { suffix_id: suffix, label, routes: input });
      }
      saved(retire);
    } catch (cause) {
      setError(
        cause instanceof APIError && cause.code === "REVISION_CONFLICT"
          ? t("域名已被修改，请关闭窗口并刷新后重试。")
          : errorMessage(cause),
      );
    } finally {
      setBusy(false);
    }
  }

  return (
    <Dialog
      open
      onOpenChange={(open) => {
        if (!open && !busy) close();
      }}
    >
      <DialogContent className="max-h-[90vh] overflow-y-auto sm:max-w-3xl">
        <DialogHeader>
          <DialogTitle>{item ? item.host : t("申请域名")}</DialogTitle>
          <DialogDescription>
            {t("精确路径优先，其次匹配最长的路径段前缀。路由指向 Worker 的 production 环境。")}
          </DialogDescription>
        </DialogHeader>
        <form
          className="grid gap-4"
          onSubmit={(event) => {
            event.preventDefault();
            void submit(retiring);
          }}
        >
          {retiring ? (
            <p className="text-sm">{t("退役会撤除所有路由，并继续保留该域名。")}</p>
          ) : (
            <>
              {!item ? (
                <>
                  <div className="grid gap-3 sm:grid-cols-2">
                    <SelectField
                      label={t("域名后缀")}
                      value={suffix}
                      onChange={setSuffix}
                    >
                      {suffixes.map((value) => (
                        <option
                          key={value.id}
                          value={value.id}
                        >
                          {value.domain}
                        </option>
                      ))}
                    </SelectField>
                    <Field label={t("域名前缀")}>
                      <Input
                        required
                        maxLength={63}
                        value={label}
                        onChange={(event) => setLabel(event.target.value)}
                        placeholder="shop"
                      />
                    </Field>
                  </div>
                  <code className="break-all text-sm text-muted-foreground">
                    {label.toLowerCase() || "shop"}.{preview}
                  </code>
                </>
              ) : null}
              {routes.map((route, index) => (
                <div
                  key={route.key}
                  className="grid gap-3 rounded-lg border p-3"
                >
                  <div className="grid gap-3 sm:grid-cols-3">
                    <SelectField
                      label={t("匹配方式")}
                      value={route.match}
                      onChange={(match) =>
                        changeRoute(route.key, { match: match === "exact" ? "exact" : "prefix" })
                      }
                    >
                      <option value="prefix">{matchLabel("prefix")}</option>
                      <option value="exact">{matchLabel("exact")}</option>
                    </SelectField>
                    <Field label={t("请求路径")}>
                      <Input
                        required
                        value={route.path}
                        onChange={(event) => changeRoute(route.key, { path: event.target.value })}
                      />
                    </Field>
                    <Field label={t("目标 Worker")}>
                      <Input
                        required
                        value={route.worker_id}
                        placeholder={t("Worker 短名或 ID")}
                        onChange={(event) =>
                          changeRoute(route.key, { worker_id: event.target.value })
                        }
                      />
                    </Field>
                  </div>
                  <div className="flex items-center justify-between">
                    <label className="flex items-center gap-2 text-sm">
                      <input
                        type="checkbox"
                        checked={route.enabled}
                        onChange={(event) =>
                          changeRoute(route.key, { enabled: event.target.checked })
                        }
                      />
                      {t("启用路由")}
                    </label>
                    <Button
                      type="button"
                      variant="ghost"
                      size="sm"
                      onClick={() => setRoutes((current) => current.filter((_, i) => i !== index))}
                    >
                      <Trash2 size={14} />
                      {t("移除路由")}
                    </Button>
                  </div>
                </div>
              ))}
              <Button
                type="button"
                variant="outline"
                className="justify-self-start"
                disabled={routes.length >= 64}
                onClick={() => {
                  setRoutes((current) => [
                    ...current,
                    {
                      key: nextKey,
                      match: "prefix",
                      path: current.length ? "/api" : "/",
                      worker_id: "",
                      enabled: true,
                    },
                  ]);
                  setNextKey((key) => key + 1);
                }}
              >
                <Plus size={16} />
                {t("添加路由")}
              </Button>
              {changesFallback ? (
                <p
                  role="status"
                  className="text-sm text-muted-foreground"
                >
                  {t("移除路由后，原路径可能命中更宽的路由。停用路由可保留路径并返回 410。")}
                </p>
              ) : null}
            </>
          )}
          {error ? (
            <p
              role="alert"
              className="text-sm text-destructive"
            >
              {error}
            </p>
          ) : null}
          <div className="flex flex-wrap justify-end gap-2">
            {item && !retiring ? (
              <Button
                type="button"
                variant="outline"
                disabled={busy}
                onClick={() => setRetiring(true)}
              >
                {t("退役域名")}
              </Button>
            ) : null}
            <Button
              type="button"
              variant="outline"
              disabled={busy}
              onClick={retiring ? () => setRetiring(false) : close}
            >
              {t("取消")}
            </Button>
            <Button
              type="submit"
              disabled={busy}
              variant={retiring ? "destructive" : "default"}
            >
              {retiring ? t("确认退役") : t("保存")}
            </Button>
          </div>
        </form>
      </DialogContent>
    </Dialog>
  );
}
