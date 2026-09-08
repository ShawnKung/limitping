package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os/exec"
	"runtime/debug"
	"strings"
	"time"
	"unicode"

	"github.com/spf13/cobra"

	"github.com/ShawnKung/limitping/internal/align"
	"github.com/ShawnKung/limitping/internal/auth"
	"github.com/ShawnKung/limitping/internal/metrics"
	"github.com/ShawnKung/limitping/internal/models"
	"github.com/ShawnKung/limitping/internal/pingstate"
	"github.com/ShawnKung/limitping/internal/updater"
	"github.com/ShawnKung/limitping/internal/usage"
)

var (
	version = "dev"
	commit  = "unknown"
)

const zhUsageTemplate = `用法:{{if .Runnable}}
  {{.UseLine}}{{end}}{{if .HasAvailableSubCommands}}
  {{.CommandPath}} [command]{{end}}{{if gt (len .Aliases) 0}}

别名:
  {{.NameAndAliases}}{{end}}{{if .HasExample}}

示例:
{{.Example}}{{end}}{{if .HasAvailableSubCommands}}{{$cmds := .Commands}}

可用命令:{{range $cmds}}{{if (or .IsAvailableCommand (eq .Name "help"))}}
  {{rpad .NameAndAliases 24}} {{.Short}}{{end}}{{end}}{{end}}{{if .HasAvailableLocalFlags}}

选项:
{{.LocalFlags.FlagUsages | trimTrailingWhitespaces}}{{end}}{{if .HasAvailableInheritedFlags}}

全局选项:
{{.InheritedFlags.FlagUsages | trimTrailingWhitespaces}}{{end}}{{if .HasAvailableSubCommands}}

使用 "{{.CommandPath}} [command] --help" 查看命令详情。{{end}}
`

func Run(args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	root := newRootCmd()
	root.SetArgs(args)
	root.SetIn(stdin)
	root.SetOut(stdout)
	root.SetErr(stderr)
	return root.Execute()
}

func newRootCmd() *cobra.Command {
	options := &globalOptions{}
	buildVersion := currentVersion()
	root := &cobra.Command{
		Use:           "limitping",
		Short:         "查询 Codex 用量并发送最小 ping",
		Long:          "limitping 查询 Codex 的 5h/周用量和重置券，并可用当前最弱的可见模型发送最小 ping。",
		Version:       buildVersion,
		SilenceUsage:  true,
		SilenceErrors: true,
		PersistentPreRunE: func(cmd *cobra.Command, _ []string) error {
			flag := cmd.Root().PersistentFlags().Lookup("push-metric")
			if flag != nil && flag.Changed && strings.TrimSpace(options.pushMetric) == "" {
				return fmt.Errorf("--push-metric 需要 Pushgateway endpoint")
			}
			if options.pushMetric != "" {
				return metrics.ValidateGatewayURL(options.pushMetric)
			}
			return nil
		},
	}
	root.PersistentFlags().StringVar(&options.pushMetric, "push-metric", "", "将用量和 ping 结果推送到指定的 Pushgateway endpoint")
	root.SetVersionTemplate("limitping {{.Version}}\n")
	root.SetUsageTemplate(zhUsageTemplate)
	root.AddCommand(newStatusCmd(options), newPingCmd(options), newAlignCmd(), newVersionCmd(), newUpdateCmd())
	root.InitDefaultCompletionCmd()
	localizeCompletionCommand(root)
	root.SetHelpCommand(newHelpCommand())
	root.InitDefaultVersionFlag()
	if versionFlag := root.Flags().Lookup("version"); versionFlag != nil {
		versionFlag.Usage = "显示版本号"
	}
	localizeHelpFlags(root)
	return root
}

type globalOptions struct {
	pushMetric string
}

func newHelpCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "help [command]",
		Short: "查看任意命令的帮助",
		Long:  "查看应用中任意命令的帮助。\n输入 limitping help [command] 查看完整详情。",
		RunE: func(cmd *cobra.Command, args []string) error {
			target, _, err := cmd.Root().Find(args)
			if target == nil || err != nil {
				return fmt.Errorf("未知帮助主题 %q", args)
			}
			return target.Help()
		},
	}
}

