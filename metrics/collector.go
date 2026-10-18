package metrics

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	ikuaiapi "github.com/zy84338719/ikuai-api"
	"github.com/zy84338719/ikuai-api/service"

	"github.com/oekit/logx"
	oekerrors "github.com/oekit/pkg/errors"

	"github.com/prometheus/client_golang/prometheus"
)

// log is a lazily-resolved package logger. Delegating (rather than capturing
// Default() at init) is what makes it follow the backend main installs via
// logx.UseSlog — a package-level Default() would be frozen before main runs,
// and would diverge from the ikuai-api logger, which uses the same trick.
var log = logx.Delegating("ikuai-exporter/metrics")

// logAPIError logs err with the iKuai business code when the SDK produced a
// BizError. Since v1.2.0 every APIError embeds one, so a router rejection
// ("token expired", code 3007) reads as such instead of as a bare transport
// string.
func logAPIError(ctx context.Context, msg string, err error) {
	if be, ok := oekerrors.AsBizError(err); ok {
		log.CtxError(ctx, msg,
			"biz_code", be.BizCode,
			"http_code", be.HTTPCode,
			"reason", be.Message,
			"err", err,
		)
		return
	}
	log.CtxError(ctx, msg, "err", err)
}

const (
	defaultTimeout = 15 * time.Second

	// clientPageSize is the per-page size requested from the paged
	// /monitoring/clients-online endpoint.
	clientPageSize = 100

	// maxClientPages bounds the paging walk. At the default page size that is
	// 10k clients, far beyond any household or small-office router, and it
	// keeps a misbehaving router that keeps echoing a full page from spinning
	// the collector forever.
	maxClientPages = 100
)

// v4 REST API response shapes.
//
// The v4-only SDK returns the inner data payload of each endpoint as a
// json.RawMessage; the exporter decodes it into these locally-defined
// structs (the SDK intentionally ships no typed models). Field names and
// nesting match what the iKuai v4 firmware actually emits.

type v4VerInfo struct {
	Version   string `json:"version"`
	Arch      string `json:"arch"`
	VerString string `json:"ver_string"`
	ModelName string `json:"model_name"`
}

type v4MemInfo struct {
	Total     int64 `json:"total"`
	Available int64 `json:"avail"`
	Cached    int64 `json:"cached"`
	Buffers   int64 `json:"buffers"`
}

type v4OnlineUser struct {
	Count int `json:"count"`
}

// v4SystemStream mirrors sysinfo.stream on /monitoring/system — real-time
// connection counters and totals (a point value, unlike the /connections
// timeseries endpoint).
type v4SystemStream struct {
	ConnectNum int `json:"connect_num"`
	TCP        int `json:"tcp_connect_num"`
	UDP        int `json:"udp_connect_num"`
	ICMP       int `json:"icmp_connect_num"`
}

// v4SystemData mirrors /monitoring/system's data object.
// The router returns {"results":{"sysinfo":{...}}} — after the SDK unwraps the
// envelope the collector sees {"sysinfo":{...}}, so the real fields live one
// level down inside SysInfo.
type v4SystemData struct {
	Uptime     int64          `json:"uptime"`
	VerInfo    v4VerInfo      `json:"verinfo"`
	CPU        []string       `json:"cpu"`
	CPUTemp    []float64      `json:"cputemp"`
	Memory     v4MemInfo      `json:"memory"`
	OnlineUser v4OnlineUser   `json:"online_user"`
	Stream     v4SystemStream `json:"stream"`
	SysInfo    *v4SystemData  `json:"sysinfo,omitempty"` // v4 nests the payload here
}

// v4IFaceCheck is one entry of /monitoring/interfaces-status[iface_check].
type v4IFaceCheck struct {
	Interface string `json:"interface"`
	Result    string `json:"result"`
}

type v4IFaceStream struct {
	Interface  string `json:"interface"`
	IPAddr     string `json:"ip_addr"`
	Comment    string `json:"comment"`
	TotalUp    int64  `json:"total_up"`
	TotalDown  int64  `json:"total_down"`
	Upload     int64  `json:"upload"`
	Download   int64  `json:"download"`
	ConnectNum string `json:"connect_num"`
}

