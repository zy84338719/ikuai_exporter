# Changelog

## v1.1.2

真机部署验证时发现的两个**字段映射错误**。两者都自 v1.0.1 起就存在且从未被测出——
因为单测 fixture 抄的是代码的假设，而不是设备的真实响应。

### Fixed

- **`ikuai_memory_used_kibibytes` 恒等于 total（100% 内存被报成已用）。**
  固件在 `sysinfo.memory` 里给的是 `available`，代码读的是 `avail`；后者不存在，
  于是拿到 0，`used = total - 0 = total`。现在两种拼写都解码。
  注意固件自己的 `used` 字段是百分比字符串（`"86%"`）而非字节数，不能直接取，
  因此仍由 `total - available` 推导，并对负值做了下界保护。

- **`ikuai_version_info` 从未被输出。** 固件给的是 `verstring` / `modelname`（无下划线），
  代码写的是 `ver_string` / `model_name`，导致 `VerString` 恒为空、该指标被条件跳过，
  Grafana 的版本面板一直是空的。现在两种拼写都解码。

### Tests

- 单测 fixture 换成**真机（iKuai 4.0.313）的真实响应**，而非此前的臆测字段名——
  这正是两个 bug 能长期存活的原因。
- 新增 `TestSystemMetricFieldNames`：断言 `used == total - available`、
  `version_info` 存在，并显式检查 `used != total`。
- 新增 `TestSystemAcceptsUnderscoredAliases`：保证旧的下划线拼写同样可用，
  兼容不同固件版本。

## v1.1.1

跟进 oekit 模块升级，并修掉一个日志流不一致。

### Changed

- **oekit 依赖跟进最新版**：`concurrency` v0.5.4 → **v0.12.0**、
  `logx` v0.6.0 → **v0.7.0**、`pkg` v0.11.0 → **v0.13.0**、
  `core` v0.6.0 → **v0.8.0**（`bootstrap` v0.6.2 已是最新）。
  已验证 `core` v0.8.0 与 `bootstrap` v0.6.2 组合下优雅关停行为不变：
  逆序关闭与全 JSON 日志均正常。
- `metrics/collector.go` 中解码失败改用 `oekerrors.New`。
  `pkg` v0.12.0 起 `errors` 包补齐 `New`/`As`/`Is`/`Unwrap`，与 stdlib 同型，
  此前为绕开缺口而使用的 `fmt.Errorf` 不再必要——「同一进程只允许一套
  errors 包」的红线现在可以完整执行。

### Fixed

- **handler panic 不再污染日志流。** net/http 自行 recover 后把 panic 通过
  `Server.ErrorLog` 输出，默认走 stderr 纯文本，会插进已经统一成 JSON 的日志流里。
  现通过 `slog.NewLogLogger(logx.SlogHandler(...))` 接入 logx，panic 与 TLS
  握手错误都变成结构化事件。

## v1.1.0

