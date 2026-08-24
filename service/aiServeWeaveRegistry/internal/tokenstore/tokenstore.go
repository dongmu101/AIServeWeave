// Package tokenstore implements the Registry's one-time bootstrap tokens: the
// short-lived secret an operator hands to a new node so it can exchange it for
// a certificate exactly once (RegisterRequest.bootstrap_token in
// api/proto/tunnel/v1/tunnel.proto).
//
// Strong, one-time-use consistency is the whole point of a bootstrap token,
// and a single Registry process is the assumption this phase of the project
// runs under (Registry high availability is explicit third-phase work in the
// top-level README's roadmap). Under that assumption a mutex-guarded,
// atomically-written file gives exactly the consistency a distributed store
// would, with none of the operational cost.
package tokenstore

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// ErrInvalidToken is returned by Consume for a token that does not exist, was
// already used, or has expired. The three cases are deliberately not
// distinguished in the returned error: telling a caller which one applies
// would let them probe for tokens that exist but are merely expired or spent.
var ErrInvalidToken = errors.New("tokenstore: token is invalid or expired")

// record is one minted token, as stored on disk.
type record struct {
	Token     string    `json:"token"`
	IssuedAt  time.Time `json:"issued_at"`
	ExpiresAt time.Time `json:"expires_at"`
	Used      bool      `json:"used"`
	UsedAt    time.Time `json:"used_at,omitzero"`
}

// gcAge is how long past expiry (or use) a record is kept before Mint or
// Consume drop it, so the file does not grow without bound. It is generous
// so an operator inspecting the file shortly after an event still finds it.
const gcAge = 24 * time.Hour

// fileMode is deliberately as strict as the node key files in
// tunnel/identity.go: a bootstrap token is a bearer credential until it is
// consumed.
const fileMode = 0o600

// Store is the on-disk, mutex-guarded bootstrap token store. The zero value
// is not usable; construct one with Open.
type Store struct {
	path string

	mu      sync.Mutex
	records map[string]*record
}

// Open loads path if it exists, or starts empty if it does not. path's
// parent directory is created if it does not exist.
func Open(path string) (*Store, error) {
	s := &Store{path: path, records: make(map[string]*record)}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("tokenstore: cannot create %s: %w", filepath.Dir(path), err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return s, nil
		}
		return nil, fmt.Errorf("tokenstore: cannot read %s: %w", path, err)
	}
	if len(data) == 0 {
		return s, nil
	}
	var recs []*record
	if err := json.Unmarshal(data, &recs); err != nil {
		return nil, fmt.Errorf("tokenstore: cannot parse %s: %w", path, err)
	}
	for _, r := range recs {
		s.records[r.Token] = r
	}
	return s, nil
}

// Mint generates a fresh one-time token valid until now+ttl, persists it, and
// returns the token value. The value is not recoverable once this call
// returns — the caller (an operator, via the Registry's -mint-token CLI mode)
// is responsible for handing it to the node out of band.
func (s *Store) Mint(ttl time.Duration, now time.Time) (string, error) {
	if ttl <= 0 {
		return "", errors.New("tokenstore: ttl must be positive")
	}
	token, err := randomToken()
	if err != nil {
		return "", err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	s.records[token] = &record{
		Token:     token,
		IssuedAt:  now,
		ExpiresAt: now.Add(ttl),
	}
	s.gc(now)
	if err := s.persistLocked(); err != nil {
		delete(s.records, token)
		return "", err
	}
	return token, nil
}

// Consume validates token and marks it used, so a second call with the same
// value fails. It returns ErrInvalidToken for a token that is unknown,
// already used, or expired.
func (s *Store) Consume(token string, now time.Time) error {
	if token == "" {
		return ErrInvalidToken
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	r, ok := s.records[token]
	if !ok || r.Used || now.After(r.ExpiresAt) {
		return ErrInvalidToken
	}
	r.Used = true
	r.UsedAt = now
	s.gc(now)
	if err := s.persistLocked(); err != nil {
		r.Used = false
		r.UsedAt = time.Time{}
		return err
	}
	return nil
}

// gc drops records that expired, or were used, more than gcAge ago. Callers
// must hold s.mu.
func (s *Store) gc(now time.Time) {
	for token, r := range s.records {
		if r.Used && now.Sub(r.UsedAt) > gcAge {
			delete(s.records, token)
			continue
		}
		if !r.Used && now.Sub(r.ExpiresAt) > gcAge {
			delete(s.records, token)
		}
	}
}

// persistLocked writes every record to s.path atomically. Callers must hold
// s.mu.
func (s *Store) persistLocked() error {
	recs := make([]*record, 0, len(s.records))
	for _, r := range s.records {
		recs = append(recs, r)
	}
	data, err := json.MarshalIndent(recs, "", "  ")
	if err != nil {
		return fmt.Errorf("tokenstore: cannot encode token file: %w", err)
	}
	return writeFileAtomic(s.path, data, fileMode)
}

// randomToken returns a 32-byte, base64url-encoded random token.
func randomToken() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("tokenstore: cannot generate token: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

// writeFileAtomic writes data to path through a temporary file in the same
// directory, so a reader never observes a partially written token file.
func writeFileAtomic(path string, data []byte, mode os.FileMode) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // no-op once the rename succeeded

	if err := tmp.Chmod(mode); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}
