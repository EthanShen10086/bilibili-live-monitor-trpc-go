package channels

import (
	"bufio"
	"context"
	"net"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/EthanShen10086/bilibili-live-monitor-trpc-go/internal/eventplatform/domain"
)

// Exercise the SMTP wire protocol: an accepted DATA survives a lost QUIT reply.
func TestSMTPAcceptanceAndTLSRequirement(t *testing.T) {
	for _, plaintext := range []bool{true, false} {
		t.Run(strconv.FormatBool(plaintext), func(t *testing.T) {
			listener, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			defer listener.Close()
			accepted := make(chan string, 1)
			go func() {
				conn, e := listener.Accept()
				if e != nil {
					return
				}
				defer conn.Close()
				conn.SetDeadline(time.Now().Add(5 * time.Second))
				conn.Write([]byte("220 test\r\n"))
				reader := bufio.NewReader(conn)
				data := false
				var message strings.Builder
				for {
					line, e := reader.ReadString('\n')
					if e != nil {
						return
					}
					if data {
						if line == ".\r\n" {
							accepted <- message.String()
							conn.Write([]byte("250 accepted\r\n"))
							data = false
						} else {
							message.WriteString(line)
						}
						continue
					}
					switch {
					case strings.HasPrefix(line, "EHLO"):
						conn.Write([]byte("250-test\r\n250 OK\r\n"))
					case strings.HasPrefix(line, "DATA"):
						data = true
						conn.Write([]byte("354 send\r\n"))
					case strings.HasPrefix(line, "QUIT"):
						return
					default:
						conn.Write([]byte("250 OK\r\n"))
					}
				}
			}()
			host, port, _ := net.SplitHostPort(listener.Addr().String())
			n, _ := strconv.Atoi(port)
			target := domain.Target{Kind: "smtp", Credentials: domain.Credentials{SMTPHost: host, SMTPPort: n, From: "sender@example.test", Recipient: "receiver@example.test"}}
			sender := Sender{AllowedSMTPHosts: []string{host}, TestSMTPPlaintext: plaintext}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			err = sender.Send(ctx, target, "直播开始\n测试", "stable-task")
			if !plaintext {
				if err == nil {
					t.Fatal("plaintext production SMTP accepted")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			select {
			case message := <-accepted:
				if !strings.Contains(message, "<stable-task@live-monitor>") || !strings.Contains(message, "直播开始") {
					t.Fatal(message)
				}
			case <-ctx.Done():
				t.Fatal("no DATA acceptance")
			}
		})
	}
}

func TestSMTPRejectsUnapprovedHost(t *testing.T) {
	err := (Sender{}).Send(context.Background(), domain.Target{Kind: "smtp", Credentials: domain.Credentials{SMTPHost: "127.0.0.1"}}, "test", "task")
	if err == nil {
		t.Fatal("unapproved SMTP host accepted")
	}
}
