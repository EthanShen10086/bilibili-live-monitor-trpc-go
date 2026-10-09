package monitor

import (
	"bytes"
	"compress/zlib"
	"context"
	"crypto/hmac"
	"crypto/md5"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/EthanShen10086/bilibili-live-monitor-trpc-go/internal/resource"

	"github.com/andybalholm/brotli"
	"github.com/gorilla/websocket"
)

func OfficialHeaders(body []byte, id, key string, stamp int64, nonce string) map[string]string {
	sum := md5.Sum(body)
	h := map[string]string{"x-bili-accesskeyid": id, "x-bili-content-md5": hex.EncodeToString(sum[:]), "x-bili-signature-method": "HMAC-SHA256", "x-bili-signature-nonce": nonce, "x-bili-signature-version": "1.0", "x-bili-timestamp": fmtRoom(stamp)}
	keys := []string{}
	for k := range h {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	lines := []string{}
	for _, k := range keys {
		lines = append(lines, k+":"+h[k])
	}
	mac := hmac.New(sha256.New, []byte(key))
	mac.Write([]byte(strings.Join(lines, "\n")))
	h["Authorization"] = hex.EncodeToString(mac.Sum(nil))
	return h
}

type Official struct {
	c        Config
	h        *HTTP
	room     int64
	game     string
	ws       *websocket.Conn
	Events   chan Observation
	Changed  chan struct{}
	mu       sync.Mutex
	err      error
	ready    chan struct{}
	done     chan struct{}
	lastAck  time.Time
	lastGame time.Time
	write    sync.Mutex
}

func NewOfficial(c Config, h *HTTP, room int64) *Official {
	return &Official{c: c, h: h, room: room, Events: make(chan Observation, 128), Changed: make(chan struct{}, 1), ready: make(chan struct{}), done: make(chan struct{})}
}

func (o *Official) setError(e error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.err == nil {
		o.err = e
		select {
		case o.Changed <- struct{}{}:
		default:
		}
	}
}

func (o *Official) api(ctx context.Context, route string, payload any, out any) error {
	body := jsonBody(payload)
	c := o.c.Detector.Official
	var r struct {
		Code *int            `json:"code"`
		Data json.RawMessage `json:"data"`
	}
	e := o.h.JSON(ctx, "POST", "https://live-open.biliapi.com/v2/app/"+route, body, OfficialHeaders(body, os.Getenv(c.KeyID), os.Getenv(c.KeySecret), time.Now().Unix(), ID()), &r)
	if e != nil {
		return e
	}
	if r.Code == nil {
		return &RemoteError{"Official", "invalid_response", true}
	}
	if *r.Code != 0 {
		retry := true
		for _, v := range []int{4001, 4002, 4003, 4004, 4005, 4006, 7001, 7002, 7003} {
			if v == *r.Code {
				retry = false
			}
		}
		return &RemoteError{"Official", strconv.Itoa(*r.Code), retry}
	}
	if out != nil && json.Unmarshal(r.Data, out) != nil {
		return &RemoteError{"Official", "session_schema", false}
	}
	return nil
}

func frame(op uint32, b []byte) []byte {
	r := make([]byte, 16+len(b))
	binary.BigEndian.PutUint32(r, uint32(len(r)))
	binary.BigEndian.PutUint16(r[4:], 16)
	binary.BigEndian.PutUint16(r[6:], 1)
	binary.BigEndian.PutUint32(r[8:], op)
	binary.BigEndian.PutUint32(r[12:], 1)
	copy(r[16:], b)
	return r
}

func DecodeFrames(b []byte, depth int, consume func(uint32, []byte) error) error {
	if depth > 4 {
		return fmt.Errorf("compression nesting limit")
	}
	for len(b) > 0 {
		if len(b) < 16 {
			return fmt.Errorf("truncated frame")
		}
		length := int(binary.BigEndian.Uint32(b))
		header := int(binary.BigEndian.Uint16(b[4:]))
		version := binary.BigEndian.Uint16(b[6:])
		op := binary.BigEndian.Uint32(b[8:])
		if length > len(b) || length < 16 || header < 16 || header > length {
			return fmt.Errorf("invalid frame length")
		}
		body := b[header:length]
		if version == 2 || version == 3 {
			var r io.Reader
			var closer io.Closer
			if version == 2 {
				z, e := zlib.NewReader(bytes.NewReader(body))
				if e != nil {
					return fmt.Errorf("invalid compressed frame")
				}
				r = z
				closer = z
			} else {
				r = brotli.NewReader(bytes.NewReader(body))
			}
			expanded, e := io.ReadAll(io.LimitReader(r, 4*1024*1024+1))
			if closer != nil {
				resource.Close(closer)
			}
			if e != nil || len(expanded) > 4*1024*1024 {
				return fmt.Errorf("expanded frame limit")
			}
			if e = DecodeFrames(expanded, depth+1, consume); e != nil {
				return e
			}
		} else if version == 0 || version == 1 {
			if e := consume(op, body); e != nil {
				return e
			}
		} else {
			return fmt.Errorf("unknown frame version")
		}
		b = b[length:]
	}
	return nil
}

func OfficialObservation(body []byte, room int64, at time.Time) (*Observation, error) {
	var m struct {
		Cmd  string `json:"cmd"`
		Data struct {
			Room  int64   `json:"room_id"`
			Title *string `json:"title"`
			Stamp any     `json:"timestamp"`
			Start any     `json:"live_start_time"`
		} `json:"data"`
	}
	if json.Unmarshal(body, &m) != nil {
		return nil, &RemoteError{"Official", "event_schema", true}
	}
	if m.Cmd == "LIVE_OPEN_PLATFORM_INTERACTION_END" {
		return nil, &RemoteError{"Official", "session_ended", true}
	}
	if m.Cmd != "LIVE_OPEN_PLATFORM_LIVE_START" && m.Cmd != "LIVE_OPEN_PLATFORM_LIVE_END" {
		return nil, nil
	}
	if m.Data.Room != room {
		return nil, &RemoteError{"Official", "wrong_event_room", false}
	}
	live := m.Cmd == "LIVE_OPEN_PLATFORM_LIVE_START"
	if live && m.Data.Title == nil {
		return nil, &RemoteError{"Official", "event_schema", true}
	}
	title := ""
	if m.Data.Title != nil {
		title = *m.Data.Title
	}
	stamp := m.Data.Stamp
	if stamp == nil {
		stamp = m.Data.Start
	}
	return &Observation{room, live, title, NormalizeStart(stamp), at.UnixMilli()}, nil
}

func (o *Official) Start(ctx context.Context) error {
	var r struct {
		Game struct {
			ID string `json:"game_id"`
		} `json:"game_info"`
		Anchor struct {
			Room int64 `json:"room_id"`
		} `json:"anchor_info"`
		Socket struct {
			Auth  string   `json:"auth_body"`
			Links []string `json:"wss_link"`
		} `json:"websocket_info"`
	}
	app, err := strconv.ParseInt(os.Getenv(o.c.Detector.Official.AppID), 10, 64)
	if err != nil || app <= 0 {
		return &RemoteError{"Official", "invalid_app_id", false}
	}
	e := o.api(ctx, "start", map[string]any{"app_id": app, "code": os.Getenv(o.c.Detector.Official.Anchor)}, &r)
	o.game = r.Game.ID
	if e != nil {
		return e
	}
	if r.Anchor.Room != o.room {
		return &RemoteError{"Official", "authorized_room_mismatch", false}
	}
	if o.game == "" || r.Socket.Auth == "" || len(r.Socket.Links) == 0 {
		return &RemoteError{"Official", "session_schema", false}
	}
	u, e := url.Parse(r.Socket.Links[0])
	if e != nil || u.Scheme != "wss" || u.Hostname() == "" {
		return &RemoteError{"Official", "invalid_wss", false}
	}
	dial := websocket.Dialer{HandshakeTimeout: 10 * time.Second}
	ws, resp, e := dial.DialContext(ctx, u.String(), http.Header{})
	if resp != nil && resp.Body != nil {
		resource.LogError("websocket_response_close", resp.Body.Close())
	}
	if e != nil {
		return &RemoteError{"Official", "socket_connect", true}
	}
	o.ws = ws
	ws.SetReadLimit(4 * 1024 * 1024)
	o.lastGame = time.Now()
	o.lastAck = time.Now()
	if e = o.send(7, []byte(r.Socket.Auth)); e != nil {
		return e
	}
	go o.read()
	go o.heartbeat()
	timer := time.NewTimer(12 * time.Second)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-o.ready:
		return o.Tick(ctx)
	case <-o.done:
		if e := o.Tick(ctx); e != nil {
			return e
		}
		return &RemoteError{"Official", "socket_closed", true}
	case <-timer.C:
		return &RemoteError{"Official", "authentication_timeout", true}
	}
}

