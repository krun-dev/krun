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
  Database,
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
import { AUTH_REQUIRED_EVENT } from "@/lib/auth-required";
import {
  canOpenSection,
  type OrganizationPermissions,
  type Section,
} from "@/lib/navigation";
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
const OAuthConsent = lazy(() =>
  import("@/components/oauth-consent").then((m) => ({
    default: m.OAuthConsent,
  })),
);
const Login = lazy(() =>
  import("@/components/login").then((module) => ({ default: module.Login })),
);
const Home = lazy(() =>
  import("@/components/home").then((module) => ({ default: module.Home })),
);
const OrganizationInvitationJoin = lazy(() =>
  import("@/components/organization-invitation-join").then((module) => ({
    default: module.OrganizationInvitationJoin,
  })),
);
const IAMPage = lazy(() =>
  import("@/components/management").then((module) => ({
    default: module.IAMPage,
  })),
);
const CheckPage = lazy(() =>
  import("@/components/management").then((module) => ({
    default: module.CheckPage,
  })),
);
const UsersPage = lazy(() =>
  import("@/components/management").then((module) => ({
    default: module.UsersPage,
  })),
);
const ResourcesPage = lazy(() =>
  import("@/components/management").then((module) => ({
    default: module.ResourcesPage,
  })),
);
const AuditPage = lazy(() =>
  import("@/components/audit").then((module) => ({
    default: module.AuditPage,
  })),
);
const WorkersPage = lazy(() =>
  import("@/components/workers").then((m) => ({ default: m.WorkersPage })),
);
const BucketsPage = lazy(() =>
  import("@/components/buckets").then((module) => ({
    default: module.BucketsPage,
  })),
);
const KeysPage = lazy(() =>
  import("@/components/keys").then((module) => ({ default: module.KeysPage })),
);

const navigation: {
  id: Section;
  name: string;
  icon: typeof Users;
  group: string;
}[] = [
  { id: "overview", name: "概览", icon: LayoutDashboard, group: "" },
  { id: "workers", name: "Workers", icon: Layers3, group: "计算服务" },
  { id: "buckets", name: "对象存储", icon: Database, group: "存储服务" },
  { id: "members", name: "组织成员", icon: Users, group: "组织管理" },
  { id: "groups", name: "用户组", icon: UsersRound, group: "组织管理" },
  { id: "roles", name: "角色授权", icon: ShieldCheck, group: "组织管理" },
  { id: "resources", name: "资源目录", icon: Building2, group: "组织管理" },
  { id: "check", name: "权限检查", icon: Search, group: "组织管理" },
  { id: "audit", name: "审计记录", icon: Activity, group: "组织管理" },
  { id: "keys", name: "API Keys", icon: KeyRound, group: "个人设置" },
];
const adminNavigation: { id: Section; name: string }[] = [
  { id: "orgs", name: "组织目录" },
  { id: "users", name: "系统用户" },
  { id: "audit", name: "全平台审计" },
  { id: "keys", name: "全平台 API 密钥" },
];

