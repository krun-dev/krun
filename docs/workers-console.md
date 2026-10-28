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
5. 在环境配置中新增、轮换或删除 Secret。值只在提交时出现，之后只能看到名称；每次
   修改都会复用当前 Artifact 自动发布一个新 Release，不重新构建代码。
6. 从历史中查看 Release，在“调用测试”中经配置好的 kgateway 验证业务响应。

构建与发布队列支持控制面重启和多副本抢占：认领使用条件更新及租约，副作用必须
幂等。Release 提交不保证边缘立即生效，runtime 通过 config revision、snapshot 和
ack 最终收敛。

调用接口只接受相对 URI，目标 Gateway 与 Host 来自平台配置。平台凭据不会转发给
业务；不跟随重定向，响应正文有大小限制并以转义文本显示。

## 规划中的平台资源池管理

多资源池落地后，资源池调整只出现在 system scope 的“Worker 资源池”管理页，完整
方案见 [Workers 资源池与网络策略设计](workers-pools.md)。组织侧 Workers 页面没有
资源池选择或迁移入口；最多只读显示平台管理的网络能力摘要。

平台页面向 `system_admin`，可以创建 pool，并直接组合 private/public、入口域名和
binding 权限；平台不预先提供权限套餐。策略修改生成不可变 policy revision，经
activate 触发该 pool 唯一 Kubernetes Deployment 的整体 rollout，池内全部服务一起
生效。页面必须展示全池影响范围、健康状态、服务当前分配、preflight、operation
进度和审计记录。

主 YAML 只定义 runtime template、可引用的 CONNECT proxy 和域名等基础设施后端；
页面不能修改 proxy 实际地址、runtime selector、NetworkPolicy YAML、token 或
ServiceAccount。“私网直连 + 代理公网”等高风险组合的正式审批留待后续；当前只允许
系统管理员从平台后台直接操作。

pool 的 id、名称、描述和 tags 都是普通展示元数据，不要求反映能力。页面必须把这些
元数据与有效 policy revision 分区显示；筛选、审批、风险提示和重分配 preflight 不能从
名称、颜色、前后缀或 tags 推断网络能力。

安全 labels 使用独立控件：pool policy labels 与 domain labels 都是 `key=value`，
pool 的 domain selector 和 domain 的 pool selector 必须双向匹配。页面展示当前解析
出的明确域名快照和 selector drift；新增或修改域名标签不会自动扩权，必须创建并
activate 新 pool policy revision。需要让部分服务保留旧策略时，必须新建另一个 pool，
也就是另一套 Deployment，再迁移这些服务。

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
