package metrics

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	ikuaiapi "github.com/zy84338719/ikuai-api"

	"github.com/oekit/concurrency/async"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
)

// newTestClient builds an SDK client pointed at srv, skipping TLS verify.
func newTestClient(t *testing.T, srv *httptest.Server) *ikuaiapi.Client {
	t.Helper()
	c, err := ikuaiapi.NewClient(srv.URL, ikuaiapi.WithToken("test"))
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	return c
}

// collect gathers every metric the collector emits into a map for easy
// assertion.
func collect(t *testing.T, c *Collector) map[string]*dto.MetricFamily {
	t.Helper()
	ch := make(chan *prometheus.Desc, 1)
	c.Describe(ch)
	close(ch)

	reg := prometheus.NewRegistry()
	if err := reg.Register(c); err != nil {
		t.Fatalf("register: %v", err)
	}
	mfs, err := reg.Gather()
	if err != nil {
		t.Fatalf("gather: %v", err)
	}
	out := make(map[string]*dto.MetricFamily, len(mfs))
	for _, mf := range mfs {
		out[mf.GetName()] = mf
	}
	return out
}

// collectValues runs one scrape and returns every emitted metric keyed by its
// Desc string, with its value already written out.
//
// The channel is drained concurrently with Collect: a 250-client router emits
// 1500+ series, so a fixed buffer would deadlock as soon as it filled.
func collectValues(t *testing.T, c *Collector) map[string]float64 {
	t.Helper()

	ch := make(chan prometheus.Metric)
	var wg sync.WaitGroup
	wg.Add(1)
	async.GoSafeLabeled("collect", func() {
		defer wg.Done()
		defer close(ch)
		c.Collect(ch)
	})

	out := make(map[string]float64, 1024)
	for m := range ch {
		pb := &dto.Metric{}
		if err := m.Write(pb); err != nil {
			t.Errorf("write metric %s: %v", m.Desc(), err)
			continue
		}
		switch {
		case pb.Gauge != nil:
			out[m.Desc().String()] = pb.GetGauge().GetValue()
		case pb.Counter != nil:
			out[m.Desc().String()] = pb.GetCounter().GetValue()
		}
	}
	wg.Wait()
	return out
}

// devicesOnlineTotal pulls the value of the devices_online_total series out of
// a scrape result.
func devicesOnlineTotal(t *testing.T, values map[string]float64) float64 {
	t.Helper()
	for desc, v := range values {
		if strings.Contains(desc, "ikuai_devices_online_total") {
			return v
		}
	}
	t.Fatalf("ikuai_devices_online_total was not emitted")
	return 0
}

// systemOK is a minimal /monitoring/system body, so paging tests can focus on
// the clients-online endpoint.
const systemOK = `{"code":0,"results":{"sysinfo":{"uptime":3600,"cpu":["12.5%"],"cputemp":[42.6],` +
	`"memory":{"total":1000000,"avail":400000,"cached":50000,"buffers":30000},"online_user":{"count":3},` +
	`"stream":{"connect_num":128,"tcp_connect_num":80,"udp_connect_num":40,"icmp_connect_num":2},` +
	`"verinfo":{"version":"4.0.1","arch":"x86","ver_string":"4.0.1 x64","model_name":"X10"}}}}`

func TestCollectSystem(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(r.URL.Path, "/monitoring/system"):
			_, _ = w.Write([]byte(systemOK))
		case strings.HasSuffix(r.URL.Path, "/monitoring/interfaces-status"):
			_, _ = w.Write([]byte(`{"code":0,"results":{"iface_check":[{"interface":"wan1","result":"success"}],"iface_stream":[{"interface":"wan1","ip_addr":"1.2.3.4","comment":"","total_up":1000,"total_down":2000,"upload":50,"download":80,"connect_num":"5"}]}}`))
		case strings.HasSuffix(r.URL.Path, "/monitoring/clients-online"):
			_, _ = w.Write([]byte(`{"code":0,"results":{"data":[{"mac":"aa:bb:cc:dd:ee:ff","hostname":"phone","ip_addr":"192.168.1.10","interface":"lan1","comment":"","total_up":100,"total_down":200,"upload":10,"download":20,"connect_num":2}]}}`))
		default:
			_, _ = w.Write([]byte(`{"code":0,"results":{"data":null}}`))
		}
	}))
	defer srv.Close()

	c := NewCollector("ikuai", "test-router", newTestClient(t, srv))
	// Trigger a Collect cycle.
	ch := make(chan prometheus.Metric, 64)
	c.Collect(ch)
	close(ch)

	got := map[string]bool{}
	for m := range ch {
		// Desc.String() contains the FQ name; just record that it was emitted.
		s := m.Desc().String()
		for _, name := range []string{"ikuai_up", "ikuai_uptime_seconds", "ikuai_cpu_usage_ratio", "ikuai_memory_total_kibibytes", "ikuai_devices_online_total", "ikuai_interface_up", "ikuai_connections_total"} {
			if strings.Contains(s, name) {
				got[name] = true
			}
		}
	}
	for _, want := range []string{"ikuai_up", "ikuai_uptime_seconds", "ikuai_cpu_usage_ratio", "ikuai_memory_total_kibibytes", "ikuai_devices_online_total", "ikuai_interface_up", "ikuai_connections_total"} {
		if !got[want] {
			t.Errorf("expected metric %s to be emitted, it was not", want)
		}
	}
}

