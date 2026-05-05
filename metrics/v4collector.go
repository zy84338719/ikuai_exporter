package metrics

import (
	"context"
	"fmt"
	"log"
	"strings"
	"sync"

	ikuaiapi "github.com/zy84338719/ikuai-api"
	"github.com/zy84338719/ikuai-api/types"

	"github.com/prometheus/client_golang/prometheus"
)

// v4 REST API response wrappers (after V4Client unwraps the envelope).

type v4SystemResult struct {
	SysInfo types.HomepageSysStat `json:"sysinfo"`
}

type v4IfaceResult struct {
	IFaceCheck  []types.IFaceCheck  `json:"iface_check"`
	IFaceStream []types.IFaceStream `json:"iface_stream"`
}

type v4ClientsResult struct {
	Data []types.MonitorLanIPItem `json:"data"`
}

// V4Collector fetches metrics from the iKuai v4 REST API using a Bearer token.
// Only HTTPS is supported (the v4 REST API is served on port 443).
type V4Collector struct {
	mu     sync.Mutex
	client *ikuaiapi.V4Client

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

func NewV4Collector(ns string, client *ikuaiapi.V4Client) *V4Collector {
	lbl := func(name, help string, labels ...string) *prometheus.Desc {
		return prometheus.NewDesc(prometheus.BuildFQName(ns, "", name), help, labels, nil)
	}
	return &V4Collector{
		client:  client,
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

func (c *V4Collector) Describe(ch chan<- *prometheus.Desc) {
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

func (c *V4Collector) Collect(ch chan<- prometheus.Metric) {
	c.mu.Lock()
	defer c.mu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()

	var sys v4SystemResult
	if err := c.client.Get(ctx, "/monitoring/system", nil, &sys); err != nil {
		log.Printf("v4 /monitoring/system: %v", err)
		ch <- gauge(c.up, 0)
		return
	}
	ch <- gauge(c.up, 1)

	c.collectSystem(ch, &sys.SysInfo)

	var ifaces v4IfaceResult
	if err := c.client.Get(ctx, "/monitoring/interfaces-status", nil, &ifaces); err != nil {
		log.Printf("v4 /monitoring/interfaces-status: %v", err)
	} else {
		c.collectIfaces(ch, ifaces.IFaceCheck, ifaces.IFaceStream)
	}

	var clients v4ClientsResult
	if err := c.client.Get(ctx, "/monitoring/clients-online", nil, &clients); err != nil {
		log.Printf("v4 /monitoring/clients-online: %v", err)
	} else {
		c.collectClients(ch, clients.Data)
	}
}

func (c *V4Collector) collectSystem(ch chan<- prometheus.Metric, s *types.HomepageSysStat) {
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
		ch <- gauge(c.cpuTemp, float64(s.CPUTemp[0]))
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

func (c *V4Collector) collectIfaces(ch chan<- prometheus.Metric, checks []types.IFaceCheck, streams []types.IFaceStream) {
	checkMap := buildCheckMap(checks)
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

func (c *V4Collector) collectClients(ch chan<- prometheus.Metric, devices []types.MonitorLanIPItem) {
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
	return addr
}