func localizeCompletionCommand(root *cobra.Command) {
	completion := findChildCommand(root, "completion")
	if completion == nil {
		return
	}
	completion.Short = "生成 shell 补全脚本"
	completion.Long = "生成 limitping 的 shell 补全脚本。"
	for _, child := range completion.Commands() {
		child.Short = fmt.Sprintf("生成 %s 补全脚本", child.Name())
		child.Long = fmt.Sprintf("生成 limitping 的 %s 补全脚本。", child.Name())
		if flag := child.Flags().Lookup("no-descriptions"); flag != nil {
			flag.Usage = "禁用补全说明"
		}
	}
}

func findChildCommand(parent *cobra.Command, name string) *cobra.Command {
	for _, child := range parent.Commands() {
		if child.Name() == name {
			return child
		}
	}
	return nil
}

func newStatusCmd(options *globalOptions) *cobra.Command {
	var jsonOutput bool
	cmd := &cobra.Command{
		Use:     "status",
		Aliases: []string{"s", "stat"},
		Short:   "查看 Codex 的 5h/周用量和重置券",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if !jsonOutput {
				fmt.Fprintln(cmd.ErrOrStderr(), "正在查询 codex 用量...")
			}
			started := time.Now()
			snapshot, err := readUsage(cmd.Context())
			collectionDuration := time.Since(started)
			if err != nil {
				return err
			}
			if jsonOutput {
				encoder := json.NewEncoder(cmd.OutOrStdout())
				encoder.SetIndent("", "  ")
				if err := encoder.Encode([]*usage.Snapshot{snapshot}); err != nil {
					return err
				}
			} else {
				printStatus(cmd.OutOrStdout(), snapshot)
			}
			if options.pushMetric != "" {
				return pushSnapshot(cmd.Context(), cmd.ErrOrStderr(), snapshot, collectionDuration, options.pushMetric, false, false, nil)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&jsonOutput, "json", false, "以 JSON 格式输出")
	return cmd
}

func newPingCmd(options *globalOptions) *cobra.Command {
	var dryRun bool
	var ifFull bool
	var withoutAlign bool
	var untilAnchored bool
	cmd := &cobra.Command{
		Use:     "ping",
		Aliases: []string{"p"},
		Short:   "用当前最弱的可见模型发送最小 ping",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return executePing(cmd.Context(), cmd.InOrStdin(), cmd.OutOrStdout(), cmd.ErrOrStderr(), pingParams{
				dryRun:        dryRun,
				ifFull:        ifFull,
				withoutAlign:  withoutAlign,
				untilAnchored: untilAnchored,
				pushEndpoint:  options.pushMetric,
			})
		},
	}
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "只打印将执行的命令")
	cmd.Flags().BoolVar(&ifFull, "if-5h-full", false, "仅在 5h 可用额度为 100% 时执行；默认会结合已配置的 cron 目标做相位对齐（见 align 子命令），仅在满额且到达对齐时间后才 ping")
	cmd.Flags().BoolVar(&withoutAlign, "without-align", false, "关闭相位对齐，只保留满额判断（满额即 ping）；仅在配合 --if-5h-full 时有意义")
	cmd.Flags().BoolVar(&untilAnchored, "until-anchored", false, "持续 ping 直到 5h 窗口开始计时：起始输入较大，每轮翻倍，每次 ping 后回读用量确认是否已锚定；与 --if-5h-full/--without-align 互斥使用")
	return cmd
}

func newVersionCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "version",
		Aliases: []string{"v", "ver"},
		Short:   "显示版本号和构建提交",
		Args:    cobra.NoArgs,
		Run: func(cmd *cobra.Command, _ []string) {
			fmt.Fprintf(cmd.OutOrStdout(), "limitping %s\ncommit: %s\n", currentVersion(), currentCommit())
		},
	}
}

func newUpdateCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "update",
		Short: "检查 GitHub Release 并自动更新",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			current := currentVersion()
			fmt.Fprintf(cmd.OutOrStdout(), "正在检查更新（当前 %s）...\n", current)
			result, err := updater.NewClient().Update(cmd.Context(), current)
			if err != nil {
				return err
			}
			switch {
			case result.Updated:
				fmt.Fprintf(cmd.OutOrStdout(), "已更新: %s -> %s\n安装位置: %s\n", result.Current, result.Latest, result.Path)
			case result.Comparison > 0:
				fmt.Fprintf(cmd.OutOrStdout(), "当前版本 %s 高于最新发布版 %s，未更新。\n", result.Current, result.Latest)
			default:
				fmt.Fprintf(cmd.OutOrStdout(), "已是最新版本: %s\n", result.Current)
			}
			return nil
		},
	}
}