// v4InterfacesData mirrors /monitoring/interfaces-status data object.
type v4InterfacesData struct {
	IFaceCheck  []v4IFaceCheck  `json:"iface_check"`
	IFaceStream []v4IFaceStream `json:"iface_stream"`
}

// v4Client mirrors one entry of /monitoring/clients-online.
type v4Client struct {
	Mac        string `json:"mac"`
	Hostname   string `json:"hostname"`
	IPAddr     string `json:"ip_addr"`
	Interface  string `json:"interface"`
	Comment    string `json:"comment"`
	TotalUp    int64  `json:"total_up"`
	TotalDown  int64  `json:"total_down"`
	Upload     int64  `json:"upload"`
	Download   int64  `json:"download"`
	ConnectNum int    `json:"connect_num"`
}

// Collector fetches metrics from the iKuai v4 REST API using a Bearer token.
// Every series carries a "router" label identifying the target instance, so a
// single Prometheus job scraping multiple exporters (or one exporter per
// router) can distinguish them.
type Collector struct {
	mu         sync.Mutex
	router     string // value of the "router" label
	client     *ikuaiapi.Client
	monitoring *service.MonitoringService

	up      *prometheus.Desc
	uptime  *prometheus.Desc
	version *prometheus.Desc

	cpuUsage *prometheus.Desc
	cpuTemp  *prometheus.Desc

	memTotal   *prometheus.Desc
	memUsed    *prometheus.Desc
	memCached  *prometheus.Desc
	memBuffers *prometheus.Desc

	onlineUsers *prometheus.Desc

	connections      *prometheus.Desc
	connectionsProto *prometheus.Desc

	ifaceUp          *prometheus.Desc
	ifaceUploadTotal *prometheus.Desc
	ifaceDlTotal     *prometheus.Desc
	ifaceUploadSpeed *prometheus.Desc
	ifaceDlSpeed     *prometheus.Desc
	ifaceConns       *prometheus.Desc

	devicesTotal *prometheus.Desc
	devInfo      *prometheus.Desc
	devUpTotal   *prometheus.Desc
	devDlTotal   *prometheus.Desc
	devUpSpeed   *prometheus.Desc
	devDlSpeed   *prometheus.Desc
	devConns     *prometheus.Desc
}

// NewCollector builds a v4-only collector wired to the given SDK client.
// router is the value used for the "router" label on every series; pass an
// empty string to omit the dimension (legacy single-instance behaviour).
func NewCollector(ns, router string, client *ikuaiapi.Client) *Collector {
	api := service.NewAPIClient(client)
	// lbl builds a Desc whose label set always ends with "router".
	lbl := func(name, help string, labels ...string) *prometheus.Desc {
		all := append(append([]string{}, labels...), "router")
		return prometheus.NewDesc(prometheus.BuildFQName(ns, "", name), help, all, nil)
	}
	// lblN builds a Desc with no variable labels except router.
	lblN := func(name, help string) *prometheus.Desc {
		return prometheus.NewDesc(prometheus.BuildFQName(ns, "", name), help, []string{"router"}, nil)
	}

	return &Collector{
		router:     router,
		client:     client,
		monitoring: api.Monitoring(),

		up:      lblN("up", "1 if the router is reachable, 0 otherwise"),
		uptime:  lblN("uptime_seconds", "Router uptime in seconds"),
		version: lbl("version_info", "Router version info (always 1)", "version", "arch", "ver_string", "model_name"),

		cpuUsage: lbl("cpu_usage_ratio", "CPU core usage ratio (0–1)", "core"),
		cpuTemp:  lblN("cpu_temperature_celsius", "CPU temperature in Celsius"),

		memTotal:   lblN("memory_total_kibibytes", "Total memory in KiB"),
		memUsed:    lblN("memory_used_kibibytes", "Used memory in KiB"),
		memCached:  lblN("memory_cached_kibibytes", "Cached memory in KiB"),
		memBuffers: lblN("memory_buffers_kibibytes", "Buffers memory in KiB"),

		onlineUsers: lblN("online_users_total", "Number of online users"),

		connections:      lblN("connections_total", "Total active connections"),
		connectionsProto: lbl("connections", "Active connections by protocol/state", "protocol"),

		ifaceUp:          lbl("interface_up", "1 if interface link check succeeded", "interface", "ip_addr", "comment"),
		ifaceUploadTotal: lbl("interface_upload_bytes_total", "Total bytes uploaded via interface", "interface", "ip_addr", "comment"),
		ifaceDlTotal:     lbl("interface_download_bytes_total", "Total bytes downloaded via interface", "interface", "ip_addr", "comment"),
		ifaceUploadSpeed: lbl("interface_upload_speed_bytes", "Current upload speed in bytes/s", "interface", "ip_addr", "comment"),
		ifaceDlSpeed:     lbl("interface_download_speed_bytes", "Current download speed in bytes/s", "interface", "ip_addr", "comment"),
		ifaceConns:       lbl("interface_connections", "Active connections via interface", "interface", "ip_addr", "comment"),

		devicesTotal: lblN("devices_online_total", "Number of online LAN devices"),
		devInfo:      lbl("device_info", "LAN device info (always 1)", "mac", "hostname", "ip_addr", "interface", "comment"),
		devUpTotal:   lbl("device_upload_bytes_total", "Total bytes uploaded by device", "mac", "ip_addr"),
		devDlTotal:   lbl("device_download_bytes_total", "Total bytes downloaded by device", "mac", "ip_addr"),
		devUpSpeed:   lbl("device_upload_speed_bytes", "Current upload speed in bytes/s for device", "mac", "ip_addr"),
		devDlSpeed:   lbl("device_download_speed_bytes", "Current download speed in bytes/s for device", "mac", "ip_addr"),
		devConns:     lbl("device_connections", "Active connections for device", "mac", "ip_addr"),
	}
}

