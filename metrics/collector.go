package metrics

import (
	"context"
	"fmt"
	"log"
	"strconv"
	"strings"
	"sync"
	"time"

	ikuaiapi "github.com/zy84338719/ikuai-api"
	"github.com/zy84338719/ikuai-api/service"
	"github.com/zy84338719/ikuai-api/types"

	"github.com/prometheus/client_golang/prometheus"
)

const defaultTimeout = 15 * time.Second

type Collector struct {
	mu      sync.Mutex
	client  *ikuaiapi.Client
	monitor service.MonitorService
	system  service.SystemService

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

func NewCollector(ns string, client *ikuaiapi.Client) *Collector {
	lbl := func(name, help string, labels ...string) *prometheus.Desc {
		return prometheus.NewDesc(prometheus.BuildFQName(ns, "", name), help, labels, nil)
	}

	return &Collector{
		client:  client,
		monitor: service.NewMonitorService(client),
		system:  service.NewSystemService(client),

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

	if err := c.ensureLoggedIn(ctx); err != nil {
		log.Printf("login failed: %v", err)
		ch <- prometheus.MustNewConstMetric(c.up, prometheus.GaugeValue, 0)
		return
	}

	ch <- prometheus.MustNewConstMetric(c.up, prometheus.GaugeValue, 1)

	c.collectHomepage(ctx, ch)
	c.collectInterfaces(ctx, ch)
	c.collectLanDevices(ctx, ch)
}

func (c *Collector) ensureLoggedIn(ctx context.Context) error {
	if c.client.IsLoggedIn() {
		return nil
	}
	return c.client.Login(ctx)
}

func (c *Collector) collectHomepage(ctx context.Context, ch chan<- prometheus.Metric) {
	stat, err := c.system.GetHomepage(ctx)
	if err != nil {
		log.Printf("homepage: %v", err)
		return
	}

	ch <- gauge(c.uptime, float64(stat.Uptime))

	if stat.VerInfo.VerString != "" {
		ch <- gauge(c.version, 1,
			stat.VerInfo.Version,
			stat.VerInfo.Arch,
			stat.VerInfo.VerString,
			stat.VerInfo.ModelName,
		)
	}

	for i, s := range stat.CPU {
		if v, err := parsePct(s); err == nil {
			ch <- gauge(c.cpuUsage, v, fmt.Sprintf("core%d", i))
		}
	}

	if len(stat.CPUTemp) > 0 {
		ch <- gauge(c.cpuTemp, float64(stat.CPUTemp[0]))
	}

	if stat.Memory.Total > 0 {
		used := stat.Memory.Total - stat.Memory.Available
		ch <- gauge(c.memTotal, float64(stat.Memory.Total))
		ch <- gauge(c.memUsed, float64(used))
		ch <- gauge(c.memCached, float64(stat.Memory.Cached))
		ch <- gauge(c.memBuffers, float64(stat.Memory.Buffers))
	}

	ch <- gauge(c.onlineUsers, float64(stat.OnlineUser.Count))
}

func (c *Collector) collectInterfaces(ctx context.Context, ch chan<- prometheus.Metric) {
	ifaces, err := c.monitor.GetInterfaces(ctx)
	if err != nil {
		log.Printf("interfaces: %v", err)
		return
	}

	checkMap := buildCheckMap(ifaces.GetIFaceCheck())

	for _, s := range ifaces.GetIFaceStream() {
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

		connNum := parseConnectNum(s.ConnectNum)
		ch <- counter(c.ifaceUploadTotal, float64(s.TotalUp), iface, ipAddr, comment)
		ch <- counter(c.ifaceDlTotal, float64(s.TotalDown), iface, ipAddr, comment)
		ch <- gauge(c.ifaceUploadSpeed, float64(s.Upload), iface, ipAddr, comment)
		ch <- gauge(c.ifaceDlSpeed, float64(s.Download), iface, ipAddr, comment)
		ch <- gauge(c.ifaceConns, float64(connNum), iface, ipAddr, comment)
	}
}

func (c *Collector) collectLanDevices(ctx context.Context, ch chan<- prometheus.Metric) {
	devices, err := c.monitor.GetLanIP(ctx)
	if err != nil {
		log.Printf("lan devices: %v", err)
		return
	}

	ch <- gauge(c.devicesTotal, float64(len(devices)))

	for _, d := range devices {
		collectDevice(ch, c, d)
	}
}

func collectDevice(ch chan<- prometheus.Metric, c *Collector, d types.MonitorLanIPItem) {
	mac := d.Mac
	ip := d.IPAddr
	hostname := d.Hostname
	iface := d.Interface
	comment := d.Comment

	ch <- gauge(c.devInfo, 1, mac, hostname, ip, iface, comment)
	ch <- counter(c.devUpTotal, float64(d.TotalUp), mac, ip)
	ch <- counter(c.devDlTotal, float64(d.TotalDown), mac, ip)
	ch <- gauge(c.devUpSpeed, float64(d.Upload), mac, ip)
	ch <- gauge(c.devDlSpeed, float64(d.Download), mac, ip)
	ch <- gauge(c.devConns, float64(d.ConnectNum), mac, ip)
}

func buildCheckMap(checks []types.IFaceCheck) map[string]types.IFaceCheck {
	m := make(map[string]types.IFaceCheck, len(checks))
	for _, c := range checks {
		m[c.Interface] = c
	}
	return m
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
