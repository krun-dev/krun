# workerd 执行池实验

在同一个 4 核 8 GiB 容器里运行 Rust/Pingora 和多个常驻 workerd。Rust 分别为普通和计算请求选执行位置，优先复用热实例。本地无法接单时，入口可以单跳转发到健康的同池容器，目标只做本地准入。workerd 使用 Dynamic Worker Loader 按需装载合成业务。

这是通过 `cargo build` 和实验脚本主动运行的独立原型。实验按测试域名区分执行组。纯轻量发压在客户端固定每个域名的入口，多容器实验用同机 loopback 和独立 cgroup。

## 复现

需要 Linux AMD64、cgroup、nerdctl、Rust 及 C/C++ 构建工具、Go、Python 3 和 GitHub CLI。容器镜像默认使用 `docker.io/library/node:22-bookworm`，可用 `BENCH_IMAGE` 指定提供相同运行依赖的镜像。以下命令在仓库根目录执行。

```bash
go build -o experiments/workerd-execution-pool/light-bench ./experiments/workerd-execution-pool/loadgen
cd experiments/workerd-execution-pool
gh release download v1.20261116.1-krun.3 --repo krun-dev/workerd --pattern workerd-linux-64.gz
gzip -dc workerd-linux-64.gz > workerd-krun.3
chmod +x workerd-krun.3
cargo build --release --locked
cargo test --locked
```

20 入口、64 个轻量域名的在途与冷却对照。每组启动独立的 20 容器，先用 1 万 QPS 热身，然后逐档发压。每行参数只需替换 `ordinary` 和 `cooldown` 两个值。脚本退出时回收本轮容器。

```bash
for preset in '16 15' '32 15' '32 5' '16 5'; do
  read -r ordinary cooldown <<< "$preset"
  GOMAXPROCS=32 python3 run-routing.py --mode routing --pods 20 --compute-processes 3 \
    --light-load-bin ./light-bench --spread-entries \
    --cpu-high 750 --cpu-reserve 1000 --sample-ms 150 --peer-selection capacity \
    --in-flight 2 --ordinary-in-flight "$ordinary" --peer-in-flight 4 \
    --peer-ordinary-in-flight 16 --cooldown-ms "$cooldown" --retry-ms 100 \
    --warmup-seconds 8 --seconds 10 --sweep-rps 10000 40000 60000 80000 100000 \
    --results "/tmp/krun-flight-${ordinary}-cooldown-${cooldown}.json"
done
```

对照两个和三个计算 workerd。每次计算请求执行 2,200 万轮同步整数计算，直连基线平均约 36.34 ms CPU。

```bash
for processes in 2 3; do
  python3 run-routing.py --mode routing --pods 20 --compute-processes "$processes" \
    --cpu-high 750 --cpu-reserve 1000 --sample-ms 150 --peer-selection capacity \
    --in-flight 2 --peer-in-flight 4 --cooldown-ms 15 --retry-ms 100 \
    --light-rps 400 --seconds 10 --sweep-rps 550 700 950 \
    --results "/tmp/krun-compute-${processes}.json"
done
```

`succeeded` 和 `failures` 分别核对成功数与失败类型。`bySlot` 用 Pod 和固定槽位统计承流进程。压测端未发出的请求单独归类。P99 只统计成功请求。

## 已测容量与配置

- 轻量请求平均使用约 0.30–0.33 ms workerd 主线程 CPU。20 个入口下，2 万 QPS 零 503 且采样未发现槽位触及 0.75 核。2.5 万 QPS 零 503，但已有槽位短时触线。
- 输入 4 万 QPS 的一轮 30 秒测试成功 39,961 QPS，P99 为 14.20 ms，共 1,171 个 503。普通组平均 CPU 为 12.2 核，主要拒绝原因包括在途额度和候选冷却。
- 普通在途 32、远端冷却 15 ms 时，输入 10 万 QPS 的两轮 30 秒测试成功 90,736–91,759 QPS，P99 为 20.37–20.70 ms，503 为 8.2%–9.2%。在同轮 10 秒对照里，在途从 16 提到 32 让 503 降 37%，P99 增加 9.83 ms。冷却降到 5 ms 后，远端执行拒绝从约 10.9 万涨至约 25.9 万次。
- 输入 950 RPS 重计算及 400 RPS 普通流量时，计算进程从每 Pod 2 个增加到 3 个，成功吞吐由 683 升为约 940 RPS。三个计算进程的 700 RPS 计算档持续 60 秒零失败。
- 固定总计 3,080 RPS 的混压里，把部分约 8 ms 的计算请求替换为约 36 ms 的请求，计算槽位 CPU 由 14.8 核升至 35.3 核，最后一档出现 449 个计算请求 503。固定的普通及模拟 I/O 请求均成功。