func (o *Official) send(op uint32, b []byte) error {
	o.write.Lock()
	defer o.write.Unlock()
	if err := o.ws.SetWriteDeadline(time.Now().Add(5 * time.Second)); err != nil {
		return &RemoteError{"Official", "socket_deadline", true}
	}
	if o.ws.WriteMessage(websocket.BinaryMessage, frame(op, b)) != nil {
		return &RemoteError{"Official", "socket_write", true}
	}
	return nil
}

func (o *Official) read() {
	defer close(o.done)
	for {
		_, b, e := o.ws.ReadMessage()
		if e != nil {
			o.setError(&RemoteError{"Official", "socket_closed", true})
			return
		}
		e = DecodeFrames(b, 0, func(op uint32, body []byte) error {
			switch op {
			case 8:
				var a struct {
					Code *int `json:"code"`
				}
				if json.Unmarshal(body, &a) != nil || a.Code == nil || *a.Code != 0 {
					return &RemoteError{"Official", "authentication_rejected", false}
				}
				select {
				case <-o.ready:
				default:
					close(o.ready)
				}
			case 3:
				o.mu.Lock()
				o.lastAck = time.Now()
				o.mu.Unlock()
			case 5:
				select {
				case <-o.ready:
				default:
					return fmt.Errorf("unauthenticated event")
				}
				v, e := OfficialObservation(body, o.room, time.Now())
				if e != nil {
					return e
				}
				if v != nil {
					select {
					case o.Events <- *v:
					default:
						return fmt.Errorf("event queue overflow")
					}
				}
			}
			return nil
		})
		if e != nil {
			o.setError(e)
			resource.Close(o.ws)
			return
		}
	}
}

