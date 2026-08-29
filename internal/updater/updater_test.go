package updater

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCompareVersions(t *testing.T) {
	tests := []struct {
		left  string
		right string
		want  int
	}{
		{"v0.1.0", "0.2.0", -1},
		{"1.0.0", "1.0.0", 0},
		{"2.0.0", "1.9.9", 1},
		{"1.0.0-rc.1", "1.0.0", -1},
		{"1.0.0-beta.2", "1.0.0-beta.11", -1},
		{"1.0.0-beta.99999999999999999999", "1.0.0-beta.100000000000000000000", -1},
		{"1.0.0+build.1", "1.0.0+build.2", 0},
	}
	for _, test := range tests {
		left, err := parseVersion(test.left)
		if err != nil {
			t.Fatal(err)
		}
		right, err := parseVersion(test.right)
		if err != nil {
			t.Fatal(err)
		}
		if got := compareVersions(left, right); got != test.want {
			t.Errorf("compareVersions(%q, %q) = %d, want %d", test.left, test.right, got, test.want)
		}
	}
}

func TestUpdateReplacesBinaryAfterChecksumVerification(t *testing.T) {
	newBinary := []byte("new limitping binary")
	binaryName := "limitping_0.2.0_darwin_arm64"
	sum := sha256.Sum256(newBinary)
	httpClient := releaseClient(t, "v0.2.0", binaryName, newBinary, fmt.Sprintf("%x  %s\n", sum, binaryName))

	target := filepath.Join(t.TempDir(), "limitping")
	if err := os.WriteFile(target, []byte("old binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	client := &Client{
		HTTPClient:     httpClient,
		APIURL:         "https://release.test/latest",
		GOOS:           "darwin",
		GOARCH:         "arm64",
		ExecutablePath: target,
	}
	result, err := client.Update(context.Background(), "0.1.0")
	if err != nil {
		t.Fatal(err)
	}
	resolvedTarget, err := filepath.EvalSymlinks(target)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Updated || result.Latest != "0.2.0" || result.Path != resolvedTarget {
		t.Fatalf("result = %+v", result)
	}
	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(newBinary) {
		t.Fatalf("binary = %q", got)
	}
	info, err := os.Stat(target)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o755 {
		t.Fatalf("mode = %o", info.Mode().Perm())
	}
}

func TestUpdateLeavesCurrentBinaryOnChecksumFailure(t *testing.T) {
	binaryName := "limitping_0.2.0_linux_amd64"
	httpClient := releaseClient(t, "v0.2.0", binaryName, []byte("corrupt"), strings.Repeat("0", 64)+"  "+binaryName+"\n")

	target := filepath.Join(t.TempDir(), "limitping")
	if err := os.WriteFile(target, []byte("old binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	client := &Client{
		HTTPClient:     httpClient,
		APIURL:         "https://release.test/latest",
		GOOS:           "linux",
		GOARCH:         "amd64",
		ExecutablePath: target,
	}
	if _, err := client.Update(context.Background(), "0.1.0"); err == nil || !strings.Contains(err.Error(), "SHA-256") {
		t.Fatalf("error = %v", err)
	}
	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "old binary" {
		t.Fatalf("binary changed to %q", got)
	}
}

func TestUpdateSkipsCurrentVersion(t *testing.T) {
	client := &Client{
		HTTPClient: releaseClient(t, "v0.2.0", "unused", nil, ""),
		APIURL:     "https://release.test/latest",
		GOOS:       "linux",
		GOARCH:     "amd64",
	}
	result, err := client.Update(context.Background(), "0.2.0")
	if err != nil {
		t.Fatal(err)
	}
	if result.Updated || result.Comparison != 0 {
		t.Fatalf("result = %+v", result)
	}
}

func TestParseVersionRejectsDevelopmentBuild(t *testing.T) {
	for _, value := range []string{"dev", "abc1234", "1.2", "1.2.03", "1.2.3-"} {
		if _, err := parseVersion(value); err == nil {
			t.Errorf("parseVersion(%q) succeeded", value)
		}
	}
}

func releaseClient(t *testing.T, tag, binaryName string, binary []byte, checksums string) *http.Client {
	t.Helper()
	releaseJSON, err := json.Marshal(map[string]any{
		"tag_name": tag,
		"assets": []map[string]string{
			{"name": binaryName, "browser_download_url": "https://release.test/binary"},
			{"name": "checksums.txt", "browser_download_url": "https://release.test/checksums"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		var body []byte
		switch r.URL.Path {
		case "/latest":
			body = releaseJSON
		case "/binary":
			body = binary
		case "/checksums":
			body = []byte(checksums)
		default:
			return &http.Response{
				StatusCode: http.StatusNotFound,
				Body:       io.NopCloser(bytes.NewReader(nil)),
				Header:     make(http.Header),
				Request:    r,
			}, nil
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(bytes.NewReader(body)),
			Header:     make(http.Header),
			Request:    r,
		}, nil
	})}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (function roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return function(request)
}
