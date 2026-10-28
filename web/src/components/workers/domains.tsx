import { useState } from "react";
import { ArrowUpRight, Plus } from "lucide-react";
import { api } from "@/lib/api";
import { errorMessage, t } from "@/lib/i18n";
import {
  type Environment,
  type WorkerDomain,
  type WorkerNetworkOption,
} from "@/lib/workers";
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
  networkOptions,
  environments,
  revision,
  active,
  reload,
  notify,
}: {
  base: string;
  domains: WorkerDomain[];
  networkOptions: WorkerNetworkOption[];
  environments: Environment[];
  revision: number;
  active: boolean;
  reload: () => void;
  notify: (s: string) => void;
}) {
  // Binding and unbinding apply immediately; there is no draft state to save.
  const bound = environments[0]?.domains ?? [];
  const [binding, setBinding] = useState(false);
  const [busy, setBusy] = useState(false);
  const [remove, setRemove] = useState<WorkerDomain | null>(null);
  const accessLabel = (access: WorkerDomain["access"]) =>
    access === "private" ? t("内网") : t("公网");
  const describe = (d: WorkerDomain) =>
    d.label ? `${d.domain}（${d.label}）` : d.domain;

  async function persist(next: string[]) {
    setBusy(true);
    try {
      await api<{ selected: string[]; revision: number }>(base + "/domains", "PUT", {
        domains: next,
        expected_revision: revision,
      });
      reload();
      return true;
    } catch (e) {
      notify(errorMessage(e));
      // A revision conflict means another console or replica won. Refresh the
      // selected domains and token before the user retries.
      reload();
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

  async function selectNetwork(option: WorkerNetworkOption) {
    const allowed = new Set(option.access);
    const next = bound.filter((id) => {
      const access = domains.find((domain) => domain.id === id)?.access;
      return access ? allowed.has(access) : false;
    });
    for (const access of option.access) {
      if (next.some((id) => domains.find((domain) => domain.id === id)?.access === access)) {
        continue;
      }
      const candidates = domains.filter((domain) => domain.access === access);
      const selected = candidates.find((domain) => domain.default) || candidates[0];
      if (selected) next.push(selected.id);
    }
    await persist(next);
  }

  const selectedAccess = (["public", "private"] as const).filter((access) =>
    domains.some(
      (domain) => domain.access === access && bound.includes(domain.id),
    ),
  );
  const selectedNetwork = selectedAccess.join("+");
  const placementLocked = environments.some((environment) => environment.release_id);
  // Domain aliases are an advanced choice inside the selected access classes;
  // changing access itself is only done by the network cards above.
  const unbound = domains.filter(
    (domain) =>
      !bound.includes(domain.id) && selectedAccess.includes(domain.access),
  );

  return (
    <>
      {networkOptions.length > 1 ? (
        <Card className="py-0 shadow-none">
          <CardContent className="grid gap-4 p-5">
            <div>
              <h3 className="font-medium">{t("网络访问")}</h3>
              <p className="mt-1 text-xs text-muted-foreground">
                {t("选择用户从公网、内网或两者访问这个 Worker。运行资源由平台自动安排。")}
              </p>
            </div>
            <div className="grid gap-2 sm:grid-cols-3">
              {networkOptions.map((option) => {
                const selected = selectedNetwork === option.id;
                return (
                  <button
                    key={option.id}
                    type="button"
                    aria-pressed={selected}
                    disabled={busy || placementLocked}
                    className={`rounded-md border p-3 text-left transition-colors ${
                      selected
                        ? "border-primary bg-primary/5"
                        : "bg-secondary/20 hover:border-primary/40"
                    } disabled:opacity-50`}
                    onClick={() => void selectNetwork(option)}
                  >
                    <span className="block text-sm font-medium">
                      {option.access.map(accessLabel).join(" + ")}
                    </span>
                    <span className="mt-1 block text-xs text-muted-foreground">
                      {option.access.length > 1
                        ? t("同时通过公网和内网入口访问。")
                        : option.access[0] === "private"
                          ? t("通过部署方配置的内网入口访问。")
                          : t("通过部署方配置的公网入口访问。")}
                    </span>
                  </button>
                );
              })}
            </div>
            {placementLocked ? (
              <p className="text-xs text-muted-foreground">
                {t("首次发布后网络访问方式由平台固定；如需调整请联系平台管理员。")}
              </p>
            ) : null}
          </CardContent>
        </Card>
      ) : null}
      {domains.length > 1 ? (
        <Card className="py-0 shadow-none">
          <CardContent className="grid gap-4 p-5">
            <div className="flex flex-wrap items-start justify-between gap-3">
              <div>
                <h3 className="font-medium">{t("访问域名")}</h3>
                <p className="mt-1 text-xs text-muted-foreground">
                  {t("每个环境会在每个已绑定域名下各有一个访问地址。")}
                </p>
              </div>
            </div>
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
                        {accessLabel(d.access)}
                      </Badge>
                      <code className="min-w-0 break-all rounded bg-background px-2 py-1 font-mono text-[11px]">
                        {d.domain}
                      </code>
                      {d.label ? (
                        <span className="text-[11px] text-muted-foreground">
                          {d.label}
                        </span>
                      ) : null}
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
      ) : null}
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
                            {d ? accessLabel(d.access) : t("域名")}
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