function repositoryCallbackNotice(): string {
  const params = new URLSearchParams(window.location.search);
  if (params.get("repository_status") === "connected") {
    return t("仓库账号连接成功。");
  }
  if (params.get("repository_status") === "error") {
    return t("仓库账号连接失败（{{code}}）。", {
      code: params.get("error") || "UNKNOWN",
    });
  }
  return "";
}

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
    [orgSelfService, setOrgSelfService] = useState(false),
    [loading, setLoading] = useState(true),
    [fatal, setFatal] = useState("");
  const [orgs, setOrgs] = useState<Organization[]>([]),
    [scope, setScope] = useState(
      new URLSearchParams(window.location.search).get("workspace") || "",
    ),
    [section, setSection] = useState<Section>(() => {
      const page = new URLSearchParams(window.location.search).get("page");
      const known: Section[] = [
        "overview",
        "orgs",
        "members",
        "groups",
        "roles",
        "keys",
        "check",
        "audit",
        "users",
        "resources",
        "workers",
        "buckets",
      ];
      return known.includes(page as Section) ? (page as Section) : "overview";
    }),
    [notice, setNotice] = useState(repositoryCallbackNotice),
    [refresh, setRefresh] = useState(0),
    [menu, setMenu] = useState(false);
  const [workersNavigation, setWorkersNavigation] = useState(0);
  const [orgsLoaded, setOrgsLoaded] = useState(false);
  const [organizationsError, setOrganizationsError] = useState("");
  const [systemView, setSystemView] = useState(
    () => new URLSearchParams(window.location.search).get("view") === "system",
  );
  const system = systemView && Boolean(me?.system_admin);
  const [permissions, setPermissions] =
    useState<OrganizationPermissions | null>(null);
  const [permissionsError, setPermissionsError] = useState("");
  const [orgCatalog, setOrgCatalog] = useState<Organization[]>([]);
  const [orgDialog, setOrgDialog] = useState(false),
    [orgName, setOrgName] = useState(""),
    [saving, setSaving] = useState(false),
    [copied, setCopied] = useState(false);
  const oauthLogin =
    new URLSearchParams(window.location.search).has("oauth") ||
    new URLSearchParams(window.location.search).has("oauth_device");
  const [joining] = useState(
    () =>
      new URLSearchParams(window.location.search).get("page") === "join" ||
      new URLSearchParams(window.location.hash.slice(1)).has("invite"),
  );
  const notify = useCallback((message: string) => setNotice(message), []);
  const reload = useCallback(() => setRefresh((x) => x + 1), []);
  useEffect(() => {
    const requireLogin = () => {
      setMe(null);
      setOrgs([]);
      setOrgCatalog([]);
      setOrgsLoaded(false);
      setOrganizationsError("");
      setPermissions(null);
      setSystemView(false);
      setMenu(false);
      setOrgDialog(false);
      setSaving(false);
      setNotice("");
    };
    window.addEventListener(AUTH_REQUIRED_EVENT, requireLogin);
    return () => window.removeEventListener(AUTH_REQUIRED_EVENT, requireLogin);
  }, []);
  useEffect(() => {
    if (!me || oauthLogin) return;
    const url = new URL(location.href);
    if (!url.searchParams.has("repository_status")) return;
    url.searchParams.delete("repository_status");
    url.searchParams.delete("repository_provider");
    url.searchParams.delete("error");
    url.searchParams.delete("request_id");
    history.replaceState(null, "", url);
  }, [me]);
  // Keep the URL in step with the visible section. Navigating used to leave the
  // previous page's parameters behind, so refreshing or sharing a link reopened
  // the wrong page.
  useEffect(() => {
    if (oauthLogin || joining || !me) {
      return;
    }
    const url = new URL(location.href);
    if (system) {
      url.searchParams.set("view", "system");
    } else {
      url.searchParams.delete("view");
    }
    if (section === "overview") url.searchParams.delete("page");
    else url.searchParams.set("page", section);
    if (section !== "workers") {
      url.searchParams.delete("worker");
      url.searchParams.delete("environment");
      url.searchParams.delete("worker_tab");
    }
    if (section !== "buckets") {
      url.searchParams.delete("bucket");
      url.searchParams.delete("prefix");
    }
    if (scope) url.searchParams.set("workspace", scope);
    else url.searchParams.delete("workspace");
    if (url.toString() !== location.href) history.replaceState(null, "", url);
  }, [section, scope, system, me, oauthLogin, joining]);
  useEffect(() => {
    let active = true;
    Promise.all([
      api<{
        providers: Provider[];
        development: boolean;
        organization_self_service?: boolean;
      }>("/api/auth/providers"),
      api<Me>("/api/auth/me").catch((e) => {
        if (e instanceof APIError && e.status === 401) return null;
        throw e;
      }),
    ])
      .then(([p, u]) => {
        if (active) {
          setProviders(p.providers);
          setDevelopment(p.development);
          setOrgSelfService(Boolean(p.organization_self_service));
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
    if (!me || oauthLogin) return;
    let active = true;
    setOrganizationsError("");
    Promise.all([
      api<List<Organization>>("/api/v1/orgs"),
      api<Me>("/api/auth/me"),
    ])
      .then(([data, currentMe]) => {
        if (active) {
          setMe(currentMe);
          setOrgs(data.items);
          setOrgCatalog(data.items);
          setOrgsLoaded(true);
          setScope((current) =>
            data.items.some((o) => o.id === current)
              ? current
              : (data.items[0]?.id ?? ""),
          );
        }
      })
      .catch((e) => {
        if (active) setOrganizationsError(errorMessage(e));
      });
    return () => {
      active = false;
    };
  }, [me?.user.user_id, refresh, oauthLogin]);
  useEffect(() => {
    const refreshPermissions = () => reload();
    window.addEventListener("focus", refreshPermissions);
    return () => window.removeEventListener("focus", refreshPermissions);
  }, [reload]);
  const org = orgs.find((o) => o.id === scope);
  useEffect(() => {
    setPermissionsError("");
    if (!me || !org || system || oauthLogin) {
      setPermissions(null);
      return;
    }
    // Revalidate the same organization without unmounting its page and losing
    // unsaved forms. A different organization remains closed until its reply.
    setPermissions((current) => current?.organization_id === org.id ? current : null);
    const controller = new AbortController();
    api<OrganizationPermissions>(
      `/api/v1/orgs/${encodeURIComponent(org.id)}/permissions`,
      "GET",
      undefined,
      controller.signal,
    )
      .then((data) => {
        if (!controller.signal.aborted) {
          setPermissions(data);
        }
      })
      .catch((error) => {
        if (!controller.signal.aborted) {
          setPermissions(null);
          setPermissionsError(errorMessage(error));
        }
      });
    return () => controller.abort();
  }, [me?.user.user_id, me?.revision, org?.id, system, refresh, oauthLogin]);
  const canOpen = (page: Section) =>
    canOpenSection(
      page,
      system,
      Boolean(me?.system_admin),
      org?.id ?? "",
      organizationsError || permissionsError ? null : permissions,
    );
  const permissionsReady =
    orgsLoaded && (system || !org || permissions?.organization_id === org.id);
  useEffect(() => {
    if (!joining && me && permissionsReady && !canOpen(section)) {
      setSection(system ? "orgs" : "overview");
    }
  }, [
    joining,
    me,
    permissionsReady,
    permissions,
    organizationsError,
    permissionsError,
    section,
    system,
    org?.id,
  ]);
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
  // Creation is allowed for system admins always, and for everyone else only
  // when the server enables self-service organizations.
  const canCreateOrg = Boolean(me?.system_admin) || orgSelfService;
  const scopeName = system ? t("平台管理") : (org?.name ?? t("选择组织"));
  function sectionHref(page: Section) {
    const params = new URLSearchParams();
    if (scope) {
      params.set("workspace", scope);
    }
    if (system) {
      params.set("view", "system");
    }
    if (page !== "overview") {
      params.set("page", page);
    }
    const query = params.toString();
    return query ? `/?${query}` : "/";
  }
  const homeHref = sectionHref(system ? "orgs" : "overview");
  async function copyID() {
    try {
      await navigator.clipboard.writeText(me!.user.user_id);
      setCopied(true);
      setTimeout(() => setCopied(false), 2000);
    } catch {
      notify(t("复制失败，请手动选中用户 ID。"));
    }
  }
  if (!me && !joining && !oauthLogin && window.location.search === "") {
    return (
      <Suspense fallback={<Loading />}>
        <Home />
      </Suspense>
    );
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
  if (joining)
    return (
      <Suspense fallback={<Loading />}>
        <OrganizationInvitationJoin me={me} providers={providers} development={development} />
      </Suspense>
    );
  if (oauthLogin)
    return (
      <Suspense fallback={<Loading />}>
        <OAuthConsent
          me={me}
          providers={providers}
          development={development}
          onLogout={() => setMe(null)}
        />
      </Suspense>
    );
  if (!me)
    return (
      <Suspense fallback={<Loading />}>
        <Login
          providers={providers}
          development={development}
          error={
            new URLSearchParams(window.location.search).get("error") ??
            undefined
          }
        />
      </Suspense>
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
                if (system) {
                  setSystemView(false);
                  setSection("overview");
                }
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
            </SelectField>
          </div>
        </div>
        {system ? (
          <>
            <div className="mt-7 px-3 text-[10px] font-semibold uppercase tracking-widest text-primary/80">
              {t("平台管理")}
            </div>
            <nav className="mt-3 grid gap-1">
              {adminNavigation.map((n) => (
                <button
                  key={n.id}
                  onClick={() => {
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
                  <ShieldCheck size={17} />
                  {t(n.name)}
                  {section === n.id && (
                    <span className="ml-auto size-1.5 rounded-full bg-primary" />
                  )}
                </button>
              ))}
            </nav>
          </>
        ) : (
          <>
            <div className="mt-7 px-3 text-[10px] font-semibold uppercase tracking-widest text-muted-foreground">
              {t("平台控制台")}
            </div>
            <nav className="mt-3 grid gap-1">
              {[
                ...navigation.filter((n) => n.group === ""),
                ...navigation.filter((n) => n.group === "计算服务"),
                ...navigation.filter((n) => n.group === "存储服务"),
                ...navigation.filter((n) => n.group === "组织管理"),
                ...navigation.filter((n) => n.group === "个人设置"),
              ]
                .filter((n) => canOpen(n.id))
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
          </>
        )}
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
            <a href="/docs/" className="hover:text-foreground">{t("使用文档")}</a>{" "}
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
          <nav aria-label={t("面包屑")} className="min-w-0">
            <ol className="flex min-w-0 items-center gap-3">
              <li className="hidden shrink-0 text-sm text-muted-foreground sm:block">
                <a href={homeHref} className="hover:text-foreground hover:underline">
                  {system ? t("平台管理") : t("组织")}
                </a>
              </li>
              <li
                aria-hidden="true"
                className="hidden text-muted-foreground/40 sm:block"
              >
                /
              </li>
              <li className="min-w-0 text-sm font-medium">
                <a href={homeHref} className="block truncate hover:underline">
                  {scopeName}
                </a>
              </li>
              <li
                aria-hidden="true"
                className="hidden text-muted-foreground/40 xl:block"
              >
                /
              </li>
              <li className="hidden shrink-0 text-xs text-muted-foreground xl:block">
                <a
                  href={sectionHref(section)}
                  aria-current="page"
                  className="hover:text-foreground hover:underline"
                >
                  {t(
                    (system ? adminNavigation : navigation).find(
                      (n) => n.id === section,
                    )?.name ?? "概览",
                  )}
                </a>
              </li>
            </ol>
          </nav>
        </div>
        <div className="ml-3 flex shrink-0 items-center gap-3">
          {me.system_admin ? (
            <Button
              variant={system ? "default" : "ghost"}
              className="gap-1.5 px-2.5 text-[12px]"
              aria-label={system ? t("返回组织") : t("平台管理")}
              onClick={() => {
                setSystemView((v) => !v);
                setSection(system ? "overview" : "orgs");
              }}
            >
              <ShieldCheck size={15} />
              <span className="hidden sm:inline">
                {system ? t("返回组织") : t("平台管理")}
              </span>
            </Button>
          ) : null}
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
        <Suspense fallback={<Loading />}>
          {organizationsError || (permissionsError && !system) ? (
            <div role="alert" className="grid justify-items-start gap-4">
              <p>{organizationsError || permissionsError}</p>
              <Button onClick={reload}>{t("重试")}</Button>
            </div>
          ) : !permissionsReady || !canOpen(section) ? (
            <Loading />
          ) : orgs.length === 0 && !system ? (
            <NoOrganization
              canCreate={canCreateOrg}
              onCreate={() => setOrgDialog(true)}
              userId={me.user.user_id}
            />
          ) : section === "overview" ? (
            <>
              <PageHeader
                eyebrow={t("Workspace overview")}
                title={t("组织概览")}
                description={t("从这里管理团队身份、访问权限与自动化凭据。")}
                action={
                  canCreateOrg ? (
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
                            setSection("overview");
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
                    {canOpen("groups") && (
                      <QuickLink
                        title={t("设置用户组与角色")}
                        description={t("让团队通过用户组共享权限")}
                        onClick={() => setSection("groups")}
                      />
                    )}
                    {canOpen("keys") && (
                      <QuickLink
                        title={t("创建受限 API Key")}
                        description={t("精确选择自动化所需资源和动作")}
                        onClick={() => setSection("keys")}
                      />
                    )}
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
                          {copied ? (
                            <Check size={14} />
                          ) : (
                            <Clipboard size={14} />
                          )}
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
          ) : section === "orgs" ? (
            <OrgCatalogPage
              orgs={orgCatalog}
              onSelect={(id) => {
                setScope(id);
                setSystemView(false);
                setSection("overview");
              }}
            />
          ) : section === "users" ? (
            <UsersPage notify={notify} refresh={refresh} reload={reload} />
          ) : !scope && !system ? (
            <NoOrganization
              canCreate={canCreateOrg}
              onCreate={() => setOrgDialog(true)}
              userId={me.user.user_id}
            />
          ) : ["members", "groups", "roles"].includes(section) ? (
            <IAMPage
              key={scope + section}
              section={section as "members" | "groups" | "roles"}
              base={base}
              scope={scope}
              system={system}
              canInvite={
                !system &&
                permissions?.actions["member.manage"] === true &&
                permissions?.actions["iam.grant"] === true
              }
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
            me.workers_configured === false ? (
              <Empty
                title={t("Workers 未配置")}
                description={t(
                  "请在控制面配置 workers: 段并重启服务，然后刷新此页面。",
                )}
              />
            ) : (
              <WorkersPage
                key={`${scope}:${workersNavigation}`}
                base={base}
                system={system}
                canCreate={permissions?.actions["worker.create"] === true}
                notify={notify}
              />
            )
          ) : section === "buckets" ? (
            <BucketsPage
              key={scope}
              base={base}
              system={system}
              canCreate={permissions?.actions["storage.bucket.create"] === true}
              canUpdate={permissions?.actions["storage.bucket.update"] === true}
              canWrite={permissions?.actions["storage.object.write"] === true}
              canDelete={permissions?.actions["storage.object.delete"] === true}
              notify={notify}
            />
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
        </Suspense>
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
                  {
                    name: orgName,
                  },
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

// Shown when the signed-in user has no organization yet. Every section is empty
// in that state, so this explains it and offers the one useful action.
function NoOrganization({
  canCreate,
  onCreate,
  userId,
}: {
  canCreate: boolean;
  onCreate: () => void;
  userId: string;
}) {
  const { t } = useTranslation();
  return (
    <section className="mx-auto grid max-w-2xl gap-5 py-10">
      <div className="rounded-lg border border-dashed p-10 text-center">
        <div className="mx-auto w-fit rounded-lg bg-secondary p-3 text-muted-foreground">
          <Building2 size={22} />
        </div>
        <h2 className="mt-4 text-lg font-semibold">
          {t("你还没有加入任何组织")}
        </h2>
        <p className="mx-auto mt-2 max-w-md text-sm text-muted-foreground">
          {canCreate
            ? t("组织是团队协作与权限隔离的边界。创建一个组织开始使用。")
            : t(
                "组织是团队协作与权限隔离的边界。请联系管理员邀请你加入现有组织。",
              )}
        </p>
        {canCreate ? (
          <Button className="mt-5" onClick={onCreate}>
            <Plus size={16} />
            {t("创建组织")}
          </Button>
        ) : (
          <div className="mx-auto mt-5 max-w-sm rounded-md border bg-card p-4 text-left">
            <p className="text-xs font-medium text-muted-foreground">
              {t("把你的用户 ID 提供给管理员")}
            </p>
            <code className="mt-1.5 block break-all text-xs">{userId}</code>
          </div>
        )}
      </div>
      {canCreate ? (
        <p className="text-center text-xs text-muted-foreground">
          {t("创建后你会自动成为该组织的管理员。")}
        </p>
      ) : null}
    </section>
  );
}

function OrgCatalogPage({
  orgs,
  onSelect,
}: {
  orgs: Organization[];
  onSelect: (id: string) => void;
}) {
  const { t } = useTranslation();
  return (
    <section className="grid gap-4">
      <div className="flex flex-wrap items-end justify-between gap-3">
        <div>
          <p className="text-[11px] font-medium uppercase tracking-widest text-muted-foreground">
            {t("平台管理")}
          </p>
          <h1 className="mt-1 text-xl font-semibold">{t("组织目录")}</h1>
          <p className="mt-1 text-sm text-muted-foreground">
            {t("平台上的全部组织。点按进入该组织的控制台。")}
          </p>
        </div>
      </div>
      {orgs.length === 0 ? (
        <Empty title={t("暂无组织")} description={t("还没有任何组织。")} />
      ) : (
        <div className="grid gap-3 md:grid-cols-2 xl:grid-cols-3">
          {orgs.map((o) => (
            <button
              key={o.id}
              onClick={() => onSelect(o.id)}
              className="rounded-lg border bg-card p-4 text-left hover:border-primary/40"
            >
              <div className="flex items-center justify-between gap-2">
                <strong className="truncate">{o.name}</strong>
                <Building2
                  size={16}
                  className="shrink-0 text-muted-foreground"
                />
              </div>
              <code className="mt-2 block break-all text-[11px] text-muted-foreground">
                {o.id}
              </code>
            </button>
          ))}
        </div>
      )}
    </section>
  );
}
