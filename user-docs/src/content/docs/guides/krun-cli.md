---
title: krun CLI 使用指南
description: 安装 krun CLI，管理身份、构建和发布 Workers，并操作对象存储。
---

krun CLI 用于登录控制面、构建和发布 Worker，以及管理组织权限与对象存储。下面的地址会使用当前控制台地址。

## 安装

在装有 curl 和 Python 3 的 macOS 或 Linux 终端运行：

```sh
(
  set -eu
  installer=$(mktemp)
  trap 'rm -f "$installer"' EXIT
  status=$(curl -q -fsS -o "$installer" -w '%{http_code}' "{{KRUN_ORIGIN}}/install.sh")
  [ "$status" = 200 ]
  sh "$installer" "{{KRUN_ORIGIN}}"
)
```

脚本自动识别 Intel/AMD 64 位和 ARM64，无需 root，安装到 `~/.local/bin/krun`；安装前会对照同源发布清单验证 SHA-256，再次运行会更新该文件。脚本不会修改 shell 配置。如果安装目录不在 PATH 中，按提示运行下面的命令，即可在当前终端使用：

```sh
export PATH="$HOME/.local/bin:$PATH"
```

要让后续终端也能使用，将同一行加入 `~/.zshrc`（zsh）或 `~/.bashrc`（bash），然后重新打开终端。运行 `krun --help` 检查安装。Windows 请从 [Quick Start](/docs/) 手动下载对应版本。

## 配置 endpoint

保存控制台地址，后续命令无需重复传入 `--endpoint`：

```sh
krun config set endpoint {{KRUN_ORIGIN}}
krun config get endpoint
krun config show
krun login
```

endpoint 必须是控制台的 origin（如 `https://console.example.com`），不要加 `/api` 或页面路径。HTTPS 为必需，本地开发允许 `http://localhost:8080` 或 `http://127.0.0.1:8080`。`config show` 显示配置文件位置、已保存的 endpoint、默认组织和凭据保存状态，不输出 Token。

默认登录配置保存在系统用户配置目录的 `krun/config.yaml`。需要切换控制台或账号时，用 `--profile NAME` 选择独立配置。例如：

```sh
krun --profile team config set endpoint {{KRUN_ORIGIN}}
krun --profile team login
krun --profile team config set org ORG_ID
krun --profile team workers list
```

命名配置保存在 `krun/profiles/NAME.yaml`，也可用 `--profile ./team.yaml` 指向已有的本地配置文件。项目中的 Wrangler 文件用于构建，登录配置请留在本机。

单次命令用 `--endpoint URL` 覆盖地址，也可用 `KRUN_ENDPOINT` 设置自动化环境的默认地址。优先级为：命令行参数 > `KRUN_ENDPOINT` > 保存的配置 > 本地默认地址 `http://127.0.0.1:8080`。`config get/show` 始终显示保存的配置。切换保存的 endpoint 会清除旧地址的登录凭据和默认组织。临时连接其他 endpoint 时，本次命令也不会沿用原地址的凭据和组织。

## 设置默认组织

从 `krun org list` 找到组织 ID，保存到当前 CLI 配置：

```sh
krun config set org ORG_ID
krun config get org
krun workers list
krun r2 bucket list
```

Worker、构建、部署、仓库、对象存储和组织管理命令会使用该默认组织。单次指定 `--org OTHER_ORG_ID` 可以覆盖默认值，`krun config unset org` 清除默认值。系统级操作显式使用 `--system`，例如 `krun audit --system`。每个 `--profile NAME` 可以保存各自的默认组织。

## 升级

```sh
krun --version
krun --version --verbose
krun upgrade --check
krun upgrade
```

正式版使用 UTC 发布日期，例如 `2026-11-10`。同一天的后续发布加序号，例如 `2026-11-10.2`。用 `--version --verbose` 可以查看构建提交和发布时间。

在终端成功执行连接控制台的命令后，正式版 CLI 每 24 小时最多向该控制台检查一次更新。有更高版本时，终端会提示用 `krun upgrade` 安装。提示写到 stderr。CI、非交互调用和开发版跳过检查。也可以用全局参数 `--no-update-check` 关闭本次自动检查。

