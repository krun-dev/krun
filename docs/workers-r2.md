# Workers R2 binding

支持标准 Cloudflare `r2_bucket` 上传 metadata。Wrangler 项目沿用：

```json
{
  "r2_buckets": [
    { "binding": "IMAGES", "bucket_name": "product-images" }
  ]
}
```

业务使用 `env.IMAGES`；不自动注入名为 `BUCKET` 的 binding。
模块顶层 `import { env } from "cloudflare:workers"`、普通 fetch handler 的 env 和
`WorkerEntrypoint.this.env` 都能访问封装后的对象。业务 isolate 没有默认出网权限，
也拿不到 SecretAccessKey。native Fetcher 只能连接部署配置中的存储目标。

## 创建逻辑桶与绑定

主配置启用 `workers.object_storage`，包括独立的 32 字节 base64url `cursor_key`。
配置示例和 STS 要求见 [凭据模块](storage-credentials.md)。
所有实例使用相同 cursor key；轮换该 key 会使尚未用完的列表游标失效。

逻辑 bucket 的管理接口：

| 接口 | 权限与行为 |
| --- | --- |
| `GET /api/v1/orgs/{org}/workers/buckets` | `organization.read`，列出该组织的逻辑桶 |
| `GET /api/v1/orgs/{org}/workers/buckets/{id}` | `organization.read`，读取本组织桶详情（含停用桶） |
| `POST /api/v1/orgs/{org}/workers/buckets` | `storage.bucket.create`，body 为 `{"name":"product-images"}` |
| `PATCH /api/v1/orgs/{org}/workers/buckets/{id}` | `storage.bucket.update`，以 `{"enabled":false,"revision":1}` 禁用 |

列表传入 `q`、`limit` 或 `after` 时启用控制台分页：`q` 按名称或 ID 不区分大小写
包含搜索，`limit` 为 1–200（默认 100），`after` 使用上次响应的 `next_after`，没有
`next_after` 表示已到末页。游标按桶 ID 推进。未传这三个参数时仍返回按名称排序的完整
组织桶列表，供绑定选择器使用。列表和单桶响应均包含 `created_at`；CF 兼容 R2 接口
继续返回 `result.buckets` 与 `creation_date`，不受控制台分页影响。

创建逻辑桶不创建物理桶；物理桶必须已由部署侧创建并授权。每组织最多 1000 个逻辑桶。
同一组织内的名字唯一，物理路径使用稳定 bucket ID：

```text
<root_prefix>/org/<organization_id>/buckets/<bucket_id>/<logical_key>
```

同组织内绑定同一个桶的服务和环境共享数据。不同组织的同名桶隔离。部署、回滚、
解绑和删除 Worker 不删除存储对象，也不重建桶。此接口不提供数据 purge 或桶删除。

可以通过 [Cloudflare 适配入口](cloudflare-api.md) 使用 Wrangler 创建桶、查询桶、
上传标准 R2 binding、读取 settings、上传 Version 和部署。原生环境 PATCH 也支持
`r2_buckets: {"IMAGES":"product-images"}`，与 `expected_revision` 一起提交，随后发布。
Preview Base 的相同字段会复制到新 Preview；需要隔离时明确使用不同的逻辑桶。

## 控制台操作

组织侧导航的「对象存储」对应 `/?page=buckets&workspace=<organization_id>`，提供逻辑桶
列表、按名称或 ID 搜索、分页、创建、启用和停用；列表与桶详情均展示创建时间。
启停带 revision 并发校验；出现冲突后刷新当前状态，不覆盖其他管理员的修改。创建和
启停分别需要 `storage.bucket.create`、`storage.bucket.update`，列表需要 `organization.read`，
最终权限由后端校验。点击已启用的桶可浏览对象、上传下载、查看详情和删除文件；对象权限
独立，见 [文件与分发](bucket-objects.md)。不提供桶删除或批量数据清空。

在 Worker 的「环境配置 → R2 存储绑定」添加绑定名称并选择本组织已启用的桶。
保存修改 desired 配置，下一次发布才进入不可变 Version；下方同时显示当前 Version
实际使用的桶名称。可以刷新桶列表、移除绑定和加载最新配置；绑定名称与变量、Secret、
版本元数据冲突时会报错。保存被其他请求更新过的环境配置需要先加载最新 revision。

