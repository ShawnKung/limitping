package align

import (
	"fmt"
	"path/filepath"
	"testing"
	"time"
)

func mustPlanner(t *testing.T, crons []string, maxDelayMinutes int) *Planner {
	t.Helper()
	planner, err := NewPlanner(&Config{Targets: targetsFromCrons(crons, maxDelayMinutes)})
	if err != nil {
		t.Fatalf("NewPlanner: %v", err)
	}
	return planner
}

func targetsFromCrons(crons []string, maxDelayMinutes int) []Target {
	targets := make([]Target, 0, len(crons))
	for i, expr := range crons {
		targets = append(targets, Target{ID: fmt.Sprintf("test%08d", i), Cron: expr, MaxDelayMinutes: maxDelayMinutes})
	}
	return targets
}

func TestDecideNoTargetsAlwaysPings(t *testing.T) {
	planner := mustPlanner(t, nil, 0)
	decision := planner.Decide(time.Now())
	if decision.Action != ActionPing || decision.HasTarget {
		t.Fatalf("expected unconditional ping, got %+v", decision)
	}
}

func TestDecideFarFromTargetPingsImmediately(t *testing.T) {
	// 目标每晚 00:00；现在 08:00，锚点应为 19:00，距今 11h，远超预算 → 立即 ping。
	planner := mustPlanner(t, []string{"0 0 * * *"}, DefaultMaxDelayMinutes)
	now := time.Date(2026, 9, 7, 8, 0, 0, 0, time.Local)
	decision := planner.Decide(now)
	if decision.Action != ActionPing {
		t.Fatalf("expected ping when far from anchor, got %+v", decision)
	}
	if !decision.HasTarget {
		t.Fatalf("expected a resolved target")
	}
}

func TestDecideHoldsWithinBudget(t *testing.T) {
	// 目标 00:00，锚点 19:00。现在 18:30，距锚点 30min，在 90min 预算内 → hold。
	planner := mustPlanner(t, []string{"0 0 * * *"}, DefaultMaxDelayMinutes)
	now := time.Date(2026, 9, 7, 18, 30, 0, 0, time.Local)
	decision := planner.Decide(now)
	if decision.Action != ActionHold {
		t.Fatalf("expected hold, got %+v", decision)
	}
	wantAnchor := time.Date(2026, 9, 7, 19, 0, 0, 0, time.Local)
	if !decision.Anchor.Equal(wantAnchor) {
		t.Fatalf("anchor = %s, want %s", decision.Anchor, wantAnchor)
	}
}

func TestDecidePingsAtAnchor(t *testing.T) {
	// 现在正好 19:00，落在锚点容差内 → ping。
	planner := mustPlanner(t, []string{"0 0 * * *"}, DefaultMaxDelayMinutes)
	now := time.Date(2026, 9, 7, 19, 0, 0, 0, time.Local)
	decision := planner.Decide(now)
	if decision.Action != ActionPing {
		t.Fatalf("expected ping at anchor, got %+v", decision)
	}
	wantTarget := time.Date(2026, 9, 8, 0, 0, 0, 0, time.Local)
	if !decision.Target.Equal(wantTarget) {
		t.Fatalf("target = %s, want %s", decision.Target, wantTarget)
	}
}

func TestDecidePicksNearestOfMultipleCrons(t *testing.T) {
	// 两个目标 00:00 与 12:00。现在 06:40，最近可达锚点是 12:00 的锚点 07:00。
	planner := mustPlanner(t, []string{"0 0 * * *", "0 12 * * *"}, DefaultMaxDelayMinutes)
	now := time.Date(2026, 9, 7, 6, 40, 0, 0, time.Local)
	decision := planner.Decide(now)
	wantTarget := time.Date(2026, 9, 7, 12, 0, 0, 0, time.Local)
	if !decision.Target.Equal(wantTarget) {
		t.Fatalf("target = %s, want %s", decision.Target, wantTarget)
	}
	if decision.Action != ActionHold {
		t.Fatalf("expected hold near noon anchor, got %+v", decision)
	}
}

