package cli

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/ShawnKung/limitping/internal/usage"
)

func TestShouldPingFull(t *testing.T) {
	if ok, _ := shouldPingFull(&usage.Window{UsedPercent: 0, WindowSeconds: 18000}); !ok {
		t.Fatal("expected full five-hour allowance to ping")
	}
	if ok, _ := shouldPingFull(&usage.Window{UsedPercent: 0.1, WindowSeconds: 18000}); ok {
		t.Fatal("expected partially used window to skip")
	}
	if ok, _ := shouldPingFull(&usage.Window{UsedPercent: 0, WindowSeconds: 604800}); ok {
		t.Fatal("expected weekly window to skip")
	}
}

func TestPrintResetCredits(t *testing.T) {
	var out bytes.Buffer
	expiresAt := time.Now().Add(22*24*time.Hour + 8*time.Hour + 42*time.Minute).Format(time.RFC3339)
	printResetCredits(&out, &usage.ResetCredits{
		AvailableCount: 1,
		Credits: []usage.ResetCredit{{
			Status: "available", GrantedAt: "2026-08-22T06:31:00+08:00", ExpiresAt: expiresAt,
		}},
	})
	got := out.String()
	if !strings.Contains(got, "重置券 1 张可用") || !strings.Contains(got, "发放于 08-22 06:31") {
		t.Fatalf("output = %q", got)
	}
	if strings.Contains(got, "d") || strings.Contains(got, "h") || !strings.Contains(got, "天") {
		t.Fatalf("remaining credit duration is not Chinese: %q", got)
	}
}

func TestShellJoin(t *testing.T) {
	got := shellJoin([]string{"exec", "-m", "gpt-5.4-mini", "ping"})
	if got != "exec -m gpt-5.4-mini ping" {
		t.Fatalf("got %q", got)
	}
	if got := shellJoin([]string{"two words"}); !strings.Contains(got, "'two words'") {
		t.Fatalf("got %q", got)
	}
}

func TestCobraHelp(t *testing.T) {
	var out bytes.Buffer
	if err := Run([]string{"--help"}, strings.NewReader(""), &out, &out); err != nil {
		t.Fatal(err)
	}
	got := out.String()
	for _, want := range []string{"可用命令:", "status, s, stat", "ping, p", "--help"} {
		if !strings.Contains(got, want) {
			t.Fatalf("help missing %q:\n%s", want, got)
		}
	}
}

func TestPushMetricIsGlobal(t *testing.T) {
	for _, command := range []string{"status", "ping"} {
		var out bytes.Buffer
		if err := Run([]string{command, "--help"}, strings.NewReader(""), &out, &out); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(out.String(), "--push-metric string") {
			t.Fatalf("%s help missing global push flag:\n%s", command, out.String())
		}
	}
}

func TestCurrentVersionUsesInjectedValue(t *testing.T) {
	original := version
	t.Cleanup(func() { version = original })
	version = "v1.2.3"
	if got := currentVersion(); got != "1.2.3" {
		t.Fatalf("currentVersion() = %q", got)
	}
}

func TestVersionCommandShowsVersionAndCommit(t *testing.T) {
	originalVersion, originalCommit := version, commit
	t.Cleanup(func() {
		version, commit = originalVersion, originalCommit
	})
	version, commit = "v1.2.3", "abcdef123456"

	var out bytes.Buffer
	if err := Run([]string{"version"}, strings.NewReader(""), &out, &out); err != nil {
		t.Fatal(err)
	}
	got := out.String()
	if !strings.Contains(got, "limitping 1.2.3\n") || !strings.Contains(got, "commit: abcdef123456\n") {
		t.Fatalf("output = %q", got)
	}
}

func TestWindowLabelsAlignByDisplayWidth(t *testing.T) {
	five := "  " + padDisplay("5h", 6) + " ["
	weekly := "  " + padDisplay("周", 6) + " ["
	if displayWidth(five) != displayWidth(weekly) {
		t.Fatalf("columns differ: five=%d weekly=%d", displayWidth(five), displayWidth(weekly))
	}
	if displayWidth("周") != 2 {
		t.Fatalf("Chinese width = %d", displayWidth("周"))
	}
}

func TestFormatDurationCN(t *testing.T) {
	tests := []struct {
		duration time.Duration
		want     string
	}{
		{12*24*time.Hour + 4*time.Hour + 4*time.Minute, "12天 4时 4分"},
		{12*time.Hour + 11*time.Minute, "12时11分"},
		{47 * time.Minute, "47分"},
		{24 * time.Hour, "1天"},
		{0, "0分"},
	}
	for _, test := range tests {
		if got := formatDurationCN(test.duration); got != test.want {
			t.Errorf("formatDurationCN(%s) = %q, want %q", test.duration, got, test.want)
		}
	}
}

func TestDurationColumnsRightAlign(t *testing.T) {
	longest := formatDurationCN(12*24*time.Hour + 4*time.Hour + 4*time.Minute)
	shorter := padDisplayLeft(formatDurationCN(12*time.Hour+11*time.Minute), displayWidth(longest))
	if displayWidth(shorter) != displayWidth(longest) {
		t.Fatalf("columns differ: longest=%q shorter=%q", longest, shorter)
	}
}

func TestProgressBarShowsRemainingAllowance(t *testing.T) {
	tests := []struct {
		remaining float64
		want      string
	}{
		{100, "[██████████]"},
		{60, "[██████░░░░]"},
		{0, "[░░░░░░░░░░]"},
	}
	for _, test := range tests {
		if got := progressBarRemaining(test.remaining); got != test.want {
			t.Errorf("progressBarRemaining(%.1f) = %q, want %q", test.remaining, got, test.want)
		}
	}
}

func TestPrintStatusShowsRemainingAllowance(t *testing.T) {
	var out bytes.Buffer
	printStatus(&out, &usage.Snapshot{
		Provider: "codex",
		Plan:     "plus",
		FiveHour: &usage.Window{UsedPercent: 8, RemainingSeconds: 30 * 60},
		Weekly:   &usage.Window{UsedPercent: 20, RemainingSeconds: 5*24*60*60 + 2*60*60 + 28*60},
	})
	got := out.String()
	for _, want := range []string{"[█████████░]  剩余  92.0%", "[████████░░]  剩余  80.0%", "5天 2时28分"} {
		if !strings.Contains(got, want) {
			t.Fatalf("status missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "已用") {
		t.Fatalf("status still describes used allowance:\n%s", got)
	}
}
