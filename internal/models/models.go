package models

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
)

type Model struct {
	Slug       string `json:"slug"`
	Visibility string `json:"visibility"`
	Priority   *int   `json:"priority"`
}

type response struct {
	Models []Model `json:"models"`
}

func Weakest(ctx context.Context) (string, error) {
	out, err := exec.CommandContext(ctx, "codex", "debug", "models").Output()
	if err != nil {
		return "", fmt.Errorf("执行 codex debug models: %w", err)
	}
	return WeakestFromJSON(out)
}

// WeakestFromJSON treats larger priority values as weaker/lower-priority.
// Hidden internal models are deliberately excluded.
func WeakestFromJSON(blob []byte) (string, error) {
	var decoded response
	if err := json.Unmarshal(blob, &decoded); err != nil {
		return "", fmt.Errorf("解析 codex debug models 输出: %w", err)
	}
	var selected Model
	found := false
	for _, model := range decoded.Models {
		if model.Slug == "" || model.Visibility != "list" {
			continue
		}
		if !found || weaker(model, selected) {
			selected, found = model, true
		}
	}
	if !found {
		return "", fmt.Errorf("codex debug models 没有返回可见模型")
	}
	return selected.Slug, nil
}

func weaker(candidate, current Model) bool {
	if candidate.Priority == nil {
		return current.Priority == nil
	}
	if current.Priority == nil {
		return true
	}
	return *candidate.Priority >= *current.Priority
}
