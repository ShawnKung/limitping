package align

import (
	"fmt"
	"time"
)

// WindowLength 是 Codex 5h 滚动窗口长度。
const WindowLength = 5 * time.Hour

// Tolerance 是判定“已到达锚点”的容差。外层 cron 通常每分钟触发一次，
// 取略大于一分钟可保证在锚点所在的那一分钟（或前一分钟）就 ping，避免因
// 分钟粒度而错过、反被顶到下一分钟造成重置时刻超调。
const Tolerance = 90 * time.Second

// Action 表示某一刻窗口满额时的调度决策。
type Action int

const (
	// ActionPing 表示应立即执行 ping。
	ActionPing Action = iota
	// ActionHold 表示应跳过本次（延后），等待更接近目标锚点时再 ping。
	ActionHold
)

// Decision 描述一次调度判定的结果，用于实际执行与 dry-run 展示。
type Decision struct {
	Action    Action
	Now       time.Time
	HasTarget bool
	// Target 是本次对齐所瞄准的目标刷新时刻 T。
	Target time.Time
	// Anchor 是理想的 ping 锚点 A* = T - Window，在此锚定可使窗口于 T 重置。
	Anchor time.Time
	// PlannedPing 是本轮满额窗口应执行 ping 的时间。它可能早于最终锚点，
	// 用于把对齐目标前的总等待时间均匀摊到多个窗口里。
	PlannedPing time.Time
	// Delay 是从现在到本轮计划 ping 还需等待的时长（Hold 时为正，Ping 时约为 0 或不适用）。
	Delay time.Duration
	// MaxDelay 是本次瞄准目标自己的延时预算。
	MaxDelay time.Duration
	Reason   string
}

// PingPlan 是前向模拟得到的一次计划：PingAt 锚定，RefreshAt 重置（PingAt+Window）。
type PingPlan struct {
	PingAt    time.Time
	RefreshAt time.Time
}

// Planner 依据一组 cron 目标，把满额窗口的重置时刻尽量对齐到目标时间。
type Planner struct {
	targets   []compiledTarget
	window    time.Duration
	tolerance time.Duration
}

// NewPlanner 根据配置构造 Planner。无 cron 时 Planner 退化为“满额即 ping”的原始行为。
func NewPlanner(cfg *Config) (*Planner, error) {
	targets, err := cfg.Compile()
	if err != nil {
		return nil, err
	}
	return &Planner{
		targets:   targets,
		window:    WindowLength,
		tolerance: Tolerance,
	}, nil
}

// WithWindow 覆盖窗口长度（主要用于按后端实际窗口长度校准和测试）。
func (p *Planner) WithWindow(window time.Duration) *Planner {
	if window > 0 {
		p.window = window
	}
	return p
}

// HasTargets 表示是否配置了对齐目标。
func (p *Planner) HasTargets() bool {
	return len(p.targets) > 0
}

// Decide 在窗口满额的前提下，判断此刻应 ping 还是 hold 延后。
// 调用方若知道当前窗口的满额起点，应优先使用 DecideAtFull 或 DecideFromLastPing，
// 避免均匀摊分计划在反复轮询时漂移。每个目标使用它自己的延时预算。
func (p *Planner) Decide(now time.Time) Decision {
	return p.DecideAtFull(now, now)
}

// DecideFromLastPing 基于最近一次成功 ping 推导当前窗口满额起点，再做对齐决策。
// 这样 cron 每分钟重试时会围绕同一个满额起点稳定计算本轮计划 ping 时间。
func (p *Planner) DecideFromLastPing(now time.Time, lastPing *time.Time) Decision {
	now = now.In(time.Local)
	full := now
	if lastPing != nil {
		nextFull := lastPing.In(time.Local).Add(p.window)
		if !nextFull.After(now) {
			full = nextFull
		}
	}
	return p.DecideAtFull(full, now)
}

