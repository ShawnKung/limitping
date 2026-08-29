package pingstate

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const filename = "last-successful-ping"

func Record(at time.Time) error {
	path, err := statePath()
	if err != nil {
		return err
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("创建状态目录: %w", err)
	}
	temporary, err := os.CreateTemp(dir, ".last-successful-ping-*")
	if err != nil {
		return fmt.Errorf("创建临时状态文件: %w", err)
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0o600); err != nil {
		temporary.Close()
		return fmt.Errorf("设置状态文件权限: %w", err)
	}
	if _, err := temporary.WriteString(at.UTC().Format(time.RFC3339Nano) + "\n"); err != nil {
		temporary.Close()
		return fmt.Errorf("写入状态文件: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("关闭状态文件: %w", err)
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return fmt.Errorf("保存状态文件: %w", err)
	}
	return nil
}

func LastSuccessfulPing() (*time.Time, error) {
	path, err := statePath()
	if err != nil {
		return nil, err
	}
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("读取状态文件: %w", err)
	}
	parsed, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(string(raw)))
	if err != nil {
		return nil, fmt.Errorf("解析状态文件: %w", err)
	}
	return &parsed, nil
}

func statePath() (string, error) {
	base := strings.TrimSpace(os.Getenv("XDG_STATE_HOME"))
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("读取用户目录: %w", err)
		}
		base = filepath.Join(home, ".local", "state")
	}
	return filepath.Join(base, "limitping", filename), nil
}