func TestDecideUsesPerTargetMaxDelay(t *testing.T) {
	// 目标 00:00 锚点 19:00。现在 18:30，距锚点 30min。
	// 若该目标预算只有 15min（<30），应立即 ping 而非 hold。
	planner, err := NewPlanner(&Config{Targets: []Target{
		{ID: "aaaa", Cron: "0 0 * * *", MaxDelayMinutes: 15},
	}})
	if err != nil {
		t.Fatalf("NewPlanner: %v", err)
	}
	now := time.Date(2026, 9, 7, 18, 30, 0, 0, time.Local)
	decision := planner.Decide(now)
	if decision.Action != ActionPing {
		t.Fatalf("expected ping when delay exceeds this target budget, got %+v", decision)
	}
	if decision.MaxDelay != 15*time.Minute {
		t.Fatalf("expected decision to carry per-target max delay 15m, got %s", decision.MaxDelay)
	}
}

func TestNextPingTimesConvergeThenAlign(t *testing.T) {
	// 从 08:00 起链式：前几次每 5h，临近后把某次拖到 19:00，使刷新落在 00:00。
	planner := mustPlanner(t, []string{"0 0 * * *"}, DefaultMaxDelayMinutes)
	start := time.Date(2026, 9, 7, 8, 0, 0, 0, time.Local)
	plans := planner.NextPingTimes(start, 5)
	if len(plans) != 5 {
		t.Fatalf("want 5 plans, got %d", len(plans))
	}
	// 每次刷新必须等于对应 ping + 5h。
	for _, plan := range plans {
		if !plan.RefreshAt.Equal(plan.PingAt.Add(WindowLength)) {
			t.Fatalf("refresh != ping+5h: %+v", plan)
		}
	}
	// 存在某次刷新命中目标 00:00（允许容差）。
	hitMidnight := false
	for _, plan := range plans {
		if plan.RefreshAt.Hour() == 0 && plan.RefreshAt.Minute() == 0 {
			hitMidnight = true
		}
	}
	if !hitMidnight {
		t.Fatalf("expected a refresh aligned to 00:00, plans=%v", plans)
	}
}

func TestUpcomingUsesRunningWindow(t *testing.T) {
	planner := mustPlanner(t, []string{"0 0 * * *"}, DefaultMaxDelayMinutes)
	now := time.Date(2026, 9, 7, 8, 0, 0, 0, time.Local)
	lastPing := now.Add(-2 * time.Hour) // 窗口还剩 3h
	plans := planner.Upcoming(now, &lastPing, 3)
	firstFull := lastPing.Add(WindowLength)
	if plans[0].PingAt.Before(firstFull) {
		t.Fatalf("first ping %s should not precede next full %s", plans[0].PingAt, firstFull)
	}
}

func TestUpcomingNormalizesPersistedUTCLastPing(t *testing.T) {
	originalLocal := time.Local
	time.Local = time.FixedZone("UTC+8", 8*60*60)
	t.Cleanup(func() { time.Local = originalLocal })

	planner := mustPlanner(t, []string{"0 0 * * *"}, 300)
	now := time.Date(2026, 9, 8, 11, 40, 0, 0, time.Local)
	lastPingLocal := time.Date(2026, 9, 8, 10, 41, 0, 0, time.Local)
	lastPingUTC := lastPingLocal.UTC()

	plans := planner.Upcoming(now, &lastPingUTC, 3)
	if len(plans) != 3 {
		t.Fatalf("want 3 plans, got %d", len(plans))
	}
	wantPing := time.Date(2026, 9, 8, 19, 0, 0, 0, time.Local)
	wantRefresh := time.Date(2026, 9, 9, 0, 0, 0, 0, time.Local)
	if !plans[0].PingAt.Equal(wantPing) || !plans[0].RefreshAt.Equal(wantRefresh) {
		t.Fatalf("plan[0] = %+v, want ping %s refresh %s", plans[0], wantPing, wantRefresh)
	}
	if plans[0].PingAt.Location() != time.Local || plans[0].RefreshAt.Location() != time.Local {
		t.Fatalf("plan should use local timezone, got ping=%s refresh=%s", plans[0].PingAt.Location(), plans[0].RefreshAt.Location())
	}
}

func TestParseCronRejectsInvalid(t *testing.T) {
	if _, err := ParseCron("not a cron"); err == nil {
		t.Fatal("expected error for invalid cron")
	}
	if _, err := ParseCron(""); err == nil {
		t.Fatal("expected error for empty cron")
	}
}

