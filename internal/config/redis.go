package config

import (
	"context"
	"encoding/json"
	"fmt"
	"net/mail"
	"sort"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
)

const AccountsKey = "mcp_accounts"

// RedisConfig locates the server-side account store.
type RedisConfig struct {
	Addr     string `yaml:"addr"`
	Username string `yaml:"username"`
	Password string `yaml:"password"`
	DB       int    `yaml:"db"`
	Timeout  string `yaml:"timeout"`
}

func (r RedisConfig) client() (*redis.Client, time.Duration, error) {
	if strings.TrimSpace(r.Addr) == "" {
		return nil, 0, fmt.Errorf("redis.addr is required; configure Redis containing the %s hash", AccountsKey)
	}
	if r.DB < 0 {
		return nil, 0, fmt.Errorf("redis.db must be non-negative")
	}
	timeout, err := parseDuration(r.Timeout, 10*time.Second)
	if err != nil {
		return nil, 0, fmt.Errorf("redis.timeout: %w", err)
	}
	return redis.NewClient(&redis.Options{
		Addr: r.Addr, Username: r.Username, Password: r.Password, DB: r.DB,
		DialTimeout: timeout, ReadTimeout: timeout, WriteTimeout: timeout,
		ContextTimeoutEnabled: true, MaxRetries: -1,
	}), timeout, nil
}

func (r RedisConfig) loadAccounts() ([]*Account, error) {
	client, timeout, err := r.client()
	if err != nil {
		return nil, err
	}
	defer client.Close()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	values, err := client.HGetAll(ctx, AccountsKey).Result()
	if err != nil {
		return nil, fmt.Errorf("read Redis %s: %w", AccountsKey, err)
	}
	return decodeAccounts(values)
}

func accountField(a *Account) (string, error) {
	field := a.IMAP.Username
	address, err := mail.ParseAddress(field)
	if err != nil || address.Address != field || !strings.Contains(field, "@") {
		return "", fmt.Errorf("account %q: imap.username must be the actual login email address", a.ID)
	}
	return field, nil
}

func decodeAccounts(values map[string]string) ([]*Account, error) {
	fields := make([]string, 0, len(values))
	for field := range values {
		fields = append(fields, field)
	}
	sort.Strings(fields)
	accounts := make([]*Account, 0, len(fields))
	for _, field := range fields {
		var a *Account
		if err := json.Unmarshal([]byte(values[field]), &a); err != nil || a == nil {
			// Decoder errors can contain fragments of credentials; do not echo values.
			return nil, fmt.Errorf("Redis %s field %q: value must be an account JSON object", AccountsKey, field)
		}
		if a.ID != "" && a.ID != field {
			return nil, fmt.Errorf("Redis %s field %q: account id must equal subkey", AccountsKey, field)
		}
		a.ID = field
		if err := a.normalize(0); err != nil {
			return nil, err
		}
		_, err := accountField(a)
		if err != nil {
			return nil, err
		}
		if err := validateSubkey(field, a); err != nil {
			return nil, fmt.Errorf("Redis %s field %q: %w", AccountsKey, field, err)
		}
		if err := (&Config{Accounts: []*Account{a}}).validate(); err != nil {
			return nil, err
		}
		accounts = append(accounts, a)
	}
	return accounts, nil
}
