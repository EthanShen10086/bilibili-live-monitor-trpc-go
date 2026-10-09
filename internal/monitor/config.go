package monitor

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	_ "time/tzdata"

	"gopkg.in/yaml.v3"
)

type Config struct {
	Observability struct {
		Tracing     bool     `yaml:"tracing"`
		SampleRatio *float64 `yaml:"sample_ratio,omitempty"`
		ServiceName string   `yaml:"service_name,omitempty"`
	} `yaml:"observability"`
	Platform    PlatformConfig `yaml:"platform"`
	Maintenance struct {
		RetentionDays *int `yaml:"history_retention_days,omitempty"`
	} `yaml:"maintenance"`
	Subscription struct {
		RoomID int64 `yaml:"room_id"`
	} `yaml:"subscription"`
	Detector struct {
		Mode    string `yaml:"mode"`
		Polling struct {
			Interval            *int `yaml:"interval_seconds,omitempty"`
			IntervalMinutes     *int `yaml:"interval_minutes,omitempty"`
			NotifiedLiveMinutes *int `yaml:"notified_live_interval_minutes,omitempty"`
			Timeout             int  `yaml:"timeout_seconds"`
		} `yaml:"polling"`
		Official struct {
			AppID     string `yaml:"app_id_env"`
			KeyID     string `yaml:"access_key_id_env"`
			KeySecret string `yaml:"access_key_secret_env"`
			Anchor    string `yaml:"anchor_code_env"`
		} `yaml:"official"`
	} `yaml:"detector"`
	Schedule struct {
		Timezone string `yaml:"timezone"`
		Weekdays []int  `yaml:"weekdays"`
		Start    string `yaml:"start"`
		End      string `yaml:"end"`
	} `yaml:"schedule"`
	Notification struct {
		Mode  string `yaml:"mode"`
		TTL   int    `yaml:"pending_ttl_minutes"`
		Group struct {
			Webhook string `yaml:"webhook_env"`
			Secret  string `yaml:"secret_env"`
		} `yaml:"group"`
		Private struct {
			AppID  string `yaml:"app_id_env"`
			Secret string `yaml:"app_secret_env"`
			IDType string `yaml:"receive_id_type"`
			ID     string `yaml:"receive_id_env"`
		} `yaml:"private"`
	} `yaml:"notification"`
	Deployment struct {
		Active string `yaml:"active"`
		Local  struct {
			Confirm bool `yaml:"confirm_each_boot"`
		} `yaml:"local"`
		Cloud struct {
			Host string `yaml:"ssh_host"`
			Dir  string `yaml:"install_dir"`
		} `yaml:"cloud"`
	} `yaml:"deployment"`
}

func Load(root string) (Config, error) {
	var c Config
	b, e := os.ReadFile(filepath.Join(root, "config.yaml"))
	if e != nil {
		return c, e
	}
	decoder := yaml.NewDecoder(bytes.NewReader(b))
	decoder.KnownFields(true)
	if e = decoder.Decode(&c); e != nil {
		return c, fmt.Errorf("invalid YAML")
	}
	var extra any
	if !errors.Is(decoder.Decode(&extra), io.EOF) {
		return c, fmt.Errorf("config.yaml must contain one YAML document")
	}
	if e = LoadEnv(root); e != nil {
		return c, e
	}
	return c, c.Validate()
}

func LoadEnv(root string) error {
	p := filepath.Join(root, ".env")
	f, e := os.Stat(p)
	if os.IsNotExist(e) {
		return nil
	}
	if e != nil {
		return e
	}
	if f.Mode().Perm() != 0o600 {
		return fmt.Errorf(".env must have permissions 600")
	}
	b, e := os.ReadFile(p)
	if e != nil {
		return e
	}
	for _, l := range strings.Split(string(b), "\n") {
		l = strings.TrimSpace(l)
		if l == "" || strings.HasPrefix(l, "#") {
			continue
		}
		k, v, ok := strings.Cut(l, "=")
		if !ok {
			return fmt.Errorf("invalid .env assignment")
		}
		k = strings.TrimSpace(strings.TrimPrefix(k, "export "))
		v = strings.Trim(strings.TrimSpace(v), "\"'")
		if _, ok = os.LookupEnv(k); !ok {
			if err := os.Setenv(k, v); err != nil {
				return fmt.Errorf("invalid environment variable name")
			}
		}
	}
	return nil
}

func minutes(s string) (int, error) {
	if s == "24:00" {
		return 1440, nil
	}
	p := strings.Split(s, ":")
	if len(p) != 2 || len(p[0]) != 2 || len(p[1]) != 2 {
		return 0, fmt.Errorf("time must be HH:MM")
	}
	h, e := strconv.Atoi(p[0])
	if e != nil {
		return 0, e
	}
	m, e := strconv.Atoi(p[1])
	if e != nil || h < 0 || h > 23 || m < 0 || m > 59 {
		return 0, fmt.Errorf("invalid time")
	}
	return h*60 + m, nil
}

