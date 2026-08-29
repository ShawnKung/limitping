package pingstate

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestRecordAndRead(t *testing.T) {
	stateHome := t.TempDir()
	t.Setenv("XDG_STATE_HOME", stateHome)
	want := time.Date(2026, 8, 29, 4, 12, 34, 567000000, time.FixedZone("UTC+8", 8*60*60))
	if err := Record(want); err != nil {
		t.Fatal(err)
	}
	got, err := LastSuccessfulPing()
	if err != nil {
		t.Fatal(err)
	}
	if got == nil || !got.Equal(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	info, err := os.Stat(filepath.Join(stateHome, "limitping", filename))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %o", info.Mode().Perm())
	}
}

func TestMissingState(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	got, err := LastSuccessfulPing()
	if err != nil || got != nil {
		t.Fatalf("got %v, err %v", got, err)
	}
}
