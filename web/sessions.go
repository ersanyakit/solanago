package web

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"sync"
	"time"

	"github.com/ersanyakit/solanago/sdk"
	"github.com/ersanyakit/solanago/svmtest"
)

// ErrSessionNotFound is returned when a deploy session ID is unknown or has
// expired.
var ErrSessionNotFound = errors.New("web: deploy session not found or expired")

// sessionTTL bounds how long a deploy session's ephemeral buffer/program
// keypairs are kept in memory. A deploy that takes longer than this to
// click through needs to start over with a fresh /api/deploy/session call.
const sessionTTL = 30 * time.Minute

// deploySession holds the two ephemeral keypairs a wallet-driven deploy
// needs server-side between preparing and finalizing: the loader buffer and
// the new program account. Only their public keys are ever sent to the
// browser; PrepareCreateBufferTransaction/PrepareDeployTransaction use the
// private halves to co-sign, leaving the fee-payer slot for the wallet.
type deploySession struct {
	id        string
	exampleID string
	feePayer  sdk.Pubkey
	rpcURL    string
	buffer    svmtest.Signer
	program   svmtest.Signer
	elfLength int
	buildID   string
	createdAt time.Time
}

type sessionStore struct {
	mu       sync.Mutex
	sessions map[string]*deploySession
}

func newSessionStore() *sessionStore {
	return &sessionStore{sessions: make(map[string]*deploySession)}
}

func (s *sessionStore) create(exampleID string, feePayer sdk.Pubkey, rpcURL string, elfLength int, buildID string) (*deploySession, error) {
	buffer, err := svmtest.NewSigner()
	if err != nil {
		return nil, err
	}
	program, err := svmtest.NewSigner()
	if err != nil {
		return nil, err
	}
	id, err := randomSessionID()
	if err != nil {
		return nil, err
	}
	session := &deploySession{
		id:        id,
		exampleID: exampleID,
		feePayer:  feePayer,
		rpcURL:    rpcURL,
		buffer:    buffer,
		program:   program,
		elfLength: elfLength,
		buildID:   buildID,
		createdAt: time.Now(),
	}
	s.mu.Lock()
	s.sweepLocked()
	s.sessions[id] = session
	s.mu.Unlock()
	return session, nil
}

func (s *sessionStore) get(id string) (*deploySession, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sweepLocked()
	session, ok := s.sessions[id]
	if !ok {
		return nil, ErrSessionNotFound
	}
	return session, nil
}

// sweepLocked drops sessions older than sessionTTL. Callers must hold s.mu.
func (s *sessionStore) sweepLocked() {
	deadline := time.Now().Add(-sessionTTL)
	for id, session := range s.sessions {
		if session.createdAt.Before(deadline) {
			delete(s.sessions, id)
		}
	}
}

func randomSessionID() (string, error) {
	raw := make([]byte, 16)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return hex.EncodeToString(raw), nil
}
