import { useEffect, useState } from "react";
import { Plus, Trash2 } from "lucide-react";
import { APIError, api } from "@/lib/api";
import { errorMessage, t } from "@/lib/i18n";
import { Field, Loading, PageHeader, SelectField } from "@/components/common";
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
  origin?: string;
  custom_domain?: boolean;
  enabled: boolean;
};
type Hostname = {
  host: string;
  suffix_id: string;
  type: "worker_custom" | "application";
  revision: number;
  routes: Route[];
};
type Suffix = { id: string; domain: string; label: string };
type Origin = { host: string; kind: "static" | "http"; label: string; suffixes: string[] };
type Catalog = { items: Hostname[]; suffixes: Suffix[]; origins: Origin[]; can_manage: boolean };
type Bindings = {
  items: Hostname[];
  suffixes: Suffix[];
  can_manage: boolean;
  can_read_catalog: boolean;
};

export function WorkerHostnameBindings({
  base,
  catalogHref,
}: {
  base: string;
  catalogHref: string;
}) {
  const path = base + "/hostnames";
  const [data, setData] = useState<Bindings>();
  const [error, setError] = useState("");
  const [revision, setRevision] = useState(0);
  const [editor, setEditor] = useState<Hostname | "new" | null>(null);
  useEffect(() => {
    const controller = new AbortController();
    setError("");
    void api<Bindings>(path, "GET", undefined, controller.signal)
      .then(setData)
      .catch((cause) => {
        if (!controller.signal.aborted) setError(errorMessage(cause));
      });
    return () => controller.abort();
  }, [path, revision]);

  const custom = data?.items.filter((item) => item.type === "worker_custom") || [];
  const applications = data?.items.filter((item) => item.type === "application") || [];

  return (
    <>
      <Card className="py-0 shadow-none">
        <CardContent className="grid gap-3 p-5">
          <div className="flex flex-wrap items-center justify-between gap-3">
            <h3 className="font-medium">{t("Worker 自定义域名")}</h3>
            {data?.can_manage && data.suffixes.length > 0 ? (
              <Button onClick={() => setEditor("new")}>
                <Plus size={16} />
                {t("绑定自定义域名")}
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
          ) : custom.length === 0 ? (
            <p className="text-sm text-muted-foreground">{t("此 Worker 尚未绑定自定义子域名。")}</p>
          ) : (
            custom.map((item) => (
              <HostnameCard
                key={item.host}
                item={item}
                edit={data.can_manage ? () => setEditor(item) : undefined}
              />
            ))
          )}
        </CardContent>
      </Card>
      {applications.length > 0 || data?.can_read_catalog ? (
        <Card className="py-0 shadow-none">
          <CardContent className="grid gap-3 p-5">
            <div className="flex flex-wrap items-center justify-between gap-3">
              <h3 className="font-medium">{t("应用域名")}</h3>
              {data?.can_read_catalog ? (
                <Button
                  variant="outline"
                  asChild
                >
                  <a href={catalogHref}>{t("管理应用域名")}</a>
                </Button>
              ) : null}
            </div>
            {applications.length > 0 ? (
              applications.map((item) => (
                <HostnameCard
                  key={item.host}
                  item={item}
                />
              ))
            ) : (
              <p className="text-sm text-muted-foreground">{t("此 Worker 尚无应用域名路由。")}</p>
            )}
          </CardContent>
        </Card>
      ) : null}
      {editor && data ? (
        <WorkerCustomDomainDialog
          path={path}
          item={editor === "new" ? undefined : editor}
          suffixes={data.suffixes}
          close={() => setEditor(null)}
          saved={() => {
            setEditor(null);
            setRevision((value) => value + 1);
          }}
        />
      ) : null}
    </>
  );
}

function WorkerCustomDomainDialog({
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
  saved: () => void;
}) {
  const [suffix, setSuffix] = useState(suffixes[0]?.id || "");
  const [label, setLabel] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  async function submit() {
    setBusy(true);
    setError("");
    try {
      if (item) {
        await api(
          path + "/" + encodeURIComponent(item.host) + "?revision=" + item.revision,
          "DELETE",
        );
      } else {
        await api(path, "POST", { suffix_id: suffix, label });
      }
      saved();
    } catch (cause) {
      setError(errorMessage(cause));
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
      <DialogContent className="sm:max-w-3xl">
        <DialogHeader>
          <DialogTitle>{item ? t("解绑自定义域名") : t("绑定自定义域名")}</DialogTitle>
          <DialogDescription>
            {item
              ? t("解绑后，该域名将停止转发到此 Worker。")
              : t("该域名的所有路径均指向此 Worker 的生产环境。")}
          </DialogDescription>
        </DialogHeader>
        <form
          className="grid gap-4"
          onSubmit={(event) => {
            event.preventDefault();
            void submit();
          }}
        >
          {item ? (
            <code>{item.host}</code>
          ) : (
            <HostnameFields
              label={label}
              setLabel={setLabel}
              suffix={suffix}
              setSuffix={setSuffix}
              suffixes={suffixes}
            />
          )}
          {error ? (
            <p
              role="alert"
              className="text-sm text-destructive"
            >
              {error}
            </p>
          ) : null}
          <div className="flex justify-end gap-2">
            <Button
              type="button"
              variant="outline"
              disabled={busy}
              onClick={close}
            >
              {t("取消")}
            </Button>
            <Button
              type="submit"
              disabled={busy}
              variant={item ? "destructive" : "default"}
            >
              {item ? t("确认解绑") : t("绑定域名")}
            </Button>
          </div>
        </form>
      </DialogContent>
    </Dialog>
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
            {item.type === "worker_custom" ? t("解绑自定义域名") : t("编辑路由")}
          </Button>
        ) : null}
      </div>
      {item.type === "worker_custom" ? (
        item.routes[0]?.enabled === false ? (
          <Badge variant="secondary">{t("已停用")}</Badge>
        ) : null
      ) : item.routes.length > 0 ? (
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
              <code className="break-all">
                {route.origin || route.worker_locator || route.worker_id}
              </code>
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
  const path = base + "/domains";
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
    <>
      <PageHeader
        title={t("应用域名")}
        description={t("按路径把同一域名的流量分给 Worker、静态站点和 HTTP 服务。")}
      />
      <Card className="py-0 shadow-none">
        <CardContent className="grid gap-4 p-5">
          <div className="flex flex-wrap items-start justify-between gap-3">
            <h2 className="font-semibold">{t("应用域名列表")}</h2>
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
                ? t("还没有应用域名。")
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
            origins={catalog.origins || []}
            close={() => setEditor(null)}
            saved={(retired) => {
              setEditor(null);
              setNotice(
                retired ? t("域名已退役，路由正在撤除。") : t("配置已保存，路由正在同步。"),
              );
              refresh();
            }}
          />
        ) : null}
      </Card>
    </>
  );
}

