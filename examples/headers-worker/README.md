# headers-worker

一个最小但完整的 krun Worker 示例：

- `/`、`/style.css`、`/app.js` 由**静态资产层**直接返回；
- `/headers` 由 **Worker** 处理，回显这次请求收到的全部 HTTP header、请求元信息、`request.cf` 和 Worker 环境变量；
- 页面里的“发送自定义 header”表单会再发一次 `fetch('/headers')`，用来验证网关 → 入口 Worker → 业务 Worker 的 header 透传。

## 文件

```text
src/index.ts        Worker 代码（/headers）
public/index.html   页面
public/app.js       页面逻辑（fetch /headers、表格渲染、敏感 header 遮罩）
public/style.css    样式
package-source.sh   生成 source.tar.gz
```

## 构建配置

在控制台创建 Worker 后，构建时使用：

```json
{
  "environment": "production",
  "entrypoint": "index.ts",
  "assets": {
    "directory": "public",
    "run_worker_first": ["/headers"],
    "not_found_handling": "none",
    "html_handling": "auto-trailing-slash"
  }
}
```

**entrypoint 是 `index.ts`，不是 `src/index.ts`。** 源码包的顶层固定是 `src/` 和 `assets/`；builder 会去掉 `src/` 前缀再编译。只有旧的内联 `files` 模式才使用 `src/index.ts`。

## 打包

```bash
./package-source.sh
```

生成 `source.tar.gz`：

```text
src/index.ts
assets/index.html
assets/app.js
assets/style.css
```

tar 里**不能有目录条目**（`src/`、`assets/` 本身），builder 只接受普通文件条目。`package.sh` 通过只把 `find -type f` 的结果传给 tar 来避免这个问题。

## 通过控制台 / API 发布

控制台如果使用仓库构建：把 `src/` 和 `public/` 提交到仓库，入口填 `index.ts`，资产目录填 `public`，`run_worker_first` 填 `/headers`。

如果直接用源码包 API：

1. `POST /api/v1/orgs/{org}/services/{service}/workers/source-uploads` 领取 `{source_upload, upload_url}`。
2. 把 `source.tar.gz` PUT 到 `upload_url`。URL 指向集群内 MinIO 时，需要从能访问 MinIO 的位置执行（例如 kind 的 tester Pod 或 port-forward）。
3. `POST .../workers/builds`，body 使用上面的构建配置，并带上 `source_type: "upload"` 和 `source_upload`。
4. 轮询 `GET .../workers/builds/{id}` 到 `succeeded`。
5. `GET .../workers/environments` 取 production 的 `release_revision`。
6. `POST .../workers/environments/production/releases`，body 为 `{build_id, expected_revision, idempotency_key}`。
7. 轮询 `GET .../workers/operations/{id}` 到 `available`。
8. 打开 production 的 host（形如 `w-xxxxxxxx-production.workers.test`），或者用控制台的 invoke 调用 `/headers`。

## 验证点

- `GET /` 返回页面，响应头带 `x-krun-release` / `x-krun-pod`；
- `GET /headers` 返回 JSON，`headers["x-demo"]` 能看到页面表单里发送的自定义 header；
- `environment.KRUN_ENVIRONMENT` / `KRUN_SERVICE_ID` / `KRUN_RELEASE_ID` 来自当前 Release；
- 页面默认遮罩 `authorization`、`cookie` 等敏感 header，勾选“显示敏感 header”后可见。
