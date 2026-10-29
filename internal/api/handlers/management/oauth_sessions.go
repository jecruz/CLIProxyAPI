package management

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const (
	oauthSessionTTL     = 10 * time.Minute
	maxOAuthStateLength = 128
)

const (
	oauthSessionSourceBuiltin = "builtin"
	oauthSessionSourcePlugin  = "plugin"
)

var (
	errInvalidOAuthState      = errors.New("invalid oauth state")
	errUnsupportedOAuthFlow   = errors.New("unsupported oauth provider")
	errOAuthSessionNotPending = errors.New("oauth session is not pending")
	errOAuthSessionExists     = errors.New("oauth session already exists")
)

// oauthSessionGeneration is an immutable per-registration ownership token.
type oauthSessionGeneration struct{ state, token string }
type oauthGenerationContextKey struct{}

// oauthGeneration returns the immutable ownership token carried by a login worker.
func oauthGeneration(ctx context.Context) *oauthSessionGeneration {
	if ctx == nil {
		return nil
	}
	generation, _ := ctx.Value(oauthGenerationContextKey{}).(*oauthSessionGeneration)
	return generation
}

// oauthSession is a snapshot; terminal outcomes are retained, never inferred from absence.
type oauthSession struct {
	generation  *oauthSessionGeneration
	Phase       string
	Provider    string
	Status      string
	Source      string
	Metadata    map[string]any
	CreatedAt   time.Time
	ExpiresAt   time.Time
	RetainUntil time.Time
}

type oauthSessionRecord struct {
	pollMu  sync.Mutex
	mu      sync.Mutex
	session oauthSession
	ctx     context.Context
	cancel  context.CancelFunc
}

type oauthSessionStore struct {
	mu       sync.RWMutex
	ttl      time.Duration
	now      func() time.Time
	sessions map[string]*oauthSessionRecord
}

func newOAuthSessionStore(ttl time.Duration) *oauthSessionStore {
	if ttl <= 0 {
		ttl = oauthSessionTTL
	}
	return &oauthSessionStore{ttl: ttl, now: time.Now, sessions: make(map[string]*oauthSessionRecord)}
}

func (s *oauthSessionStore) lookup(state string) *oauthSessionRecord {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.sessions[strings.TrimSpace(state)]
}

func (s *oauthSessionStore) expireLocked(record *oauthSessionRecord) {
	if record.session.Phase == "pending" && !s.now().Before(record.session.ExpiresAt) {
		s.finishLocked(record, "expired", "Authentication expired; start again")
	}
}

func (s *oauthSessionStore) finishLocked(record *oauthSessionRecord, phase, message string) {
	record.session.Phase = phase
	record.session.Status = message
	record.session.RetainUntil = s.now().Add(s.ttl)
	if phase == "expired" {
		record.session.RetainUntil = record.session.ExpiresAt.Add(s.ttl)
	}
	record.cancel()
}

func (s *oauthSessionStore) purgeExpired() {
	s.mu.RLock()
	records := make(map[string]*oauthSessionRecord, len(s.sessions))
	for state, record := range s.sessions {
		records[state] = record
	}
	s.mu.RUnlock()
	for state, record := range records {
		// A save hook may register another login while holding this record's
		// commit lock. Cleanup is opportunistic; never wait on that lock.
		if !record.mu.TryLock() {
			continue
		}
		s.expireLocked(record)
		expired := record.session.Phase != "pending" && !s.now().Before(record.session.RetainUntil)
		if expired {
			s.mu.Lock()
			if s.sessions[state] == record {
				delete(s.sessions, state)
			}
			s.mu.Unlock()
		}
		record.mu.Unlock()
	}
}

func (s *oauthSessionStore) register(state, provider, source string, metadata map[string]any,
	parent context.Context, deadline time.Time) (context.Context, error) {
	state = strings.TrimSpace(state)
	provider = strings.ToLower(strings.TrimSpace(provider))
	if err := ValidateOAuthState(state); err != nil {
		return nil, err
	}
	if provider == "" {
		return nil, errUnsupportedOAuthFlow
	}
	s.purgeExpired()
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	// Validate the absolute producer deadline after cleanup and lock acquisition;
	// neither time spent waiting nor another commit can extend its authority.
	if !now.Before(deadline) {
		return nil, fmt.Errorf("login deadline has expired")
	}
	if _, exists := s.sessions[state]; exists {
		return nil, errOAuthSessionExists
	}
	token := make([]byte, 32)
	if _, err := rand.Read(token); err != nil {
		return nil, fmt.Errorf("create oauth owner: %w", err)
	}
	generation := &oauthSessionGeneration{state: state, token: hex.EncodeToString(token)}
	ctx, cancel := context.WithDeadline(context.WithValue(parent, oauthGenerationContextKey{}, generation), deadline)
	record := &oauthSessionRecord{
		session: oauthSession{generation: generation, Phase: "pending", Provider: provider, Source: source,
			Metadata: cloneOAuthSessionMetadata(metadata), CreatedAt: now, ExpiresAt: deadline},
		ctx: ctx, cancel: cancel,
	}
	s.sessions[state] = record
	return ctx, nil
}

