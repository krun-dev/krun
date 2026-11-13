---
title: 灰度发布
description: 让同一个 Worker 的两个版本按百分比分担流量，验证候选版本后逐步加量。
---

同一个 Worker 环境最多可同时部署两个版本。域名仍指向同一个资源池，每次请求由运行时按当前比例选版。可以先让候选版接收 0% 普通流量，用授权请求验证，再逐步加量。

| 步骤 | 稳定版 | 候选版 |
| --- | ---: | ---: |
| 定向验证 | 100% | 0% |
| 开始灰度 | 95% | 5% |
| 晋级 | 退出部署 | 100% |

## 在控制台发布

1. 打开目标 Worker，选中已有部署的环境。在「构建与发布」中，从候选构建的更多操作里选「创建待部署版本」。
2. 打开「版本与部署」，找到候选版本，点「部署此版本」，填写「此版本流量百分比」。输入 `0` 可以先做定向验证，输入 `5` 会把 5% 流量交给候选版。
3. 等发布状态显示生效后，在当前部署查看版本和比例。需要晋级时重新部署候选版并填 `100`。需要结束灰度并恢复全部流量时，在「版本与部署」重新部署稳定版，确认它接收 100%。

## 从终端发布

准备同一 Worker 环境的两个已上传版本。从 `versions list` 取得版本 ID，将 `STABLE_VERSION` 和 `CANDIDATE_VERSION` 设为对应的 ID。百分比合计 100，支持 0% 和最多三位小数，最小非零比例为 0.01%。

### krun CLI

安装 krun CLI，使用浏览器登录，或在 CI 设置 `KRUN_ENDPOINT` 和 `KRUN_TOKEN`。把 `ORG_ID`、`WORKER_ID` 换成实际 ID。所需权限、API Key 策略和上传步骤见 [krun CLI 灰度指南](/docs/guides/krun-cli.html#双版本灰度)。

```sh
krun versions deploy "${STABLE_VERSION}@100%" "${CANDIDATE_VERSION}@0%" --org "$ORG_ID" --worker "$WORKER_ID" --env production --yes
krun versions deploy "${STABLE_VERSION}@95%" "${CANDIDATE_VERSION}@5%" --org "$ORG_ID" --worker "$WORKER_ID" --env production --yes
krun deployments status --org "$ORG_ID" --worker "$WORKER_ID" --env production
```

晋级时执行 `versions deploy`，只传候选版本和 `@100%`。回滚时执行 `krun rollback "$STABLE_VERSION" --org "$ORG_ID" --worker "$WORKER_ID" --env production --yes`。默认等待操作进入 `available`。

### Wrangler

准备 Node.js 和 npm，在 Worker 项目目录运行。使用 [Wrangler 连接配置与 API Key 策略](/docs/guides/cloudflare-workers.html#1-准备账号)，使 Wrangler 指向 krun。

```sh
npx wrangler@4.155.0 versions deploy "${STABLE_VERSION}@100%" "${CANDIDATE_VERSION}@0%" --yes
npx wrangler@4.155.0 versions deploy "${STABLE_VERSION}@95%" "${CANDIDATE_VERSION}@5%" --yes
npx wrangler@4.155.0 deployments status
```

晋级时只传候选版的 `@100%`。回滚时执行 `npx wrangler@4.155.0 rollback "$STABLE_VERSION" --yes`。三种入口使用同一发布记录。

## 验证实际命中的版本

需要 curl、目标环境的 `WORKER_URL`、Worker 定位符 `WORKER_NAME`，以及有该环境 `worker.publish` 权限的 `KRUN_TOKEN`。先确认候选版已进入部署列表，再请求正式域名：

```sh
curl -fsS -D - -o /dev/null \
  -H "Authorization: Bearer $KRUN_TOKEN" \
  -H "Cloudflare-Workers-Version-Overrides: $WORKER_NAME=\"$CANDIDATE_VERSION\"" \
  "$WORKER_URL"
```

预期业务返回成功状态，响应头 `X-Krun-Version` 等于候选版本 ID。若收到 HTTP 403（`version override denied` 或 `version override unavailable`），检查凭据及目标环境发布权限后重试。业务也需要 `Authorization` 时，把 krun 发布凭据放在 `Krun-Workers-Verify-Authorization`。指定版本离开部署列表后，请求按当前权重选版。

要让同一会话稳定命中一个版本，在发布比例不变时连续发送相同的 `Krun-Workers-Version-Key`，并比较返回的 `X-Krun-Version`。也可用 `Cloudflare-Workers-Version-Key`，两个头同时出现时以 krun 为准。普通请求按当前比例随机选版。

## 缓存和静态资源

共享响应缓存按实际命中的版本隔离，鉴权和定向验证请求绕过缓存。退场版本的哈希静态资源可在保留窗口内继续读取。平台默认保留 24 小时，可按平台和环境配置。

发布后查看 [Workers 观测](/docs/guides/workers-observability.html) 中两个版本的请求数、错误率和延迟。最终晋级或回滚后，确认部署列表只剩预期版本，且普通请求的 `X-Krun-Version` 与该版本 ID 一致。
