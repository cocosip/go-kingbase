package kingbase

import (
	"strings"
	"testing"
)

func TestEnvironmentFallbackFillsAbsentFields(t *testing.T) {
	t.Setenv("KINGBASE_HOST", "envhost")
	t.Setenv("KINGBASE_PORT", "6543")
	t.Setenv("KINGBASE_USER", "envuser")
	t.Setenv("KINGBASE_SSLMODE", "disable")
	cfg := mustParse(t, "dbname=tenant")
	if cfg.Host != "envhost" || cfg.Port != 6543 {
		t.Errorf("endpoint = %q:%d, want envhost:6543", cfg.Host, cfg.Port)
	}
	if cfg.User != "envuser" {
		t.Errorf("User = %q, want envuser", cfg.User)
	}
	if cfg.SSLMode != SSLDisable {
		t.Errorf("SSLMode = %v, want disable", cfg.SSLMode)
	}
}

func TestEnvironmentFallbackExplicitValuesWin(t *testing.T) {
	t.Setenv("KINGBASE_HOST", "envhost")
	t.Setenv("KINGBASE_PORT", "6543")
	cfg := mustParse(t, "host=dsnhost port=5555 user=alice")
	if cfg.Host != "dsnhost" || cfg.Port != 5555 {
		t.Errorf("endpoint = %q:%d, want dsnhost:5555", cfg.Host, cfg.Port)
	}
}

func TestEnvironmentFallbackKeepsEmptyPassword(t *testing.T) {
	t.Setenv("KINGBASE_PASSWORD", "envsecret")
	empty := mustParse(t, "user=alice password=")
	if empty.Password != "" {
		t.Errorf("explicit empty password became %q", empty.Password)
	}
	inherited := mustParse(t, "user=alice")
	if inherited.Password != "envsecret" {
		t.Errorf("inherited password = %q, want the environment value", inherited.Password)
	}
}

func TestEnvironmentFallbackIsCaseInsensitive(t *testing.T) {
	t.Setenv("kingbase_user", "envuser")
	cfg := mustParse(t, "host=h")
	if cfg.User != "envuser" {
		t.Errorf("User = %q, want envuser from a lower-case variable", cfg.User)
	}
}

func TestEnvironmentFallbackRejectsConflicts(t *testing.T) {
	t.Setenv("KINGBASE_MODE", "pg")
	t.Setenv("KINGBASE_COMPATIBILITYMODE", "mysql")
	_, err := ParseConfig("user=alice")
	if err == nil || !strings.Contains(err.Error(), "conflicting environment variables") {
		t.Fatalf("ParseConfig = %v, want a conflict error", err)
	}
}

func TestEnvironmentFallbackRejectsInvalidValue(t *testing.T) {
	t.Setenv("KINGBASE_PORT", "nope")
	_, err := ParseConfig("user=alice")
	if err == nil || !strings.Contains(err.Error(), "KINGBASE_PORT") || !strings.Contains(err.Error(), "invalid port") {
		t.Fatalf("ParseConfig = %v, want a KINGBASE_PORT validation error", err)
	}
}

func TestEnvironmentFallbackIgnoresUnknownVariables(t *testing.T) {
	t.Setenv("KINGBASE_UNKNOWN_SETTING", "x")
	cfg := mustParse(t, "host=h user=alice")
	if cfg.Host != "h" || cfg.User != "alice" {
		t.Errorf("config = %+v, want the DSN values", cfg)
	}
}

func TestEnvironmentEmptyHostUsesDefault(t *testing.T) {
	t.Setenv("KINGBASE_HOST", "")
	cfg := mustParse(t, "user=alice")
	if cfg.Host != "localhost" {
		t.Errorf("Host = %q, want the local default", cfg.Host)
	}
}
