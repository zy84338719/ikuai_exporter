package main

import (
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"

	ikuaiapi "github.com/zy84338719/ikuai-api"
	"github.com/zy84338719/ikuai-exporter/metrics"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

func main() {
	var (
		addr        string
		username    string
		password    string
		token       string
		listenAddr  string
		metricsPath string
		namespace   string
		insecure    bool
	)

	flag.StringVar(&addr, "router", "http://192.168.1.1", "iKuai router address")
	flag.StringVar(&username, "username", "", "Router username (required unless -token is set)")
	flag.StringVar(&password, "password", "", "Router password (required unless -token is set)")
	flag.StringVar(&token, "token", "", "Personal API token for v4 REST API (HTTPS only, no username/password needed)")
	flag.StringVar(&listenAddr, "listen", ":9100", "Address to listen on for metrics")
	flag.StringVar(&metricsPath, "path", "/metrics", "Metrics path")
	flag.StringVar(&namespace, "namespace", "ikuai", "Prometheus metrics namespace")
	flag.BoolVar(&insecure, "insecure", true, "Skip TLS certificate verification")
	flag.Parse()

	if token == "" && (username == "" || password == "") {
		log.Fatal("provide either -token (v4 REST) or both -username and -password (v3/v4 session)")
	}

	var collector prometheus.Collector

	if token != "" {
		// v4 REST mode: token-only, no login needed.
		// The v4 REST API runs on HTTPS; auto-upgrade the scheme if needed.
		httpsAddr := metrics.ToHTTPS(addr)
		v4client := ikuaiapi.NewV4RESTClient(httpsAddr, token,
			ikuaiapi.WithV4RawMode(false),
		)
		collector = metrics.NewV4Collector(namespace, v4client)
		log.Printf("v4 REST mode  addr=%s", httpsAddr)
	} else {
		// Session mode: username/password login via /Action/login.
		// Works for both v3 and v4 routers (version auto-detected).
		opts := []ikuaiapi.ClientOption{ikuaiapi.WithInsecureSkipVerify(insecure)}
		client := ikuaiapi.NewClient(addr, username, password, opts...)
		collector = metrics.NewCollector(namespace, client)
		log.Printf("session mode  addr=%s  username=%s", addr, username)
	}

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
	http.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ok"))
	})

	log.Printf("listening on %s%s", listenAddr, metricsPath)
	if err := http.ListenAndServe(listenAddr, nil); err != nil {
		log.Printf("server error: %v", err)
		os.Exit(1)
	}
}
