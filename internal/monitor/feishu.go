package monitor

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"strconv"
	"sync"
	"time"
)

func fmtRoom(id int64) string { return strconv.FormatInt(id, 10) }
func FeishuSign(stamp, secret string) string {
	h := hmac.New(sha256.New, []byte(stamp+"\n"+secret))
	return base64.StdEncoding.EncodeToString(h.Sum(nil))
}

func FormatNotice(n Notice, room int64) string {
	loc := time.FixedZone("Asia/Shanghai", 8*60*60)
	kind := "开播了"
	if n.Catchup {
		kind = "当前正在直播"
	}
	title := n.Title
	if title == "" {
		title = "暂无标题"
	}
	text := fmt.Sprintf("[B站订阅] %s\n房间：%d（真实 ID %d）\n标题：%s", kind, room, n.RoomID, title)
	if n.Start != "" {
		if t, e := time.Parse(time.RFC3339, n.Start); e == nil {
			text += "\n开播时间：" + t.In(loc).Format("2006-01-02 15:04:05")
		}
	}
	return text + "\n检测时间：" + time.UnixMilli(n.At).In(loc).Format("2006-01-02 15:04:05") + "\nhttps://live.bilibili.com/" + fmtRoom(room)
}

type Feishu struct {
	Config  Config
	HTTP    *HTTP
	mu      sync.Mutex
	token   string
	expires time.Time
}

func checked(code *int) error {
	if code == nil {
		return &RemoteError{"Feishu", "invalid_response", true}
	}
	if *code == 0 {
		return nil
	}
	retry := true
	for _, v := range []int{19021, 19022, 19024, 19025, 99991661, 99991663, 99991672, 230002, 230013, 230017} {
		if *code == v {
			retry = false
		}
	}
	return &RemoteError{"Feishu", strconv.Itoa(*code), retry}
}

func (f *Feishu) access(ctx context.Context) (string, error) {
	if f.token != "" && time.Until(f.expires) > time.Minute {
		return f.token, nil
	}
	c := f.Config.Notification.Private
	var r struct {
		Code   *int   `json:"code"`
		Token  string `json:"tenant_access_token"`
		Expire int    `json:"expire"`
	}
	e := f.HTTP.JSON(ctx, "POST", "https://open.feishu.cn/open-apis/auth/v3/tenant_access_token/internal", jsonBody(map[string]string{"app_id": os.Getenv(c.AppID), "app_secret": os.Getenv(c.Secret)}), nil, &r)
	if e != nil {
		return "", e
	}
	if e = checked(r.Code); e != nil {
		return "", e
	}
	if r.Token == "" || r.Expire <= 0 {
		return "", &RemoteError{"Feishu", "token_schema", true}
	}
	f.token = r.Token
	f.expires = time.Now().Add(time.Duration(r.Expire) * time.Second)
	return f.token, nil
}

func (f *Feishu) Send(ctx context.Context, text, key string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	c := f.Config.Notification
	var r struct {
		Code *int `json:"code"`
	}
	if c.Mode == "feishu_group" {
		stamp := fmtRoom(time.Now().Unix())
		e := f.HTTP.JSON(ctx, "POST", os.Getenv(c.Group.Webhook), jsonBody(map[string]any{"timestamp": stamp, "sign": FeishuSign(stamp, os.Getenv(c.Group.Secret)), "msg_type": "text", "content": map[string]string{"text": text}}), nil, &r)
		if e != nil {
			return e
		}
		return checked(r.Code)
	}
	send := func() error {
		token, e := f.access(ctx)
		if e != nil {
			return e
		}
		hash := sha256.Sum256([]byte(key))
		e = f.HTTP.JSON(ctx, "POST", "https://open.feishu.cn/open-apis/im/v1/messages?receive_id_type="+c.Private.IDType, jsonBody(map[string]any{"receive_id": os.Getenv(c.Private.ID), "msg_type": "text", "content": string(jsonBody(map[string]string{"text": text})), "uuid": hex.EncodeToString(hash[:])[:32]}), map[string]string{"Authorization": "Bearer " + token}, &r)
		if e != nil {
			return e
		}
		return checked(r.Code)
	}
	e := send()
	var remote *RemoteError
	if errors.As(e, &remote) && (remote.Code == "99991661" || remote.Code == "99991663" || remote.Code == "99991668") {
		f.token = ""
		return send()
	}
	return e
}
