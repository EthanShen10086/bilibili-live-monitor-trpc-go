package monitor

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestLogLimitsAndArchives(t *testing.T) {
	file := filepath.Join(t.TempDir(), "events.log")
	if e := os.WriteFile(file, make([]byte, 3*1024*1024), 0o600); e != nil {
		t.Fatal(e)
	}
	for i := 0; i < 140; i++ {
		if e := AppendBoundedLog(file, bytes.Repeat([]byte{'a'}, 64*1024)); e != nil {
			t.Fatal(e)
		}
	}
	if e := AppendBoundedLog(file, []byte("TAIL_MARKER")); e != nil {
		t.Fatal(e)
	}
	tail, e := os.ReadFile(file)
	if e != nil || !bytes.HasSuffix(tail, []byte("TAIL_MARKER")) {
		t.Fatal(e)
	}
	if _, e = os.Stat(file + ".3"); e != nil {
		t.Fatal(e)
	}
	if _, e = os.Stat(file + ".4"); !os.IsNotExist(e) {
		t.Fatal("too many archives")
	}
	entries, _ := os.ReadDir(filepath.Dir(file))
	for _, entry := range entries {
		s, e := entry.Info()
		if e != nil || s.Size() > 2*1024*1024 {
			t.Fatal(s, e)
		}
	}
}

func TestCaptureManagedDescriptors(t *testing.T) {
	if root := os.Getenv("MONITOR_LOG_TEST_ROOT"); root != "" {
		cached := os.Stdout
		restore, e := CaptureManagedLogs(root)
		if e != nil {
			t.Fatal(e)
		}
		fmt.Fprintln(cached, "CACHED_STDOUT")
		fmt.Fprintln(os.Stderr, "CAPTURED_STDERR")
		restore()
		fmt.Println("RESTORED_OUTPUT")
		return
	}
	root := t.TempDir()
	cmd := exec.Command(os.Args[0], "-test.run=^TestCaptureManagedDescriptors$")
	cmd.Env = append(os.Environ(), "MONITOR_LOG_TEST_ROOT="+root)
	out, e := cmd.CombinedOutput()
	if e != nil || !strings.Contains(string(out), "RESTORED_OUTPUT") || strings.Contains(string(out), "CACHED_STDOUT") {
		t.Fatal(string(out), e)
	}
	service, e := os.ReadFile(filepath.Join(root, "var/service.log"))
	if e != nil || !bytes.Contains(service, []byte("CACHED_STDOUT")) {
		t.Fatal(string(service), e)
	}
	errors, e := os.ReadFile(filepath.Join(root, "var/error.log"))
	if e != nil || !bytes.Contains(errors, []byte("CAPTURED_STDERR")) {
		t.Fatal(string(errors), e)
	}
}
