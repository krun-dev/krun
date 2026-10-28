import { t, locale } from "./i18n.ts";
import { notifyAuthRequired } from "./auth-required.ts";
export class APIError extends Error {
  status: number;
  code: string;
  requestID?: string;

  constructor(
    status: number,
    code: string,
    message: string,
    requestID?: string,
  ) {
    super(message);
    this.status = status;
    this.code = code;
    this.requestID = requestID;
  }
}
export async function api<T>(
  path: string,
  method = "GET",
  input?: unknown,
  signal?: AbortSignal,
): Promise<T> {
  if (!path.startsWith("/api/")) {
    throw new APIError(0, "INVALID_API_PATH", "Refusing a non-API request path");
  }
  signal = signal
    ? AbortSignal.any([signal, AbortSignal.timeout(20000)])
    : AbortSignal.timeout(20000);
  const headers: Record<string, string> = {
    "Content-Type": "application/json",
    "Accept-Language": locale(),
  };
  if (method !== "GET") {
    const response = await fetch("/api/auth/csrf", {
      method: "POST",
      credentials: "same-origin",
      signal,
      headers: { "Accept-Language": locale() },
    });
    notifyAuthRequired(response.status);
    if (!response.ok)
      throw new APIError(
        response.status,
        "CSRF_UNAVAILABLE",
        t("无法获取安全令牌"),
      );
    headers["X-CSRF-Token"] = (await response.json()).csrf_token;
  }
  const response = await fetch(path, {
    method,
    headers,
    credentials: "same-origin",
    signal,
    body: input === undefined ? undefined : JSON.stringify(input),
  });
  notifyAuthRequired(response.status);
  if (response.status === 204) return undefined as T;
  const raw = await response.text();
  let data;
  try {
    data = raw ? JSON.parse(raw) : {};
  } catch {
    throw new APIError(
      response.status,
      "INVALID_RESPONSE",
      t("控制服务响应异常，请稍后刷新。"),
    );
  }
  if (!response.ok)
    throw new APIError(
      response.status,
      data.error?.code,
      data.error?.message ?? t("请求失败"),
      data.request_id,
    );
  return data as T;
}
export type User = {
  user_id: string;
  name: string;
  email?: string;
  source_id: string;
  subject: string;
  status: string;
};
export type Me = {
  user: User;
  system_admin: boolean;
  credential_kind: string;
  revision: number;
  workers_configured: boolean;
};
export type Organization = {
  id: string;
  name: string;
  allowed_sources: string[];
  revision: number;
};
export type Group = {
  id: string;
  name: string;
  scope_kind: string;
  scope_id: string;
  members: string[];
  revision: number;
};
export type Assignment = {
  assignment_id: string;
  subject_kind: string;
  subject_id: string;
  role_id: string;
  scope_kind: string;
  scope_id: string;
  conditions: { environments?: string[] };
  revision: number;
};
export type Resource = {
  id: string;
  kind: string;
  name: string;
  organization_id: string;
  revision: number;
};
export type APIKey = {
  id: string;
  name: string;
  owner_user_id: string;
  prefix: string;
  revoked: boolean;
  expires_at: string | null;
  policy_revision: number;
  policy: Policy;
};
export type Policy = {
  schema_version: number;
  statements: {
    effect: "allow" | "deny";
    actions: string[];
    resource: {
      organization_id?: string;
      kind: string;
      ids?: string[];
      all_in_organization?: boolean;
      all_in_system?: boolean;
    };
    conditions: { environments?: string[] };
  }[];
};
export type Provider = { id: string; type: string; name: string };
export type Decision = {
  allowed: boolean;
  user_allowed: boolean;
  key_allowed: boolean;
  revision: number;
  reason: string;
};
type Audit = {
  id: string;
  time: string;
  actor: string;
  credential: string;
  action: string;
  resource: string;
  allowed: boolean;
  request_id: string;
};
export type AuditPageResponse = {
  items: Audit[];
  total: number;
  limit: number;
  has_more: boolean;
  cursor: string;
  next_cursor: string;
  as_of: string;
  retention_days: number;
};
export type List<T> = { items: T[] };
