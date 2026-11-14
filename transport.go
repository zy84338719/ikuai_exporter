// transport.go —— 路由器外呼的传输层。
package main

import (
	"crypto/tls"
	"fmt"
	"net/http"

	"github.com/cloudwego/hertz/pkg/app/client"
	"github.com/oekit/httpx"
	oekmetrics "github.com/oekit/metrics"
	"github.com/sony/gobreaker"
)

// breakerName 是熔断器的名字，进入 httpx 的弹性指标（breaker_state /
// breaker_rejected_total 的 service 标签）。
const breakerName = "ikuai-router"

// newRouterTransport 构造路由器外呼的传输层。
//
// 目标寻址随请求自带 scheme://host——不设 BaseURL/Endpoints 时 httpx 的
// 桥接层拿请求自身的 host 拨号，因此对 ikuai-api 完全透明：SDK 照旧按
// 绝对 URL 发请求，熔断/指标/链路透传都在传输层完成。
//
// 熔断挂在 httpx 客户端上：
//   - ReadyToTrip 按「连续失败次数」触发（gobreaker 的 ConsecutiveFailures
//     遇成功即清零，中间成功一次不会误跳闸）；
//   - Timeout 是冷却时长，到点后半开；
//   - MaxRequests=1 让半开态只放一个探测请求；
//   - 5xx 计入失败统计但不外抛（httpx #107），4xx 不算失败。
//
// httpx 侧重试缺省关闭——ikuai-api 已有自己的重试引擎，两层都开会把尝试
// 次数乘起来。
func newRouterTransport(cfg *Config, target string) (http.RoundTripper, error) {
	opts := []httpx.ClientOption{
		// 请求指标与熔断弹性指标挂到共享 registry——/metrics 一面可见。
		httpx.WithMetrics(oekmetrics.Registry, cfg.Namespace),
		httpx.WithTarget(target),
		httpx.WithCircuitBreaker(breakerName, gobreaker.Settings{
			Name:        breakerName,
			MaxRequests: 1,
			Timeout:     cfg.BreakerCooldown,
			ReadyToTrip: func(counts gobreaker.Counts) bool {
				return counts.ConsecutiveFailures >= uint32(cfg.BreakerFailures)
			},
		}),
	}
	// TLS 策略显式装进传输层——两个原因，缺一不可：
	//
	// 1. ikuaiapi.WithInsecureSkipVerify 做的是 *http.Transport 的类型断言，
	//    transport 被换掉之后它静默失效（既不报错也不生效），所以 skip-verify
	//    只能由 httpx 这层承担。
	// 2. hertz 的 WithTLSConfig 会把拨号器换成 standard.NewDialer()。缺省
	//    拨号器在 (amd64||arm64)&&(linux||darwin) 上是 netpoll——它的
	//    AddTLS/DialConnection 直接返回 "not support tls"。所以只要不装 TLS
	//    配置，HTTPS 目标在 Docker（linux）里根本连不上：不设 -insecure 的
	//    用户会撞上 "not support tls" 而不是证书校验失败。
	//
	// 因此这里无条件装 TLS 配置，InsecureSkipVerify 由 -insecure 决定。
	opts = append(opts, httpx.WithHertzOption(client.WithTLSConfig(&tls.Config{
		InsecureSkipVerify: cfg.Insecure, //nolint:gosec // -insecure 显式要求
	})))

	rt, err := httpx.NewRoundTripperE(cfg.Namespace, opts...)
	if err != nil {
		return nil, fmt.Errorf("build router transport: %w", err)
	}
	return rt, nil
}

// closeTransport 释放传输层资源。
//
// httpx 的 roundTripper 只暴露 Close()（没有 CloseIdleConnections），而
// http.Client.CloseIdleConnections 与 ikuai-api 的 Client.Close 都依赖后者
// ——所以关闭动作必须在这里显式做，否则传输层活到进程退出。
func closeTransport(rt http.RoundTripper) {
	if c, ok := rt.(interface{ Close() }); ok {
		c.Close()
	}
}
