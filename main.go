// Command ikuai_exporter serves iKuai router metrics for Prometheus over the
// v4 REST API.
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	ikuaiapi "github.com/zy84338719/ikuai-api"
	"github.com/zy84338719/ikuai_exporter/metrics"

	"github.com/oekit/bootstrap"
	"github.com/oekit/logx"
	oekerrors "github.com/oekit/pkg/errors"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// readHeaderTimeout bounds how long a client may take to send request headers.
// Without it a stalled connection holds a goroutine open indefinitely.
const readHeaderTimeout = 5 * time.Second

// readyTimeout bounds the router probe behind /ready so a wedged router cannot
// pin kubelet's readiness request open.
const readyTimeout = 5 * time.Second

func main() {
	var (
		addr            string
		token           string
		username        string
		password        string
		routerLabel     string
		listenAddr      string
		metricsPath     string
		namespace       string
		shutdownTimeout time.Duration
		insecure        bool
	)

	flag.StringVar(&addr, "router", "https://192.168.1.1", "iKuai router address (v4 REST API, HTTPS)")
	flag.StringVar(&token, "token", "", "Personal API token for the v4 REST API (required; generate it in 系统设置 → 个人令牌)")
	flag.StringVar(&username, "username", "", "(deprecated) v4-only SDK no longer supports username/password login; use -token")
	flag.StringVar(&password, "password", "", "(deprecated) v4-only SDK no longer supports username/password login; use -token")
	flag.StringVar(&routerLabel, "router-label", "", "Value of the 'router' label on every metric (defaults to the -router host)")
	flag.StringVar(&listenAddr, "listen", ":9100", "Address to listen on for metrics")
	flag.StringVar(&metricsPath, "path", "/metrics", "Metrics path")
	flag.StringVar(&namespace, "namespace", "ikuai", "Prometheus metrics namespace")
	flag.DurationVar(&shutdownTimeout, "shutdown-timeout", 15*time.Second, "Grace period for in-flight scrapes on SIGINT/SIGTERM")
	flag.BoolVar(&insecure, "insecure", true, "Skip TLS certificate verification")
	flag.Parse()

	// The logging backend must be installed before anything else logs. Both
	// this binary and the embedded ikuai-api resolve their logger lazily
	// through logx.Delegating, so a single call here makes the SDK's events
	// (which carry the same trace context) structured too.
	logx.UseSlog(namespace)

	// oekit/core's lifecycle manager logs through slog.Default() rather than
	// logx (that package stays dependency-free), and bootstrap constructs the
	// manager internally with no logger-injection option. Pointing slog's
	// default at the same logx handler keeps the whole stream — including the
	// graceful-shutdown events — in one JSON format instead of interleaving
	// slog's default text handler with ours.
	slog.SetDefault(logx.Slogger(logx.WithService(namespace)))

	if token == "" {
		if username != "" || password != "" {
			logx.Error("username/password login is not supported by the v4-only SDK",
				"hint", "generate a personal API token in 系统设置 → 个人令牌 and pass -token")
			os.Exit(2)
		}
		logx.Error("missing -token",
			"hint", "generate a personal API token in 系统设置 → 个人令牌 and pass -token")
		os.Exit(2)
	}

	// The v4 REST API is served on HTTPS; auto-upgrade the scheme if the user
	// passed an http:// address.
	httpsAddr := metrics.ToHTTPS(addr)

	client, err := ikuaiapi.NewClient(httpsAddr,
		ikuaiapi.WithToken(token),
		ikuaiapi.WithInsecureSkipVerify(insecure),
	)
	if err != nil {
		logx.Error("create ikuai client", "addr", httpsAddr, "err", err)
		os.Exit(1)
	}

	// Default the router label to the host portion of the address.
	if routerLabel == "" {
		routerLabel = hostOf(httpsAddr)
	}

	collector := metrics.NewCollector(namespace, routerLabel, client)

	// Registering the client closer before the server means AddResource's
	// reverse-order close drains the listener first and only then releases the
	// HTTP client — no scrape can outlive the transport it is using.
	b, err := bootstrap.New(
		bootstrap.WithServiceName(namespace),
		bootstrap.WithShutdownTimeout(shutdownTimeout),
	)
	if err != nil {
		logx.Error("create bootstrapper", "err", err)
		_ = client.Close(context.Background())
		os.Exit(1)
	}

	registry := prometheus.NewRegistry()
	registry.MustRegister(collector)

	srv := &http.Server{
		Addr:              listenAddr,
		Handler:           newMux(registry, collector, metricsPath),
		ReadHeaderTimeout: readHeaderTimeout,
		// net/http recovers a handler panic itself and writes it through
		// ErrorLog — defaulting to plain text on stderr. Routing that into
		// logx keeps panics and TLS handshake errors inside the structured
		// JSON stream instead of interleaving unstructured lines with it.
		ErrorLog: slog.NewLogLogger(logx.SlogHandler(logx.WithService(namespace)), slog.LevelError),
	}

	if err := b.Run(func(app *bootstrap.App) error {
		app.AddResource("ikuai-api-client", func(ctx context.Context) error {
			if cerr := client.Close(ctx); cerr != nil {
				return oekerrors.Wrap(cerr, "close ikuai client")
			}
			return nil
		})
		app.AddResource("http-server", func(ctx context.Context) error {
			if serr := srv.Shutdown(ctx); serr != nil && !oekerrors.In(serr, http.ErrServerClosed) {
				return oekerrors.Wrap(serr, "shutdown http server")
			}
			return nil
		})

		// Bind before announcing readiness so a port conflict fails startup
		// loudly instead of leaving the pod "ready" but unserving.
		ln, lerr := net.Listen("tcp", listenAddr)
		if lerr != nil {
			return oekerrors.Wrap(lerr, "listen", "addr", listenAddr)
		}

		logx.Info("ikuai exporter listening",
			"addr", ln.Addr().String(),
			"path", metricsPath,
			"router", httpsAddr,
			"router_label", routerLabel,
			"namespace", namespace,
			"insecure", insecure,
		)
		app.Ready()

		if serr := srv.Serve(ln); serr != nil && !oekerrors.In(serr, http.ErrServerClosed) {
			return oekerrors.Wrap(serr, "serve metrics")
		}
		return nil
	}); err != nil {
		logx.Error("exporter stopped with error", "err", err)
		os.Exit(1)
	}
	logx.Info("ikuai exporter shut down cleanly")
}

