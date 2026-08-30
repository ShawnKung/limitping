package metrics

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/ShawnKung/limitping/internal/usage"
)

type Options struct {
	GatewayURL         string
	Instance           string
	DryRun             bool
	PingCompleted      bool
	LastSuccessfulPing *time.Time
	HTTPClient         *http.Client
}

type Result struct {
	URL            string
	Payload        string
	MissingMetrics []string
	Uploaded       bool
}

func Deliver(ctx context.Context, snapshot *usage.Snapshot, collectionDuration time.Duration, options Options) (Result, error) {
	result := Result{}
	if strings.TrimSpace(options.GatewayURL) == "" {
		return result, fmt.Errorf("--push-metric 需要 Pushgateway endpoint")
	}
	instance := strings.TrimSpace(options.Instance)
	if instance == "" {
		var err error
		instance, err = os.Hostname()
		if err != nil || instance == "" {
			return result, fmt.Errorf("读取当前主机名: %w", err)
		}
	}
	var err error
	result.URL, err = groupingURL(options.GatewayURL, instance)
	if err != nil {
		return result, err
	}
	result.Payload, result.MissingMetrics = Render(snapshot, collectionDuration, time.Now(), options.PingCompleted, options.LastSuccessfulPing)
	if options.DryRun {
		return result, nil
	}

	client := options.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, result.URL, strings.NewReader(result.Payload))
	if err != nil {
		return result, err
	}
	req.Header.Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	resp, err := client.Do(req)
	if err != nil {
		return result, fmt.Errorf("推送指标: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusAccepted {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return result, fmt.Errorf("Pushgateway 返回 HTTP %d: %s", resp.StatusCode, bytes.TrimSpace(body))
	}
	result.Uploaded = true
	return result, nil
}

func ValidateGatewayURL(gatewayURL string) error {
	_, err := groupingURL(gatewayURL, "validation")
	return err
}

func groupingURL(gatewayURL, instance string) (string, error) {
	baseURL := strings.TrimRight(strings.TrimSpace(gatewayURL), "/")
	base, err := url.Parse(baseURL)
	if err != nil || (base.Scheme != "http" && base.Scheme != "https") || base.Host == "" {
		return "", fmt.Errorf("--push-metric 必须是合法的 http:// 或 https:// URL")
	}
	if base.User != nil || base.RawQuery != "" || base.Fragment != "" {
		return "", fmt.Errorf("--push-metric 不能包含用户信息、查询参数或 fragment")
	}
	labels := [][2]string{
		{"job", "limitping"},
		{"instance", instance},
		{"collector", "limitping"},
	}
	parts := []string{"metrics"}
	for _, label := range labels {
		parts = append(parts, url.PathEscape(label[0]), url.PathEscape(label[1]))
	}
	return baseURL + "/" + strings.Join(parts, "/"), nil
}

