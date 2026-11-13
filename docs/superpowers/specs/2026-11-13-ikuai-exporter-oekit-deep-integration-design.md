# ikuai_exporter × ikuai-api v1.3.4 × oekit 深度接入 — 设计

日期：2026-11-13
状态：已获用户批准（brainstorming 问卷）
范围：ikuai-exporter（本仓）+ ikuai-api 发布链（同级仓，push 前需再次确认）

## 背景与现状

- 本仓 go.mod：ikuai-api v1.3.3（= 远端最新 tag）、oekit bootstrap v0.7.0 / concurrency v0.31.0 / logx v0.8.12 / pkg v0.41.0（core v0.13.5 间接）、go 1.24.0。
- 同级 ikuai-api 仓：本地 HEAD 领先 origin/main 1 commit（oekit 升级），另有 8 文件未提交 WIP（Retry-After 溢出修复、CJK 表格列宽修复、oekit pkg v0.46.0/logx v0.10.0），`go test ./...` 全绿。
- 本仓 go.work（gitignore，不入库）把 ../ikuai-api 接进本地构建，但写着 `go 1.24.0`，而 ikuai-api 要求 `go >= 1.26.0` —— 当前 workspace 模式下 go 命令报错。
- 用户决策（问卷）：① ikuai-api 走「提交 WIP → 打 v1.3.4 → push → exporter 升级」；② oekit 取深度档。

## 关键 API 事实（已核实，版本为设计时缓存）

- `metrics.Init(serviceName)`：一次性注册 go/process collector、`app_build_info`、`app_start_time` 到包级导出的 `metrics.Registry`（`prometheus.NewRegistry()`）；重复调用幂等，首调 service 名即定。
- `httpx.NewRoundTripperE(service, opts ...ClientOption) (http.RoundTripper, error)`：net/http 桥接层（#8）；无 BaseURL/Endpoints 时目标 = 请求自身 scheme://host（对 ikuai-api 透明）；请求体/响应体全量缓冲；重试缺省关闭；熔断拒绝返回裸 `ErrCircuitOpen`；`Close()` 释放底层资源。
- `httpx.WithMetrics(reg prom.Registerer, namespace string)`：nil 显式回退 `metrics.Registry`；请求指标 + resilience 弹性指标族同 Registry 同 namespace。
- `httpx.WithCircuitBreaker(name string, settings gobreaker.Settings)`（sony/gobreaker v1.0.0）：5xx 计熔断失败但不外抛（#107）；状态迁移与拒绝回调写入 resilience 指标。
- `httpx.WithTLSSkipVerify()`（options.go:185）：传输层跳过证书校验。
- `ikuaiapi.WithInsecureSkipVerify(skip)` 内部类型断言 `c.HTTPClient.Transport.(*http.Transport)`（client.go:79）—— transport 被换成 httpx roundTripper 后该 option **静默失效**。
- `ikuaiapi.WithHTTPClient(h *http.Client)` 可注入自定义 client；本地 WIP 的 NewClient 在 option 应用后把 Transport 包一层 `retryAfterTransport{httpjson.RetryAfterTransport{Base: ...}}`（Close 转发保留）。
- `core/config.LoadFile(path, dst, opts ...LoadOption)`：任意路径 YAML 读入，与 LoadBootstrap 同一解码线（default/env/require tag 校验）；`config.ApplyEnvOverlay(dst)`：env 覆盖文件值（含显式零值），12-factor 形态。`LoadBootstrap` 走 etc/config.yaml 查找、文件缺失即错 —— 不适用本仓 flags-first 部署。
- `syncx.FanInLimitCtx(ctx, n, limit, fn func(ctx, i) (T, error)) []Slot[T]`（concurrency v0.35.x）：ctx 感知扇出，未启动项不调 fn。
- `gobreaker.Settings`：MaxRequests / Interval / Timeout / ReadyToTrip / IsSuccessful / OnStateChange。

## 设计（8 节，已获批）

### 1. ikuai-api 发布链

1. ikuai-api 仓提交 8 文件 WIP；CHANGELOG 的 Unreleased 条目归入 v1.3.4 段。
2. 打 tag `v1.3.4`；push main + tag。**push 是外部可见动作，实施前单独向用户确认。**
3. 本仓 `GOWORK=off go get github.com/zy84338719/ikuai-api@v1.3.4` —— GOWORK=off 保证取远端 tag 而非本地 go.work。

### 2. 依赖升级

- go.mod `go` 1.24.0 → 1.26.0（ikuai-api 硬要求）；go.work 同步改 1.26。
- oekit 升到设计时最新：bootstrap v0.7.10、concurrency v0.35.x、logx v0.10.0、pkg v0.47.1、core v0.15.2；新增 metrics v0.3.1、httpx v0.11.2（实施时以 `go get -u` 实解析为准）。
- Dockerfile `golang:1.24-alpine` → `golang:1.26-alpine`；CI 走 `go-version-file: go.mod` 自动跟随，无需改。

### 3. oekit/metrics 门面

- 启动早期 `metrics.Init(namespace)`。
- 删私有 `prometheus.NewRegistry()`，`/metrics` 改为 `promhttp.HandlerFor(metrics.Registry, ...)` —— 本仓 collector、go/process、build info、httpx 弹性指标同处一 registry。
- `registry.MustRegister(collector)` → `metrics.Registry.Register(collector)`（重复注册错误按常规上抛处理）。

