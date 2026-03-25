package session

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fusemomo-api/internal/models"
	"sync"
	"time"
)

const (
	// how long a server session remains valid.
	SessionTTL = 24 * time.Hour
	// how often the background GC purges expired entries.
	gcInterval = 5 * time.Minute
	// HttpOnly cookie name set on the browser.
	CookieName = "fm_session"
)

// Store is a thread-safe in-memory map from session ID → Session.
type Store struct {
	mu       sync.RWMutex
	sessions map[string]models.Session
}

func New() *Store {
	return &Store{
		sessions: make(map[string]models.Session),
	}
}

// Create generates a cryptographically random session ID, stores the session,
// and returns it. Returns an error only if the system CSPRNG fails.
func (s *Store) Create(tenantID, authUserID, role, plan string) (models.Session, error) {
	id, err := generateID()
	if err != nil {
		return models.Session{}, err
	}

	sess := models.Session{
		ID:         id,
		TenantID:   tenantID,
		AuthUserID: authUserID,
		Role:       role,
		Plan:       plan,
		ExpiresAt:  time.Now().Add(SessionTTL),
	}

	s.mu.Lock()
	s.sessions[id] = sess
	s.mu.Unlock()

	return sess, nil
}

// Get returns the session for the given ID.
// Returns false if the session does not exist or has expired.
func (s *Store) Get(id string) (models.Session, bool) {
	s.mu.RLock()
	sess, ok := s.sessions[id]
	s.mu.RUnlock()

	if !ok || time.Now().After(sess.ExpiresAt) {
		return models.Session{}, false
	}

	return sess, true
}

// Delete removes a session (e.g. on logout).
func (s *Store) Delete(id string) {
	s.mu.Lock()
	delete(s.sessions, id)
	s.mu.Unlock()
}

// GCLoop runs a background goroutine that purges expired sessions every gcInterval.
// Call this once at startup. It stops when ctx is cancelled.
func (s *Store) GCLoop(ctx context.Context) {
	ticker := time.NewTicker(gcInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.purgeExpired()
		}
	}
}

func (s *Store) purgeExpired() {
	now := time.Now()

	s.mu.Lock()
	for id, sess := range s.sessions {
		if now.After(sess.ExpiresAt) {
			delete(s.sessions, id)
		}
	}
	s.mu.Unlock()
}

// generateID returns a 32-byte hex-encoded cryptographically random ID.
func generateID() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}
