// Portions of this file are adapted from wavever/CCLimitPing.
// See THIRD_PARTY_NOTICES.md for attribution and license details.
package auth

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"
)

const (
	codexOAuthClientID = "app_EMoamEEZ73f0CkXaXp7hrann"
	tokenEndpoint      = "https://auth.openai.com/oauth/token"
)

// Codex reuses the OAuth credentials maintained by the official Codex CLI.
type Codex struct {
	Path      string
	Client    *http.Client
	access    string
	refresh   string
	accountID string
	raw       map[string]any
}

func NewCodex() (*Codex, error) {
	path, err := authPath()
	if err != nil {
		return nil, err
	}
	return &Codex{Path: path, Client: &http.Client{Timeout: 30 * time.Second}}, nil
}

func authPath() (string, error) {
	if dir := os.Getenv("CODEX_HOME"); dir != "" {
		return filepath.Join(dir, "auth.json"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".codex", "auth.json"), nil
}

func (a *Codex) Token() (string, error) {
	if a.access == "" {
		if err := a.Reload(); err != nil {
			return "", err
		}
	}
	return a.access, nil
}

func (a *Codex) AccountID() (string, error) {
	if a.access == "" {
		if err := a.Reload(); err != nil {
			return "", err
		}
	}
	return a.accountID, nil
}

func (a *Codex) Reload() error {
	blob, err := os.ReadFile(a.Path)
	if err != nil {
		return fmt.Errorf("找不到 Codex 登录凭据 %s: %w", a.Path, err)
	}
	var raw map[string]any
	if err := json.Unmarshal(blob, &raw); err != nil {
		return fmt.Errorf("Codex 登录凭据不是合法 JSON: %w", err)
	}
	tokens, _ := raw["tokens"].(map[string]any)
	if tokens == nil {
		return fmt.Errorf("Codex 登录凭据缺少 tokens 对象: %s", a.Path)
	}
	a.access, _ = tokens["access_token"].(string)
	a.refresh, _ = tokens["refresh_token"].(string)
	a.accountID, _ = tokens["account_id"].(string)
	if a.access == "" {
		return fmt.Errorf("Codex 登录凭据缺少 access_token: %s", a.Path)
	}
	a.raw = raw
	return nil
}

func (a *Codex) Refresh(ctx context.Context) error {
	if a.refresh == "" {
		if err := a.Reload(); err != nil {
			return err
		}
	}
	if a.refresh == "" {
		return fmt.Errorf("Codex 登录凭据缺少 refresh_token")
	}
	payload, _ := json.Marshal(map[string]string{
		"grant_type": "refresh_token", "refresh_token": a.refresh, "client_id": codexOAuthClientID,
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, tokenEndpoint, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := a.Client.Do(req)
	if err != nil {
		return fmt.Errorf("刷新 Codex OAuth token: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 300))
		return fmt.Errorf("刷新 Codex OAuth token: HTTP %d: %s", resp.StatusCode, bytes.TrimSpace(body))
	}
	var result struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		IDToken      string `json:"id_token"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return err
	}
	if result.AccessToken == "" {
		return fmt.Errorf("刷新 Codex OAuth token: 返回了空 access_token")
	}
	a.access = result.AccessToken
	if result.RefreshToken != "" {
		a.refresh = result.RefreshToken
	}
	return a.persist(result.IDToken)
}

func (a *Codex) persist(idToken string) error {
	tokens, _ := a.raw["tokens"].(map[string]any)
	if tokens == nil {
		tokens = map[string]any{}
		a.raw["tokens"] = tokens
	}
	tokens["access_token"] = a.access
	tokens["refresh_token"] = a.refresh
	if idToken != "" {
		tokens["id_token"] = idToken
	}
	a.raw["last_refresh"] = time.Now().UTC().Format(time.RFC3339)
	blob, err := json.MarshalIndent(a.raw, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(a.Path), ".limitping-auth-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(blob); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, a.Path)
}
