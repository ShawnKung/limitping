package metrics

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/ShawnKung/limitping/internal/usage"
)

func completeSnapshot() *usage.Snapshot {
	return &usage.Snapshot{
		Provider: "codex",
		Plan:     "plus",
		FiveHour: &usage.Window{
			UsedPercent: 25, RemainingPercent: 75, Active: true,
			ResetsAt: "2026-08-29T07:26:56+08:00", RemainingSeconds: 9000, WindowSeconds: 18000,
		},
		Weekly: &usage.Window{
			UsedPercent: 5, RemainingPercent: 95, Active: true,
			ResetsAt: "2026-09-04T00:27:18+08:00", RemainingSeconds: 500000, WindowSeconds: 604800,
		},
		ResetCredits: &usage.ResetCredits{
			AvailableCount: 1,
			Credits: []usage.ResetCredit{{
				Status: "available", ExpiresAt: "2026-09-21T06:31:00+08:00",
			}},
		},
		FetchedAt: "2026-08-29T03:00:00+08:00",
	}
}

func TestRenderMatchesExistingCollectorMetrics(t *testing.T) {
	lastPing := time.Unix(900, 0)
	payload, missing := Render(completeSnapshot(), 250*time.Millisecond, time.Unix(1000, 0), true, &lastPing)
	if len(missing) != 0 {
		t.Fatalf("missing metrics: %v", missing)
	}
	want := []string{
		"metrics_pusher_collector_success",
		"metrics_pusher_collector_duration_seconds",
		"metrics_pusher_last_run_timestamp_seconds",
		"limitping_window_used_ratio",
		"limitping_window_remaining_ratio",
		"limitping_window_active",
		"limitping_window_remaining_seconds",
		"limitping_window_seconds",
		"limitping_window_reset_timestamp_seconds",
		"limitping_limit_reached",
		"limitping_reset_credits_available",
		"limitping_reset_credit_expiration_timestamp_seconds",
		"limitping_usage_fetched_timestamp_seconds",
		"limitping_ping_completed",
		"limitping_last_successful_ping_timestamp_seconds",
		"limitping_collector_generated_timestamp_seconds",
	}
	for _, metric := range want {
		if !strings.Contains(payload, metric) {
			t.Errorf("payload missing %s", metric)
		}
	}
	if !strings.Contains(payload, `limitping_ping_completed{plan="plus",provider="codex"} 1`) {
		t.Fatalf("completed ping metric missing:\n%s", payload)
	}
	if !strings.Contains(payload, `limitping_last_successful_ping_timestamp_seconds{plan="plus",provider="codex"} 900.000`) {
		t.Fatalf("last successful ping metric missing:\n%s", payload)
	}
	if !strings.Contains(payload, `limitping_reset_credit_expiration_timestamp_seconds{plan="plus",provider="codex"}`) {
		t.Fatalf("reset credit expiration metric missing:\n%s", payload)
	}
}

func TestEarliestAvailableCreditExpiration(t *testing.T) {
	credits := &usage.ResetCredits{Credits: []usage.ResetCredit{
		{Status: "redeemed", ExpiresAt: "2026-08-30T00:00:00Z", RedeemedAt: "2026-08-29T00:00:00Z"},
		{Status: "available", ExpiresAt: "2026-09-21T00:00:00Z"},
		{Status: "available", ExpiresAt: "2026-09-14T00:00:00Z"},
	}}
	got, ok := earliestAvailableCreditExpiration(credits)
	want := float64(time.Date(2026, 9, 14, 0, 0, 0, 0, time.UTC).Unix())
	if !ok || got != want {
		t.Fatalf("expiration = %f, %v; want %f", got, ok, want)
	}
}

func TestRenderReportsMissingLastSuccessfulPing(t *testing.T) {
	_, missing := Render(completeSnapshot(), time.Second, time.Unix(1000, 0), false, nil)
	if !strings.Contains(strings.Join(missing, ","), "limitping_last_successful_ping_timestamp_seconds") {
		t.Fatalf("missing metrics = %v", missing)
	}
}

func TestDeliverUsesExistingGroupingAndPut(t *testing.T) {
	var request *http.Request
	client := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		request = req
		return &http.Response{StatusCode: http.StatusAccepted, Body: io.NopCloser(strings.NewReader("")), Header: make(http.Header)}, nil
	})}
	result, err := Deliver(context.Background(), completeSnapshot(), time.Second, Options{
		GatewayURL:    "http://push.example:9091/",
		Instance:      "mac mini",
		PingCompleted: true,
		HTTPClient:    client,
	})
	if err != nil {
		t.Fatal(err)
	}
	wantURL := "http://push.example:9091/metrics/job/limitping/instance/mac%20mini/collector/limitping"
	if result.URL != wantURL || request.URL.String() != wantURL {
		t.Fatalf("URL = %q, request = %q", result.URL, request.URL)
	}
	if request.Method != http.MethodPut {
		t.Fatalf("method = %s", request.Method)
	}
	if got := request.Header.Get("Content-Type"); got != "text/plain; version=0.0.4; charset=utf-8" {
		t.Fatalf("content type = %q", got)
	}
	if !result.Uploaded {
		t.Fatal("expected uploaded result")
	}
}

func TestValidateGatewayURL(t *testing.T) {
	for _, invalid := range []string{
		"", "push.example:9091", "ftp://push.example",
		"http://user:password@push.example", "http://push.example?token=secret", "http://push.example#fragment",
	} {
		if err := ValidateGatewayURL(invalid); err == nil {
			t.Errorf("expected %q to fail", invalid)
		}
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (fn roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return fn(request)
}