// TestClientsOnlinePaging is the regression test for the unpaged read: the
// endpoint is paged, so a router holding more clients than one page must have
// all of them counted, and the request must carry `limit` (the key the v4
// contract accepts — `page_size` is silently ignored).
func TestClientsOnlinePaging(t *testing.T) {
	const total = 250 // spans 3 pages at the collector's page size of 100

	var (
		mu       sync.Mutex
		requests []string
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(r.URL.Path, "/monitoring/system"):
			_, _ = w.Write([]byte(systemOK))
		case strings.HasSuffix(r.URL.Path, "/monitoring/clients-online"):
			page, _ := strconv.Atoi(r.URL.Query().Get("page"))
			limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))

			mu.Lock()
			requests = append(requests, r.URL.RawQuery)
			mu.Unlock()

			// Serve the requested slice, honouring the limit the collector sent.
			start := (page - 1) * limit
			end := start + limit
			if start > total {
				start = total
			}
			if end > total {
				end = total
			}
			var items []string
			for i := start; i < end; i++ {
				items = append(items, fmt.Sprintf(
					`{"mac":"aa:bb:cc:%02x:%02x:%02x","hostname":"dev%d","ip_addr":"192.168.1.%d",`+
						`"interface":"lan1","comment":"","total_up":1,"total_down":2,"upload":0,`+
						`"download":0,"connect_num":0}`,
					(i>>16)&0xff, (i>>8)&0xff, i&0xff, i, i%250))
			}
			if items == nil {
				items = []string{}
			}
			_, _ = w.Write([]byte(`{"code":0,"results":{"data":[` + strings.Join(items, ",") + `]}}`))
		default:
			_, _ = w.Write([]byte(`{"code":0,"results":{"data":null}}`))
		}
	}))
	defer srv.Close()

	c := NewCollector("ikuai", "paged-router", newTestClient(t, srv))
	got := devicesOnlineTotal(t, collectValues(t, c))

	if got != total {
		t.Errorf("devices_online_total = %v, want %v (paging did not walk every page)", got, total)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(requests) != 3 {
		t.Fatalf("expected 3 page requests for %d clients, got %d: %v", total, len(requests), requests)
	}
	for _, q := range requests {
		if !strings.Contains(q, "limit=") {
			t.Errorf("request %q is missing the limit parameter; the v4 contract ignores page_size", q)
		}
		if strings.Contains(q, "page_size") {
			t.Errorf("request %q used page_size, which the router silently ignores", q)
		}
	}
}

// TestDecodeClientPageShapes locks the three list wrapping shapes the v4 API
// uses, since the firmware is not consistent about which one it returns.
func TestDecodeClientPageShapes(t *testing.T) {
	tests := []struct {
		name string
		body string
		want int
	}{
		{"data key", `{"data":[{"mac":"a"},{"mac":"b"}]}`, 2},
		{"bare array", `[{"mac":"a"},{"mac":"b"},{"mac":"c"}]`, 3},
		{"group_data key", `{"clients_online_data":[{"mac":"a"}],"clients_online_total":1}`, 1},
		{"empty data", `{"data":[]}`, 0},
		{"null data", `{"data":null}`, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := decodeClientPage(json.RawMessage(tt.body))
			if err != nil {
				t.Fatalf("decodeClientPage(%s): %v", tt.body, err)
			}
			if len(got) != tt.want {
				t.Errorf("decodeClientPage(%s) = %d items, want %d", tt.body, len(got), tt.want)
			}
		})
	}
}

func TestDecodeClientPageRejectsUnknown(t *testing.T) {
	if _, err := decodeClientPage(json.RawMessage(`{"unexpected":123}`)); err == nil {
		t.Error("expected an error for a payload with no recognisable collection key")
	}
	if _, err := decodeClientPage(json.RawMessage(`"not-json"`)); err == nil {
		t.Error("expected an error for a non-JSON payload")
	}
}

