# ikuai_exporter

iKuai 路由器 Prometheus Exporter，基于 [ikuai-api](https://github.com/zy84338719/ikuai-api) 构建，**仅支持 iKuai v4**（v4 REST API + 个人令牌）。

> v3（`/Action/call` + 账号密码）已被 SDK 移除，本 exporter 不再支持。

## 认证

唯一认证方式：**v4 个人 API 令牌**。在路由器管理界面 → **系统设置 → 个人令牌** 生成后，用 `-token` 传入。

所有指标都带 `router` 标签（值来自 `-router-label`，默认取 `-router` 的 host），便于单实例区分多台路由器。

## 配置

优先级：**显式 flag > 环境变量 > 配置文件 > 内置缺省**。内置缺省只有一处来源（代码里的 `default:` 标签），
flag 的默认值也从它渲染——两处缺省不会各自漂移。

| 层 | 载体 | 说明 |
|----|------|------|
| 1 | 显式 flag | 命令行里**写了**才算（只有被设置过的 flag 才覆盖其它层） |
| 2 | 环境变量 | `IKUAI_` 前缀，见下表 |
| 3 | 配置文件 | `-config /etc/ikuai/config.yaml`，YAML |
| 4 | 内置缺省 | 代码里的 `default:` 标签 |

### 环境变量

| 变量 | 对应 flag | 说明 |
|------|-----------|------|
| `IKUAI_ROUTER` | `-router` | 路由器地址（自动转 HTTPS） |
| `IKUAI_TOKEN` | `-token` | 个人 API 令牌（必填） |
| `IKUAI_USERNAME` | `-username` | 已废弃（v4-only SDK 不支持账号密码登录） |
| `IKUAI_PASSWORD` | `-password` | 已废弃（同上） |
| `IKUAI_ROUTER_LABEL` | `-router-label` | 指标的 `router` 标签值 |
| `IKUAI_LISTEN` | `-listen` | 监听地址 |
| `IKUAI_METRICS_PATH` | `-path` | Metrics 路径 |
| `IKUAI_NAMESPACE` | `-namespace` | 指标前缀 |
| `IKUAI_SHUTDOWN_TIMEOUT` | `-shutdown-timeout` | 优雅关停窗口 |
| `IKUAI_INSECURE` | `-insecure` | 跳过 TLS 证书验证 |
| `IKUAI_BREAKER_FAILURES` | `-breaker-failures` | 连续失败多少次触发熔断 |
| `IKUAI_BREAKER_COOLDOWN` | `-breaker-cooldown` | 熔断冷却时长 |

环境变量要**非空**才生效：`IKUAI_INSECURE=false` 这类显式零值有效，空串视同未设置。

### 配置文件

```yaml
# /etc/ikuai/config.yaml
router: https://10.10.30.254
token: <your-api-token>
router_label: office
listen: ":9100"
breaker_failures: 3
breaker_cooldown: 30s
```

```bash
ikuai_exporter -config /etc/ikuai/config.yaml
```

配置文件的键就是上面的 YAML 键；**未知键直接拒载**，并报出该层的合法键清单与近似拼写建议——
拼错的键被静默忽略会让配置「看着生效、其实没生效」。文件里没写的键回落到环境变量 → 内置缺省。

区间校验（如熔断阈值、冷却时长）发生在**最终值**上——即 env 与显式 flag 覆写之后。
所以基线文件里写 `breaker_failures: 0`（占位）再由 env 给 `IKUAI_BREAKER_FAILURES=5`
覆盖是可行的；没有覆盖时它才会作为一条问题被列出来。

`-config` 的路径**只认命令行**（不读环境变量）——「找配置文件」这一步本身不该再依赖环境。

缺配置时（没令牌等）与非法值（越界等）一次列全所有问题，退出码 2。

---

## 支持的 Metrics

所有指标均含 `router` 标签（下表省略）。

| Metric | 类型 | 说明 |
|--------|------|------|
| `ikuai_up` | Gauge | system 端点（`/monitoring/system`）是否应答（1=正常，0=失败） |
| `ikuai_uptime_seconds` | Gauge | 路由器运行时长（秒） |
| `ikuai_version_info{version,arch,ver_string,model_name}` | Gauge | 固件版本信息 |
| `ikuai_cpu_usage_ratio{core}` | Gauge | 各核 CPU 使用率（0–1） |
| `ikuai_cpu_temperature_celsius` | Gauge | CPU 温度（°C） |
| `ikuai_memory_total_kibibytes` | Gauge | 总内存（KiB） |
| `ikuai_memory_used_kibibytes` | Gauge | 已用内存（KiB） |
| `ikuai_memory_cached_kibibytes` | Gauge | 缓存内存（KiB） |
| `ikuai_memory_buffers_kibibytes` | Gauge | 缓冲内存（KiB） |
| `ikuai_online_users_total` | Gauge | 在线用户总数 |
| `ikuai_connections_total` | Gauge | 活跃连接总数 |
| `ikuai_connections{protocol}` | Gauge | 按协议/状态的连接数（tcp/udp/icmp） |
| `ikuai_interface_up{interface,ip_addr,comment}` | Gauge | iKuai **线路检测**结果（1=检测成功）。只有 WAN 线路参与检测，**LAN 接口恒为 0**——这个 0 表示「没有成功的线路检测」，无法区分「未参与检测」与「检测失败」，**不能用来判断 LAN 链路状态** |
| `ikuai_interface_upload_bytes_total{interface,ip_addr,comment}` | Counter | 接口累计上传字节数 |
| `ikuai_interface_download_bytes_total{interface,ip_addr,comment}` | Counter | 接口累计下载字节数 |
| `ikuai_interface_upload_speed_bytes{interface,ip_addr,comment}` | Gauge | 接口实时上传速率（bytes/s） |
| `ikuai_interface_download_speed_bytes{interface,ip_addr,comment}` | Gauge | 接口实时下载速率（bytes/s） |
| `ikuai_interface_connections{interface,ip_addr,comment}` | Gauge | 接口活跃连接数 |
| `ikuai_devices_online_total` | Gauge | 在线 LAN 设备总数 |
| `ikuai_device_info{mac,hostname,ip_addr,interface,comment}` | Gauge | 设备信息（值恒为 1） |
| `ikuai_device_upload_bytes_total{mac,ip_addr}` | Counter | 设备累计上传字节数 |
| `ikuai_device_download_bytes_total{mac,ip_addr}` | Counter | 设备累计下载字节数 |
| `ikuai_device_upload_speed_bytes{mac,ip_addr}` | Gauge | 设备实时上传速率（bytes/s） |
| `ikuai_device_download_speed_bytes{mac,ip_addr}` | Gauge | 设备实时下载速率（bytes/s） |
| `ikuai_device_connections{mac,ip_addr}` | Gauge | 设备活跃连接数 |

## 熔断与弹性指标

路由器外呼走 `oekit/httpx` 的传输层，熔断挂在传输层上：

- 连续失败 `-breaker-failures` 次（默认 3）后熔断打开，后续请求**立即失败**
  （`ErrCircuitOpen`），不再等路由器的超时——scrape 因此不会被挂死的路由器拖满 15s
  采集超时，`ikuai_up` 快速变 0。
- 熔断打开 `-breaker-cooldown`（默认 30s）后半开，只放一个探测请求；探测成功即回到
  闭态。中间成功一次会清零连续失败计数，不会误跳闸。
- 5xx 计入熔断失败统计（但不外抛），4xx 不算失败。
- 熔断打开后 ikuai-api 自带的重试引擎仍会退避重试（httpx 侧不重复开重试），实测
  一次 scrape 在 0.7s 量级失败——远小于 15s 采集超时。

传输层同时暴露这些指标（`<ns>` 与 `-namespace` 一致）：

| Metric | 类型 | 说明 |
|--------|------|------|
| `<ns>_requests_total{service,target,method,status_code}` | Counter | 外呼请求数 |
| `<ns>_request_duration_seconds{service,target,method}` | Histogram | 外呼时延 |
| `<ns>_requests_in_flight{service,target,method}` | Gauge | 在途请求数 |
| `<ns>_breaker_state{service,kind}` | Gauge | 熔断状态（0=closed / 1=half_open / 2=open） |
| `<ns>_breaker_transitions_total{service,kind,from,to}` | Counter | 熔断状态迁移次数 |
| `<ns>_breaker_rejected_total{service,kind}` | Counter | 熔断拒绝次数 |

`/metrics` 只有一个 registry：exporter 自己的指标、go/process collector、
`app_build_info` / `app_start_time_seconds` 与上表的传输层指标都在同一面。

---
## 参数说明

| 参数 | 默认值 | 说明 |
|------|--------|------|
| `-router` | `https://192.168.1.1` | 路由器地址（自动转 HTTPS） |
| `-token` | `""` | **必填**，个人 API 令牌 |
| `-router-label` | `""` | 指标的 `router` 标签值（默认取 `-router` 的 host） |
| `-listen` | `:9100` | Exporter 监听地址 |
| `-path` | `/metrics` | Metrics 路径 |
| `-namespace` | `ikuai` | Prometheus 指标前缀 |
| `-shutdown-timeout` | `15s` | SIGINT/SIGTERM 时等待在途 scrape 的优雅关停窗口 |
| `-insecure` | `true` | 跳过 TLS 证书验证（由传输层承担，见「熔断与弹性指标」） |
| `-breaker-failures` | `3` | 连续失败多少次触发熔断 |
| `-breaker-cooldown` | `30s` | 熔断冷却时长（到期后半开，放一个探测请求） |
| `-config` | `""` | YAML 配置文件路径（只认命令行） |

### HTTP 端点

| 路径 | 说明 |
|------|------|
| `/metrics` | Prometheus 指标 |
| `/live`、`/healthz` | 存活探针（进程在跑即 200，不触碰路由器） |
| `/ready` | 就绪探针（**真实探测路由器**，不可达返回 503，供 kubelet readiness 用） |

进程收到 `SIGINT`/`SIGTERM` 时按 `bootstrap` 生命周期编排优雅关停：先停止对外服务并等待在途
scrape 结束，再关闭 ikuai-api 客户端，最后退出。Kubernetes 侧已配置
`terminationGracePeriodSeconds: 30`。

---

## 快速开始

### 二进制

```bash
./ikuai_exporter \
  -router http://10.10.30.254 \
  -token <your-api-token> \
  -router-label office
```

访问 `http://localhost:9100/metrics` 查看指标。

---

### Docker

```bash
docker run -d \
  --name ikuai-exporter \
  -p 9100:9100 \
  ghcr.io/zy84338719/ikuai_exporter:latest \
  -router http://10.10.30.254 \
  -token <your-api-token> \
  -router-label office
```

---

### Docker Compose

`deploy/docker-compose.yml` 提供了模板，按需取消注释：

```bash
vi deploy/docker-compose.yml    # 填写路由器地址和凭证
docker compose -f deploy/docker-compose.yml up -d
```

**监控多台路由器**：每台路由器启动一个实例，监听不同端口，各自用 `-router-label` 区分：

```bash
# 实例 1（端口 9100，router-label=office）
# 实例 2（端口 9101，router-label=home）
docker compose -f deploy/docker-compose.yml up -d
```

---

### Kubernetes

```bash
# 1. 创建 namespace（若已存在可跳过）
kubectl create namespace monitoring

# 2. 选择认证模式并填写凭证
vi deploy/k8s/secret.yaml
kubectl apply -f deploy/k8s/secret.yaml

# 3. 部署 Exporter（默认 v4 令牌模式，可在 deployment.yaml 中切换）
kubectl apply -f deploy/k8s/deployment.yaml

# 4. 若使用 kube-prometheus-stack，创建 ServiceMonitor 自动发现
kubectl apply -f deploy/k8s/servicemonitor.yaml
```

`deploy/k8s/deployment.yaml` 中包含两种模式的注释配置，切换方式见文件内说明。

---

### Prometheus 手动配置

不使用 ServiceMonitor 时，在 `prometheus.yml` 添加：

```yaml
scrape_configs:
  - job_name: ikuai
    static_configs:
      - targets:
          - ikuai-exporter:9100   # office
          - ikuai-exporter:9101   # home
```

---

## 本地开发

```bash
# 构建
make build

# 本地门禁（gofmt + vet + race 测试，与 CI 一致）
make check

# 运行
./bin/ikuai_exporter -router http://10.10.30.254 -token <token> -router-label office
```

### 本地联调 SDK

`go.mod` 现在锁定 ikuai-api 的**已发布版本**，且不再带 `replace`——本地、CI 和镜像构建看到的依赖因此一致（此前 `replace ../ikuai-api` 会让本地和 CI 构建到未发布的 `main`，而 Docker 构建却从 proxy 取已发布版）。

仓库根目录有一个**不入库**的 `go.work`（见 `.gitignore`），它把同级的 `../ikuai-api` 接进工作区：改 SDK 后 exporter 立即生效，而 `go.mod` 依旧锁定发布版。

```bash
# 确认当前解析到哪个版本
go list -m github.com/zy84338719/ikuai-api   # 有 go.work 时显示本地路径

# 临时验证「纯发布版」行为（等价于 CI / 镜像构建）
GOWORK=off go test ./...
```

若 `go.work` 丢失，重建即可：

```bash
printf 'go 1.26.0\n\nuse (\n\t.\n\t../ikuai-api\n)\n' > go.work
```

### 工程规范

本项目遵循 [oekit](https://oekit.github.io) 的工程契约：

- **日志**统一走 `logx` 门面（kv 结构化风格），`main` 中 `logx.UseSlog()` 选择 slog JSON 后端。
  内嵌的 ikuai-api 通过 `logx.Delegating` 懒解析，会自动跟随同一后端。
- **错误**统一用 `oekit/pkg/errors` 包装，保留错误链以便 `AsBizError` 提取 iKuai 业务码。
- **生命周期**由 `oekit/bootstrap` 编排（信号监听、资源逆序关闭、优雅关停）。
- **配置**由 `oekit/core/config` 装配（`default:`/`env:` tag、严格模式、汇总报错）。
- **传输层**用 `oekit/httpx`（熔断、请求指标、TLS 策略单源）。
- **指标**统一注册到 `oekit/metrics.Registry`，`/metrics` 只 gather 这一个。
- **并发**使用 `oekit/concurrency/async` 的 GoSafe 系列，不写裸 `go func`。

> 依赖版本一律由 `go.mod` 锁定为已发布的 ikuai-api；本地联调用不入库的 `go.work`，
> 不再通过 `replace` 污染模块声明。

## 许可证

MIT
