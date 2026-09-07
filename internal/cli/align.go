package cli

import (
	"fmt"
	"io"
	"time"

	"github.com/spf13/cobra"

	"github.com/ShawnKung/limitping/internal/align"
	"github.com/ShawnKung/limitping/internal/pingstate"
)

func newAlignCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "align",
		Short: "管理刷新对齐目标（cron），并预览未来的 ping/刷新时间",
		Long: "align 用一组 cron 表达式声明希望 5h 窗口刷新命中的目标时间。\n" +
			"平时每 5h 正常链式 ping，临近目标时会在延时预算内延后，使窗口尽量在目标时间重置。\n" +
			"配置保存在 ${XDG_CONFIG_HOME:-~/.config}/limitping/config.json。",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return cmd.Help()
		},
	}
	cmd.AddCommand(newAlignListCmd(), newAlignAddCmd(), newAlignDeleteCmd(), newAlignClearCmd(), newAlignPreviewCmd())
	return cmd
}

func newAlignListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "列出当前对齐目标（每行：id、cron、该目标的延时预算 max-delay）",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := align.Load()
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			if len(cfg.Targets) == 0 {
				fmt.Fprintln(out, "未配置对齐目标。使用 limitping align add \"<cron>\" 添加。")
			} else {
				for _, target := range cfg.Targets {
					fmt.Fprintf(out, "%s  %-11s  max-delay=%s\n", target.ID, target.Cron, target.MaxDelay().Round(time.Minute))
				}
			}
			if path, err := align.ConfigPath(); err == nil {
				fmt.Fprintf(out, "配置文件: %s\n", path)
			}
			return nil
		},
	}
}

func newAlignAddCmd() *cobra.Command {
	var maxDelay time.Duration
	cmd := &cobra.Command{
		Use:   "add \"<cron>\"",
		Short: "追加一个 cron 目标（标准 5 字段），返回其 id；--max-delay 设置该目标自己的延时预算（默认 90m）",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := align.Load()
			if err != nil {
				return err
			}
			maxDelayMinutes := 0
			if cmd.Flags().Changed("max-delay") {
				maxDelayMinutes = int(maxDelay.Minutes())
			}
			target, created, err := cfg.Add(args[0], maxDelayMinutes)
			if err != nil {
				return err
			}
			if err := align.Save(cfg); err != nil {
				return err
			}
			verb := "已更新对齐目标"
			if created {
				verb = "已添加对齐目标"
			}
			fmt.Fprintf(cmd.OutOrStdout(), "%s: %s  %s  max-delay=%s\n",
				verb, target.ID, target.Cron, target.MaxDelay().Round(time.Minute))
			return nil
		},
	}
	cmd.Flags().DurationVar(&maxDelay, "max-delay", 0, "该目标临近时单窗口最大延后时长（如 90m），缺省 90m")
	return cmd
}

func newAlignDeleteCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "delete <id>",
		Aliases: []string{"rm"},
		Short:   "按 id（或唯一前缀）删除一个对齐目标",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := align.Load()
			if err != nil {
				return err
			}
			removed, err := cfg.Delete(args[0])
			if err != nil {
				return err
			}
			if err := align.Save(cfg); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "已删除对齐目标: %s  %s\n", removed.ID, removed.Cron)
			return nil
		},
	}
}

func newAlignClearCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "clear",
		Short: "清空所有对齐目标",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := align.Load()
			if err != nil {
				return err
			}
			cfg.Targets = nil
			if err := align.Save(cfg); err != nil {
				return err
			}
			fmt.Fprintln(cmd.OutOrStdout(), "已清空对齐目标。")
			return nil
		},
	}
}

func newAlignPreviewCmd() *cobra.Command {
	var count int
	cmd := &cobra.Command{
		Use:   "preview",
		Short: "预览未来若干次计划 ping 时间与对应的刷新时间",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if count <= 0 {
				count = plannedPingCount
			}
			cfg, err := align.Load()
			if err != nil {
				return err
			}
			planner, err := align.NewPlanner(cfg)
			if err != nil {
				return err
			}
			lastPing, err := pingstate.LastSuccessfulPing()
			if err != nil {
				return err
			}
			printAlignPreview(cmd.OutOrStdout(), planner, lastPing, count)
			return nil
		},
	}
	cmd.Flags().IntVar(&count, "count", plannedPingCount, "预览的计划 ping 数量")
	return cmd
}

func printAlignPreview(out io.Writer, planner *align.Planner, lastPing *time.Time, count int) {
	now := time.Now()
	if !planner.HasTargets() {
		fmt.Fprintln(out, "未配置对齐目标：满额即 ping，重置时间即 ping 时间 + 5h。")
	}
	if lastPing != nil {
		fmt.Fprintf(out, "最近成功 ping: %s\n", lastPing.Local().Format("2006-01-02 15:04"))
	}
	plans := planner.Upcoming(now, lastPing, count)
	fmt.Fprintln(out, "未来计划：")
	for i, plan := range plans {
		fmt.Fprintf(out, "  %d. ping %s  →  刷新 %s\n",
			i+1,
			plan.PingAt.Local().Format("01-02 15:04"),
			plan.RefreshAt.Local().Format("01-02 15:04"))
	}
}
