// Portions of this file are adapted from wavever/CCLimitPing.
// See THIRD_PARTY_NOTICES.md for attribution and license details.
package usage

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/ShawnKung/limitping/internal/auth"
)

const defaultBaseURL = "https://chatgpt.com/backend-api"

type Window struct {
	UsedPercent      float64 `json:"used_percent"`
	RemainingPercent float64 `json:"remaining_percent"`
	Active           bool    `json:"active"`
	ResetsAt         string  `json:"resets_at,omitempty"`
	RemainingSeconds int64   `json:"remaining_seconds"`
	WindowSeconds    int     `json:"window_seconds"`
}

type Snapshot struct {
	Provider     string        `json:"provider"`
	Plan         string        `json:"plan,omitempty"`
	FiveHour     *Window       `json:"five_hour,omitempty"`
	Weekly       *Window       `json:"weekly,omitempty"`
	ResetCredits *ResetCredits `json:"reset_credits,omitempty"`
	LimitReached bool          `json:"limit_reached"`
	FetchedAt    string        `json:"fetched_at"`
}

type ResetCredits struct {
	AvailableCount int           `json:"available_count"`
	Credits        []ResetCredit `json:"credits,omitempty"`
}

type ResetCredit struct {
	Status     string `json:"status,omitempty"`
	GrantedAt  string `json:"granted_at,omitempty"`
	ExpiresAt  string `json:"expires_at,omitempty"`
	RedeemedAt string `json:"redeemed_at,omitempty"`
}

type backendWindow struct {
	UsedPercent        float64 `json:"used_percent"`
	LimitWindowSeconds int     `json:"limit_window_seconds"`
	ResetAt            int64   `json:"reset_at"`
}

type rateLimit struct {
	LimitReached bool           `json:"limit_reached"`
	Primary      *backendWindow `json:"primary_window"`
	Secondary    *backendWindow `json:"secondary_window"`
}

type backendResponse struct {
	PlanType     string              `json:"plan_type"`
	RateLimit    rateLimit           `json:"rate_limit"`
	ResetCredits *inlineResetCredits `json:"rate_limit_reset_credits"`
}

type inlineResetCredits struct {
	AvailableCount int `json:"available_count"`
}

type resetCreditsResponse struct {
	AvailableCount *int          `json:"available_count"`
	Credits        []ResetCredit `json:"credits"`
}

type Client struct {
	Auth    *auth.Codex
	HTTP    *http.Client
	BaseURL string
	Now     func() time.Time
}

func NewClient(a *auth.Codex) *Client {
	baseTransport, ok := http.DefaultTransport.(*http.Transport)
	if !ok {
		return &Client{
			Auth: a, HTTP: &http.Client{Timeout: 30 * time.Second},
			BaseURL: configuredBaseURL(), Now: time.Now,
		}
	}
	transport := baseTransport.Clone()
	transport.ForceAttemptHTTP2 = false
	if transport.TLSClientConfig == nil {
		transport.TLSClientConfig = &tls.Config{}
	} else {
		transport.TLSClientConfig = transport.TLSClientConfig.Clone()
	}
	transport.TLSClientConfig.NextProtos = []string{"http/1.1"}
	transport.TLSNextProto = map[string]func(string, *tls.Conn) http.RoundTripper{}
	return &Client{
		Auth:    a,
		HTTP:    &http.Client{Transport: transport, Timeout: 30 * time.Second},
		BaseURL: configuredBaseURL(),
		Now:     time.Now,
	}
}

func (c *Client) Read(ctx context.Context) (*Snapshot, error) {
	body, err := c.get(ctx, usageURL(c.BaseURL), false)
	if err != nil {
		return nil, err
	}
	var response backendResponse
	if err := json.Unmarshal(body, &response); err != nil {
		return nil, fmt.Errorf("解析 Codex 用量响应: %w", err)
	}
	now := c.Now()
	five, weekly := classifyWindows(response.RateLimit, now)
	snapshot := &Snapshot{
		Provider: "codex", Plan: response.PlanType, FiveHour: five, Weekly: weekly,
		LimitReached: response.RateLimit.LimitReached, FetchedAt: now.Format(time.RFC3339),
	}
	if credits, err := c.readResetCredits(ctx); err == nil {
		snapshot.ResetCredits = credits
	} else if response.ResetCredits != nil {
		snapshot.ResetCredits = &ResetCredits{AvailableCount: response.ResetCredits.AvailableCount}
	}
	return snapshot, nil
}

func (c *Client) readResetCredits(ctx context.Context) (*ResetCredits, error) {
	body, err := c.get(ctx, resetCreditsURL(c.BaseURL), true)
	if err != nil {
		return nil, err
	}
	var response resetCreditsResponse
	if err := json.Unmarshal(body, &response); err != nil {
		return nil, fmt.Errorf("解析 Codex 重置券响应: %w", err)
	}
	count := len(response.Credits)
	if response.AvailableCount != nil {
		count = *response.AvailableCount
	}
	return &ResetCredits{AvailableCount: count, Credits: response.Credits}, nil
}

