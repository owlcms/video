package access

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestViewerTokensAndSessions(t *testing.T) {
	dir := t.TempDir()
	s := Open(dir)
	if state, _ := s.ViewerState(); state != StateOpen {
		t.Fatal("new installation must be open")
	}
	a, av, err := s.AddViewer(" RAI ")
	if err != nil {
		t.Fatal(err)
	}
	if len(a) != 12 || strings.Trim(a, alphabet) != "" || av.Name != "RAI" {
		t.Fatalf("unexpected generated token or name: %q, %q", a, av.Name)
	}
	if life := av.ExpiresAt.Sub(av.CreatedAt); life != 21*24*time.Hour {
		t.Fatalf("token lifetime %s, want 21 days", life)
	}
	b, bv, err := s.AddViewer("OBS")
	if err != nil {
		t.Fatal(err)
	}
	if a == b {
		t.Fatal("duplicate tokens")
	}
	if _, _, err := s.AddViewer("rai"); err == nil {
		t.Fatal("duplicate name accepted")
	}
	for _, invalid := range []string{"", "?", strings.Repeat("x", 41)} {
		if _, _, err := s.AddViewer(invalid); err == nil {
			t.Fatalf("invalid name %q accepted", invalid)
		}
	}
	cookie, expiry, err := s.IssueSession(av.ID)
	if err != nil || !expiry.Equal(av.ExpiresAt) {
		t.Fatalf("issue session: %v", err)
	}
	if got, decision := s.VerifySession(cookie); decision != Allowed || got.ID != av.ID {
		t.Fatal("valid session refused")
	}
	if _, decision := s.VerifySession(cookie + "x"); decision != Invalid {
		t.Fatal("tampered session accepted")
	}
	if _, decision := s.VerifyViewer(strings.Repeat("a", 129)); decision != Invalid {
		t.Fatal("oversized token accepted")
	}
	reopened := Open(dir)
	if got, decision := reopened.VerifyViewer(a); decision != Allowed || got.ID != av.ID {
		t.Fatal("token did not survive reopen")
	}
	if _, decision := reopened.VerifySession(cookie); decision != Allowed {
		t.Fatal("session did not survive reopen")
	}
	raw, err := os.ReadFile(filepath.Join(dir, filename))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), a) || strings.Contains(string(raw), b) {
		t.Fatal("file contains a token value")
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(filepath.Join(dir, filename))
		if err != nil || info.Mode().Perm() != 0600 {
			t.Fatalf("file permissions: %v %v", info, err)
		}
	}
	if err := s.RevokeViewer(av.ID); err != nil {
		t.Fatal(err)
	}
	if _, decision := s.VerifyViewer(a); decision != Invalid {
		t.Fatal("revoked token accepted")
	}
	if _, decision := s.VerifySession(cookie); decision != Invalid {
		t.Fatal("revoked session accepted")
	}
	if _, decision := s.VerifyViewer(b); decision != Allowed {
		t.Fatal("other token affected by revocation")
	}
	replacement, replacementViewer, err := s.RegenerateViewer(bv.ID)
	if err != nil || replacement == b || replacementViewer.ID == bv.ID || replacementViewer.Name != bv.Name {
		t.Fatalf("regeneration: %v", err)
	}
	if err := s.RevokeViewer(replacementViewer.ID); err != nil {
		t.Fatal(err)
	}
	if state, _ := s.ViewerState(); state != StateOpen {
		t.Fatal("revoking the last token must restore open access")
	}
	if _, decision := s.VerifyViewer(""); decision != Allowed {
		t.Fatal("open mode requires credentials")
	}
}

func TestExpiryAndRecovery(t *testing.T) {
	dir := t.TempDir()
	s := Open(dir)
	now := time.Date(2026, 11, 6, 0, 0, 0, 0, time.UTC)
	s.now = func() time.Time { return now }
	token, viewer, err := s.AddViewer("expired")
	if err != nil {
		t.Fatal(err)
	}
	cookie, _, err := s.IssueSession(viewer.ID)
	if err != nil {
		t.Fatal(err)
	}
	now = viewer.ExpiresAt
	if _, decision := s.VerifyViewer(token); decision != ExpiredToken {
		t.Fatal("token valid at expiry")
	}
	if _, decision := s.VerifySession(cookie); decision != ExpiredToken {
		t.Fatal("session valid at expiry")
	}
	if state, _ := s.ViewerState(); state != StateProtected {
		t.Fatal("expired token must not open access")
	}
	if s.timer != nil {
		s.timer.Stop()
	}
	if err := os.WriteFile(filepath.Join(dir, filename), []byte("{"), 0600); err != nil {
		t.Fatal(err)
	}
	s = Open(dir)
	if _, decision := s.VerifyViewer(token); decision != ServiceUnavailable {
		t.Fatal("corrupt file did not fail closed")
	}
	if err := s.RevokeViewer(viewer.ID); err == nil {
		t.Fatal("revoke accepted on corrupt store")
	}
	if _, _, err := s.AddViewer("recovery"); err != nil {
		t.Fatal(err)
	}
}

func TestUnknownFieldsAndFailedWrites(t *testing.T) {
	dir := t.TempDir()
	s := Open(dir)
	token, _, err := s.AddViewer("one")
	if err != nil {
		t.Fatal(err)
	}

	path := filepath.Join(dir, filename)
	raw, _ := os.ReadFile(path)
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		t.Fatal(err)
	}
	fields["future"] = json.RawMessage(`{"enabled":true}`)
	raw, _ = json.Marshal(fields)
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	s = Open(dir)
	if _, _, err := s.AddViewer("two"); err != nil {
		t.Fatal(err)
	}
	raw, _ = os.ReadFile(path)
	if !strings.Contains(string(raw), `"future"`) {
		t.Fatal("unknown field lost")
	}
	s.dir = filepath.Join(dir, "missing")
	if value, _, err := s.AddViewer("three"); err == nil || value != "" {
		t.Fatal("failed write must return an error and no token")
	}
	if _, decision := s.VerifyViewer(token); decision != Allowed {
		t.Fatal("failed write changed in-memory state")
	}
}

func TestViewerLimitAndExpiryNotification(t *testing.T) {
	s := Open(t.TempDir())
	for i := 0; i < 50; i++ {
		if _, _, err := s.AddViewer(fmt.Sprintf("viewer-%d", i)); err != nil {
			t.Fatal(err)
		}
	}
	if _, _, err := s.AddViewer("one-too-many"); err == nil {
		t.Fatal("51st token accepted")
	}
	fired := make(chan struct{}, 1)
	s.OnChange(func() { fired <- struct{}{} })
	s.mu.Lock()
	s.data.Viewer.Tokens[0].ExpiresAt = time.Now().Add(40 * time.Millisecond)
	s.rescheduleLocked()
	s.mu.Unlock()
	select {
	case <-fired:
	case <-time.After(2 * time.Second):
		t.Fatal("expiry notification did not fire")
	}
	s.mu.Lock()
	if s.timer != nil {
		s.timer.Stop()
	}
	s.mu.Unlock()
}

func TestMissingSecurityDirectoryIsUnavailable(t *testing.T) {
	s := Open(filepath.Join(t.TempDir(), "missing"))
	if state, _ := s.ViewerState(); state != StateUnavailable {
		t.Fatal("missing security directory must fail closed")
	}
}
