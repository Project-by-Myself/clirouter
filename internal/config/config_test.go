package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestZeroTimeoutsUseDefaults(t *testing.T) {
	c := Default()
	c.Timeouts = Timeouts{} // e.g. fields emptied in the dashboard
	c.Logging.RetentionDays = 0
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
	d := Default()
	if c.Timeouts.FirstByte != d.Timeouts.FirstByte || c.Timeouts.Idle != d.Timeouts.Idle || c.Timeouts.Total != d.Timeouts.Total || c.Timeouts.Connect != d.Timeouts.Connect {
		t.Fatalf("timeouts not defaulted: %+v", c.Timeouts)
	}
	if c.Timeouts.EarlyPing != 0 || c.Timeouts.Keepalive != 0 {
		t.Fatalf("early_ping/keepalive 0 must stay 0 (disabled): %+v", c.Timeouts)
	}
	if c.Logging.RetentionDays != d.Logging.RetentionDays {
		t.Fatalf("retention_days 0 would delete all history: %d", c.Logging.RetentionDays)
	}
}

func TestValidateRejectsBadValues(t *testing.T) {
	c := Default()
	c.Timeouts.Idle = Duration(-time.Second)
	c.Listen = "not-an-address"
	err := c.Validate()
	if err == nil || !strings.Contains(err.Error(), "idle") || !strings.Contains(err.Error(), "listen") {
		t.Fatalf("got %v", err)
	}
}

func TestLoadZeroTotalFromYAML(t *testing.T) {
	p := filepath.Join(t.TempDir(), "config.yaml")
	os.WriteFile(p, []byte("listen: 127.0.0.1:1\ntimeouts:\n  total: 0s\n"), 0o600)
	c, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if c.Timeouts.Total.D() != time.Hour {
		t.Fatalf("total = %v", c.Timeouts.Total.D())
	}
}
