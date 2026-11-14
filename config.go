// config.go —— exporter 的配置装配层。
//
// 缺省源唯一：Config 的 `default:` tag。所有 flag 的默认值一律清零——只有
// 用户真正显式传了的 flag（flag.Visit 只报被设置过的 flag）才覆写装配结果。
// 优先级恒为：显式 flag > env > 配置文件 > default tag。
package main

import (
	"flag"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/oekit/core/config"
)

// Config 是 exporter 的全部运行期配置。
//
// yaml 键 = 配置文件里的键；env 键 = 环境变量名（core/config 的默认命名
// 策略只认显式 env tag——没打 env tag 的字段不读 env）；default tag 是唯一
// 缺省源，flag 的缺省文案也从这里渲染。
type Config struct {
	Router          string        `yaml:"router"           default:"https://192.168.1.1" env:"IKUAI_ROUTER"`
	Token           string        `yaml:"token"            env:"IKUAI_TOKEN"`
	Username        string        `yaml:"username"         env:"IKUAI_USERNAME"`
	Password        string        `yaml:"password"         env:"IKUAI_PASSWORD"`
	RouterLabel     string        `yaml:"router_label"     env:"IKUAI_ROUTER_LABEL"`
	Listen          string        `yaml:"listen"           default:":9100"          env:"IKUAI_LISTEN"`
	MetricsPath     string        `yaml:"metrics_path"     default:"/metrics"        env:"IKUAI_METRICS_PATH"`
	Namespace       string        `yaml:"namespace"        default:"ikuai"          env:"IKUAI_NAMESPACE"`
	ShutdownTimeout time.Duration `yaml:"shutdown_timeout" default:"15s"            env:"IKUAI_SHUTDOWN_TIMEOUT"`
	Insecure        bool          `yaml:"insecure"         default:"true"           env:"IKUAI_INSECURE"`
	BreakerFailures uint          `yaml:"breaker_failures" default:"3"   env:"IKUAI_BREAKER_FAILURES"`
	BreakerCooldown time.Duration `yaml:"breaker_cooldown" default:"30s" env:"IKUAI_BREAKER_COOLDOWN"`
}

// 熔断参数的合法区间。
//
// 这两个区间**不用 range tag** 表达：core/config 的 LoadFile 内建 tag 接线会在
// 解码文件后立刻跑 ValidateTags——那一刻 env 与显式 flag 还没参与覆写，于是
// 「基线配置文件 + env/flag 覆盖」这条 12-factor 主线会被文件里的旧值当场毙掉
// （文件写 breaker_failures: 0、env 给 5 ⇒ 仍然 exit 2）。区间改为在最终值上
// 显式校验，与其它配置问题一起汇总。
const (
	minBreakerFailures = 1
	maxBreakerFailures = 100
	maxBreakerCooldown = 10 * time.Minute
)

// ConfigError 是配置问题的汇总错误：一次列全所有问题，而不是只报第一个。
//
// 汇总而非 fail-fast 是刻意的——运维改配置时最烦的是「改一个、跑一次、
// 再报下一个」。
type ConfigError struct {
	Problems []string
}

func (e *ConfigError) Error() string {
	return "配置有问题 / configuration problems:\n  - " + strings.Join(e.Problems, "\n  - ")
}

// validate 汇总所有配置问题。
//
// token 是唯一的必填项。username/password 的 deprecated 提示只在 token 缺失
// 时出现——v4-only SDK 一直以来的行为，原样保留（token 齐全时带
// username 并不报错）。
func (c *Config) validate() error {
	var problems []string
	if c.Token == "" {
		problems = append(problems,
			"token: 缺失 / missing（generate a personal API token in 系统设置 → 个人令牌）")
		if c.Username != "" || c.Password != "" {
			problems = append(problems,
				"username/password: v4-only SDK 不支持账号密码登录 / unsupported by the v4-only SDK, use -token")
		}
	}
	if c.Router == "" {
		problems = append(problems, "router: 缺失 / missing")
	}
	if c.BreakerFailures < minBreakerFailures || c.BreakerFailures > maxBreakerFailures {
		problems = append(problems, fmt.Sprintf(
			"breaker_failures: %d 不在 [%d,%d] 范围内 / out of range",
			c.BreakerFailures, minBreakerFailures, maxBreakerFailures))
	}
	if c.BreakerCooldown <= 0 || c.BreakerCooldown > maxBreakerCooldown {
		problems = append(problems, fmt.Sprintf(
			"breaker_cooldown: %s 不在 (0,%s] 范围内 / out of range",
			c.BreakerCooldown, maxBreakerCooldown))
	}
	if len(problems) == 0 {
		return nil
	}
	return &ConfigError{Problems: problems}
}

// defaultConfig 返回带 default tag 的缺省配置——缺省源只有一个，
// flag 的缺省文案也从这里渲染。
func defaultConfig() (Config, error) {
	var c Config
	if err := config.FillDefaults(&c); err != nil {
		return c, err
	}
	return c, nil
}

// flagTargets 持有各 flag 的解析结果。
//
// 默认值一律清零：缺省由 Config 的 default tag 提供，避免 flag 与 tag 两处
// 缺省漂移。
type flagTargets struct {
	router          string
	token           string
	username        string
	password        string
	routerLabel     string
	listen          string
	metricsPath     string
	namespace       string
	shutdownTimeout time.Duration
	insecure        bool
	breakerFailures uint
	breakerCooldown time.Duration
	configPath      string
}