升级会从配置的控制台下载当前系统和架构的已发布 CLI，支持 macOS、Linux、Windows 的 amd64/arm64。无需登录，也不会发送 API Token。`--check` 只检查，不替换文件。

未保存 endpoint 时，可直接运行：

```sh
krun upgrade --endpoint {{KRUN_ORIGIN}}
```

命令更新当前运行的可执行文件，保留配置与登录信息。下载完成并通过 SHA-256 校验后才替换文件；下载失败或校验失败会保留原文件。如果文件通过符号链接调用，更新的是链接目标。安装目录需要可写；Windows 可能保留一个 `.old` 备份，下次升级会清理，若提示被占用请先退出旧的 CLI 进程。

升级目标是**该控制台当前发布的版本**；切到较旧的控制台版本也会安装对应版本。旧 CLI 如果还没有 `upgrade` 命令，macOS/Linux 可重新运行上面的安装脚本，Windows 可重新下载后替换文件。

## 登录和退出

```sh
krun login --endpoint {{KRUN_ORIGIN}}
krun whoami
krun logout
```

登录时在浏览器中完成授权；CLI 会保存登录信息并自动刷新授权。退出会撤销当前授权。

远程终端可以使用设备授权，按终端提示在浏览器完成登录：

```sh
krun login --endpoint {{KRUN_ORIGIN}} --device --browser=false
```

CLI 接受 HTTPS 控制台地址，本地开发允许 loopback HTTP。登录多个账号时，可用 `--profile NAME` 分开保存。

## 管理组织和邀请

```sh
krun org get --org platform
krun org permissions --org platform
krun org update --org platform --name "平台工程"
krun org update --org platform --allowed-source SOURCE_ID
krun groups update --org platform --id GROUP_ID --name "值班团队" --revision 1
```

`--org` 接受组织短名或 ID，可以省略以使用当前配置的默认组织。`org permissions` 返回当前登录身份在组织全局范围内的有效权限。检查指定 Worker 和环境下的动作时，可用 `krun check --kind worker --id platform/workers/edge-api --action worker.publish --env production`。

`org update` 保留本次未指定的字段。重复传入 `--allowed-source` 可以指定多个允许的登录来源，这些值会替换整个列表。登录来源使用来源 ID，可以从 `krun whoami` 的 `user.source_id` 查看当前账号的来源。使用 `--clear-allowed-sources` 恢复允许所有已启用的来源。变更导致最后一位有效组织管理员失去访问时，更新会被拒绝。更新默认使用读到的组织修订号，也可以用 `--revision` 指定预期修订号。用户组改名需要传入 `krun groups list` 返回的 `id` 和当前 `revision`。遇到修订冲突时，先重新读取并核对当前配置。

组织管理员用 `krun login` 登录后，可以管理邀请链接：

```sh
krun org invitations create --org platform --role viewer --target-email teammate@example.com
krun org invitations list --org platform --limit 20
krun org invitations list --org platform --limit 20 --cursor NEXT_CURSOR
krun org invitations revoke --org platform --id INVITATION_ID
```

邀请默认授予 `member` 角色，有效期为 7 天，允许 1 位新成员加入。可以用 `--max-uses` 指定 1 到 100 位新成员，也可用 `--target-user` 指定本地用户 ID。指定用户与已验证邮箱只能二选一。完整的 `invite_url` 仅在创建时返回，请只发给受邀者。受邀者在浏览器中打开链接，登录后确认加入。列表返回元数据和 `next_cursor`，可用其中的邀请 ID 撤销链接。

## 查看组织和资源

```sh
krun org list
krun members list --org ORGANIZATION_ID
krun groups list --org ORGANIZATION_ID
krun roles list --org ORGANIZATION_ID
krun keys list --org ORGANIZATION_ID
krun workers list --org ORGANIZATION_ID
```

