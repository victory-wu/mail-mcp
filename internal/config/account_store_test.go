package config

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
)

type memoryRedis struct {
	redis.Cmdable
	values map[string]string
	err    error
	t      *testing.T
}

func (m *memoryRedis) check(ctx context.Context, key string) {
	m.t.Helper()
	if key != AccountsKey {
		m.t.Fatalf("unexpected Redis key %q", key)
	}
	if _, ok := ctx.Deadline(); !ok {
		m.t.Fatal("Redis operation has no deadline")
	}
}

func (m *memoryRedis) HSetNX(ctx context.Context, key, field string, value interface{}) *redis.BoolCmd {
	m.check(ctx, key)
	_, exists := m.values[field]
	if !exists && m.err == nil {
		m.values[field] = value.(string)
	}
	return redis.NewBoolResult(!exists, m.err)
}

func (m *memoryRedis) HGetAll(ctx context.Context, key string) *redis.MapStringStringCmd {
	m.check(ctx, key)
	return redis.NewMapStringStringResult(m.values, m.err)
}

func (m *memoryRedis) HDel(ctx context.Context, key string, fields ...string) *redis.IntCmd {
	m.check(ctx, key)
	var count int64
	for _, field := range fields {
		if _, ok := m.values[field]; ok && m.err == nil {
			delete(m.values, field)
			count++
		}
	}
	return redis.NewIntResult(count, m.err)
}

func validCreateInput() CreateAccountInput {
	return CreateAccountInput{Hostname: "caller-host", Email: "alice@example.com", IMAP: Endpoint{Host: "imap.example.com", Password: "secret"}, SMTP: Endpoint{Host: "smtp.example.com"}}
}

func TestAccountStoreLifecycle(t *testing.T) {
	m := &memoryRedis{values: map[string]string{}, t: t}
	s := &RedisAccountStore{client: m, timeout: time.Second}
	ctx := context.Background()
	in := validCreateInput()
	deny := false
	in.AllowSend = &deny
	first, err := s.Create(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	second, err := s.Create(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	if first == second || !strings.HasPrefix(first, "caller-host-alice@example.com-") {
		t.Fatal("subkeys must have unique UUID suffixes")
	}
	in.Hostname = "other-host"
	if _, err := s.Create(ctx, in); err != nil {
		t.Fatal(err)
	}
	results, err := s.Find(ctx, "caller-host-alice@example.com")
	if err != nil || len(results) != 2 {
		t.Fatalf("prefix lookup: %d %v", len(results), err)
	}
	for _, record := range results {
		if record.Account.IMAP.Host != "imap.example.com" || record.Account.Hostname != "caller-host" {
			t.Fatal("caller hostname must be independent of mail host")
		}
		if record.Account.AllowSend == nil || *record.Account.AllowSend {
			t.Fatal("explicit false gate was lost")
		}
		if record.Subkey != record.Account.ID {
			t.Fatal("subkey and ID diverged")
		}
	}
	if matches, err := s.Find(ctx, "caller-host-*"); err != nil || len(matches) != 0 {
		t.Fatal("prefix must be literal, not a Redis glob")
	}
	if deleted, err := s.Delete(ctx, first); err != nil || !deleted {
		t.Fatalf("delete: %v %v", deleted, err)
	}
	if deleted, err := s.Delete(ctx, first); err != nil || deleted {
		t.Fatal("missing deletion should return false")
	}
	if _, ok := m.values[second]; !ok {
		t.Fatal("delete affected another account")
	}
	if _, err := s.Find(ctx, ""); !errors.Is(err, ErrInvalidAccount) {
		t.Fatal("empty prefix accepted")
	}
	if _, err := s.Delete(ctx, ""); !errors.Is(err, ErrInvalidAccount) {
		t.Fatal("empty subkey accepted")
	}
	m.err = errors.New("redis unavailable")
	if _, err := s.Create(ctx, in); err == nil {
		t.Fatal("lost create failure")
	}
	if _, err := s.Find(ctx, "caller"); err == nil {
		t.Fatal("lost query failure")
	}
	if _, err := s.Delete(ctx, second); err == nil {
		t.Fatal("lost delete failure")
	}
}

func TestCreateValidationBeforeRedis(t *testing.T) {
	for name, edit := range map[string]func(*CreateAccountInput){
		"missing caller hostname": func(in *CreateAccountInput) { in.Hostname = "" },
		"invalid email":           func(in *CreateAccountInput) { in.Email = "not-email" },
		"mismatched login":        func(in *CreateAccountInput) { in.IMAP.Username = "other@example.com" },
		"missing mail host":       func(in *CreateAccountInput) { in.IMAP.Host = "" },
		"missing password":        func(in *CreateAccountInput) { in.IMAP.Password = "" },
		"bad port":                func(in *CreateAccountInput) { in.IMAP.Port = -1 },
		"bad security":            func(in *CreateAccountInput) { in.SMTP.Security = "bad" },
	} {
		t.Run(name, func(t *testing.T) {
			in := validCreateInput()
			edit(&in)
			s := &RedisAccountStore{} // Any Redis call before validation would panic.
			if _, err := s.Create(context.Background(), in); !errors.Is(err, ErrInvalidAccount) {
				t.Fatalf("invalid input: %v", err)
			}
		})
	}
}

func TestRejectMismatchedSubkey(t *testing.T) {
	a, err := prepareAccount(validCreateInput())
	if err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(a)
	for _, key := range []string{"alice@example.com", a.ID + "junk", "other" + a.ID} {
		if _, err := decodeAccounts(map[string]string{key: string(data)}); err == nil {
			t.Fatal("mismatched subkey accepted")
		}
	}
	a.ID = ""
	data, _ = json.Marshal(a)
	if _, err := decodeAccounts(map[string]string{"caller-host-alice@example.com-not-a-uuid": string(data)}); err == nil {
		t.Fatal("invalid UUID accepted")
	}
}
