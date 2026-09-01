package server

import (
	"crypto/rand"
	"encoding/hex"
	"sync"
	"time"
)

// sessionStore keeps the logged-in sessions in memory. Itztli is a
// single small binary in front of one tezcatl server; losing sessions
// on restart just means logging in again.
type sessionStore struct {
	mutex    sync.Mutex
	ttl      time.Duration
	sessions map[string]time.Time
}

func newSessionStore(ttl time.Duration) *sessionStore {
	return &sessionStore{
		ttl:      ttl,
		sessions: map[string]time.Time{},
	}
}

func (s *sessionStore) create() string {
	token := randomToken()

	s.mutex.Lock()
	defer s.mutex.Unlock()

	s.prune()
	s.sessions[token] = time.Now().Add(s.ttl)

	return token
}

func (s *sessionStore) valid(token string) bool {
	if token == "" {
		return false
	}

	s.mutex.Lock()
	defer s.mutex.Unlock()

	expiry, exists := s.sessions[token]
	if !exists || time.Now().After(expiry) {
		delete(s.sessions, token)

		return false
	}

	return true
}

func (s *sessionStore) revoke(token string) {
	s.mutex.Lock()
	defer s.mutex.Unlock()

	delete(s.sessions, token)
}

// prune drops the expired sessions; called with the lock held.
func (s *sessionStore) prune() {
	now := time.Now()
	for token, expiry := range s.sessions {
		if now.After(expiry) {
			delete(s.sessions, token)
		}
	}
}

func randomToken() string {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		// crypto/rand failing means the platform is broken; there is
		// no session to hand out in that world.
		panic(err)
	}

	return hex.EncodeToString(raw)
}
