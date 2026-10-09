package monitor

import (
	"context"
	"fmt"
	"path/filepath"
	"regexp"
	"time"
)

// Repository keeps durability independent of the scheduling and notification engines.
type Repository interface {
	Observe(Observation, bool, int) (bool, error)
	PollingPhase(int64) (string, error)
	Due(time.Time) (*Job, error)
	Sent(string) error
	Failed(*Job, error, time.Time) error
	Retry(time.Time) (int64, error)
	DataVersion() (int64, error)
	Counts() (map[string]int, error)
	NextWake() (time.Time, error)
	NextCleanupAt(int, time.Time) (time.Time, error)
	CleanupHistory(int, time.Time) (int64, error)
	Close() error
}

// QueueAge is optional so custom repositories can evolve independently.
type QueueAge interface{ OldestPending() (int64, error) }

type TaskQueue interface {
	Publish(context.Context) error
	Receive(context.Context) (*Job, string, error)
	Ack(context.Context, string) error
	Close() error
}

type PlatformConfig struct {
	Storage        string `yaml:"storage"`
	Cache          string `yaml:"cache"`
	Queue          string `yaml:"queue"`
	Role           string `yaml:"role"`
	SubscriptionID string `yaml:"subscription_id"`
	Postgres       struct {
		DSNEnv      string `yaml:"dsn_env"`
		AutoMigrate *bool  `yaml:"auto_migrate,omitempty"`
	} `yaml:"postgres"`
	Redis struct {
		URLEnv string `yaml:"url_env"`
	} `yaml:"redis"`
}

func (p PlatformConfig) StorageMode() string {
	if p.Storage == "" {
		return "sqlite"
	}
	return p.Storage
}

func (p PlatformConfig) WorkerRole() string {
	if p.Role == "" {
		return "both"
	}
	return p.Role
}

func (p PlatformConfig) QueueMode() string {
	if p.Queue == "" {
		return "database"
	}
	return p.Queue
}

func (p PlatformConfig) CacheMode() string {
	if p.Cache == "" {
		return "memory"
	}
	return p.Cache
}

func (p PlatformConfig) Validate(c Config) error {
	if p.StorageMode() != "sqlite" && p.StorageMode() != "postgres" {
		return fmt.Errorf("platform.storage must be sqlite|postgres")
	}
	if p.CacheMode() != "memory" && p.CacheMode() != "redis" {
		return fmt.Errorf("platform.cache must be memory|redis")
	}
	if p.QueueMode() != "database" && p.QueueMode() != "redis_streams" {
		return fmt.Errorf("platform.queue must be database|redis_streams")
	}
	if p.WorkerRole() != "both" && p.WorkerRole() != "detector" && p.WorkerRole() != "sender" {
		return fmt.Errorf("platform.role must be both|detector|sender")
	}
	if p.StorageMode() == "sqlite" {
		if p.QueueMode() != "database" || p.CacheMode() != "memory" || p.WorkerRole() != "both" {
			return fmt.Errorf("distributed roles and Redis require PostgreSQL mode")
		}
	} else {
		if c.Deployment.Active != "cloud" || c.Detector.Mode != "polling" {
			return fmt.Errorf("PostgreSQL platform currently requires cloud polling mode")
		}
		if !regexp.MustCompile(`^[a-zA-Z0-9_-]{1,64}$`).MatchString(p.SubscriptionID) {
			return fmt.Errorf("set stable platform.subscription_id (1..64 letters, digits, - or _)")
		}
		if p.Postgres.DSNEnv == "" {
			return fmt.Errorf("set platform.postgres.dsn_env")
		}
	}
	if (p.CacheMode() == "redis" || p.QueueMode() == "redis_streams") && p.Redis.URLEnv == "" {
		return fmt.Errorf("set platform.redis.url_env")
	}
	return nil
}

func OpenRepository(ctx context.Context, root string, c Config) (Repository, error) {
	if c.Platform.StorageMode() == "postgres" {
		return OpenPostgres(ctx, c)
	}
	return OpenStore(filepath.Join(root, "var/state.sqlite"))
}
