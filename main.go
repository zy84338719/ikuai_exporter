package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	ikuaiapi "github.com/zy84338719/ikuai-api"
	"github.com/zy84338719/ikuai_exporter/metrics"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

func main() {
	var (
		addr        string
		token       string
		username    string
		password    string
		routerLabel string
		listenAddr  string
		metricsPath string
		namespace   string
		insecure    bool
	)

	flag.StringVar(&addr, "router", "https://192.168.1.1", "iKuai router address (v4 REST API, HTTPS)")
	flag.StringVar(&token, "token", "", "Personal API token for the v4 REST API (required; generate it in 系统设置 → 个人令牌)")
	flag.StringVar(&username, "username", "", "(deprecated) v4-only SDK no longer supports username/password login; use -token")
	flag.StringVar(&password, "password", "", "(deprecated) v4-only SDK no longer supports username/password login; use -token")
	flag.StringVar(&routerLabel, "router-label", "", "Value of the 'router' label on every metric (defaults to the -router host)")
	flag.StringVar(&listenAddr, "listen", ":9100", "Address to listen on for metrics")
	flag.StringVar(&metricsPath, "path", "/metrics", "Metrics path")
	flag.StringVar(&namespace, "namespace", "ikuai", "Prometheus metrics namespace")
	flag.BoolVar(&insecure, "insecure", true, "Skip TLS certificate verification")
	flag.Parse()

	if token == "" {
		if username != "" || password != "" {
			log.Fatal("the v4-only SDK dropped username/password login. Generate a personal API token in the router UI (系统设置 → 个人令牌) and pass it with -token.")
		}
		log.Fatal("missing -token: generate a personal API token in the router UI (系统设置 → 个人令牌) and pass it with -token.")
	}

	// The v4 REST API is served on HTTPS; auto-upgrade the scheme if the user
	// passed an http:// address.
	httpsAddr := metrics.ToHTTPS(addr)

	client, err := ikuaiapi.NewClient(httpsAddr,
		ikuaiapi.WithToken(token),
		ikuaiapi.WithInsecureSkipVerify(insecure),
	)
	if err != nil {
		log.Fatalf("create ikuai client: %v", err)
	}

	// Default the router label to the host portion of the address.
	if routerLabel == "" {
		routerLabel = hostOf(httpsAddr)
	}

	collector := metrics.NewCollector(namespace, routerLabel, client)
	log.Printf("v4 REST mode  addr=%s  router=%s", httpsAddr, routerLabel)

	registry := prometheus.NewRegistry()
	registry.MustRegister(collector)

	http.Handle(metricsPath, promhttp.HandlerFor(registry, promhttp.HandlerOpts{
		EnableOpenMetrics: false,
	}))
	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `<html><head><title>iKuai Exporter</title></head>
<body><h1>iKuai Prometheus Exporter</h1>
<p><a href="%s">Metrics</a></p>
</body></html>`, metricsPath)
	})
	// /live is a cheap liveness probe (process is up).
	http.HandleFunc("/live", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	// /healthz kept as an alias of /live for backward compatibility.
	http.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	// /ready is a readiness probe: it actually probes the router so kubelet
	// can detect a dead/misconfigured target and stop sending scrape traffic.
	http.HandleFunc("/ready", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()
		if err := collector.HealthCheck(ctx); err != nil {
			http.Error(w, "router not ready: "+err.Error(), http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ready"))
	})

	log.Printf("listening on %s%s", listenAddr, metricsPath)
	if err := http.ListenAndServe(listenAddr, nil); err != nil {
		log.Printf("server error: %v", err)
		os.Exit(1)
	}
}

// hostOf extracts the host (no scheme, no port) for use as a default label.
func hostOf(rawURL string) string {
	if u, err := url.Parse(rawURL); err == nil && u.Hostname() != "" {
		return u.Hostname()
	}
	return strings.TrimPrefix(strings.TrimPrefix(rawURL, "https://"), "http://")
}
