---
title: 使用 Wrangler 部署 Workers
description: 使用 Cloudflare Wrangler 创建、构建和发布 Worker 到 krun。
sidebar:
  order: 2
---

krun 支持使用 Cloudflare Wrangler 部署模块格式的 JavaScript 和 TypeScript Worker。下面的示例使用已验证的 Wrangler 4.155.0，将 Worker 发布到 krun。

**仅兼容本文列出的 Wrangler 用法和[组件文档](/docs/guides/component-workers.html)列出的 API；很多 Cloudflare API 和产品不支持。**

## 1. 准备账号

准备 Node.js、npm、krun 控制台地址、组织 ID，以及有 Worker 发布权限的 krun API Key。可先通过 `krun org list` 查看组织 ID，并在控制台创建 API Key。

在终端配置连接，将组织 ID 和 API Key 替换为自己的值：

```sh
export CLOUDFLARE_API_BASE_URL={{KRUN_ORIGIN}}/api/v1/cloudflare/client/v4
export CLOUDFLARE_ACCOUNT_ID=replace-with-organization-id
export CLOUDFLARE_API_TOKEN=replace-with-krun-api-key
```

API Key 是凭据，请勿提交到代码仓库。以下命令使用当前终端中的配置，目标是 krun。

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

## 使用仓库构建

也可以在 krun 控制台的 Workers 页面创建 Worker、连接 Git 仓库，然后构建并发布。仓库需要包含 Wrangler 配置及一个 npm、pnpm、Yarn 或 Bun 锁文件。平台按仓库配置执行构建；构建成功后，在控制台选择产物和目标环境发布。

Wrangler 配置的用法可参考 [Cloudflare 官方文档](https://developers.cloudflare.com/workers/wrangler/configuration/)；自定义构建命令可配置在 `build.command` 中，详见 [Custom builds](https://developers.cloudflare.com/workers/wrangler/custom-builds/)。

编写 Workers 代码时，参照 [Workers 代码兼容范围](/docs/guides/workers-runtime-compatibility.html) 选择可用 binding 和方法。