// register 把 flag 注册到 fs，并返回「flag 名 → 写回 Config」的映射。
//
// d 是带 default tag 装配出来的缺省值，只用于渲染 flag 的缺省文案。
func (t *flagTargets) register(fs *flag.FlagSet, d Config) map[string]func(*Config) {
	fs.StringVar(&t.router, "router", "", fmt.Sprintf("iKuai router address (v4 REST API, HTTPS) (default %q)", d.Router))
	fs.StringVar(&t.token, "token", "", "Personal API token for the v4 REST API (required; generate it in 系统设置 → 个人令牌)")
	fs.StringVar(&t.username, "username", "", "(deprecated) v4-only SDK no longer supports username/password login; use -token")
	fs.StringVar(&t.password, "password", "", "(deprecated) v4-only SDK no longer supports username/password login; use -token")
	fs.StringVar(&t.routerLabel, "router-label", "", "Value of the 'router' label on every metric (defaults to the -router host)")
	fs.StringVar(&t.listen, "listen", "", fmt.Sprintf("Address to listen on for metrics (default %q)", d.Listen))
	fs.StringVar(&t.metricsPath, "path", "", fmt.Sprintf("Metrics path (default %q)", d.MetricsPath))
	fs.StringVar(&t.namespace, "namespace", "", fmt.Sprintf("Prometheus metrics namespace (default %q)", d.Namespace))
	fs.DurationVar(&t.shutdownTimeout, "shutdown-timeout", 0, fmt.Sprintf("Grace period for in-flight scrapes on SIGINT/SIGTERM (default %s)", d.ShutdownTimeout))
	fs.BoolVar(&t.insecure, "insecure", false, fmt.Sprintf("Skip TLS certificate verification (default %v)", d.Insecure))
	fs.UintVar(&t.breakerFailures, "breaker-failures", 0, fmt.Sprintf("Consecutive router failures that trip the circuit breaker (default %d)", d.BreakerFailures))
	fs.DurationVar(&t.breakerCooldown, "breaker-cooldown", 0, fmt.Sprintf("Cooldown before the circuit breaker half-opens (default %s)", d.BreakerCooldown))
	fs.StringVar(&t.configPath, "config", "", "Path to a YAML config file (env and explicit flags override its values)")

	return map[string]func(*Config){
		"router":           func(c *Config) { c.Router = t.router },
		"token":            func(c *Config) { c.Token = t.token },
		"username":         func(c *Config) { c.Username = t.username },
		"password":         func(c *Config) { c.Password = t.password },
		"router-label":     func(c *Config) { c.RouterLabel = t.routerLabel },
		"listen":           func(c *Config) { c.Listen = t.listen },
		"path":             func(c *Config) { c.MetricsPath = t.metricsPath },
		"namespace":        func(c *Config) { c.Namespace = t.namespace },
		"shutdown-timeout": func(c *Config) { c.ShutdownTimeout = t.shutdownTimeout },
		"insecure":         func(c *Config) { c.Insecure = t.insecure },
		"breaker-failures": func(c *Config) { c.BreakerFailures = t.breakerFailures },
		"breaker-cooldown": func(c *Config) { c.BreakerCooldown = t.breakerCooldown },
		// -config 只用于装配本身，不进 Config（配置文件的路径不该被下游
		// 当成运行期配置读走）。
		"config": func(*Config) {},
	}
}

// loadConfig 装配最终配置。
//
// 装配顺序：配置文件（或 default tag）→ env 覆写 → 显式 flag 覆写 → 统一
// 校验。校验放在 flag 覆写之后，这样越界的 flag 值也会被 range/options
// 拦住，而不是带着非法值上线。
func loadConfig(args []string, stderr io.Writer) (*Config, error) {
	// 缺省值：default tag 是唯一来源。
	d, err := defaultConfig()
	if err != nil {
		return nil, fmt.Errorf("load defaults: %w", err)
	}

	fs := flag.NewFlagSet("ikuai_exporter", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var t flagTargets
	setters := t.register(fs, d)

	if err := fs.Parse(args); err != nil {
		return nil, err
	}

	var cfg Config
	if t.configPath != "" {
		// 严格模式：未知键直接拒载，并给出该层合法键清单与近似拼写建议。
		// 配置文件是本次新增的能力，没有历史配置会被它弄坏。
		if err := config.LoadFile(t.configPath, &cfg, config.WithStrict()); err != nil {
			return nil, fmt.Errorf("load config file %s: %w", t.configPath, err)
		}
	} else {
		if err := config.FillDefaults(&cfg); err != nil {
			return nil, fmt.Errorf("load defaults: %w", err)
		}
	}

	// env 覆盖文件值（含显式零值：IKUAI_INSECURE=false 能关掉文件里的 true）。
	if err := config.ApplyEnvOverlay(&cfg); err != nil {
		return nil, fmt.Errorf("apply env overlay: %w", err)
	}

	// 只有用户显式传了的 flag 才覆写（flag.Visit 只报被设置过的 flag）。
	fs.Visit(func(f *flag.Flag) {
		if set, ok := setters[f.Name]; ok {
			set(&cfg)
		}
	})

	// 最终值校验（range/options）——放在 flag 覆写之后，flag 给的非法值
	// 同样 fail-closed。
	if err := config.ValidateTags(&cfg); err != nil {
		return nil, fmt.Errorf("validate config: %w", err)
	}

	if err := cfg.validate(); err != nil {
		return nil, err
	}
	return &cfg, nil
}
