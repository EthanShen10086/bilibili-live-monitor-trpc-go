package eventplatform

import (
	"context"
	"net"
	"net/http"
	"testing"
	"time"
)

func TestWorkerHealthCancellationBeforeServe(t *testing.T) {
	for range 100 {
		listener, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		address := listener.Addr().String()
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		done := make(chan error, 1)
		go func() { done <- serveWorkerHealth(ctx, listener, http.NewServeMux()) }()
		select {
		case err := <-done:
			if err != nil {
				t.Fatal(err)
			}
		case <-time.After(time.Second):
			t.Fatal("cancelled health service prevented worker exit")
		}
		conn, err := net.DialTimeout("tcp", address, time.Second)
		if err == nil {
			_ = conn.Close()
			t.Fatal("health listener survived worker shutdown")
		}
	}
}