// TestClientsOnlineMidWalkFailureKeepsPartial asserts a failure after the first
// page degrades to partial data rather than dropping every client metric.
func TestClientsOnlineMidWalkFailureKeepsPartial(t *testing.T) {
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(r.URL.Path, "/monitoring/system"):
			_, _ = w.Write([]byte(systemOK))
		case strings.HasSuffix(r.URL.Path, "/monitoring/clients-online"):
			calls++
			if calls > 1 {
				w.WriteHeader(http.StatusInternalServerError)
				_, _ = w.Write([]byte(`{"code":3001,"message":"boom"}`))
				return
			}
			var items []string
			for i := 0; i < clientPageSize; i++ {
				items = append(items, fmt.Sprintf(`{"mac":"aa:bb:cc:00:00:%02x","hostname":"d%d","ip_addr":"192.168.1.%d"}`, i, i, i%250))
			}
			_, _ = w.Write([]byte(`{"code":0,"results":{"data":[` + strings.Join(items, ",") + `]}}`))
		default:
			_, _ = w.Write([]byte(`{"code":0,"results":{"data":null}}`))
		}
	}))
	defer srv.Close()

	c := NewCollector("ikuai", "flaky-router", newTestClient(t, srv))
	got := devicesOnlineTotal(t, collectValues(t, c))

	if got != clientPageSize {
		t.Errorf("devices_online_total = %v, want %v (the first page should survive a later failure)", got, clientPageSize)
	}
}

// TestClientsOnlineFirstPageFailure asserts a hard failure on the first page
// surfaces as no device series at all rather than a misleading zero.
func TestClientsOnlineFirstPageFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.HasSuffix(r.URL.Path, "/monitoring/system") {
			_, _ = w.Write([]byte(systemOK))
			return
		}
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"code":3001,"message":"boom"}`))
	}))
	defer srv.Close()

	c := NewCollector("ikuai", "broken-router", newTestClient(t, srv))
	values := collectValues(t, c)

	for desc := range values {
		if strings.Contains(desc, "ikuai_devices_online_total") {
			t.Errorf("devices_online_total should not be emitted when the first page failed, got %s", desc)
		}
	}
	if !strings.Contains(anyDescContaining(values, "ikuai_up"), "ikuai_up") {
		t.Error("expected the exporter to stay up when only the client list fails")
	}
}

func anyDescContaining(values map[string]float64, needle string) string {
	for desc := range values {
		if strings.Contains(desc, needle) {
			return desc
		}
	}
	return ""
}

func TestCollectRouterDown(t *testing.T) {
	// Server returns 500 on /monitoring/system → up=0.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"code":0,"message":"down"}`))
	}))
	defer srv.Close()

	c := NewCollector("ikuai", "dead", newTestClient(t, srv))
	ch := make(chan prometheus.Metric, 16)
	c.Collect(ch)
	close(ch)

	var upVal float64
	for m := range ch {
		if strings.Contains(m.Desc().String(), "ikuai_up") {
			pb := &dto.Metric{}
			if err := m.Write(pb); err == nil {
				upVal = pb.GetGauge().GetValue()
			}
		}
	}
	if upVal != 0 {
		t.Errorf("expected ikuai_up=0 when router is down, got %v", upVal)
	}
}

func TestToHTTPS(t *testing.T) {
	tests := []struct{ in, want string }{
		{"http://192.168.1.1", "https://192.168.1.1"},
		{"https://router.lan", "https://router.lan"},
		{"192.168.1.1", "https://192.168.1.1"},
		{"http://1.2.3.4/", "https://1.2.3.4"},
	}
	for _, tt := range tests {
		if got := ToHTTPS(tt.in); got != tt.want {
			t.Errorf("ToHTTPS(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestParsePct(t *testing.T) {
	tests := []struct {
		in   string
		want float64
		err  bool
	}{
		{"50%", 0.5, false},
		{"12.5", 0.125, false},
		{"0", 0, false},
		{"abc", 0, true},
	}
	for _, tt := range tests {
		got, err := parsePct(tt.in)
		if (err != nil) != tt.err {
			t.Errorf("parsePct(%q) err=%v, want err=%v", tt.in, err, tt.err)
		}
		if !tt.err && got != tt.want {
			t.Errorf("parsePct(%q) = %v, want %v", tt.in, got, tt.want)
		}
	}
}

func TestParseConnectNum(t *testing.T) {
	tests := []struct {
		in   string
		want int
	}{
		{"42", 42},
		{"", 0},
		{"  7  ", 7},
		{"abc", 0},
	}
	for _, tt := range tests {
		if got := parseConnectNum(tt.in); got != tt.want {
			t.Errorf("parseConnectNum(%q) = %v, want %v", tt.in, got, tt.want)
		}
	}
}

// TestHealthCheckUsesContext asserts the readiness probe respects the caller's
// deadline instead of hanging on a wedged router.
func TestHealthCheckUsesContext(t *testing.T) {
	block := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-block
	}))
	defer func() {
		close(block)
		srv.Close()
	}()

	c := NewCollector("ikuai", "slow-router", newTestClient(t, srv))
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	if err := c.HealthCheck(ctx); err == nil {
		t.Error("expected HealthCheck to fail once the context deadline passed")
	}
}
