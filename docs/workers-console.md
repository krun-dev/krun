# Workers 控制台

Workers 页面属于主 React/Vite 控制台，不是独立应用。日常开发使用 local-first
拓扑：控制面、PostgreSQL、MinIO、local builder 和 Vite 在开发机，集群只保留
runtime（workerd）与 kgateway。

```bash
cp config/development.example.yaml .data/dev.yaml
# 把 config/example.yaml 的 workers: 段合并进去，并填写本机实际值
npm ci
make dev
```

入口是 `http://127.0.0.1:5173/?page=workers`。Vite 只在开发期把同源
`/api/*` 代理到 Go；页面不保存 API Key，也不读取后端地址。配置、隧道和启动细节见
[本地开发环境](local-dev.md)。

## 页面流程

1. 从组织服务列表进入 Worker，选择上传源码或配置好的 GitHub / GitLab 仓库。
2. 查看持久化 Build 状态、编译日志和对象存储 Artifact。
3. 选择成功构建并发布到目标环境；Operation 经认领、验证、提交和 runtime ack
   最终收敛到 available。
4. 用 CAS revision 修改环境变量、服务域名或执行回滚，避免覆盖并发更新；预览环境
   的创建和删除也通过服务行 CAS 仲裁，多个控制面副本不会并发突破数量上限。
5. 从历史中查看 Release，在“调用测试”中经配置好的 kgateway 验证业务响应。

构建与发布队列支持控制面重启和多副本抢占：认领使用条件更新及租约，副作用必须
幂等。Release 提交不保证边缘立即生效，runtime 通过 config revision、snapshot 和
ack 最终收敛。

调用接口只接受相对 URI，目标 Gateway 与 Host 来自平台配置。平台凭据不会转发给
业务；不跟随重定向，响应正文有大小限制并以转义文本显示。

仓库内的 [`examples/workers-fullstack`](../examples/workers-fullstack/README.md) 是完整
验证夹具：同一个 Release 同时包含 Worker API 和纯静态前端，并由 kind E2E 实际构建、
发布和调用，不是只靠 mock 或单元测试证明。

## 完整 kind E2E

`deploy/workers-kind` 是独立的完整验收环境，会在集群内部署 backend、PostgreSQL、
MinIO、builder/runtime 相关组件，并执行故障恢复测试。它不能与 local-first 环境同时
运行。使用方法见 [kind E2E README](../deploy/workers-kind/README.md)。

提交前至少运行：

```bash
make test
npm run build
make check-static-build
```
