package monitor

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

type Options struct {
	Root, Side, Managed string
	Probe, OfficialAuth bool
	Args                []string
}

func ParseOptions(args []string) (Options, error) {
	root, _ := os.Getwd()
	o := Options{Root: root}
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--root", "--side", "--managed":
			if i+1 >= len(args) {
				return o, fmt.Errorf("missing option value")
			}
			k, v := args[i], args[i+1]
			i++
			switch k {
			case "--root":
				o.Root = v
			case "--side":
				o.Side = v
			case "--managed":
				o.Managed = v
			}
		case "--probe":
			o.Probe = true
		case "--official-auth":
			o.OfficialAuth = true
		default:
			o.Args = append(o.Args, args[i])
		}
	}
	o.Root, _ = filepath.Abs(o.Root)
	return o, nil
}
func Print(v any) error {
	b, e := json.MarshalIndent(v, "", "  ")
	if e != nil {
		return e
	}
	fmt.Println(string(b))
	return nil
}
func PrepareRun(ctx context.Context, o Options) (Config, error) {
	c, e := Load(o.Root)
	if e != nil {
		return c, e
	}
	side := o.Managed
	if side == "" {
		side = "cloud"
		if runtime.GOOS == "darwin" {
			side = "local"
		}
	}
	if side != c.Deployment.Active {
		return c, fmt.Errorf("worker disabled by deployment.active")
	}
	if e = c.Credentials(); e != nil {
		return c, e
	}
	if o.Managed == "local" && c.Deployment.Local.Confirm {
		e = AwaitApproval(ctx, o.Root, c)
	}
	return c, e
}
func CLI(ctx context.Context, o Options) error {
	cmd := "help"
	if len(o.Args) > 0 {
		cmd = o.Args[0]
	}
	if cmd == "help" {
		fmt.Println("monitor check-config [--probe] [--official-auth]\nmonitor platform-import-sqlite SOURCE_ROOT (stopped source, empty target)\nmonitor platform-check (connect selected backends; no messages)\nmonitor healthcheck live|ready|business\nmonitor platform-migrate\nmonitor backup /absolute/new-file.sqlite\nmonitor restore /absolute/backup.sqlite (empty stopped target)\nmonitor probe\nmonitor run\nmonitor status\nmonitor test-notification\nmonitor retry-failed\nmonitor confirm-start\nmonitor service install|start|stop|health|doctor|verify-recovery --side local|cloud\nmonitor switch local|cloud\nmonitor set-active local|cloud (initial setup only)")
		return nil
	}
	c, e := Load(o.Root)
	if e != nil {
		return e
	}
	if e = os.MkdirAll(filepath.Join(o.Root, "var"), 0700); e != nil {
		return e
	}
	h := NewHTTP()
	arg := func() string {
		if len(o.Args) > 1 {
			return o.Args[1]
		}
		return ""
	}
	switch cmd {
	case "healthcheck":
		s, err := ReadStatus(o.Root)
		if err != nil {
			return fmt.Errorf("status unavailable")
		}
		valid := false
		switch arg() {
		case "live":
			valid = s.Live(time.Now())
		case "ready":
			valid = s.Ready(time.Now())
		case "business":
			valid = s.BusinessHealthy(time.Now())
		default:
			return fmt.Errorf("healthcheck live|ready|business")
		}
		if !valid {
			return fmt.Errorf("worker %s unhealthy", arg())
		}
		return nil
	case "platform-migrate":
		if c.Platform.StorageMode() != "postgres" {
			return fmt.Errorf("platform-migrate requires PostgreSQL")
		}
		if os.Getenv(c.Platform.Postgres.DSNEnv) == "" {
			return fmt.Errorf("PostgreSQL DSN missing")
		}
		apply := true
		c.Platform.Postgres.AutoMigrate = &apply
		db, err := OpenPostgres(ctx, c)
		if err != nil {
			return err
		}
		defer db.Close()
		return Print(map[string]any{"schema_version": 1, "notification_sent": false})
	case "backup":
		if c.Platform.StorageMode() != "sqlite" {
			return fmt.Errorf("use pg_dump for PostgreSQL backups")
		}
		if arg() == "" {
			return fmt.Errorf("backup /absolute/new-file.sqlite")
		}
		if err := BackupSQLite(ctx, o.Root, arg()); err != nil {
			return err
		}
		return Print(map[string]any{"backup": arg(), "notification_sent": false})
	case "restore":
		if c.Platform.StorageMode() != "sqlite" {
			return fmt.Errorf("use pg_restore for PostgreSQL")
		}
		if arg() == "" {
			return fmt.Errorf("restore /absolute/backup.sqlite (empty stopped target)")
		}
		return RestoreSQLite(ctx, o.Root, arg())
	case "platform-import-sqlite":
		if c.Platform.StorageMode() != "postgres" || len(o.Args) != 2 {
			return fmt.Errorf("usage: platform-import-sqlite /absolute/stopped/source-root (PostgreSQL mode)")
		}
		if e = AssertStopped(o.Root); e != nil {
			return e
		}
		if e = c.Credentials(); e != nil {
			return e
		}
		db, err := OpenPostgres(ctx, c)
		if err != nil {
			return err
		}
		defer db.Close()
		n, err := db.ImportSQLite(ctx, o.Args[1], c)
		if err != nil {
			return err
		}
		return Print(map[string]any{"imported_jobs": n, "notification_sent": false})
	case "platform-check":
		if e = c.Credentials(); e != nil {
			return e
		}
		db, err := OpenRepository(ctx, o.Root, c)
		if err != nil {
			return err
		}
		defer db.Close()
		if c.Platform.CacheMode() == "redis" {
			cache, err := OpenCache(c)
			if err != nil {
				return err
			}
			cache.Close()
		}
		if c.Platform.QueueMode() == "redis_streams" {
			q, err := OpenStreamQueue(c, db.(*PostgresStore))
			if err != nil {
				return err
			}
			q.Close()
		}
		return Print(map[string]any{"ready": true, "storage": c.Platform.StorageMode(), "queue": c.Platform.QueueMode(), "cache": c.Platform.CacheMode(), "role": c.Platform.WorkerRole(), "notification_sent": false})
	case "run":
		c, e = PrepareRun(ctx, o)
		if e != nil {
			return e
		}
		return Run(ctx, o.Root, c, h)
	case "probe":
		r, e := h.Probe(ctx, c)
		if e != nil {
			return e
		}
		return Print(r)
	case "check-config":
		if e = c.Credentials(); e != nil {
			return e
		}
		if o.Probe {
			r, e := h.Probe(ctx, c)
			if e != nil {
				return e
			}
			if o.OfficialAuth && c.Detector.Mode == "official" {
				if e = AssertStopped(o.Root); e != nil {
					return e
				}
				of := NewOfficial(c, h, r.RoomID)
				e = of.Start(ctx)
				cleanup := of.Stop(ctx)
				if e != nil {
					return e
				}
				if cleanup != nil {
					return cleanup
				}
			}
		}
		return Print(map[string]any{"valid": true, "detector": c.Detector.Mode, "notification": c.Notification.Mode, "network_probe": o.Probe})
	case "test-notification":
		if e = c.Credentials(); e != nil {
			return e
		}
		f := Feishu{Config: c, HTTP: h}
		if e = f.Send(ctx, "[B站订阅] tRPC-Go 真实通知测试。https://live.bilibili.com/"+fmtRoom(c.Subscription.RoomID), "test:"+ID()); e != nil {
			return e
		}
		fmt.Println("Feishu accepted notification; verify chat visibility and phone separately")
		return nil
	case "confirm-start":
		if e = c.Credentials(); e != nil {
			return e
		}
		return Confirm(o.Root)
	case "status":
		s, e := ReadStatus(o.Root)
		var state any
		if e == nil {
			state = s
		}
		result := map[string]any{"configured_active": c.Deployment.Active, "local_status": state, "status_fresh": e == nil && s.Running && time.Now().UnixMilli()-s.Updated < 20000}
		localOnly := false
		for _, a := range o.Args {
			if a == "--local-only" {
				localOnly = true
			}
		}
		if c.Deployment.Active == "cloud" && !localOnly {
			out, e := Remote(ctx, c, nil, "status", "--local-only")
			if e != nil {
				result["cloud_status"] = map[string]string{"error": "cloud status unavailable"}
			} else {
				var remote any
				json.Unmarshal([]byte(out), &remote)
				result["cloud_status"] = remote
			}
		}
		return Print(result)
	case "retry-failed":
		if e = c.Credentials(); e != nil {
			return e
		}
		release, e := Lock(o.Root, "management")
		if e != nil {
			return e
		}
		defer release()
		db, e := OpenRepository(ctx, o.Root, c)
		if e != nil {
			return e
		}
		defer db.Close()
		n, e := db.Retry(time.Now())
		if e != nil {
			return e
		}
		return Print(map[string]int64{"requeued": n})
	case "service":
		if o.Side != "local" && o.Side != "cloud" {
			return fmt.Errorf("expected --side local|cloud")
		}
		if arg() == "doctor" {
			if e = c.Credentials(); e != nil {
				return e
			}
			r, e := Doctor(ctx, o.Side)
			if e != nil {
				return e
			}
			return Print(r)
		}
		if arg() == "verify-recovery" {
			if c.Deployment.Active != o.Side {
				return fmt.Errorf("active side differs")
			}
			r, e := Recovery(ctx, o.Root, o.Side)
			if e != nil {
				return e
			}
			return Print(r)
		}
		if arg() == "install" || arg() == "start" || arg() == "stop" {
			release, err := Lock(o.Root, "management")
			if err != nil {
				return err
			}
			defer release()
		}
		if arg() == "start" {
			if c.Deployment.Active != o.Side {
				return fmt.Errorf("active side differs")
			}
			if e = c.Credentials(); e != nil {
				return e
			}
		}
		if e = Service(ctx, o.Root, o.Side, arg()); e != nil {
			return e
		}
		fmt.Println("service", arg(), "completed")
		return nil
	case "switch":
		return DeploySwitch(ctx, o.Root, c, arg())
	case "set-active":
		if e = AssertStopped(o.Root); e != nil {
			return e
		}
		release, e := Lock(o.Root, "instance")
		if e != nil {
			return e
		}
		defer release()
		return SetActive(o.Root, arg())
	case "state-export":
		out, e := Export(o.Root)
		if e != nil {
			return e
		}
		fmt.Println(out)
		return nil
	case "state-import":
		b, e := io.ReadAll(io.LimitReader(os.Stdin, 4*1024*1024+1))
		if e != nil || len(b) > 4*1024*1024 {
			return fmt.Errorf("state input too large")
		}
		return Import(o.Root, string(b))
	case "check-import-config", "config-import":
		b, e := io.ReadAll(io.LimitReader(os.Stdin, 1024*1024))
		if e != nil {
			return e
		}
		var incoming Config
		if yaml.Unmarshal(b, &incoming) != nil {
			return fmt.Errorf("invalid incoming YAML")
		}
		if e = incoming.Validate(); e != nil {
			return e
		}
		if e = incoming.Credentials(); e != nil {
			return e
		}
		if cmd == "check-import-config" {
			if o.Probe {
				if _, e = h.Probe(ctx, incoming); e != nil {
					return e
				}
			}
			fmt.Println("incoming config valid")
			return nil
		}
		if e = AssertStopped(o.Root); e != nil {
			return e
		}
		release, e := Lock(o.Root, "instance")
		if e != nil {
			return e
		}
		defer release()
		return Atomic(filepath.Join(o.Root, "config.yaml"), b)
	}
	return fmt.Errorf("unknown command: %s", strings.TrimSpace(cmd))
}
