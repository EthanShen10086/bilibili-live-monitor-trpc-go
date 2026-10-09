package monitor

import (
	"bytes"
	"compress/zlib"
	"context"
	"encoding/binary"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func TestOfficialSigningAndRoom(t *testing.T) {
	h := OfficialHeaders([]byte("{}"), "id", "key", 123, "fixed-nonce")
	if h["x-bili-content-md5"] != "99914b932bd37a50b983c5e7c90ae93b" || len(h["Authorization"]) != 64 {
		t.Fatal("headers")
	}
	if h["Authorization"] != OfficialHeaders([]byte("{}"), "id", "key", 123, "fixed-nonce")["Authorization"] {
		t.Fatal("non-deterministic")
	}
	if _, e := OfficialObservation([]byte(`{"cmd":"LIVE_OPEN_PLATFORM_LIVE_START","data":{"room_id":1,"title":"test","timestamp":1791115200}}`), 2, time.Now()); e == nil || Retryable(e) {
		t.Fatal("room mismatch")
	}
	o, e := OfficialObservation([]byte(`{"cmd":"LIVE_OPEN_PLATFORM_LIVE_START","data":{"room_id":1,"title":"test","timestamp":1791115200}}`), 1, time.Now())
	if e != nil || o == nil || !o.Live || o.Start == "" {
		t.Fatal(o, e)
	}
}

func TestFramesCompressionAndLimits(t *testing.T) {
	b := frame(5, []byte(`{"cmd":"test"}`))
	calls := 0
	consume := func(op uint32, p []byte) error {
		calls++
		if op != 5 || !strings.Contains(string(p), "test") {
			t.Fatal(op, string(p))
		}
		return nil
	}
	if e := DecodeFrames(append(b, b...), 0, consume); e != nil || calls != 2 {
		t.Fatal(e, calls)
	}
	var compressed bytes.Buffer
	w := zlib.NewWriter(&compressed)
	w.Write(b)
	w.Close()
	packed := frame(5, compressed.Bytes())
	binary.BigEndian.PutUint16(packed[6:], 2)
	if e := DecodeFrames(packed, 0, consume); e != nil {
		t.Fatal(e)
	}
	bad := append([]byte(nil), b...)
	binary.BigEndian.PutUint32(bad, 99999)
	if DecodeFrames(bad, 0, consume) == nil {
		t.Fatal("bad frame accepted")
	}
	if DecodeFrames(b[:5], 0, consume) == nil {
		t.Fatal("truncation")
	}
	if DecodeFrames(b, 5, consume) == nil {
		t.Fatal("depth")
	}
}

func TestSwitchStopsBeforeTransferAndRollback(t *testing.T) {
	calls := []string{}
	ops := SwitchOps{Preflight: func(s string) error { calls = append(calls, "pre:"+s); return nil }, Stop: func(s string) error { calls = append(calls, "stop:"+s); return nil }, Assert: func(s string) error { calls = append(calls, "assert:"+s); return nil }, Transfer: func(a, b string) error { calls = append(calls, "transfer:"+a+":"+b); return nil }, Active: func(s string) error { calls = append(calls, "active:"+s); return nil }, Start: func(s string) error { calls = append(calls, "start:"+s); return nil }, Health: func(s string) error { calls = append(calls, "health:"+s); return nil }}
	if e := Switch("local", "cloud", ops); e != nil {
		t.Fatal(e)
	}
	actual := strings.Join(calls, ",")
	if actual != "pre:cloud,stop:cloud,assert:cloud,stop:local,assert:local,transfer:local:cloud,active:cloud,start:cloud,health:cloud" {
		t.Fatal(actual)
	}
	calls = nil
	ops.Preflight = func(string) error { return &RemoteError{"preflight", "linger_missing", false} }
	if e := Switch("local", "cloud", ops); e == nil || len(calls) != 0 {
		t.Fatal("source changed after failed preflight")
	}
}

func TestInvalidStateImport(t *testing.T) {
	if e := Import(t.TempDir(), "bm90IGEgZGF0YWJhc2U="); e == nil {
		t.Fatal("invalid SQLite imported")
	}
}

func TestSwitchRollbackRequiresStoppedTarget(t *testing.T) {
	for _, stopFails := range []bool{false, true} {
		starts := []string{}
		transfers := []string{}
		stops := 0
		ops := SwitchOps{
			Preflight: func(string) error { return nil },
			Stop: func(s string) error {
				stops++
				if stopFails && stops == 3 {
					return &RemoteError{"stop", "unconfirmed", false}
				}
				return nil
			},
			Assert:   func(string) error { return nil },
			Transfer: func(a, b string) error { transfers = append(transfers, a+":"+b); return nil },
			Active:   func(string) error { return nil },
			Start:    func(s string) error { starts = append(starts, s); return nil },
			Health: func(s string) error {
				if s == "cloud" {
					return &RemoteError{"health", "failed", true}
				}
				return nil
			},
		}
		if Switch("local", "cloud", ops) == nil {
			t.Fatal("failed target accepted")
		}
		if stopFails {
			if strings.Join(starts, ",") != "cloud" || len(transfers) != 1 {
				t.Fatal("unsafe rollback", starts, transfers)
			}
		} else if strings.Join(starts, ",") != "cloud,local" || strings.Join(transfers, ",") != "local:cloud,cloud:local" {
			t.Fatal("latest state not restored", starts, transfers)
		}
	}
}

func TestOfficialReadAuthEventAndReject(t *testing.T) {
	for _, rejected := range []bool{false, true} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			up := websocket.Upgrader{}
			ws, e := up.Upgrade(w, r, nil)
			if e != nil {
				return
			}
			defer ws.Close()
			code := []byte(`{"code":0}`)
			if rejected {
				code = []byte(`{"code":-1}`)
			}
			ws.WriteMessage(websocket.BinaryMessage, frame(8, code))
			if !rejected {
				ws.WriteMessage(websocket.BinaryMessage, frame(5, []byte(`{"cmd":"LIVE_OPEN_PLATFORM_LIVE_START","data":{"room_id":1,"title":"test","timestamp":1791115200}}`)))
			}
			ws.ReadMessage()
		}))
		ws, resp, e := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http"), nil)
		if e != nil {
			t.Fatal(e)
		}
		if resp != nil && resp.Body != nil {
			resp.Body.Close()
		}
		o := NewOfficial(testConfig(t), NewHTTP(), 1)
		o.ws = ws
		o.lastAck = time.Now()
		o.lastGame = time.Now()
		go o.read()
		if rejected {
			select {
			case <-o.done:
			case <-time.After(time.Second):
				t.Fatal("rejection not handled")
			}
			if e = o.Tick(context.Background()); e == nil || Retryable(e) {
				t.Fatal("authentication failure must block", e)
			}
		} else {
			select {
			case got := <-o.Events:
				if !got.Live || got.RoomID != 1 {
					t.Fatal(got)
				}
			case <-time.After(time.Second):
				t.Fatal("event missing")
			}
		}
		ws.Close()
		<-o.done
		server.Close()
	}
}

func TestOfficialMismatchRetainsSessionUntilCleanup(t *testing.T) {
	t.Setenv("BILI_APP_ID", "1")
	ended := 0
	h := mockHTTP(func(r *http.Request) (*http.Response, error) {
		if strings.HasSuffix(r.URL.Path, "/end") {
			ended++
			return response(`{"code":0,"data":{}}`, 200), nil
		}
		return response(`{"code":0,"data":{"game_info":{"game_id":"owned"},"anchor_info":{"room_id":2}}}`, 200), nil
	})
	o := NewOfficial(testConfig(t), h, 1)
	if e := o.Start(context.Background()); e == nil || Retryable(e) {
		t.Fatal("wrong room accepted")
	}
	if o.game != "owned" {
		t.Fatal("session lost")
	}
	if e := o.Stop(context.Background()); e != nil || ended != 1 || o.game != "" {
		t.Fatal(e, ended)
	}
}