func (s *oauthSessionStore) Register(state, provider string) context.Context {
	ctx, _ := s.register(state, provider, oauthSessionSourceBuiltin, nil, context.Background(), s.now().Add(5*time.Minute))
	return ctx
}

// RegisterPlugin honors the producer deadline, defaulting to five minutes when omitted.
func (s *oauthSessionStore) RegisterPlugin(state, provider string, metadata map[string]any, expiresAt ...time.Time) error {
	_, err := s.RegisterPluginContext(state, provider, metadata, expiresAt...)
	return err
}

// RegisterPluginContext returns the owner context for the exact newly registered plugin login.
func (s *oauthSessionStore) RegisterPluginContext(state, provider string, metadata map[string]any, expiresAt ...time.Time) (context.Context, error) {
	deadline := s.now().Add(5 * time.Minute)
	if len(expiresAt) > 0 && !expiresAt[0].IsZero() {
		deadline = expiresAt[0]
	}
	return s.register(state, provider, oauthSessionSourcePlugin, metadata, context.Background(), deadline)
}

// RegisterContext bounds acquisition, callback waits, and token exchange for one builtin flow.
func (s *oauthSessionStore) RegisterContext(state, provider string, parent context.Context, duration time.Duration) (context.Context, error) {
	return s.register(state, provider, oauthSessionSourceBuiltin, nil, parent, s.now().Add(duration))
}

// currentLocked checks identity while the record lock prevents purge/replacement.
func (s *oauthSessionStore) currentLocked(state string, record *oauthSessionRecord, generation *oauthSessionGeneration) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return generation != nil && record.session.generation == generation && s.sessions[strings.TrimSpace(state)] == record
}

func (s *oauthSessionStore) transition(state string, generation *oauthSessionGeneration, phase, message string) bool {
	record := s.lookup(state)
	if record == nil {
		return false
	}
	record.mu.Lock()
	defer record.mu.Unlock()
	if !s.currentLocked(state, record, generation) {
		return false
	}
	s.expireLocked(record)
	if record.session.Phase != "pending" {
		return false
	}
	s.finishLocked(record, phase, message)
	return true
}

func (s *oauthSessionStore) SetError(state, message string) {
	if record := s.lookup(state); record != nil {
		s.setErrorGeneration(state, record.session.generation, message)
	}
}

func (s *oauthSessionStore) setErrorGeneration(state string, generation *oauthSessionGeneration, message string) {
	if strings.TrimSpace(message) == "" {
		message = "Authentication failed"
	}
	s.transition(state, generation, "failed", strings.TrimSpace(message))
}

// SetErrorContext cannot apply a stale worker's error to a replacement login.
func (s *oauthSessionStore) SetErrorContext(state string, owner context.Context, message string) {
	s.setErrorGeneration(state, oauthGeneration(owner), message)
}

func (s *oauthSessionStore) Complete(state string) {
	if record := s.lookup(state); record != nil {
		s.transition(state, record.session.generation, "succeeded", "")
	}
}

// CompleteContext completes only the generation that originally started this worker.
func (s *oauthSessionStore) CompleteContext(state string, owner context.Context) {
	s.transition(state, oauthGeneration(owner), "succeeded", "")
}

// Cancel serializes with credential commit; an existing terminal outcome never changes.
func (s *oauthSessionStore) Cancel(state string) (oauthSession, bool) {
	record := s.lookup(state)
	if record == nil {
		return oauthSession{}, false
	}
	record.mu.Lock()
	defer record.mu.Unlock()
	if !s.currentLocked(state, record, record.session.generation) {
		return oauthSession{}, false
	}
	s.expireLocked(record)
	if record.session.Phase == "pending" {
		s.finishLocked(record, "cancelled", "Authentication cancelled")
	}
	return record.session, true
}

// CancelOwned refuses a stale client token before expiring or changing the current generation.
func (s *oauthSessionStore) CancelOwned(state, token string) (oauthSession, bool) {
	record := s.lookup(state)
	if record == nil {
		return oauthSession{}, false
	}
	record.mu.Lock()
	defer record.mu.Unlock()
	generation := record.session.generation
	if token == "" || subtle.ConstantTimeCompare([]byte(token), []byte(generation.token)) != 1 || !s.currentLocked(state, record, generation) {
		return oauthSession{}, false
	}
	s.expireLocked(record)
	if record.session.Phase == "pending" {
		s.finishLocked(record, "cancelled", "Authentication cancelled")
	}
	return record.session, true
}

