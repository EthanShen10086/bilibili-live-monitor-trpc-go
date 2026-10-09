package monitor

import (
	"context"
	"encoding/json"
	"strconv"
	"time"
)

// Cache observations only briefly. It is never authoritative for sessions or
// delivery state, and failures/corruption fall through to the upstream detector.
type cachedDetector struct {
	Detector
	cache Cache
	now   func() time.Time
}

// WithObservationCache adds bounded cache reuse without changing durable state.
func WithObservationCache(detector Detector, cache Cache, now func() time.Time) Detector {
	return cachedDetector{Detector: detector, cache: cache, now: now}
}

func (d cachedDetector) Probe(ctx context.Context, c Config) (Observation, error) {
	if err := ctx.Err(); err != nil {
		return Observation{}, err
	}
	ttl := min(5*time.Second, time.Duration(c.PollingSeconds())*time.Second)
	key := "room-observation-v1:" + strconv.FormatInt(c.Subscription.RoomID, 10)
	if raw, ok := d.cache.Get(ctx, key); ok {
		var o Observation
		if json.Unmarshal(raw, &o) == nil && o.RoomID == c.Subscription.RoomID && o.At > 0 {
			age := d.now().Sub(time.UnixMilli(o.At))
			if age >= 0 && age < ttl {
				return o, nil
			}
		}
	}
	o, err := d.Detector.Probe(ctx, c)
	if err != nil {
		return o, err
	}
	if o.RoomID == c.Subscription.RoomID && o.At > 0 {
		age := d.now().Sub(time.UnixMilli(o.At))
		if age >= 0 && age < ttl {
			if raw, encodeErr := json.Marshal(o); encodeErr == nil {
				d.cache.Put(ctx, key, raw, ttl-age)
			}
		}
	}
	return o, nil
}
