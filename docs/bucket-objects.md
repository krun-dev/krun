# 对象浏览、直传与 CDN 分发

组织的「对象存储」页面点击桶名称进入文件浏览器。支持按 `/` 分隔目录、路径前缀、
前后分页、刷新、对象详情、多个文件顺序上传、进度、取消、下载及确认删除。页面刷新
保留 bucket 与 prefix 查询参数。列表只读取 S3 一页，不逐项 HEAD；打开详情才 HEAD。
目录由 key 的 `/` 前缀推导，不创建目录实体。数据库不保存逐对象记录。

## 请求与权限

对象身份是逻辑桶 ID（Worker 配置中使用桶名称）和相对 key。底层映射为：

```text
<root_prefix>/org/<organization_id>/buckets/<bucket_id>/<key>
```

原生对象接口的 base 为 `/api/v1/orgs/{org}/workers/buckets/{bucketID}/objects`：

| 接口 | 行为 | 权限 |
| --- | --- | --- |
| `GET <base>?prefix=&cursor=&limit=100` | `/` delimiter 列表，limit 为 1–1000 | `storage.object.read` |
| `GET <base>/head?key=...` | 大小、ETag、修改时间及 R2 元数据 | `storage.object.read` |
| `DELETE <base>?key=...` | 删除单个对象，存储端执行幂等删除 | `storage.object.delete` |
| `POST <base>/access` | `upload` 的 PUT 签名 | `storage.object.write` |
| `POST <base>/access` | `read` / `download` / `delivery` 的 GET 链接 | `storage.object.read` |
| `GET <base>/download?key=...` | 身份校验后 302 到带 attachment 的预签名 GET | `storage.object.read` |

三个对象权限独立作用于组织资源。`organization.read` 只允许看桶目录，不允许读取对象。
system_admin、本组织 org_admin 和 member 可以管理对象；viewer 可以浏览和下载，
不能上传或删除。逻辑桶创建和状态修改分别要求 `storage.bucket.create`、
`storage.bucket.update`，同样仅管理员和 member 拥有，不授予组织设置修改权限。
API Key 的权限仍与持有者当前角色取交集。当前 IAM 以组织为授权粒度，
不提供逐逻辑桶、逐 key 的 IAM 选择器。STS 云端会话仍限制到当前逻辑桶的固定 prefix
及本次 read/write/delete/list 操作。禁用桶、跨组织请求不能签发新链接。

所有 API 保持同源 `/api/*`、现有 Session/CSRF 或 Bearer 鉴权。控制面只处理列表、
HEAD、删除和签名，不接收文件内容。网络调用在状态快照和授权完成之后执行，不持有
状态锁或数据库事务。列表、签名和详情不产生逐对象数据库记录。

列表 cursor 带 HMAC，绑定组织、bucket ID/revision、store、prefix、delimiter 和
start_after，一小时失效。不是 S3 continuation token 的透传；不要自行解析或修改。

## 上传下载协议

请求上传链接：

```json
{
  "operation": "upload",
  "key": "images/photo.png",
  "size": 12345,
  "http_metadata": { "contentType": "image/png" },
  "custom_metadata": { "source": "console" }
}
```

响应包含 `key`、`url`、`method`、`headers`、`expires_at` 和上传的随机 `version`。
向这个 URL 发出 PUT，发送原始文件及返回的全部 headers。Content-Length 由客户端
HTTP 栈生成，必须等于声明的 size；浏览器不要手动设置。签名绑定 Content-Length
及保留元数据，修改元数据或大小会使签名无效。单次上传最多 5 GiB；浏览器目前采用
单次 PUT，不提供 multipart、断点续传或上传重试。取消可能留下已完成的对象，重新
列表确认；同 key 会覆盖。R2 binding 自身的 multipart 能力见 [R2 文档](workers-r2.md)。

上传签名同时写入 `x-amz-meta-krun-v1`，沿用 R2 version、HTTP/custom metadata
envelope 与大小预算，因此 Worker 可以直接读取控制台上传的对象。直接用第三方 S3
工具写入的裸对象可以浏览、下载和删除；缺少 envelope 时详情会提示 binding 无法读取。
工具的任意 S3 metadata 不会自动转为 R2 metadata。

下载签名请求为 `{"operation":"download","key":"images/photo.png"}`；`read` 不强制
attachment。GET 支持存储端的 Range。签名最长约 5 分钟，且早于 STS 过期时间至少
30 秒；每次下载重新生成。响应和 302 均 no-store。浏览器直传不发送控制台 Cookie、
Authorization 或 CSRF header；它使用 URL 中的短期能力授权。业务 API 可拿到链接后
交给自己的客户端。将 `{bucketID,key}` 保存为引用，不能将签名 URL 当作永久地址。

预签名 URL 会显示物理域名、桶和前缀以及临时 AccessKeyId/SessionToken 等签名参数，
不包含 SecretAccessKey；能转发给任何持有者使用至失效。需要隐藏存储位置时使用下述
CDN 域名。已签发的 S3/CDN 链接不保证随平台角色撤销或桶停用而立即失效。

## 浏览器 CORS 与静态部署

生产控制台仍是 OSS 静态 SPA，所有控制面 API 都走同源 `/api/*`。文件字节直连对象
存储是独立的数据通道；不增加前端服务端运行时，也不为 Go 增加 CORS。

在**物理对象桶**配置 CORS，保留已有业务规则，增加控制台的准确 origin，例如：

