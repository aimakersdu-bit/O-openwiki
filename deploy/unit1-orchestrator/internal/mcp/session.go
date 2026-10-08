package mcp

import (
	"errors"
	"net/url"
	"strings"
	"sync"
	"time"
)

var (
	ErrMaxSessionsExceeded = errors.New("maximum concurrent sessions limit exceeded")
	ErrSessionNotFound     = errors.New("session not found")
	ErrInvalidOrigin       = errors.New("origin header not allowed")
)

type ClientSession struct {
	ID         string
	CreatedAt  time.Time
	LastActive time.Time
	MsgChan    chan any
	Done       chan struct{}
	mu         sync.Mutex
	closed     bool
}

func newClientSession(id string) *ClientSession {
	now := time.Now()
	return &ClientSession{
		ID:         id,
		CreatedAt:  now,
		LastActive: now,
		MsgChan:    make(chan any, 100),
		Done:       make(chan struct{}),
	}
}

func (s *ClientSession) Touch() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.LastActive = time.Now()
}

func (s *ClientSession) Send(msg any) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return false
	}
	select {
	case s.MsgChan <- msg:
		return true
	default:
		// Queue full
		return false
	}
}

func (s *ClientSession) Close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.closed {
		s.closed = true
		close(s.Done)
	}
}

type SessionManager struct {
	mu             sync.RWMutex
	sessions       map[string]*ClientSession
	maxSessions    int
	ttl            time.Duration
	allowedOrigins []string
	stopCleanup    chan struct{}
}

func NewSessionManager(maxSessions int, ttl time.Duration, allowedOrigins []string) *SessionManager {
	if maxSessions <= 0 {
		maxSessions = 100
	}
	if ttl <= 0 {
		ttl = 30 * time.Minute
	}

	sm := &SessionManager{
		sessions:       make(map[string]*ClientSession),
		maxSessions:    maxSessions,
		ttl:            ttl,
		allowedOrigins: allowedOrigins,
		stopCleanup:    make(chan struct{}),
	}

	go sm.cleanupLoop()
	return sm
}

// Stop shuts down the session manager and its cleanup loop.
func (sm *SessionManager) Stop() {
	close(sm.stopCleanup)
	sm.mu.Lock()
	defer sm.mu.Unlock()
	for _, sess := range sm.sessions {
		sess.Close()
	}
	sm.sessions = make(map[string]*ClientSession)
}

func (sm *SessionManager) cleanupLoop() {
	ticker := time.NewTicker(1 * time.Minute)
	defer ticker.Stop()

	for {
		select {
		case <-sm.stopCleanup:
			return
		case <-ticker.C:
			sm.cleanExpired()
		}
	}
}

func (sm *SessionManager) cleanExpired() {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	now := time.Now()
	for id, sess := range sm.sessions {
		if now.Sub(sess.LastActive) > sm.ttl {
			sess.Close()
			delete(sm.sessions, id)
		}
	}
}

// CheckOrigin verifies the Origin header against host and allowed origins to prevent DNS rebinding.
func (sm *SessionManager) CheckOrigin(origin string, host string) bool {
	if origin == "" {
		// Non-browser or direct requests might not have an Origin header
		return true
	}

	for _, allowed := range sm.allowedOrigins {
		if allowed == "*" || allowed == origin {
			return true
		}
	}

	// Default behavior: allow same host
	parsed, err := url.Parse(origin)
	if err == nil {
		if strings.EqualFold(parsed.Host, host) {
			return true
		}
	}

	// If no explicit whitelist was configured and origin matches host, allow
	if len(sm.allowedOrigins) == 0 {
		return true
	}

	return false
}

// Get returns an existing session and touches its active time.
func (sm *SessionManager) Get(id string) (*ClientSession, error) {
	sm.mu.RLock()
	sess, exists := sm.sessions[id]
	sm.mu.RUnlock()

	if !exists {
		return nil, ErrSessionNotFound
	}
	sess.Touch()
	return sess, nil
}

// GetOrCreate returns an existing session or creates a new one if not exceeded.
func (sm *SessionManager) GetOrCreate(id string) (*ClientSession, error) {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	if sess, exists := sm.sessions[id]; exists {
		sess.Touch()
		return sess, nil
	}

	if len(sm.sessions) >= sm.maxSessions {
		return nil, ErrMaxSessionsExceeded
	}

	sess := newClientSession(id)
	sm.sessions[id] = sess
	return sess, nil
}

// Remove closes and deletes a session.
func (sm *SessionManager) Remove(id string) {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	if sess, exists := sm.sessions[id]; exists {
		sess.Close()
		delete(sm.sessions, id)
	}
}

// Count returns the number of active sessions.
func (sm *SessionManager) Count() int {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	return len(sm.sessions)
}
