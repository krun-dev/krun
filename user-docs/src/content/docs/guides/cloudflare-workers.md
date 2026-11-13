---
title: 使用 Wrangler 部署 Workers
description: 使用 Cloudflare Wrangler 创建、构建和发布 Worker 到 krun。
sidebar:
  order: 2
---

krun 支持使用 Cloudflare Wrangler 部署模块格式的 JavaScript 和 TypeScript Worker。下面的示例使用已验证的 Wrangler 4.155.0，将 Worker 发布到 krun。

Wrangler 是 Cloudflare 提供的 Workers 开发和部署命令行工具；它读取项目的 Wrangler 配置并处理构建。本指南将 Wrangler 的 API 地址指向 krun，因此发布目标是 krun。krun CLI 是另一个工具，用于管理 krun 的组织、环境、对象存储和仓库构建发布；两者如何选择见[基础概念](/docs/guides/concepts.html#wrangler-和-krun-cli)。

**仅兼容本文列出的 Wrangler 用法和[组件文档](/docs/guides/component-workers.html)列出的 API；很多 Cloudflare API 和产品不支持。**

迁移已有项目时，先检查 [Wrangler 配置兼容范围](/docs/guides/wrangler-configuration.html)，确认配置文件、构建字段、binding 和环境配置是否适用于所选发布方式。

## 1. 准备账号

准备 Node.js、npm、krun 控制台地址、组织短名或 ID，以及有 Worker 发布权限的 krun API Key。可先通过 `krun org list` 查看组织短名（`short_name`）和 ID，并在控制台创建 API Key。

在终端配置连接，将组织短名或 ID 和 API Key 替换为自己的值：

```sh
export CLOUDFLARE_API_BASE_URL={{KRUN_ORIGIN}}/api/v1/cloudflare/client/v4
export CLOUDFLARE_ACCOUNT_ID=replace-with-organization-short-name
export CLOUDFLARE_API_TOKEN=replace-with-krun-api-key
```

API Key 是凭据，请勿提交到代码仓库。以下命令使用当前终端中的配置，目标是 krun。

### 创建可部署的 API Key

API Key 携带一份策略（policy）。**策略决定 Wrangler 部署能否成功：`CLOUDFLARE_API_TOKEN` 鉴权通过但策略不覆盖所需动作时，兼容接口会返回 `code 10000 没有访问权限`。**

部署 Worker 至少需要 `worker.create`、`worker.read`、`worker.list`、`worker.version.read`、`worker.publish`（按需再加 `build.start`、`log.read`、`worker.invoke`、R2 对象权限）。注意通配符的展开规则：

- `actions: ["*"]` **只会展开到选择器 `kind` 匹配的动作**。`kind: organization` 的 `*` 不含 `worker.create` 等 Worker 级动作——这是「Key 能登录却部署失败」的最常见原因。
- 组织内全量部署用 `kind: "*"` + `all_in_organization: true`；最小授权用 `kind: worker` + `ids: [Worker 定位符]`。

用 CLI 创建（`--policy` 文件只包含策略对象本身，`--name`、`--expires` 单独传参）：

```sh
cat > deploy-policy.yaml <<'EOF'
schema_version: 1
statements:
  - effect: allow
    actions: ['*']
    resource:
      kind: '*'
      organization_id: replace-with-organization-id
      all_in_organization: true
EOF

krun keys create --org ORG_ID --name wrangler-deploy --expires 24h --policy deploy-policy.yaml
```

命令输出的 `key`（`krun_key_` 开头，仅显示一次）填入 `CLOUDFLARE_API_TOKEN`。策略仍与创建者本人的权限取交集；更细粒度的样例见仓库 [config/key-policy.example.yaml](https://github.com/krun-dev/krun/blob/dev/config/key-policy.example.yaml)。也可以在控制台「API Keys」页面创建。

## 2. 创建 Workers 项目

创建目录和入口文件：

```sh
mkdir hello-worker
cd hello-worker
mkdir -p src
```

创建 `src/index.js`：

```js
export default {
  fetch() {
    return new Response("Hello from krun!");
  },
};
```

在项目根目录创建 `wrangler.jsonc`：

```jsonc
{
  "name": "hello-worker",
  "main": "src/index.js",
  "compatibility_date": "2026-09-28"
}
```

`name` 是组织内的 Worker 名称；首次发布会在 krun 中创建对应的 Worker。后续使用相同名称发布会更新该 Worker。

## 3. 构建并发布

先在项目目录中检查构建结果；此命令不会发布：

```sh
npx wrangler@4.155.0 deploy --dry-run --outdir ./dist
```

构建成功后发布：

```sh
npx wrangler@4.155.0 deploy
```

`deploy` 会再次构建、上传并部署 Worker。发布完成后，使用命令输出或 krun 控制台中的访问地址验证；地址使用 krun 配置的域名。

修改代码后再次运行 `deploy` 即可发布新版本。已有 Wrangler 项目也可以在项目目录中使用相同命令；配置文件中的入口和构建配置会交给 Wrangler 处理。

## 4. 管理版本和预览

如果希望先上传版本、稍后再发布：

```sh
npx wrangler@4.155.0 versions upload
# 将 VERSION_ID 替换为上传命令输出的版本 ID
npx wrangler@4.155.0 versions deploy VERSION_ID@100% --yes
```

上传版本不会改变当前生产流量；部署命令将流量全部切换到指定版本。

用 `versions list` 查询已上传版本。将下面的 `STABLE_VERSION` 和 `CANDIDATE_VERSION` 换成同一 Worker 的两个版本 ID：

```sh
npx wrangler@4.155.0 versions deploy "STABLE_VERSION@100%" "CANDIDATE_VERSION@0%" --yes
npx wrangler@4.155.0 versions deploy "STABLE_VERSION@95%" "CANDIDATE_VERSION@5%" --yes
npx wrangler@4.155.0 deployments status
```

控制台与两种 CLI 共用发布记录。0% 定向验证、会话分桶和回滚步骤见 [Workers 灰度发布](/docs/guides/workers-gradual-deployments.html)。

也可以创建独立预览，检查修改：

```sh
npx wrangler@4.155.0 preview --name my-preview
npx wrangler@4.155.0 preview delete --name my-preview
```

预览使用独立地址，不会替换生产部署。

## 5. 设置 Secret

在项目目录运行以下命令，按提示输入 Secret 值：

```sh
npx wrangler@4.155.0 secret put API_TOKEN
npx wrangler@4.155.0 secret list
```

Worker 可以通过 `env.API_TOKEN` 读取该值。Secret 更新会基于当前部署生成并发布新版本；如果已有尚未部署的新版本，请先处理该版本。

## 6. 常见错误对照

Wrangler 的报错来自 krun 的 Cloudflare 兼容接口，错误码与平台语义的对应：

| Wrangler 报错 | 平台原因 | 处理 |
| --- | --- | --- |
| `code 10000 没有访问权限` | Token 无效，或 API Key 策略未覆盖该接口所需动作（见上文通配符展开规则） | 核对 `CLOUDFLARE_API_TOKEN`；用 `krun check --kind worker --id ORG/WORKER --action worker.publish --env production` 验证策略 |
| `code 10007 资源不存在` | 目标 Worker 尚不存在，或当前身份无权访问该组织 | 先创建 Worker（`krun workers create --name NAME`）或核对 `CLOUDFLARE_ACCOUNT_ID` 组织短名 |
| `code 10021`（创建 R2 桶等资源） | 兼容接口不会按 `wrangler.jsonc` 自动创建桶等资源 | 先在 krun 创建资源并绑定，再把配置中的 `bucket_name` 指向实际桶名 |
| `HYPERDRIVE_NOT_CONFIGURED` | 控制面未配置 Workers 密钥环 | 请平台管理员配置 Workers 密钥环 |

## 7. 无浏览器自动化部署（Agent / CI）

Wrangler 路径天然适合无人值守：API Key 鉴权不经过浏览器。示例仓库已含全部 `wrangler.jsonc`，克隆后逐目录执行：

```sh
git clone --depth 1 https://github.com/krun-dev/krun-examples.git
cd krun-examples/examples/hono
npm ci
npx wrangler@4.155.0 deploy
```

需要 R2、Hyperdrive 绑定的示例，按各示例页先创建资源、更新环境绑定并把配置中的桶名/配置 ID 改为实际值。CI 中把 `CLOUDFLARE_API_BASE_URL`、`CLOUDFLARE_ACCOUNT_ID`、`CLOUDFLARE_API_TOKEN` 设为环境变量即可；`krun` CLI 侧可用 `KRUN_ENDPOINT` + `KRUN_TOKEN` 免交互执行资源管理命令。

每篇文档都可以直接抓取 Markdown 原文（页面顶部「复制 Markdown」即来自该端点）：在页面 URL 后加 `.md.txt`，例如 `{{KRUN_ORIGIN}}/docs/guides/cloudflare-workers.md.txt`。`{{KRUN_ORIGIN}}` 占位符替换为控制台 origin。给编码 Agent 的入口清单见 [Workers 代码兼容范围](/docs/guides/workers-runtime-compatibility.html#给编码-agent-的列表)。

## 使用仓库构建

也可以在 krun 控制台的 Workers 页面创建 Worker、连接 Git 仓库，然后构建并发布。仓库需要包含 Wrangler 配置及一个 npm、pnpm、Yarn 或 Bun 锁文件。平台按仓库配置执行构建；构建成功后，在控制台选择产物和目标环境发布。

可以从 [Worker 示例部署指南](/docs/guides/worker-examples.html) 开始，按照纯静态小站、Hono、Astro 和对象存储示例了解完整流程。纯静态示例仅声明 `assets`，无需编写 Worker 入口。

Wrangler 配置的用法可参考 [Cloudflare 官方文档](https://developers.cloudflare.com/workers/wrangler/configuration/)；自定义构建命令可配置在 `build.command` 中，详见 [Custom builds](https://developers.cloudflare.com/workers/wrangler/custom-builds/)。

编写 Workers 代码时，参照 [Workers 代码兼容范围](/docs/guides/workers-runtime-compatibility.html) 选择可用 binding 和方法。
