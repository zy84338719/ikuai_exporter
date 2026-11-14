package main

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/oekit/httpx"
	ikuaiapi "github.com/zy84338719/ikuai-api"
	"github.com/zy84338719/ikuai_exporter/metrics"

	"github.com/prometheus/client_golang/prometheus"
)

// newTestConfig 造一份只跟熔断相关的配置。
func newTestConfig() *Config {
	return &Config{
		Namespace:       "ikuai",
		BreakerFailures: 3,
		BreakerCooldown: 30 * time.Second,
		Insecure:        true,
	}
}

// TestRouterTransport_BreakerFailsFast 连续失败触发开闸后，请求立即以
// ErrCircuitOpen 失败——远快于采集超时。
//
// 这条是熔断的收益面：路由器挂死时 scrape 不该被 15s 超时拖满。
func TestRouterTransport_BreakerFailsFast(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	rt, err := newRouterTransport(newTestConfig(), "test-router")
	if err != nil {
		t.Fatalf("newRouterTransport: %v", err)
	}
	defer closeTransport(rt)

	client := &http.Client{Transport: rt}

	// 前三次：5xx 计入熔断失败统计，但按标准库语义不算错误（响应原样返回）。
	for i := 0; i < 3; i++ {
		resp, err := client.Get(srv.URL)
		if err != nil {
			t.Fatalf("request %d: unexpected error %v", i, err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusInternalServerError {
			t.Fatalf("request %d: status = %d, want 500", i, resp.StatusCode)
		}
	}

	// 第四次：熔断已开，立即失败。
	start := time.Now()
	_, err = client.Get(srv.URL)
	elapsed := time.Since(start)
	if !errors.Is(err, httpx.ErrCircuitOpen) {
		t.Fatalf("err = %v, want ErrCircuitOpen", err)
	}
	if elapsed > time.Second {
		t.Fatalf("breaker rejection took %s, want fail-fast (<1s)", elapsed)
	}
}

// TestRouterTransport_TLSSkipVerify HTTPS + 自签证书：skip-verify 必须真的
// 生效——iKuai 路由器用的是自签证书，验不过就等于连不上。
func TestRouterTransport_TLSSkipVerify(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	cfg := newTestConfig()
	rt, err := newRouterTransport(cfg, "test-router")
	if err != nil {
		t.Fatalf("newRouterTransport: %v", err)
	}
	defer closeTransport(rt)

	client := &http.Client{Transport: rt}
	resp, err := client.Get(srv.URL)
	if err != nil {
		t.Fatalf("GET over TLS: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
}

// TestRouterTransport_TLSVerificationOn 证书校验必须真的开着。
//
// 两条断言：
//   - 自签证书必须被拒（-insecure=false 时校验生效）；
//   - 失败形态必须是「证书校验失败」而不是「not support tls」——后者说明
//     TLS 握手根本没发生（hertz 回落到了不支持 TLS 的 netpoll 拨号器），
//     那样连「证书有效的路由器」也连不上。
func TestRouterTransport_TLSVerificationOn(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	cfg := newTestConfig()
	cfg.Insecure = false
	rt, err := newRouterTransport(cfg, "test-router")
	if err != nil {
		t.Fatalf("newRouterTransport: %v", err)
	}
	defer closeTransport(rt)

	client := &http.Client{Transport: rt}
	_, err = client.Get(srv.URL)
	if err == nil {
		t.Fatal("want certificate verification error for a self-signed cert")
	}
	if strings.Contains(err.Error(), "not support tls") {
		t.Fatalf("TLS handshake never happened (netpoll dialer): %v", err)
	}
	if !strings.Contains(err.Error(), "certificate") {
		t.Fatalf("err = %v, want certificate verification failure", err)
	}
}

// TestScrapeFailsFastWhenRouterDead 端到端：路由器持续 500 时，熔断打开后
// scrape 必须快速返回且 up=0——不被 15s 采集超时拖满。
func TestScrapeFailsFastWhenRouterDead(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	cfg := newTestConfig()
	rt, err := newRouterTransport(cfg, "test-router")
	if err != nil {
		t.Fatalf("newRouterTransport: %v", err)
	}
	defer closeTransport(rt)

	client, err := ikuaiapi.NewClient(srv.URL,
		ikuaiapi.WithToken("test-token"),
		ikuaiapi.WithHTTPClient(&http.Client{Transport: rt}),
	)
	if err != nil {
		t.Fatalf("new ikuai client: %v", err)
	}

	collector := metrics.NewCollector("ikuai", "router-1", client)
	reg := prometheus.NewRegistry()
	if err := reg.Register(collector); err != nil {
		t.Fatalf("register collector: %v", err)
	}

	scrape := func() (float64, time.Duration) {
		start := time.Now()
		families, err := reg.Gather()
		if err != nil {
			t.Fatalf("gather: %v", err)
		}
		elapsed := time.Since(start)
		for _, f := range families {
			if f.GetName() != "ikuai_up" {
				continue
			}
			for _, m := range f.GetMetric() {
				return m.GetGauge().GetValue(), elapsed
			}
		}
		return -1, elapsed
	}

	// 第一轮：路由器 500 → 熔断计数累积并开闸，up=0。
	up, _ := scrape()
	if up != 0 {
		t.Fatalf("up = %v, want 0 while the router is failing", up)
	}

	// 后续 scrape：熔断已开，必须快速失败（远小于 15s 采集超时）。
	up, elapsed := scrape()
	if up != 0 {
		t.Fatalf("up = %v, want 0", up)
	}
	if elapsed > 3*time.Second {
		t.Fatalf("scrape took %s, want fail-fast under the breaker", elapsed)
	}
}

// TestRouterTransport_BreakerCountsConsecutiveFailures 「连续」语义：中间
// 成功一次就清零，失败次数不够阈值不开闸。
func TestRouterTransport_BreakerCountsConsecutiveFailures(t *testing.T) {
	var calls atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		// 第 3 次请求成功一次，其余 500。
		if calls.Add(1) == 3 {
			w.WriteHeader(http.StatusOK)
			return
		}
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	rt, err := newRouterTransport(newTestConfig(), "test-router")
	if err != nil {
		t.Fatalf("newRouterTransport: %v", err)
	}
	defer closeTransport(rt)

	client := &http.Client{Transport: rt}
	// 失败、失败、成功 → 连续失败计数清零。
	for i := 0; i < 3; i++ {
		resp, err := client.Get(srv.URL)
		if err != nil {
			t.Fatalf("request %d: unexpected error %v", i, err)
		}
		_ = resp.Body.Close()
	}
	// 再失败两次：连续失败数只有 2，未达阈值 3，仍应拿到 500 响应而非熔断错误。
	for i := 0; i < 2; i++ {
		resp, err := client.Get(srv.URL)
		if err != nil {
			t.Fatalf("request %d after reset: unexpected error %v", i, err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusInternalServerError {
			t.Fatalf("request %d after reset: status = %d, want 500", i, resp.StatusCode)
		}
	}
}
