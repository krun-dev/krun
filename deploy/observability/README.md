# krun 运行指标

在 krun runtime 所在的 Kubernetes 集群按需安装这套监控。vmagent 从各节点
kubelet 的 `/metrics/cadvisor` 采集 `workerd` 容器的 CPU 和内存指标，并从
serving Pod 的 `:8084/metrics` 采集业务请求及制品加载指标。数据写入单节点
VictoriaMetrics。Grafana 预置数据源和 **krun Workers Runtime** 看板。
VictoriaMetrics 单节点版同时提供写入、存储和查询，集群版的 `vmstorage` 则需要搭配
`vminsert` 和 `vmselect`。

看板按 namespace 和资源池展示业务 QPS、5xx 比例、响应延迟及冷加载的成功率趋势和耗时，
并按 Pod 展示 CPU、内存及采集状态。业务指标只统计转发给业务 Worker 的 HTTP 调用，
延迟量到拿到响应，包含调用期间发生的冷加载。冷加载指标量到制品下载、校验和准备完成。
容器 CPU 和内存覆盖 serving runtime 和 validator，多个 Worker 共用该容器的资源。

**krun Worker Metrics** 看板按组织和 Worker 查询请求数、HTTP 5xx 比例以及 P50、P95、P99
响应延迟。环境和版本均可多选，每组环境和版本分别画线。版本取实际执行请求的版本。数据从
升级 serving runtime 后开始积累，制品或域名变化不影响 Worker ID。资源池看板仍使用原有聚合指标。

选择环境、比较版本和解读面板的步骤见 [Workers 观测](../../user-docs/src/content/docs/guides/workers-observability.md)。

## 配置收敛与业务入口

**Runtime configuration convergence** 按 serving Pod 显示本地配置是否在有效期内、是否应用了
配置流最近报告的 head，以及当前 revision 的 ACK 是否在最近 10 秒内被控制面接受。
`krun_runtime_config_revision_lag` 是已应用 revision 与配置流最近报告的 head 的差值。
`krun_runtime_config_fresh` 和 `krun_runtime_config_sync_age_seconds` 用于定位配置断连。
`krun_runtime_config_ack_current` 表示当前已应用 revision 的 ACK 是否仍有效。
发布操作的 `available` 记录该次发布的 Runtime 确认。

