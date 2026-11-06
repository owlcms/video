// Package access manages viewer tokens and signed browser sessions.
package access

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
)

type State int

const (
	StateOpen State = iota
	StateProtected
	StateUnavailable
)

type Decision int

const (
	Allowed Decision = iota
	Missing
	Invalid
	ExpiredToken
	ServiceUnavailable
)

const (
	filename  = "video-access.json"
	tokenLife = 21 * 24 * time.Hour
	alphabet  = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_"
)

type Viewer struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	CreatedAt time.Time `json:"createdAt"`
	ExpiresAt time.Time `json:"expiresAt"`
}

type tokenRecord struct {
	Viewer
	Hash string `json:"hash"`
}

type viewerRecords struct {
	Tokens []tokenRecord `json:"tokens"`
}

type fileData struct {
	Version    int            `json:"version"`
	InstanceID string         `json:"instanceId"`
	SessionKey string         `json:"sessionKey"`
	Viewer     *viewerRecords `json:"viewer"`
}

type Service struct {
	mu        sync.Mutex
	dir       string
	data      fileData
	extra     map[string]json.RawMessage
	loadErr   error
	random    io.Reader
	now       func() time.Time
	timer     *time.Timer
	callbacks []func()
}

// Open loads the token file. An invalid or unreadable file fails closed.
func Open(dir string) *Service {
	s := &Service{dir: dir, random: rand.Reader, now: time.Now}
	info, err := os.Stat(dir)
	if err != nil {
		s.loadErr = fmt.Errorf("access directory: %w", err)
		return s
	}
	if !info.IsDir() {
		s.loadErr = errors.New("access directory is not a directory")
		return s
	}
	raw, err := os.ReadFile(filepath.Join(dir, filename))
	if errors.Is(err, os.ErrNotExist) {
		return s
	}
	if err != nil {
		s.loadErr = err
		return s
	}
	if err := json.Unmarshal(raw, &s.extra); err != nil || s.extra == nil {
		s.loadErr = fmt.Errorf("invalid access file: %v", err)
		return s
	}
	if err := json.Unmarshal(raw, &s.data); err != nil {
		s.loadErr = fmt.Errorf("invalid access file: %w", err)
		return s
	}
	if err := validate(s.data); err != nil {
		s.loadErr = err
		return s
	}
	s.rescheduleLocked()
	return s
}

func validate(d fileData) error {
	if d.Version != 1 {
		return errors.New("unsupported access file version")
	}
	id, err := hex.DecodeString(d.InstanceID)
	if err != nil || len(id) != 4 {
		return errors.New("invalid instance ID")
	}
	key, err := base64.StdEncoding.DecodeString(d.SessionKey)
	if err != nil || len(key) != 32 {
		return errors.New("invalid session key")
	}
	if d.Viewer == nil {
		return nil
	}
	if len(d.Viewer.Tokens) > 50 {
		return errors.New("too many viewer tokens")
	}
	ids, names, hashes := map[string]bool{}, map[string]bool{}, map[string]bool{}
	for _, record := range d.Viewer.Tokens {
		tokenID, err := base64.RawURLEncoding.DecodeString(record.ID)
		hash, hashErr := hex.DecodeString(record.Hash)
		name := strings.ToLower(record.Name)
		if err != nil || len(tokenID) != 16 || hashErr != nil || len(hash) != sha256.Size ||
			!validName(record.Name) || record.CreatedAt.IsZero() || !record.ExpiresAt.After(record.CreatedAt) ||
			ids[record.ID] || names[name] || hashes[record.Hash] {
			return errors.New("invalid or duplicate viewer token record")
		}
		ids[record.ID], names[name], hashes[record.Hash] = true, true, true
	}
	return nil
}

func validName(name string) bool {
	if name != strings.TrimSpace(name) || len([]rune(name)) == 0 || len([]rune(name)) > 40 {
		return false
	}
	for _, r := range name {
		if !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != ' ' && r != '.' && r != '-' && r != '_' {
			return false
		}
	}
	return true
}

func (s *Service) stateLocked() State {
	if s.loadErr != nil {
		return StateUnavailable
	}
	if s.data.Viewer == nil || len(s.data.Viewer.Tokens) == 0 {
		return StateOpen
	}
	return StateProtected
}

func (s *Service) LoadError() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.loadErr
}

func (s *Service) ViewerState() (State, []Viewer) {
	s.mu.Lock()
	defer s.mu.Unlock()
	state := s.stateLocked()
	var viewers []Viewer
	if state == StateProtected {
		for _, record := range s.data.Viewer.Tokens {
			viewers = append(viewers, record.Viewer)
		}
		sort.Slice(viewers, func(i, j int) bool {
			return strings.ToLower(viewers[i].Name) < strings.ToLower(viewers[j].Name)
		})
	}
	return state, viewers
}

func (s *Service) freshData() (fileData, error) {
	id := make([]byte, 4)
	key := make([]byte, 32)
	if _, err := io.ReadFull(s.random, id); err != nil {
		return fileData{}, err
	}
	if _, err := io.ReadFull(s.random, key); err != nil {
		return fileData{}, err
	}
	return fileData{Version: 1, InstanceID: hex.EncodeToString(id), SessionKey: base64.StdEncoding.EncodeToString(key)}, nil
}

