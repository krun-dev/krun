import { useState } from "react";
import { useTranslation } from "react-i18next";
import { Trigger as DialogTrigger } from "radix-ui/dialog";
import { Button } from "@/components/ui/button";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { t } from "@/lib/i18n";
import { Loading } from "./common";
import { ErrorState, useData } from "./management";

type PoolManifests = {
  deployment: string;
  config_map: string;
  supporting_resources: string;
};

function ManifestCode({ yaml }: { yaml: string }) {
  const [copyStatus, setCopyStatus] = useState("");
  async function copy() {
    try {
      await navigator.clipboard.writeText(yaml);
      setCopyStatus(t("YAML 已复制"));
    } catch {
      setCopyStatus(t("复制失败，请手动选择 YAML。"));
    }
  }

  return (
    <div className="min-w-0 space-y-2">
      <div className="flex flex-wrap items-center gap-2">
        <Button
          variant="outline"
          size="sm"
          onClick={copy}
        >
          {t("复制 YAML")}
        </Button>
        <span
          aria-live="polite"
          className="text-sm text-muted-foreground"
        >
          {copyStatus}
        </span>
      </div>
      <pre
        tabIndex={0}
        aria-label={t("YAML 内容")}
        className="max-h-[min(50dvh,30rem)] overflow-auto rounded-md border bg-muted/40 p-4 text-xs leading-5 text-foreground focus-visible:outline focus-visible:outline-ring"
      >
        <code>{yaml}</code>
      </pre>
    </div>
  );
}

function ManifestPreview({ poolID }: { poolID: string }) {
  const { data, error, loading } = useData<PoolManifests>(
    `/api/v1/system/config/runtime-pools/${encodeURIComponent(poolID)}/manifests`,
    0,
  );
  if (loading) return <Loading />;
  if (error) return <ErrorState error={error} />;
  if (!data) return null;
  return (
    <Tabs
      defaultValue="deployment"
      className="min-w-0"
    >
      <TabsList aria-label={t("Kubernetes 清单")}>
        <TabsTrigger value="deployment">Deployment</TabsTrigger>
        <TabsTrigger value="config-map">ConfigMap</TabsTrigger>
        <TabsTrigger value="supporting">{t("配套资源")}</TabsTrigger>
      </TabsList>
      <TabsContent value="deployment">
        <ManifestCode yaml={data.deployment} />
      </TabsContent>
      <TabsContent value="config-map">
        <ManifestCode yaml={data.config_map} />
      </TabsContent>
      <TabsContent value="supporting">
        <ManifestCode yaml={data.supporting_resources} />
      </TabsContent>
    </Tabs>
  );
}

export function RuntimePoolManifests({ poolID }: { poolID: string }) {
  useTranslation();
  return (
    <Dialog>
      <DialogTrigger asChild>
        <Button
          variant="outline"
          size="sm"
        >
          {t("查看清单")}
          <span className="sr-only"> {poolID}</span>
        </Button>
      </DialogTrigger>
      <DialogContent className="max-h-[90dvh] min-w-0 overflow-y-auto sm:max-w-3xl">
        <DialogHeader className="pr-8">
          <DialogTitle>{t("资源池清单 · {{pool}}", { pool: poolID })}</DialogTitle>
          <DialogDescription>
            {t("按当前配置生成。Secret 由部署方维护，清单只包含引用。")}
          </DialogDescription>
        </DialogHeader>
        <ManifestPreview poolID={poolID} />
      </DialogContent>
    </Dialog>
  );
}
