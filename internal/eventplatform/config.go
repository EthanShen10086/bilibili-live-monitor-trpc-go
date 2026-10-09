// Package eventplatform composes module contracts into independent platform roles.
package eventplatform

import (
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"strconv"
	"strings"

	"github.com/EthanShen10086/bilibili-live-monitor-trpc-go/internal/eventplatform/bus"
	"github.com/EthanShen10086/bilibili-live-monitor-trpc-go/internal/eventplatform/secrets"
)

type Config struct {
	DSN              string
	Issuer           string
	Audience         string
	AdminSubjects    []string
	Vault            *secrets.Vault
	Kafka            bus.Config
	SMTPHosts        []string
	RetentionDays    int
	Tracing          bool
	TraceSampleRatio float64
}

func LoadConfig() (Config, error) {
	c := Config{DSN: os.Getenv("EVENT_POSTGRES_DSN"), Issuer: os.Getenv("EVENT_OIDC_ISSUER"), Audience: os.Getenv("EVENT_OIDC_AUDIENCE"), AdminSubjects: split(os.Getenv("EVENT_ADMIN_SUBJECTS")), SMTPHosts: split(os.Getenv("EVENT_SMTP_ALLOWED_HOSTS")), RetentionDays: 90, TraceSampleRatio: .1, Tracing: os.Getenv("EVENT_TRACING") == "true"}
	if c.DSN == "" {
		return c, errors.New("EVENT_POSTGRES_DSN required")
	}
	keys := map[string]string{}
	if e := json.Unmarshal([]byte(os.Getenv("EVENT_CREDENTIAL_KEYS")), &keys); e != nil {
		return c, errors.New("EVENT_CREDENTIAL_KEYS must contain base64 encryption keys")
	}
	var e error
	c.Vault, e = secrets.New(os.Getenv("EVENT_CREDENTIAL_KEY_ID"), keys)
	if e != nil {
		return c, e
	}
	c.Kafka = bus.Config{Brokers: split(os.Getenv("EVENT_KAFKA_BROKERS")), Topic: os.Getenv("EVENT_KAFKA_TOPIC"), TLS: os.Getenv("EVENT_KAFKA_TLS") == "true", Username: os.Getenv("EVENT_KAFKA_USERNAME"), Password: os.Getenv("EVENT_KAFKA_PASSWORD")}
	if c.Kafka.Topic == "" {
		c.Kafka.Topic = "live.events.v1"
	}
	if v := os.Getenv("EVENT_RETENTION_DAYS"); v != "" {
		c.RetentionDays, e = strconv.Atoi(v)
		if e != nil || c.RetentionDays < 1 || c.RetentionDays > 3650 {
			return c, errors.New("invalid EVENT_RETENTION_DAYS")
		}
	}
	if c.Issuer != "" {
		issuer, err := url.Parse(c.Issuer)
		if err != nil || issuer.Scheme != "https" || issuer.Host == "" || issuer.User != nil || issuer.RawQuery != "" || issuer.Fragment != "" {
			return c, errors.New("EVENT_OIDC_ISSUER must be an HTTPS issuer")
		}
	}
	if value := os.Getenv("EVENT_TRACE_SAMPLE_RATIO"); value != "" {
		c.TraceSampleRatio, e = strconv.ParseFloat(value, 64)
		if e != nil || c.TraceSampleRatio < 0 || c.TraceSampleRatio > 1 {
			return c, errors.New("invalid trace sample ratio")
		}
	}
	return c, nil
}

func split(value string) []string {
	out := []string{}
	for _, v := range strings.Split(value, ",") {
		if v = strings.TrimSpace(v); v != "" {
			out = append(out, v)
		}
	}
	return out
}