func Render(snapshot *usage.Snapshot, collectionDuration time.Duration, now time.Time, pingCompleted bool, lastSuccessfulPing *time.Time) (string, []string) {
	var output strings.Builder
	output.WriteString("# HELP metrics_pusher_collector_success Whether the collector completed successfully.\n")
	output.WriteString("# TYPE metrics_pusher_collector_success gauge\n")
	output.WriteString("metrics_pusher_collector_success 1\n")
	output.WriteString("# HELP metrics_pusher_collector_duration_seconds Collector execution duration.\n")
	output.WriteString("# TYPE metrics_pusher_collector_duration_seconds gauge\n")
	fmt.Fprintf(&output, "metrics_pusher_collector_duration_seconds %s\n", number(collectionDuration.Seconds()))
	output.WriteString("# HELP metrics_pusher_last_run_timestamp_seconds Unix timestamp of the latest collection attempt.\n")
	output.WriteString("# TYPE metrics_pusher_last_run_timestamp_seconds gauge\n")
	startedAt := now.Add(-collectionDuration)
	fmt.Fprintf(&output, "metrics_pusher_last_run_timestamp_seconds %.3f\n", float64(startedAt.UnixNano())/1e9)

	output.WriteString("# HELP limitping_window_used_ratio Fraction of the provider window already used.\n")
	output.WriteString("# TYPE limitping_window_used_ratio gauge\n")
	output.WriteString("# HELP limitping_window_remaining_ratio Fraction of the provider window remaining.\n")
	output.WriteString("# TYPE limitping_window_remaining_ratio gauge\n")
	output.WriteString("# HELP limitping_window_active Whether the provider window is active.\n")
	output.WriteString("# TYPE limitping_window_active gauge\n")
	output.WriteString("# HELP limitping_window_remaining_seconds Seconds until the provider window resets.\n")
	output.WriteString("# TYPE limitping_window_remaining_seconds gauge\n")
	output.WriteString("# HELP limitping_window_seconds Configured provider window length in seconds.\n")
	output.WriteString("# TYPE limitping_window_seconds gauge\n")
	output.WriteString("# HELP limitping_window_reset_timestamp_seconds Unix timestamp when the window resets.\n")
	output.WriteString("# TYPE limitping_window_reset_timestamp_seconds gauge\n")
	output.WriteString("# HELP limitping_limit_reached Whether the provider reports its limit reached.\n")
	output.WriteString("# TYPE limitping_limit_reached gauge\n")
	output.WriteString("# HELP limitping_reset_credits_available Number of available reset credits.\n")
	output.WriteString("# TYPE limitping_reset_credits_available gauge\n")
	output.WriteString("# HELP limitping_reset_credit_expiration_timestamp_seconds Unix timestamp when the earliest available reset credit expires.\n")
	output.WriteString("# TYPE limitping_reset_credit_expiration_timestamp_seconds gauge\n")
	output.WriteString("# HELP limitping_usage_fetched_timestamp_seconds Unix timestamp of the usage snapshot.\n")
	output.WriteString("# TYPE limitping_usage_fetched_timestamp_seconds gauge\n")
	output.WriteString("# HELP limitping_ping_completed Whether this invocation completed a ping operation.\n")
	output.WriteString("# TYPE limitping_ping_completed gauge\n")
	output.WriteString("# HELP limitping_last_successful_ping_timestamp_seconds Unix timestamp of the latest completed ping operation.\n")
	output.WriteString("# TYPE limitping_last_successful_ping_timestamp_seconds gauge\n")

	provider := snapshot.Provider
	if provider == "" {
		provider = "unknown"
	}
	plan := snapshot.Plan
	if plan == "" {
		plan = "unknown"
	}
	providerLabels := prometheusLabels(map[string]string{"provider": provider, "plan": plan})
	limitReached := 0
	if snapshot.LimitReached {
		limitReached = 1
	}
	fmt.Fprintf(&output, "limitping_limit_reached%s %d\n", providerLabels, limitReached)
	pingValue := 0
	if pingCompleted {
		pingValue = 1
	}
	fmt.Fprintf(&output, "limitping_ping_completed%s %d\n", providerLabels, pingValue)

	missing := make([]string, 0)
	if lastSuccessfulPing == nil {
		missing = append(missing, "limitping_last_successful_ping_timestamp_seconds")
	} else {
		lastPingLabels := prometheusLabels(map[string]string{
			"provider": provider,
			"plan":     plan,
			"elapsed":  compactHighestUnit(now.Sub(*lastSuccessfulPing)),
		})
		fmt.Fprintf(&output, "limitping_last_successful_ping_timestamp_seconds%s %.3f\n", lastPingLabels, float64(lastSuccessfulPing.UnixNano())/1e9)
	}
	if snapshot.ResetCredits == nil {
		missing = append(missing, "limitping_reset_credits_available")
		missing = append(missing, "limitping_reset_credit_expiration_timestamp_seconds")
	} else {
		fmt.Fprintf(&output, "limitping_reset_credits_available%s %d\n", providerLabels, snapshot.ResetCredits.AvailableCount)
		if expiration, ok := earliestAvailableCreditExpiration(snapshot.ResetCredits); ok {
			expirationLabels := prometheusLabels(map[string]string{
				"provider":  provider,
				"plan":      plan,
				"remaining": compactRemaining(expiration, now),
			})
			fmt.Fprintf(&output, "limitping_reset_credit_expiration_timestamp_seconds%s %.3f\n", expirationLabels, expiration)
		} else {
			missing = append(missing, "limitping_reset_credit_expiration_timestamp_seconds")
		}
	}
	if fetched, ok := parseTimestamp(snapshot.FetchedAt); ok {
		fmt.Fprintf(&output, "limitping_usage_fetched_timestamp_seconds%s %.3f\n", providerLabels, fetched)
	} else {
		missing = append(missing, "limitping_usage_fetched_timestamp_seconds")
	}

	missing = append(missing, renderWindow(&output, snapshot.FiveHour, provider, plan, "five_hour")...)
	missing = append(missing, renderWindow(&output, snapshot.Weekly, provider, plan, "weekly")...)

	output.WriteString("# HELP limitping_collector_generated_timestamp_seconds Unix timestamp when metrics were rendered.\n")
	output.WriteString("# TYPE limitping_collector_generated_timestamp_seconds gauge\n")
	fmt.Fprintf(&output, "limitping_collector_generated_timestamp_seconds %.3f\n", float64(now.UnixNano())/1e9)
	return output.String(), missing
}