```json
{
  "AllowedOrigins": ["https://console.example.com"],
  "AllowedMethods": ["GET", "HEAD", "PUT"],
  "AllowedHeaders": ["content-type", "content-language", "content-disposition", "content-encoding", "cache-control", "expires", "range", "x-amz-meta-krun-v1"],
  "ExposeHeaders": ["ETag", "Content-Length", "Content-Range", "Last-Modified"],
  "MaxAgeSeconds": 600
}
```

这是 S3 规则对象，各厂商控制台/API 的外层格式可能不同。允许 OPTIONS 预检由对象
存储处理，不把 OPTIONS 加入 S3 AllowedMethods。不要通过向对象桶转发控制台 Cookie
解决 CORS。参考 [Cloudflare 的预签名访问与 CORS 说明](https://developers.cloudflare.com/r2/buckets/cors/)。

静态 OSS/CDN 的 HTML 响应 CSP 还需将 `connect-src` 配成 `'self'` 加所有配置中实际
对象存储 origin。virtual 模式包含物理 bucket hostname，例如
`https://example-worker-data.tos-s3-cn-beijing.volces.com`；path 模式使用 endpoint origin。
Go 本地静态托管会从主配置自动生成此白名单。生产静态 HTML 由 OSS/CDN 返回，因此
必须在该层设置，不能只改 Go 的 API 响应头。其他脚本、样式和 iframe 策略无需放开。

## 自有域名和火山 CDN

`internal/adapters/objectdelivery.Provider` 独立于 S3/STS 适配器。目前支持火山 CDN
Type A、SHA256、参数名 `sign`、uid `0`。主 YAML 的对应 store 可选配置：

```yaml
delivery:
  type: volc_cdn_a
  base_url: https://files.example.com
  signing_key: ${KRUN_OBJECT_CDN_SIGNING_KEY}
  ttl_seconds: 300
```

key 至少 32 字符，真实值只放主配置或由其展开的环境变量。字段通过 LoadConfig 加载，
不注入前端或 workerd。TTL 为 60–3600 秒，必须与 CDN 配置相同。配置后对象详情出现
「复制短期 CDN 链接」，外部客户端也能 POST access，body 使用 `delivery`。

签名路径稳定为 `/org/{org}/buckets/{bucketID}/{key}`，查询签名每次不同：

```text
https://files.example.com/org/<org>/buckets/<bucketID>/<key>?sign=<timestamp>-<rand>-0-<sha256>
sha256 = SHA256(<转义后的完整路径>-<timestamp>-<rand>-0-<signing_key>)
```

平台实现签名和返回链接，不自动创建 CDN 域名、证书、CNAME、桶策略或回源规则。部署时
在火山 CDN 设置以下对应能力，参考 [Type A 官方配置](https://docs.volcengine.com/docs/information-delivery-network/type-a-configuration?lang=zh)
及 [TOS 私有桶 CDN 加速](https://docs.volcengine.com/docs/TorchObjectStorage/AcceleratingTOSResourceAccesswithCDN?lang=zh)：

1. 自有域名、HTTPS 证书和 CNAME；使用同一个域名作为 base_url。
2. Type A + SHA256 + `sign` + 相同 key、TTL，必须在命中缓存时也先验证签名。
3. 私有 TOS 源站跨服务授权，或 CDN 自己持有的回源凭据；不能把用户 URL 的 Type A
   签名当成 TOS S3 签名。其他 S3 服务的私有回源需要 CDN 支持对应的签名方式。
4. 只接受 `/org/.../buckets/.../...` 命名空间，回源时原样添加 `/<root_prefix>`；例如
   `/org/a/buckets/b/file.png` 变成 `/worker-data/v1/org/a/buckets/b/file.png`。
   保留 Unicode、百分号、空格及大小写的对象语义，禁止目录穿越和路径归一化产生别名。
5. 鉴权后移除 `sign` 回源，缓存 key 忽略该签名参数，其他内容参数按实际业务配置。

字节链路是「客户端 → CDN → 私有对象存储」。缓存 miss 和 hit 都不经过 krun-server。
控制台 302 入口属于需要登录的稳定下载地址，会经过一次 Go 授权；缓存签名链接或
直接使用 CDN 的短期链接则不需要每个字节请求都向 Go 申请。当前不提供永久匿名公开
桶、S3 虚拟 endpoint、完整 Cloudflare 对象内容代理或 CDN 自动化开通。

## 验证

```sh
# 静态产物 + Go + 随机 PostgreSQL schema + 独立 MinIO 容器，无 API mock
scripts/test-object-browser.sh .data/dev.yaml

# 使用主配置中的真实 provider，只写随机逻辑桶前缀并清理
go test ./internal/adapters/bucketobjects -run TestConfiguredDirectAccess -v \
  -storage-config ../../../.data/dev.yaml

# Go 预签名直传后，由实际 workerd R2 binding 读回内容和元数据
go test ./internal/adapters/storagecredentials -run TestConfiguredR2Runtime -v \
  -storage-config ../../../.data/dev.yaml
```

浏览器脚本需要 Docker、Node、Go、已安装的 Playwright Chromium，以及主配置指向的
可连接 PostgreSQL（允许创建 schema）。不清空应用库；只创建并清理自己的随机 schema，
MinIO 使用临时容器且不挂载持久卷。fixture 会覆写内存中的测试 origin、开发登录及
对象存储连接，不创建第二份应用 YAML。既有桶和绑定管理 e2e 一起执行。

已验证真实 TOS 直传/签名、特殊字符 key、分页、隔离、清理，以及 Go 写入和 workerd
R2 读取互通。浏览器完整链路使用真实 MinIO CORS；CDN 验证覆盖本地 Type A 协议、
签名防篡改和路径改写，尚未使用真实 CDN 账号/域名验证线上回源。
