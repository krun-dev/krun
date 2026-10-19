import { errorMessage, t, locale, environmentLabel } from "@/lib/i18n";
import { useTranslation } from "react-i18next";
import { useState } from "react";
import {
  Check,
  CheckCircle2,
  Copy,
  KeyRound,
  LoaderCircle,
  Plus,
  Search,
  ShieldCheck,
  Terminal,
} from "lucide-react";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Textarea } from "@/components/ui/textarea";
import { Badge } from "@/components/ui/badge";
import { Checkbox } from "@/components/ui/checkbox";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { api, type APIKey, type List, type Policy } from "@/lib/api";
import {
  actionLabel,
  buildPolicy,
  initialSelections,
  keyState,
  parseSelections,
  presetActions,
  resourceKinds,
  type Preset,
  type Selection,
} from "@/lib/permissions";
import { Empty, Field, Loading, PageHeader, SelectField } from "./common";
import {
  Confirm,
  DataTable,
  ErrorState,
  useData,
  type Props,
} from "./management";

const presets: { id: Preset; label: string }[] = [
  { id: "all", label: "全部 API" },
  { id: "read", label: "只读" },
  { id: "readwrite", label: "读写" },
  { id: "clear", label: "清空" },
];
const statusLabels = { active: "有效", expired: "已过期", revoked: "已撤销" };
function expiryLabel(key: APIKey) {
  return key.expires_at
    ? new Date(key.expires_at).toLocaleDateString(locale())
    : t("长期有效");
}
function PolicySummary({ policy }: { policy: Policy }) {
  useTranslation();
  return (
    <div className="flex flex-wrap gap-1.5">
      {policy.statements.map((s, index) => (
        <Badge
          key={index}
          variant="outline"
          className="font-normal text-muted-foreground"
        >
          {s.effect === "deny" ? t("拒绝 · ") : ""}
          {t(
            resourceKinds.find((k) => k.kind === s.resource.kind)?.label ??
              "全部资源",
          )}
          {s.resource.all_in_system || s.resource.all_in_organization
            ? " *"
            : t(" · {{v0}} 个资源", {
                v0: s.resource.ids?.length ?? 0,
                count: s.resource.ids?.length ?? 0,
              })}
          <span className="border-l pl-1.5 text-foreground">
            {s.actions.includes("*")
              ? t("全部操作")
              : t("{{v0}} 项", {
                  v0: s.actions.length,
                  count: s.actions.length,
                })}
          </span>
        </Badge>
      ))}
    </div>
  );
}