「预览基础配置」也支持相同的 R2 绑定编辑器，新建 Preview 继承此配置。
配置和桶名称符合 Cloudflare R2 binding 模型；控制台使用原生管理接口获取启停状态，
Cloudflare 兼容接口访问同一逻辑桶目录。管理响应使用逻辑桶；文件直传链接短期暴露物理
桶与 prefix，浏览器不接收 SecretAccessKey。需要隐藏底层地址时使用自有 CDN 域名。

页面为 Vite 静态产物，API 都使用同源 `/api/*`。生产部署仍需要 OSS 前的同源路径转发，
符合 [部署约束](local-dev.md)，不依赖运行期 Node。

浏览器管理流程验收：

```sh
npm run build
KRUN_UI_ORIGIN=https://console.example.com npx playwright test tests/ui/r2.spec.mjs
```

验收环境需要启用开发登录、具有组织创建权限和 `workers.object_storage` 配置，并使用
匹配当前 schema 的专用测试库。测试创建自己的组织、服务和桶；不清空既有组织的数据。
本次在独立随机 PostgreSQL schema 上，以同源 Go 服务托管真实静态构建产物完成验收，
没有 mock 浏览器 API。覆盖创建/重名错误、绑定保存与刷新、名称冲突、UI 新建 Preview
继承、停用与重新启用、并发冲突、解绑、跨组织隔离、中英文和手机布局。
这项验收覆盖浏览器到 PostgreSQL 的管理链路，不包含构建发布、Kubernetes rollout 或
浏览器上传对象到真实 TOS 的完整连续流程；runtime/TOS 数据链路由下述独立验收覆盖。

## 已实现的 runtime API

| API / 类型 | 当前行为 |
| --- | --- |
| `head(key)` | 元数据或不存在时的 `null`；403 保留为异常 |
| `get(key, options)` | `R2ObjectBody`、条件不满足时无 body 的元数据、或不存在时 `null` |
| `put(key, value, options)` | string、ArrayBuffer、view、Blob、null、ReadableStream；写后 HEAD 确认本次 version |
| `delete(key / keys[])` | 最多 1000 项，每批最多 8 个请求；部分失败抛错，不承诺原子性 |
| `list(options)` | prefix、`/` delimiter、limit、include、认证的 opaque cursor；每页最多 64 项 |
| 对象属性 | key、持久化随机 version、实际 size、etag/httpEtag、后端 Last-Modified 转 Date、元数据、Standard 存储类型 |
| body 方法 | ReadableStream、bodyUsed、arrayBuffer、text、json、blob、writeHttpMetadata |
| Range | offset/length、suffix，或单个 HTTP bytes range |
| 条件 GET | 单个 ETag 条件，或秒精度日期条件；支持条件 Headers |
| 条件 PUT | If-Match，或 If-None-Match `*`；412 返回 `null`，409 等保留为错误 |
| multipart | createMultipartUpload、同步 resumeMultipartUpload、uploadPart、complete、abort |

读取内容是原始存储字节。内部 GET 用签名的 `response-content-encoding=identity` 防止
HTTP 自动解压；原始 contentEncoding 保留在 R2 httpMetadata 中。

PUT 不缓存整个流，不自动改为 multipart。固定长度的入站请求流已在 workerd 验证直传，
存储端会在上传结束前收到数据。任意自建未知长度流仍受后端 HTTP/S3 的限制，不承诺所有
输入形态都可使用。响应流保持背压；显式取消或被 runtime 观察到的客户端断开会取消上游请求。
网络代理和 TCP 半关闭可能延迟断开信号，操作另受主配置 `request_timeout_seconds`
约束（默认 600 秒，控制响应为 30 秒）。不承诺断开瞬间停止已在途传输。

对象元数据使用原子写入的保留 header `x-amz-meta-krun-v1`，保存 version、customMetadata
和 HTTP 字段的存在性。保留 envelope 最多 1800 个 base64 字符，JSON 最多 1350 字符，
customMetadata 最多 64 个字段；UTF-8 后仍受 envelope 大小限制。
超过预算会在写入前失败。自定义字段的 Unicode 和大小写保留。
`uploaded` 的时间精度来自实际后端，TOS 为秒级，不使用本地时间伪造。

