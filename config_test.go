package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// writeConfigFile 落一份配置文件，返回路径。
func writeConfigFile(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write config file: %v", err)
	}
	return path
}

// TestLoadConfig_Precedence 覆盖三层优先级：配置文件 < env < 显式 flag。
//
// 这是本次配置管线的核心契约——三层各自都能改值，且压过下一层。
func TestLoadConfig_Precedence(t *testing.T) {
	path := writeConfigFile(t, ""+
		"router: https://10.0.0.1\n"+
		"token: file-token\n"+
		"listen: :9111\n")

	t.Run("file only", func(t *testing.T) {
		cfg, err := loadConfig([]string{"-config", path}, os.Stderr)
		if err != nil {
			t.Fatalf("loadConfig: %v", err)
		}
		if cfg.Router != "https://10.0.0.1" {
			t.Errorf("router = %q, want https://10.0.0.1", cfg.Router)
		}
		if cfg.Token != "file-token" {
			t.Errorf("token = %q, want file-token", cfg.Token)
		}
		if cfg.Listen != ":9111" {
			t.Errorf("listen = %q, want :9111", cfg.Listen)
		}
		// 文件没写的字段回落到 default tag。
		if cfg.Namespace != "ikuai" {
			t.Errorf("namespace = %q, want ikuai", cfg.Namespace)
		}
		if cfg.BreakerFailures != 3 {
			t.Errorf("breaker failures = %d, want 3", cfg.BreakerFailures)
		}
		if cfg.BreakerCooldown != 30*time.Second {
			t.Errorf("breaker cooldown = %s, want 30s", cfg.BreakerCooldown)
		}
	})

	t.Run("env beats file", func(t *testing.T) {
		t.Setenv("IKUAI_TOKEN", "env-token")
		t.Setenv("IKUAI_LISTEN", ":9222")
		cfg, err := loadConfig([]string{"-config", path}, os.Stderr)
		if err != nil {
			t.Fatalf("loadConfig: %v", err)
		}
		if cfg.Token != "env-token" {
			t.Errorf("token = %q, want env-token", cfg.Token)
		}
		if cfg.Listen != ":9222" {
			t.Errorf("listen = %q, want :9222", cfg.Listen)
		}
	})

	t.Run("flag beats env", func(t *testing.T) {
		t.Setenv("IKUAI_TOKEN", "env-token")
		cfg, err := loadConfig([]string{"-config", path, "-token", "flag-token"}, os.Stderr)
		if err != nil {
			t.Fatalf("loadConfig: %v", err)
		}
		if cfg.Token != "flag-token" {
			t.Errorf("token = %q, want flag-token", cfg.Token)
		}
	})
}

// TestLoadConfig_NoConfigFile 不用 -config 时，缺省全部来自 default tag。
func TestLoadConfig_NoConfigFile(t *testing.T) {
	cfg, err := loadConfig([]string{"-token", "t"}, os.Stderr)
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	if cfg.Router != "https://192.168.1.1" {
		t.Errorf("router = %q, want https://192.168.1.1", cfg.Router)
	}
	if cfg.Listen != ":9100" {
		t.Errorf("listen = %q, want :9100", cfg.Listen)
	}
	if cfg.MetricsPath != "/metrics" {
		t.Errorf("metrics path = %q, want /metrics", cfg.MetricsPath)
	}
	if cfg.ShutdownTimeout != 15*time.Second {
		t.Errorf("shutdown timeout = %s, want 15s", cfg.ShutdownTimeout)
	}
	if !cfg.Insecure {
		t.Error("insecure = false, want true (default)")
	}
}

// TestLoadConfig_MissingRequired 必填项缺失时一次性列全问题。
func TestLoadConfig_MissingRequired(t *testing.T) {
	_, err := loadConfig([]string{}, os.Stderr)
	if err == nil {
		t.Fatal("want error for missing token")
	}
	var ce *ConfigError
	if !errors.As(err, &ce) {
		t.Fatalf("want *ConfigError, got %T", err)
	}
	if len(ce.Problems) == 0 {
		t.Fatal("want aggregated problems")
	}
	joined := strings.Join(ce.Problems, "|")
	if !strings.Contains(joined, "token") {
		t.Errorf("problems = %v, want token mentioned", ce.Problems)
	}
}

// TestLoadConfig_DeprecatedCredentials 保留旧语义：username/password 只在
// token 缺失时才是问题（v4-only SDK 一直以来的行为）。
func TestLoadConfig_DeprecatedCredentials(t *testing.T) {
	_, err := loadConfig([]string{"-username", "admin", "-password", "x"}, os.Stderr)
	if err == nil {
		t.Fatal("want error")
	}
	msg := err.Error()
	if !strings.Contains(msg, "token") || !strings.Contains(msg, "username/password") {
		t.Errorf("error = %v, want token + username/password mentioned", msg)
	}

	if _, err := loadConfig([]string{"-token", "t", "-username", "admin"}, os.Stderr); err != nil {
		t.Fatalf("token present must not trigger deprecated complaint: %v", err)
	}
}

// TestLoadConfig_UnknownKeyRejected 配置文件里的未知键直接拒载——拼错的键
// 静默忽略会让配置「看着生效、其实没生效」。
func TestLoadConfig_UnknownKeyRejected(t *testing.T) {
	path := writeConfigFile(t, ""+
		"token: t\n"+
		"router: https://10.0.0.1\n"+
		"listen_addres: :9100\n")

	_, err := loadConfig([]string{"-config", path}, os.Stderr)
	if err == nil {
		t.Fatal("want error for unknown key")
	}
	if !strings.Contains(err.Error(), "listen_addres") {
		t.Errorf("error = %v, want unknown key named", err)
	}
}

// TestLoadConfig_RangeViolation 超出 range 的值 fail-closed。
func TestLoadConfig_RangeViolation(t *testing.T) {
	_, err := loadConfig([]string{"-token", "t", "-breaker-failures", "0"}, os.Stderr)
	if err == nil {
		t.Fatal("want error for out-of-range breaker failures")
	}
}