func TestConfigRoundTrip(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	cfg := &Config{}
	first, _, err := cfg.Add("0 0 * * *", 120)
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	if _, _, err := cfg.Add("30 12 * * 1", 0); err != nil {
		t.Fatalf("Add: %v", err)
	}
	if err := Save(cfg); err != nil {
		t.Fatalf("Save: %v", err)
	}
	loaded, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(loaded.Targets) != 2 {
		t.Fatalf("round trip mismatch: %+v", loaded)
	}
	if loaded.Targets[0].ID != first.ID || loaded.Targets[0].Cron != "0 0 * * *" || loaded.Targets[0].MaxDelayMinutes != 120 {
		t.Fatalf("id/cron/max-delay not persisted: %+v", loaded.Targets[0])
	}
	// 第二个目标未指定预算，落盘为 0，读回时以默认值解释。
	if loaded.Targets[1].MaxDelayMinutes != 0 || loaded.Targets[1].MaxDelay() != DefaultMaxDelayMinutes*time.Minute {
		t.Fatalf("default max-delay wrong: %+v -> %s", loaded.Targets[1], loaded.Targets[1].MaxDelay())
	}
	if path, _ := ConfigPath(); path != filepath.Join(dir, "limitping", configFilename) {
		t.Fatalf("unexpected config path %s", path)
	}
}

func TestLoadMissingReturnsDefault(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(cfg.Targets) != 0 {
		t.Fatalf("expected no targets, got %v", cfg.Targets)
	}
	if (Target{}).MaxDelay() != DefaultMaxDelayMinutes*time.Minute {
		t.Fatalf("expected default max delay, got %s", (Target{}).MaxDelay())
	}
}

func TestSaveRejectsInvalidCron(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if err := Save(&Config{Targets: []Target{{ID: "abc", Cron: "bogus"}}}); err == nil {
		t.Fatal("expected save to reject invalid cron")
	}
}

func TestAddAssignsUniqueIDsAndUpdatesDelayOnDup(t *testing.T) {
	cfg := &Config{}
	a, created, err := cfg.Add("0 0 * * *", 45)
	if err != nil || !created {
		t.Fatalf("Add: %v created=%v", err, created)
	}
	b, created, err := cfg.Add("0 12 * * *", 0)
	if err != nil || !created {
		t.Fatalf("Add: %v created=%v", err, created)
	}
	if a.ID == "" || b.ID == "" || a.ID == b.ID {
		t.Fatalf("expected distinct non-empty ids, got %q and %q", a.ID, b.ID)
	}
	if len(a.ID) != idLength {
		t.Fatalf("id length = %d, want %d", len(a.ID), idLength)
	}
	if a.MaxDelayMinutes != 45 {
		t.Fatalf("expected per-target max delay 45, got %d", a.MaxDelayMinutes)
	}
	// 重复 cron 且给了新预算：不新增目标，只更新其预算。
	dup, created, err := cfg.Add("0 0 * * *", 30)
	if err != nil || created {
		t.Fatalf("Add dup: %v created=%v", err, created)
	}
	if dup.ID != a.ID || len(cfg.Targets) != 2 || cfg.Targets[0].MaxDelayMinutes != 30 {
		t.Fatalf("duplicate cron should reuse id and update delay: dup=%+v targets=%d", dup, len(cfg.Targets))
	}
}

func TestDeleteByIDAndPrefix(t *testing.T) {
	cfg := &Config{}
	a, _, _ := cfg.Add("0 0 * * *", 0)
	b, _, _ := cfg.Add("0 12 * * *", 0)

	removed, err := cfg.Delete(a.ID)
	if err != nil {
		t.Fatalf("Delete by full id: %v", err)
	}
	if removed.ID != a.ID || len(cfg.Targets) != 1 {
		t.Fatalf("full-id delete failed: removed=%+v remaining=%d", removed, len(cfg.Targets))
	}

	removed, err = cfg.Delete(b.ID[:4])
	if err != nil {
		t.Fatalf("Delete by prefix: %v", err)
	}
	if removed.ID != b.ID || len(cfg.Targets) != 0 {
		t.Fatalf("prefix delete failed: removed=%+v remaining=%d", removed, len(cfg.Targets))
	}

	if _, err := cfg.Delete("deadbeef"); err == nil {
		t.Fatal("expected error deleting unknown id")
	}
}
