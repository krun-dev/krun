import { t, locale } from "./i18n.ts";
import type { Policy } from "./api";

export const resourceKinds = [
  {
    kind: "organization",
    label: "组织与成员",
    description: "组织、成员及角色管理",
  },
  { kind: "service", label: "服务", description: "服务、版本与运行操作" },
  { kind: "model", label: "模型", description: "模型、版本与调用操作" },
  { kind: "api_key", label: "API Keys", description: "密钥元数据与生命周期" },
  { kind: "system", label: "平台", description: "系统用户与平台管理" },
] as const;
export type Selection = {
  actions: string[];
  all: boolean;
  ids: string;
  organization: string;
};
type Selections = Record<string, Selection>;
export type Preset = "all" | "read" | "readwrite" | "clear";
function isRead(action: string) {
  return /\.(read|list)$/.test(action);
}
export function presetActions(actions: string[], preset: Preset) {
  if (preset === "clear") return [];
  if (preset === "all") return actions;
  return actions.filter(
    (action) =>
      isRead(action) ||
      (preset === "readwrite" &&
        /\.(create|update|delete|manage|grant|revoke|attach|detach|configure)$/.test(
          action,
        )),
  );
}
export function initialSelections(): Selections {
  return Object.fromEntries(
    resourceKinds.map(({ kind }) => [
      kind,
      { actions: [], all: true, ids: "", organization: "" },
    ]),
  );
}
export function buildPolicy(
  selections: Selections,
  envs: string[],
  scope: string,
  system: boolean,
): Policy {
  return {
    schema_version: 1,
    statements: Object.entries(selections)
      .filter(([, s]) => s.actions.length > 0)
      .map(([kind, s]) => ({
        effect: "allow",
        actions: s.actions,
        resource:
          system && s.all
            ? { kind, all_in_system: true }
            : {
                kind,
                organization_id: system ? s.organization : scope,
                ...(s.all
                  ? { all_in_organization: true }
                  : {
                      ids: s.ids
                        .split(",")
                        .map((id) => id.trim())
                        .filter(Boolean),
                    }),
              },
        conditions: envs.length ? { environments: envs } : {},
      })),
  };
}

// Hand-written deny statements and multiple selectors must remain in JSON mode.
// Refuse conversion instead of silently dropping restrictions.
export function parseSelections(
  policy: Policy,
  registry: Record<string, string[]>,
  scope: string,
  system: boolean,
): { selections: Selections; envs: string[] } | null {
  if (policy.schema_version !== 1 || !Array.isArray(policy.statements))
    return null;
  const selections = initialSelections();
  const seen = new Set<string>();
  let envs: string[] | undefined;
  for (const statement of policy.statements) {
    const resource = statement.resource;
    if (
      statement.effect !== "allow" ||
      !resource ||
      !selections[resource.kind] ||
      seen.has(resource.kind) ||
      !Array.isArray(statement.actions) ||
      !statement.actions.length
    )
      return null;
    if (
      Object.keys(statement).some(
        (key) => !["effect", "actions", "resource", "conditions"].includes(key),
      ) ||
      Object.keys(resource).some(
        (key) =>
          ![
            "kind",
            "organization_id",
            "ids",
            "all_in_organization",
            "all_in_system",
          ].includes(key),
      )
    )
      return null;
    const conditions = statement.conditions ?? {};
    if (Object.keys(conditions).some((key) => key !== "environments"))
      return null;
    const environments = conditions.environments ?? [];
    if (
      !Array.isArray(environments) ||
      !environments.every((env) =>
        ["global", "development", "staging", "production"].includes(env),
      )
    )
      return null;
    if (envs && [...envs].sort().join() !== [...environments].sort().join())
      return null;
    envs = environments;
    if (
      resource.kind === "system" &&
      resource.all_in_system &&
      statement.actions.includes("*")
    )
      return null;
    const choices = Object.entries(registry)
      .filter(([, kinds]) => kinds.includes(resource.kind))
      .map(([action]) => action);
    if (
      statement.actions.some(
        (action) => action !== "*" && !choices.includes(action),
      )
    )
      return null;
    const hasIDs =
      Array.isArray(resource.ids) &&
      resource.ids.length > 0 &&
      resource.ids.every((id) => typeof id === "string" && !id.includes(","));
    if (system) {
      if (
        resource.all_in_system
          ? hasIDs || resource.all_in_organization || resource.organization_id
          : !hasIDs || resource.all_in_organization || !resource.organization_id
      )
        return null;
    } else if (
      resource.organization_id !== scope ||
      resource.all_in_system ||
      resource.all_in_organization === hasIDs ||
      (!resource.all_in_organization && !hasIDs)
    )
      return null;
    selections[resource.kind] = {
      actions: statement.actions.includes("*") ? choices : statement.actions,
      all: !!(resource.all_in_organization || resource.all_in_system),
      ids: (resource.ids ?? []).join(", "),
      organization: resource.organization_id ?? "",
    };
    seen.add(resource.kind);
  }
  return { selections, envs: envs ?? [] };
}

const names: Record<string, string> = {
  organization: "组织",
  member: "成员",
  iam: "角色",
  apiKey: "密钥",
  service: "服务",
  model: "模型",
  build: "构建",
  log: "日志",
  source: "源码",
  binding: "绑定",
  audit: "审计",
  system: "系统",
  user: "用户",
  role: "角色",
  version: "版本",
};
const verbs: Record<string, string> = {
  read: "读取",
  list: "列出",
  create: "创建",
  update: "更新",
  delete: "删除",
  manage: "管理",
  grant: "授权",
  revoke: "撤销",
  publish: "发布",
  rollback: "回滚",
  invoke: "调用",
  use: "使用",
  start: "启动",
  attach: "添加",
  detach: "移除",
  configure: "配置",
};
export function actionLabel(action: string) {
  const parts = action.split(".");
  const verb = parts.pop()!;
  return (
    t(verbs[verb] ?? verb) +
    (locale() === "en" ? " " : "") +
    parts
      .map((part) => t(names[part] ?? part))
      .join(locale() === "en" ? " " : "")
  );
}
export function keyState(
  key: { revoked: boolean; expires_at: string | null },
  time = Date.now(),
) {
  return key.revoked
    ? "revoked"
    : key.expires_at && Date.parse(key.expires_at) <= time
      ? "expired"
      : "active";
}
