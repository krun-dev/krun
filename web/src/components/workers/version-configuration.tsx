import { t } from "@/lib/i18n";
import type { Version } from "@/lib/workers";

type Configuration = Pick<
  Version,
  "variables" | "r2_buckets" | "hyperdrive_bindings" | "version_metadata" | "assets_binding"
> & { secret_names?: string[]; secret_digest?: string };

export function VersionConfiguration({ config }: { config: Configuration }) {
  const bindings = Object.entries(config.r2_buckets || {});
  const hyperdrive = Object.entries(config.hyperdrive_bindings || {});
  return (
    <div className="grid gap-3 text-sm">
      <details className="rounded-md border p-3">
        <summary className="cursor-pointer font-medium">
          {t("普通变量（{{count}}）", { count: Object.keys(config.variables || {}).length })}
        </summary>
        <pre className="mt-3 max-h-48 overflow-auto whitespace-pre-wrap break-all text-xs">
          {JSON.stringify(config.variables || {}, null, 2)}
        </pre>
      </details>
      <div>
        <p className="font-medium">{t("对象存储绑定")}</p>
        {bindings.length ? (
          <ul className="mt-1 grid gap-1 break-all text-xs text-muted-foreground">
            {bindings.map(([name, bucket]) => (
              <li key={name}>
                <code>{name}</code> → {bucket.bucket_name}
              </li>
            ))}
          </ul>
        ) : (
          <p className="mt-1 text-xs text-muted-foreground">{t("无")}</p>
        )}
      </div>
      <div>
        <p className="font-medium">{t("Hyperdrive 绑定")}</p>
        {hyperdrive.length ? (
          <ul className="mt-1 grid gap-1 break-all text-xs text-muted-foreground">
            {hyperdrive.map(([name, id]) => (
              <li key={name}>
                <code>{name}</code> → {id}
              </li>
            ))}
          </ul>
        ) : (
          <p className="mt-1 text-xs text-muted-foreground">{t("无")}</p>
        )}
      </div>
      <div>
        <p className="font-medium">{t("Secrets")}</p>
        <p className="mt-1 break-all text-xs text-muted-foreground">
          {config.secret_names
            ? config.secret_names.join("、") || t("无")
            : config.secret_digest
              ? t("已固定 Secret 版本引用，值不可读取。")
              : t("无")}
        </p>
      </div>
      <dl className="grid gap-2 text-xs">
        <div className="flex flex-wrap justify-between gap-2">
          <dt className="text-muted-foreground">{t("版本元数据绑定")}</dt>
          <dd className="break-all font-mono">{config.version_metadata || t("无")}</dd>
        </div>
        <div className="flex flex-wrap justify-between gap-2">
          <dt className="text-muted-foreground">{t("静态资产绑定")}</dt>
          <dd className="break-all font-mono">{config.assets_binding || t("无")}</dd>
        </div>
      </dl>
    </div>
  );
}