func (c *Client) get(ctx context.Context, endpoint string, creditHeaders bool) ([]byte, error) {
	body, status, err := c.request(ctx, endpoint, creditHeaders)
	if err != nil {
		return nil, err
	}
	if status == http.StatusUnauthorized {
		old, _ := c.Auth.Token()
		if err := c.Auth.Reload(); err == nil {
			fresh, _ := c.Auth.Token()
			if fresh != old {
				body, status, err = c.request(ctx, endpoint, creditHeaders)
			}
		}
	}
	if err != nil {
		return nil, err
	}
	if status == http.StatusUnauthorized {
		if err := c.Auth.Refresh(ctx); err != nil {
			return nil, fmt.Errorf("接口未授权且刷新登录失败: %w", err)
		}
		body, status, err = c.request(ctx, endpoint, creditHeaders)
	}
	if err != nil {
		return nil, err
	}
	if status != http.StatusOK {
		return nil, fmt.Errorf("Codex 接口返回 HTTP %d: %s", status, bytes.TrimSpace(body))
	}
	return body, nil
}

func (c *Client) request(ctx context.Context, endpoint string, creditHeaders bool) ([]byte, int, error) {
	token, err := c.Auth.Token()
	if err != nil {
		return nil, 0, err
	}
	accountID, _ := c.Auth.AccountID()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "limitping")
	if creditHeaders {
		req.Header.Set("OpenAI-Beta", "codex-1")
		req.Header.Set("originator", "Codex Desktop")
	}
	if accountID != "" {
		req.Header.Set("ChatGPT-Account-Id", accountID)
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, 0, fmt.Errorf("读取 Codex 用量: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	return body, resp.StatusCode, err
}

func classifyWindows(rate rateLimit, now time.Time) (five, weekly *Window) {
	const weeklyMinSeconds = 2 * 24 * 60 * 60
	for _, candidate := range []*backendWindow{rate.Primary, rate.Secondary} {
		if candidate == nil {
			continue
		}
		window := makeWindow(*candidate, now)
		if candidate.LimitWindowSeconds >= weeklyMinSeconds {
			weekly = window
		} else {
			five = window
		}
	}
	return five, weekly
}

func makeWindow(raw backendWindow, now time.Time) *Window {
	remaining := int64(0)
	reset := ""
	active := false
	if raw.ResetAt > 0 {
		resetTime := time.Unix(raw.ResetAt, 0)
		reset = resetTime.Format(time.RFC3339)
		remaining = int64(resetTime.Sub(now).Seconds())
		active = raw.UsedPercent > 0 && resetTime.After(now)
		if remaining < 0 {
			remaining = 0
		}
	}
	remainingPercent := 100 - raw.UsedPercent
	if remainingPercent < 0 {
		remainingPercent = 0
	}
	return &Window{
		UsedPercent: raw.UsedPercent, RemainingPercent: remainingPercent, Active: active,
		ResetsAt: reset, RemainingSeconds: remaining, WindowSeconds: raw.LimitWindowSeconds,
	}
}

func usageURL(base string) string {
	base = strings.TrimRight(strings.TrimSpace(base), "/")
	if base == "" {
		base = defaultBaseURL
	}
	path := "/api/codex/usage"
	if strings.Contains(base, "/backend-api") {
		path = "/wham/usage"
	}
	endpoint := base + path
	if _, err := url.ParseRequestURI(endpoint); err != nil {
		return defaultBaseURL + "/wham/usage"
	}
	return endpoint
}

func resetCreditsURL(base string) string {
	base = strings.TrimRight(strings.TrimSpace(base), "/")
	if base == "" {
		base = defaultBaseURL
	}
	endpoint := base + "/wham/rate-limit-reset-credits"
	if _, err := url.ParseRequestURI(endpoint); err != nil {
		return defaultBaseURL + "/wham/rate-limit-reset-credits"
	}
	return endpoint
}

func configuredBaseURL() string {
	path := ""
	if dir := os.Getenv("CODEX_HOME"); dir != "" {
		path = filepath.Join(dir, "config.toml")
	} else if home, err := os.UserHomeDir(); err == nil {
		path = filepath.Join(home, ".codex", "config.toml")
	}
	if blob, err := os.ReadFile(path); err == nil {
		for _, line := range strings.Split(string(blob), "\n") {
			parts := strings.SplitN(strings.SplitN(line, "#", 2)[0], "=", 2)
			if len(parts) == 2 && strings.TrimSpace(parts[0]) == "chatgpt_base_url" {
				value := strings.Trim(strings.TrimSpace(parts[1]), "\"'")
				if value != "" {
					value = strings.TrimRight(value, "/")
					if (strings.HasPrefix(value, "https://chatgpt.com") || strings.HasPrefix(value, "https://chat.openai.com")) && !strings.Contains(value, "/backend-api") {
						value += "/backend-api"
					}
					return value
				}
			}
		}
	}
	return defaultBaseURL
}
