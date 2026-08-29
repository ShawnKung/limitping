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
	root.AddCommand(newStatusCmd(options), newPingCmd(options), newVersionCmd(), newUpdateCmd())
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
				return pushSnapshot(cmd.Context(), cmd.ErrOrStderr(), snapshot, collectionDuration, options.pushMetric, false, false)
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
	cmd := &cobra.Command{
		Use:     "ping",
		Aliases: []string{"p"},
		Short:   "用当前最弱的可见模型发送最小 ping",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return executePing(cmd.Context(), cmd.InOrStdin(), cmd.OutOrStdout(), cmd.ErrOrStderr(), dryRun, ifFull, options.pushMetric)
		},
	}
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "只打印将执行的命令")
	cmd.Flags().BoolVar(&ifFull, "if-5h-full", false, "仅在 5h 可用额度为 100% 时执行")
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

func executePing(ctx context.Context, stdin io.Reader, stdout, stderr io.Writer, dryRun, ifFull bool, pushEndpoint string) error {
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
				return pushSnapshot(ctx, stdout, snapshot, collectionDuration, pushEndpoint, dryRun, false)
			}
			return nil
		}
		fmt.Fprintln(stdout, "5h 可用额度为 100%，满足触发条件。")
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
			return pushSnapshot(ctx, stdout, snapshot, collectionDuration, pushEndpoint, true, false)
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
		pushErr := pushSnapshot(ctx, stdout, snapshot, collectionDuration, pushEndpoint, false, pingErr == nil)
		return errors.Join(wrapPingError(pingErr), wrapStateError(stateErr), pushErr)
	}
	if pingErr != nil {
		return fmt.Errorf("ping 失败: %w", pingErr)
	}
	return wrapStateError(stateErr)
}

func pushSnapshot(ctx context.Context, out io.Writer, snapshot *usage.Snapshot, collectionDuration time.Duration, endpoint string, dryRun, pingCompleted bool) error {
	lastSuccessfulPing, err := pingstate.LastSuccessfulPing()
	if err != nil {
		return fmt.Errorf("读取最近成功 ping: %w", err)
	}
	result, err := metrics.Deliver(ctx, snapshot, collectionDuration, metrics.Options{
		GatewayURL:         endpoint,
		DryRun:             dryRun,
		PingCompleted:      pingCompleted,
		LastSuccessfulPing: lastSuccessfulPing,
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