// oauthAttemptToken returns only the ownership capability captured at registration.
func oauthAttemptToken(ctx context.Context) string {
	if generation := oauthGeneration(ctx); generation != nil {
		return generation.token
	}
	return ""
}

// Commit is the credential-save linearization point shared with cancellation.
// Once commit wins this lock before expiry, persistence runs without cancellation;
// a cancel that wins first can never call save. Other sessions have separate locks.
func (s *oauthSessionStore) Commit(state string, owner context.Context, save func(context.Context) (string, error)) (string, error) {
	record := s.lookup(state)
	if record == nil {
		return "", errOAuthSessionNotPending
	}
	record.mu.Lock()
	defer record.mu.Unlock()
	if !s.currentLocked(state, record, oauthGeneration(owner)) {
		return "", errOAuthSessionNotPending
	}
	s.expireLocked(record)
	if record.session.Phase != "pending" || record.ctx.Err() != nil {
		return "", errOAuthSessionNotPending
	}
	path, err := save(context.WithoutCancel(record.ctx))
	if err != nil {
		s.finishLocked(record, "failed", "Failed to save authentication tokens")
		return path, err
	}
	s.finishLocked(record, "succeeded", "")
	return path, nil
}

func (s *oauthSessionStore) CompleteProvider(provider, source string) int {
	s.mu.RLock()
	records := make(map[string]*oauthSessionGeneration)
	for state, record := range s.sessions {
		// Provider/source and generation are immutable after registration.
		if strings.EqualFold(record.session.Provider, strings.TrimSpace(provider)) && (source == "" || record.session.Source == source) {
			records[state] = record.session.generation
		}
	}
	s.mu.RUnlock()
	return s.completeProviderRecords(records)
}

// completeProviderRecords retains the generation observed by the provider batch.
func (s *oauthSessionStore) completeProviderRecords(records map[string]*oauthSessionGeneration) int {
	count := 0
	for state, generation := range records {
		if s.transition(state, generation, "succeeded", "") {
			count++
		}
	}
	return count
}

func (s *oauthSessionStore) Get(state string) (oauthSession, bool) {
	return s.getGeneration(state, nil)
}

// getGeneration rejects a replaced record before any lazy expiry or response.
func (s *oauthSessionStore) getGeneration(state string, generation *oauthSessionGeneration) (oauthSession, bool) {
	record := s.lookup(state)
	if record == nil {
		return oauthSession{}, false
	}
	record.mu.Lock()
	defer record.mu.Unlock()
	if generation == nil {
		generation = record.session.generation
	}
	if !s.currentLocked(state, record, generation) {
		return oauthSession{}, false
	}
	s.expireLocked(record)
	if record.session.Phase != "pending" && !s.now().Before(record.session.RetainUntil) {
		return oauthSession{}, false
	}
	snapshot := record.session
	snapshot.Metadata = cloneOAuthSessionMetadata(snapshot.Metadata)
	return snapshot, true
}

func (s *oauthSessionStore) IsPending(state, provider string) bool {
	session, ok := s.Get(state)
	return ok && session.Phase == "pending" && session.Source != oauthSessionSourcePlugin &&
		(provider == "" || strings.EqualFold(session.Provider, strings.TrimSpace(provider)))
}

func cloneOAuthSessionMetadata(in map[string]any) map[string]any {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]any, len(in))
	for key, value := range in {
		out[key] = value
	}
	return out
}

var oauthSessions = newOAuthSessionStore(oauthSessionTTL)

// RegisterOAuthSession records a legacy state-only login.
// Deprecated: Worker ownership is not returned; see the SDK migration in #4367.
func RegisterOAuthSession(state, provider string) { oauthSessions.Register(state, provider) }

// RegisterPluginOAuthSession records a plugin login using its authoritative deadline when supplied.
func RegisterPluginOAuthSession(state, provider string, metadata map[string]any, expiresAt ...time.Time) error {
	return oauthSessions.RegisterPlugin(state, provider, metadata, expiresAt...)
}

// SetOAuthSessionError applies a legacy state-only error.
// Deprecated: Async workers must use ownership-bound transitions; see #4367.
func SetOAuthSessionError(state, message string) { oauthSessions.SetError(state, message) }

// CompleteOAuthSession applies legacy state-only completion.
// Deprecated: Async workers must use ownership-bound transitions; see #4367.
func CompleteOAuthSession(state string) { oauthSessions.Complete(state) }

func CompleteOAuthSessionsByProvider(provider string) int {
	return oauthSessions.CompleteProvider(provider, oauthSessionSourceBuiltin)
}

func CompletePluginOAuthSessionsByProvider(provider string) int {
	return oauthSessions.CompleteProvider(provider, oauthSessionSourcePlugin)
}

