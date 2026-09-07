// Package align 负责把 5h 滚动窗口的刷新时刻，尽量对齐到用户用 cron 表达式声明的
// 目标时间（例如每晚 00:00）。它只做“尽力而为”的相位规划：平时每 5h 正常链式
// ping，临近目标时间时在允许的延时预算内 hold，让某次窗口刚好在目标时间重置。
package align

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/robfig/cron/v3"
)

const configFilename = "config.json"

// DefaultMaxDelayMinutes 是临近目标时间时，单个窗口允许 hold 的默认上限（分钟）。
const DefaultMaxDelayMinutes = 90

// idLength 是对齐目标短 id 的 hex 字符数（docker 风格）。
const idLength = 12

// Target 是一条对齐目标：一个短 id、一个标准 5 字段 cron 表达式，以及该目标临近时
// 单个窗口允许 hold 的延时预算（分钟）。延时预算绑定在每个目标上，互不影响。
type Target struct {
	ID              string `json:"id"`
	Cron            string `json:"cron"`
	MaxDelayMinutes int    `json:"max_delay_minutes"`
}

// Config 是落盘在 ~/.config/limitping/config.json 的对齐配置。
type Config struct {
	// Targets 是一组对齐目标。多条时取最近的一次触发，并用该目标自己的延时预算。
	Targets []Target `json:"targets"`
}

// MaxDelay 返回该目标的延时预算，缺省时使用默认值。
func (t Target) MaxDelay() time.Duration {
	minutes := t.MaxDelayMinutes
	if minutes <= 0 {
		minutes = DefaultMaxDelayMinutes
	}
	return time.Duration(minutes) * time.Minute
}

// ConfigPath 返回配置文件路径，优先使用 XDG_CONFIG_HOME，否则回退到 ~/.config。
func ConfigPath() (string, error) {
	base := strings.TrimSpace(os.Getenv("XDG_CONFIG_HOME"))
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("读取用户目录: %w", err)
		}
		base = filepath.Join(home, ".config")
	}
	return filepath.Join(base, "limitping", configFilename), nil
}

// Load 读取配置。文件不存在时返回空配置，不视为错误。
func Load() (*Config, error) {
	path, err := ConfigPath()
	if err != nil {
		return nil, err
	}
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return &Config{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("读取对齐配置: %w", err)
	}
	var cfg Config
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return nil, fmt.Errorf("解析对齐配置: %w", err)
	}
	return &cfg, nil
}

// Save 原子写入配置，权限 0600，必要时创建目录。
func Save(cfg *Config) error {
	if err := cfg.Validate(); err != nil {
		return err
	}
	path, err := ConfigPath()
	if err != nil {
		return err
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("创建配置目录: %w", err)
	}
	blob, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return fmt.Errorf("序列化对齐配置: %w", err)
	}
	blob = append(blob, '\n')
	temporary, err := os.CreateTemp(dir, ".config-*")
	if err != nil {
		return fmt.Errorf("创建临时配置文件: %w", err)
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0o600); err != nil {
		temporary.Close()
		return fmt.Errorf("设置配置文件权限: %w", err)
	}
	if _, err := temporary.Write(blob); err != nil {
		temporary.Close()
		return fmt.Errorf("写入配置文件: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("关闭配置文件: %w", err)
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return fmt.Errorf("保存配置文件: %w", err)
	}
	return nil
}

// Validate 校验所有 cron 表达式可解析，且延时预算非负。
func (c *Config) Validate() error {
	for _, target := range c.Targets {
		if target.MaxDelayMinutes < 0 {
			return fmt.Errorf("max_delay_minutes 不能为负数")
		}
		if _, err := ParseCron(target.Cron); err != nil {
			return err
		}
	}
	return nil
}

// Add 追加一个 cron 目标并分配唯一短 id，同时记录该目标的延时预算（分钟，<=0 表示用默认值）。
// 若表达式已存在：当 maxDelayMinutes>0 时更新其延时预算并返回（created=false）；否则原样返回。
func (c *Config) Add(expr string, maxDelayMinutes int) (target Target, created bool, err error) {
	trimmed := strings.TrimSpace(expr)
	if _, err := ParseCron(trimmed); err != nil {
		return Target{}, false, err
	}
	if maxDelayMinutes < 0 {
		return Target{}, false, fmt.Errorf("max_delay_minutes 不能为负数")
	}
	for i, existing := range c.Targets {
		if existing.Cron == trimmed {
			if maxDelayMinutes > 0 {
				c.Targets[i].MaxDelayMinutes = maxDelayMinutes
			}
			return c.Targets[i], false, nil
		}
	}
	target = Target{ID: c.newID(), Cron: trimmed, MaxDelayMinutes: maxDelayMinutes}
	c.Targets = append(c.Targets, target)
	return target, true, nil
}

// Delete 按 id 或其唯一前缀删除一个目标。
func (c *Config) Delete(idPrefix string) (Target, error) {
	prefix := strings.TrimSpace(idPrefix)
	if prefix == "" {
		return Target{}, fmt.Errorf("需要提供对齐目标 id")
	}
	index := -1
	for i, target := range c.Targets {
		if target.ID == prefix {
			index = i
			break
		}
	}
	if index == -1 {
		matches := make([]int, 0, 2)
		for i, target := range c.Targets {
			if strings.HasPrefix(target.ID, prefix) {
				matches = append(matches, i)
			}
		}
		switch len(matches) {
		case 0:
			return Target{}, fmt.Errorf("未找到 id 为 %q 的对齐目标", idPrefix)
		case 1:
			index = matches[0]
		default:
			return Target{}, fmt.Errorf("id 前缀 %q 匹配到多个对齐目标，请提供更长的 id", idPrefix)
		}
	}
	removed := c.Targets[index]
	c.Targets = append(c.Targets[:index], c.Targets[index+1:]...)
	return removed, nil
}

// newID 生成一个在当前配置内唯一的 docker 风格短 id。
func (c *Config) newID() string {
	for {
		buf := make([]byte, idLength/2)
		if _, err := rand.Read(buf); err != nil {
			// 退化到时间戳，极少发生。
			return hex.EncodeToString([]byte(fmt.Sprintf("%012x", time.Now().UnixNano())))[:idLength]
		}
		id := hex.EncodeToString(buf)
		unique := true
		for _, target := range c.Targets {
			if target.ID == id {
				unique = false
				break
			}
		}
		if unique {
			return id
		}
	}
}

// compiledTarget 是一条目标解析后的调度对象及其自己的延时预算。
type compiledTarget struct {
	schedule cron.Schedule
	maxDelay time.Duration
}

// Compile 把每个目标解析成调度对象，并附带该目标自己的延时预算。
func (c *Config) Compile() ([]compiledTarget, error) {
	compiled := make([]compiledTarget, 0, len(c.Targets))
	for _, target := range c.Targets {
		schedule, err := ParseCron(target.Cron)
		if err != nil {
			return nil, err
		}
		compiled = append(compiled, compiledTarget{schedule: schedule, maxDelay: target.MaxDelay()})
	}
	return compiled, nil
}

// ParseCron 解析标准 5 字段 cron 表达式（分 时 日 月 周），按本地时区解释。
func ParseCron(expr string) (cron.Schedule, error) {
	trimmed := strings.TrimSpace(expr)
	if trimmed == "" {
		return nil, fmt.Errorf("cron 表达式不能为空")
	}
	schedule, err := cron.ParseStandard(trimmed)
	if err != nil {
		return nil, fmt.Errorf("解析 cron 表达式 %q: %w", expr, err)
	}
	return schedule, nil
}
