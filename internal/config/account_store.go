package config

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
)

var ErrInvalidAccount = errors.New("invalid account configuration")

// CreateAccountInput supplies the caller's hostname and login email separately so
// the caller never needs to construct or choose an account's persistent ID.
type CreateAccountInput struct {
	Hostname    string   `json:"hostname" validate:"required"`
	Email       string   `json:"email" validate:"required"`
	IMAP        Endpoint `json:"imap" validate:"required"`
	SMTP        Endpoint `json:"smtp"`
	FromAddress string   `json:"from_address,omitempty"`
	FromName    string   `json:"from_name,omitempty"`
	SaveSent    *bool    `json:"save_sent,omitempty"`
}

type AccountRecord struct {
	Subkey  string   `json:"subkey"`
	Account *Account `json:"account"`
}

// RedisAccountStore reads the current Redis state for each management request.
type RedisAccountStore struct {
	client  redis.Cmdable
	close   func() error
	timeout time.Duration
}

func (r RedisConfig) AccountStore() (*RedisAccountStore, error) {
	client, timeout, err := r.client()
	if err != nil {
		return nil, err
	}
	return &RedisAccountStore{client: client, close: client.Close, timeout: timeout}, nil
}

func (s *RedisAccountStore) Close() error { return s.close() }

func prepareAccount(in CreateAccountInput) (*Account, error) {
	host := strings.TrimSpace(in.Hostname)
	email := strings.TrimSpace(in.Email)
	if host == "" || strings.ContainsAny(host, " /\\\t\r\n?#@") {
		return nil, fmt.Errorf("%w: hostname must be a non-empty caller host identifier without whitespace or URL delimiters", ErrInvalidAccount)
	}
	if in.IMAP.Username != "" && in.IMAP.Username != email {
		return nil, fmt.Errorf("%w: imap.username must equal email when supplied", ErrInvalidAccount)
	}
	identifier, err := uuid.NewRandom()
	if err != nil {
		return nil, fmt.Errorf("generate account UUID: %w", err)
	}
	a := &Account{ID: host + "-" + email + "-" + identifier.String(), Hostname: host, IMAP: in.IMAP, SMTP: in.SMTP,
		FromAddress: in.FromAddress, FromName: in.FromName, SaveSent: in.SaveSent}
	a.IMAP.Username = email
	if _, err := accountField(a); err != nil {
		return nil, fmt.Errorf("%w: email must be the actual login email address", ErrInvalidAccount)
	}
	if err := a.normalize(0); err != nil {
		return nil, fmt.Errorf("%w: %s", ErrInvalidAccount, err)
	}
	if err := (&Config{Accounts: []*Account{a}}).validate(); err != nil {
		return nil, fmt.Errorf("%w: %s", ErrInvalidAccount, err)
	}
	return a, nil
}

func validateSubkey(field string, a *Account) error {
	prefix := a.Hostname + "-" + a.IMAP.Username + "-"
	if a.Hostname == "" || !strings.HasPrefix(field, prefix) {
		return fmt.Errorf("account subkey must be hostname-email-uuid matching hostname and imap.username")
	}
	suffix := strings.TrimPrefix(field, prefix)
	identifier, err := uuid.Parse(suffix)
	if err != nil || identifier.String() != suffix || identifier.Version() != 4 || identifier.Variant() != uuid.RFC4122 {
		return fmt.Errorf("account subkey must end with a canonical UUID v4")
	}
	return nil
}

func (s *RedisAccountStore) Create(ctx context.Context, in CreateAccountInput) (string, error) {
	a, err := prepareAccount(in)
	if err != nil {
		return "", err
	}
	data, err := json.Marshal(a)
	if err != nil {
		return "", fmt.Errorf("encode account: %w", err)
	}
	ctx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()
	created, err := s.client.HSetNX(ctx, AccountsKey, a.ID, string(data)).Result()
	if err != nil {
		return "", fmt.Errorf("create Redis account: %w", err)
	}
	if !created {
		return "", fmt.Errorf("generated account subkey already exists; retry creation")
	}
	return a.ID, nil
}

func (s *RedisAccountStore) Find(ctx context.Context, prefix string) ([]AccountRecord, error) {
	prefix = strings.TrimSpace(prefix)
	if prefix == "" {
		return nil, fmt.Errorf("%w: prefix is required (hostname-email)", ErrInvalidAccount)
	}
	ctx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()
	values, err := s.client.HGetAll(ctx, AccountsKey).Result()
	if err != nil {
		return nil, fmt.Errorf("query Redis accounts: %w", err)
	}
	accounts, err := decodeAccounts(values)
	if err != nil {
		return nil, err
	}
	result := make([]AccountRecord, 0)
	for _, a := range accounts {
		if strings.HasPrefix(a.ID, prefix) {
			result = append(result, AccountRecord{Subkey: a.ID, Account: a})
		}
	}
	return result, nil
}

func (s *RedisAccountStore) Delete(ctx context.Context, subkey string) (bool, error) {
	if strings.TrimSpace(subkey) == "" {
		return false, fmt.Errorf("%w: subkey is required", ErrInvalidAccount)
	}
	ctx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()
	count, err := s.client.HDel(ctx, AccountsKey, subkey).Result()
	if err != nil {
		return false, fmt.Errorf("delete Redis account: %w", err)
	}
	return count > 0, nil
}
