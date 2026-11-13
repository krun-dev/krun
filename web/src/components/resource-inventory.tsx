import { useTranslation } from "react-i18next";
import { t } from "@/lib/i18n";
import { useCollectionPage } from "@/lib/collection-page";
import type { Organization } from "@/lib/api";
import { Badge } from "@/components/ui/badge";
import { CollectionPagination, CollectionToolbar } from "./collection-controls";
import { Empty, Loading, PageHeader } from "./common";
import { DataTable, ErrorState } from "./management";

type InventoryResource = {
  id: string;
  name: string;
  organization_id: string;
  organization_name: string;
  enabled?: boolean;
};

export default function ResourceInventoryPage({
  section,
  organizations,
  workspace,
  refresh,
}: {
  section: "workers" | "buckets";
  organizations: Organization[];
  workspace: string;
  refresh: number;
}) {
  useTranslation();
  const page = useCollectionPage<InventoryResource>(
    "/api/v1/system/inventory/" + section,
    true,
    refresh,
  );
  const workers = section === "workers";
  return (
    <>
      <PageHeader
        title={t(workers ? "全平台 Workers" : "全平台 Bucket")}
        description={t(workers ? "查看所有组织的 Worker。" : "查看所有组织的对象存储桶。")}
      />
      <CollectionToolbar
        page={page}
        label={t("搜索名称、ID 或组织")}
      />
      {page.error ? (
        <ErrorState error={page.error} />
      ) : !page.items ? (
        <Loading />
      ) : !page.items.length ? (
        <Empty
          title={t(workers ? "没有匹配的 Worker" : "没有匹配的桶")}
          description={t("请调整搜索条件或返回上一页。")}
        />
      ) : (
        <DataTable
          rows={page.items}
          getKey={(row) => row.id}
          columns={[
            {
              title: t("名称"),
              render: (row) => {
                const org = organizations.find((org) => org.id === row.organization_id);
                const params = new URLSearchParams({
                  view: "system",
                  page: section,
                  inventory_org: row.organization_id,
                  inventory_resource: row.id,
                });
                if (workspace) params.set("workspace", workspace);
                return (
                  <div className="min-w-36">
                    {org ? (
                      <a
                        className="font-medium text-primary underline-offset-4 hover:underline"
                        href={`/?${params}`}
                      >
                        {row.name}
                      </a>
                    ) : (
                      <span className="font-medium">{row.name}</span>
                    )}
                    <div className="break-all font-mono text-xs text-muted-foreground">
                      {row.id}
                    </div>
                  </div>
                );
              },
            },
            {
              title: t("所属组织"),
              render: (row) => <span title={row.organization_id}>{row.organization_name}</span>,
            },
            ...(!workers
              ? [
                  {
                    title: t("状态"),
                    render: (row: InventoryResource) => (
                      <Badge variant="outline">{t(row.enabled ? "已启用" : "已停用")}</Badge>
                    ),
                  },
                ]
              : []),
          ]}
        />
      )}
      <CollectionPagination page={page} />
    </>
  );
}
