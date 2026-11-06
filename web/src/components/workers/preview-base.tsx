import { useEffect, useState } from "react";
import { api } from "@/lib/api";
import { t, errorMessage } from "@/lib/i18n";
import type { Environment } from "@/lib/workers";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Textarea } from "@/components/ui/textarea";
import { Field } from "@/components/common";
import { R2BindingsEditor } from "./r2-bindings";
import { bindingRows, bindingInput } from "@/lib/object-storage";
export function PreviewBasePanel({
  base,
  organizationBase,
  notify,
}: {
  base: string;
  organizationBase: string;
  notify: (s: string) => void;
}) {
  const [config, setConfig] = useState<Environment>();
  const [variables, setVariables] = useState("{}");
  const [versionMetadata, setVersionMetadata] = useState("");
  const [r2Bindings, setR2Bindings] = useState(() => bindingRows());
  const [name, setName] = useState("");
  const [secret, setSecret] = useState("");
  const [busy, setBusy] = useState(false);
  async function load() {
    const value = await api<Environment>(base + "/previews-base");
    setConfig(value);
    setVariables(JSON.stringify(value.variables, null, 2));
    setVersionMetadata(value.version_metadata || "");
    setR2Bindings(bindingRows(value.r2_buckets));
  }
  useEffect(() => {
    let current = true;
    void api<Environment>(base + "/previews-base")
      .then((v) => {
        if (current) {
          setConfig(v);
          setVariables(JSON.stringify(v.variables, null, 2));
          setVersionMetadata(v.version_metadata || "");
          setR2Bindings(bindingRows(v.r2_buckets));
        }
      })
      .catch((e) => {
        if (current) notify(errorMessage(e));
      });
    return () => {
      current = false;
    };
  }, [base, notify]);
  async function save(secretChange: boolean) {
    if (!config) return;
    setBusy(true);
    try {
      if (secretChange) {
        await api(base + "/previews-base/secrets", "PATCH", {
          secrets: { [name]: secret },
          expected_revision: 0,
          idempotency_key: crypto.randomUUID(),
        });
        setSecret("");
        setName("");
      } else {
        await api(base + "/previews-base", "PATCH", {
          variables: JSON.parse(variables),
          version_metadata: versionMetadata,
          r2_buckets: bindingInput(
            r2Bindings,
            JSON.parse(variables),
            config.secret_names || [],
            versionMetadata,
          ),
          expected_revision: config.configuration_revision,
        });
      }
      await load();
      notify(t("预览基础配置已保存，仅影响新建 Preview。"));
    } catch (e) {
      notify(errorMessage(e));
    } finally {
      setBusy(false);
    }
  }
  return (
    <div className="grid gap-4">
      <p className="text-sm text-muted-foreground">
        {t("新建 Preview 复制这里的变量、Secret 和 R2 绑定；已有 Preview 保持不变。")}
      </p>
      <Field label={t("普通变量 JSON")}>
        <Textarea
          value={variables}
          onChange={(e) => setVariables(e.target.value)}
          rows={6}
        />
      </Field>
      <Field label={t("版本元数据绑定")}>
        <Input
          value={versionMetadata}
          placeholder="CF_VERSION_METADATA"
          onChange={(e) => setVersionMetadata(e.target.value)}
        />
        <p className="text-xs text-muted-foreground">
          {t("将版本 ID、标签和创建时间注入此变量；留空关闭。")}
        </p>
      </Field>
      <div>
        <R2BindingsEditor
          organizationBase={organizationBase}
          rows={r2Bindings}
          onChange={setR2Bindings}
          disabled={busy || !config}
        />
      </div>
      <div>
        <Button
          disabled={busy || !config}
          onClick={() => void save(false)}
        >
          {t("保存预览基础配置")}
        </Button>
      </div>
      <p className="text-sm">
        {t("已有 Secret")}：{config?.secret_names?.join(", ") || "—"}
      </p>
      <Field label={t("Secret 名称")}>
        <Input
          value={name}
          onChange={(e) => setName(e.target.value)}
          autoComplete="off"
        />
      </Field>
      <Field label={t("Secret 值")}>
        <Input
          type="password"
          value={secret}
          onChange={(e) => setSecret(e.target.value)}
          autoComplete="new-password"
        />
      </Field>
      <div>
        <Button
          disabled={busy || !config || !name}
          onClick={() => void save(true)}
        >
          {t("保存预览基础 Secret")}
        </Button>
      </div>
    </div>
  );
}