将 `ORGANIZATION_ID` 替换为 `krun org list` 输出中的组织短名或 ID。创建组织可用 `krun org create --name NAME --short-name platform` 指定短名。使用 `krun workers get --id platform/workers/image-resize` 可以按完整路径读取 Worker。

Worker 使用 `workers` 命令管理，`WORKER_ID` 来自创建或列表输出中的 `id`：

```sh
krun workers create --org ORGANIZATION_ID --name NAME
krun workers get --org ORGANIZATION_ID --id WORKER_ID
krun workers update --org ORGANIZATION_ID --id WORKER_ID --name NAME --revision 1
krun workers delete --org ORGANIZATION_ID --id WORKER_ID
krun build logs --org ORGANIZATION_ID --worker WORKER_ID --build BUILD_ID --follow
```

更新时使用当前资源的 `revision`。授权给指定 Worker 时使用 `--scope-kind worker --scope-id WORKER_ID`；API Key 的资源类型为 `worker`，动作包括 `worker.read`、`worker.publish` 和 `worker.version.read`。构建与日志分别使用 `build.start`、`log.read`，资源类型同样为 `worker`。Worker 的审计动作使用同一套名称。

查看审计记录：

```sh
krun audit --org ORGANIZATION_ID --limit 50
```

## 构建并部署 Worker

先用 `krun config set org ORG_ID` 选定组织。`--env` 选择 Worker 的目标环境。已绑定 Git 仓库的 Worker 可以直接构建并部署：

```sh
krun deploy --worker WORKER_ID --env production
```

默认构建仓库绑定中的 Git ref，也可以用 `--ref` 指定分支或提交。CLI 会等构建成功再提交发布，并等待运行时确认。`--build-timeout` 限制等待构建的时间，`--timeout` 限制等待发布确认的时间。`--wait=false` 仍会等待构建完成，提交发布后即返回。

要从本地源文件构建，先得到 Build ID，再通过 `--build` 复用。将下面的 `WORKER_ID` 替换成 `workers create` 返回的 `id`。

```sh
krun workers create --name hello-worker
krun build start --worker WORKER_ID --env production --file ./index.js
krun build logs --worker WORKER_ID --build BUILD_ID --follow
krun build wait --worker WORKER_ID --build BUILD_ID
krun deploy --worker WORKER_ID --env production --build BUILD_ID
krun workers invoke --worker WORKER_ID --env production --path /
```

`build start` 返回构建 ID，`build wait` 在构建失败时以非零状态退出。使用返回的操作 ID 执行 `deployments wait --worker WORKER_ID --operation OPERATION_ID`，可以继续等待。用 `deployments status` 查询目标环境当前提交与已确认的版本。

需要先审核版本再发布时，用 `versions upload --worker WORKER_ID --env production` 构建已绑定的仓库并上传版本，或附加 `--build BUILD_ID` 复用已有构建。上传版本不会改变流量。审核后执行 `versions deploy VERSION_ID@100% --worker WORKER_ID --env production`。省略 `@100%` 同样会切换全部流量；当前只支持一个版本。回滚用 `rollback VERSION_ID`，目标必须是在该环境成功发布过的版本。

要预检本地源码，在项目目录执行：

```sh
krun deploy --dry-run --config ./wrangler.jsonc --outdir ./build-preview
```

这会通过 Node.js 和 npm 调用 Wrangler 打包本地工作区，不向 krun 创建构建或发布。省略 `--config` 时由 Wrangler 查找项目文件。它不会检出服务端绑定的 Git ref，也不会验证 krun 的目标环境、权限或绑定。正式发布仍从服务端绑定的仓库取代码。

带静态资产的本地源码可以先按平台要求打成 portable ustar 格式的 `tar.gz`，顶层包含 `src/` 和 `assets/`。使用 `build upload --bundle FILE` 上传，再将返回的 `source_upload` 与 `--entrypoint src/index.ts` 传给 `build start`；需要配置静态资产时传入 `--assets-config FILE`。在本地按 `wrangler.jsonc` 打包并直接上传的项目可继续使用 [Wrangler 部署流程](/docs/guides/cloudflare-workers.html)。

