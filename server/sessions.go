package server

import (
	"crypto/rand"
	"encoding/hex"
	"sync"
	"time"
)

const (
	sessionCookieName = "idtrack_session"
	defaultSessionTTL = 24 * time.Hour
	keepLoggedInTTL   = 30 * 24 * time.Hour
)

type session struct {
	username  string
	expiresAt time.Time
}

type sessionStore struct {
	mu       sync.Mutex
	sessions map[string]*session
}

func newSessionStore() *sessionStore {
	return &sessionStore{sessions: make(map[string]*session)}
}

// create generates a cryptographically random session token, stores it with
// the given username and TTL, and returns the token. The token is 32 random
// bytes encoded as 64 lowercase hex characters.
func (ss *sessionStore) create(username string, ttl time.Duration) string {
	b := make([]byte, 32)
	rand.Read(b) //nolint:errcheck // rand.Read never returns an error on supported platforms
	token := hex.EncodeToString(b)

	ss.mu.Lock()
	ss.sessions[token] = &session{username: username, expiresAt: time.Now().Add(ttl)}
	ss.mu.Unlock()

	return token
}

// lookup returns the username associated with token if it exists and has not
// expired. Expired entries are deleted on lookup so the map does not grow
// unboundedly.
func (ss *sessionStore) lookup(token string) (string, bool) {
	ss.mu.Lock()
	defer ss.mu.Unlock()

	s, ok := ss.sessions[token]
	if !ok {
		return "", false
	}

	if time.Now().After(s.expiresAt) {
		delete(ss.sessions, token)
		return "", false
	}

	return s.username, true
}

// delete removes a session by token. It is a no-op if the token does not exist.
func (ss *sessionStore) delete(token string) {
	ss.mu.Lock()
	delete(ss.sessions, token)
	ss.mu.Unlock()
}
