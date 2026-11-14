package metrics

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestInterfaceUpFollowsLineCheckOnly pins the semantics of
// ikuai_interface_up: it mirrors iKuai's line check (iface_check), and iKuai
// only runs line checks on WAN lines. A real router answers with iface_check
// holding wan1 alone while iface_stream also lists the LAN interfaces, so
// every LAN interface is exported with up=0 while it is carrying traffic.
// Reading that 0 as "the link is down" is a false alarm, so this test locks
// the behaviour in instead of leaving it open to be "fixed" into something
// the router cannot report.
func TestInterfaceUpFollowsLineCheckOnly(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/monitoring/system"):
			_, _ = w.Write([]byte(systemOK))
		case strings.HasSuffix(r.URL.Path, "/monitoring/interfaces-status"):
			_, _ = w.Write([]byte(`{"code":0,"results":{` +
				`"iface_check":[{"interface":"wan1","result":"success"},{"interface":"wan2","result":"fail"}],` +
				`"iface_stream":[{"interface":"wan1","ip_addr":"1.2.3.4","comment":"wan"},` +
				`{"interface":"wan2","ip_addr":"5.6.7.8","comment":"wan2"},` +
				`{"interface":"lan1","ip_addr":"192.168.1.1","comment":"lan"}]}}`))
		case strings.HasSuffix(r.URL.Path, "/monitoring/clients-online"):
			_, _ = w.Write([]byte(`{"code":0,"results":{"data":[]}}`))
		default:
			_, _ = w.Write([]byte(`{"code":0,"results":{"data":null}}`))
		}
	}))
	defer srv.Close()

	c := NewCollector("ikuai", "10.0.0.1", newTestClient(t, srv))
	mfs := collect(t, c)
	mf, ok := mfs["ikuai_interface_up"]
	if !ok {
		t.Fatal("ikuai_interface_up was not emitted")
	}

	got := map[string]float64{}
	for _, m := range mf.GetMetric() {
		iface := ""
		for _, l := range m.GetLabel() {
			if l.GetName() == "interface" {
				iface = l.GetValue()
			}
		}
		got[iface] = m.GetGauge().GetValue()
	}

	want := map[string]float64{
		"wan1": 1, // line check succeeded
		"wan2": 0, // line check failed
		"lan1": 0, // no line check at all — not a down link
	}
	for iface, wantVal := range want {
		gotVal, ok := got[iface]
		if !ok {
			t.Errorf("ikuai_interface_up was not emitted for interface %q", iface)
			continue
		}
		if gotVal != wantVal {
			t.Errorf("ikuai_interface_up{interface=%q} = %v, want %v", iface, gotVal, wantVal)
		}
	}
}
