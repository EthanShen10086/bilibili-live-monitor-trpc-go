package monitor

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"html"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"

	"gopkg.in/yaml.v3"
)

const Label = "com.bilibili.live-monitor"
const Unit = "live-monitor.service"

func Quote(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'" }
func servicePath(side string) string {
	home, _ := os.UserHomeDir()
	if side == "local" {
		return filepath.Join(home, "Library/LaunchAgents", Label+".plist")
	}
	return filepath.Join(home, ".config/systemd/user", Unit)
}
func unitQuote(s string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`, `%`, `%%`).Replace(s) + `"`
}
func ServiceFiles(root, exe string) (string, string) {
	args := []string{exe, "--root", root, "run", "--managed", "local"}
	plist := `<?xml version="1.0" encoding="UTF-8"?><!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd"><plist version="1.0"><dict><key>Label</key><string>` + Label + `</string><key>ProgramArguments</key><array>`
	for _, v := range args {
		plist += `<string>` + html.EscapeString(v) + `</string>`
	}
	plist += `</array><key>WorkingDirectory</key><string>` + html.EscapeString(root) + `</string><key>RunAtLoad</key><true/><key>KeepAlive</key><true/><key>ThrottleInterval</key><integer>20</integer><key>ExitTimeOut</key><integer>80</integer><key>StandardOutPath</key><string>` + html.EscapeString(filepath.Join(root, "var/service.log")) + `</string><key>StandardErrorPath</key><string>` + html.EscapeString(filepath.Join(root, "var/error.log")) + `</string></dict></plist>`
	unit := "[Unit]\nDescription=Bilibili live subscription monitor (Go)\nAfter=network-online.target\nStartLimitIntervalSec=0\n\n[Service]\nType=simple\nWorkingDirectory=" + unitQuote(root) + "\nExecStart=" + unitQuote(exe) + " --root " + unitQuote(root) + " run --managed cloud\nRestart=on-failure\nRestartSec=20\nTimeoutStopSec=80\nUMask=0077\nNoNewPrivileges=true\n\n[Install]\nWantedBy=default.target\n"
	return plist, unit
}
func target() string { return "gui/" + strconv.Itoa(os.Getuid()) + "/" + Label }
func platform(side string) error {
	if (side == "local" && runtime.GOOS == "darwin") || (side == "cloud" && runtime.GOOS == "linux") {
		return nil
	}
	return fmt.Errorf("side %s does not match operating system", side)
}
func Loaded(ctx context.Context, side string) bool {
	if side == "local" {
		_, e := Command(ctx, "launchctl", "print", target())
		return e == nil
	}
	_, e := Command(ctx, "systemctl", "--user", "is-active", "--quiet", Unit)
	return e == nil
}
func Doctor(ctx context.Context, side string) (map[string]any, error) {
	if e := platform(side); e != nil {
		return nil, e
	}
	if _, e := os.Stat(servicePath(side)); e != nil {
		return nil, fmt.Errorf("service not installed")
	}
	r := map[string]any{"installed": true, "platform": runtime.GOOS}
	if side == "cloud" {
		if _, e := Command(ctx, "systemctl", "--user", "show-environment"); e != nil {
			return nil, e
		}
		out, e := Command(ctx, "loginctl", "show-user", strconv.Itoa(os.Getuid()), "--property=Linger", "--value")
		if e != nil || out != "yes" {
			return nil, fmt.Errorf("cloud startup requires loginctl enable-linger for the service account")
		}
		r["linger"] = true
		r["user_manager"] = true
	}
	return r, nil
}
func Health(ctx context.Context, root, side string) error {
	if !Loaded(ctx, side) {
		return fmt.Errorf("service manager reports inactive")
	}
	s, e := ReadStatus(root)
	host, _ := os.Hostname()
	if e != nil || s.Host != host || !s.BusinessHealthy(time.Now()) {
		return fmt.Errorf("managed worker is not healthy")
	}
	release, e := Lock(root, "instance")
	if e == nil {
		release()
		return fmt.Errorf("status stale: no instance lock")
	}
	if !strings.Contains(e.Error(), "ELOCKED") {
		return e
	}
	return nil
}
func AssertStopped(root string) error {
	s, e := ReadStatus(root)
	if e != nil && !os.IsNotExist(e) {
		return fmt.Errorf("cannot read previous status; refusing state transfer")
	}
	host, _ := os.Hostname()
	if e == nil && s.Running && s.Host == host && s.PID > 1 {
		p, err := os.FindProcess(s.PID)
		if err == nil {
			err = p.Signal(syscall.Signal(0))
			if err == nil || (!errors.Is(err, syscall.ESRCH) && !errors.Is(err, os.ErrProcessDone)) {
				return fmt.Errorf("previous process has not been proven stopped")
			}
		}
	}
	release, e := Lock(root, "instance")
	if e != nil {
		return e
	}
	release()
	return nil
}
func Service(ctx context.Context, root, side, action string) error {
	if e := platform(side); e != nil {
		return e
	}
	switch action {
	case "install":
		if Loaded(ctx, side) {
			return fmt.Errorf("stop the existing managed service before replacing its entrypoint")
		}
		if e := AssertStopped(root); e != nil {
			return e
		}
		exe, e := os.Executable()
		if e != nil {
			return e
		}
		p, u := ServiceFiles(root, exe)
		body := p
		if side == "cloud" {
			body = u
		}
		if e = Atomic(servicePath(side), []byte(body)); e != nil {
			return e
		}
		if side == "cloud" {
			_, e = Command(ctx, "systemctl", "--user", "daemon-reload")
		}
		return e
	case "start":
		if _, e := Doctor(ctx, side); e != nil {
			return e
		}
		if side == "local" {
			if _, e := Command(ctx, "launchctl", "enable", target()); e != nil {
				return e
			}
			if Loaded(ctx, side) {
				_, e := Command(ctx, "launchctl", "kickstart", target())
				return e
			}
			_, e := Command(ctx, "launchctl", "bootstrap", "gui/"+strconv.Itoa(os.Getuid()), servicePath(side))
			return e
		}
		_, e := Command(ctx, "systemctl", "--user", "enable", "--now", Unit)
		return e
	case "stop":
		if side == "local" {
			if _, e := Command(ctx, "launchctl", "disable", target()); e != nil {
				return e
			}
			if Loaded(ctx, side) {
				if _, e := Command(ctx, "launchctl", "bootout", target()); e != nil {
					return e
				}
			}
		} else {
			if _, e := Command(ctx, "systemctl", "--user", "disable", "--now", Unit); e != nil {
				return e
			}
		}
		for i := 0; i < 50; i++ {
			if e := AssertStopped(root); e == nil {
				return nil
			}
			if e := Pause(ctx, 500*time.Millisecond); e != nil {
				return e
			}
		}
		return fmt.Errorf("cannot confirm worker stopped")
	case "assert-stopped":
		if Loaded(ctx, side) {
			return fmt.Errorf("service still active")
		}
		return AssertStopped(root)
	case "health":
		return Health(ctx, root, side)
	}
	return fmt.Errorf("unknown service action")
}
func Recovery(ctx context.Context, root, side string) (map[string]any, error) {
	release, e := Lock(root, "management")
	if e != nil {
		return nil, e
	}
	defer release()
	if e = Health(ctx, root, side); e != nil {
		return nil, e
	}
	old, _ := ReadStatus(root)
	managerPID := func() (int, error) {
		if side == "cloud" {
			out, e := Command(ctx, "systemctl", "--user", "show", Unit, "--property=MainPID", "--value")
			n, _ := strconv.Atoi(out)
			return n, e
		}
		out, e := Command(ctx, "launchctl", "print", target())
		for _, l := range strings.Split(out, "\n") {
			l = strings.TrimSpace(l)
			if strings.HasPrefix(l, "pid = ") {
				n, _ := strconv.Atoi(strings.TrimPrefix(l, "pid = "))
				return n, e
			}
		}
		return 0, fmt.Errorf("manager PID unavailable")
	}
	n, e := managerPID()
	if e != nil || n != old.PID || n <= 1 {
		return nil, fmt.Errorf("refusing recovery test: PID mismatch")
	}
	p, e := os.FindProcess(n)
	if e != nil {
		return nil, e
	}
	if e = p.Signal(syscall.SIGKILL); e != nil {
		return nil, fmt.Errorf("cannot signal managed worker; recovery remains unverified")
	}
	for i := 0; i < 60; i++ {
		if e = Pause(ctx, time.Second); e != nil {
			return nil, e
		}
		if Health(ctx, root, side) == nil {
			s, _ := ReadStatus(root)
			n, e := managerPID()
			if e == nil && n == s.PID && n != old.PID {
				return map[string]any{"recovered": true, "before_pid": old.PID, "after_pid": n}, nil
			}
		}
	}
	return nil, fmt.Errorf("no new healthy managed worker within 60 seconds")
}
func SetActive(root, side string) error {
	if side != "local" && side != "cloud" {
		return fmt.Errorf("invalid active side")
	}
	c, e := Load(root)
	if e != nil {
		return e
	}
	c.Deployment.Active = side
	b, e := yaml.Marshal(c)
	if e != nil {
		return e
	}
	return Atomic(filepath.Join(root, "config.yaml"), b)
}
func Remote(ctx context.Context, c Config, input []byte, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	shell := "cd " + Quote(c.Deployment.Cloud.Dir) + " && " + Quote(filepath.Join(c.Deployment.Cloud.Dir, "bin/monitor"))
	for _, a := range args {
		shell += " " + Quote(a)
	}
	cmd := exec.CommandContext(ctx, "ssh", "-o", "BatchMode=yes", "-o", "ConnectTimeout=10", c.Deployment.Cloud.Host, shell)
	cmd.Stdin = strings.NewReader(string(input))
	out, e := cmd.Output()
	if e != nil {
		return "", fmt.Errorf("remote command failed; inspect target config/SSH/service logs")
	}
	return strings.TrimSpace(string(out)), nil
}
func Export(root string) (string, error) {
	if e := AssertStopped(root); e != nil {
		return "", e
	}
	release, e := Lock(root, "instance")
	if e != nil {
		return "", e
	}
	defer release()
	db, e := OpenStore(filepath.Join(root, "var/state.sqlite"))
	if e != nil {
		return "", e
	}
	db.DB.Close()
	b, e := os.ReadFile(filepath.Join(root, "var/state.sqlite"))
	return base64.StdEncoding.EncodeToString(b), e
}
func Import(root, b64 string) error {
	if e := AssertStopped(root); e != nil {
		return e
	}
	release, e := Lock(root, "instance")
	if e != nil {
		return e
	}
	defer release()
	b, e := base64.StdEncoding.DecodeString(strings.TrimSpace(b64))
	if e != nil || !bytesSQLite(b) {
		return fmt.Errorf("invalid SQLite snapshot")
	}
	p := filepath.Join(root, "var/state.sqlite")
	if prev, e := os.ReadFile(p); e == nil {
		if e = Atomic(p+".previous", prev); e != nil {
			return e
		}
	}
	return Atomic(p, b)
}
func bytesSQLite(b []byte) bool { return len(b) >= 16 && string(b[:16]) == "SQLite format 3\x00" }

type SwitchOps struct {
	Preflight func(string) error
	Stop      func(string) error
	Assert    func(string) error
	Transfer  func(string, string) error
	Active    func(string) error
	Start     func(string) error
	Health    func(string) error
}

func Switch(from, to string, o SwitchOps) error {
	if from == to {
		return o.Health(to)
	}
	if e := o.Preflight(to); e != nil {
		return e
	}
	if e := o.Stop(to); e != nil {
		return e
	}
	if e := o.Assert(to); e != nil {
		return e
	}
	if e := o.Stop(from); e != nil {
		return e
	}
	if e := o.Assert(from); e != nil {
		return e
	}
	started := false
	apply := func() error {
		if e := o.Transfer(from, to); e != nil {
			return e
		}
		if e := o.Active(to); e != nil {
			return e
		}
		started = true
		if e := o.Start(to); e != nil {
			return e
		}
		return o.Health(to)
	}
	if e := apply(); e != nil {
		if o.Stop(to) != nil || o.Assert(to) != nil {
			return fmt.Errorf("switch failed: target stop unconfirmed; rollback refused")
		}
		if started {
			if o.Transfer(to, from) != nil {
				return fmt.Errorf("switch failed: return-state transfer failed; rollback refused")
			}
		}
		if o.Active(from) != nil || o.Start(from) != nil || o.Health(from) != nil {
			return fmt.Errorf("switch failed: source recovery failed")
		}
		return fmt.Errorf("switch failed: source restored")
	}
	return nil
}
func DeploySwitch(ctx context.Context, root string, c Config, to string) error {
	if c.Platform.StorageMode() != "sqlite" {
		return fmt.Errorf("shared PostgreSQL mode uses independent deployment; SQLite file switching is disabled")
	}
	if to != "local" && to != "cloud" {
		return fmt.Errorf("invalid destination")
	}
	release, e := Lock(root, "management")
	if e != nil {
		return e
	}
	defer release()
	cfg, e := os.ReadFile(filepath.Join(root, "config.yaml"))
	if e != nil {
		return e
	}
	if _, e = Remote(ctx, c, cfg, "check-import-config"); e != nil {
		return e
	}
	op := func(side, action string) error {
		if side == "cloud" {
			_, e := Remote(ctx, c, nil, "service", action, "--side", "cloud")
			return e
		}
		return Service(ctx, root, "local", action)
	}
	o := SwitchOps{Preflight: func(side string) error {
		if side == "cloud" {
			if _, e := Remote(ctx, c, nil, "service", "doctor", "--side", "cloud"); e != nil {
				return e
			}
			_, e := Remote(ctx, c, cfg, "check-import-config", "--probe")
			return e
		}
		if c.Deployment.Local.Confirm {
			boot, e := BootID()
			if e != nil {
				return e
			}
			a := ReadApproval(root)
			if a.Boot != boot || a.Decision != "approved" {
				return fmt.Errorf("confirm this Mac boot before switching local")
			}
		}
		if e := c.Credentials(); e != nil {
			return e
		}
		_, e := NewHTTP().Probe(ctx, c)
		return e
	}, Stop: func(s string) error { return op(s, "stop") }, Assert: func(s string) error { return op(s, "assert-stopped") }, Transfer: func(from, to string) error {
		var snapshot string
		var e error
		if from == "local" {
			snapshot, e = Export(root)
		} else {
			snapshot, e = Remote(ctx, c, nil, "state-export")
		}
		if e != nil {
			return e
		}
		if to == "local" {
			return Import(root, snapshot)
		}
		if _, e = Remote(ctx, c, []byte(snapshot), "state-import"); e != nil {
			return e
		}
		_, e = Remote(ctx, c, cfg, "config-import")
		return e
	}, Active: func(s string) error {
		if e := SetActive(root, s); e != nil {
			return e
		}
		_, e := Remote(ctx, c, nil, "set-active", s)
		return e
	}, Start: func(s string) error { return op(s, "start") }, Health: func(s string) error {
		for i := 0; i < 30; i++ {
			if e := op(s, "health"); e == nil {
				return nil
			}
			if e := Pause(ctx, time.Second); e != nil {
				return e
			}
		}
		return fmt.Errorf("target health failed")
	}}
	if e = Switch(c.Deployment.Active, to, o); e != nil {
		return e
	}
	return AtomicJSON(filepath.Join(root, "var/deployment.json"), map[string]any{"active": to, "switched_at": time.Now().UnixMilli()})
}
