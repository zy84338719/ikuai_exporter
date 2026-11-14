package main

import (
	"net/http/httptest"
	"strings"
	"testing"

	oekmetrics "github.com/oekit/metrics"

	ikuaiapi "github.com/zy84338719/ikuai-api"
	"github.com/zy84338719/ikuai_exporter/metrics"
)

// TestMetricsSingleRegistry 锁住「单源 registry」这个属性：
//
// /metrics 这一面必须同时可见 exporter 自己的指标、go/process collector 与
// build info——它们都挂在 oekit/metrics.Registry 上。此前 exporter 自建
// prometheus.NewRegistry()，/metrics 只 gather 那一个，go/process 与 build
// info 永远不会出现在抓取面上。
func TestMetricsSingleRegistry(t *testing.T) {
	oekmetrics.Init("ikuai-test")

	families, err := oekmetrics.Registry.Gather()
	if err != nil {
		t.Fatalf("gather registry: %v", err)
	}
	names := make(map[string]bool, len(families))
	for _, f := range families {
		names[f.GetName()] = true
	}
	for _, want := range []string{
		"go_goroutines",
		"process_start_time_seconds",
		"app_build_info",
		"app_start_time_seconds",
	} {
		if !names[want] {
			t.Errorf("metric family %q missing from the shared registry", want)
		}
	}

	// 抓取面本身也要能看到它们。collector 传真实实例（/ready 闭包会用到它，
	// 传 nil 的话将来一旦在该测试里请求 /ready 就是 nil 解引用）。
	client, err := ikuaiapi.NewClient("https://127.0.0.1:1", ikuaiapi.WithToken("t"))
	if err != nil {
		t.Fatalf("new ikuai client: %v", err)
	}
	mux := newMux(oekmetrics.Registry, metrics.NewCollector("ikuai", "r", client), "/metrics")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest("GET", "/metrics", nil))
	if rec.Code != 200 {
		t.Fatalf("/metrics status = %d, want 200", rec.Code)
	}
	body := rec.Body.String()
	for _, want := range []string{"go_goroutines", "app_build_info", "app_start_time_seconds"} {
		if !strings.Contains(body, want) {
			t.Errorf("/metrics body missing %q", want)
		}
	}
}
