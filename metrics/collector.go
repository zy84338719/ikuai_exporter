package metrics

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strconv"
	"strings"
	"sync"
	"time"

	ikuaiapi "github.com/zy84338719/ikuai-api"
	"github.com/zy84338719/ikuai-api/service"

	"github.com/prometheus/client_golang/prometheus"
)

const defaultTimeout = 15 * time.Second

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

// v4SystemData mirrors /monitoring/system's data object.
type v4SystemData struct {
	Uptime     int64         `json:"uptime"`
	VerInfo    v4VerInfo     `json:"verinfo"`
	CPU        []string      `json:"cpu"`
	CPUTemp    []float64     `json:"cputemp"`
	Memory     v4MemInfo     `json:"mem"`
	OnlineUser v4OnlineUser  `json:"online_user"`
	SysInfo    *v4SystemData `json:"sysinfo,omitempty"` // some firmwares nest under sysinfo
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
type Collector struct {
	mu          sync.Mutex
	client      *ikuaiapi.Client
	monitoring  *service.MonitoringService
	apiBaseTime time.Time

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
func NewCollector(ns string, client *ikuaiapi.Client) *Collector {
	api := service.NewAPIClient(client)
	lbl := func(name, help string, labels ...string) *prometheus.Desc {
		return prometheus.NewDesc(prometheus.BuildFQName(ns, "", name), help, labels, nil)
	}

	return &Collector{
		client:     client,
		monitoring: api.Monitoring(),

		up:      lbl("up", "1 if the router is reachable, 0 otherwise"),
		uptime:  lbl("uptime_seconds", "Router uptime in seconds"),
		version: lbl("version_info", "Router version info (always 1)", "version", "arch", "ver_string", "model_name"),

		cpuUsage: lbl("cpu_usage_ratio", "CPU core usage ratio (0–1)", "core"),
		cpuTemp:  lbl("cpu_temperature_celsius", "CPU temperature in Celsius"),

		memTotal:   lbl("memory_total_kibibytes", "Total memory in KiB"),
		memUsed:    lbl("memory_used_kibibytes", "Used memory in KiB"),
		memCached:  lbl("memory_cached_kibibytes", "Cached memory in KiB"),
		memBuffers: lbl("memory_buffers_kibibytes", "Buffers memory in KiB"),

		onlineUsers: lbl("online_users_total", "Number of online users"),

		ifaceUp:          lbl("interface_up", "1 if interface link check succeeded", "interface", "ip_addr", "comment"),
		ifaceUploadTotal: lbl("interface_upload_bytes_total", "Total bytes uploaded via interface", "interface", "ip_addr", "comment"),
		ifaceDlTotal:     lbl("interface_download_bytes_total", "Total bytes downloaded via interface", "interface", "ip_addr", "comment"),
		ifaceUploadSpeed: lbl("interface_upload_speed_bytes", "Current upload speed in bytes/s", "interface", "ip_addr", "comment"),
		ifaceDlSpeed:     lbl("interface_download_speed_bytes", "Current download speed in bytes/s", "interface", "ip_addr", "comment"),
		ifaceConns:       lbl("interface_connections", "Active connections via interface", "interface", "ip_addr", "comment"),

		devicesTotal: lbl("devices_online_total", "Number of online LAN devices"),
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

func (c *Collector) Collect(ch chan<- prometheus.Metric) {
	c.mu.Lock()
	defer c.mu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()

	raw, err := c.monitoring.GetMonitoringSystem(ctx)
	if err != nil {
		log.Printf("v4 /monitoring/system: %v", err)
		ch <- gauge(c.up, 0)
		return
	}
	ch <- gauge(c.up, 1)

	var sys v4SystemData
	if err := json.Unmarshal(raw, &sys); err != nil {
		log.Printf("decode /monitoring/system: %v", err)
		return
	}
	// Some firmaments nest the payload under a "sysinfo" key.
	if sys.SysInfo != nil && sys.Uptime == 0 && sys.SysInfo.Uptime != 0 {
		sys = *sys.SysInfo
	}
	c.collectSystem(ch, &sys)

	if raw, err := c.monitoring.GetMonitoringInterfacesStatus(ctx); err != nil {
		log.Printf("v4 /monitoring/interfaces-status: %v", err)
	} else {
		var ifaces v4InterfacesData
		if err := json.Unmarshal(raw, &ifaces); err != nil {
			log.Printf("decode /monitoring/interfaces-status: %v", err)
		} else {
			c.collectIfaces(ch, ifaces.IFaceCheck, ifaces.IFaceStream)
		}
	}

	if raw, err := c.monitoring.GetMonitoringClientsOnline(ctx); err != nil {
		log.Printf("v4 /monitoring/clients-online: %v", err)
	} else {
		var clients []v4Client
		if err := json.Unmarshal(raw, &clients); err != nil {
			log.Printf("decode /monitoring/clients-online: %v", err)
		} else {
			c.collectClients(ch, clients)
		}
	}
}

func (c *Collector) collectSystem(ch chan<- prometheus.Metric, s *v4SystemData) {
	ch <- gauge(c.uptime, float64(s.Uptime))

	if s.VerInfo.VerString != "" {
		ch <- gauge(c.version, 1,
			s.VerInfo.Version, s.VerInfo.Arch, s.VerInfo.VerString, s.VerInfo.ModelName)
	}

	for i, cpu := range s.CPU {
		if v, err := parsePct(cpu); err == nil {
			ch <- gauge(c.cpuUsage, v, fmt.Sprintf("core%d", i))
		}
	}
	if len(s.CPUTemp) > 0 {
		ch <- gauge(c.cpuTemp, s.CPUTemp[0])
	}

	if s.Memory.Total > 0 {
		used := s.Memory.Total - s.Memory.Available
		ch <- gauge(c.memTotal, float64(s.Memory.Total))
		ch <- gauge(c.memUsed, float64(used))
		ch <- gauge(c.memCached, float64(s.Memory.Cached))
		ch <- gauge(c.memBuffers, float64(s.Memory.Buffers))
	}

	ch <- gauge(c.onlineUsers, float64(s.OnlineUser.Count))
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
		ch <- gauge(c.ifaceUp, upVal, iface, ipAddr, comment)
		ch <- counter(c.ifaceUploadTotal, float64(s.TotalUp), iface, ipAddr, comment)
		ch <- counter(c.ifaceDlTotal, float64(s.TotalDown), iface, ipAddr, comment)
		ch <- gauge(c.ifaceUploadSpeed, float64(s.Upload), iface, ipAddr, comment)
		ch <- gauge(c.ifaceDlSpeed, float64(s.Download), iface, ipAddr, comment)
		ch <- gauge(c.ifaceConns, float64(parseConnectNum(s.ConnectNum)), iface, ipAddr, comment)
	}
}

func (c *Collector) collectClients(ch chan<- prometheus.Metric, devices []v4Client) {
	ch <- gauge(c.devicesTotal, float64(len(devices)))
	for _, d := range devices {
		mac, ip := d.Mac, d.IPAddr
		ch <- gauge(c.devInfo, 1, mac, d.Hostname, ip, d.Interface, d.Comment)
		ch <- counter(c.devUpTotal, float64(d.TotalUp), mac, ip)
		ch <- counter(c.devDlTotal, float64(d.TotalDown), mac, ip)
		ch <- gauge(c.devUpSpeed, float64(d.Upload), mac, ip)
		ch <- gauge(c.devDlSpeed, float64(d.Download), mac, ip)
		ch <- gauge(c.devConns, float64(d.ConnectNum), mac, ip)
	}
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