LIST 为获得持久化 version 会额外 HEAD 每项，并按 include 决定是否返回 metadata。
一页最多 8 个并发 HEAD，控制响应上限 1 MiB。游标绑定 bucket ID、prefix、delimiter、
include，带 HMAC 和一小时有效期。对象并发删除时该项可能不出现在最终页中。

## Multipart 上传

创建时保存 HTTP/custom metadata 和随机 version。分片数据直接通过固定目标 Fetcher
上传到 S3/TOS；只有签名参数经过 runtime 内控制调用。

```js
const upload = await env.BUCKET.createMultipartUpload("video.mp4", {
  httpMetadata: { contentType: "video/mp4" },
});
// 将 key 和 uploadId 返回给客户端，后续请求可以在其他 runtime Pod 续传。
const resumed = env.BUCKET.resumeMultipartUpload(upload.key, upload.uploadId);
const part = await resumed.uploadPart(1, request.body);
const object = await resumed.complete([part]);
```

partNumber 为 1–10000，complete 接收按 partNumber 严格升序排列的 ETag 清单。
完成前通过后端 ListParts 分页核对 ETag 和大小，非最后一片必须等大且至少 5 MiB，
最后一片可以更小，各片最大 5 GiB。单片上传也可直接完成。分片大小和上传状态由后端
保存，不依赖 isolate 内存、数据库行或发起创建的请求。合并检查 HTTP 状态和 XML
错误内容，防止把 S3 的 HTTP 200 错误当成功；随后 HEAD 核对 version 与最终 ETag。
abort 可以重复调用；已完成或已取消的上传在后端返回 404 时视为取消成功。

uploadId 是平台签名的 opaque token，携带后端上传 ID、逻辑 key、bucket/store、version
和七天有效期；跨桶、跨 key、篡改或过期会拒绝。它不能直接传给原生 S3 multipart API，
也不支持拿原生 S3 uploadId 直接 resume。所有 Pod 使用同一 `cursor_key`，轮换它会同时
使旧分页游标和未完成上传的 uploadId 失效。STS 可以在续传请求之间重新获取。

部署侧需要为物理桶配置七天清理未完成分片的生命周期规则。平台不会自动改写既有桶的
lifecycle，也不承诺 token 到期就自动清除后端分片；没有配置规则时需要部署侧清理。

## Cache API

提供 `caches.default`、`caches.open(name)` 和 cache 的 `put/match/delete`。
当前固定 workerd 版本的 WorkerLoader 尚未暴露 cacheApiOutbound，因此由可信父 Worker
提供有界本地缓存，业务预加载模块安装兼容 facade，无需修改业务的 compatibility flags。

缓存按组织、服务、环境和 cache name 隔离，同环境不同版本共享；validator 使用独立范围。
URL 包括查询参数，不包括 fragment。内容只在当前 runtime 进程内有效，重启或调度到其他
Pod 可能 miss；没有跨 Pod 复制或全局 purge。缓存数据与控制调用都不经过 krun-server。

当前支持：

- Cache-Control 的 s-maxage/max-age、Expires、Age 和常见状态码默认 TTL。
- no-store/no-cache/private 与 Set-Cookie 不存储；支持 private=Set-Cookie 去除 cookie 后缓存。
- Vary 变体、ETag/Last-Modified 条件读取、带 Content-Length 的单段 Range。
- match/delete 的 ignoreMethod；delete 清理该 URL 的所有变体。

