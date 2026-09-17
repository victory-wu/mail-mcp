package tools

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/kacperkwapisz/mail-mcp/internal/msgid"
)

func TestAccountAttachmentIsolation(t *testing.T) {
	cfg := loadTestConfig(t)
	a := NewForAccount(cfg, cfg.Accounts[0], nil, nil, "test", "secret")
	b := NewForAccount(cfg, cfg.Accounts[1], nil, nil, "test", "secret")
	if len(cfg.Accounts) != 2 {
		t.Fatal("scoping changed shared config")
	}
	if account, err := a.resolveAccount(); err != nil || account.ID != cfg.Accounts[0].ID {
		t.Fatalf("authenticated account did not resolve: %v", err)
	}
	owned := filepath.Join(cfg.Limits.AttachmentDir, a.attachmentPrefix+"file.txt")
	foreign := filepath.Join(cfg.Limits.AttachmentDir, b.attachmentPrefix+"file.txt")
	arbitrary := filepath.Join(cfg.Limits.AttachmentDir, "config.yml")
	for _, path := range []string{owned, foreign, arbitrary} {
		if err := os.WriteFile(path, []byte("attachment"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := a.loadAttachments([]AttachmentInput{{FilePath: owned}}); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{foreign, arbitrary, filepath.Join(t.TempDir(), a.attachmentPrefix+"file.txt")} {
		if _, err := a.loadAttachments([]AttachmentInput{{FilePath: path}}); err == nil {
			t.Fatalf("accepted unauthorized path %q", path)
		}
	}
	if _, err := a.loadAttachments([]AttachmentInput{{Filename: "text.txt", ContentBase64: "aGk="}}); err != nil {
		t.Fatal(err)
	}
	_, _, err := a.getAttachment(context.Background(), nil, getAttachmentInput{
		messageInput: messageInput{MessageID: msgid.Encode(cfg.Accounts[0].ID, "INBOX", 1, 1)},
		PartID:       "1", OutputDir: t.TempDir(),
	})
	if err == nil {
		t.Fatal("accepted custom HTTP output directory")
	}
}

func TestAccountAttachmentRejectsSymlink(t *testing.T) {
	cfg := loadTestConfig(t)
	s := NewForAccount(cfg, cfg.Accounts[0], nil, nil, "test", "secret")
	target := filepath.Join(t.TempDir(), s.attachmentPrefix+"target.txt")
	if err := os.WriteFile(target, []byte("private"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(cfg.Limits.AttachmentDir, s.attachmentPrefix+"link.txt")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if err := s.checkAttachmentPath(link); err == nil {
		t.Fatal("accepted symlink outside attachment directory")
	}
}

func TestMessageHandleAccountMatchingIsExact(t *testing.T) {
	cfg := loadTestConfig(t)
	s := NewForAccount(cfg, cfg.Accounts[0], nil, nil, "test", "secret")
	for _, id := range []string{"work", "PERSONAL", "me@example.com"} {
		if _, _, err := s.resolveMessage(msgid.Encode(id, "INBOX", 1, 1)); err == nil {
			t.Fatalf("accepted handle for %q", id)
		}
	}
	if _, acc, err := s.resolveMessage(msgid.Encode("personal", "INBOX", 1, 1)); err != nil || acc.ID != "personal" {
		t.Fatalf("rejected authenticated account handle: %v", err)
	}
}
