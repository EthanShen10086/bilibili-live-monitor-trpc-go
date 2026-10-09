//go:build eventintegration

package bus

import (
	"context"
	"errors"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/twmb/franz-go/pkg/kgo"

	"github.com/EthanShen10086/bilibili-live-monitor-trpc-go/internal/eventplatform/domain"
	"github.com/EthanShen10086/bilibili-live-monitor-trpc-go/internal/monitor"
)

func integrationConfig(t *testing.T) Config {
	t.Helper()
	brokers := os.Getenv("EVENT_TEST_KAFKA")
	if brokers == "" {
		t.Fatal("EVENT_TEST_KAFKA must name disposable brokers")
	}
	return Config{Brokers: strings.Split(brokers, ","), Topic: "live.events.v1", Group: "contract-" + monitor.ID()}
}

func event(id string, room int64) domain.Event {
	return domain.Event{SpecVersion: "1.0", ID: id, Source: "/bilibili/rooms", Type: "live.started.v1", Time: time.Now(), Data: domain.EventData{SchemaVersion: 1, RequestedRoom: room, SessionKey: id}}
}

func TestKafkaPublicationCrashRecovery(t *testing.T) {
	c := integrationConfig(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	producer, e := Open(ctx, Config{Brokers: c.Brokers, Topic: c.Topic})
	if e != nil {
		t.Fatal(e)
	}
	defer producer.Close()
	record := event(monitor.ID(), 9191)
	if e = producer.Publish(ctx, record); e != nil {
		t.Fatal(e)
	}
	first, e := Open(ctx, c)
	if e != nil {
		t.Fatal(e)
	}
	effects := 0
	attempts := 0
	err := first.Consume(ctx, func(_ context.Context, got domain.Event) error {
		if got.ID != record.ID {
			return nil
		}
		attempts++
		effects++
		// Durable effect already happened; simulate loss of broker connectivity before offset commit.
		first.Client.AllowRebalance()
		first.Client.Close()
		return nil
	}, func(context.Context, string) error { return nil })
	if err == nil {
		t.Fatal("offset loss was hidden")
	}
	second, e := Open(ctx, c)
	if e != nil {
		t.Fatal(e)
	}
	defer second.Close()
	child, stop := context.WithCancel(ctx)
	defer stop()
	recovered := make(chan struct{}, 1)
	done := make(chan error, 1)
	go func() {
		done <- second.Consume(child, func(_ context.Context, got domain.Event) error {
			if got.ID == record.ID {
				attempts++
				recovered <- struct{}{} /* persisted idempotency marker prevents a second effect */
			}
			return nil
		}, func(context.Context, string) error { return nil })
	}()
	select {
	case <-recovered:
	case <-ctx.Done():
		t.Fatal("uncommitted event was lost")
	}
	// Let the recovery commit complete before ending the poll loop.
	time.Sleep(time.Second)
	stop()
	<-done
	if effects != 1 || attempts != 2 {
		t.Fatalf("effects=%d attempts=%d", effects, attempts)
	}
}

func TestKafkaDuplicatesPoisonAndRebalance(t *testing.T) {
	c := integrationConfig(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	producer, e := Open(ctx, Config{Brokers: c.Brokers, Topic: c.Topic})
	if e != nil {
		t.Fatal(e)
	}
	defer producer.Close()
	records := map[string]bool{}
	for i := int64(1); i <= 12; i++ {
		records[monitor.ID()] = false
	}
	ids := make([]string, 0, len(records))
	for id := range records {
		ids = append(ids, id)
	}
	var mu sync.Mutex
	attempts := 0
	poisons := map[string]bool{}
	failedOnce := false
	handle := func(_ context.Context, got domain.Event) error {
		mu.Lock()
		defer mu.Unlock()
		if _, wanted := records[got.ID]; !wanted {
			return nil
		}
		attempts++
		if !failedOnce {
			failedOnce = true
			return errors.New("transient database failure")
		}
		records[got.ID] = true
		return nil
	}
	poison := func(_ context.Context, id string) error { mu.Lock(); defer mu.Unlock(); poisons[id] = true; return nil }
	done := make(chan error, 2)
	for range 2 {
		consumer, e := Open(ctx, c)
		if e != nil {
			t.Fatal(e)
		}
		defer consumer.Close()
		go func() { done <- consumer.Consume(ctx, handle, poison) }()
	}
	room := int64(5000)
	for _, id := range ids {
		room++
		for range 2 {
			if e = producer.Publish(ctx, event(id, room)); e != nil {
				t.Fatal(e)
			}
		}
	}
	if e = producer.Client.ProduceSync(ctx, &kgo.Record{Topic: c.Topic, Key: []byte("poison"), Value: []byte(`{"specversion":"unsupported"}`)}).FirstErr(); e != nil {
		t.Fatal(e)
	}
	complete := false
	for ctx.Err() == nil {
		mu.Lock()
		complete = true
		for _, seen := range records {
			complete = complete && seen
		}
		complete = complete && len(poisons) > 0
		mu.Unlock()
		if complete {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if !complete {
		t.Fatal("rebalance/duplicate/poison recovery failed")
	}
	cancel()
	for range 2 {
		<-done
	}
	mu.Lock()
	defer mu.Unlock()
	if attempts < 13 {
		t.Fatal("transient handler was not retried")
	}
}
