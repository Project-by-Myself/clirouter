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

func TestCloudProviderValidation(t *testing.T) {
	ok := []Provider{
		{ID: "az", Type: "azure-openai", BaseURL: "https://res.openai.azure.com", Keys: []string{"k"}},
		{ID: "ol", Type: "ollama"},
		{ID: "br", Type: "bedrock", BaseURL: "https://bedrock-runtime.us-east-1.amazonaws.com", Keys: []string{"env:AWS_BEARER_TOKEN_BEDROCK", "aws:AKID:SECRET", "aws:AKID:SECRET:TOKEN", "aws:env"}},
		{ID: "br2", Type: "bedrock", BaseURL: "http://127.0.0.1:9999", Region: "eu-central-1", Keys: []string{"k"}},
		{ID: "vx", Type: "vertex", Keys: []string{"sa:/etc/sa.json", "sa:env:GOOGLE_SA_JSON", "AIzaKey"}, Region: "us-east5", Project: "my-proj"},
	}
	c := Default()
	c.Providers = ok
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
	if c.Providers[1].BaseURL != "http://127.0.0.1:11434/v1" {
		t.Fatalf("ollama default base_url = %q", c.Providers[1].BaseURL)
	}
	if r := c.Providers[2].BedrockRegion(); r != "us-east-1" {
		t.Fatalf("bedrock region %q", r)
	}
	if l := c.Providers[4].VertexLocation(); l != "us-east5" {
		t.Fatalf("vertex location %q", l)
	}
	if l := (&Provider{BaseURL: "https://asia-northeast1-aiplatform.googleapis.com"}).VertexLocation(); l != "asia-northeast1" {
		t.Fatalf("location from host %q", l)
	}
	if l := (&Provider{BaseURL: "https://aiplatform.googleapis.com"}).VertexLocation(); l != "global" {
		t.Fatalf("default location %q", l)
	}

	bad := map[string]Provider{
		"base_url is required":        {ID: "az", Type: "azure-openai", Keys: []string{"k"}},
		"AWS region":                  {ID: "br", Type: "bedrock", BaseURL: "https://example.com", Keys: []string{"k"}},
		"aws:<ACCESS_KEY_ID>":         {ID: "br", Type: "bedrock", BaseURL: "https://bedrock-runtime.us-east-1.amazonaws.com", Keys: []string{"aws:ONLYID"}},
		"only valid for type bedrock": {ID: "an", Type: "anthropic", Keys: []string{"aws:A:B"}},
		"only valid for type vertex":  {ID: "g", Type: "gemini", Keys: []string{"sa:/x.json"}},
		"sa:env:<VAR>":                {ID: "vx", Type: "vertex", Keys: []string{"sa:"}},
		"region":                      {ID: "vx", Type: "vertex", Region: "US East", Keys: []string{"k"}},
		"want http(s)":                {ID: "ol", Type: "ollama", BaseURL: "localhost:11434"},
	}
	for want, p := range bad {
		c := Default()
		c.Providers = []Provider{p}
		err := c.Validate()
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: got %v", want, err)
		}
		if err != nil && (strings.Contains(err.Error(), "ONLYID") || strings.Contains(err.Error(), "aws:A:B")) {
			t.Errorf("error echoes the key: %v", err)
		}
	}
}
