// Command ikuai_exporter serves iKuai router metrics for Prometheus over the
// v4 REST API.
package main

import (
	"context"
	"errors"
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
	oekmetrics "github.com/oekit/metrics"
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
	// The logging backend must be installed before anything logs — including
	// the configuration error path below. The namespace is not known yet
	// (it comes from the config), so the default one goes first and is
	// replaced right after the config is assembled.
	d, derr := defaultConfig()
	if derr != nil {
		fmt.Fprintln(os.Stderr, "load defaults:", derr)
		os.Exit(2)
	}
	logx.UseSlog(d.Namespace)

	cfg, err := loadConfig(os.Args[1:], os.Stderr)
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			os.Exit(0)
		}
		logx.Error("invalid configuration", "err", err)
		os.Exit(2)
	}

	// Now that the namespace is known, point the logging backend at it.
	// Both this binary and the embedded ikuai-api resolve their logger
	// lazily through logx.Delegating, so a single call here makes the SDK's
	// events (which carry the same trace context) structured too.
	logx.UseSlog(cfg.Namespace)

	// oekit/core's lifecycle manager logs through slog.Default() rather than
	// logx (that package stays dependency-free), and bootstrap constructs the
	// manager internally with no logger-injection option. Pointing slog's
	// default at the same logx handler keeps the whole stream — including the
	// graceful-shutdown events — in one JSON format instead of interleaving
	// slog's default text handler with ours.
	slog.SetDefault(logx.Slogger(logx.WithService(cfg.Namespace)))

	// The v4 REST API is served on HTTPS; auto-upgrade the scheme if the user
	// passed an http:// address.
	httpsAddr := metrics.ToHTTPS(cfg.Router)

	// Default the router label to the host portion of the address.
	routerLabel := cfg.RouterLabel
	if routerLabel == "" {
		routerLabel = hostOf(httpsAddr)
	}

	// The SDK talks to the router through the httpx bridge: the circuit
	// breaker, the request metrics and the TLS policy all live in the
	// transport. ikuaiapi.WithInsecureSkipVerify is deliberately NOT used —
	// it type-asserts *http.Transport, so with a replaced transport it
	// fails silently; skip-verify is carried by httpx.WithTLSSkipVerify.
	rt, err := newRouterTransport(cfg, routerLabel)
	if err != nil {
		logx.Error("create router transport", "err", err)
		os.Exit(1)
	}

	client, err := ikuaiapi.NewClient(httpsAddr,
		ikuaiapi.WithToken(cfg.Token),
		ikuaiapi.WithHTTPClient(&http.Client{Transport: rt}),
	)
	if err != nil {
		logx.Error("create ikuai client", "addr", httpsAddr, "err", err)
		closeTransport(rt)
		os.Exit(1)
	}

	collector := metrics.NewCollector(cfg.Namespace, routerLabel, client)

	// Registering the client closer before the server means AddResource's
	// reverse-order close drains the listener first and only then releases the
	// HTTP client — no scrape can outlive the transport it is using.
	b, err := bootstrap.New(
		bootstrap.WithServiceName(cfg.Namespace),
		bootstrap.WithShutdownTimeout(cfg.ShutdownTimeout),
	)
	if err != nil {
		logx.Error("create bootstrapper", "err", err)
		_ = client.Close(context.Background())
		os.Exit(1)
	}

	// One registry for the whole process: the exporter's own collector, the
	// go/process collectors, the build-info gauges and (from Task 5) the
	// httpx request/resilience metrics all land on oekit/metrics.Registry,
	// and /metrics gathers exactly that one. A second private registry would
	// split the scrape surface in two.
	oekmetrics.Init(cfg.Namespace)
	if err := oekmetrics.Registry.Register(collector); err != nil {
		logx.Error("register collector", "err", err)
		os.Exit(1)
	}

	srv := &http.Server{
		Addr:              cfg.Listen,
		Handler:           newMux(oekmetrics.Registry, collector, cfg.MetricsPath),
		ReadHeaderTimeout: readHeaderTimeout,
		// net/http recovers a handler panic itself and writes it through
		// ErrorLog — defaulting to plain text on stderr. Routing that into
		// logx keeps panics and TLS handshake errors inside the structured
		// JSON stream instead of interleaving unstructured lines with it.
		ErrorLog: slog.NewLogLogger(logx.SlogHandler(logx.WithService(cfg.Namespace)), slog.LevelError),
	}

	if err := b.Run(func(app *bootstrap.App) error {
		// Registration order is the reverse of the close order: the
		// transport is registered first so it is closed last — no scrape
		// can outlive the transport it is using, and the client is closed
		// before the transport it rides on.
		app.AddResource("router-transport", func(context.Context) error {
			closeTransport(rt)
			return nil
		})
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
		ln, lerr := net.Listen("tcp", cfg.Listen)
		if lerr != nil {
			return oekerrors.Wrap(lerr, "listen", "addr", cfg.Listen)
		}

		logx.Info("ikuai exporter listening",
			"addr", ln.Addr().String(),
			"path", cfg.MetricsPath,
			"router", httpsAddr,
			"router_label", routerLabel,
			"namespace", cfg.Namespace,
			"insecure", cfg.Insecure,
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
func newMux(gatherer prometheus.Gatherer, collector *metrics.Collector, metricsPath string) http.Handler {
	mux := http.NewServeMux()

	mux.Handle(metricsPath, promhttp.HandlerFor(gatherer, promhttp.HandlerOpts{
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