func (c Config) Validate() error {
	if p := c.Observability.SampleRatio; p != nil && (*p < 0 || *p > 1) {
		return fmt.Errorf("observability.sample_ratio must be 0..1")
	}
	if err := c.Platform.Validate(c); err != nil {
		return err
	}
	if days := c.HistoryRetentionDays(); days < 0 || days > 3650 {
		return fmt.Errorf("history_retention_days must be 0..3650")
	}
	if v := c.Detector.Polling.NotifiedLiveMinutes; v != nil && (*v < 1 || *v > 60) {
		return fmt.Errorf("notified_live_interval_minutes must be 1..60")
	}
	if c.Subscription.RoomID <= 0 {
		return fmt.Errorf("invalid room_id")
	}
	if c.Detector.Mode != "polling" && c.Detector.Mode != "official" {
		return fmt.Errorf("invalid detector mode")
	}
	if c.Detector.Polling.IntervalMinutes != nil && c.Detector.Polling.Interval != nil {
		return fmt.Errorf("configure exactly one of interval_minutes or legacy interval_seconds")
	}
	if c.Detector.Polling.IntervalMinutes != nil && (*c.Detector.Polling.IntervalMinutes < 1 || *c.Detector.Polling.IntervalMinutes > 60) {
		return fmt.Errorf("interval_minutes must be 1..60")
	}
	if c.PollingSeconds() < 1 || c.PollingSeconds() > 3600 || c.Detector.Polling.Timeout < 1 {
		return fmt.Errorf("invalid polling interval/timeout")
	}
	if c.Notification.Mode != "feishu_group" && c.Notification.Mode != "feishu_private" {
		return fmt.Errorf("invalid notification mode")
	}
	if c.Notification.TTL < 1 {
		return fmt.Errorf("invalid notification TTL")
	}
	if c.Deployment.Active != "local" && c.Deployment.Active != "cloud" {
		return fmt.Errorf("invalid deployment.active")
	}
	if _, e := time.LoadLocation(c.Schedule.Timezone); e != nil {
		return fmt.Errorf("invalid timezone")
	}
	s, e := minutes(c.Schedule.Start)
	if e != nil {
		return e
	}
	end, e := minutes(c.Schedule.End)
	if e != nil || s >= end {
		return fmt.Errorf("invalid same-day time window")
	}
	if len(c.Schedule.Weekdays) == 0 {
		return fmt.Errorf("empty weekdays")
	}
	for _, d := range c.Schedule.Weekdays {
		if d < 1 || d > 7 {
			return fmt.Errorf("weekday must be 1..7")
		}
	}
	if c.Notification.Mode == "feishu_private" && c.Notification.Private.IDType != "open_id" && c.Notification.Private.IDType != "user_id" && c.Notification.Private.IDType != "union_id" {
		return fmt.Errorf("invalid receive_id_type")
	}
	return nil
}

func (c Config) Credentials() error {
	names := []string{}
	if c.Platform.StorageMode() == "postgres" {
		names = append(names, c.Platform.Postgres.DSNEnv)
	}
	if c.Platform.Cache == "redis" || c.Platform.Queue == "redis_streams" {
		names = append(names, c.Platform.Redis.URLEnv)
	}
	if c.Notification.Mode == "feishu_group" {
		names = append(names, c.Notification.Group.Webhook, c.Notification.Group.Secret)
	} else {
		names = append(names, c.Notification.Private.AppID, c.Notification.Private.Secret, c.Notification.Private.ID)
	}
	if c.Detector.Mode == "official" {
		o := c.Detector.Official
		names = append(names, o.AppID, o.KeyID, o.KeySecret, o.Anchor)
		id, e := strconv.ParseInt(os.Getenv(o.AppID), 10, 64)
		if e != nil || id <= 0 {
			return fmt.Errorf("official app id must be positive")
		}
	}
	for _, n := range names {
		if n == "" || os.Getenv(n) == "" {
			return fmt.Errorf("missing environment variable %s", n)
		}
	}
	if c.Notification.Mode == "feishu_group" && !strings.HasPrefix(os.Getenv(c.Notification.Group.Webhook), "https://open.feishu.cn/open-apis/bot/v2/hook/") {
		return fmt.Errorf("invalid Feishu webhook URL")
	}
	return nil
}

func (c Config) InWindow(now time.Time) bool {
	loc, err := time.LoadLocation(c.Schedule.Timezone)
	if err != nil {
		return false
	}
	t := now.In(loc)
	day := int(t.Weekday())
	if day == 0 {
		day = 7
	}
	found := false
	for _, v := range c.Schedule.Weekdays {
		if v == day {
			found = true
		}
	}
	s, err := minutes(c.Schedule.Start)
	if err != nil {
		return false
	}
	e, err := minutes(c.Schedule.End)
	if err != nil {
		return false
	}
	m := t.Hour()*60 + t.Minute()
	return found && m >= s && m < e
}

func Backoff(n, base int) time.Duration {
	if n < 1 {
		n = 1
	}
	if n > 16 {
		n = 16
	}
	v := base * (1 << (n - 1))
	if cap := max(300, base); v > cap {
		v = cap
	}
	return time.Duration(v) * time.Second
}

func (c Config) PollingSeconds() int {
	if c.Detector.Polling.IntervalMinutes != nil {
		return *c.Detector.Polling.IntervalMinutes * 60
	}
	if c.Detector.Polling.Interval != nil {
		return *c.Detector.Polling.Interval
	}
	return 0
}

func (c Config) NotifiedLiveSeconds() int {
	minutes := 5
	if c.Detector.Polling.NotifiedLiveMinutes != nil {
		minutes = *c.Detector.Polling.NotifiedLiveMinutes
	}
	return max(c.PollingSeconds(), minutes*60)
}

func (c Config) HistoryRetentionDays() int {
	if c.Maintenance.RetentionDays != nil {
		return *c.Maintenance.RetentionDays
	}
	return 90
}