func (c *Collector) Describe(ch chan<- *prometheus.Desc) {
	ch <- c.up
	ch <- c.uptime
	ch <- c.version
	ch <- c.cpuUsage
	ch <- c.cpuTemp
	ch <- c.memTotal
	ch <- c.memUsed
	ch <- c.memCached
	ch <- c.memBuffers
	ch <- c.onlineUsers
	ch <- c.connections
	ch <- c.connectionsProto
	ch <- c.ifaceUp
	ch <- c.ifaceUploadTotal
	ch <- c.ifaceDlTotal
	ch <- c.ifaceUploadSpeed
	ch <- c.ifaceDlSpeed
	ch <- c.ifaceConns
	ch <- c.devicesTotal
	ch <- c.devInfo
	ch <- c.devUpTotal
	ch <- c.devDlTotal
	ch <- c.devUpSpeed
	ch <- c.devDlSpeed
	ch <- c.devConns
}

// r appends the router label value to a label-value slice.
func (c *Collector) r(labels ...string) []string {
	return append(labels, c.router)
}

func (c *Collector) Collect(ch chan<- prometheus.Metric) {
	c.mu.Lock()
	defer c.mu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()

	raw, err := c.monitoring.GetMonitoringSystem(ctx)
	if err != nil {
		logAPIError(ctx, "v4 /monitoring/system failed", err)
		ch <- gauge(c.up, 0, c.router)
		return
	}
	ch <- gauge(c.up, 1, c.router)

	var sys v4SystemData
	if err := json.Unmarshal(raw, &sys); err != nil {
		log.CtxError(ctx, "decode /monitoring/system", "err", err)
		return
	}
	if sys.SysInfo != nil && sys.Uptime == 0 && sys.SysInfo.Uptime != 0 {
		sys = *sys.SysInfo
	}
	c.collectSystem(ch, &sys)

	if raw, err := c.monitoring.GetMonitoringInterfacesStatus(ctx); err != nil {
		logAPIError(ctx, "v4 /monitoring/interfaces-status failed", err)
	} else {
		var ifaces v4InterfacesData
		if err := json.Unmarshal(raw, &ifaces); err != nil {
			log.CtxError(ctx, "decode /monitoring/interfaces-status", "err", err)
		} else {
			c.collectIfaces(ch, ifaces.IFaceCheck, ifaces.IFaceStream)
		}
	}

	devices, err := c.fetchAllOnlineClients(ctx)
	if err != nil {
		logAPIError(ctx, "v4 /monitoring/clients-online failed", err)
		return
	}
	c.collectClients(ch, devices)
}