// DecideAtFull 在给定窗口满额起点 full 的前提下，判断 now 是否应 ping。
func (p *Planner) DecideAtFull(full, now time.Time) Decision {
	full = full.In(time.Local)
	now = now.In(time.Local)
	if len(p.targets) == 0 {
		return Decision{Action: ActionPing, Now: now, Reason: "未配置对齐目标，满额即 ping"}
	}
	// 只考虑锚点仍可达（A* = T - window >= now - tol）的目标，取锚点最近的一个。
	threshold := full.Add(p.window).Add(-p.tolerance)
	target, maxDelay, ok := p.nextOnOrAfter(threshold)
	if !ok {
		return Decision{Action: ActionPing, Now: now, Reason: "无可用 cron 目标，满额即 ping"}
	}
	anchor := target.Add(-p.window)
	plannedPing, feasible := p.plannedPingAt(full, anchor, maxDelay)
	delay := plannedPing.Sub(now)
	base := Decision{Now: now, HasTarget: true, Target: target, Anchor: anchor, PlannedPing: plannedPing, Delay: delay, MaxDelay: maxDelay}
	switch {
	case !feasible:
		base.Action = ActionPing
		base.Reason = fmt.Sprintf("距目标锚点还需累计等待 %s，超过延时预算 %s，正常链式 ping",
			roundDuration(anchor.Sub(full)), roundDuration(maxDelay))
	case delay <= p.tolerance:
		base.Action = ActionPing
		base.Reason = fmt.Sprintf("已到本轮计划点，ping 使窗口约在 %s 重置",
			plannedPing.Add(p.window).Format("01-02 15:04"))
	default:
		base.Action = ActionHold
		base.Reason = fmt.Sprintf("均匀延后：等到 %s 再 ping，逐步对齐 %s 刷新",
			plannedPing.Format("01-02 15:04"), target.Format("01-02 15:04"))
	}
	return base
}

// Upcoming 推导下一个满额时刻，并前向模拟未来 n 次计划 ping，供预览与指标共用。
// lastPing 为最近一次成功 ping：若当前窗口仍在运行（lastPing+window 晚于 now），
// 则从该窗口重置时刻起算；否则视为此刻已满额。
func (p *Planner) Upcoming(now time.Time, lastPing *time.Time, n int) []PingPlan {
	start := now
	if lastPing != nil {
		if nextFull := lastPing.Add(p.window); nextFull.After(now) {
			start = nextFull
		}
	}
	return p.NextPingTimes(start, n)
}

// NextPingTimes 从 start 时刻（应为窗口满额时刻）出发，前向模拟未来 n 次计划 ping。
// 模拟假设每次 ping 后窗口在 window 后再次满额（工具自身链式语义），不建模真实使用。
func (p *Planner) NextPingTimes(start time.Time, n int) []PingPlan {
	plans := make([]PingPlan, 0, n)
	full := start.In(time.Local)
	for i := 0; i < n; i++ {
		decision := p.DecideAtFull(full, full)
		pingAt := full
		if decision.Action == ActionHold {
			pingAt = decision.PlannedPing
		}
		if pingAt.Before(full) {
			pingAt = full
		}
		plans = append(plans, PingPlan{PingAt: pingAt, RefreshAt: pingAt.Add(p.window)})
		full = pingAt.Add(p.window)
	}
	return plans
}

func (p *Planner) plannedPingAt(full, anchor time.Time, maxDelay time.Duration) (time.Time, bool) {
	untilAnchor := anchor.Sub(full)
	if untilAnchor <= p.tolerance {
		return full, true
	}
	windowsBeforeAnchor := int(untilAnchor / p.window)
	plannedPings := windowsBeforeAnchor + 1
	totalHold := untilAnchor - time.Duration(windowsBeforeAnchor)*p.window
	if totalHold < 0 {
		totalHold = 0
	}
	if totalHold > maxDelay*time.Duration(plannedPings) {
		return full, false
	}
	return full.Add(totalHold / time.Duration(plannedPings)), true
}

// nextOnOrAfter 返回所有 cron 中不早于 t 的最近一次触发时刻，及该目标自己的延时预算。
func (p *Planner) nextOnOrAfter(t time.Time) (time.Time, time.Duration, bool) {
	// cron.Schedule.Next 返回严格晚于入参的时刻，减一纳秒以纳入 t 本身。
	probe := t.Add(-time.Nanosecond)
	var earliest time.Time
	var maxDelay time.Duration
	found := false
	for _, target := range p.targets {
		next := target.schedule.Next(probe)
		if next.IsZero() {
			continue
		}
		if !found || next.Before(earliest) {
			earliest, maxDelay, found = next, target.maxDelay, true
		}
	}
	return earliest, maxDelay, found
}

func roundDuration(d time.Duration) time.Duration {
	if d < 0 {
		d = 0
	}
	return d.Round(time.Minute)
}