put 的非 GET、206 响应和 Vary `*` 会报错。不支持 ignoreSearch、ignoreVary、SWR、
stale-if-error、标签全局清理或 Cloudflare CDN/Tiered Cache。Cache-Tag 在读取结果中移除。
默认 TTL 与语义参考 [Cloudflare Cache API](https://developers.cloudflare.com/workers/runtime-apis/cache/)
和 [默认状态码 TTL](https://developers.cloudflare.com/cache/how-to/configure-cache-status-code/)。

进程共享缓存预算为 16 MiB、最多 1024 项，单个 body 最多 4 MiB，最多两个并行写入，
控制 metadata 上限 24 KiB，读写控制调用超时 30 秒。超出容量、写入并发预算或缓存策略
不允许时，put 可以成功返回但不存储；业务必须允许 miss。缓存只保存完整字节和普通
metadata，不跨请求保留 Request、Response、reader 或进行中的 Promise。

## 当前不支持的能力

本实现是常用 API 子集，不是完整 R2 替代品。以下调用会明确报错：SSE-C、
PUT checksum 参数、InfrequentAccess、组合条件、任意 ETag-not-equals 写、日期条件写、
非空且非 `/` 的 delimiter、startAfter，以及点段或反斜线 key。物理 key 总长不得超过 1024 UTF-8
字节，因此逻辑 key 的可用长度还需减去平台前缀。

`checksums.toJSON()` 可用，但当前未提供默认 MD5，也不将任意 32 位 ETag 假装成 MD5。
依赖 `checksums.md5` 或上传 checksum 校验的业务需要调整，不能按完全兼容处理。
LIST 遵循旧 compatibility date 与 `r2_list_honor_include` 的默认 metadata 行为。

直接通过原生 S3 API 写入、缺少平台 envelope 的已有对象会返回元数据错误；此版本没有
自动迁移或回填工具。仓库构建当前不从源码的 `r2_buckets` 自动修改环境 desired bindings，
使用 Wrangler 协议部署，或先通过环境 PATCH 配置 bindings。没有下载 URL 扩展和自动 302；
`get()` 始终保持返回对象/流的语义。

## 凭据与撤销

可信父 Worker 通过 bound props 固定 Version、binding 和 bucket ID。内部签发接口只接收
这两个资源标识，服务端按当前部署、组织、pool、桶状态及授权 revision 解析范围。
普通对象请求在父 Worker 本地签名，不逐次访问 krun-server。签名请求最长有效 60 秒。

runtime 只缓存完成的普通凭据数据，授权租约为 5 分钟；STS 剩余有效期不足 2 分钟时重新
获取。缓存有 1024 项上限。配置 feed 超过 60 秒没有成功同步即停止新签名。
禁用桶立即阻止服务端的新签发；runtime 已缓存的租约最多还能签名约 5 分钟。
已发 STS、签名或在途数据请求不受本地删缓存的即时撤销保证。

候选校验使用独立的 loader 和缓存命名空间，即使运行在 serving Pod 中，签名入口也会
根据可信父 Worker 的 validation props 拒绝凭据获取，绑定的对象传输 Fetcher 同样禁用。
独立 validator 只挂载派生或显式配置的 validator token，不挂载 serving token。
候选代码不能借验证写生产对象。
trusted entrypoint 开启 request signal 以转发断开事件；业务模块的 compatibility flags
继续来自原始 manifest。
签名控制调用使用 runtime 内的 Fetcher，消息上限 32 KiB，不包含对象数据；不要求业务
开启 RPC，因此早于 `2024-04-03` 的 compatibility date 也能使用 R2 binding。

## 运行与验证

runtime 镜像包含 `workers/runtime/storage/`。local-first 的配置渲染器为每个 store 生成
固定目标、固定 Host、校验证书的 ExternalServer，并生成相应网络出口规则。
issuer AK/SK 和 cursor key 不写入 runtime ConfigMap；临时凭据经已认证内部接口按需获取。

新增了三个完整基线表：`krun_worker_buckets`、`krun_worker_environment_r2`、
`krun_worker_version_r2`。binding 的父实体删除时在事务内清理；版本保留清理任务也同步
清理引用。桶和对象是持久资源，没有默认 TTL。没有逐对象数据库行。
已有数据库 fingerprint 与本版不一致时启动会拒绝；需要显式使用新建的专用开发/测试库，
本实现不会自动重建现有数据库。

```sh
npm test --prefix workers/runtime
go test -race ./...
make check-storage-models
make check-secrets
```

真实 TOS 验收命令见 [凭据模块的验证说明](storage-credentials.md)。该验收覆盖真实
workerd 的 facade、原始 gzip 字节、Range、条件读写、空对象、分页、元数据和清理。
mock 存储还验证上传提前到达、固定 Host 签名、32 MiB 流的背压与断开后的上游取消。
没有完成 Cloudflare 账号上的逐项对照，也没有宣称所有 S3 实现已通过这些能力。

## 开源项目兼容性实测

2026-11-04 拉取以下项目，在本仓库固定版本的 workerd、实际 `entry.js` 和 R2 facade
上执行原始业务代码，后端使用主配置声明的真实 TOS 与 prefix 受限 STS。
共 21 项选定 HTTP 检查通过，包含 multipart 和 Cache API；这不能视为所有项目功能完全兼容。

| 项目 / 固定 commit | 已验证流程 | 结果与边界 |
| --- | --- | --- |
| [r2-file-manager](https://github.com/msaifuddin/r2-file-manager/tree/282ad563ccc48ae4297fa3506ceae77bfa329a81) | 请求流上传、表单 File.stream、索引页、下载、ETag、删除 | 4 项通过；未测试并发更新索引 |
| [ByteGo](https://github.com/zopiya/bytego/tree/ff21c5b6de9a2b3e5f648139753a73d08ddf6f2a) | 首页 HTML、鉴权上传、中文文件名 metadata、删除 | 3 项通过；保留上游 2023 年 compatibility date；CDN URL 仅验证生成格式，不验证独立公开域名服务 |
| [r2-share-worker](https://github.com/itoschum/r2-share-worker/tree/70b2b79be23a18fe76964ff483a0cc5a6e6669be) | 错误 token、65 个分页样本、附件下载、媒体 HEAD + Range | 4 项通过；样本预置有效平台 envelope，不代表原生 S3 对象已迁移兼容 |
| [R2-Explorer](https://github.com/G4brym/R2-Explorer/tree/2c09ecbea00be66992ddb8c63946aed1a9be59e6) | 上传、平铺列表、目录列表、下载、流式重命名、删除、跨请求 multipart 续传合并 | 7 项通过；分片为 5 MiB + 尾片，下载核对完整字节；未验证 dashboard 浏览器交互、分享、邮件和全部列表参数 |
| [r2-image-worker](https://github.com/yusukebe/r2-image-worker/tree/80cc728a049af10bfd9347a11088ee1d6dc4bbbd) | Basic Auth、表单 File 上传、下载与再次读取缓存命中 | 3 项通过；第二次响应检查 Age；未注入可选 IMAGES binding，未测试图片变换 |

实测修复了两个问题：`list({delimiter:""})` 现在等价于省略 delimiter；签名从依赖业务
RPC flag 的调用改为本地 Fetcher 控制调用，ByteGo 不必修改 compatibility date。
native runtime 验收还覆盖真实 TOS 的分片取消、重复取消及取消后写入失败；Cache API
跨请求的 put、match、Range、条件读取和 delete 也在真实 workerd 中执行。

测试通过 esbuild 打包上游代码，业务源码不修改。R2-Explorer 使用其公开的
`R2Explorer({readonly:false})` 入口；其余直接打包原始 default export。
配置分发、路由和凭据接口由本地测试 fixture 提供，凭据由真实 Go provider 签发。
这覆盖真实 runtime 和对象存储数据链路，不包含控制台部署、完整 Wrangler 上传流程、
Kubernetes rollout 或 Cloudflare 线上账号对照。上游仓库和安装产物只保存在 gitignored
`.data/r2-project-compat/repos/`，没有复制到发布产物中。

在仓库根目录准备固定上游版本和生产依赖（不运行上游安装脚本）：

```sh
node workers/runtime/test/r2-projects.mjs --prepare
go test ./internal/adapters/storagecredentials -count=1 \
  -run '^TestConfiguredR2Projects$' -v -args \
  -storage-config=../../../.data/dev.yaml \
  -storage-projects=../../../.data/r2-project-compat/repos
```

脚本检查上游 commit 和 tracked 文件未修改；npm 依赖按上游声明解析，安装结果保留在
本地，上游未锁定的依赖不承诺跨时间完全一致。Explorer 的 Hono peer dependency 使用
同批图片项目安装的 Hono（本次为 4.13.16）。测试不依赖主数据库，不重建现有开发库；
每次创建随机测试 prefix，凭据文件权限为 0600，结束后逐对象清理并确认该 prefix 为空。
正常 `npm test` / `go test` 不下载上游，也不隐式访问真实 TOS。