func earliestAvailableCreditExpiration(credits *usage.ResetCredits) (float64, bool) {
	var earliest time.Time
	for _, credit := range credits.Credits {
		if credit.RedeemedAt != "" || (credit.Status != "" && credit.Status != "available") {
			continue
		}
		expires, err := time.Parse(time.RFC3339Nano, credit.ExpiresAt)
		if err != nil {
			continue
		}
		if earliest.IsZero() || expires.Before(earliest) {
			earliest = expires
		}
	}
	if earliest.IsZero() {
		return 0, false
	}
	return float64(earliest.UnixNano()) / 1e9, true
}

func compactRemaining(expiration float64, now time.Time) string {
	remaining := time.Duration(expiration*float64(time.Second)) - time.Duration(now.UnixNano())
	if remaining < 0 {
		remaining = 0
	}
	days := int64(remaining / (24 * time.Hour))
	hours := int64((remaining % (24 * time.Hour)) / time.Hour)
	return fmt.Sprintf("%d天%d时", days, hours)
}

func compactHighestUnit(elapsed time.Duration) string {
	if elapsed < 0 {
		elapsed = 0
	}
	if elapsed >= 24*time.Hour {
		return fmt.Sprintf("%d天", int64(elapsed/(24*time.Hour)))
	}
	if elapsed >= time.Hour {
		return fmt.Sprintf("%d时", int64(elapsed/time.Hour))
	}
	return fmt.Sprintf("%d秒", int64(elapsed/time.Second))
}

func renderWindow(output *strings.Builder, window *usage.Window, provider, plan, name string) []string {
	metricNames := []string{
		"limitping_window_used_ratio", "limitping_window_remaining_ratio", "limitping_window_active",
		"limitping_window_remaining_seconds", "limitping_window_seconds", "limitping_window_reset_timestamp_seconds",
	}
	if window == nil {
		missing := make([]string, len(metricNames))
		for i, metric := range metricNames {
			missing[i] = metric + `{window="` + name + `"}`
		}
		return missing
	}
	labels := prometheusLabels(map[string]string{"provider": provider, "plan": plan, "window": name})
	fmt.Fprintf(output, "limitping_window_used_ratio%s %s\n", labels, number(window.UsedPercent/100))
	fmt.Fprintf(output, "limitping_window_remaining_ratio%s %s\n", labels, number(window.RemainingPercent/100))
	active := 0
	if window.Active {
		active = 1
	}
	fmt.Fprintf(output, "limitping_window_active%s %d\n", labels, active)
	fmt.Fprintf(output, "limitping_window_remaining_seconds%s %d\n", labels, window.RemainingSeconds)
	fmt.Fprintf(output, "limitping_window_seconds%s %d\n", labels, window.WindowSeconds)
	if reset, ok := parseTimestamp(window.ResetsAt); ok {
		fmt.Fprintf(output, "limitping_window_reset_timestamp_seconds%s %.3f\n", labels, reset)
		return nil
	}
	return []string{"limitping_window_reset_timestamp_seconds" + `{window="` + name + `"}`}
}

func prometheusLabels(values map[string]string) string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	rendered := make([]string, 0, len(keys))
	for _, key := range keys {
		value := strings.ReplaceAll(values[key], `\`, `\\`)
		value = strings.ReplaceAll(value, "\n", `\n`)
		value = strings.ReplaceAll(value, `"`, `\"`)
		rendered = append(rendered, key+`="`+value+`"`)
	}
	return "{" + strings.Join(rendered, ",") + "}"
}

func parseTimestamp(value string) (float64, bool) {
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return 0, false
	}
	return float64(parsed.UnixNano()) / 1e9, true
}

func number(value float64) string {
	return strconv.FormatFloat(value, 'g', -1, 64)
}
