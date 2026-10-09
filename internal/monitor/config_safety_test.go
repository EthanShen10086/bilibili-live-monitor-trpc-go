package monitor

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestInvalidScheduleAndApprovalFailClosed(t *testing.T) {
	c := testConfig(t)
	c.Schedule.Timezone = "unknown/timezone"
	if c.InWindow(time.Now()) {
		t.Fatal("invalid timezone enabled monitoring")
	}
	c.Schedule.Timezone = "Asia/Shanghai"
	c.Schedule.Start = "invalid"
	if c.InWindow(time.Now()) {
		t.Fatal("invalid window enabled monitoring")
	}
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "var"), 0o700); err != nil {
		t.Fatal(err)
	}
	// A truncated JSON document must not retain partially decoded approval.
	if err := os.WriteFile(filepath.Join(root, "var/boot-approval.json"), []byte(`{"boot":"current","decision":"approved"`), 0o600); err != nil {
		t.Fatal(err)
	}
	if a := ReadApproval(root); a.Decision == "approved" {
		t.Fatal("corrupt approval enabled monitoring")
	}
}
