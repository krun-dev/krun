import { t } from "./i18n.ts";

export type WorkerBucket = {
  resource_path?: string;
  id: string;
  organization_id: string;
  name: string;
  enabled: boolean;
  revision: number;
  created_at: string;
};
export type R2Binding = { bucket_id: string; bucket_name: string };
export type R2BindingRow = { id: string; name: string; bucket: string };

export type BucketObject = {
  key: string;
  size: number;
  etag: string;
  uploaded: string;
  version?: string;
  r2_compatible?: boolean;
  http_metadata?: Record<string, string>;
  custom_metadata?: Record<string, string>;
};
export type ObjectPage = {
  objects: BucketObject[];
  delimited_prefixes: string[];
  cursor?: string;
  truncated: boolean;
  delivery_available: boolean;
};
export type ObjectAccess = {
  key: string;
  url: string;
  method: string;
  headers: Record<string, string>;
  expires_at: string;
};

// Signed URLs point to the provider, never through the console API. Do not use
// api(): its CSRF header and session credentials belong only to our own origin.
export function uploadObject(
  access: ObjectAccess,
  file: Blob,
  signal: AbortSignal,
  progress: (percent: number) => void,
): Promise<void> {
  return new Promise((resolve, reject) => {
    const request = new XMLHttpRequest();
    const abort = () => request.abort();
    const finish = () => signal.removeEventListener("abort", abort);
    request.open(access.method, access.url);
    request.withCredentials = false;
    request.timeout = 30 * 60 * 1000;
    for (const [name, value] of Object.entries(access.headers)) {
      request.setRequestHeader(name, value);
    }
    request.upload.onprogress = (event) => {
      if (event.lengthComputable) {
        progress(Math.round((event.loaded / event.total) * 100));
      }
    };
    request.onload = () => {
      finish();
      if (request.status >= 200 && request.status < 300) {
        resolve();
      } else {
        reject(new Error(t("存储直传失败，请检查权限、链接有效期及存储桶 CORS 配置。")));
      }
    };
    request.onerror = request.ontimeout = () => {
      finish();
      reject(new Error(t("存储直传失败，请检查权限、链接有效期及存储桶 CORS 配置。")));
    };
    request.onabort = () => {
      finish();
      reject(new DOMException("Aborted", "AbortError"));
    };
    signal.addEventListener("abort", abort, { once: true });
    if (signal.aborted) {
      finish();
      reject(new DOMException("Aborted", "AbortError"));
      return;
    }
    request.send(file);
  });
}

export function bindingRows(bindings: Record<string, R2Binding> = {}): R2BindingRow[] {
  return Object.entries(bindings).map(([name, bucket]) => ({
    id: crypto.randomUUID(),
    name,
    bucket: bucket.bucket_name,
  }));
}

export function bindingInput(
  rows: R2BindingRow[],
  variables: unknown,
  secrets: string[],
  metadata: string,
) {
  const used = new Set([
    ...Object.keys(variables || {}),
    ...secrets,
    ...(metadata ? [metadata] : []),
  ]);
  const result: Record<string, string> = Object.create(null);
  for (const row of rows) {
    const name = row.name.trim();
    if (
      !/^[A-Za-z_][A-Za-z0-9_]{0,63}$/.test(name) ||
      name.startsWith("KRUN_") ||
      ["__proto__", "prototype", "constructor"].includes(name)
    ) {
      throw new Error(
        t(
          "对象存储绑定名称须为 1–64 位字母、数字或下划线，以字母或下划线开头，不能使用 KRUN_ 前缀。",
        ),
      );
    }
    if (used.has(name)) {
      throw new Error(t("对象存储绑定名称重复，或与变量、Secret、版本元数据冲突。"));
    }
    if (!row.bucket) {
      throw new Error(t("请为每个对象存储绑定选择一个桶。"));
    }
    used.add(name);
    result[name] = row.bucket;
  }
  return result;
}
