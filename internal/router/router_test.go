package router

import (
	"testing"
	"time"

	"github.com/Project-by-Myself/clirouter/internal/config"
)

func cfg() *config.Config {
	c := config.Default()
	c.Providers = []config.Provider{
		{ID: "ant", Type: "anthropic", Keys: []string{"k1-aaaaaaaaaaaa", "k2-bbbbbbbbbbbb"}},
		{ID: "or", Type: "openai-compatible", BaseURL: "http://x/v1", Keys: []string{"k3-cccccccccccc"}, Models: []string{"glm-4.6"}, ModelMap: map[string]string{"fast": "glm-4.5-air"}, Priority: 1},
		{ID: "oai", Type: "openai", Keys: []string{"k4-dddddddddddd"}},
	}
	c.Combos = []config.Combo{{Name: "coding", Strategy: "fallback", Targets: []string{"ant/claude-sonnet-4-5", "or/glm-4.6"}}}
	c.Aliases = map[string]string{"sonnet": "claude-sonnet-4-5", "best": "coding"}
	if err := c.Validate(); err != nil {
		panic(err)
	}
	return c
}

func TestResolve(t *testing.T) {
	r := New(cfg(), nil, nil)
	cases := map[string][]string{
		"claude-sonnet-4-5": {"ant:claude-sonnet-4-5"},
		"sonnet":            {"ant:claude-sonnet-4-5"},
		"best":              {"ant:claude-sonnet-4-5", "or:glm-4.6"},
		"glm-4.6":           {"or:glm-4.6"},
		"fast":              {"or:glm-4.5-air"},
		"gpt-5":             {"oai:gpt-5"},
		"or/some-model":     {"or:some-model"},
	}
	for model, want := range cases {
		ts, err := r.Resolve(model)
		if err != nil {
			t.Fatalf("%s: %v", model, err)
		}
		var got []string
		for _, x := range ts {
			got = append(got, x.Provider.Cfg.ID+":"+x.Model)
		}
		if len(got) != len(want) {
			t.Fatalf("%s: got %v want %v", model, got, want)
		}
		for i := range want {
			if got[i] != want[i] {
				t.Fatalf("%s: got %v want %v", model, got, want)
			}
		}
	}
	if _, err := r.Resolve("llama-unknown"); err == nil {
		t.Fatal("expected error for unknown model")
	}
}

func TestPickCooldownAffinityAndReload(t *testing.T) {
	c := cfg()
	r := New(c, nil, nil)
	ts, _ := r.Resolve("claude-sonnet-4-5")
	tg := ts[0]
	a1, _ := r.Pick(tg, "", nil)
	a1.MarkFailure(tg.Model, FailRateLimit, time.Minute, "429")
	for i := 0; i < 5; i++ {
		a, _ := r.Pick(tg, "", nil)
		if a == a1 {
			t.Fatal("picked cooling account")
		}
	}
	other, _ := r.Pick(tg, "", nil)
	other.MarkFailure(tg.Model, FailRateLimit, 2*time.Second, "429")
	if a, wait := r.Pick(tg, "", nil); a != nil || wait <= 0 || wait > 2*time.Second {
		t.Fatalf("want nil + short wait, got %v %v", a, wait)
	}
	// Model-scoped: another model on the same account is still usable.
	if a, _ := r.Pick(Target{Provider: tg.Provider, Model: "claude-haiku"}, "", nil); a == nil {
		t.Fatal("model cooldown leaked to other models")
	}
	// Reload keeps state.
	r2 := New(c, r, nil)
	ts2, _ := r2.Resolve("claude-sonnet-4-5")
	if a, _ := r2.Pick(ts2[0], "", nil); a != nil {
		t.Fatal("cooldown lost on reload")
	}
	// Affinity.
	r3 := New(cfg(), nil, nil)
	ts3, _ := r3.Resolve("claude-sonnet-4-5")
	a, _ := r3.Pick(ts3[0], "sess", nil)
	r3.Bind("sess", a)
	for i := 0; i < 5; i++ {
		if b, _ := r3.Pick(ts3[0], "sess", nil); b != a {
			t.Fatal("affinity not honored")
		}
	}
}

func TestTransientErrorsNeedRepeatsToBench(t *testing.T) {
	r := New(cfg(), nil, nil)
	ts, _ := r.Resolve("glm-4.6")
	a, _ := r.Pick(ts[0], "", nil)
	a.MarkFailure("glm-4.6", FailServer, 0, "500")
	if b, _ := r.Pick(ts[0], "", nil); b == nil {
		t.Fatal("single 5xx must not bench the only account")
	}
	a.MarkFailure("glm-4.6", FailServer, 0, "500")
	a.MarkFailure("glm-4.6", FailServer, 0, "500")
	if b, _ := r.Pick(ts[0], "", nil); b != nil {
		t.Fatal("3 consecutive 5xx should bench briefly")
	}
	a.Reset()
	if b, _ := r.Pick(ts[0], "", nil); b == nil {
		t.Fatal("reset failed")
	}
}