function HostnameEditor({
  path,
  item,
  suffixes,
  origins,
  close,
  saved,
}: {
  path: string;
  item?: Hostname;
  suffixes: Suffix[];
  origins: Origin[];
  close: () => void;
  saved: (retired: boolean) => void;
}) {
  const [suffix, setSuffix] = useState(suffixes[0]?.id || "");
  const [label, setLabel] = useState("");
  const [routes, setRoutes] = useState(() =>
    (item?.routes || []).map((route, key) => ({ ...route, key })),
  );
  const availableOrigins = origins.filter((origin) =>
    origin.suffixes.includes(item?.suffix_id || suffix),
  );
  const routeLimit = routes.some((route) => route.origin) ? 16 : 64;
  const [nextKey, setNextKey] = useState(routes.length);
  const [retiring, setRetiring] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
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
      const input = routes.map(({ match, path, worker_id, origin, enabled }) => ({
        match,
        path,
        ...(origin ? { origin } : { worker_id }),
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
        await api(path, "POST", { suffix_id: suffix, label });
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
            {item
              ? t("精确路径优先，其次匹配最长的路径段前缀。")
              : t("申请后可在域名详情配置路由。")}
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
                <HostnameFields
                  label={label}
                  setLabel={setLabel}
                  suffix={suffix}
                  setSuffix={setSuffix}
                  suffixes={suffixes}
                />
              ) : null}
              {item
                ? routes.map((route, index) => (
                    <div
                      key={route.key}
                      className="grid gap-3 rounded-lg border p-3"
                    >
                      <div className="grid gap-3 sm:grid-cols-2">
                        <SelectField
                          label={t("匹配方式")}
                          value={route.match}
                          onChange={(match) =>
                            changeRoute(route.key, {
                              match: match === "exact" ? "exact" : "prefix",
                            })
                          }
                        >
                          <option value="prefix">{matchLabel("prefix")}</option>
                          <option value="exact">{matchLabel("exact")}</option>
                        </SelectField>
                        <Field label={t("请求路径")}>
                          <Input
                            required
                            value={route.path}
                            onChange={(event) =>
                              changeRoute(route.key, { path: event.target.value })
                            }
                          />
                        </Field>
                        <SelectField
                          label={t("目标类型")}
                          value={route.origin ?? ""}
                          onChange={(origin) =>
                            changeRoute(route.key, { origin: origin || undefined, worker_id: "" })
                          }
                        >
                          <option value="">{t("Worker")}</option>
                          {availableOrigins.map((origin) => (
                            <option
                              key={origin.host}
                              value={origin.host}
                            >
                              {origin.kind === "static" ? t("静态站点") : t("HTTP 服务")} ·{" "}
                              {origin.label || origin.host}
                            </option>
                          ))}
                          {route.origin &&
                          !availableOrigins.some((origin) => origin.host === route.origin) ? (
                            <option value={route.origin}>{route.origin}</option>
                          ) : null}
                        </SelectField>
                        {route.origin ? null : (
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
                        )}
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
                          onClick={() =>
                            setRoutes((current) => current.filter((_, i) => i !== index))
                          }
                        >
                          <Trash2 size={14} />
                          {t("移除路由")}
                        </Button>
                      </div>
                    </div>
                  ))
                : null}
              {item ? (
                <Button
                  type="button"
                  variant="outline"
                  className="justify-self-start"
                  disabled={routes.length >= routeLimit}
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
              ) : null}
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
              {retiring ? t("确认退役") : item ? t("保存") : t("申请域名")}
            </Button>
          </div>
        </form>
      </DialogContent>
    </Dialog>
  );
}

function HostnameFields({
  label,
  setLabel,
  suffix,
  setSuffix,
  suffixes,
}: {
  label: string;
  setLabel: (label: string) => void;
  suffix: string;
  setSuffix: (suffix: string) => void;
  suffixes: Suffix[];
}) {
  const preview = suffixes.find((value) => value.id === suffix)?.domain;
  return (
    <>
      <div className="grid gap-3 sm:grid-cols-2">
        <Field label={t("域名前缀")}>
          <Input
            required
            maxLength={63}
            value={label}
            onChange={(event) => setLabel(event.target.value)}
            placeholder="shop"
          />
        </Field>
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
      </div>
      <code className="break-all text-sm text-muted-foreground">
        {label.toLowerCase() || "shop"}.{preview}
      </code>
    </>
  );
}