## 从私有仓库构建并发布带 R2 的 Worker

准备已启用 Workers、对象存储和 Git 仓库授权的控制台，以及一个有权创建 Worker、创建桶和读写对象的测试组织账号。按顺序运行下列命令，并用 CLI 返回的 ID 替换示例中的占位符。

### 1. 准备私有仓库

创建一个私有 GitHub 或 GitLab 仓库，将下面两个文件提交并推送到 `main`。这是专用的验收 Worker，仅处理两个测试对象。

`wrangler.jsonc`

```jsonc
{
  "name": "cli-private-r2-e2e",
  "main": "src/index.js",
  "compatibility_date": "2026-09-28"
}
```

`src/index.js`

```js
export default {
  async fetch(request, env) {
    const path = new URL(request.url).pathname;
    const key = path.slice(1);

    if (request.method === "GET" && path === "/cli-e2e/from-cli.txt") {
      const object = await env.FILES.get(key);
      return object
        ? new Response(await object.text(), { headers: { "content-type": "text/plain" } })
        : new Response("Object not found", { status: 404 });
    }

    if (request.method === "PUT" && path === "/cli-e2e/from-worker.txt") {
      const text = await request.text();
      if (!/^worker-to-cli-[0-9a-f]{8}$/.test(text)) {
        return new Response("Invalid test payload", { status: 400 });
      }
      await env.FILES.put(key, text, { httpMetadata: { contentType: "text/plain" } });
      return new Response(null, { status: 201 });
    }

    return new Response("Not found", { status: 404 });
  },
};
```

### 2. 登录并创建 Worker 和逻辑桶

将 `ORG_ID` 换成 `krun org list` 输出中的测试组织 ID。也可用 `krun org create --name "CLI 私库验收"` 创建专用组织。

```sh frame="terminal" title="登录并创建资源"
krun login
krun org list
krun config set org ORG_ID

krun workers create --name private-r2-demo
krun r2 bucket create cli-private-demo
```

从 `workers create` 的输出中复制 `id`，替换后续命令里的 `WORKER_ID`。如果示例桶名已被占用，换一个新名字，并在后续命令和绑定文件中统一使用。

### 3. 授权并绑定私有仓库

先查看当前组织已启用的仓库平台。下方命令使用示例 provider ID `github` 和 `gitlab`，请按 `providers` 的返回值替换。

```sh frame="terminal" title="查看仓库平台"
krun workers repository providers
```

授权时，浏览器需登录与 CLI 相同的 krun 账号。命令会生成一条一次性链接，浏览器打开后自动跳转到 Git 平台的授权页。在远程终端可加 `--browser=false`，再用已登录的浏览器打开终端给出的链接。

#### GitHub 私库

**第一步，授权 GitHub 账号。** 运行下面的命令，浏览器会自动打开 GitHub 授权页。核对 App 名称后，点图中标出的 **Authorize**。GitHub 随后会带你回到 krun。

```sh frame="terminal" title="打开 GitHub 授权"
krun workers repository authorize --provider github
```

<div class="krun-consent-screenshot">
  <a href="/docs/screenshots/git-repositories/github-consent.png">
    <img src="/docs/screenshots/git-repositories/github-consent.png" alt="GitHub 授权确认页，标记 1 指向 Authorize 按钮，账号已遮挡" loading="lazy" />
    <span aria-hidden="true">1</span>
  </a>
</div>