[压测结果摘要](benchmark-results.json) 使用合成负载。容量试算先按每 Pod 1,200 个同类轻量 RPS，计算按入口 RPS 乘实际每调用 CPU 秒数估算。把 `2+2` 的每 Pod 计算预算暂取 0.73 核，`2+3` 暂取 1.0 核。分别计算普通、计算和热驻留内存所需副本，取最大值再加故障余量。以目标 P99、最终错误率和混合负载下的故障复测校准预算。

| 配置 | 对照值 | 作用 |
|---|---:|---|
| `POOL_ORDINARY`、`POOL_COMPUTE` | 2、2 或 3 | 两类常驻进程数 |
| `POOL_ORDINARY_IN_FLIGHT`、`POOL_COMPUTE_IN_FLIGHT` | 16、2 | 每 workerd 在途额度，未配置时继承 `POOL_IN_FLIGHT` |
| `POOL_PEER_ORDINARY_IN_FLIGHT`、`POOL_PEER_COMPUTE_IN_FLIGHT` | 16、4 | 每入口对一个目标 Pod 的分组额度，未配置时继承 `POOL_PEER_IN_FLIGHT` |
| `POOL_CPU_HIGH_MCPU`、`POOL_CPU_RESUME_MCPU` | 750、525 | 单 workerd CPU 暂停和恢复投递的水位 |
| `POOL_CPU_RESERVE_MCPU`、`POOL_MEMORY_RESERVE_MIB` | 1000、1639 | 容器的 CPU 和内存准入预留 |
| `POOL_PEER_COOLDOWN_MS` | 15 | 收到执行前容量拒绝后的候选冷却 |

## 监测与排查

- 按 `execution_group`、`workerd_slot` 看 `krun_execution_slot_cpu_high` 和 `krun_execution_slot_in_flight / krun_execution_slot_in_flight_limit`。少数热槽持续触线且同池有空位时，可以为热点增加候选。
- 用 `krun_execution_route_admission_rejections_total` 按 `listener`、`execution_group`、`reason` 对比入口和目标。短时间增长的 `workerd_in_flight`、`peer_in_flight` 或 `peer_cooldown` 可以帮助定位在途和选点压力。同一请求可命中多个原因，最终错误率按入口响应统计。
- 比较 `krun_execution_pod_memory_bytes` 与 `krun_execution_pod_memory_admission_ceiling_bytes`。多个 Pod 的内存持续接近准入线，同时 `cold_load_memory` 增长时，核对版本副本和淘汰速度。`cold_load_in_flight` 反映冷加载突发并发压力。
- 对按 Ready Pod 数量折算的业务容量设置趋势预警。例如扣掉故障余量后，持续达到规划预算的 85% 提醒，超过规划预算且多个 Pod 的同组有压力时扩 Pod。入口 P99 和最终错误率单独按业务 SLO 告警。以上阈值按压测样本校准。

复测执行前拒绝后的 GET 换点、POST 保持拒绝和禁止多跳转发可使用 `python3 run-routing.py --mode loop-check --peer-selection capacity --cooldown-ms 15 --results /tmp/krun-loop-check.json`。

[Grafana 大盘](grafana.json) 可导入已采集实验 Rust /metrics 的实例。导入时选择
Prometheus 兼容数据源，并在采集时附加 `namespace` 与 `pod` 标签。面板可按执行组和
workerd 槽位筛选，展示 CPU 水位、在途占比、装载和带原因的拒绝。