func currentVersion() string {
	if version != "" && version != "dev" {
		return strings.TrimPrefix(version, "v")
	}
	if info, ok := debug.ReadBuildInfo(); ok && info.Main.Version != "" && info.Main.Version != "(devel)" {
		return strings.TrimPrefix(info.Main.Version, "v")
	}
	return "dev"
}

func currentCommit() string {
	if commit != "" && commit != "unknown" {
		return commit
	}
	if info, ok := debug.ReadBuildInfo(); ok {
		for _, setting := range info.Settings {
			if setting.Key == "vcs.revision" && setting.Value != "" {
				return setting.Value
			}
		}
	}
	return "unknown"
}

func localizeHelpFlags(cmd *cobra.Command) {
	cmd.InitDefaultHelpFlag()
	if help := cmd.Flags().Lookup("help"); help != nil {
		help.Usage = "显示帮助信息"
	}
	for _, child := range cmd.Commands() {
		localizeHelpFlags(child)
	}
}

type pingParams struct {
	dryRun        bool
	ifFull        bool
	withoutAlign  bool
	untilAnchored bool
	pushEndpoint  string
}

// ping until-anchored 模式的参数：从一个较大的起始长度出发，每轮把 prompt 长度翻倍，
// 每次 ping 后等待片刻再检查 5h 窗口是否已开始计时，直到计时或到达轮数上限。
const (
	pingAnchorStartRunes = 2000
	pingAnchorMaxRounds  = 6
	pingAnchorCheckDelay = 15 * time.Second
)