// newMux wires the scrape endpoint and the kubelet probes. A dedicated mux is
// used rather than http.DefaultServeMux so the catch-all "/" cannot shadow a
// probe path added later.
func newMux(registry *prometheus.Registry, collector *metrics.Collector, metricsPath string) http.Handler {
	mux := http.NewServeMux()

	mux.Handle(metricsPath, promhttp.HandlerFor(registry, promhttp.HandlerOpts{
		EnableOpenMetrics: false,
	}))

	mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprintf(w, `<html><head><title>iKuai Exporter</title></head>
<body><h1>iKuai Prometheus Exporter</h1>
<p><a href="%s">Metrics</a></p>
</body></html>`, metricsPath)
	})

	// /live is a cheap liveness probe (process is up) and must never touch
	// the router — a flaky router must not get the pod restarted.
	mux.HandleFunc("/live", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	// /healthz kept as an alias of /live for backward compatibility.
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	// /ready is a readiness probe: it actually probes the router so kubelet
	// can detect a dead/misconfigured target and stop sending scrape traffic.
	mux.HandleFunc("/ready", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), readyTimeout)
		defer cancel()
		if err := collector.HealthCheck(ctx); err != nil {
			http.Error(w, "router not ready: "+err.Error(), http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ready"))
	})

	return mux
}

// hostOf extracts the host (no scheme, no port) for use as a default label.
func hostOf(rawURL string) string {
	if u, err := url.Parse(rawURL); err == nil && u.Hostname() != "" {
		return u.Hostname()
	}
	return strings.TrimPrefix(strings.TrimPrefix(rawURL, "https://"), "http://")
}
