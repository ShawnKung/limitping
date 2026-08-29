package auth

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAuthPathHonorsCodexHome(t *testing.T) {
	t.Setenv("CODEX_HOME", "/tmp/codex-test-home")
	got, err := authPath()
	if err != nil {
		t.Fatal(err)
	}
	if want := "/tmp/codex-test-home/auth.json"; got != want {
		t.Fatalf("authPath() = %q, want %q", got, want)
	}
}

func TestReloadAndRefreshPersistTokens(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "auth.json")
	initial := `{"custom":"preserved","tokens":{"access_token":"old-access","refresh_token":"old-refresh","account_id":"account-1"}}`
	if err := os.WriteFile(path, []byte(initial), 0o600); err != nil {
		t.Fatal(err)
	}

	client := &http.Client{Transport: authRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.Method != http.MethodPost || request.URL.String() != tokenEndpoint {
			t.Fatalf("unexpected request: %s %s", request.Method, request.URL)
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body: io.NopCloser(strings.NewReader(
				`{"access_token":"new-access","refresh_token":"new-refresh","id_token":"new-id"}`,
			)),
		}, nil
	})}
	auth := &Codex{Path: path, Client: client}
	if err := auth.Reload(); err != nil {
		t.Fatal(err)
	}
	if token, _ := auth.Token(); token != "old-access" {
		t.Fatalf("initial token = %q", token)
	}
	if err := auth.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}

	blob, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var saved map[string]any
	if err := json.Unmarshal(blob, &saved); err != nil {
		t.Fatal(err)
	}
	tokens := saved["tokens"].(map[string]any)
	if tokens["access_token"] != "new-access" || tokens["refresh_token"] != "new-refresh" || tokens["id_token"] != "new-id" {
		t.Fatalf("saved tokens were not refreshed: %#v", tokens)
	}
	if saved["custom"] != "preserved" {
		t.Fatalf("unrelated auth data was lost: %#v", saved)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("auth mode = %o, want 600", info.Mode().Perm())
	}
}

type authRoundTripFunc func(*http.Request) (*http.Response, error)

func (fn authRoundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return fn(request)
}