func executePing(ctx context.Context, stdin io.Reader, stdout, stderr io.Writer, params pingParams) error {
	if params.untilAnchored {
		return executePingUntilAnchored(ctx, stdout, stderr, params)
	}
	dryRun := params.dryRun
	ifFull := params.ifFull
	pushEndpoint := params.pushEndpoint
	// 相位对齐是 --if-5h-full 的默认行为；--without-align 关闭它，退化为“满额即 ping”。
	useAlign := ifFull && !params.withoutAlign

	var planner *align.Planner
	if useAlign {
		cfg, err := align.Load()
		if err != nil {
			return err
		}
		planner, err = align.NewPlanner(cfg)
		if err != nil {
			return err
		}
		if !planner.HasTargets() {
			fmt.Fprintln(stderr, "提示：未配置对齐目标（limitping align add \"<cron>\"），本次退化为满额即 ping。")
		}
	}

	var snapshot *usage.Snapshot
	var collectionDuration time.Duration
	collect := func() error {
		started := time.Now()
		var err error
		snapshot, err = readUsage(ctx)
		collectionDuration = time.Since(started)
		return err
	}
	if ifFull {
		if err := collect(); err != nil {
			return fmt.Errorf("检查 5h 用量: %w", err)
		}
		ok, reason := shouldPingFull(snapshot.FiveHour)
		if !ok {
			fmt.Fprintf(stdout, "跳过 ping：%s\n", reason)
			if pushEndpoint != "" {
				return pushSnapshot(ctx, stdout, snapshot, collectionDuration, pushEndpoint, dryRun, false, planner)
			}
			return nil
		}
		fmt.Fprintln(stdout, "5h 可用额度为 100%，满足触发条件。")
		if planner != nil && planner.HasTargets() {
			decision := planner.Decide(time.Now())
			fmt.Fprintf(stdout, "对齐决策：%s\n", decision.Reason)
			if decision.Action == align.ActionHold {
				fmt.Fprintf(stdout, "本次延后 ping（目标 %s）。\n", decision.Target.Format("01-02 15:04"))
				if pushEndpoint != "" {
					return pushSnapshot(ctx, stdout, snapshot, collectionDuration, pushEndpoint, dryRun, false, planner)
				}
				return nil
			}
		}
	}
	model, err := models.Weakest(ctx)
	if err != nil {
		return err
	}
	commandArgs := []string{"exec", "-m", model, "-c", "model_reasoning_effort=low", "ping"}
	commandText := "codex " + shellJoin(commandArgs)
	if dryRun {
		fmt.Fprintf(stdout, "将执行: %s\n", commandText)
		if pushEndpoint != "" {
			if snapshot == nil {
				if err := collect(); err != nil {
					return fmt.Errorf("采集推送指标: %w", err)
				}
			}
			return pushSnapshot(ctx, stdout, snapshot, collectionDuration, pushEndpoint, true, false, planner)
		}
		return nil
	}
	fmt.Fprintf(stdout, "执行: %s\n", commandText)
	cmd := exec.CommandContext(ctx, "codex", commandArgs...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = stdin, stdout, stderr
	pingErr := cmd.Run()
	var stateErr error
	if pingErr == nil {
		stateErr = pingstate.Record(time.Now())
	}
	if pushEndpoint != "" {
		if err := collect(); err != nil {
			metricErr := fmt.Errorf("采集推送指标失败: %w", err)
			if pingErr != nil {
				return errors.Join(fmt.Errorf("ping 失败: %w", pingErr), metricErr)
			}
			return metricErr
		}
		pushErr := pushSnapshot(ctx, stdout, snapshot, collectionDuration, pushEndpoint, false, pingErr == nil, planner)
		return errors.Join(wrapPingError(pingErr), wrapStateError(stateErr), pushErr)
	}
	if pingErr != nil {
		return fmt.Errorf("ping 失败: %w", pingErr)
	}
	return wrapStateError(stateErr)
}

// executePingUntilAnchored 持续发送 ping 直到 5h 窗口开始计时（被首次使用锚定）。
//
// 极小的 ping 未必能立刻让窗口进入计时，且窗口是否计时无法从单次调用即时得知，
// 需要回读用量确认。为提高单轮成功率同时避免一开始就发过大的请求，这里从一个较大的
// 起始 prompt 长度出发，每轮把长度翻倍，逐步增加单次消耗；每次 ping 后等待片刻再回读
// 用量，一旦检测到窗口在计时立即停止，或到达轮数上限后停止。
func executePingUntilAnchored(ctx context.Context, stdout, stderr io.Writer, params pingParams) error {
	if snapshot, err := readUsage(ctx); err == nil {
		if window := snapshot.FiveHour; window != nil && window.Active {
			fmt.Fprintln(stdout, "5h 窗口已在计时，无需 ping。")
			return maybePush(ctx, stdout, snapshot, params, false, nil)
		}
	}

	model, err := models.Weakest(ctx)
	if err != nil {
		return err
	}

	runes := pingAnchorStartRunes
	for round := 1; round <= pingAnchorMaxRounds; round++ {
		prompt := anchorPrompt(runes)
		fmt.Fprintf(stdout, "第 %d 轮：发送 ping（输入约 %d 字符，模型 %s）...\n", round, len([]rune(prompt)), model)
		if params.dryRun {
			fmt.Fprintf(stdout, "将执行: codex exec -m %s -c model_reasoning_effort=low -（stdin 输入约 %d 字符）\n", model, runes)
		} else {
			if err := runAnchorPing(ctx, stdout, stderr, model, prompt); err != nil {
				return fmt.Errorf("第 %d 轮 ping 失败: %w", round, err)
			}
			_ = pingstate.Record(time.Now())
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(pingAnchorCheckDelay):
			}
		}

		snapshot, err := readUsage(ctx)
		if err != nil {
			return fmt.Errorf("回读 5h 用量: %w", err)
		}
		window := snapshot.FiveHour
		if params.dryRun {
			fmt.Fprintln(stdout, "dry-run：跳过实际 ping 与锚定检查。")
			return maybePush(ctx, stdout, snapshot, params, false, nil)
		}
		if window != nil && window.Active {
			fmt.Fprintf(stdout, "5h 窗口已开始计时，%s 后重置。\n", formatDurationCN(time.Duration(window.RemainingSeconds)*time.Second))
			return maybePush(ctx, stdout, snapshot, params, true, nil)
		}
		fmt.Fprintln(stdout, "窗口尚未开始计时，加大输入后重试。")
		runes *= 2
	}

	return fmt.Errorf("已尝试 %d 轮仍未观察到 5h 窗口开始计时", pingAnchorMaxRounds)
}

// anchorPrompt 生成指定长度的输入，用重复段落放大输入 token，并要求模型只做最小回复。
func anchorPrompt(runes int) string {
	const seg = "分布式限流滚动窗口令牌桶漏桶共识算法幂等背压。"
	if runes < 1 {
		runes = 1
	}
	segRunes := []rune(seg)
	body := make([]rune, 0, runes)
	for len(body) < runes {
		body = append(body, segRunes...)
	}
	body = body[:runes]
	return "这是背景资料，只需回复两个字「已读」，不要复述：\n" + string(body)
}

// runAnchorPing 通过 stdin 把长输入喂给 codex exec，避免超长命令行参数。
func runAnchorPing(ctx context.Context, stdout, stderr io.Writer, model, prompt string) error {
	cmd := exec.CommandContext(ctx, "codex", "exec", "-m", model, "-c", "model_reasoning_effort=low", "-")
	cmd.Stdin = strings.NewReader(prompt)
	cmd.Stdout, cmd.Stderr = stdout, stderr
	return cmd.Run()
}