**第二步，检查 GitHub App 的仓库范围。** 执行 `repository list`。能看到目标私库就可以绑定。如果没有，确认当前 GitHub 账号能读取该仓库，并通过 `providers` 返回的 `install_url` 安装 App 或调整其仓库范围。组织可能要求管理员批准。[GitHub App 安装与仓库范围](/docs/guides/github-integration.html#安装或管理-github-app)

```sh frame="terminal" title="查找并绑定 GitHub 私库"
krun workers repository list --provider github
krun workers repository set --worker WORKER_ID \
  --provider github --repository owner/private-worker --ref main --directory . --private
```

#### GitLab 私库

运行下面的命令，浏览器会打开所配置的 GitLab 实例的授权页。确认读取仓库和 API 的权限，点 **Authorize**，然后回到终端。

```sh frame="terminal" title="打开 GitLab 授权"
krun workers repository authorize --provider gitlab
```

<div class="krun-gitlab-consent-screenshot">
  <a href="/docs/screenshots/git-repositories/gitlab-consent.png">
    <img src="/docs/screenshots/git-repositories/gitlab-consent.png" alt="GitLab 授权确认页，标记 1 指向 Authorize 按钮" loading="lazy" />
    <span aria-hidden="true">1</span>
  </a>
</div>

```sh frame="terminal" title="查找并绑定 GitLab 私库"
krun workers repository list --provider gitlab
krun workers repository set --worker WORKER_ID \
  --provider gitlab --repository group/subgroup/private-worker --ref main --directory . --private
```

将示例仓库路径换成你的仓库。GitLab 子组仓库使用完整路径。绑定完成后，确认 CLI 返回的 `authorization_status` 为 `connected`。

### 4. 绑定 R2 并上传测试对象

新建 `bindings.json`，将源码中的 `env.FILES` 绑定到刚创建的桶。

```json title="bindings.json"
{
  "FILES": "cli-private-demo"
}
```

```sh frame="terminal" title="配置 R2 并上传测试对象"
krun workers environments update --worker WORKER_ID --env production \
  --r2-bindings-file bindings.json

printf 'cli-to-worker\n' > from-cli.txt
krun r2 object put cli-private-demo/cli-e2e/from-cli.txt --file from-cli.txt --content-type text/plain
```

上传成功后，CLI 会返回对象的 `key` 和 `size`。

### 5. 构建、发布并确认生效

```sh frame="terminal" title="一键构建与发布"
krun deploy --worker WORKER_ID --env production
```

CLI 会显示构建和发布进度。最后返回的 JSON 中，`state` 为 `available` 表示运行时已确认发布，`version_id` 是本次发布的版本。下面是关键字段示例。

```json
{
  "state": "available",
  "version_id": "VERSION_ID"
}
```

需要核对 Git 提交时，用终端打印的 Build ID 运行 `krun build get --worker WORKER_ID --build BUILD_ID`，查看 `repository_source.commit`。构建失败时，用 `krun build logs --worker WORKER_ID --build BUILD_ID` 查看日志。

### 6. 验证 Worker 与 R2 双向读写

先用 Worker 读取刚才上传的对象。

```sh frame="terminal" title="Worker 读取 CLI 上传的对象"
krun workers invoke --worker WORKER_ID --env production --path /cli-e2e/from-cli.txt
```

输出中的 `status` 应为 `200`，`body` 应为刚上传的内容。

```json
{
  "status": 200,
  "body": "cli-to-worker\n"
}
```

再让 Worker 写入另一个对象，并用 CLI 直接显示对象内容。

```sh frame="terminal" title="CLI 读取 Worker 写入的对象"
printf 'worker-to-cli-a1b2c3d4' > from-worker.txt
krun workers invoke --worker WORKER_ID --env production \
  --method PUT --path /cli-e2e/from-worker.txt --file from-worker.txt
krun r2 object get cli-private-demo/cli-e2e/from-worker.txt --pipe
```

写入命令返回 `status: 201`，读取命令输出 `worker-to-cli-a1b2c3d4`。这条流程已用真实 GitHub 私库验证，两条读写路径的对象内容均逐字节一致。

### 7. 清理测试资源

```sh frame="terminal" title="清理测试资源"
krun r2 object delete cli-private-demo/cli-e2e/from-cli.txt
krun r2 object delete cli-private-demo/cli-e2e/from-worker.txt
krun workers delete --id WORKER_ID
krun r2 bucket update cli-private-demo --enabled=false
```

### 已绑定仓库的 CI 发布

完成首次用户授权和私库绑定后，在 CI 密钥管理中设置 `KRUN_TOKEN`，并设置 `KRUN_ENDPOINT` 为控制台地址。CI 可以直接运行下面的命令，跳过交互式登录。将示例中的组织 ID 和 Worker ID 换成你的资源。

```sh frame="terminal" title="CI 发布"
krun deploy --org ORG_ID --worker WORKER_ID --env production
```

将构建与发布的 API Key 限定到目标 Worker，授予 `worker.read`、`build.start`、`worker.version.read`、`worker.publish`。如需查看构建日志或调用 Worker，再分别授予 `log.read`、`worker.invoke`。对象存储读写另需对象权限。

## 配置 Worker

| 命令 | 用途 |
| --- | --- |
| `workers environments list/get/create/update/delete` | 管理环境。用 `--env NAME` 选择目标，`create` 创建预览环境 |
| `workers previews-base get/update` | 配置新建预览环境继承的默认设置 |
| `workers configurations list/create/deploy` | 保存配置快照，或用指定快照与指定构建或版本发布 |
| `secret list/put/delete/bulk` | 管理环境或预览默认配置中的 Secret |
| `workers domains list/get/set` | 查看域名目录、读取或替换 Worker 域名选择 |
| `workers network list/set` | 查看和选择网络 profile |

更新环境的 `--vars-file` 接收 JSON 或 YAML 对象，`--r2-bindings-file` 接收 binding 名到桶名的映射。提供文件会替换对应的整组配置，空对象清空该组，省略则保留。版本元数据 binding 使用 `--version-metadata NAME`。环境更新用于准备后续版本。

```sh
krun secret put API_TOKEN --org ORG_ID --worker WORKER_ID --env production < ./secret.txt
krun secret list --org ORG_ID --worker WORKER_ID --env production
```

`secret put` 读取标准输入或 `--file` 的内容。已有发布版本时，Secret 修改默认创建并提交新版本，可用 `--stage` 创建版本后暂缓部署。使用 `--previews-base` 修改新建预览环境的默认 Secret。

构建和发布等写操作支持 `--idempotency-key`。遇到响应丢失时，复用标准错误流中打印的键和原始参数重试。如果命令还打印了 revision，重试时传入相应的 `--revision`、`--configuration-revision` 或 `--repository-revision`，以重放同一请求。自动构建的 `deploy` 或 `versions upload` 重试时，复用同一个发布键和原始 Git ref、仓库 revision；CLI 会由该键计算相同的构建键。已经拿到成功的 Build ID 时，也可以用 `--build BUILD_ID` 跳过构建。提交成功并拿到 ID 后，用对应的 `wait` 命令继续检查。

## 对象存储

```sh
krun r2 bucket create uploads --org ORG_ID
krun r2 bucket list --org ORG_ID
krun r2 object put BUCKET_ID/images/logo.svg --org ORG_ID --file ./logo.svg
krun r2 object list --org ORG_ID --bucket BUCKET_ID --prefix images/
krun r2 object head BUCKET_ID/images/logo.svg --org ORG_ID
krun r2 object get BUCKET_ID/images/logo.svg --org ORG_ID --file ./downloaded.svg
```

对象路径的第一段可以是桶名或桶的 `id`，其余部分为完整对象键。用桶名时 CLI 需要读取组织的桶列表。上传可用 `--file` 读取文件，或用 `--pipe` 从标准输入读取。下载可用 `--pipe` 输出原始字节，或用 `--file` 写入文件。覆盖本地文件需显式加 `--force`。使用 `r2 object delete` 删除对象，`r2 object url` 取得短期签名下载地址。使用 `r2 bucket update BUCKET_ID --enabled=false` 停用逻辑桶。

## 调用 API

对于尚无专用命令的操作，可调用有权限的 `/api/v1/` 接口：

```sh
krun api --method GET --path /api/v1/orgs
```

需要请求体时，用 `--file FILE` 提供 YAML 文件。自动化可设置 `KRUN_ENDPOINT` 和 `KRUN_TOKEN`；Token 使用 krun API Key，权限仍受 Key 策略和创建者权限限制。不要把凭据提交到仓库。

运行 `krun --help` 查看命令组，在具体命令后加 `--help` 查看参数。
