// Package bus transports versioned events; durable business effects precede offset commits.
package bus

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/twmb/franz-go/pkg/kgo"
	"github.com/twmb/franz-go/pkg/sasl/plain"

	"github.com/EthanShen10086/bilibili-live-monitor-trpc-go/internal/eventplatform/domain"
)

type Config struct {
	Brokers  []string
	Topic    string
	Group    string
	TLS      bool
	Username string
	Password string
}
type Kafka struct {
	Client *kgo.Client
	Topic  string
}

func Open(ctx context.Context, c Config) (*Kafka, error) {
	if len(c.Brokers) == 0 || c.Topic == "" {
		return nil, errors.New("Kafka brokers and topic required")
	}
	options := []kgo.Opt{kgo.SeedBrokers(c.Brokers...), kgo.RequiredAcks(kgo.AllISRAcks()), kgo.ProducerBatchMaxBytes(1 << 20), kgo.RecordDeliveryTimeout(10 * time.Second), kgo.ProducerLinger(5 * time.Millisecond)}
	if c.TLS {
		options = append(options, kgo.DialTLSConfig(&tls.Config{MinVersion: tls.VersionTLS12}))
	}
	if c.Username != "" {
		if !c.TLS {
			return nil, errors.New("Kafka SASL credentials require TLS")
		}
		options = append(options, kgo.SASL(plain.Auth{User: c.Username, Pass: c.Password}.AsMechanism()))
	}
	if c.Group != "" {
		options = append(options, kgo.ConsumerGroup(c.Group), kgo.ConsumeTopics(c.Topic), kgo.DisableAutoCommit(), kgo.BlockRebalanceOnPoll(), kgo.FetchMaxBytes(1<<20))
	}
	client, e := kgo.NewClient(options...)
	if e != nil {
		return nil, e
	}
	if e = client.Ping(ctx); e != nil {
		client.Close()
		return nil, errors.New("Kafka unavailable")
	}
	return &Kafka{client, c.Topic}, nil
}
func (k *Kafka) Close() error { k.Client.AllowRebalance(); k.Client.Close(); return nil }
func (k *Kafka) Publish(ctx context.Context, event domain.Event) error {
	if e := event.Validate(); e != nil {
		return e
	}
	raw, e := json.Marshal(event)
	if e != nil {
		return e
	}
	return k.Client.ProduceSync(ctx, &kgo.Record{Topic: k.Topic, Key: []byte(fmt.Sprint(event.Data.RequestedRoom)), Value: raw}).FirstErr()
}

// Consume processes one record at a time. No offset can advance past an unpersisted record.
func (k *Kafka) Consume(ctx context.Context, handle func(context.Context, domain.Event) error, poison func(context.Context, string) error) error {
	defer k.Client.AllowRebalance()
	for ctx.Err() == nil {
		fetch := k.Client.PollRecords(ctx, 1)
		if ctx.Err() != nil {
			return nil
		}
		if len(fetch.Errors()) > 0 {
			return errors.New("Kafka fetch unavailable")
		}
		for _, r := range fetch.Records() {
			var event domain.Event
			e := json.Unmarshal(r.Value, &event)
			valid := e == nil && event.Validate() == nil
			for {
				if valid {
					e = handle(ctx, event)
				} else {
					id := fmt.Sprintf("%x", sha256.Sum256([]byte(fmt.Sprintf("%s:%d:%d", r.Topic, r.Partition, r.Offset))))
					e = poison(ctx, id)
				}
				if e == nil {
					break
				}
				timer := time.NewTimer(time.Second)
				select {
				case <-ctx.Done():
					timer.Stop()
					return nil
				case <-timer.C:
				}
			}
			if e = k.Client.CommitRecords(ctx, r); e != nil {
				return errors.New("Kafka commit unavailable")
			}
		}
		k.Client.AllowRebalance()
	}
	return nil
}
