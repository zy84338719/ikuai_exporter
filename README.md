# ikuai_exporter

iKuai 路由器 Prometheus Exporter，基于 [ikuai-api](https://github.com/zy84338719/ikuai-api) 构建，**仅支持 iKuai v4**（v4 REST API + 个人令牌）。

> v3（`/Action/call` + 账号密码）已被 SDK 移除，本 exporter 不再支持。

## 认证

唯一认证方式：**v4 个人 API 令牌**。在路由器管理界面 → **系统设置 → 个人令牌** 生成后，用 `-token` 传入。

所有指标都带 `router` 标签（值来自 `-router-label`，默认取 `-router` 的 host），便于单实例区分多台路由器。

## 支持的 Metrics

所有指标均含 `router` 标签（下表省略）。

| Metric | 类型 | 说明 |
|--------|------|------|
| `ikuai_up` | Gauge | 路由器是否可达（1=正常，0=故障） |
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
| `ikuai_interface_up{interface,ip_addr,comment}` | Gauge | 接口链路状态（1=正常） |
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

## 参数说明

| 参数 | 默认值 | 说明 |
|------|--------|------|
| `-router` | `https://192.168.1.1` | 路由器地址（自动转 HTTPS） |
| `-token` | `""` | **必填**，个人 API 令牌 |
| `-router-label` | `""` | 指标的 `router` 标签值（默认取 `-router` 的 host） |
| `-listen` | `:9100` | Exporter 监听地址 |
| `-path` | `/metrics` | Metrics 路径 |
| `-namespace` | `ikuai` | Prometheus 指标前缀 |
| `-insecure` | `true` | 跳过 TLS 证书验证 |

### HTTP 端点

| 路径 | 说明 |
|------|------|
| `/metrics` | Prometheus 指标 |
| `/live`、`/healthz` | 存活探针（进程在跑即 200） |
| `/ready` | 就绪探针（**真实探测路由器**，不可达返回 503，供 kubelet readiness 用） |

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

# 运行
./bin/ikuai_exporter -router http://10.10.30.254 -token <token> -router-label office
```

## 许可证

MIT
