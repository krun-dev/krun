import { useState } from "react";
import { ArrowUpRight, Plus } from "lucide-react";
import { api } from "@/lib/api";
import { errorMessage, t } from "@/lib/i18n";
import { type Environment, type WorkerDomain } from "@/lib/workers";
import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
import { Card, CardContent } from "@/components/ui/card";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { workerURL } from "./shared";

// ServiceDomains is the service-level routing configuration: which of the
// platform's domains this service answers on. Every environment gets one host
// per selected domain, so this is a property of the service, not of an
// environment.
export function ServiceDomains({
  base,
  domains,
  environments,
  active,
  reload,
  notify,
}: {
  base: string;
  domains: WorkerDomain[];
  environments: Environment[];
  active: boolean;
  reload: () => void;
  notify: (s: string) => void;
}) {
  // Binding and unbinding apply immediately; there is no draft state to save.
  const bound = environments[0]?.domains ?? [];
  const [binding, setBinding] = useState(false);
  const [busy, setBusy] = useState(false);
  const [remove, setRemove] = useState<WorkerDomain | null>(null);
  // Catalog entries already bound are not offered again; the rest can be added.
  const unbound = domains.filter((d) => !bound.includes(d.id));
  const describe = (d: WorkerDomain) =>
    d.label ? `${d.domain}（${d.label}）` : d.domain;

  async function persist(next: string[]) {
    setBusy(true);
    try {
      await api<{ selected: string[] }>(base + "/domains", "PUT", {
        domains: next,
      });
      reload();
      return true;
    } catch (e) {
      notify(errorMessage(e));
      return false;
    } finally {
      setBusy(false);
    }
  }

  async function unbind(d: WorkerDomain) {
    if (bound.length <= 1) {
      notify(t("至少保留一个域名。"));
      return;
    }
    setRemove(null);
    await persist(bound.filter((id) => id !== d.id));
  }

  return (
    <>
      <Card className="py-0 shadow-none">
        <CardContent className="grid gap-4 p-5">
          <div className="flex flex-wrap items-start justify-between gap-3">
            <div>
              <h3 className="font-medium">{t("已绑定域名")}</h3>
              <p className="mt-1 text-xs text-muted-foreground">
                {t(
                  "每个环境会在每个已绑定域名下各有一个访问地址。括号内是备注，只用于说明。",
                )}
              </p>
            </div>
          </div>
          {bound.length === 0 ? (
            <p className="text-sm text-muted-foreground">
              {t("还没有绑定域名，绑定后环境才能被访问。")}
            </p>
          ) : (
            <div className="grid gap-2">
              {bound.map((id) => {
                const d = domains.find((x) => x.id === id);
                if (!d) return null;
                return (
                  <div
                    key={id}
                    className="flex flex-wrap items-center justify-between gap-2 rounded-md border bg-secondary/20 px-3 py-2"
                  >
                    <div className="flex min-w-0 flex-wrap items-center gap-2">
                      <Badge variant="secondary" className="shrink-0 text-[10px]">
                        {d.label || t("域名")}
                      </Badge>
                      <code className="min-w-0 break-all rounded bg-background px-2 py-1 font-mono text-[11px]">
                        {d.domain}
                      </code>
                      <span className="text-[11px] text-muted-foreground">
                        → {t("网关")} {d.gateway}
                      </span>
                    </div>
                    <Button
                      variant="outline"
                      size="sm"
                      disabled={busy}
                      onClick={() => setRemove(d)}
                    >
                      {t("解绑")}
                    </Button>
                  </div>
                );
              })}
            </div>
          )}
          {unbound.length > 0 ? (
            <div className="flex flex-wrap items-center gap-2">
              <Button
                variant="outline"
                size="sm"
                disabled={busy}
                onClick={() => setBinding(true)}
              >
                <Plus size={14} />
                {t("绑定域名")}
              </Button>
            </div>
          ) : null}
          {active ? (
            <p className="text-xs text-muted-foreground">
              {t("有发布正在进行，域名变更会在下次下发时生效。")}
            </p>
          ) : null}
        </CardContent>
      </Card>
      <Card className="py-0 shadow-none">
        <CardContent className="p-5">
          <h3 className="mb-1 font-medium">{t("各环境的访问地址")}</h3>
          <p className="mb-4 text-xs text-muted-foreground">
            {t("每个环境在已绑定的每个域名下各有一条地址。")}
          </p>
          <div className="grid gap-3">
            {environments.map((e) => (
              <div key={e.name} className="rounded-md border bg-secondary/20 p-3">
                <div className="mb-2 flex flex-wrap items-center gap-2">
                  <span className="text-sm font-medium">{e.name}</span>
                  {e.name === "production" ? (
                    <Badge variant="outline" className="text-[10px]">
                      {t("基线")}
                    </Badge>
                  ) : null}
                  {!e.enabled ? (
                    <Badge variant="outline" className="text-[10px]">
                      {t("未启用")}
                    </Badge>
                  ) : null}
                </div>
                {e.hosts.length === 0 ? (
                  <p className="text-xs text-muted-foreground">
                    {t("未绑定域名")}
                  </p>
                ) : (
                  <div className="grid gap-1.5">
                    {e.hosts.map((host, i) => {
                      const d = domains.find(
                        (x) => x.id === (e.domains || [])[i],
                      );
                      return (
                        <div
                          key={host}
                          className="flex flex-wrap items-center gap-2"
                        >
                          <Badge
                            variant="secondary"
                            className="shrink-0 text-[10px]"
                          >
                            {d?.label || t("域名")}
                          </Badge>
                          <a
                            href={workerURL(host)}
                            target="_blank"
                            rel="noreferrer"
                            className="inline-flex min-w-0 items-center gap-1 break-all rounded bg-background px-2 py-1 font-mono text-[11px] text-muted-foreground underline decoration-dotted underline-offset-2 hover:text-foreground"
                          >
                            {host}
                            <ArrowUpRight size={11} className="shrink-0" />
                          </a>
                        </div>
                      );
                    })}
                  </div>
                )}
              </div>
            ))}
          </div>
        </CardContent>
      </Card>
      <Dialog open={binding} onOpenChange={(v) => !busy && setBinding(v)}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>{t("绑定域名")}</DialogTitle>
            <DialogDescription>
              {t("从平台已配置的域名中选择；绑定后每个环境各多一个访问地址。")}
            </DialogDescription>
          </DialogHeader>
          <div className="grid gap-1.5">
            {unbound.map((d) => (
              <button
                key={d.id}
                type="button"
                disabled={busy}
                className="flex flex-wrap items-center justify-between gap-2 rounded-md border px-3 py-2 text-left text-sm hover:bg-secondary disabled:opacity-50"
                onClick={async () => {
                  setBinding(false);
                  await persist([...bound, d.id]);
                }}
              >
                <code className="break-all text-xs">{describe(d)}</code>
                <Plus size={14} />
              </button>
            ))}
          </div>
        </DialogContent>
      </Dialog>
      <Dialog open={!!remove} onOpenChange={(v) => !v && !busy && setRemove(null)}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>{t("解绑域名")}</DialogTitle>
            <DialogDescription>
              {t("解绑后该域名下所有环境的地址立即失效。已发布的版本不受影响。")}
            </DialogDescription>
          </DialogHeader>
          {remove ? (
            <code className="break-all text-sm">{describe(remove)}</code>
          ) : null}
          <Button
            variant="destructive"
            disabled={busy}
            onClick={() => remove && void unbind(remove)}
          >
            {busy ? t("解绑中…") : t("确认解绑")}
          </Button>
        </DialogContent>
      </Dialog>
    </>
  );
}