func (s *Service) newToken(name string) (string, tokenRecord, error) {
	b := make([]byte, 12)
	id := make([]byte, 16)
	if _, err := io.ReadFull(s.random, b); err != nil {
		return "", tokenRecord{}, err
	}
	if _, err := io.ReadFull(s.random, id); err != nil {
		return "", tokenRecord{}, err
	}
	for i := range b {
		b[i] = alphabet[b[i]&63]
	}
	value := string(b)
	hash := sha256.Sum256(b)
	now := s.now().UTC()
	return value, tokenRecord{Viewer: Viewer{
		ID: base64.RawURLEncoding.EncodeToString(id), Name: name,
		CreatedAt: now, ExpiresAt: now.Add(tokenLife),
	}, Hash: hex.EncodeToString(hash[:])}, nil
}

func copyData(d fileData) fileData {
	if d.Viewer != nil {
		d.Viewer = &viewerRecords{Tokens: append([]tokenRecord(nil), d.Viewer.Tokens...)}
	}
	return d
}

func (s *Service) AddViewer(name string) (string, Viewer, error) {
	name = strings.TrimSpace(name)
	if !validName(name) {
		return "", Viewer{}, errors.New("invalid token name")
	}
	s.mu.Lock()
	d := copyData(s.data)
	var err error
	if s.loadErr != nil || d.Version == 0 {
		d, err = s.freshData()
		if err != nil {
			s.mu.Unlock()
			return "", Viewer{}, err
		}
	}
	if d.Viewer == nil {
		d.Viewer = &viewerRecords{}
	}
	if len(d.Viewer.Tokens) >= 50 {
		s.mu.Unlock()
		return "", Viewer{}, errors.New("maximum of 50 tokens")
	}
	for _, record := range d.Viewer.Tokens {
		if strings.EqualFold(record.Name, name) {
			s.mu.Unlock()
			return "", Viewer{}, errors.New("token name already exists")
		}
	}
	value, record, err := s.newToken(name)
	if err == nil {
		d.Viewer.Tokens = append(d.Viewer.Tokens, record)
		err = s.saveLocked(d, s.loadErr != nil)
	}
	callbacks := s.callbacksAfterChangeLocked(err)
	s.mu.Unlock()
	call(callbacks)
	if err != nil {
		return "", Viewer{}, err
	}
	return value, record.Viewer, err
}

func (s *Service) RegenerateViewer(id string) (string, Viewer, error) {
	s.mu.Lock()
	if s.loadErr != nil {
		err := s.loadErr
		s.mu.Unlock()
		return "", Viewer{}, err
	}
	d := copyData(s.data)
	if d.Viewer == nil {
		s.mu.Unlock()
		return "", Viewer{}, errors.New("token not found")
	}
	for i, old := range d.Viewer.Tokens {
		if old.ID == id {
			value, record, err := s.newToken(old.Name)
			if err == nil {
				d.Viewer.Tokens[i] = record
				err = s.saveLocked(d, false)
			}
			callbacks := s.callbacksAfterChangeLocked(err)
			s.mu.Unlock()
			call(callbacks)
			if err != nil {
				return "", Viewer{}, err
			}
			return value, record.Viewer, err
		}
	}
	s.mu.Unlock()
	return "", Viewer{}, errors.New("token not found")
}

func (s *Service) RevokeViewer(id string) error {
	s.mu.Lock()
	if s.loadErr != nil {
		err := s.loadErr
		s.mu.Unlock()
		return err
	}
	d := copyData(s.data)
	if d.Viewer != nil {
		for i, record := range d.Viewer.Tokens {
			if record.ID == id {
				d.Viewer.Tokens = append(d.Viewer.Tokens[:i], d.Viewer.Tokens[i+1:]...)
				if len(d.Viewer.Tokens) == 0 {
					d.Viewer = nil
				}
				err := s.saveLocked(d, false)
				callbacks := s.callbacksAfterChangeLocked(err)
				s.mu.Unlock()
				call(callbacks)
				return err
			}
		}
	}
	s.mu.Unlock()
	return errors.New("token not found")
}

func (s *Service) OpenViewerAccess() error {
	s.mu.Lock()
	d := copyData(s.data)
	var err error
	recovery := s.loadErr != nil
	if recovery || d.Version == 0 {
		d, err = s.freshData()
	}
	if err == nil {
		d.Viewer = nil
		err = s.saveLocked(d, recovery)
	}
	callbacks := s.callbacksAfterChangeLocked(err)
	s.mu.Unlock()
	call(callbacks)
	return err
}