// fetchAllOnlineClients walks every page of /monitoring/clients-online.
//
// The catalog marks this endpoint "paged", and the unpaged
// GetMonitoringClientsOnline sends no limit at all — so it returns whatever
// single page the router considers default. Since ikuai-api v1.2.0 fixed the
// paging key to `limit` (the SDK used to send the ignored `page_size`), an
// explicit limit+page walk is what makes devices_online_total mean "all
// online clients" rather than "the first N".
func (c *Collector) fetchAllOnlineClients(ctx context.Context) ([]v4Client, error) {
	var out []v4Client
	for page := 1; page <= maxClientPages; page++ {
		raw, err := c.monitoring.QueryMonitoringClientsOnline(ctx, &service.MonitoringClientsOnlineQueryOptions{
			Page:     page,
			PageSize: clientPageSize,
		})
		if err != nil {
			// A failure past the first page still leaves usable data; only a
			// first-page failure means we have nothing.
			if page > 1 && len(out) > 0 {
				logAPIError(ctx, "clients-online paging stopped early",
					oekerrors.Wrap(err, "paging clients-online", "page", page, "collected", len(out)))
				return out, nil
			}
			return nil, err
		}

		batch, err := decodeClientPage(raw)
		if err != nil {
			if len(out) > 0 {
				log.CtxError(ctx, "decode clients-online page stopped early",
					"page", page, "collected", len(out), "err", err)
				return out, nil
			}
			return nil, err
		}
		out = append(out, batch...)

		// A short (or empty) page means the collection is exhausted.
		if len(batch) < clientPageSize {
			return out, nil
		}
	}
	log.CtxWarn(ctx, "clients-online paging hit the page cap; client list may be truncated",
		"pages", maxClientPages, "page_size", clientPageSize, "collected", len(out))
	return out, nil
}

// decodeClientPage peels whichever wrapping shape the router used. The v4 API
// answers lists three ways — a bare array, {"data": [...]}, and
// {"<group>_data": [...], "<group>_total": n} (the object-group / DHCP list
// style) — and the firmware is not consistent about which, so all three are
// accepted rather than betting on one.
func decodeClientPage(raw json.RawMessage) ([]v4Client, error) {
	var bare []v4Client
	if err := json.Unmarshal(raw, &bare); err == nil {
		return bare, nil
	}

	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err != nil {
		return nil, oekerrors.Wrap(err, "clients-online payload is neither an array nor an object")
	}
	if inner, ok := obj["data"]; ok {
		var out []v4Client
		if err := json.Unmarshal(inner, &out); err != nil {
			return nil, oekerrors.Wrap(err, "clients-online data key is not an array")
		}
		return out, nil
	}
	for key, inner := range obj {
		if strings.HasSuffix(key, "_data") {
			var out []v4Client
			if err := json.Unmarshal(inner, &out); err != nil {
				return nil, oekerrors.Wrap(err, "clients-online collection key is not an array", "key", key)
			}
			return out, nil
		}
	}
	return nil, oekerrors.New("clients-online response had no recognisable collection key")
}

func (c *Collector) collectSystem(ch chan<- prometheus.Metric, s *v4SystemData) {
	ch <- gauge(c.uptime, float64(s.Uptime), c.router)

	if s.VerInfo.VerString != "" {
		ch <- gauge(c.version, 1, c.r(s.VerInfo.Version, s.VerInfo.Arch, s.VerInfo.VerString, s.VerInfo.ModelName)...)
	}

	for i, cpu := range s.CPU {
		if v, err := parsePct(cpu); err == nil {
			ch <- gauge(c.cpuUsage, v, c.r(fmt.Sprintf("core%d", i))...)
		}
	}
	if len(s.CPUTemp) > 0 {
		ch <- gauge(c.cpuTemp, s.CPUTemp[0], c.router)
	}

	if s.Memory.Total > 0 {
		used := s.Memory.Total - s.Memory.Available
		ch <- gauge(c.memTotal, float64(s.Memory.Total), c.router)
		ch <- gauge(c.memUsed, float64(used), c.router)
		ch <- gauge(c.memCached, float64(s.Memory.Cached), c.router)
		ch <- gauge(c.memBuffers, float64(s.Memory.Buffers), c.router)
	}

	ch <- gauge(c.onlineUsers, float64(s.OnlineUser.Count), c.router)

	// Connection counters from sysinfo.stream (point value, not the timeseries
	// /monitoring/connections endpoint).
	if s.Stream.ConnectNum > 0 {
		ch <- gauge(c.connections, float64(s.Stream.ConnectNum), c.router)
		if s.Stream.TCP > 0 {
			ch <- gauge(c.connectionsProto, float64(s.Stream.TCP), c.r("tcp")...)
		}
		if s.Stream.UDP > 0 {
			ch <- gauge(c.connectionsProto, float64(s.Stream.UDP), c.r("udp")...)
		}
		if s.Stream.ICMP > 0 {
			ch <- gauge(c.connectionsProto, float64(s.Stream.ICMP), c.r("icmp")...)
		}
	}
}

