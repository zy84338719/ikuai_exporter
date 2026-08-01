package metrics

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	ikuaiapi "github.com/zy84338719/ikuai-api"

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

func TestCollectSystem(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(r.URL.Path, "/monitoring/system"):
			_, _ = w.Write([]byte(`{"code":0,"results":{"sysinfo":{"uptime":3600,"cpu":["12.5%","7.0%"],"cputemp":[42.6],"memory":{"total":1000000,"avail":400000,"cached":50000,"buffers":30000},"online_user":{"count":3},"stream":{"connect_num":128,"tcp_connect_num":80,"udp_connect_num":40,"icmp_connect_num":2},"verinfo":{"version":"4.0.1","arch":"x86","ver_string":"4.0.1 x64","model_name":"X10"}}}}`))
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
