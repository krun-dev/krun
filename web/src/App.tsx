import { LanguageSwitch } from "@/components/language";
import { errorMessage, t } from "@/lib/i18n";
import { useTranslation } from "react-i18next";
import { lazy, Suspense, useCallback, useEffect, useState } from "react";
import {
  Activity,
  ArrowUpRight,
  Building2,
  Check,
  CircleHelp,
  Clipboard,
  KeyRound,
  LayoutDashboard,
  Layers3,
  LogOut,
  Sun,
  Moon,
  PanelLeft,
  Plus,
  Search,
  ShieldCheck,
  Terminal,
  Users,
  UsersRound,
} from "lucide-react";
import { Button } from "@/components/ui/button";
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Badge } from "@/components/ui/badge";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import {
  api,
  APIError,
  type List,
  type Me,
  type Organization,
  type Provider,
} from "@/lib/api";
import { cn } from "@/lib/utils";
import {
  Empty,
  Field,
  Loading,
  Logo,
  Metric,
  Notice,
  PageHeader,
  QuickLink,
  SelectField,
} from "@/components/common";
import { Login } from "@/components/login";
import {
  IAMPage,
  CheckPage,
  UsersPage,
  ResourcesPage,
} from "@/components/management";

import { AuditPage } from "@/components/audit";
const WorkersPage = lazy(() =>
  import("@/components/workers").then((m) => ({ default: m.WorkersPage })),
);
import { KeysPage } from "@/components/keys";

type Section =
  | "overview"
  | "members"
  | "groups"
  | "roles"
  | "keys"
  | "check"
  | "audit"
  | "users"
  | "resources"
  | "workers";
