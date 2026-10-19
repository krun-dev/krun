import {
  errorMessage,
  t,
  environmentLabel,
  roleLabel,
  resourceLabel,
} from "@/lib/i18n";
import { useTranslation } from "react-i18next";
import { useEffect, useState, type ReactNode } from "react";
import {
  ArrowRight,
  LoaderCircle,
  Plus,
  ShieldAlert,
  ShieldCheck,
  Trash2,
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
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
import { Tabs, TabsList, TabsTrigger } from "@/components/ui/tabs";
import {
  api,
  type Assignment,
  type Decision,
  type Group,
  type List,
  type Resource,
  type User,
} from "@/lib/api";
import {
  Empty,
  Field,
  Loading,
  PageHeader,
  SelectField,
  StatusBadge,
} from "./common";

export type Props = {
  base: string;
  scope: string;
  system: boolean;
  notify: (message: string) => void;
  refresh: number;
  reload: () => void;
};
export function useData<T>(path: string, refresh: number) {
  const [data, setData] = useState<T | null>(null),
    [error, setError] = useState(""),
    [loading, setLoading] = useState(true);
  useEffect(() => {
    let active = true;
    setLoading(true);
    setError("");
    api<T>(path)
      .then((d) => {
        if (active) setData(d);
      })
      .catch((e) => {
        if (active) setError(errorMessage(e));
      })
      .finally(() => {
        if (active) setLoading(false);
      });
    return () => {
      active = false;
    };
  }, [path, refresh]);
  return { data, error, loading };
}
export function ErrorState({ error }: { error: string }) {
  useTranslation();
  return (
    <div
      role="alert"
      className="rounded-lg border border-destructive/25 bg-destructive/10 p-4 text-sm text-destructive"
    >
      {t(error)}
    </div>
  );
}
export function DataTable<T>({
  rows,
  columns,
  getKey,
  className = "",
}: {
  rows: T[];
  columns: { title: string; render: (row: T) => ReactNode }[];
  getKey: (row: T) => string;
  className?: string;
}) {
  useTranslation();
  return (
    <div className={`overflow-hidden rounded-lg border bg-card ${className}`}>
      <Table>
        <TableHeader>
          <TableRow>
            {columns.map((c) => (
              <TableHead key={c.title} className="h-11 bg-secondary/30 text-xs">
                {c.title}
              </TableHead>
            ))}
          </TableRow>
        </TableHeader>
        <TableBody>
          {rows.map((row) => (
            <TableRow key={getKey(row)}>
              {columns.map((c) => (
                <TableCell key={c.title} className="py-4">
                  {c.render(row)}
                </TableCell>
              ))}
            </TableRow>
          ))}
        </TableBody>
      </Table>
    </div>
  );
}
export function ID({ value }: { value: string }) {
  useTranslation();
  return (
    <code className="break-all text-[11px] text-muted-foreground">{value}</code>
  );
}
export function Confirm({
  label,
  description,
  onConfirm,
  disabled,
}: {
  label: string;
  description: string;
  onConfirm: () => Promise<void>;
  disabled?: boolean;
}) {
  useTranslation();
  const [open, setOpen] = useState(false),
    [busy, setBusy] = useState(false);
  return (
    <>
      <Button
        variant="ghost"
        size="sm"
        disabled={disabled}
        onClick={() => setOpen(true)}
        className="text-muted-foreground"
      >
        <Trash2 size={14} />
        {label}
      </Button>
      <Dialog open={open} onOpenChange={setOpen}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>{label}</DialogTitle>
            <DialogDescription>{description}</DialogDescription>
          </DialogHeader>
          <div className="mt-3 flex justify-end gap-2">
            <Button variant="outline" onClick={() => setOpen(false)}>
              {t("取消")}
            </Button>
            <Button
              variant="destructive"
              disabled={busy}
              onClick={async () => {
                setBusy(true);
                try {
                  await onConfirm();
                  setOpen(false);
                } finally {
                  setBusy(false);
                }
              }}
            >
              {t("确认")}
              {label}
            </Button>
          </div>
        </DialogContent>
      </Dialog>
    </>
  );
}

export function IAMPage({
  section,
  ...p
}: Props & { section: "members" | "groups" | "roles" }) {
  useTranslation();
  const { data, error, loading } = useData<List<User | Group | Assignment>>(
    p.base + "/" + (section === "roles" ? "role-assignments" : section),
    p.refresh,
  );
  const [open, setOpen] = useState(false),
    [busy, setBusy] = useState(false),
    [name, setName] = useState(""),
    [user, setUser] = useState(""),
    [group, setGroup] = useState(""),
    [subjectKind, setSubjectKind] = useState("user"),
    [role, setRole] = useState(p.system ? "system_admin" : "viewer"),
    [scopeKind, setScopeKind] = useState(p.system ? "system" : "organization"),
    [scopeID, setScopeID] = useState(""),
    [envs, setEnvs] = useState("");
  const labels = {
    members: {
      title: t("组织成员"),
      description: t("成员资格与角色分别管理，加入组织后按需授予权限。"),
      action: t("添加成员"),
    },
    groups: {
      title: t("用户组"),
      description: t("把共同协作的成员放进用户组，通过角色统一授予权限。"),
      action: t("创建用户组"),
    },
    roles: {
      title: t("角色授权"),
      description: t(
        "为用户或用户组分配固定角色，并明确组织、资源与环境范围。",
      ),
      action: t("授予角色"),
    },
  };
  const l = labels[section];
  async function run(fn: () => Promise<unknown>, message: string) {
    setBusy(true);
    try {
      await fn();
      p.notify(message);
      p.reload();
    } catch (e) {
      p.notify(errorMessage(e));
    } finally {
      setBusy(false);
    }
  }
  async function submit() {
    await run(async () => {
      if (section === "members")
        await api(p.base + "/members", "POST", { user_id: user });
      if (section === "groups") await api(p.base + "/groups", "POST", { name });
      if (section === "roles")
        await api(p.base + "/role-assignments", "POST", {
          subject_kind: subjectKind,
          subject_id: user,
          role_id: role,
          scope_kind: scopeKind,
          scope_id:
            scopeKind === "organization" || scopeKind === "system"
              ? p.scope
              : scopeID,
          conditions: {
            environments: envs ? envs.split(",").map((s) => s.trim()) : [],
          },
        });
      setOpen(false);
      setName("");
      setUser("");
    }, t("已保存。"));
  }
  if (p.system && section === "members")
    return (
      <Empty
        title={t("系统空间没有组织成员")}
        description={t(
          "在系统用户中管理账号，在系统用户组和角色授权中管理系统管理员。",
        )}
      />
    );
  return (
    <>
      <PageHeader
        eyebrow={t("Identity & access")}
        title={l.title}
        description={l.description}
        action={
          <Button onClick={() => setOpen(true)}>
            <Plus size={16} />
            {l.action}
          </Button>
        }
      />
      {loading ? (
        <Loading />
      ) : error ? (
        <ErrorState error={t(error)} />
      ) : !data?.items.length ? (
        <Empty
          title={t("暂无{{v0}}", { v0: l.title })}
          action={
            <Button variant="outline" onClick={() => setOpen(true)}>
              {l.action}
            </Button>
          }
        />
      ) : section === "members" ? (
        <DataTable
          rows={data.items as User[]}
          getKey={(u) => u.user_id}
          columns={[
            {
              title: t("成员"),
              render: (u) => (
                <div>
                  <p className="font-medium">{u.name || u.user_id}</p>
                  <p className="mt-1 text-xs text-muted-foreground">
                    {u.email || t("未提供邮箱")}
                  </p>
                </div>
              ),
            },
            {
              title: t("本地用户 ID"),
              render: (u) => <ID value={u.user_id} />,
            },
            {
              title: t("身份来源"),
              render: (u) => <Badge variant="outline">{u.source_id}</Badge>,
            },
            {
              title: t("状态"),
              render: (u) => <StatusBadge active={u.status === "active"} />,
            },
            {
              title: t("操作"),
              render: (u) => (
                <Confirm
                  label={t("移除")}
                  description={t(
                    "移除成员会同时清理本组织用户组成员关系和直接角色授权。已有 Key 将失去对应权限。",
                  )}
                  onConfirm={() =>
                    run(
                      () => api(p.base + "/members/" + u.user_id, "DELETE"),
                      t("成员已移除。"),
                    )
                  }
                />
              ),
            },
          ]}
        />
      ) : section === "groups" ? (
        <div className="grid gap-5 md:grid-cols-2">
          {(data.items as Group[]).map((g) => (
            <Card key={g.id} className="gap-0 shadow-none">
              <CardHeader className="pb-4">
                <div className="flex items-center justify-between">
                  <div className="flex items-center gap-3">
                    <div className="rounded-lg bg-primary/5 p-2.5 text-primary">
                      <UsersRound size={20} />
                    </div>
                    <div>
                      <CardTitle className="text-base">{g.name}</CardTitle>
                      <CardDescription className="mt-1">
                        {t("{{count}} 位成员 · 修订 {{revision}}", {
                          count: g.members.length,
                          revision: g.revision,
                        })}
                      </CardDescription>
                    </div>
                  </div>
                  <Confirm
                    label={t("删除")}
                    description={t("此组关联的角色授权也将删除。")}
                    onConfirm={() =>
                      run(
                        () => api(p.base + "/groups/" + g.id, "DELETE"),
                        t("用户组已删除。"),
                      )
                    }
                  />
                </div>
                <ID value={g.id} />
              </CardHeader>
              <CardContent className="grid gap-3 border-t pt-4">
                {g.members.length ? (
                  g.members.map((u) => (
                    <div
                      key={u}
                      className="flex items-center justify-between gap-2 rounded-md bg-secondary/60 px-3 py-1"
                    >
                      <ID value={u} />
                      <Confirm
                        label={t("移出")}
                        description={t(
                          "用户将失去通过此组取得的权限，其他直接角色授权保留。",
                        )}
                        onConfirm={() =>
                          run(
                            () =>
                              api(
                                `${p.base}/groups/${g.id}/members/${u}`,
                                "DELETE",
                              ),
                            t("成员已移出。"),
                          )
                        }
                      />
                    </div>
                  ))
                ) : (
                  <p className="py-3 text-xs text-muted-foreground">
                    {t("还没有成员。添加成员后，再为此组分配角色。")}
                  </p>
                )}
                <div className="flex gap-2">
                  <Input
                    aria-label={t("加入 {{v0}} 的用户 ID", { v0: g.name })}
                    placeholder={t("本地用户 ID")}
                    value={group === g.id ? user : ""}
                    onChange={(e) => {
                      setGroup(g.id);
                      setUser(e.target.value);
                    }}
                  />
                  <Button
                    variant="outline"
                    disabled={busy || group !== g.id || !user}
                    onClick={() =>
                      run(async () => {
                        await api(
                          `${p.base}/groups/${g.id}/members/${user}`,
                          "PUT",
                          {},
                        );
                        setUser("");
                      }, t("成员已加入用户组。"))
                    }
                  >
                    {t("添加")}
                  </Button>
                </div>
              </CardContent>
            </Card>
          ))}
        </div>
      ) : (
        <DataTable
          rows={data.items as Assignment[]}
          getKey={(a) => a.assignment_id}
          columns={[
            {
              title: t("主体"),
              render: (a) => (
                <div>
                  <Badge variant="outline" className="mb-1">
                    {a.subject_kind === "group" ? t("用户组") : t("用户")}
                  </Badge>
                  <div>
                    <ID value={a.subject_id} />
                  </div>
                </div>
              ),
            },
            {
              title: t("角色"),
              render: (a) => (
                <Badge
                  className="bg-primary/5 text-primary"
                  variant="secondary"
                >
                  {roleLabel(a.role_id)}
                </Badge>
              ),
            },
            {
              title: t("范围"),
              render: (a) => (
                <div>
                  <p className="text-xs">{resourceLabel(a.scope_kind)}</p>
                  <ID value={a.scope_id} />
                </div>
              ),
            },
            {
              title: t("环境"),
              render: (a) => (
                <span className="text-xs">
                  {a.conditions.environments
                    ?.map(environmentLabel)
                    .join(", ") || t("全部环境")}
                </span>
              ),
            },
            {
              title: t("操作"),
              render: (a) => (
                <Confirm
                  label={t("撤销")}
                  description={t("撤销后，下一次请求立即使用新的授权状态。")}
                  onConfirm={() =>
                    run(
                      () =>
                        api(
                          p.base + "/role-assignments/" + a.assignment_id,
                          "DELETE",
                        ),
                      t("角色授权已撤销。"),
                    )
                  }
                />
              ),
            },
          ]}
        />
      )}
      <Dialog open={open} onOpenChange={setOpen}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>{l.action}</DialogTitle>
            <DialogDescription>{l.description}</DialogDescription>
          </DialogHeader>
          <form
            className="grid gap-5"
            onSubmit={(e) => {
              e.preventDefault();
              void submit();
            }}
          >
            {section === "groups" ? (
              <Field label={t("用户组名称")}>
                <Input
                  required
                  value={name}
                  onChange={(e) => setName(e.target.value)}
                  placeholder={t("例如：Platform Developers")}
                  maxLength={128}
                />
              </Field>
            ) : (
              <>
                {section === "roles" && (
                  <Field label={t("授权主体")}>
                    <SelectField value={subjectKind} onChange={setSubjectKind}>
                      <option value="user">{t("用户")}</option>
                      <option value="group">{t("用户组")}</option>
                    </SelectField>
                  </Field>
                )}
                <Field
                  label={
                    section === "roles" && subjectKind === "group"
                      ? t("用户组 ID")
                      : t("本地用户 ID")
                  }
                  hint={t(
                    "使用平台生成的准确 ID，邮箱和显示名不作为身份凭据。",
                  )}
                >
                  <Input
                    required
                    value={user}
                    onChange={(e) => setUser(e.target.value)}
                    placeholder={subjectKind === "group" ? "grp_…" : "usr_…"}
                  />
                </Field>
                {section === "roles" && (
                  <>
                    <Field label={t("角色")}>
                      <SelectField value={role} onChange={setRole}>
                        {(p.system
                          ? ["system_admin"]
                          : ["org_admin", "developer", "publisher", "viewer"]
                        ).map((r) => (
                          <option key={r} value={r}>
                            {roleLabel(r)} ({r})
                          </option>
                        ))}
                      </SelectField>
                    </Field>
                    {!p.system && (
                      <>
                        <Field label={t("资源范围")}>
                          <SelectField
                            value={scopeKind}
                            onChange={setScopeKind}
                          >
                            <option value="organization">
                              {t("整个组织")}
                            </option>
                            <option value="service">{t("指定服务")}</option>
                            <option value="model">{t("指定模型")}</option>
                          </SelectField>
                        </Field>
                        {scopeKind !== "organization" && (
                          <Field label={t("资源 ID")}>
                            <Input
                              required
                              value={scopeID}
                              onChange={(e) => setScopeID(e.target.value)}
                            />
                          </Field>
                        )}
                        <Field
                          label={t("环境条件（可选）")}
                          hint={t(
                            "逗号分隔：global, development, test, staging, production。留空表示全部环境。",
                          )}
                        >
                          <Input
                            value={envs}
                            onChange={(e) => setEnvs(e.target.value)}
                            placeholder={t("例如：staging,production")}
                          />
                        </Field>
                      </>
                    )}
                  </>
                )}
              </>
            )}
            <Button type="submit" disabled={busy}>
              {busy && <LoaderCircle size={16} className="animate-spin" />}
              {t("保存")}
            </Button>
          </form>
        </DialogContent>
      </Dialog>
    </>
  );
}

export function CheckPage({
  scope,
  system,
}: {
  scope: string;
  system: boolean;
}) {
  useTranslation();
  const [kind, setKind] = useState(system ? "system" : "organization"),
    [id, setID] = useState(scope),
    [action, setAction] = useState(
      system ? "system.role.read" : "organization.read",
    ),
    [environment, setEnvironment] = useState("global"),
    [decision, setDecision] = useState<Decision | null>(null),
    [error, setError] = useState(""),
    [busy, setBusy] = useState(false);
  useEffect(() => {
    setKind(system ? "system" : "organization");
    setID(scope);
    setAction(system ? "system.role.read" : "organization.read");
    setDecision(null);
  }, [scope, system]);
  return (
    <>
      <PageHeader
        eyebrow={t("Permission explorer")}
        title={t("权限检查")}
        description={t(
          "检查当前登录身份对指定资源与环境的访问能力。实际操作仍会重新授权。",
        )}
      />
      <div className="grid gap-6 lg:grid-cols-[1fr_1fr]">
        <Card className="shadow-none">
          <CardHeader>
            <CardTitle className="text-base">{t("构造检查请求")}</CardTitle>
            <CardDescription>
              {t("资源的组织归属由服务端解析。")}
            </CardDescription>
          </CardHeader>
          <CardContent>
            <form
              className="grid gap-5"
              onSubmit={async (e) => {
                e.preventDefault();
                setBusy(true);
                setError("");
                setDecision(null);
                try {
                  setDecision(
                    await api<Decision>("/api/v1/authz/check", "POST", {
                      resource_kind: kind,
                      resource_id: id,
                      action,
                      environment,
                    }),
                  );
                } catch (e) {
                  setError(errorMessage(e));
                } finally {
                  setBusy(false);
                }
              }}
            >
              <Field label={t("资源类型")}>
                <SelectField value={kind} onChange={setKind}>
                  {[
                    "system",
                    "organization",
                    "service",
                    "model",
                    "api_key",
                  ].map((k) => (
                    <option key={k} value={k}>
                      {resourceLabel(k)} ({k})
                    </option>
                  ))}
                </SelectField>
              </Field>
              <Field label={t("资源 ID")}>
                <Input
                  required
                  value={id}
                  onChange={(e) => setID(e.target.value)}
                />
              </Field>
              <Field label={t("动作")}>
                <Input
                  required
                  value={action}
                  onChange={(e) => setAction(e.target.value)}
                  placeholder="service.read"
                />
              </Field>
              <Field label={t("目标环境")}>
                <SelectField value={environment} onChange={setEnvironment}>
                  {["global", "development", "test", "staging", "production"].map(
                    (env) => (
                      <option key={env} value={env}>
                        {environmentLabel(env)}
                      </option>
                    ),
                  )}
                </SelectField>
              </Field>
              <Button disabled={busy} type="submit">
                {t("检查权限")}
                <ArrowRight size={16} />
              </Button>
            </form>
          </CardContent>
        </Card>
        <Card className="shadow-none">
          <CardHeader>
            <CardTitle className="text-base">{t("决策结果")}</CardTitle>
            <CardDescription>
              {t("默认拒绝 · 用户与凭据分别求值")}
            </CardDescription>
          </CardHeader>
          <CardContent>
            {error ? (
              <ErrorState error={t(error)} />
            ) : decision ? (
              <div>
                <div
                  className={`mb-6 rounded-lg border p-6 ${decision.allowed ? "border-emerald-200 bg-emerald-50" : "border-red-200 bg-red-50"}`}
                >
                  {decision.allowed ? (
                    <ShieldCheck className="mb-3 text-emerald-600" size={28} />
                  ) : (
                    <ShieldAlert className="mb-3 text-red-600" size={28} />
                  )}
                  <h3 className="text-xl font-semibold">
                    {decision.allowed ? t("允许访问") : t("拒绝访问")}
                  </h3>
                  <p className="mt-2 font-mono text-xs text-muted-foreground">
                    {t(
                      decision.reason === "ALLOWED"
                        ? "权限已允许"
                        : "没有访问权限",
                    )}{" "}
                    ({decision.reason})
                  </p>
                </div>
                <div className="grid gap-4 text-sm">
                  <div className="flex justify-between">
                    <span className="text-muted-foreground">
                      {t("用户角色检查")}
                    </span>
                    <span>{decision.user_allowed ? t("允许") : t("拒绝")}</span>
                  </div>
                  <div className="flex justify-between">
                    <span className="text-muted-foreground">
                      {t("凭据限制检查")}
                    </span>
                    <span>{decision.key_allowed ? t("通过") : t("拒绝")}</span>
                  </div>
                  <div className="flex justify-between">
                    <span className="text-muted-foreground">
                      {t("权限修订")}
                    </span>
                    <code>{decision.revision}</code>
                  </div>
                </div>
              </div>
            ) : (
              <Empty
                title={t("等待检查")}
                description={t("输入资源和动作，查看当前身份的授权决策。")}
              />
            )}
          </CardContent>
        </Card>
      </div>
    </>
  );
}
export function UsersPage({
  notify,
  refresh,
  reload,
}: {
  notify: (message: string) => void;
  refresh: number;
  reload: () => void;
}) {
  useTranslation();
  const { data, error, loading } = useData<List<User>>(
    "/api/v1/system/users",
    refresh,
  );
  return (
    <>
      <PageHeader
        eyebrow={t("System administration")}
        title={t("系统用户")}
        description={t(
          "不同来源的账号独立保存权限。停用用户会撤销已有会话并阻止其 Key 访问。",
        )}
      />
      {loading ? (
        <Loading />
      ) : error ? (
        <ErrorState error={t(error)} />
      ) : (
        <DataTable
          rows={data?.items ?? []}
          getKey={(u) => u.user_id}
          columns={[
            {
              title: t("用户"),
              render: (u) => (
                <div>
                  <p className="font-medium">{u.name}</p>
                  <p className="text-xs text-muted-foreground">{u.email}</p>
                </div>
              ),
            },
            { title: t("本地 ID"), render: (u) => <ID value={u.user_id} /> },
            {
              title: t("外部身份"),
              render: (u) => (
                <div>
                  <Badge variant="outline">{u.source_id}</Badge>
                  <p className="mt-1 text-xs text-muted-foreground">
                    {u.subject}
                  </p>
                </div>
              ),
            },
            {
              title: t("状态"),
              render: (u) => <StatusBadge active={u.status === "active"} />,
            },
            {
              title: t("操作"),
              render: (u) => (
                <Button
                  variant="outline"
                  size="sm"
                  onClick={async () => {
                    try {
                      await api("/api/v1/system/users/" + u.user_id, "PATCH", {
                        status: u.status === "active" ? "disabled" : "active",
                      });
                      notify(t("用户状态已更新。"));
                      reload();
                    } catch (e) {
                      notify(errorMessage(e));
                    }
                  }}
                >
                  {u.status === "active" ? t("停用") : t("启用")}
                </Button>
              ),
            },
          ]}
        />
      )}
    </>
  );
}
export function ResourcesPage({
  base,
  system,
  notify,
  refresh,
  reload,
}: Omit<Props, "scope">) {
  useTranslation();
  const [kind, setKind] = useState("services"),
    [open, setOpen] = useState(false),
    [name, setName] = useState(""),
    [busy, setBusy] = useState(false);
  const { data, error, loading } = useData<List<Resource>>(
    base + "/" + kind,
    refresh,
  );
  if (system)
    return (
      <Empty
        title={t("请切换到组织空间")}
        description={t("服务与模型归属于具体组织。")}
      />
    );
  return (
    <>
      <PageHeader
        eyebrow={t("Resource catalog")}
        title={t("资源目录")}
        description={t(
          "管理服务与模型元数据；Workers 构建与发布可从 Workers 页面操作。",
        )}
        action={
          <Button onClick={() => setOpen(true)}>
            <Plus size={16} />
            {t("创建资源")}
          </Button>
        }
      />
      <Tabs value={kind} onValueChange={setKind} className="mb-5">
        <TabsList>
          <TabsTrigger value="services">{t("服务")}</TabsTrigger>
          <TabsTrigger value="models">{t("模型")}</TabsTrigger>
        </TabsList>
      </Tabs>
      {loading ? (
        <Loading />
      ) : error ? (
        <ErrorState error={t(error)} />
      ) : !data?.items.length ? (
        <Empty title={t("暂无资源")} />
      ) : (
        <DataTable
          rows={data.items}
          getKey={(r) => r.id}
          columns={[
            {
              title: t("名称"),
              render: (r) => <span className="font-medium">{r.name}</span>,
            },
            { title: t("资源 ID"), render: (r) => <ID value={r.id} /> },
            { title: t("修订"), render: (r) => r.revision },
            {
              title: t("操作"),
              render: (r) => (
                <Confirm
                  label={t("删除")}
                  description={t(
                    "相关直接角色授权会清理。重建同名资源将使用新 ID，旧 Key 权限不会恢复。",
                  )}
                  onConfirm={async () => {
                    try {
                      await api(base + "/" + kind + "/" + r.id, "DELETE");
                      reload();
                      notify(t("资源已删除。"));
                    } catch (e) {
                      notify(errorMessage(e));
                    }
                  }}
                />
              ),
            },
          ]}
        />
      )}
      <Dialog open={open} onOpenChange={setOpen}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>
              {t("创建")}
              {kind === "services" ? t("服务") : t("模型")}
            </DialogTitle>
            <DialogDescription>
              {t("资源创建后获得不可复用的稳定 ID。")}
            </DialogDescription>
          </DialogHeader>
          <form
            className="grid gap-5"
            onSubmit={async (e) => {
              e.preventDefault();
              setBusy(true);
              try {
                await api(base + "/" + kind, "POST", { name });
                setOpen(false);
                setName("");
                reload();
              } catch (e) {
                notify(errorMessage(e));
              } finally {
                setBusy(false);
              }
            }}
          >
            <Field label={t("资源名称")}>
              <Input
                value={name}
                onChange={(e) => setName(e.target.value)}
                required
                maxLength={128}
              />
            </Field>
            <Button disabled={busy} type="submit">
              {t("创建资源")}
            </Button>
          </form>
        </DialogContent>
      </Dialog>
    </>
  );
}