// maybePush 在配置了 --push-metric 时推送快照，否则直接返回。
func maybePush(ctx context.Context, stdout io.Writer, snapshot *usage.Snapshot, params pingParams, pingCompleted bool, planner *align.Planner) error {
	if params.pushEndpoint == "" {
		return nil
	}
	return pushSnapshot(ctx, stdout, snapshot, 0, params.pushEndpoint, params.dryRun, pingCompleted, planner)
}

func pushSnapshot(ctx context.Context, out io.Writer, snapshot *usage.Snapshot, collectionDuration time.Duration, endpoint string, dryRun, pingCompleted bool, planner *align.Planner) error {
	lastSuccessfulPing, err := pingstate.LastSuccessfulPing()
	if err != nil {
		return fmt.Errorf("读取最近成功 ping: %w", err)
	}
	var plannedPings []time.Time
	if planner != nil && planner.HasTargets() {
		for _, plan := range planner.Upcoming(time.Now(), lastSuccessfulPing, plannedPingCount) {
			plannedPings = append(plannedPings, plan.PingAt)
		}
	}
	result, err := metrics.Deliver(ctx, snapshot, collectionDuration, metrics.Options{
		GatewayURL:         endpoint,
		DryRun:             dryRun,
		PingCompleted:      pingCompleted,
		LastSuccessfulPing: lastSuccessfulPing,
		PlannedPings:       plannedPings,
	})
	if err != nil {
		return err
	}
	if dryRun {
		fmt.Fprintf(out, "将推送指标到: %s\n%s", result.URL, result.Payload)
		return nil
	}
	fmt.Fprintf(out, "指标已推送: %s\n", result.URL)
	return nil
}

// plannedPingCount 是上报及预览默认展示的未来计划 ping 数量。
const plannedPingCount = 5

func wrapPingError(err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("ping 失败: %w", err)
}

func wrapStateError(err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("记录最近成功 ping: %w", err)
}

func readUsage(ctx context.Context) (*usage.Snapshot, error) {
	authClient, err := auth.NewCodex()
	if err != nil {
		return nil, err
	}
	readCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	return usage.NewClient(authClient).Read(readCtx)
}

func shouldPingFull(window *usage.Window) (bool, string) {
	if window == nil {
		return false, "当前没有生效的 5h 窗口"
	}
	if window.WindowSeconds != 5*60*60 {
		return false, fmt.Sprintf("窗口长度为 %s，不是 5h", time.Duration(window.WindowSeconds)*time.Second)
	}
	if window.UsedPercent > 0 {
		available := math.Max(0, 100-window.UsedPercent)
		return false, fmt.Sprintf("5h 可用额度为 %.1f%%，尚未恢复到 100%%", available)
	}
	return true, ""
}

func printStatus(out io.Writer, snapshot *usage.Snapshot) {
	plan := ""
	if snapshot.Plan != "" {
		plan = " (" + snapshot.Plan + ")"
	}
	durationWidth := 0
	for _, window := range []*usage.Window{snapshot.FiveHour, snapshot.Weekly} {
		if window == nil {
			continue
		}
		width := displayWidth(formatDurationCN(time.Duration(window.RemainingSeconds) * time.Second))
		if width > durationWidth {
			durationWidth = width
		}
	}
	fmt.Fprintf(out, "codex%s\n", plan)
	printWindow(out, "5h", snapshot.FiveHour, durationWidth)
	printWindow(out, "周", snapshot.Weekly, durationWidth)
	printResetCredits(out, snapshot.ResetCredits)
}

func printResetCredits(out io.Writer, credits *usage.ResetCredits) {
	if credits == nil {
		return
	}
	unit := "张"
	fmt.Fprintf(out, "  重置券 %d %s可用\n", credits.AvailableCount, unit)
	for _, credit := range credits.Credits {
		parts := []string{creditStatus(credit)}
		if granted, ok := parseLocalTime(credit.GrantedAt); ok {
			parts = append(parts, "发放于 "+granted.Format("01-02 15:04"))
		}
		if expires, ok := parseLocalTime(credit.ExpiresAt); ok {
			expiry := "有效期至 " + expires.Format("01-02 15:04") + " " + zoneName(expires)
			if remaining := time.Until(expires); remaining > 0 && credit.RedeemedAt == "" {
				expiry += " (剩 " + formatDurationCN(remaining) + ")"
			}
			parts = append(parts, expiry)
		}
		fmt.Fprintf(out, "    - %s\n", strings.Join(parts, "，"))
	}
}