const navigation: { id: Section; name: string; icon: typeof Users; group: string }[] = [
  { id: "overview", name: "概览", icon: LayoutDashboard, group: "" },
  { id: "workers", name: "Workers", icon: Layers3, group: "计算服务" },
  { id: "members", name: "组织成员", icon: Users, group: "组织管理" },
  { id: "groups", name: "用户组", icon: UsersRound, group: "组织管理" },
  { id: "roles", name: "角色授权", icon: ShieldCheck, group: "组织管理" },
  { id: "keys", name: "API Keys", icon: KeyRound, group: "组织管理" },
  { id: "resources", name: "资源目录", icon: Building2, group: "组织管理" },
  { id: "check", name: "权限检查", icon: Search, group: "组织管理" },
  { id: "audit", name: "审计记录", icon: Activity, group: "组织管理" },
  { id: "users", name: "系统用户", icon: Users, group: "系统" },
];
export default function App() {
  useTranslation();
  const [theme, setTheme] = useState(() =>
    document.documentElement.classList.contains("dark") ? "dark" : "light",
  );
  useEffect(() => {
    document.documentElement.classList.toggle("dark", theme === "dark");
    try {
      localStorage.setItem("krun.theme", theme);
    } catch {
      /* Theme still works without storage. */
    }
  }, [theme]);
  const [me, setMe] = useState<Me | null>(null),
    [providers, setProviders] = useState<Provider[]>([]),
    [development, setDevelopment] = useState(false),
    [loading, setLoading] = useState(true),
    [fatal, setFatal] = useState("");
  const [orgs, setOrgs] = useState<Organization[]>([]),
    [scope, setScope] = useState(
      new URLSearchParams(window.location.search).get("workspace") || "",
    ),
    [section, setSection] = useState<Section>(
      new URLSearchParams(window.location.search).get("page") === "workers"
        ? "workers"
        : "overview",
    ),
    [notice, setNotice] = useState(""),
    [refresh, setRefresh] = useState(0),
    [menu, setMenu] = useState(false);
  const [workersNavigation, setWorkersNavigation] = useState(0);
  const [orgDialog, setOrgDialog] = useState(false),
    [orgName, setOrgName] = useState(""),
    [saving, setSaving] = useState(false),
    [copied, setCopied] = useState(false);
  const [device, setDevice] = useState(
    new URLSearchParams(window.location.search).get("cli") ?? "",
  );
  const notify = useCallback((message: string) => setNotice(message), []);
  const reload = useCallback(() => setRefresh((x) => x + 1), []);
  useEffect(() => {
    let active = true;
    Promise.all([
      api<{ providers: Provider[]; development: boolean }>("/api/auth/providers"),
      api<Me>("/api/auth/me").catch((e) => {
        if (e instanceof APIError && e.status === 401) return null;
        throw e;
      }),
    ])
      .then(([p, u]) => {
        if (active) {
          setProviders(p.providers);
          setDevelopment(p.development);
          setMe(u);
          setLoading(false);
        }
      })
      .catch(() => {
        if (active) {
          setFatal(t("无法连接控制服务，请检查服务状态后刷新。"));
          setLoading(false);
        }
      });
    return () => {
      active = false;
    };
  }, []);
  useEffect(() => {
    if (!me) return;
    let active = true;
    api<List<Organization>>("/api/v1/orgs")
      .then((data) => {
        if (active) {
          setOrgs(data.items);
          setScope((current) =>
            current === "system" || data.items.some((o) => o.id === current)
              ? current
              : (data.items[0]?.id ?? "system"),
          );
        }
      })
      .catch((e) => {
        if (active) notify(errorMessage(e));
      });
    return () => {
      active = false;
    };
  }, [me, refresh, notify]);
  async function mutate(operation: () => Promise<unknown>, message: string) {
    setSaving(true);
    try {
      await operation();
      notify(message);
      reload();
    } catch (e) {
      notify(errorMessage(e));
    } finally {
      setSaving(false);
    }
  }
  const org = orgs.find((o) => o.id === scope),
    system = scope === "system",
    scopeName = system ? t("全部组织") : (org?.name ?? t("选择组织"));
  async function copyID() {
    try {
      await navigator.clipboard.writeText(me!.user.user_id);
      setCopied(true);
      setTimeout(() => setCopied(false), 2000);
    } catch {
      notify(t("复制失败，请手动选中用户 ID。"));
    }
  }
  if (loading) return <Loading />;
  if (fatal)
    return (
      <div className="m-auto max-w-lg p-10">
        <div className="flex items-center justify-between gap-4">
          <Logo />
          <LanguageSwitch />
        </div>
        <p role="alert" className="mt-8">
          {t(fatal)}
        </p>
        <Button className="mt-4" onClick={() => window.location.reload()}>
          {t("重试")}
        </Button>
      </div>
    );
  if (!me)
    return (
      <Login
        providers={providers}
        development={development}
        error={
          new URLSearchParams(window.location.search).get("error") ?? undefined
        }
      />
    );
  const base = system
    ? "/api/v1/system"
    : `/api/v1/orgs/${encodeURIComponent(scope)}`;
  return (
    <div className="min-h-screen lg:pl-[232px]">
      <aside
        className={cn(
          "fixed inset-y-0 left-0 z-30 w-[232px] flex-col border-r bg-card px-3 py-5 transition-transform lg:translate-x-0",
          menu
            ? "flex translate-x-0 shadow-lg"
            : "hidden -translate-x-full lg:flex",
        )}
      >
        <div className="px-3">
          <Logo />
        </div>
        <div className="mt-6 rounded-md border bg-background/50 p-3">
          <div className="mb-2 flex items-center gap-2 text-[10px] font-medium uppercase tracking-widest text-muted-foreground">
            <Building2 size={12} />
            {t("组织")}
          </div>
          <div className="relative">
            <SelectField
              label={t("切换组织")}
              value={scope}
              onChange={(v) => {
                setScope(v);
                if (v === "system" && !me.system_admin) setSection("keys");
                setMenu(false);
              }}
            >
              <option value="" disabled>
                {t("选择组织")}
              </option>
              {orgs.map((o) => (
                <option key={o.id} value={o.id}>
                  {o.name}
                </option>
              ))}
              <option value="system">{t("全部组织")}</option>
            </SelectField>
          </div>
        </div>
        <div className="mt-7 px-3 text-[10px] font-semibold uppercase tracking-widest text-muted-foreground">
          {t("平台控制台")}
        </div>
        <nav className="mt-3 grid gap-1">
          {[
            ...navigation.filter((n) => n.group === ""),
            ...navigation.filter((n) => n.group === "计算服务"),
            ...navigation.filter((n) => n.group === "组织管理"),
            ...navigation.filter((n) => n.group === "系统"),
          ]
            .filter(
              (n) =>
                !system ||
                me.system_admin ||
                ["overview", "keys", "check"].includes(n.id),
            )
            .map((n, index, all) => (
              <div key={n.id} className="contents">
                {index > 0 && n.group !== all[index - 1].group && (
                  <div className="mt-5 px-3 pb-1 text-[10px] font-semibold uppercase tracking-widest text-muted-foreground/80">
                    {n.group === "" ? t("通用") : t(n.group)}
                  </div>
                )}
                <button
                  onClick={() => {
                    if (n.id === "workers") {
                      const url = new URL(location.href);
                      url.searchParams.set("page", "workers");
                      if (scope) url.searchParams.set("workspace", scope);
                      url.searchParams.delete("worker");
                      url.searchParams.delete("environment");
                      history.replaceState(null, "", url);
                      setWorkersNavigation((v) => v + 1);
                    }
                    setSection(n.id);
                    setMenu(false);
                  }}
                  className={cn(
                    "flex items-center gap-3 rounded-md px-3 py-2.5 text-left text-[13px] transition",
                    section === n.id
                      ? "bg-secondary font-medium text-foreground"
                      : "text-muted-foreground hover:bg-secondary/60 hover:text-foreground",
                  )}
                >
                  {n.group === "" ? null : (
                    <span className="w-[17px] shrink-0" />
                  )}
                  <n.icon size={17} />
                  {t(n.name)}
                  {section === n.id && (
                    <span className="ml-auto size-1.5 rounded-full bg-primary" />
                  )}
                </button>
              </div>
            ))}
        </nav>
        <div className="mt-auto pt-8">
          <div className="rounded-md border bg-background/50 p-4">
            <Terminal size={19} className="text-primary" />
            <p className="mt-3 text-sm font-medium">{t("连接终端")}</p>
            <p className="my-2 text-xs leading-relaxed text-muted-foreground">
              {t("通过浏览器登录，安全地管理你的组织。")}
            </p>
            <code className="block rounded-md bg-secondary/60 px-2 py-1.5 text-[11px]">
              $ krun login
            </code>
          </div>
          <div className="mt-4 flex items-center gap-2 px-3 text-xs text-muted-foreground">
            <CircleHelp size={13} />
            {t("krun Control Plane")}{" "}
            <Badge variant="outline" className="ml-auto text-[9px]">
              v0.1
            </Badge>
          </div>
        </div>
      </aside>
      {menu && (
        <button
          className="fixed inset-0 z-20 bg-black/20 lg:hidden"
          aria-label={t("关闭导航")}
          onClick={() => setMenu(false)}
        />
      )}
      <header className="flex h-[56px] items-center justify-between border-b bg-card px-5 md:px-9">
        <div className="flex min-w-0 flex-1 items-center gap-3">
          <Button
            className="lg:hidden"
            variant="ghost"
            size="icon"
            aria-label={t("打开导航")}
            onClick={() => setMenu(true)}
          >
            <PanelLeft size={18} />
          </Button>
          <span className="hidden shrink-0 text-sm text-muted-foreground sm:inline">
            {t("组织")}
          </span>
          <span className="hidden text-muted-foreground/40 sm:inline">/</span>
          <span className="truncate text-sm font-medium">{scopeName}</span>
          <span className="hidden text-muted-foreground/40 xl:inline">/</span>
          <span className="hidden shrink-0 text-xs text-muted-foreground xl:inline">
            {t(navigation.find((n) => n.id === section)?.name ?? "系统用户")}
          </span>
        </div>
        <div className="ml-3 flex shrink-0 items-center gap-3">
          <LanguageSwitch />
          <Button
            variant="ghost"
            size="icon-sm"
            aria-label={
              theme === "dark" ? t("切换浅色主题") : t("切换深色主题")
            }
            onClick={() =>
              setTheme((value) => (value === "dark" ? "light" : "dark"))
            }
          >
            {theme === "dark" ? <Sun size={16} /> : <Moon size={16} />}
          </Button>
          {development && (
            <Badge
              variant="outline"
              className="hidden border-primary/25 bg-primary/5 text-primary sm:flex"
            >
              {t("开发环境")}
            </Badge>
          )}
          <div className="flex size-8 items-center justify-center rounded-full bg-primary/10 text-xs font-semibold text-primary">
            {me.user.name.slice(0, 1).toUpperCase() || "K"}
          </div>
          <span className="hidden max-w-28 truncate text-sm xl:inline">
            {me.user.name}
          </span>
          <Button
            size="icon"
            variant="ghost"
            aria-label={t("退出登录")}
            disabled={saving}
            onClick={async () => {
              setSaving(true);
              try {
                await api("/api/auth/logout", "POST", {});
                window.location.assign("/");
              } catch (e) {
                notify(errorMessage(e));
                setSaving(false);
              }
            }}
          >
            <LogOut size={16} />
          </Button>
        </div>
      </header>
      <main className="mx-auto max-w-[1440px] px-5 py-7 md:px-9 md:py-8">
        {notice && <Notice message={notice} close={() => setNotice("")} />}
        {device && (
          <Card className="mb-6 border-primary/20 bg-primary/5 py-0 shadow-none">
            <CardContent className="flex flex-wrap items-center justify-between gap-4 p-5">
              <div>
                <p className="flex items-center gap-2 font-medium">
                  <Terminal size={18} />
                  {t("确认终端登录")}
                </p>
                <p className="mt-2 text-sm text-muted-foreground">
                  {t("请核对终端显示的代码：")}
                  <code className="ml-2 font-semibold text-foreground">
                    {device}
                  </code>
                </p>
                <p className="mt-1 text-xs text-muted-foreground">
                  {t("确认后签发 1 小时凭据，权限随你的当前角色变化。")}
                </p>
              </div>
              <div className="flex gap-2">
                <Button
                  variant="outline"
                  onClick={() => {
                    setDevice("");
                    window.history.replaceState(null, "", "/");
                  }}
                >
                  {t("取消")}
                </Button>
                <Button
                  disabled={saving}
                  onClick={() =>
                    mutate(async () => {
                      await api("/api/auth/cli/approve", "POST", {
                        user_code: device,
                      });
                      setDevice("");
                      window.history.replaceState(null, "", "/");
                    }, t("已批准 CLI 登录，可返回终端。"))
                  }
                >
                  {t("确认登录")}
                </Button>
              </div>
            </CardContent>
          </Card>
        )}
        {section === "overview" ? (
          <>
            <PageHeader
              eyebrow={t("Workspace overview")}
              title={t("组织概览")}
              description={t("从这里管理团队身份、访问权限与自动化凭据。")}
              action={
                me.system_admin ? (
                  <Button onClick={() => setOrgDialog(true)}>
                    <Plus size={16} />
                    {t("创建组织")}
                  </Button>
                ) : undefined
              }
            />
            <div className="grid gap-4 sm:grid-cols-3">
              <Metric
                label={t("可访问组织")}
                value={orgs.length}
                icon={<Building2 size={18} />}
                note={t("只展示当前身份获准访问的组织")}
              />
              <Metric
                label={t("已启用身份来源")}
                value={providers.length}
                icon={<ShieldCheck size={18} />}
                note={t("每个来源对应独立的平台账号")}
              />
              <Metric
                label={t("当前身份")}
                value={
                  <span className="text-xl">
                    {me.system_admin ? t("系统管理员") : t("平台用户")}
                  </span>
                }
                icon={<Users size={18} />}
                note={t("权限修订 {{v0}} · 每次请求重新检查", {
                  v0: me.revision,
                })}
              />
            </div>
            <div className="mt-6 grid gap-6 xl:grid-cols-[1.35fr_1fr]">
              <Card className="gap-0 shadow-none">
                <CardHeader className="border-b pb-5">
                  <CardTitle className="text-base">{t("我的组织")}</CardTitle>
                  <CardDescription>
                    {t("协作与权限隔离的组织")}
                  </CardDescription>
                </CardHeader>
                <CardContent className="pt-5">
                  {orgs.length ? (
                    orgs.map((o) => (
                      <button
                        className="mb-2 flex w-full items-center gap-4 rounded-lg border p-4 text-left hover:bg-secondary/50"
                        key={o.id}
                        onClick={() => {
                          setScope(o.id);
                          setSection("members");
                        }}
                      >
                        <div className="rounded-lg bg-secondary p-2.5">
                          <Building2 size={18} />
                        </div>
                        <div>
                          <p className="text-sm font-medium">{o.name}</p>
                          <p className="mt-1 font-mono text-[11px] text-muted-foreground">
                            {o.id}
                          </p>
                        </div>
                        <ArrowUpRight
                          className="ml-auto text-muted-foreground"
                          size={16}
                        />
                      </button>
                    ))
                  ) : (
                    <Empty
                      title={t("尚未加入组织")}
                      description={
                        me.system_admin
                          ? t("创建组织后即可管理成员与权限。")
                          : t(
                              "把下方的本地用户 ID 提供给管理员，申请组织成员资格与角色。",
                            )
                      }
                    />
                  )}
                </CardContent>
              </Card>
              <Card className="gap-0 shadow-none">
                <CardHeader className="border-b pb-5">
                  <CardTitle className="text-base">{t("快速开始")}</CardTitle>
                  <CardDescription>
                    {t("把权限配置成团队需要的样子")}
                  </CardDescription>
                </CardHeader>
                <CardContent className="grid gap-3 pt-5">
                  <QuickLink
                    title={t("设置用户组与角色")}
                    description={t("让团队通过用户组共享权限")}
                    onClick={() => setSection("groups")}
                  />
                  <QuickLink
                    title={t("创建受限 API Key")}
                    description={t("精确选择自动化所需资源和动作")}
                    onClick={() => setSection("keys")}
                  />
                  <div className="mt-1 rounded-lg bg-secondary/70 p-4">
                    <p className="text-xs text-muted-foreground">
                      {t("你的本地用户 ID")}
                    </p>
                    <div className="mt-2 flex items-center gap-2">
                      <code className="min-w-0 break-all text-xs">
                        {me.user.user_id}
                      </code>
                      <Button
                        size="icon"
                        className="ml-auto shrink-0"
                        variant="ghost"
                        aria-label={t("复制用户 ID")}
                        onClick={copyID}
                      >
                        {copied ? <Check size={14} /> : <Clipboard size={14} />}
                      </Button>
                    </div>
                    <p className="mt-1 text-[11px] text-muted-foreground">
                      {t("来源：")}
                      {me.user.source_id}
                    </p>
                  </div>
                </CardContent>
              </Card>
            </div>
          </>
        ) : section === "users" ? (
          <UsersPage notify={notify} refresh={refresh} reload={reload} />
        ) : !scope ? (
          <Empty
            title={t("请先选择一个组织")}
            description={t("加入组织并获得角色后，即可管理此组织。")}
          />
        ) : ["members", "groups", "roles"].includes(section) ? (
          <IAMPage
            key={scope + section}
            section={section as "members" | "groups" | "roles"}
            base={base}
            scope={scope}
            system={system}
            notify={notify}
            refresh={refresh}
            reload={reload}
          />
        ) : section === "keys" ? (
          <KeysPage
            key={scope}
            base={base}
            scope={scope}
            system={system}
            notify={notify}
            refresh={refresh}
            reload={reload}
          />
        ) : section === "workers" ? (
          <Suspense fallback={<Loading />}>
            <WorkersPage
              key={`${scope}:${workersNavigation}`}
              base={base}
              system={system}
              notify={notify}
            />
          </Suspense>
        ) : section === "resources" ? (
          <ResourcesPage
            key={scope}
            base={base}
            system={system}
            notify={notify}
            refresh={refresh}
            reload={reload}
          />
        ) : section === "check" ? (
          <CheckPage scope={scope} system={system} />
        ) : (
          <AuditPage key={base} base={base} refresh={refresh} />
        )}
      </main>
      <Dialog open={orgDialog} onOpenChange={setOrgDialog}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>{t("创建组织")}</DialogTitle>
            <DialogDescription>
              {t("组织是团队成员与资源权限的隔离边界。")}
            </DialogDescription>
          </DialogHeader>
          <form
            className="grid gap-5"
            onSubmit={async (e) => {
              e.preventDefault();
              await mutate(async () => {
                const organization = await api<Organization>(
                  "/api/v1/orgs",
                  "POST",
                  { name: orgName },
                );
                setScope(organization.id);
                setOrgDialog(false);
                setOrgName("");
              }, t("组织已创建。"));
            }}
          >
            <Field label={t("组织名称")}>
              <Input
                value={orgName}
                onChange={(e) => setOrgName(e.target.value)}
                maxLength={128}
                required
                placeholder={t("例如：Platform Engineering")}
              />
            </Field>
            <Button type="submit" disabled={saving}>
              {t("创建组织")}
            </Button>
          </form>
        </DialogContent>
      </Dialog>
    </div>
  );
}
