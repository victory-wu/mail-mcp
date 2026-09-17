package config

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestAccountJSONRoundTrip(t *testing.T) {
	cfg := loadOK(t, minimal+"    save_sent: false\n    from_address: alias@example.com\n    from_name: Test User\n")
	cfg.Accounts[0].Hostname = "client-host"
	cfg.Accounts[0].ID = "client-host-me@example.com-550e8400-e29b-41d4-a716-446655440000"
	data, err := json.Marshal(cfg.Accounts[0])
	if err != nil {
		t.Fatal(err)
	}
	raw := string(data)
	values := map[string]string{cfg.Accounts[0].ID: raw}
	if !strings.Contains(raw, `"from_address":"alias@example.com"`) || !strings.Contains(raw, `"save_sent":false`) {
		t.Fatalf("JSON must retain snake_case fields and explicit false values")
	}
	accounts, err := decodeAccounts(values)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := json.Marshal(accounts[0])
	want, _ := json.Marshal(cfg.Accounts[0])
	if string(got) != string(want) {
		t.Fatal("account configuration changed during JSON round trip")
	}
}

func TestRedisAccountValidation(t *testing.T) {
	for name, values := range map[string]map[string]string{
		"malformed":        {"me@example.com": `{"password":"secret"`},
		"null":             {"me@example.com": `null`},
		"array":            {"me@example.com": `[]`},
		"mismatched login": {"other@example.com": `{"imap":{"username":"me@example.com"}}`},
		"missing login":    {"me@example.com": `{}`},
		"display name":     {"Me <me@example.com>": `{"imap":{"username":"Me <me@example.com>"}}`},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := decodeAccounts(values)
			if err == nil {
				t.Fatal("expected rejection")
			}
			if strings.Contains(err.Error(), "secret") {
				t.Fatal("error exposed credentials")
			}
		})
	}
}

func TestRedisStableIDsAndOrder(t *testing.T) {
	aKey := "client-a@example.com-550e8400-e29b-41d4-a716-446655440000"
	zKey := "client-z@example.com-550e8400-e29b-41d4-a716-446655440000"
	values := map[string]string{
		zKey: `{"hostname":"client","imap":{"host":"imap.example.com","username":"z@example.com","password":"p"}}`,
		aKey: `{"hostname":"client","imap":{"host":"imap.example.com","username":"a@example.com","password":"p"}}`,
	}
	accounts, err := decodeAccounts(values)
	if err != nil {
		t.Fatal(err)
	}
	if accounts[0].ID != aKey || accounts[1].ID != zKey {
		t.Fatal("unstable account IDs or ordering")
	}
	delete(values, aKey)
	accounts, err = decodeAccounts(values)
	if err != nil || accounts[0].ID != zKey {
		t.Fatal("removing another account changed its ID")
	}
}

func TestEmptyAccountStoreCanStart(t *testing.T) {
	accounts, err := decodeAccounts(map[string]string{})
	if err != nil || len(accounts) != 0 {
		t.Fatalf("empty store: %v", err)
	}
	if _, err := loadConfig(write(t, "limits: {}"), false); err != nil {
		t.Fatal(err)
	}
}

func TestLoadRequiresRedis(t *testing.T) {
	if _, err := Load(write(t, minimal)); err == nil || !strings.Contains(err.Error(), "configure accounts in Redis") {
		t.Fatalf("expected Redis configuration guidance: %v", err)
	}
	if _, err := Load(write(t, "limits: {}")); err == nil || !strings.Contains(err.Error(), "redis.addr") {
		t.Fatalf("expected required redis address: %v", err)
	}
	for _, cfg := range []RedisConfig{{Addr: "localhost:6379", DB: -1}, {Addr: "localhost:6379", Timeout: "-1s"}, {Addr: "localhost:6379", Timeout: "bad"}} {
		if client, _, err := cfg.client(); err == nil {
			client.Close()
			t.Fatal("expected invalid Redis config rejection")
		}
	}
}

func TestNullAccountRejected(t *testing.T) {
	if _, err := loadConfig(write(t, "accounts: [null]"), false); err == nil {
		t.Fatal("expected null account rejection")
	}
}