func creditStatus(credit usage.ResetCredit) string {
	switch credit.Status {
	case "available", "":
		return "可用"
	case "redeemed":
		return "已使用"
	case "expired":
		return "已过期"
	default:
		return credit.Status
	}
}

func parseLocalTime(raw string) (time.Time, bool) {
	parsed, err := time.Parse(time.RFC3339, raw)
	return parsed.Local(), err == nil
}

func printWindow(out io.Writer, label string, window *usage.Window, durationWidth int) {
	if window == nil {
		fmt.Fprintf(out, "  %s 当前未生效\n", padDisplay(label, 6))
		return
	}
	reset := ""
	if window.ResetsAt != "" {
		if parsed, err := time.Parse(time.RFC3339, window.ResetsAt); err == nil {
			local := parsed.Local()
			reset = fmt.Sprintf(" (%s %s)", chineseWeekday(local.Weekday()), local.Format("15:04 ")+zoneName(local))
		}
	}
	remaining := padDisplayLeft(formatDurationCN(time.Duration(window.RemainingSeconds)*time.Second), durationWidth)
	remainingPercent := math.Max(0, math.Min(100, 100-window.UsedPercent))
	fmt.Fprintf(out, "  %s %s  剩余 %5.1f%%  %s 后重置%s\n",
		padDisplay(label, 6), progressBarRemaining(remainingPercent), remainingPercent,
		remaining, reset)
}

func padDisplay(value string, width int) string {
	padding := width - displayWidth(value)
	if padding < 0 {
		padding = 0
	}
	return value + strings.Repeat(" ", padding)
}

func padDisplayLeft(value string, width int) string {
	padding := width - displayWidth(value)
	if padding < 0 {
		padding = 0
	}
	return strings.Repeat(" ", padding) + value
}

func displayWidth(value string) int {
	width := 0
	for _, r := range value {
		switch {
		case r == 0:
		case unicode.Is(unicode.Mn, r), unicode.Is(unicode.Me, r):
		case unicode.Is(unicode.Han, r),
			unicode.Is(unicode.Hangul, r),
			unicode.In(r, unicode.Hiragana, unicode.Katakana):
			width += 2
		case r >= 0xFF01 && r <= 0xFF60, r >= 0xFFE0 && r <= 0xFFE6:
			width += 2
		default:
			width++
		}
	}
	return width
}

func progressBarRemaining(percent float64) string {
	filled := int(math.Round(percent / 10))
	if filled < 0 {
		filled = 0
	}
	if filled > 10 {
		filled = 10
	}
	return "[" + strings.Repeat("█", filled) + strings.Repeat("░", 10-filled) + "]"
}

func formatDurationCN(duration time.Duration) string {
	if duration < 0 {
		duration = 0
	}
	minutes := int64(duration / time.Minute)
	days := minutes / (24 * 60)
	hours := minutes / 60 % 24
	minutes %= 60

	result := ""
	if days > 0 {
		result = fmt.Sprintf("%d天", days)
	}
	if hours > 0 {
		if result == "" {
			result = fmt.Sprintf("%d时", hours)
		} else {
			result += fmt.Sprintf("%2d时", hours)
		}
	}
	if minutes > 0 {
		if result == "" {
			result = fmt.Sprintf("%d分", minutes)
		} else {
			result += fmt.Sprintf("%2d分", minutes)
		}
	}
	if result == "" {
		return "0分"
	}
	return result
}

func chineseWeekday(day time.Weekday) string {
	return []string{"周日", "周一", "周二", "周三", "周四", "周五", "周六"}[day]
}

func zoneName(t time.Time) string {
	_, offset := t.Zone()
	if offset%3600 == 0 {
		return fmt.Sprintf("UTC%+d", offset/3600)
	}
	return t.Format("MST")
}

func shellJoin(args []string) string {
	quoted := make([]string, len(args))
	for i, arg := range args {
		if arg != "" && strings.IndexFunc(arg, func(r rune) bool {
			return !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("_./:=-", r))
		}) == -1 {
			quoted[i] = arg
		} else {
			quoted[i] = "'" + strings.ReplaceAll(arg, "'", "'\\''") + "'"
		}
	}
	return strings.Join(quoted, " ")
}
