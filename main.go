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
		forceV3     bool
	)

	flag.StringVar(&addr, "router", "http://192.168.1.1", "iKuai router address (e.g. http://10.10.10.254)")
	flag.StringVar(&username, "username", "admin", "Router username")
	flag.StringVar(&password, "password", "admin", "Router password")
	flag.StringVar(&token, "token", "", "Router token for v4 (optional)")
	flag.StringVar(&listenAddr, "listen", ":9100", "Address to listen on for metrics")
	flag.StringVar(&metricsPath, "path", "/metrics", "Metrics path")
	flag.StringVar(&namespace, "namespace", "ikuai", "Prometheus metrics namespace")
	flag.BoolVar(&insecure, "insecure", true, "Skip TLS certificate verification")
	flag.BoolVar(&forceV3, "v3", false, "Force v3 protocol (skip auto-detection)")
	flag.Parse()

	opts := []ikuaiapi.ClientOption{}
	if insecure {
		opts = append(opts, ikuaiapi.WithInsecureSkipVerify(true))
	}
	if forceV3 {
		opts = append(opts, ikuaiapi.WithVersion(ikuaiapi.VersionV3))
	}
	if token != "" {
		opts = append(opts, ikuaiapi.WithToken(token))
	}

	client := ikuaiapi.NewClient(addr, username, password, opts...)

	collector := metrics.NewCollector(namespace, client)
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

	log.Printf("iKuai exporter listening on %s%s  router=%s", listenAddr, metricsPath, addr)
	if err := http.ListenAndServe(listenAddr, nil); err != nil {
		log.Printf("server error: %v", err)
		os.Exit(1)
	}
}