// GetOAuthSession preserves the legacy getter: successful completion is no longer pending.
func GetOAuthSession(state string) (provider string, status string, ok bool) {
	session, ok := oauthSessions.Get(state)
	if !ok || session.Phase == "succeeded" {
		return "", "", false
	}
	return session.Provider, session.Status, true
}

// GetOAuthSessionPhase returns explicit lifecycle state, including retained success.
func GetOAuthSessionPhase(state string) (string, bool) {
	session, ok := oauthSessions.Get(state)
	return session.Phase, ok
}

func GetOAuthSessionDetails(state string) (provider string, status string, isPlugin bool, metadata map[string]any, ok bool) {
	session, ok := oauthSessions.Get(state)
	if !ok {
		return "", "", false, nil, false
	}
	return session.Provider, session.Status, session.Source == oauthSessionSourcePlugin, cloneOAuthSessionMetadata(session.Metadata), true
}

func IsOAuthSessionPending(state, provider string) bool {
	return oauthSessions.IsPending(state, provider)
}

func oauthSessionErrorWithCause(message string, cause error) string {
	message = strings.TrimSpace(message)
	if message == "" {
		message = "Authentication failed"
	}
	if cause == nil {
		return message
	}
	detail := strings.TrimSpace(cause.Error())
	if detail == "" {
		return message
	}
	return message + ": " + detail
}

func ValidateOAuthState(state string) error {
	trimmed := strings.TrimSpace(state)
	if trimmed == "" {
		return fmt.Errorf("%w: empty", errInvalidOAuthState)
	}
	if len(trimmed) > maxOAuthStateLength {
		return fmt.Errorf("%w: too long", errInvalidOAuthState)
	}
	if strings.Contains(trimmed, "/") || strings.Contains(trimmed, "\\") {
		return fmt.Errorf("%w: contains path separator", errInvalidOAuthState)
	}
	if strings.Contains(trimmed, "..") {
		return fmt.Errorf("%w: contains '..'", errInvalidOAuthState)
	}
	for _, r := range trimmed {
		switch {
		case r >= 'a' && r <= 'z':
		case r >= 'A' && r <= 'Z':
		case r >= '0' && r <= '9':
		case r == '-' || r == '_' || r == '.':
		default:
			return fmt.Errorf("%w: invalid character", errInvalidOAuthState)
		}
	}
	return nil
}

func NormalizeOAuthProvider(provider string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(provider)) {
	case "anthropic", "claude":
		return "anthropic", nil
	case "codex", "openai":
		return "codex", nil
	case "gemini", "google":
		return "gemini", nil
	case "antigravity", "anti-gravity":
		return "antigravity", nil
	case "xai", "x-ai", "x.ai", "grok":
		return "xai", nil
	default:
		return "", errUnsupportedOAuthFlow
	}
}

type oauthCallbackFilePayload struct {
	Code  string `json:"code"`
	State string `json:"state"`
	Error string `json:"error"`
}

func WriteOAuthCallbackFile(authDir, provider, state, code, errorMessage string) (string, error) {
	if strings.TrimSpace(authDir) == "" {
		return "", fmt.Errorf("auth dir is empty")
	}
	canonicalProvider, err := NormalizeOAuthProvider(provider)
	if err != nil {
		return "", err
	}
	if err := ValidateOAuthState(state); err != nil {
		return "", err
	}

	fileName := fmt.Sprintf(".oauth-%s-%s.oauth", canonicalProvider, state)
	filePath := filepath.Join(authDir, fileName)
	if err := os.MkdirAll(authDir, 0o700); err != nil {
		return "", fmt.Errorf("create oauth callback dir: %w", err)
	}
	payload := oauthCallbackFilePayload{
		Code:  strings.TrimSpace(code),
		State: strings.TrimSpace(state),
		Error: strings.TrimSpace(errorMessage),
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("marshal oauth callback payload: %w", err)
	}
	if err := os.WriteFile(filePath, data, 0o600); err != nil {
		return "", fmt.Errorf("write oauth callback file: %w", err)
	}
	return filePath, nil
}

// WriteOAuthCallbackFileForPendingSession serializes callback publication against cancel.
func WriteOAuthCallbackFileForPendingSession(authDir, provider, state, code, errorMessage string) (string, error) {
	canonicalProvider, err := NormalizeOAuthProvider(provider)
	if err != nil {
		return "", err
	}
	record := oauthSessions.lookup(state)
	if record == nil {
		return "", errOAuthSessionNotPending
	}
	record.mu.Lock()
	defer record.mu.Unlock()
	oauthSessions.expireLocked(record)
	if record.session.Phase != "pending" || record.session.Source != oauthSessionSourceBuiltin ||
		!strings.EqualFold(record.session.Provider, canonicalProvider) {
		return "", errOAuthSessionNotPending
	}
	return WriteOAuthCallbackFile(authDir, canonicalProvider, state, code, errorMessage)
}
