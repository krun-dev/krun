# S3 兼容对象存储

制品、构建日志、静态资产和 Runtime 配置快照共用主 YAML 的
`workers.artifact_storage`。业务逻辑桶和 R2 binding 继续使用独立的
`workers.object_storage`，因为它们按组织和业务桶分别授权。见 [R2 binding](workers-r2.md)。

`endpoint` 是控制面的读写和清理地址。只有网络入口不同时才填写
`builder_endpoint`、`runtime_endpoint` 或 `log_read_endpoint`，三个地址都必须指向同一个桶。
构建和日志默认使用 `endpoint`，Runtime 默认使用构建地址。日志地址用于发给浏览器或 CLI
的签名 URL，因此要从对应客户端可达。`config_base_url` 默认根据 Runtime 地址、桶和前缀推导。

| 寻址方式 | 对象地址 | 用法 |
| --- | --- | --- |
| `path` | `https://endpoint/bucket/key` | MinIO 和支持 path-style 的兼容服务 |
| `virtual` | `https://bucket.endpoint/key` | 火山 TOS 和支持桶域名的服务 |
| `auto` | 根据 endpoint 选择 | AWS 域名用 virtual，其他地址用 path，HTTPS 下带点的桶名用 path |

`region` 是对象请求的签名区域。`bucket_mode: managed` 由控制面创建专用私有桶，
`external` 由部署方提供已有私有桶。更换物理桶或对象前缀时使用新的 `location_id`，
防止把已有制品记录当作新位置中的对象。

## AK/SK

用 `mode: ak_sk` 直连 MinIO、AWS S3 或其他 S3 兼容服务。填入该存储的 AK/SK，
并把示例地址和桶换成部署所在环境的值。

```yaml
workers:
  artifact_storage:
    endpoint: https://s3.example.com
    region: us-east-1
    bucket: krun-artifacts
    prefix: dev/run
    location_id: primary
    addressing_style: path
    bucket_mode: external
    credentials:
      mode: ak_sk
      access_key_id: ${KRUN_ARTIFACT_ACCESS}
      secret_access_key: ${KRUN_ARTIFACT_SECRET}
```

## STS

`mode: sts` 必须指定 provider。目前支持 `provider: volcengine`，调用火山 STS AssumeRole。
AK/SK 用于扮演角色，S3 请求使用自动刷新的临时凭据。STS 模式使用已有的桶和非空的对象前缀，
寻址默认为 `virtual`。地址使用同区域的 TOS HTTPS S3 公网或内网域名。

```yaml
workers:
  artifact_storage:
    endpoint: https://tos-s3-cn-beijing.ivolces.com
    log_read_endpoint: https://tos-s3-cn-beijing.volces.com
    region: cn-beijing
    bucket: example-krun-artifacts
    prefix: krun/production/artifacts
    location_id: company-pilot-v1
    credentials:
      mode: sts
      provider: volcengine
      access_key_id: ${KRUN_ARTIFACT_ACCESS}
      secret_access_key: ${KRUN_ARTIFACT_SECRET}
      role_trn: ${KRUN_ARTIFACT_ROLE}
      # sts_endpoint: https://sts.volcengineapi.com
```

将 `KRUN_ARTIFACT_ROLE` 设为获授权的 IAM 角色 TRN。AK/SK 所属身份需要
`sts:AssumeRole` 权限，目标角色的信任策略需要包含该身份。角色最大会话时长至少为
1 小时。以下是该示例的角色权限策略。如使用不同的桶或前缀，替换两处相应的资源和前缀条件。

```json
{
  "Statement": [
    {
      "Effect": "Allow",
      "Action": ["tos:GetObject", "tos:PutObject", "tos:DeleteObject", "tos:AbortMultipartUpload", "tos:ListMultipartUploadParts"],
      "Resource": ["trn:tos:::example-krun-artifacts/krun/production/artifacts/*"]
    },
    {
      "Effect": "Allow",
      "Action": ["tos:ListBucket"],
      "Resource": ["trn:tos:::example-krun-artifacts"],
      "Condition": {
        "StringLike": {"tos:prefix": ["krun/production/artifacts/*"]}
      }
    }
  ]
}
```

控制面会在角色权限内将会话限制到配置的桶和前缀。Runtime 获得的是单个制品或资产目录、
或本池当前配置代次的只读授权。构建器和日志客户端使用预签名 URL。详细的租约和刷新行为见
[对象存储凭据](storage-credentials.md)。

## 从旧配置升级

旧版扁平 `workers.s3_*` 继续可读。切换到新块时，地址、桶、前缀、区域、寻址和位置 ID
从旧字段原样迁移，例如 `s3_endpoint` 到 `artifact_storage.endpoint`。
可选的 `config_base_url` 也移入新块。删掉旧字段后再添加新块，两种布局不能混用。
旧版单独使用 `s3_runtime_credentials` 的部署可保留旧布局，直到制品存储准备好统一使用 STS。

切换 STS 时一起升级控制面和 serving Runtime。使用新版渲染器生成的 serving 配置会自动
启用 `CONFIG_STORAGE_STS`。如果维护手写的 workerd 配置，为可信入口添加文本绑定
`CONFIG_STORAGE_STS=1`，移除 `STORAGE_ACCESS_KEY`、`STORAGE_SECRET_KEY`
和 `STORAGE_REGION`，以及专用于这两个密钥的挂载。保留 `CONFIG_BASE`。
控制面需能访问 STS，Runtime 需能访问同一桶的对象端点。

升级后部署一个 Worker，确认构建、日志读取和真实域名访问正常。
`storage credentials: permission_denied` 表明 AssumeRole 被拒绝，优先检查调用者授权和角色信任。
`list artifact objects under …: Access Denied` 表明对象列表请求被拒绝，检查角色上的
`tos:ListBucket`、桶资源和 `tos:prefix` 条件。