升级到 ikuai-api **v1.2.0** 并按 [oekit](https://oekit.github.io) 工程契约重构。

### Fixed

- **`ikuai_devices_online_total` 少计在线设备。** `/monitoring/clients-online`
  在 v4 目录里被标为 `paged`，而 exporter 此前调用的是无分页的
  `GetMonitoringClientsOnline`——它不带任何 `limit`，只会拿到路由器默认的那一页。
  现在改用 `QueryMonitoringClientsOnline` 显式按 `limit`+`page` 逐页拉取，
  `devices_online_total` 才是真正的在线总数。ikuai-api v1.2.0 修好了分页键
  （此前 SDK 发的是路由器会静默忽略的 `page_size`），这次升级正好让分页可用。
- 首屏后不再出现「部分指标」的静默截断：在线设备列表拉取失败时，
  首屏失败不再输出 `devices_online_total`，翻页中途失败则保留已取到的页。

### Changed

- **日志改为 `logx` kv 结构化风格**（原先是 stdlib `log.Printf`）。
  `main` 中 `logx.UseSlog()` 选择 slog JSON 后端；ikuai-api 的包级 logger
  通过 `logx.Delegating` 懒解析，会自动跟随同一后端，不再各自为政。
  路由器返回的错误现在会带上 iKuai 业务码（`APIError` 自 v1.2.0 内嵌 `BizError`）。
  另将 `slog.SetDefault` 指向同一个 logx handler：`oekit/core` 的 lifecycle
  manager 为保持零依赖直接走 `slog.Default()`，而 `bootstrap` 没有暴露注入通道，
  不接管的话优雅关停阶段的日志会以 slog 默认文本格式混进 JSON 流里。
- **错误处理统一到 `oekit/pkg/errors`**，遵守「同一进程只允许一套 errors 包」的
  类型同一性红线；原 stdlib `errors.Is` 换成 `errors.In`。
- **`main.go` 改用 `oekit/bootstrap` 组合根**，信号监听、资源逆序关闭、
  后台任务收尾交给框架；监听改为先 `net.Listen` 再 `app.Ready()`，
  端口占用会在启动时直接失败，而不是进入「已就绪但没在服务」的状态。
- **新增优雅关停**（此前收到 SIGTERM 直接被杀，进行中的 scrape 会被截断）。
  关停顺序：停止对外服务 → 等待在途请求 → `client.Close(ctx)`。
  新增 `-shutdown-timeout`（默认 15s），`deployment.yaml` 配套
  `terminationGracePeriodSeconds: 30`。
- `/live`、`/healthz` 不再触碰路由器（行为不变，但明确了与 `/ready` 的分工）。
- 改用独立 `http.ServeMux`，避免 `/` 兜底路由遮蔽后续新增的探针路径。
- `http.Server` 增加 `ReadHeaderTimeout`，防止慢连接长期占用 goroutine。
- 清理 `metrics/collector.go` 中遗留的 gofmt 未格式化问题。

### Added

- 分页与响应包裹形态的回归测试：多页遍历（断言请求带 `limit` 且不带
  `page_size`）、`data` / 裸数组 / `<group>_data` 三种包裹形态、首屏失败与
  翻页中途失败的降级行为、`/ready` 的 context 超时。
- `make check`（gofmt + vet + `go test -race`），CI 同步补上
  格式检查与 `-race` 测试步骤——此前 CI 只 build + vet，不跑测试。
- CI 增加第二个 job，按镜像的方式（`GOWORK=off` + 纯 proxy 解析）构建静态
  二进制，确保「CI 过的构建」就是「发布镜像的构建」。
- `docker-compose.yml` 增加 `stop_grace_period: 30s`，并移除 Compose v2 已废弃的
  `version: "3.8"`。

### Dependencies

- `github.com/zy84338719/ikuai-api` v1.0.1 → **v1.2.0**（本地 pin 与
  Docker 镜像构建的 proxy 版本同时更新）。此前 go.sum 缺少 v1.2.0 引入的
  `oekit/*` 依赖，实际处于构建不过的状态，本次一并修复。
- 新增 `oekit/bootstrap`、`oekit/logx`、`oekit/pkg`、`oekit/concurrency`。
  bootstrap 是零重依赖编排壳，未引入 hertz——二进制仅从 11.21MB 增至 11.27MB。
- **移除 `replace github.com/zy84338719/ikuai-api => ../ikuai-api`。**
  此前同一条依赖有三条解析路径且互不一致：本地和 CI 经 replace 用工作区里的
  SDK（CI 拉的是 `main` 分支，可能领先于 tag 构建出未发布代码），Docker 构建
  却 drop 掉 replace 从 proxy 取已发布版。现在 `go.mod` 锁定已发布版本，
  本地 / CI / 镜像三者一致。本地联调改用**不入库**的 `go.work`（已加进
  `.gitignore`），既保留「改 SDK 立即生效」，又不再污染模块声明。
- **新增 `.dockerignore`。** `COPY . .` 会把本地 `go.work` 拷进构建上下文，
  而镜像里不存在 `../ikuai-api`，会导致构建失败；Dockerfile 另加
  `ENV GOWORK=off` 兜底。
- **Dockerfile 简化**：移除已无意义的 `go mod edit -dropreplace` + `go get` 步骤
  （依赖版本现在就在 `go.mod` 里）。

## v1.0.1 — 2026-08-01

Field-mapping fixes verified against a live iKuai 4.0.303 router.

### Fixed
- `/monitoring/system`: read memory from the `memory` key (was `mem`); the
  payload nests under `sysinfo`, already unwrapped.
- `/monitoring/clients-online`: peel the inner `{"data":[...]}` wrapper (was
  unmarshalling the wrapper object as a bare array, yielding zero devices).
- `/monitoring/connections` is a timeseries, not a summary — dropped.
  Connection counters (`ikuai_connections_total`, `ikuai_connections{protocol}`)
  now come from `sysinfo.stream` (a real-time point value).

Verified live: CPU (9 cores), memory, connections (tcp/udp/icmp), 11 online
devices, wan/lan interfaces with traffic.

## v1.0.0 — 2026-08-01

### Changed
- **v4-only**: dropped the v3 username/password (`/Action/login`) code path.
  The exporter now speaks a single mode — v4 REST over HTTPS with a personal
  Bearer token (`-token`). `-username`/`-password` are rejected.
- Migrated onto `ikuai-api` v1.0.1+ (v4-only SDK); the heavy `imroc/req` +
  `quic-go` transitive dependencies are gone (the SDK is now stdlib-only).
- `metrics.NewCollector` signature changed to `NewCollector(ns, router,
  client)`; every series carries a `router` label so one Prometheus job can
  distinguish many targets.
- Collapsed the old v3 session collector and v4 collector into a single v4
  collector built on `service.APIClient.Monitoring()`; responses are decoded
  from `json.RawMessage` into local structs (the SDK ships no typed models).

### Added
- `ikuai_connections_total` and `ikuai_connections{protocol}` metrics from
  `/monitoring/connections`.
- `-router-label` flag (defaults to the `-router` host).
- Health probes: `/live` (process up) and `/ready` (real router probe, 503
  when unreachable) for kubelet; `/healthz` kept as a `/live` alias.
- k8s readinessProbe now uses `/ready`.
- `metrics/collector_test.go` — first test coverage (JSON parsing, router-down
  degradation, helpers).

### Removed
- All v3 username/password deployment remnants (k8s secret/deployment,
  docker-compose).