func (o *Official) heartbeat() {
	t := time.NewTicker(20 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-o.done:
			return
		case <-t.C:
			if e := o.send(2, nil); e != nil {
				o.setError(e)
				resource.Close(o.ws)
				return
			}
		}
	}
}

func (o *Official) Tick(ctx context.Context) error {
	o.mu.Lock()
	err := o.err
	last := o.lastAck
	o.mu.Unlock()
	if err != nil {
		return err
	}
	if time.Since(last) > 45*time.Second {
		return &RemoteError{"Official", "heartbeat_timeout", true}
	}
	if time.Since(o.lastGame) >= 20*time.Second {
		if e := o.api(ctx, "heartbeat", map[string]string{"game_id": o.game}, nil); e != nil {
			return e
		}
		o.lastGame = time.Now()
	}
	return nil
}

func (o *Official) Stop(ctx context.Context) error {
	if o.ws != nil {
		resource.Close(o.ws)
	}
	if o.game != "" {
		app, err := strconv.ParseInt(os.Getenv(o.c.Detector.Official.AppID), 10, 64)
		if err != nil || app <= 0 {
			return &RemoteError{"Official", "invalid_app_id", false}
		}
		if e := o.api(ctx, "end", map[string]any{"app_id": app, "game_id": o.game}, nil); e != nil {
			return e
		}
		o.game = ""
	}
	return nil
}
