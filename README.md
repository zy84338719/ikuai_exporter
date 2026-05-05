# ikuai_exporter

iKuai 路由器 Prometheus Exporter，基于 [ikuai-api](https://github.com/zy84338719/ikuai-api) 构建。

支持两种认证模式：

| 模式 | 适用 | 所需凭证 |
|------|------|---------|
| **v4 REST**（推荐） | iKuai v4 路由器 | 个人 API 令牌（`-token`），无需用户名/密码 |
| **Session** | iKuai v3 / v4 路由器 | 用户名 + 密码，版本自动检测 |

v4 个人 API 令牌可在路由器管理界面的 **系统设置 → 个人令牌** 中获取。v4 REST API 运行在 **HTTPS (443)**，指定 `-router` 时使用 `http://` 或 `https://` 均可（自动转换）。

## 支持的 Metrics

| Metric | 类型 | 说明 |
|--------|------|------|
| `ikuai_up` | Gauge | 路由器是否可达（1=正常，0=故障） |
| `ikuai_uptime_seconds` | Gauge | 路由器运行时长（秒） |
| `ikuai_version_info` | Gauge | 固件版本信息（标签携带详情） |
| `ikuai_cpu_usage_ratio{core}` | Gauge | 各核 CPU 使用率（0–1） |
| `ikuai_cpu_temperature_celsius` | Gauge | CPU 温度（摄氏度） |
| `ikuai_memory_total_kibibytes` | Gauge | 总内存（KiB） |
| `ikuai_memory_used_kibibytes` | Gauge | 已用内存（KiB） |
| `ikuai_memory_cached_kibibytes` | Gauge | 缓存内存（KiB） |
| `ikuai_memory_buffers_kibibytes` | Gauge | 缓冲内存（KiB） |
| `ikuai_online_users_total` | Gauge | 在线用户总数 |
| `ikuai_interface_up{interface,ip_addr,comment}` | Gauge | 接口链路状态（1=正常） |
| `ikuai_interface_upload_bytes_total{...}` | Counter | 接口累计上传字节数 |
| `ikuai_interface_download_bytes_total{...}` | Counter | 接口累计下载字节数 |
| `ikuai_interface_upload_speed_bytes{...}` | Gauge | 接口实时上传速率（bytes/s） |
| `ikuai_interface_download_speed_bytes{...}` | Gauge | 接口实时下载速率（bytes/s） |
| `ikuai_interface_connections{...}` | Gauge | 接口活跃连接数 |
| `ikuai_devices_online_total` | Gauge | 在线 LAN 设备总数 |
| `ikuai_device_info{mac,hostname,ip_addr,interface,comment}` | Gauge | 设备信息（值恒为 1） |
| `ikuai_device_upload_bytes_total{mac,ip_addr}` | Counter | 设备累计上传字节数 |
| `ikuai_device_download_bytes_total{mac,ip_addr}` | Counter | 设备累计下载字节数 |
| `ikuai_device_upload_speed_bytes{mac,ip_addr}` | Gauge | 设备实时上传速率（bytes/s） |
| `ikuai_device_download_speed_bytes{mac,ip_addr}` | Gauge | 设备实时下载速率（bytes/s） |
| `ikuai_device_connections{mac,ip_addr}` | Gauge | 设备活跃连接数 |

## 快速开始

### 二进制运行

```bash
# v4 路由器 —— 个人 API 令牌模式（推荐，无需用户名/密码）
./ikuai_exporter \
  -router http://10.10.30.254 \
  -token YjlmMjFjMzctZjlmYy00MjhiLThkZmUt

# v3 / v4 路由器 —— 用户名/密码模式
./ikuai_exporter \
  -router http://192.168.1.1 \
  -username admin \
  -password admin
```

访问 `http://localhost:9100/metrics` 查看指标。

### 参数说明

| 参数 | 默认值 | 说明 |
|------|--------|------|
| `-router` | `http://192.168.1.1` | 路由器地址（token 模式自动转 HTTPS） |
| `-token` | `""` | 个人 API 令牌（v4 REST 模式，与 username/password 二选一） |
| `-username` | `""` | 登录用户名（session 模式） |
| `-password` | `""` | 登录密码（session 模式） |
| `-listen` | `:9100` | Exporter 监听地址 |
| `-path` | `/metrics` | Metrics 路径 |
| `-namespace` | `ikuai` | Prometheus 指标前缀 |
| `-insecure` | `true` | 跳过 TLS 证书验证（session 模式） |

### Docker

```bash
docker run -d \
  --name ikuai-exporter \
  -p 9100:9100 \
  ghcr.io/zy84338719/ikuai_exporter:latest \
  -router http://192.168.1.1 \
  -username admin \
  -password admin
```

### Docker Compose

```bash
# 编辑 deploy/docker-compose.yml 中的路由器地址和凭证
vi deploy/docker-compose.yml

docker compose -f deploy/docker-compose.yml up -d
```

### Kubernetes

```bash
# 1. 创建 namespace（若尚未存在）
kubectl create namespace monitoring

# 2. 填写路由器凭证
vi deploy/k8s/secret.yaml
kubectl apply -f deploy/k8s/secret.yaml

# 3. 部署 Exporter
kubectl apply -f deploy/k8s/deployment.yaml

# 4. （可选）若使用 kube-prometheus-stack，创建 ServiceMonitor
kubectl apply -f deploy/k8s/servicemonitor.yaml
```

## Prometheus 配置（手动抓取）

若未使用 ServiceMonitor，在 `prometheus.yml` 中添加：

```yaml
scrape_configs:
  - job_name: ikuai
    static_configs:
      - targets: ["ikuai-exporter:9100"]
```

## 本地开发

```bash
# 构建
make build

# 运行
./bin/ikuai_exporter -router http://192.168.1.1 -username admin -password admin
```

## 许可证

MIT
