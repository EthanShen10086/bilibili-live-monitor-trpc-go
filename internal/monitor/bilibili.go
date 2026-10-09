package monitor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"time"
)

type Observation struct {
	RoomID int64  `json:"roomId"`
	Live   bool   `json:"live"`
	Title  string `json:"title"`
	Start  string `json:"startTime,omitempty"`
	At     int64  `json:"detectedAt"`
}
type Notice struct {
	Observation
	Key     string `json:"key"`
	Catchup bool   `json:"catchup"`
}

func NormalizeStart(v any) string {
	var t time.Time
	switch x := v.(type) {
	case float64:
		if x <= 0 {
			return ""
		}
		t = time.Unix(int64(x), 0)
	case string:
		var e error
		t, e = time.Parse("2006-01-02 15:04:05 -0700", x+" +0800")
		if e != nil || t.Year() < 1970 {
			return ""
		}
	default:
		return ""
	}
	return t.UTC().Format("2006-01-02T15:04:05.000Z")
}

func ParseRoom(b []byte, requested int64, at time.Time) (Observation, error) {
	var r struct {
		Code *int `json:"code"`
		Data struct {
			RoomID int64   `json:"room_id"`
			Short  int64   `json:"short_id"`
			Live   *int    `json:"live_status"`
			Title  *string `json:"title"`
			Start  any     `json:"live_time"`
		} `json:"data"`
	}
	if json.Unmarshal(b, &r) != nil || r.Code == nil {
		return Observation{}, &RemoteError{"Bilibili", "schema_changed", true}
	}
	if *r.Code != 0 {
		return Observation{}, &RemoteError{"Bilibili", strconv.Itoa(*r.Code), true}
	}
	d := r.Data
	if d.RoomID <= 0 || d.Live == nil || *d.Live < 0 || *d.Live > 2 || d.Title == nil {
		return Observation{}, &RemoteError{"Bilibili", "schema_changed", true}
	}
	if d.RoomID != requested && d.Short != requested {
		return Observation{}, &RemoteError{"Bilibili", "wrong_room", false}
	}
	return Observation{d.RoomID, *d.Live == 1, *d.Title, NormalizeStart(d.Start), at.UnixMilli()}, nil
}

func (h *HTTP) Probe(ctx context.Context, c Config) (Observation, error) {
	ctx, cancel := context.WithTimeout(ctx, time.Duration(c.Detector.Polling.Timeout)*time.Second)
	defer cancel()
	var raw json.RawMessage
	e := h.JSON(ctx, "GET", fmt.Sprintf("https://api.live.bilibili.com/room/v1/Room/get_info?room_id=%d", c.Subscription.RoomID), nil, nil, &raw)
	if e != nil {
		var r *RemoteError
		if errors.As(e, &r) {
			r.Retry = true
		}
		return Observation{}, e
	}
	return ParseRoom(raw, c.Subscription.RoomID, time.Now())
}