func (s *Service) saveLocked(d fileData, recovery bool) error {
	if err := validate(d); err != nil {
		return err
	}
	extra := make(map[string]json.RawMessage)
	if !recovery {
		for k, v := range s.extra {
			extra[k] = v
		}
	}
	known, err := json.Marshal(d)
	if err != nil {
		return err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(known, &fields); err != nil {
		return err
	}
	for k, v := range fields {
		extra[k] = v
	}
	raw, err := json.MarshalIndent(extra, "", "  ")
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(s.dir, ".video-access-*.tmp")
	if err != nil {
		return err
	}
	name := f.Name()
	defer os.Remove(name)
	if err = f.Chmod(0600); err == nil {
		_, err = f.Write(raw)
	}
	if err == nil {
		err = f.Sync()
	}
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err == nil {
		err = os.Rename(name, filepath.Join(s.dir, filename))
	}
	if err != nil {
		return fmt.Errorf("save access file: %w", err)
	}
	s.data, s.extra, s.loadErr = d, extra, nil
	s.rescheduleLocked()
	return nil
}

func (s *Service) VerifyViewer(token string) (Viewer, Decision) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stateLocked() == StateUnavailable {
		return Viewer{}, ServiceUnavailable
	}
	if s.stateLocked() == StateOpen {
		return Viewer{}, Allowed
	}
	if token == "" {
		return Viewer{}, Missing
	}
	if len(token) > 128 {
		return Viewer{}, Invalid
	}
	hash := sha256.Sum256([]byte(token))
	var match *tokenRecord
	for i := range s.data.Viewer.Tokens {
		record := &s.data.Viewer.Tokens[i]
		stored, _ := hex.DecodeString(record.Hash)
		if subtle.ConstantTimeCompare(hash[:], stored) == 1 {
			match = record
		}
	}
	if match == nil {
		return Viewer{}, Invalid
	}
	if !s.now().Before(match.ExpiresAt) {
		return Viewer{}, ExpiredToken
	}
	return match.Viewer, Allowed
}

func (s *Service) IssueSession(id string) (string, time.Time, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stateLocked() != StateProtected {
		return "", time.Time{}, errors.New("viewer access is not protected")
	}
	for _, record := range s.data.Viewer.Tokens {
		if record.ID == id && s.now().Before(record.ExpiresAt) {
			issued := strconv.FormatInt(s.now().Unix(), 10)
			payload := "v1|" + id + "|" + issued
			key, _ := base64.StdEncoding.DecodeString(s.data.SessionKey)
			mac := hmac.New(sha256.New, key)
			mac.Write([]byte(payload))
			return "v1." + id + "." + issued + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil)), record.ExpiresAt, nil
		}
	}
	return "", time.Time{}, errors.New("token is not valid")
}

func (s *Service) VerifySession(value string) (Viewer, Decision) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stateLocked() == StateUnavailable {
		return Viewer{}, ServiceUnavailable
	}
	if s.stateLocked() == StateOpen {
		return Viewer{}, Allowed
	}
	if value == "" {
		return Viewer{}, Missing
	}
	parts := strings.Split(value, ".")
	if len(value) > 256 || len(parts) != 4 || parts[0] != "v1" {
		return Viewer{}, Invalid
	}
	if _, err := strconv.ParseInt(parts[2], 10, 64); err != nil {
		return Viewer{}, Invalid
	}
	sig, err := base64.RawURLEncoding.DecodeString(parts[3])
	if err != nil {
		return Viewer{}, Invalid
	}
	key, _ := base64.StdEncoding.DecodeString(s.data.SessionKey)
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte("v1|" + parts[1] + "|" + parts[2]))
	if !hmac.Equal(sig, mac.Sum(nil)) {
		return Viewer{}, Invalid
	}
	for _, record := range s.data.Viewer.Tokens {
		if record.ID == parts[1] {
			if !s.now().Before(record.ExpiresAt) {
				return Viewer{}, ExpiredToken
			}
			return record.Viewer, Allowed
		}
	}
	return Viewer{}, Invalid
}

// CookieName returns the session cookie name, or "" before a token has ever
// been created (no instance id yet, so no session can exist).
func (s *Service) CookieName() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.data.InstanceID == "" {
		return ""
	}
	return "owlcms_replays_" + s.data.InstanceID
}

func (s *Service) OnChange(callback func()) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.callbacks = append(s.callbacks, callback)
}

func (s *Service) callbacksAfterChangeLocked(err error) []func() {
	if err != nil {
		return nil
	}
	return append([]func(){}, s.callbacks...)
}

func call(callbacks []func()) {
	for _, callback := range callbacks {
		callback()
	}
}

func (s *Service) rescheduleLocked() {
	if s.timer != nil {
		s.timer.Stop()
	}
	if s.data.Viewer == nil {
		return
	}
	now := s.now()
	var earliest time.Time
	for _, record := range s.data.Viewer.Tokens {
		if record.ExpiresAt.After(now) && (earliest.IsZero() || record.ExpiresAt.Before(earliest)) {
			earliest = record.ExpiresAt
		}
	}
	if !earliest.IsZero() {
		s.timer = time.AfterFunc(earliest.Sub(now), func() {
			s.mu.Lock()
			s.rescheduleLocked()
			callbacks := append([]func(){}, s.callbacks...)
			s.mu.Unlock()
			call(callbacks)
		})
	}
}
