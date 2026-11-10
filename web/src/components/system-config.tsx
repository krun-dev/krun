import { useTranslation } from "react-i18next";
import { t } from "@/lib/i18n";
import { Badge } from "@/components/ui/badge";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Empty, Loading, PageHeader } from "./common";
import { DataTable, ErrorState, useData } from "./management";

type RuntimePool = {
  id: string;
  default: boolean;
  access: string[];
  private_egress: string;
  public_egress: string;
  validator_configured: boolean;
  network_profiles: string[];
};

type SystemConfig = {
  workers: {
    runtime_pools: RuntimePool[];
    validator_pool?: string;
    runtime_api_override: boolean;
    builder_mode: string;
    preview_limit: number;
  } | null;
  deployment?: { runtime_replicas: number; validator_replicas: number };
};

const accessLabels: Record<string, string> = { public: "公网", private: "内网" };
const egressLabels: Record<string, string> = {
  deny: "禁止",
  direct: "直连",
  "connect-proxy": "CONNECT 代理",
};

function WorkersConfig({
  workers,
  deployment,
}: {
  workers: NonNullable<SystemConfig["workers"]>;
  deployment: SystemConfig["deployment"];
}) {
  const summary = [
    {
      label: "候选校验方式",
      value: workers.validator_pool
        ? t("独立校验池 {{pool}}", { pool: workers.validator_pool })
        : t("使用业务池的运行实例"),
    },
    {
      label: "Runtime API 连接",
      value: workers.runtime_api_override ? t("自定义地址（已隐藏）") : t("集群内部 Service"),
    },
    { label: "构建执行方式", value: workers.builder_mode },
    { label: "每个 Worker 的预览环境上限", value: workers.preview_limit },
  ];
  if (deployment) {
    summary.push({ label: "每池声明的运行副本数", value: deployment.runtime_replicas });
    if (workers.validator_pool) {
      summary.push({ label: "声明的独立校验副本数", value: deployment.validator_replicas });
    }
  }
  return (
    <>
      <Card className="mb-8 shadow-none">
        <CardHeader>
          <CardTitle className="text-base">{t("Workers 运行配置")}</CardTitle>
        </CardHeader>
        <CardContent>
          <dl className="grid gap-x-6 gap-y-4 sm:grid-cols-2 lg:grid-cols-3">
            {summary.map(({ label, value }) => (
              <div key={label}>
                <dt className="text-xs text-muted-foreground">{t(label)}</dt>
                <dd className="mt-1 break-words text-sm font-medium">{value}</dd>
              </div>
            ))}
          </dl>
        </CardContent>
      </Card>
      <section aria-labelledby="runtime-pools-heading">
        <h2
          id="runtime-pools-heading"
          className="mb-3 text-base font-medium"
        >
          {t("运行资源池")}
        </h2>
        <DataTable
          rows={workers.runtime_pools}
          getKey={(pool) => pool.id}
          columns={[
            {
              title: t("资源池 ID"),
              render: (pool) => (
                <div className="flex flex-wrap items-center gap-2">
                  <code>{pool.id}</code>
                  {pool.default && <Badge variant="secondary">{t("默认池")}</Badge>}
                </div>
              ),
            },
            {
              title: t("入口能力"),
              render: (pool) =>
                pool.access.map((value) => t(accessLabels[value] ?? value)).join(" / "),
            },
            {
              title: t("公网出口"),
              render: (pool) => t(egressLabels[pool.public_egress] ?? pool.public_egress),
            },
            {
              title: t("内网出口"),
              render: (pool) => t(egressLabels[pool.private_egress] ?? pool.private_egress),
            },
            {
              title: t("独立校验服务"),
              render: (pool) =>
                pool.id === workers.validator_pool ? (
                  <Badge variant="secondary">{t("正在使用")}</Badge>
                ) : (
                  t(pool.validator_configured ? "已配置，未选用" : "未配置")
                ),
            },
            {
              title: t("匹配的网络档案"),
              render: (pool) =>
                pool.network_profiles.length ? (
                  <div className="flex flex-wrap gap-1">
                    {pool.network_profiles.map((id) => (
                      <Badge
                        key={id}
                        variant="outline"
                        className="font-mono"
                      >
                        {id}
                      </Badge>
                    ))}
                  </div>
                ) : (
                  <span className="text-muted-foreground">{t("无匹配档案")}</span>
                ),
            },
          ]}
        />
      </section>
    </>
  );
}

export default function SystemConfigPage({ refresh }: { refresh: number }) {
  useTranslation();
  const { data, error, loading } = useData<SystemConfig>("/api/v1/system/config", refresh);
  return (
    <>
      <PageHeader
        eyebrow={t("平台管理")}
        title={t("系统配置")}
        description={t("查看当前控制面加载的配置。凭据、内部地址、服务名和标签原文已隐藏。")}
        action={<Badge variant="outline">{t("只读")}</Badge>}
      />
      {loading ? (
        <Loading />
      ) : error ? (
        <ErrorState error={error} />
      ) : data?.workers ? (
        <WorkersConfig
          workers={data.workers}
          deployment={data.deployment}
        />
      ) : (
        <Empty
          title={t("Workers 未配置")}
          description={t("当前控制面没有加载 Workers 运行配置。")}
        />
      )}
    </>
  );
}