### 4. httpx 传输层 + 熔断

构造：

```go
rt, err := httpx.NewRoundTripperE(namespace,
    httpx.WithMetrics(metrics.Registry, namespace),
    httpx.WithTarget(routerLabel),
    httpx.WithCircuitBreaker(breakerName, breakerSettings(cfg)),
    httpx.WithTLSSkipVerify(),          // 仅 insecure=true 时加
    // 不设 BaseURL/Endpoints：目标随请求自带 host，对 ikuai-api 透明
)
client, err := ikuaiapi.NewClient(httpsAddr,
    ikuaiapi.WithToken(token),
    ikuaiapi.WithHTTPClient(&http.Client{Transport: rt}),
    // ikuaiapi.WithInsecureSkipVerify 不再使用（见 TLS 陷阱）
)
```

- **TLS 陷阱**：替换 transport 后 `WithInsecureSkipVerify` 静默失效，skip-verify 由 `httpx.WithTLSSkipVerify()` 承担；`insecure=false` 时两者都不设，证书校验保持开启。
- **重试单源**：ikuai-api retry engine 不动；httpx 重试缺省关闭（`WithRetryConfig` 不设），避免双重退避。
- **熔断参数**：`ReadyToTrip` 按连续失败 ≥ 阈值触发（避开单次抖动），`Timeout`=冷却时长，`MaxRequests`=1（半开单探测）。阈值与冷却进配置字段，默认保守（连续失败 3 次、冷却 30s）。
- **收益**：熔断打开时 scrape 毫秒级失败 → `up=0`，不再被挂死路由器拖满 15s 超时；resilience 指标族（状态迁移/拒绝）自动出现在 `/metrics`；`/ready` 探测同样快速 503。
- Close：roundTripper 有 `Close()`，注册进 bootstrap 资源链（与 ikuai client close 一起，顺序：先停 HTTP 监听，再关 client/transport）。

### 5. core/config 配置管线

- 不用 `LoadBootstrap`（etc/ 查找会破坏纯 flag 部署），用同源的 `config.LoadFile(-config path)` + `config.ApplyEnvOverlay` + `default/env/require` tag 校验。
- 优先级：**显式 flag（flag.Visit 判定）> env > 配置文件 > default tag**。
- 实现要点：所有现有 flag 默认值清零，迁移为 struct `default:` tag（唯一缺省源）；`-config` 为新增 flag（路径本身只认 flag、不读 env，避免「找配置文件」这一步自身还要查环境的循环）；解析顺序 = 注册 flag → Parse → 若 `-config` 非空则 LoadFile → ApplyEnvOverlay → 对 flag.Visit 命中的 flag 覆写 → 校验。
- token 必填改为校验管线**汇总报错**（列全缺失项 + 提示），退出码语义保留（缺配置 → 2）。
- `-username/-password` deprecated 行为原样保留。
- k8s/docker-compose 现有 `command/args` 零破坏（显式传入的 flag 永远最高优先）。

### 6. 并行采集

- `Collect` 拆三路：system / interfaces / clients（分页 walk 作为整体一路），`syncx.FanInLimitCtx(ctx, 3, 3, ...)` 扇出。
- `up` 语义不变：取决于 system 路结果；其余两路失败照旧 logAPIError 不影响 up。
- `mu` 串行保留（防并发 scrape 重入）；15s 采集超时 ctx 照旧。

### 7. 测试与验证

- 新增：配置优先级单测（file vs env vs 显式 flag 三层覆盖）、必填汇总报错测试、熔断 fail-fast 测试（httptest 死端点：先连续失败触发开闸，再断言错误为 ErrCircuitOpen 且耗时远小于超时）。
- 现有 collector 测试保持绿；`make check`（gofmt + vet + race）。
- `GOWORK=off go build ./...` 验证纯远端依赖可构建（模拟 CI/Docker）。

### 8. 文档

- README：`-config` 用法、env 变量清单、熔断行为与新增指标、`-insecure` 语义变化（transport 层承担）。
- CHANGELOG：本仓条目。
- deploy/（docker-compose、k8s）注释按需补充，不改现有 args 形态。

## 已知取舍

- httpx 拖入 hertz、registry、tracing、metrics、concurrency 依赖树 —— 深度档已接受。
- 熔断阈值默认保守，可配；首版不引入 httpx 重试（ikuai-api 已有）。
- `LoadBootstrap` etc/ 语义弃用、改 `LoadFile` —— 与「core/config 管线」的诉求等价（同一解码线、同一 tag 校验），但不绑架目录约定。

## 实施顺序（供 writing-plans）

1. ikuai-api 提交 + tag v1.3.4（push 前确认）。
2. 本仓依赖升级（go 1.26、oekit 全家桶、ikuai-api@v1.3.4、Dockerfile），先跑通 build/test。
3. metrics 门面替换（registry 单源）。
4. httpx transport + 熔断（含 TLS 陷阱与 Close 链）。
5. core/config 管线（flag 语义兼容重排）。
6. Collect 并行化。
7. 新增测试 + make check。
8. 文档 + CHANGELOG。
