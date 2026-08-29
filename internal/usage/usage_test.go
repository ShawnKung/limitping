package usage

import (
	"context"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ShawnKung/limitping/internal/auth"
)

func TestClassifyWindowsByDuration(t *testing.T) {
	now := time.Unix(1000, 0)
	five, weekly := classifyWindows(rateLimit{
		Primary:   &backendWindow{UsedPercent: 100, LimitWindowSeconds: 18000, ResetAt: 2000},
		Secondary: &backendWindow{UsedPercent: 4, LimitWindowSeconds: 604800, ResetAt: 3000},
	}, now)
	if five == nil || five.WindowSeconds != 18000 || five.UsedPercent != 100 {
		t.Fatalf("bad five-hour window: %#v", five)
	}
	if !five.Active {
		t.Fatal("used window with a future reset should be active")
	}
	if weekly == nil || weekly.WindowSeconds != 604800 || weekly.UsedPercent != 4 {
		t.Fatalf("bad weekly window: %#v", weekly)
	}
}

func TestUsageURL(t *testing.T) {
	if got := usageURL("https://chatgpt.com/backend-api"); got != "https://chatgpt.com/backend-api/wham/usage" {
		t.Fatalf("got %q", got)
	}
}

func TestClientRead(t *testing.T) {
	now := time.Unix(1_000, 0)
	httpClient := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if got := r.Header.Get("Authorization"); got != "Bearer test-token" {
			t.Fatalf("authorization = %q", got)
		}
		if got := r.Header.Get("ChatGPT-Account-Id"); got != "account-1" {
			t.Fatalf("account header = %q", got)
		}
		var body string
		switch r.URL.Path {
		case "/api/codex/usage":
			body = `{"plan_type":"plus","rate_limit":{"limit_reached":false,"primary_window":{"used_percent":25,"limit_window_seconds":18000,"reset_at":2000},"secondary_window":{"used_percent":5,"limit_window_seconds":604800,"reset_at":3000}},"rate_limit_reset_credits":{"available_count":1}}`
		case "/wham/rate-limit-reset-credits":
			if r.Header.Get("OpenAI-Beta") != "codex-1" || r.Header.Get("originator") != "Codex Desktop" {
				t.Fatalf("missing reset-credit headers: %#v", r.Header)
			}
			body = `{"available_count":1,"credits":[{"status":"available","granted_at":"2026-08-22T06:31:00+08:00","expires_at":"2026-09-21T06:31:00+08:00"}]}`
		default:
			t.Fatalf("path = %q", r.URL.Path)
		}
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
	})}

	dir := t.TempDir()
	authPath := filepath.Join(dir, "auth.json")
	if err := os.WriteFile(authPath, []byte(`{"tokens":{"access_token":"test-token","refresh_token":"refresh","account_id":"account-1"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	a := &auth.Codex{Path: authPath, Client: httpClient}
	client := &Client{Auth: a, HTTP: httpClient, BaseURL: "https://example.test", Now: func() time.Time { return now }}
	snapshot, err := client.Read(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Plan != "plus" || snapshot.FiveHour == nil || snapshot.FiveHour.UsedPercent != 25 {
		t.Fatalf("snapshot = %#v", snapshot)
	}
	if snapshot.ResetCredits == nil || snapshot.ResetCredits.AvailableCount != 1 || len(snapshot.ResetCredits.Credits) != 1 {
		t.Fatalf("reset credits = %#v", snapshot.ResetCredits)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (fn roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return fn(request)
}
