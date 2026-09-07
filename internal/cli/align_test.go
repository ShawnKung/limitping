package cli

import (
	"bytes"
	"strings"
	"testing"
)

func TestAlignAddListAndClear(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	var out bytes.Buffer
	if err := Run([]string{"align", "add", "0 0 * * *"}, strings.NewReader(""), &out, &out); err != nil {
		t.Fatalf("align add: %v", err)
	}
	if !strings.Contains(out.String(), "已添加对齐目标:") || !strings.Contains(out.String(), "0 0 * * *") {
		t.Fatalf("add output = %q", out.String())
	}

	out.Reset()
	if err := Run([]string{"align", "list"}, strings.NewReader(""), &out, &out); err != nil {
		t.Fatalf("align list: %v", err)
	}
	if !strings.Contains(out.String(), "0 0 * * *") || !strings.Contains(out.String(), "max-delay=") {
		t.Fatalf("list output = %q", out.String())
	}

	out.Reset()
	if err := Run([]string{"align", "clear"}, strings.NewReader(""), &out, &out); err != nil {
		t.Fatalf("align clear: %v", err)
	}
	out.Reset()
	if err := Run([]string{"align", "list"}, strings.NewReader(""), &out, &out); err != nil {
		t.Fatalf("align list after clear: %v", err)
	}
	if !strings.Contains(out.String(), "未配置对齐目标") {
		t.Fatalf("list-after-clear output = %q", out.String())
	}
}

func TestAlignDeleteByID(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	var out bytes.Buffer
	if err := Run([]string{"align", "add", "0 0 * * *"}, strings.NewReader(""), &out, &out); err != nil {
		t.Fatalf("align add: %v", err)
	}
	fields := strings.Fields(out.String())
	if len(fields) < 2 {
		t.Fatalf("cannot parse id from add output %q", out.String())
	}
	id := fields[1]

	out.Reset()
	if err := Run([]string{"align", "delete", id}, strings.NewReader(""), &out, &out); err != nil {
		t.Fatalf("align delete: %v", err)
	}
	if !strings.Contains(out.String(), "已删除对齐目标:") || !strings.Contains(out.String(), id) {
		t.Fatalf("delete output = %q", out.String())
	}

	out.Reset()
	if err := Run([]string{"align", "list"}, strings.NewReader(""), &out, &out); err != nil {
		t.Fatalf("align list: %v", err)
	}
	if !strings.Contains(out.String(), "未配置对齐目标") {
		t.Fatalf("list-after-delete output = %q", out.String())
	}
}

func TestAlignAddWithMaxDelay(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	var out bytes.Buffer
	if err := Run([]string{"align", "add", "0 0 * * *", "--max-delay", "45m"}, strings.NewReader(""), &out, &out); err != nil {
		t.Fatalf("align add: %v", err)
	}
	got := out.String()
	if !strings.Contains(got, "0 0 * * *") {
		t.Fatalf("add output missing cron: %q", got)
	}
	if !strings.Contains(got, "45m0s") {
		t.Fatalf("add output missing max delay: %q", got)
	}
}

func TestAlignAddRejectsInvalidCron(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	var out bytes.Buffer
	err := Run([]string{"align", "add", "not-a-cron"}, strings.NewReader(""), &out, &out)
	if err == nil {
		t.Fatal("expected invalid cron to error")
	}
}

func TestAlignPreviewRendersPlans(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	if err := Run([]string{"align", "add", "0 0 * * *"}, strings.NewReader(""), &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("align add: %v", err)
	}
	var out bytes.Buffer
	if err := Run([]string{"align", "preview", "--count", "3"}, strings.NewReader(""), &out, &out); err != nil {
		t.Fatalf("align preview: %v", err)
	}
	got := out.String()
	if !strings.Contains(got, "未来计划：") || !strings.Contains(got, "刷新") {
		t.Fatalf("preview output = %q", got)
	}
	if strings.Count(got, "ping ") < 3 {
		t.Fatalf("expected 3 planned pings, output = %q", got)
	}
}
