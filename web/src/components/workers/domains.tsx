import { useState } from "react";
import { ArrowUpRight, Plus } from "lucide-react";
import { api } from "@/lib/api";
import { errorMessage, t } from "@/lib/i18n";
import {
  type Environment,
  type WorkerDomain,
  type WorkerNetworkProfile,
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
  networkProfiles,
  currentProfile,
  environments,
  revision,
  active,
  reload,
  notify,
}: {
  base: string;
  domains: WorkerDomain[];
  networkProfiles: WorkerNetworkProfile[];
  currentProfile?: WorkerNetworkProfile;
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
  const describe = (d: WorkerDomain) =>
    d.label ? `*.${d.domain}（${d.label}）` : `*.${d.domain}`;

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

  async function selectProfile(profile: WorkerNetworkProfile) {
    if (profile.id === currentProfile?.id) return;
    setBusy(true);
    try {
      await api(base + "/network-profile", "PUT", {
        network_profile: profile.id,
        expected_revision: revision,
      });
      reload();
    } catch (e) {
      notify(errorMessage(e));
      reload();
    } finally {
      setBusy(false);
    }
  }

  const placementLocked = environments.some((environment) => environment.release_id);
  const profiles =
    currentProfile && !networkProfiles.some((profile) => profile.id === currentProfile.id)
      ? [currentProfile, ...networkProfiles]
      : networkProfiles;
  // The backend only returns domains allowed by the current profile. This
  // component deliberately does not infer or expose the underlying pool or
  // domain classification.
  const unbound = domains.filter((domain) => !bound.includes(domain.id));

  return (
    <>
      {profiles.length > 1 ? (
        <Card className="py-0 shadow-none">
          <CardContent className="grid gap-4 p-5">
            <div>
              <h3 className="font-medium">{t("服务网络")}</h3>
              <p className="mt-1 text-xs text-muted-foreground">
                {t("选择这个 Worker 使用的网络策略。具体资源池和基础设施由平台管理。")}
              </p>
            </div>
            <div className="grid gap-2 sm:grid-cols-3">
              {profiles.map((profile) => {
                const selected = currentProfile?.id === profile.id;
                return (
                  <button
                    key={profile.id}
                    type="button"
                    aria-pressed={selected}
                    disabled={busy || placementLocked || profile.approval_required}
                    className={`rounded-md border p-3 text-left transition-colors ${
                      selected
                        ? "border-primary bg-primary/5"
                        : "bg-secondary/20 hover:border-primary/40"
                    } disabled:opacity-50`}
                    onClick={() => void selectProfile(profile)}
                  >
                    <span className="flex items-center justify-between gap-2 text-sm font-medium">
                      {profile.name}
                      {profile.approval_required ? (
                        <Badge variant="outline" className="text-[10px]">
                          {t("需要平台审批")}
                        </Badge>
                      ) : null}
                    </span>
                    {profile.description ? (
                      <span className="mt-1 block text-xs text-muted-foreground">
                        {profile.description}
                      </span>
                    ) : null}
                  </button>
                );
              })}
            </div>
            {placementLocked ? (
              <p className="text-xs text-muted-foreground">
                {t("首次发布后服务网络由平台固定；如需调整请联系平台管理员。")}
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
                <h3 className="font-medium">{t("访问域名后缀")}</h3>
                <p className="mt-1 text-xs text-muted-foreground">
                  {t("这里展示的是地址后缀，不是可直接访问的地址。平台会为每个环境生成独立子域名，实际地址见下方。")}
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
                      <code className="min-w-0 break-all rounded bg-background px-2 py-1 font-mono text-[11px]">
                        *.{d.domain}
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
                      disabled={busy || bound.length <= 1}
                      title={
                        bound.length <= 1
                          ? t("至少需要保留一个域名后缀。如需替换，请先添加另一个后缀。")
                          : undefined
                      }
                      onClick={() => setRemove(d)}
                    >
                      {bound.length <= 1 ? t("必须保留") : t("解绑")}
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
                  {t("添加域名后缀")}
                </Button>
              </div>
            ) : null}
            {bound.length <= 1 ? (
              <p className="text-xs text-muted-foreground">
                {t("至少需要保留一个域名后缀。如需替换，请先添加另一个后缀。")}
              </p>
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
            <DialogTitle>{t("添加域名后缀")}</DialogTitle>
            <DialogDescription>
              {t("选择一个地址后缀。添加后，平台会在该后缀下为每个环境生成独立的访问地址。")}
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
            <DialogTitle>{t("解绑域名后缀")}</DialogTitle>
            <DialogDescription>
              {t("解绑后，该后缀下生成的所有环境访问地址会立即失效。已发布的版本不受影响。")}
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