export function KeysPage(p: Props) {
  useTranslation();
  const { data, error, loading } = useData<List<APIKey>>(
    p.base + "/api-keys",
    p.refresh,
  );
  const registry = useData<{ actions: Record<string, string[]> }>(
    "/api/v1/roles",
    0,
  );
  const [open, setOpen] = useState(false),
    [secret, setSecret] = useState(""),
    [busy, setBusy] = useState(false),
    [name, setName] = useState(""),
    [expiry, setExpiry] = useState("2592000"),
    [envs, setEnvs] = useState<string[]>([]),
    [selections, setSelections] = useState(initialSelections),
    [advanced, setAdvanced] = useState(false),
    [raw, setRaw] = useState(""),
    [createError, setCreateError] = useState(""),
    [copied, setCopied] = useState(false),
    [search, setSearch] = useState(""),
    [status, setStatus] = useState("all"),
    [permissionSearch, setPermissionSearch] = useState(""),
    [detail, setDetail] = useState<APIKey | null>(null);
  const groups = resourceKinds.filter((k) => p.system || k.kind !== "system");
  const catalog = registry.data?.actions ?? {};
  const choicesFor = (kind: string) =>
    Object.entries(catalog)
      .filter(([, kinds]) => kinds.includes(kind))
      .map(([action]) => action)
      .sort();
  const selectedCount = Object.values(selections).reduce(
    (sum, s) => sum + s.actions.length,
    0,
  );
  const items = data?.items ?? [];
  const visible = items.filter(
    (key) =>
      (status === "all" || keyState(key) === status) &&
      `${key.name} ${key.id} ${key.prefix}`
        .toLowerCase()
        .includes(search.toLowerCase()),
  );
  function update(kind: string, patch: Partial<Selection>) {
    setSelections((current) => ({
      ...current,
      [kind]: { ...current[kind], ...patch },
    }));
  }
  function choose(preset: Preset, kind?: string) {
    setSelections((current) =>
      Object.fromEntries(
        Object.entries(current).map(([key, value]) => [
          key,
          (!kind || key === kind) && groups.some((group) => group.kind === key)
            ? { ...value, actions: presetActions(choicesFor(key), preset) }
            : value,
        ]),
      ),
    );
  }
  function startCreate() {
    setName("");
    setExpiry("2592000");
    setEnvs([]);
    setSelections(initialSelections());
    setAdvanced(false);
    setRaw("");
    setPermissionSearch("");
    setCreateError("");
    setOpen(true);
  }
  function switchMode(mode: string) {
    setCreateError("");
    if (mode === "json") {
      setRaw(
        JSON.stringify(
          buildPolicy(selections, envs, p.scope, p.system),
          null,
          2,
        ),
      );
      setAdvanced(true);
    } else {
      try {
        const parsed = parseSelections(
          JSON.parse(raw),
          catalog,
          p.scope,
          p.system,
        );
        if (!parsed)
          throw new Error(
            t(
              "该策略包含可视化表单无法表达的规则，请继续在 JSON 中编辑，以保留全部限制。",
            ),
          );
        setSelections(parsed.selections);
        setEnvs(parsed.envs);
        setAdvanced(false);
      } catch (e) {
        setCreateError(errorMessage(e));
      }
    }
  }
  async function issue() {
    setBusy(true);
    setCreateError("");
    try {
      const response = await api<{ key: string }>(
        p.base + "/api-keys",
        "POST",
        {
          name,
          expires_in: Number(expiry),
          policy: advanced
            ? JSON.parse(raw)
            : buildPolicy(selections, envs, p.scope, p.system),
        },
      );
      setSecret(response.key);
      setCopied(false);
      setOpen(false);
      p.reload();
    } catch (e) {
      setCreateError(errorMessage(e));
    } finally {
      setBusy(false);
    }
  }
  async function revoke(id: string) {
    try {
      await api("/api/v1/api-keys/" + id, "DELETE");
      p.notify(t("API Key 已撤销。"));
      p.reload();
    } catch (e) {
      p.notify(errorMessage(e));
    }
  }
  return (
    <>
      <PageHeader
        eyebrow={t("Access management")}
        title={t("API Keys")}
        description={t("管理脚本、CLI 和自动化任务的访问凭据。")}
        action={
          <Button onClick={startCreate}>
            <Plus size={16} />
            {t("创建 API Key")}
          </Button>
        }
      />
      <div className="mb-7 flex flex-wrap items-center gap-x-6 gap-y-3 border-b pb-4 text-sm">
        <span className="flex items-center gap-2 font-medium">
          <KeyRound size={15} className="text-primary" />
          {t("访问密钥")}
          <Badge variant="secondary">{items.length}</Badge>
        </span>
        <span className="ml-auto flex items-center gap-2 text-xs text-muted-foreground">
          <ShieldCheck size={14} />
          {t("权限始终受所属用户的当前角色约束")}
        </span>
      </div>
      <div className="mb-5 flex flex-wrap items-center gap-3">
        <div className="relative min-w-48 flex-1 sm:max-w-sm">
          <Search
            size={15}
            className="absolute left-3 top-2.5 text-muted-foreground"
          />
          <Input
            aria-label={t("搜索 API Key")}
            className="pl-9"
            placeholder={t("搜索名称、前缀或 ID…")}
            value={search}
            onChange={(e) => setSearch(e.target.value)}
          />
        </div>
        <div className="w-36">
          <SelectField
            label={t("密钥状态")}
            value={status}
            onChange={setStatus}
          >
            <option value="all">{t("全部状态")}</option>
            <option value="active">{t("有效")}</option>
            <option value="expired">{t("已过期")}</option>
            <option value="revoked">{t("已撤销")}</option>
          </SelectField>
        </div>
        <span className="ml-auto text-xs text-muted-foreground">
          {t("{{count}} 个密钥", { count: visible.length })}
        </span>
      </div>
      {loading ? (
        <Loading />
      ) : error ? (
        <ErrorState error={error} />
      ) : !items.length ? (
        <Empty
          title={t("创建你的第一个 API Key")}
          description={t("选择所需的 API 和资源范围，连接你的自动化工作流。")}
          action={
            <Button variant="outline" onClick={startCreate}>
              <Plus size={15} />
              {t("创建 API Key")}
            </Button>
          }
        />
      ) : !visible.length ? (
        <Empty
          title={t("没有匹配的密钥")}
          description={t("试试其他关键词或状态。")}
        />
      ) : (
        <DataTable
          rows={visible}
          getKey={(key) => key.id}
          columns={[
            {
              title: t("名称"),
              render: (key) => (
                <button
                  className="flex items-center gap-3 text-left"
                  onClick={() => setDetail(key)}
                >
                  <div className="rounded-md border bg-secondary/40 p-2 text-muted-foreground">
                    <KeyRound size={16} />
                  </div>
                  <div>
                    <p className="text-sm font-medium hover:text-primary">
                      {key.name}
                    </p>
                    <code className="mt-1 block text-[11px] text-muted-foreground">
                      {key.prefix}••••••
                    </code>
                  </div>
                </button>
              ),
            },
            {
              title: t("权限范围"),
              render: (key) => (
                <div className="max-w-sm">
                  <PolicySummary policy={key.policy} />
                </div>
              ),
            },
            {
              title: t("有效期"),
              render: (key) => (
                <span className="whitespace-nowrap text-xs text-muted-foreground">
                  {expiryLabel(key)}
                </span>
              ),
            },
            {
              title: t("状态"),
              render: (key) => (
                <Badge
                  variant="outline"
                  className={
                    keyState(key) === "active"
                      ? "border-primary/25 bg-primary/10 font-normal text-primary"
                      : "font-normal text-muted-foreground"
                  }
                >
                  <span
                    className={`size-1.5 rounded-full ${keyState(key) === "active" ? "bg-primary" : "bg-muted-foreground"}`}
                  />
                  {t(statusLabels[keyState(key)])}
                </Badge>
              ),
            },
            {
              title: "",
              render: (key) => (
                <div className="flex items-center justify-end gap-1">
                  <Button
                    variant="ghost"
                    size="sm"
                    onClick={() => setDetail(key)}
                  >
                    {t("查看权限")}
                  </Button>
                  <Confirm
                    label={t("撤销")}
                    disabled={key.revoked}
                    description={t(
                      "此 Key 将无法再用于新的 API 请求，此操作不能恢复。",
                    )}
                    onConfirm={() => revoke(key.id)}
                  />
                </div>
              ),
            },
          ]}
        />
      )}
      <div className="mt-7 grid gap-4 rounded-lg border bg-card/40 p-5 sm:grid-cols-[auto_1fr]">
        <Terminal size={18} className="mt-0.5 text-muted-foreground" />
        <div>
          <p className="text-sm font-medium">{t("在终端中使用")}</p>
          <p className="mt-1 text-xs leading-6 text-muted-foreground">
            {t("通过标准输入登录，或在自动化环境中设置 KRUN_TOKEN。")}
          </p>
          <code className="mt-2 block text-xs text-muted-foreground">
            krun login --token-stdin
          </code>
        </div>
      </div>
      <Dialog
        open={open}
        onOpenChange={(value) => {
          if (!busy) setOpen(value);
        }}
      >
        <DialogContent className="flex max-h-[92dvh] flex-col gap-0 overflow-hidden p-0 sm:max-w-3xl">
          <DialogHeader className="border-b px-6 py-5">
            <DialogTitle className="flex items-center gap-2">
              <KeyRound size={18} className="text-primary" />
              {t("创建 API Key")}
            </DialogTitle>
            <DialogDescription>
              {t(
                "选择自动化所需权限。所有资源类型直接展开，支持一次配置多种资源。",
              )}
            </DialogDescription>
          </DialogHeader>
          <form
            className="flex min-h-0 flex-col"
            onSubmit={(e) => {
              e.preventDefault();
              void issue();
            }}
          >
            <div className="min-h-0 space-y-6 overflow-y-auto px-6 py-5">
              <div className="grid gap-4 sm:grid-cols-[1fr_180px]">
                <Field label={t("名称")}>
                  <Input
                    required
                    maxLength={128}
                    value={name}
                    onChange={(e) => setName(e.target.value)}
                    placeholder={t("例如 production-ci")}
                  />
                </Field>
                <Field label={t("有效期")}>
                  <SelectField
                    label={t("有效期")}
                    value={expiry}
                    onChange={setExpiry}
                  >
                    <option value="0">{t("长期有效")}</option>
                    <option value="3600">{t("1 小时")}</option>
                    <option value="86400">{t("1 天")}</option>
                    <option value="604800">{t("7 天")}</option>
                    <option value="2592000">{t("30 天")}</option>
                    <option value="7776000">{t("90 天")}</option>
                  </SelectField>
                </Field>
              </div>
              <div className="flex items-start gap-2.5 rounded-md border border-primary/20 bg-primary/5 px-3 py-3">
                <ShieldCheck
                  size={16}
                  className="mt-0.5 shrink-0 text-primary"
                />
                <p className="text-xs leading-5 text-muted-foreground">
                  {t(
                    "{{scope}}的 * 范围包含未来资源；每次请求仍检查你的实时权限。撤销角色或成员资格后，Key 会立即失去相应访问权限。",
                    { scope: p.system ? t("跨组织") : t("当前组织") },
                  )}
                </p>
              </div>
              <Tabs
                value={advanced ? "json" : "builder"}
                onValueChange={switchMode}
              >
                <TabsList className="mb-4">
                  <TabsTrigger value="builder">{t("选择权限")}</TabsTrigger>
                  <TabsTrigger value="json">{t("策略 JSON")}</TabsTrigger>
                </TabsList>
                <TabsContent value="builder" className="space-y-4">
                  {registry.loading ? (
                    <Loading />
                  ) : registry.error ? (
                    <ErrorState error={registry.error} />
                  ) : (
                    <>
                      <div className="flex flex-wrap items-center justify-between gap-3">
                        <p className="text-sm font-medium">
                          {t("允许的 API")}{" "}
                          <span className="ml-1 text-xs font-normal text-muted-foreground">
                            {t("已选 {{count}} 项", { count: selectedCount })}
                          </span>
                        </p>
                        <div className="flex flex-wrap gap-1.5">
                          {presets.map((preset) => (
                            <Button
                              type="button"
                              key={preset.id}
                              variant="outline"
                              size="xs"
                              onClick={() => choose(preset.id)}
                            >
                              {t(preset.label)}
                            </Button>
                          ))}
                        </div>
                      </div>
                      <Input
                        aria-label={t("搜索权限")}
                        placeholder={t("搜索 API 名称或操作…")}
                        value={permissionSearch}
                        onChange={(e) => setPermissionSearch(e.target.value)}
                      />
                      <p className="text-[11px] leading-5 text-muted-foreground">
                        {t(
                          "读写包含读取、创建、更新、删除及管理操作。发布、回滚、执行与调用可单独勾选；目录也包含预留的领域动作。",
                        )}
                      </p>
                      <div className="space-y-3">
                        {groups.map((group) => {
                          const choices = choicesFor(group.kind);
                          const filtered = choices.filter((action) =>
                            `${action} ${actionLabel(action)} ${t(group.label)}`
                              .toLowerCase()
                              .includes(permissionSearch.toLowerCase()),
                          );
                          const selection = selections[group.kind];
                          if (!filtered.length) return null;
                          return (
                            <section
                              key={group.kind}
                              className="overflow-hidden rounded-lg border bg-card"
                              aria-label={t("{{v0}}权限", {
                                v0: t(group.label),
                              })}
                            >
                              <div className="flex flex-wrap items-center gap-3 border-b bg-secondary/35 px-4 py-3">
                                <Checkbox
                                  aria-label={t("选择{{v0}}全部 API", {
                                    v0: t(group.label),
                                  })}
                                  checked={
                                    selection.actions.length === choices.length
                                      ? true
                                      : selection.actions.length
                                        ? "indeterminate"
                                        : false
                                  }
                                  onCheckedChange={(value) =>
                                    choose(
                                      value === true ? "all" : "clear",
                                      group.kind,
                                    )
                                  }
                                />
                                <div>
                                  <h3 className="text-sm font-medium">
                                    {t(group.label)}{" "}
                                    <span className="ml-1.5 font-mono text-[10px] font-normal text-muted-foreground">
                                      {group.kind}
                                    </span>
                                  </h3>
                                  <p className="mt-0.5 text-[11px] text-muted-foreground">
                                    {t(group.description)}
                                  </p>
                                </div>
                                <div className="ml-auto flex gap-1">
                                  <Button
                                    type="button"
                                    variant="ghost"
                                    size="xs"
                                    onClick={() => choose("read", group.kind)}
                                  >
                                    {t("只读")}
                                  </Button>
                                  <Button
                                    type="button"
                                    variant="ghost"
                                    size="xs"
                                    onClick={() =>
                                      choose("readwrite", group.kind)
                                    }
                                  >
                                    {t("读写")}
                                  </Button>
                                  <span className="ml-1 self-center text-[11px] tabular-nums text-muted-foreground">
                                    {selection.actions.length}/{choices.length}
                                  </span>
                                </div>
                              </div>
                              <div className="grid gap-1 p-3 sm:grid-cols-3">
                                {filtered.map((action) => (
                                  <label
                                    key={action}
                                    className="flex cursor-pointer items-start gap-2.5 rounded-md p-2 transition-colors hover:bg-secondary/60"
                                  >
                                    <Checkbox
                                      aria-label={`${t(group.label)} ${action}`}
                                      className="mt-0.5"
                                      checked={selection.actions.includes(
                                        action,
                                      )}
                                      onCheckedChange={(value) =>
                                        setSelections((current) => ({
                                          ...current,
                                          [group.kind]: {
                                            ...current[group.kind],
                                            actions:
                                              value === true
                                                ? [
                                                    ...new Set([
                                                      ...current[group.kind]
                                                        .actions,
                                                      action,
                                                    ]),
                                                  ]
                                                : current[
                                                    group.kind
                                                  ].actions.filter(
                                                    (a) => a !== action,
                                                  ),
                                          },
                                        }))
                                      }
                                    />
                                    <span>
                                      <span className="block text-xs">
                                        {actionLabel(action)}
                                      </span>
                                      <code className="mt-1 block break-all text-[10px] text-muted-foreground">
                                        {action}
                                      </code>
                                    </span>
                                  </label>
                                ))}
                              </div>
                              {selection.actions.length > 0 && (
                                <div className="space-y-3 border-t px-4 py-3">
                                  <label className="flex cursor-pointer items-center gap-2 text-xs">
                                    <Checkbox
                                      aria-label={t("{{v0}}全部资源", {
                                        v0: t(group.label),
                                      })}
                                      disabled={group.kind === "system"}
                                      checked={selection.all}
                                      onCheckedChange={(value) =>
                                        update(group.kind, {
                                          all: value === true,
                                        })
                                      }
                                    />
                                    {t("全部{{resource}}资源", {
                                      resource: t(group.label),
                                    })}{" "}
                                    <code className="text-primary">*</code>
                                    <span className="ml-auto text-[11px] text-muted-foreground">
                                      {t("受我的权限约束")}
                                    </span>
                                  </label>
                                  {!selection.all && (
                                    <div className="grid gap-3 sm:grid-cols-2">
                                      {p.system && (
                                        <Field
                                          label={t("{{v0}}所属组织 ID", {
                                            v0: t(group.label),
                                          })}
                                        >
                                          <Input
                                            required
                                            value={selection.organization}
                                            onChange={(e) =>
                                              update(group.kind, {
                                                organization: e.target.value,
                                              })
                                            }
                                            placeholder="org_…"
                                          />
                                        </Field>
                                      )}
                                      <Field
                                        label={t("{{v0}}资源 ID", {
                                          v0: t(group.label),
                                        })}
                                        hint={t("多个 ID 用逗号分隔")}
                                      >
                                        <Input
                                          required
                                          value={selection.ids}
                                          onChange={(e) =>
                                            update(group.kind, {
                                              ids: e.target.value,
                                            })
                                          }
                                          placeholder={
                                            group.kind === "organization"
                                              ? p.scope
                                              : `${group.kind}_…`
                                          }
                                        />
                                      </Field>
                                    </div>
                                  )}
                                </div>
                              )}
                            </section>
                          );
                        })}
                      </div>
                    </>
                  )}
                  <div className="space-y-3 border-t pt-4">
                    <p className="text-sm font-medium">
                      {t("环境条件")}{" "}
                      <span className="ml-2 text-xs font-normal text-muted-foreground">
                        {t("不选则允许全部环境")}
                      </span>
                    </p>
                    <div className="flex flex-wrap gap-5">
                      {["global", "development", "test", "staging", "production"].map(
                        (env) => (
                          <label
                            key={env}
                            className="flex items-center gap-2 text-xs"
                          >
                            <Checkbox
                              checked={envs.includes(env)}
                              onCheckedChange={(value) =>
                                setEnvs((current) =>
                                  value === true
                                    ? [...current, env]
                                    : current.filter((e) => e !== env),
                                )
                              }
                            />
                            {environmentLabel(env)}
                          </label>
                        ),
                      )}
                    </div>
                    <p className="text-[11px] text-muted-foreground">
                      {t("全局详情读取需要 global。")}
                    </p>
                  </div>
                </TabsContent>
                <TabsContent value="json">
                  <Textarea
                    aria-label={t("Key 策略 JSON")}
                    className="min-h-80 font-mono text-xs leading-6"
                    value={raw}
                    onChange={(e) => setRaw(e.target.value)}
                  />
                </TabsContent>
              </Tabs>
              {createError && <ErrorState error={createError} />}
            </div>
            <div className="flex flex-wrap items-center justify-between gap-3 border-t bg-card px-6 py-4">
              <span className="text-xs text-muted-foreground">
                {advanced
                  ? t("自定义策略")
                  : t("{{v0}} 项 API · {{v1}}", {
                      v0: selectedCount,
                      v1: envs.length
                        ? t("{{count}} 个环境", { count: envs.length })
                        : t("全部环境"),
                    })}{" "}
                · {expiry === "0" ? t("长期有效") : t("到期自动失效")}
              </span>
              <div className="flex gap-2">
                <Button
                  type="button"
                  variant="outline"
                  disabled={busy}
                  onClick={() => setOpen(false)}
                >
                  {t("取消")}
                </Button>
                <Button
                  type="submit"
                  disabled={
                    busy ||
                    (!advanced &&
                      (!selectedCount || registry.loading || !!registry.error))
                  }
                >
                  {busy ? (
                    <LoaderCircle size={15} className="animate-spin" />
                  ) : (
                    <Plus size={15} />
                  )}
                  {t("创建密钥")}
                </Button>
              </div>
            </div>
          </form>
        </DialogContent>
      </Dialog>
      <Dialog
        open={!!secret}
        onOpenChange={(value) => {
          if (!value) {
            setSecret("");
            setCopied(false);
          }
        }}
      >
        <DialogContent>
          <DialogHeader>
            <DialogTitle className="flex items-center gap-2">
              <CheckCircle2 size={20} className="text-primary" />
              {t("API Key 已创建")}
            </DialogTitle>
            <DialogDescription>
              {t("完整凭据只显示这一次。请保存到你的 Secret 管理系统。")}
            </DialogDescription>
          </DialogHeader>
          <code className="select-all break-all rounded-md border bg-secondary/50 p-4 text-xs leading-6">
            {secret}
          </code>
          <Button
            onClick={async () => {
              try {
                await navigator.clipboard.writeText(secret);
                setCopied(true);
              } catch {
                p.notify(t("复制失败，请手动复制。"));
              }
            }}
          >
            {copied ? <Check size={16} /> : <Copy size={16} />}
            {copied ? t("已复制") : t("复制凭据")}
          </Button>
          <p className="text-xs text-muted-foreground">
            {t("使用 krun login --token-stdin 从标准输入读取。")}
          </p>
        </DialogContent>
      </Dialog>
      <Dialog
        open={!!detail}
        onOpenChange={(value) => {
          if (!value) setDetail(null);
        }}
      >
        <DialogContent className="max-h-[85dvh] overflow-auto sm:max-w-2xl">
          {detail && (
            <>
              <DialogHeader>
                <DialogTitle>{detail.name}</DialogTitle>
                <DialogDescription>
                  {t("密钥的有效权限 = 策略允许范围 ∩ 所属用户当前权限。")}
                </DialogDescription>
              </DialogHeader>
              <dl className="grid grid-cols-2 gap-4 rounded-md border p-4 text-xs">
                <div>
                  <dt className="text-muted-foreground">{t("密钥 ID")}</dt>
                  <dd className="mt-1 break-all font-mono">{detail.id}</dd>
                </div>
                <div>
                  <dt className="text-muted-foreground">{t("所属用户")}</dt>
                  <dd className="mt-1 break-all font-mono">
                    {detail.owner_user_id}
                  </dd>
                </div>
                <div>
                  <dt className="text-muted-foreground">{t("有效期")}</dt>
                  <dd className="mt-1">{expiryLabel(detail)}</dd>
                </div>
                <div>
                  <dt className="text-muted-foreground">
                    {t("状态 / 策略修订")}
                  </dt>
                  <dd className="mt-1">
                    {t(statusLabels[keyState(detail)])} /{" "}
                    {detail.policy_revision}
                  </dd>
                </div>
              </dl>
              <PolicySummary policy={detail.policy} />
              <pre className="overflow-auto rounded-md border bg-secondary/30 p-4 text-xs leading-6">
                {JSON.stringify(detail.policy, null, 2)}
              </pre>
            </>
          )}
        </DialogContent>
      </Dialog>
    </>
  );
}