**Worker ingress probes** 每 30 秒通过配置的业务 HTTPS URL 请求专用 canary Worker。
探测验证 DNS 和 TLS、HTTP 200、`X-Krun-Version` 及响应正文 `krun-ingress-ok`。
出口视角取决于 vmagent 所在网络。为内网、办公网和公网分别验证时，在对应客户端网络运行采集器，
使用该网络用户实际访问的域名。目标用 HTTPS URL 配置，保留标准证书验证。
[Blackbox Exporter 探测配置](https://github.com/prometheus/blackbox_exporter/blob/v0.28.0/CONFIGURATION.md)

Grafana 预置两条独立的告警。已发现的 Runtime Pod 持续 2 分钟未收敛或采集失败时告警。
已配置的入口探测持续 1 分钟失败或采集失败时告警。若 vmagent 整体停采，面板会缺少新样本。
采集链路存活和数据新鲜度由部署侧监控。外部通知使用部署方配置的
[Grafana 联系点与通知策略](https://grafana.com/docs/grafana/latest/alerting/configure-notifications/)。

### 配置业务 HTTPS 探测

准备一个专用 Worker，选择代表待测客户端网络的网络配置和域名。需要已配置并有该 Worker 发布权限的
krun CLI 会话。把 `ORG_ID`、`WORKER_ID` 和 `WORKER_INGRESS_URL` 换成目标组织、Worker ID
和已绑定域名下的 HTTPS 路径。将 Worker 的响应声明为 `no-store`，使探测经过实际入口。

```sh
ORG_ID='replace-with-organization-id'
WORKER_ID='replace-with-worker-id'
WORKER_INGRESS_URL='https://canary.workers.example.com/health'
CANARY_DIR="$(mktemp -d)"
cat >"$CANARY_DIR/wrangler.json" <<'JSON'
{"name":"ingress-canary","main":"index.js","compatibility_date":"2026-09-28"}
JSON
cat >"$CANARY_DIR/index.js" <<'JS'
export default {
  fetch(request) {
    if (new URL(request.url).pathname !== "/health") {
      return new Response("not found", { status: 404 });
    }
    return new Response("krun-ingress-ok", {
      headers: { "Cache-Control": "no-store" },
    });
  },
};
JS
krun deploy --org "$ORG_ID" --worker "$WORKER_ID" --env production --path "$CANARY_DIR"
curl -fsS -D - "$WORKER_INGRESS_URL"
```

预期是 HTTP 200、响应头包含 `X-Krun-Version`、正文为 `krun-ingress-ok`。将该 URL 加入
监控部署的 Kustomize overlay。以下命令从仓库根目录执行，需要 kubectl，并沿用上一步的
`WORKER_INGRESS_URL`。将 `CONTEXT` 换成监控所在集群的 context。

```sh
CONTEXT='replace-context'
OVERLAY="$(mktemp -d deploy/.observability-site-XXXXXX)"
cat >"$OVERLAY/kustomization.yaml" <<'YAML'
apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
namespace: krun-observability
resources:
  - ../observability
configMapGenerator:
  - name: vmagent-scrape
    behavior: merge
    files:
      - ingress-targets.yaml
YAML
printf '%s\n' '- targets:' "    - '$WORKER_INGRESS_URL'" >"$OVERLAY/ingress-targets.yaml"
kubectl --context "$CONTEXT" apply -k "$OVERLAY"
kubectl --context "$CONTEXT" -n krun-observability rollout status deployment/vmagent --timeout=180s
```

在部署侧保存目标列表和 overlay，用于后续更新。目标 URL 会出现在指标标签中，使用无凭据的专用路径。
默认目标列表为空，部署方填入目标后才开始探测。探测器只在 vmagent Pod 的 loopback 上监听。

## 在控制台嵌入 Worker 指标

在 krun 主配置启用以下设置，并滚动重启控制面以加载配置。将 `https://grafana.example.com`
替换为浏览器可访问的 Grafana 地址，支持子路径。`dashboard_uid` 可省略，默认使用预置看板
`krun-worker-metrics`。

```yaml
worker_metrics:
  grafana_url: https://grafana.example.com
  dashboard_uid: krun-worker-metrics
```

在 Grafana 的网关响应 CSP 中配置 `frame-ancestors https://console.example.com`，将示例来源
替换为控制台的实际 origin。控制面托管静态文件时从配置生成 `frame-src`。通过 OSS 或独立
代理托管控制台时，在静态站点响应的 CSP 中加入 Grafana origin，例如
`frame-src 'self' https://grafana.example.com`。

在部署配置中设置 `GF_SERVER_ROOT_URL` 和 `GF_SECURITY_ALLOW_EMBEDDING=true`。如果已安装
Grafana，可以用 `kubectl` 更新当前 Deployment。将 `CONTEXT` 和 `GRAFANA_URL` 替换为
实际的 kubectl context 和 Grafana 公开地址，并把同样的环境变量保存到部署 overlay，供后续重部署使用。

```bash
CONTEXT='replace-context'
GRAFANA_URL='https://grafana.example.com'
kubectl --context "$CONTEXT" -n krun-observability set env deployment/grafana \
  "GF_SERVER_ROOT_URL=${GRAFANA_URL%/}/" GF_SECURITY_ALLOW_EMBEDDING=true
kubectl --context "$CONTEXT" -n krun-observability rollout status deployment/grafana --timeout=180s
```

更换 Grafana 地址时同步更新控制面、Grafana 和控制台静态站点的 CSP 配置，控制台产物无需重新构建。

嵌入使用 Grafana 自己的登录和数据源授权。可以通过「在 Grafana 中打开」完成顶层登录后再
回到控制台；使用同站点域名可让 iframe 使用已有的 Grafana 会话。看板 URL 中的 Worker
是展示筛选条件。内置 VictoriaMetrics 数据源可查看整个采集范围，只向有权查看该范围的人
授予 Grafana 数据源访问权限。按组织或 Worker 开放时，需为 Grafana 配置相应隔离的数据源。

## 安装

- 本地需要 `kubectl` 和 `openssl`。终端验证还需要 `curl` 和 `jq`。
- 准备有默认 StorageClass 的集群。VictoriaMetrics 使用 10 GiB PVC 并保留 14 天数据，
  Grafana 使用 1 GiB PVC。vmagent 的重试队列使用临时卷，随 Pod 重建清空。
- vmagent 需要连到 Kubernetes API 和节点 kubelet HTTPS 端口。kubelet 需启用
  ServiceAccount bearer token 的 Webhook 认证和 Webhook 授权。清单使用
  ServiceAccount 的 CA 验证 kubelet 证书，节点地址必须匹配证书 SAN。
  如果 kubelet 使用独立 CA，请在自己的 Kustomize overlay 中挂载该 CA，并替换
  `scrape.yaml` 中的 `tls_config.ca_file`。
- 业务指标需要同时更新 serving workerd 镜像和 runtime 配置。部署清单声明名为
  `metrics` 的 8084 端口，并允许 `krun-observability` 中的 vmagent Pod 访问该端口。

在仓库根目录执行。将 `<context>` 换成目标集群的 kubectl context。只在首次安装时创建
Grafana 管理员 Secret。

```bash
CONTEXT='<context>'
kubectl --context "$CONTEXT" apply -f deploy/observability/namespace.yaml
openssl rand -hex 24 | tr -d '\n' |
  kubectl --context "$CONTEXT" -n krun-observability create secret generic grafana-admin \
    --from-file=admin-password=/dev/stdin
kubectl --context "$CONTEXT" apply -k deploy/observability
kubectl --context "$CONTEXT" -n krun-observability rollout status deployment --timeout=180s
```

Grafana 默认使用 ClusterIP。通过下面的命令转发到本机，打开
[krun Workers Runtime 看板](http://127.0.0.1:3000/d/krun-workers-runtime)，
以 `admin` 登录。首次初始化时 Grafana 从 `grafana-admin` 读取密码，并将账号写入
PVC 中的数据库。可以在另一个终端读取初始密码。

```bash
kubectl --context "$CONTEXT" -n krun-observability port-forward service/grafana 3000:3000
```

```bash
kubectl --context "$CONTEXT" -n krun-observability get secret grafana-admin \
  -o go-template='{{index .data "admin-password" | base64decode}}{{"\n"}}'
```

| 现象 | 排查 |
| --- | --- |
| kubelet target 为 `0`，vmagent 日志有 `x509: certificate signed by unknown authority` 或证书 SAN 不匹配 | 检查 kubelet serving 证书、签发 CA 和节点地址。kubeadm / kind 默认使用自签证书时，配置受信任的 [kubelet serving 证书](https://kubernetes.io/docs/tasks/administer-cluster/kubeadm/kubeadm-certs/#enabling-signed-kubelet-serving-certificates)。 |
| vmagent 日志出现 kubelet `401` 或 `403` | 检查 kubelet Webhook 认证与授权。确认 ClusterRoleBinding 生效且 `kubectl --context "$CONTEXT" auth can-i get nodes --subresource=metrics --as=system:serviceaccount:krun-observability:vmagent` 返回 `yes`。 |
| kubelet target 为 `1`，看板没有容器曲线 | 确认目标 Pod 正在运行，容器名是 `workerd`，并查看看板的 Namespace、Pod 筛选项。 |
| Runtime scrape health 为空或为 `0` | 确认 serving Pod 标签为 `role=krun-runtime`，容器已声明 `metrics:8084`，并检查 NetworkPolicy 是否允许 vmagent 访问。 |
| PVC 一直 `Pending` | 检查默认 StorageClass 和集群存储供给。 |

卸载用 `kubectl --context "$CONTEXT" delete -k deploy/observability`。这会删除监控
namespace 和 PVC，底层卷是否保留取决于 StorageClass 的回收策略。

## 验证

保持 Grafana 的端口转发运行。需要 `kubectl`、`curl` 和 `jq`。在另一个终端将
`CONTEXT` 替换为安装时使用的 kubectl context。以下两条检查应返回 `true`，分别确认
预置的资源池看板和 Worker 详情看板。

```bash
CONTEXT='replace-context'
GRAFANA_PASSWORD=$(kubectl --context "$CONTEXT" -n krun-observability get secret grafana-admin \
  -o go-template='{{index .data "admin-password" | base64decode}}')

printf 'user = "admin:%s"\n' "$GRAFANA_PASSWORD" |
  curl -fsS --config - http://127.0.0.1:3000/api/dashboards/uid/krun-workers-runtime |
  jq -e '.dashboard.title == "krun Workers Runtime"'

printf 'user = "admin:%s"\n' "$GRAFANA_PASSWORD" |
  curl -fsS --config - http://127.0.0.1:3000/api/dashboards/uid/krun-worker-metrics |
  jq -e '.dashboard.title == "krun Worker Metrics"'

unset GRAFANA_PASSWORD
```

打开 **krun Workers Runtime** 看板，确认 kubelet 和 serving runtime 的采集状态为 `1`。
有运行中的 workerd Pod 时，内存面板应显示 working set。采集周期为 30 秒，CPU 曲线需要多个样本。

在浏览器访问目标 Worker 已发布的 HTTP 路径，选择实际执行 Worker 的路径。等待两个采集周期后，
在 Worker 详情打开「指标」，选择对应环境和时段。确认看板出现 `Requests`、`HTTP 5xx ratio`
和 `Response latency` 三个面板，图例包含实际处理请求的版本 ID。如果显示 `No data`，检查
「Runtime scrape health」是否为 `1`，再核对 serving runtime 镜像与配置，以及选中的环境和时间范围。

直开 Grafana 成功而嵌入区域显示拒绝连接时，检查 Grafana 响应的 `X-Frame-Options` 和
`frame-ancestors`，以及控制台响应的 `frame-src`。

已配置业务探测时，从业务网络执行以下检查。第一条应返回 HTTP 200、`X-Krun-Version` 和
`krun-ingress-ok`。第二条在两个采集周期后应输出 `1`。另开终端运行
`kubectl --context "$CONTEXT" -n krun-observability port-forward service/victoria-metrics 8428:8428`。

```sh
curl -fsS -D - "$WORKER_INGRESS_URL"
curl -fsSG http://127.0.0.1:8428/api/v1/query \
  --data-urlencode "query=probe_success{job=\"krun-worker-ingress\",instance=\"$WORKER_INGRESS_URL\"}" |
  jq -er '.data.result[0].value[1] | select(. == "1")'
```

| 指标现象 | 排查 |
| --- | --- |
| 配置收敛为 `0`，入口探测为 `1` | 检查 `krun_runtime_config_fresh`、`krun_runtime_config_revision_lag` 和 `krun_runtime_config_ack_current`。热路由可能仍处于短时断连窗口。 |
| 配置收敛为 `1`，入口探测为 `0` | 检查 `probe_http_status_code`。值为 `0` 时查探测侧 DNS、TLS 和网络，HTTP 404 查业务域名与 Gateway 路由，HTTP 503 查 Gateway 后端和 Runtime。HTTP 200 时核对正文、响应头和 CDN 缓存。 |
| 入口探测为空 | 检查 overlay 是否为 vmagent 配置了目标，并查询 `up{job="krun-worker-ingress"}`。有目标且为 `0` 时查同 Pod 的 blackbox-exporter 容器。 |
