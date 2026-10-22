# 后端 kind 实验

本目录只部署后端。使用已有 kind 集群，独立命名空间 `workers-mvp`，不会删除或修改已有 `workers-lab`。需要集群已经安装 kgateway / Gateway API、支持 NetworkPolicy 的 CNI（本次 Calico）和默认 StorageClass。

> ⚠️ 这是完整的独立 kind 验收环境：backend、Postgres、MinIO、runtime 都在集群里。日常 local-first 开发请用 [本地开发环境](../../docs/local-dev.md)：控制面、Postgres、MinIO、builder 在开发机，集群只保留 runtime + kgateway。不要同时运行两套 backend/Postgres/MinIO。

本次机器：`<user>@<kind-host>`。集群 `krun-workers-20261010`，kubeconfig `/home/<user>/kind/kubeconfig`，源代码目录 `/home/<user>/krun`。

## 部署

需要 Docker、kubectl、Python 3、Node.js，以及 Go 1.26。远端缺少 Go 时可在本地先交叉编译，并将源码与二进制一起复制过去：

```sh
mkdir -p deploy/workers-kind/generated
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath \
  -o deploy/workers-kind/generated/krun-server ./cmd/krun-server
```

在 Docker 所在机器的代码根目录执行：

```sh
export KUBECONFIG=/home/<user>/kind/kubeconfig
export KIND_CLUSTER_NAME=krun-workers-20261010
bash deploy/workers-kind/deploy.sh
```

脚本校验 kubeconfig 指向预期 kind context，构建并导入三个镜像，自动读取 Kubernetes API 的 IP 收紧出口策略，生成并应用 manifest，等待部署和 Gateway Ready。镜像导入指定 `linux/amd64`，适配本次 Docker 29 的多架构存储行为。

PostgreSQL 和 MinIO 分别使用 1 GiB PVC。生成的凭证、manifest、二进制位于被 Git 忽略的 `generated/`；凭证重用而非每次重生成。丢失 `credentials.json` 时不要直接重新部署到已有 PVC，需要先恢复凭证或显式迁移。

Builder `imagePullPolicy: Never` 仅用于此 kind 预加载镜像；后端通用配置默认 `IfNotPresent`。重新部署会滚动重启平台 Pod。业务版本发布通过 Loader 完成，不滚动重启 runtime Pod。

## 验收

在远端执行：

```sh
cd deploy/workers-kind
KUBECONFIG=/home/<user>/kind/kubeconfig node e2e.mjs
```

脚本创建独立测试组织、服务和 API Key，真实调用现有登录 / IAM 接口，提交构建 Job、读写 MinIO、发布到两个 workerd 副本，并从 tester Pod 经 kgateway 调用业务 Host。它会重启实验 backend、替换一个 runtime Pod；故障恢复检查通过 kind 节点的 nsenter，临时在 backend Pod 的独立网络命名空间拒绝到达 internal listener 9001 的入站请求（配置增量流与 ack），随后删除同一条规则。需要远端 Docker 权限及 kind 节点中的 crictl / nsenter / iptables。旧实验不参与测试。

证据输出 `evidence/e2e.json`，新鲜测试 Key 只保存在 `generated/api-key.json`（0600）。测试默认从 tester Pod 直接发送 API 请求，不依赖 port-forward。读取请求遇到传输中断最多重试 3 次，另写 `evidence/transport-retries.json`，写操作不自动重试。本次 kind 节点 containerd 2.1.1 在创建 Pod 时多次 SIGSEGV 并被 systemd 重启，曾中断 port-forward / exec；该实验环境问题单独记录。可用 `KRUN_E2E_TRANSPORT=port-forward` 切换测试传输，此模式指定 `KUBECTL_PORT_FORWARD_WEBSOCKETS=false`。具体结果见 [验证报告](../../docs/workers-kind-verification.md)。

## API 访问

kind 开发登录只能经本机回环口访问。远端先运行：

```sh
KUBECTL_PORT_FORWARD_WEBSOCKETS=false kubectl \
  --kubeconfig /home/<user>/kind/kubeconfig \
  -n workers-mvp port-forward deployment/backend 4380:9000 --address=127.0.0.1
```

本机另开 SSH tunnel：

```sh
ssh -N -L 4380:127.0.0.1:4380 <user>@<kind-host>
```

控制 API 位于 `http://127.0.0.1:4380`；本轮没有部署控制台前端。可通过现有鉴权流程或验收 Key 调用 API。完整请求格式见 [后端接口与流程](../../docs/workers-backend.md)。

Runtime 内部服务 API 经后端的独立端口 9001 提供，仅允许 runtime 访问；用户端口 9000 不转发 `/internal/workers/*`。默认环境业务 Host 仅在实验私网入口暴露，尚未设置公网 DNS / TLS。
