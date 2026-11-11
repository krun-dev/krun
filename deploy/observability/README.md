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

保持 Grafana 的端口转发运行。在另一个终端设置同一个 `CONTEXT` 后执行以下命令。
第一个查询检查预置看板标题，第二个查询检查至少发现一个 kubelet，且每个目标的
`up` 都为 `1`。两条命令应返回 `true`。

```bash
GRAFANA_PASSWORD=$(kubectl --context "$CONTEXT" -n krun-observability get secret grafana-admin \
  -o go-template='{{index .data "admin-password" | base64decode}}')

printf 'user = "admin:%s"\n' "$GRAFANA_PASSWORD" |
  curl -fsS --config - http://127.0.0.1:3000/api/dashboards/uid/krun-workers-runtime |
  jq -e '.dashboard.title == "krun Workers Runtime"'

printf 'user = "admin:%s"\n' "$GRAFANA_PASSWORD" |
  curl -fsS --config - -G \
    --data-urlencode 'query=up{job="krun-kubelet-cadvisor"}' \
    http://127.0.0.1:3000/api/datasources/proxy/uid/victoria-metrics/api/v1/query |
  jq -e '.status == "success" and (.data.result | length > 0 and all(.value[1] == "1"))'

unset GRAFANA_PASSWORD
```

有运行中的 workerd Pod 时，看板应显示内存 working set。采集周期为 30 秒，CPU 曲线
需要多个样本。