func (c *Collector) collectIfaces(ch chan<- prometheus.Metric, checks []v4IFaceCheck, streams []v4IFaceStream) {
	checkMap := make(map[string]v4IFaceCheck, len(checks))
	for _, ck := range checks {
		checkMap[ck.Interface] = ck
	}
	for _, s := range streams {
		iface := s.Interface
		ipAddr := s.IPAddr
		comment := s.Comment
		if comment == "" {
			comment = iface
		}

		upVal := 0.0
		if chk, ok := checkMap[iface]; ok && chk.Result == "success" {
			upVal = 1.0
		}
		ch <- gauge(c.ifaceUp, upVal, c.r(iface, ipAddr, comment)...)
		ch <- counter(c.ifaceUploadTotal, float64(s.TotalUp), c.r(iface, ipAddr, comment)...)
		ch <- counter(c.ifaceDlTotal, float64(s.TotalDown), c.r(iface, ipAddr, comment)...)
		ch <- gauge(c.ifaceUploadSpeed, float64(s.Upload), c.r(iface, ipAddr, comment)...)
		ch <- gauge(c.ifaceDlSpeed, float64(s.Download), c.r(iface, ipAddr, comment)...)
		ch <- gauge(c.ifaceConns, float64(parseConnectNum(s.ConnectNum)), c.r(iface, ipAddr, comment)...)
	}
}

func (c *Collector) collectClients(ch chan<- prometheus.Metric, devices []v4Client) {
	ch <- gauge(c.devicesTotal, float64(len(devices)), c.router)
	for _, d := range devices {
		mac, ip := d.Mac, d.IPAddr
		ch <- gauge(c.devInfo, 1, c.r(mac, d.Hostname, ip, d.Interface, d.Comment)...)
		ch <- counter(c.devUpTotal, float64(d.TotalUp), c.r(mac, ip)...)
		ch <- counter(c.devDlTotal, float64(d.TotalDown), c.r(mac, ip)...)
		ch <- gauge(c.devUpSpeed, float64(d.Upload), c.r(mac, ip)...)
		ch <- gauge(c.devDlSpeed, float64(d.Download), c.r(mac, ip)...)
		ch <- gauge(c.devConns, float64(d.ConnectNum), c.r(mac, ip)...)
	}
}

// HealthCheck performs a single probe against the router to verify the token
// and connectivity. Used by the /ready endpoint.
func (c *Collector) HealthCheck(ctx context.Context) error {
	_, err := c.monitoring.GetMonitoringSystem(ctx)
	return err
}

// ToHTTPS ensures the address uses https:// (v4 REST API requires HTTPS).
func ToHTTPS(addr string) string {
	addr = strings.TrimRight(addr, "/")
	if strings.HasPrefix(addr, "http://") {
		return "https://" + addr[7:]
	}
	if !strings.HasPrefix(addr, "https://") {
		return "https://" + addr
	}
	return addr
}

func gauge(desc *prometheus.Desc, val float64, labels ...string) prometheus.Metric {
	return prometheus.MustNewConstMetric(desc, prometheus.GaugeValue, val, labels...)
}

func counter(desc *prometheus.Desc, val float64, labels ...string) prometheus.Metric {
	return prometheus.MustNewConstMetric(desc, prometheus.CounterValue, val, labels...)
}

func parsePct(s string) (float64, error) {
	s = strings.TrimSuffix(strings.TrimSpace(s), "%")
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0, err
	}
	return v / 100, nil
}

// ConnectNum in IFaceStream is a string (e.g. "42").
func parseConnectNum(s string) int {
	n, _ := strconv.Atoi(strings.TrimSpace(s))
	return n
}
