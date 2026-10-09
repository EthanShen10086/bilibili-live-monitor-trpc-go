package monitor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"
)

func AtomicJSON(file string, v any) error {
	b, e := json.MarshalIndent(v, "", "  ")
	if e != nil {
		return e
	}
	return Atomic(file, append(b, '\n'))
}
func Atomic(file string, b []byte) error {
	if e := os.MkdirAll(filepath.Dir(file), 0700); e != nil {
		return e
	}
	tmp := file + "." + ID() + ".tmp"
	if e := os.WriteFile(tmp, b, 0600); e != nil {
		return e
	}
	if e := os.Rename(tmp, file); e != nil {
		os.Remove(tmp)
		return e
	}
	return nil
}
func Event(root, name string, data any) {
	slog.Info(name, "details", data)
	file := filepath.Join(root, "var/events.log")
	b, _ := json.Marshal(map[string]any{"time": time.Now().UTC().Format(time.RFC3339Nano), "event": name, "details": data})
	if err := AppendBoundedLog(file, append(b, '\n')); err != nil {
		slog.Error("event_log_failed", "error", err.Error())
	}
}

// Same directory-lock path/heartbeat timing as Node proper-lockfile.
func Lock(root, name string) (func(), error) {
	dir := filepath.Join(root, "var")
	if e := os.MkdirAll(dir, 0700); e != nil {
		return nil, e
	}
	guard := filepath.Join(dir, name+".guard")
	f, e := os.OpenFile(guard, os.O_CREATE|os.O_WRONLY, 0600)
	if e != nil {
		return nil, e
	}
	f.Close()
	p := guard + ".lock"
	if e = os.Mkdir(p, 0700); e != nil {
		stat, se := os.Stat(p)
		if se != nil || time.Since(stat.ModTime()) < 15*time.Second {
			return nil, fmt.Errorf("ELOCKED: %s", name)
		}
		if e = os.Remove(p); e != nil {
			return nil, fmt.Errorf("ELOCKED: %s", name)
		}
		if e = os.Mkdir(p, 0700); e != nil {
			return nil, fmt.Errorf("ELOCKED: %s", name)
		}
	}
	original, e := os.Stat(p)
	if e != nil {
		return nil, e
	}
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		tick := time.NewTicker(2 * time.Second)
		defer tick.Stop()
		for {
			select {
			case <-stop:
				return
			case <-tick.C:
				now, e := os.Stat(p)
				if e != nil || !os.SameFile(original, now) {
					os.Exit(1)
				}
				if os.Chtimes(p, time.Now(), time.Now()) != nil {
					os.Exit(1)
				}
			}
		}
	}()
	return func() {
		close(stop)
		<-done
		if f, e := os.Stat(p); e == nil && os.SameFile(original, f) {
			os.Remove(p)
		}
	}, nil
}
func Command(ctx context.Context, name string, args ...string) (string, error) {
	out, e := exec.CommandContext(ctx, name, args...).Output()
	if e != nil {
		return "", fmt.Errorf("command failed: %s %s", filepath.Base(name), strings.Join(args[:min(1, len(args))], " "))
	}
	return strings.TrimSpace(string(out)), nil
}

type Approval struct {
	Boot     string `json:"boot_id"`
	Decision string `json:"decision"`
	At       int64  `json:"confirmed_at"`
}

func BootID() (string, error) {
	if runtime.GOOS != "darwin" {
		return "", errors.New("boot confirmation requires macOS")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out, e := Command(ctx, "/usr/sbin/sysctl", "-n", "kern.boottime")
	if e != nil {
		return "", errors.New("cannot read kernel boot identity; confirmation remains closed")
	}
	p := regexp.MustCompile(`sec\s*=\s*(\d+),\s*usec\s*=\s*(\d+)`).FindStringSubmatch(out)
	if len(p) != 3 {
		return "", errors.New("invalid boot identity")
	}
	return p[1] + ":" + p[2], nil
}
func ReadApproval(root string) Approval {
	var a Approval
	b, _ := os.ReadFile(filepath.Join(root, "var/boot-approval.json"))
	json.Unmarshal(b, &a)
	return a
}
func Confirm(root string) error {
	id, e := BootID()
	if e != nil {
		return e
	}
	return AtomicJSON(filepath.Join(root, "var/boot-approval.json"), Approval{id, "approved", time.Now().UnixMilli()})
}
func AwaitApproval(ctx context.Context, root string, c Config) error {
	boot, e := BootID()
	if e != nil {
		return e
	}
	return awaitApproval(ctx, root, c, boot)
}
func awaitApproval(ctx context.Context, root string, c Config, boot string) error {
	release, e := Lock(root, "boot-confirmation")
	if e != nil {
		return e
	}
	defer release()
	writer := StatusWriter{File: filepath.Join(root, "var/status.json")}
	host, _ := os.Hostname()
	status := Status{PID: os.Getpid(), Host: host, Running: true, State: "waiting_confirmation", Mode: c.Detector.Mode, HeartbeatSeconds: 10}
	report := func() error { return writer.Report(&status, time.Now(), false) }
	defer func() {
		if writer.Writes > 0 && ctx.Err() != nil {
			status.Running = false
			status.State = "stopped"
			writer.Report(&status, time.Now(), true)
		}
	}()
	a := ReadApproval(root)
	if a.Boot == boot && a.Decision == "approved" {
		return nil
	}
	if a.Boot != boot {
		if e = report(); e != nil {
			return e
		}
		dialog, cancel := context.WithTimeout(ctx, 120*time.Second)
		type result struct {
			out string
			err error
		}
		results := make(chan result, 1)
		go func() {
			out, err := Command(dialog, "/usr/bin/osascript", "-e", `return button returned of (display dialog "是否启用本次电脑启动期间的 B 站开播订阅？确认后登录自启动、崩溃自恢复；下次电脑重启重新确认。" with title "B 站开播订阅" buttons {"暂不启用", "启用"} default button "暂不启用")`)
			results <- result{out, err}
		}()
		tick := time.NewTicker(WorkerHeartbeat)
		var out string
	prompting:
		for {
			select {
			case r := <-results:
				out, e = r.out, r.err
				break prompting
			case <-ctx.Done():
				cancel()
				tick.Stop()
				return ctx.Err()
			case <-tick.C:
				if re := report(); re != nil {
					cancel()
					tick.Stop()
					return re
				}
			}
		}
		tick.Stop()
		cancel()
		decision := "declined"
		if e == nil && out == "启用" {
			decision = "approved"
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if e = AtomicJSON(filepath.Join(root, "var/boot-approval.json"), Approval{boot, decision, time.Now().UnixMilli()}); e != nil {
			return e
		}
		Event(root, "boot_"+decision, nil)
	}
	for {
		a = ReadApproval(root)
		if a.Boot == boot && a.Decision == "approved" {
			return nil
		}
		if e = report(); e != nil {
			return e
		}
		if e = Pause(ctx, WorkerHeartbeat); e != nil {
			return e
		}
	}
}
func Pause(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}
