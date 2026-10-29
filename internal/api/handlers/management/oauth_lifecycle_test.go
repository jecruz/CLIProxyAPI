package management

import (
	"context"
	"fmt"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
)

// TestOAuthCompletionRetainsExplicitSuccess retains success and rejects a late failure.
func TestOAuthCompletionRetainsExplicitSuccess(t *testing.T) {
	isolateOAuthSessions(t)
	store := newOAuthSessionStore(time.Minute)
	store.Register("success", "codex")
	store.Complete("success")
	session, ok := store.Get("success")
	if !ok || session.Phase != "succeeded" {
		t.Fatalf("success must be an explicit retained terminal record, got phase=%q present=%v", session.Phase, ok)
	}
	store.SetError("success", "late failure")
	session, _ = store.Get("success")
	if session.Phase != "succeeded" {
		t.Fatal("success was relabelled")
	}
}

// TestOAuthExpiryWithFakeClockIsNotSuccess uses a fake clock to distinguish expiry from success.
func TestOAuthExpiryWithFakeClockIsNotSuccess(t *testing.T) {
	isolateOAuthSessions(t)
	now := time.Now()
	store := newOAuthSessionStore(time.Minute)
	store.now = func() time.Time { return now }
	store.Register("expired", "codex")
	now = now.Add(5*time.Minute + 30*time.Second)
	session, ok := store.Get("expired")
	if !ok || session.Phase != "expired" || store.IsPending("expired", "codex") {
		t.Fatalf("expiry must remain a terminal record, got phase=%q present=%v", session.Phase, ok)
	}
}

// TestUnknownOAuthStatusIsNotSuccess rejects missing and unknown login state.
func TestUnknownOAuthStatusIsNotSuccess(t *testing.T) {
	isolateOAuthSessions(t)
	gin.SetMode(gin.TestMode)
	h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: t.TempDir()}, nil)
	for _, query := range []string{"", "?state=unknown-session-4362"} {
		response := httptest.NewRecorder()
		ctx, _ := gin.CreateTestContext(response)
		ctx.Request = httptest.NewRequest("GET", "/get-auth-status"+query, nil)
		h.GetAuthStatus(ctx)
		if strings.Contains(response.Body.String(), `"status":"ok"`) {
			t.Fatalf("missing/unknown state reported success: %s", response.Body.String())
		}
	}
}

// TestCancelWinsBeforeLateCallbackAndCredentialCommit preserves existing credentials after cancellation.
func TestCancelWinsBeforeLateCallbackAndCredentialCommit(t *testing.T) {
	isolateOAuthSessions(t)
	state := "cancel-wins-4362"
	RegisterOAuthSession(state, "codex")
	store := &memoryAuthStore{}
	existing := &coreauth.Auth{ID: "existing", Provider: "codex"}
	_, _ = store.Save(context.Background(), existing)
	handler := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: t.TempDir()}, nil)
	handler.tokenStore = store
	session, ok := oauthSessions.Cancel(state)
	if !ok || session.Phase != "cancelled" {
		t.Fatalf("cancel: %+v", session)
	}
	if _, err := handler.saveOAuthTokenRecord(oauthSessions.lookup(state).ctx, state, &coreauth.Auth{ID: "late", Provider: "codex"}); err == nil {
		t.Fatal("late credential save was allowed")
	}
	if _, err := WriteOAuthCallbackFileForPendingSession(handler.cfg.AuthDir, "codex", state, "synthetic-code", ""); err == nil {
		t.Fatal("late callback was accepted")
	}
	items, _ := store.List(context.Background())
	if len(items) != 1 || items[0].ID != "existing" {
		t.Fatal("cancel changed preexisting credentials")
	}
	repeated, _ := oauthSessions.Cancel(state)
	if repeated.Phase != "cancelled" {
		t.Fatal("repeated cancel changed outcome")
	}
}

// TestCredentialCommitWinsAndCancelCannotRelabel serializes a concurrent cancellation behind a successful commit.
func TestCredentialCommitWinsAndCancelCannotRelabel(t *testing.T) {
	isolateOAuthSessions(t)
	store := newOAuthSessionStore(time.Minute)
	store.Register("commit-wins", "codex")
	entered, release, committed := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	go func() {
		_, err := store.Commit("commit-wins", store.lookup("commit-wins").ctx, func(ctx context.Context) (string, error) {
			close(entered)
			<-release
			if ctx.Err() != nil {
				return "", ctx.Err()
			}
			return "saved", nil
		})
		committed <- err
	}()
	<-entered
	cancelled := make(chan oauthSession, 1)
	go func() { session, _ := store.Cancel("commit-wins"); cancelled <- session }()
	close(release)
	if err := <-committed; err != nil {
		t.Fatal(err)
	}
	if session := <-cancelled; session.Phase != "succeeded" {
		t.Fatalf("cancel relabelled success: %q", session.Phase)
	}
}

// TestOAuthCancellationIsIndependentAndCancelsContext wakes only the cancelled login worker.
func TestOAuthCancellationIsIndependentAndCancelsContext(t *testing.T) {
	isolateOAuthSessions(t)
	store := newOAuthSessionStore(time.Minute)
	ctx, err := store.RegisterContext("one", "codex", context.Background(), time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	store.Register("two", "codex")
	store.Cancel("one")
	select {
	case <-ctx.Done():
	default:
		t.Fatal("worker context did not cancel")
	}
	if !store.IsPending("two", "codex") {
		t.Fatal("cancel affected a different login")
	}
	if err := store.RegisterPlugin("plugin", "custom", nil); err != nil {
		t.Fatal(err)
	}
	store.Complete("two")
	session, _ := store.Get("plugin")
	if session.Phase != "pending" {
		t.Fatal("builtin completion affected plugin login")
	}
}

// TestExpiredOAuthCannotSaveAndTerminalRecordAgesOut prevents expired writes and bounds terminal retention.
func TestExpiredOAuthCannotSaveAndTerminalRecordAgesOut(t *testing.T) {
	isolateOAuthSessions(t)
	now := time.Now()
	store := newOAuthSessionStore(time.Minute)
	store.now = func() time.Time { return now }
	store.Register("expired-save", "codex")
	now = now.Add(5 * time.Minute)
	saved := false
	_, err := store.Commit("expired-save", store.lookup("expired-save").ctx, func(context.Context) (string, error) { saved = true; return "saved", nil })
	if err == nil || saved {
		t.Fatal("expired session saved credentials")
	}
	session, _ := store.Cancel("expired-save")
	if session.Phase != "expired" {
		t.Fatal("cancel relabelled expiry")
	}
	now = now.Add(time.Minute)
	if _, ok := store.Get("expired-save"); ok {
		t.Fatal("terminal record retained past TTL")
	}
}

// TestCancelEndpointDeletesOnlyOwnedCallbackAndReportsSuccessWinner qualifies state-bound cleanup and terminal outcome preservation.
func TestCancelEndpointDeletesOnlyOwnedCallbackAndReportsSuccessWinner(t *testing.T) {
	isolateOAuthSessions(t)
	gin.SetMode(gin.TestMode)
	directory := t.TempDir()
	handler := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: directory}, nil)
	RegisterOAuthSession("owned-4362", "codex")
	RegisterOAuthSession("other-4362", "codex")
	owned, err := WriteOAuthCallbackFileForPendingSession(directory, "codex", "owned-4362", "synthetic", "")
	if err != nil {
		t.Fatal(err)
	}
	other, err := WriteOAuthCallbackFileForPendingSession(directory, "codex", "other-4362", "synthetic", "")
	if err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(response)
	ctx.Request = httptest.NewRequest("POST", "/cancel-auth", strings.NewReader(fmt.Sprintf(`{"state":"owned-4362","attempt_token":%q}`, oauthSessions.lookup("owned-4362").session.generation.token)))
	ctx.Request.Header.Set("Content-Type", "application/json")
	handler.CancelAuth(ctx)
	if response.Code != 200 || !strings.Contains(response.Body.String(), `"phase":"cancelled"`) {
		t.Fatal(response.Body.String())
	}
	if _, err := os.Stat(owned); !os.IsNotExist(err) {
		t.Fatal("owned callback not removed")
	}
	if _, err := os.Stat(other); err != nil {
		t.Fatal("other session callback changed")
	}
	CompleteOAuthSession("other-4362")
	response = httptest.NewRecorder()
	ctx, _ = gin.CreateTestContext(response)
	ctx.Request = httptest.NewRequest("POST", "/cancel-auth", strings.NewReader(fmt.Sprintf(`{"state":"other-4362","attempt_token":%q}`, oauthSessions.lookup("other-4362").session.generation.token)))
	ctx.Request.Header.Set("Content-Type", "application/json")
	handler.CancelAuth(ctx)
	if !strings.Contains(response.Body.String(), `"phase":"succeeded"`) {
		t.Fatal("success lost cancellation race")
	}
}

// TestUntouchedExpiryRetentionUsesOriginalDeadline bounds old records without a prior poll.
func TestUntouchedExpiryRetentionUsesOriginalDeadline(t *testing.T) {
	isolateOAuthSessions(t)
	now := time.Now()
	store := newOAuthSessionStore(time.Minute)
	store.now = func() time.Time { return now }
	store.Register("untouched", "codex")
	now = now.Add(20 * time.Minute)
	if _, ok := store.Get("untouched"); ok {
		t.Fatal("untouched expiry retained past deadline plus TTL")
	}
	if _, err := store.RegisterContext("untouched", "codex", context.Background(), time.Minute); err != nil {
		t.Fatal("old state still reserved after terminal retention:", err)
	}
}

// TestPluginOAuthDeadlineHonorsProducer verifies long, short, and already-expired deadlines.
func TestPluginOAuthDeadlineHonorsProducer(t *testing.T) {
	isolateOAuthSessions(t)
	now := time.Now()
	store := newOAuthSessionStore(time.Minute)
	store.now = func() time.Time { return now }
	for _, duration := range []time.Duration{time.Minute, 20 * time.Minute} {
		state := duration.String()
		deadline := now.Add(duration)
		if err := store.RegisterPlugin(state, "plugin", nil, deadline); err != nil {
			t.Fatal(err)
		}
		session, _ := store.Get(state)
		if !session.ExpiresAt.Equal(deadline) {
			t.Fatalf("deadline=%s want %s", session.ExpiresAt, deadline)
		}
	}
	if err := store.RegisterPlugin("past", "plugin", nil, now.Add(-time.Second)); err == nil {
		t.Fatal("already-expired plugin flow accepted")
	}
}

// TestConcurrentPluginCompletionReportsWinningPhase rejects duplicate writes and contradictory responses.
func TestConcurrentPluginCompletionReportsWinningPhase(t *testing.T) {
	isolateOAuthSessions(t)
	gin.SetMode(gin.TestMode)
	state := "plugin-concurrent-4362"
	if err := RegisterPluginOAuthSession(state, "plugin", nil); err != nil {
		t.Fatal(err)
	}
	handler := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: t.TempDir()}, nil)
	handler.tokenStore = &memoryAuthStore{}
	responses := make(chan *httptest.ResponseRecorder, 2)
	for range 2 {
		go func() {
			response := httptest.NewRecorder()
			ctx, _ := gin.CreateTestContext(response)
			ctx.Request = httptest.NewRequest("GET", "/get-auth-status?state="+state, nil)
			handler.completePluginOAuthLogin(oauthSessions.lookup(state).ctx, ctx, state, "plugin", &coreauth.Auth{ID: "plugin", Provider: "plugin"})
			responses <- response
		}()
	}
	for range 2 {
		response := <-responses
		if !strings.Contains(response.Body.String(), `"phase":"succeeded"`) || !strings.Contains(response.Body.String(), `"status":"ok"`) {
			t.Fatal("plugin success winner was not serialized:", response.Body.String())
		}
	}
}

// TestStatelessAuthOverviewProbeStaysCompatible never claims a login succeeded.
func TestStatelessAuthOverviewProbeStaysCompatible(t *testing.T) {
	isolateOAuthSessions(t)
	gin.SetMode(gin.TestMode)
	handler := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: t.TempDir()}, nil)
	response := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(response)
	ctx.Request = httptest.NewRequest("GET", "/get-auth-status", nil)
	handler.GetAuthStatus(ctx)
	if response.Code != 200 || strings.Contains(response.Body.String(), `"status":"ok"`) {
		t.Fatal("auth-list overview probe rejected or reported success:", response.Code, response.Body.String())
	}
}

// TestAbsolutePluginDeadlineCannotDriftDuringCleanup preserves producer authority.
func TestAbsolutePluginDeadlineCannotDriftDuringCleanup(t *testing.T) {
	isolateOAuthSessions(t)
	start := time.Now()
	store := newOAuthSessionStore(time.Minute)
	calls := 0
	store.now = func() time.Time { calls++; return start.Add(time.Duration(calls) * time.Second) }
	deadline := start.Add(time.Hour)
	if err := store.RegisterPlugin("absolute", "plugin", nil, deadline); err != nil {
		t.Fatal(err)
	}
	session, _ := store.Get("absolute")
	if !session.ExpiresAt.Equal(deadline) {
		t.Fatalf("deadline drifted: got %s want %s", session.ExpiresAt, deadline)
	}
}

// TestLegacyOAuthSDKGettersKeepCompletionMeaning distinguishes terminal success from pending.
func TestLegacyOAuthSDKGettersKeepCompletionMeaning(t *testing.T) {
	isolateOAuthSessions(t)
	RegisterOAuthSession("legacy-completed-4362", "codex")
	CompleteOAuthSession("legacy-completed-4362")
	if _, _, ok := GetOAuthSession("legacy-completed-4362"); ok {
		t.Error("legacy getter exposes success as empty pending status")
	}
	if phase, ok := GetOAuthSessionPhase("legacy-completed-4362"); !ok || phase != "succeeded" {
		t.Fatal("phase-aware SDK getter lost retained success")
	}
	RegisterOAuthSession("legacy-provider-4362", "legacy-bulk")
	CompleteOAuthSessionsByProvider("legacy-bulk")
	session, _ := oauthSessions.Get("legacy-provider-4362")
	if session.Phase != "succeeded" {
		t.Fatalf("bulk completion changed meaning: %q", session.Phase)
	}
}

// TestPostPersistenceHookCannotRelabelCommittedCredentials keeps the saved outcome authoritative.
func TestPostPersistenceHookCannotRelabelCommittedCredentials(t *testing.T) {
	isolateOAuthSessions(t)
	state := "post-hook-4362"
	RegisterOAuthSession(state, "codex")
	handler := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: t.TempDir()}, nil)
	store := &memoryAuthStore{}
	handler.tokenStore = store
	handler.postAuthPersistHook = func(context.Context, *coreauth.Auth) error { return fmt.Errorf("synthetic post-save hook failure") }
	if _, err := handler.saveOAuthTokenRecord(oauthSessions.lookup(state).ctx, state, &coreauth.Auth{ID: "saved", Provider: "codex"}); err != nil {
		t.Error("saved credential reported failure:", err)
	}
	items, _ := store.List(context.Background())
	session, _ := oauthSessions.Cancel(state)
	if len(items) != 1 || session.Phase != "succeeded" {
		t.Fatalf("saved credentials relabelled: count=%d phase=%q", len(items), session.Phase)
	}
}

// TestConflictingProviderPollsSerializeBeforeCallingProvider includes the provider operation in the race.
func TestConflictingProviderPollsSerializeBeforeCallingProvider(t *testing.T) {
	isolateOAuthSessions(t)
	gin.SetMode(gin.TestMode)
	state := "poll-lock-4362"
	if err := RegisterPluginOAuthSession(state, "plugin", nil); err != nil {
		t.Fatal(err)
	}
	handler := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: t.TempDir()}, nil)
	observed, _ := oauthSessions.Get(state)
	entered, release, firstDone := make(chan struct{}), make(chan struct{}), make(chan struct{})
	contextFor := func() *gin.Context {
		response := httptest.NewRecorder()
		ctx, _ := gin.CreateTestContext(response)
		ctx.Request = httptest.NewRequest("GET", "/get-auth-status?state="+state, nil)
		return ctx
	}
	go func() {
		defer close(firstDone)
		handler.pollOAuthSession(contextFor(), state, observed, func(context.Context) { close(entered); <-release; CompleteOAuthSession(state) })
	}()
	<-entered
	secondStarted, secondDone := make(chan struct{}), make(chan struct{})
	called := atomic.Bool{}
	go func() {
		defer close(secondDone)
		close(secondStarted)
		handler.pollOAuthSession(contextFor(), state, observed, func(context.Context) { called.Store(true); SetOAuthSessionError(state, "conflicting provider failure") })
	}()
	<-secondStarted
	// The second provider callback must be excluded while the first is live.
	select {
	case <-secondDone:
	case <-time.After(20 * time.Millisecond):
	}
	close(release)
	<-firstDone
	<-secondDone
	session, _ := oauthSessions.Get(state)
	if called.Load() || session.Phase != "succeeded" {
		t.Fatalf("provider operation raced: secondCalled=%v phase=%q", called.Load(), session.Phase)
	}
}

// TestOldWorkerCannotCommitIntoReusedState binds persistence to the original context generation.
func TestOldWorkerCannotCommitIntoReusedState(t *testing.T) {
	isolateOAuthSessions(t)
	now := time.Now()
	store := newOAuthSessionStore(time.Minute)
	store.now = func() time.Time { return now }
	old, err := store.RegisterContext("reused", "codex", context.Background(), time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	store.Cancel("reused")
	now = now.Add(2 * time.Minute)
	if _, err := store.RegisterContext("reused", "codex", context.Background(), time.Minute); err != nil {
		t.Fatal(err)
	}
	called := false
	if _, err := store.Commit("reused", old, func(context.Context) (string, error) { called = true; return "saved", nil }); err == nil || called {
		t.Fatal("old cancelled worker persisted into a newer login generation")
	}
}

// TestStateReuseBetweenStatusLookupAndPollRejectsOldProvider rejects a stale initial observation.
func TestStateReuseBetweenStatusLookupAndPollRejectsOldProvider(t *testing.T) {
	isolateOAuthSessions(t)

	now := time.Now()
	oauthSessions.now = func() time.Time { return now }
	state := "reused-poll"
	if err := RegisterPluginOAuthSession(state, "old-provider", map[string]any{"flow": "old"}); err != nil {
		t.Fatal(err)
	}
	observed, _ := oauthSessions.Get(state)

	oauthSessions.Cancel(state)
	now = now.Add(oauthSessionTTL + time.Minute)
	if err := RegisterPluginOAuthSession(state, "new-provider", map[string]any{"flow": "new"}); err != nil {
		t.Fatal(err)
	}
	handler := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: t.TempDir()}, nil)
	store := &memoryAuthStore{}
	handler.tokenStore = store
	response := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(response)
	c.Request = httptest.NewRequest("GET", "/get-auth-status?state="+state, nil)
	called := false
	handler.pollOAuthSession(c, state, observed, func(ctx context.Context) {
		called = true
		_, _ = handler.saveOAuthTokenRecord(ctx, state, &coreauth.Auth{ID: "old", Provider: "old-provider"})
		SetOAuthSessionError(state, "old provider failure")
	})
	items, _ := store.List(context.Background())
	replacement, _ := oauthSessions.Get(state)
	if called || len(items) != 0 || replacement.Phase != "pending" || replacement.Status != "" {
		t.Fatalf("stale poll affected replacement: called=%v saved=%d phase=%s error=%q", called, len(items), replacement.Phase, replacement.Status)
	}
}

// TestCompleteProviderSnapshotCannotCompleteReplacement exercises a delayed captured provider batch.
func TestCompleteProviderSnapshotCannotCompleteReplacement(t *testing.T) {
	isolateOAuthSessions(t)
	store := newOAuthSessionStore(time.Minute)
	now := time.Now()
	store.now = func() time.Time { return now }
	store.Register("provider-reused", "old-provider")
	captured := map[string]*oauthSessionGeneration{"provider-reused": store.lookup("provider-reused").session.generation}
	store.Cancel("provider-reused")
	now = now.Add(2 * time.Minute)
	store.Register("provider-reused", "new-provider")
	completed := store.completeProviderRecords(captured)
	replacement, _ := store.Get("provider-reused")
	if completed != 0 || replacement.Phase != "pending" {
		t.Fatalf("provider batch completed replacement: count=%d phase=%s", completed, replacement.Phase)
	}
}

// isolateOAuthSessions gives serial tests independent retained state and cancels test contexts.
func isolateOAuthSessions(t *testing.T) {
	t.Helper()
	original := oauthSessions
	local := newOAuthSessionStore(oauthSessionTTL)
	oauthSessions = local
	t.Cleanup(func() {
		for _, record := range local.sessions {
			record.cancel()
		}
		oauthSessions = original
	})
}

// TestInFlightOldProviderCannotMutateReplacement checks every late terminal path.
func TestInFlightOldProviderCannotMutateReplacement(t *testing.T) {
	isolateOAuthSessions(t)
	now := time.Now()
	oauthSessions.now = func() time.Time { return now }
	state := "inflight-reused"
	if err := RegisterPluginOAuthSession(state, "same-provider", map[string]any{"flow": "old"}); err != nil {
		t.Fatal(err)
	}
	observed, _ := oauthSessions.Get(state)
	handler := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: t.TempDir()}, nil)
	store := &memoryAuthStore{}
	handler.tokenStore = store
	response := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(response)
	c.Request = httptest.NewRequest("GET", "/get-auth-status?state="+state, nil)
	handler.pollOAuthSession(c, state, observed, func(owner context.Context) {
		oauthSessions.Cancel(state)
		now = now.Add(oauthSessionTTL + time.Minute)
		if err := RegisterPluginOAuthSession(state, "same-provider", map[string]any{"flow": "new"}); err != nil {
			t.Fatal(err)
		}
		oauthSessions.SetErrorContext(state, owner, "late error")
		oauthSessions.CompleteContext(state, owner)
		handler.completePluginOAuthLogin(owner, c, state, "same-provider", &coreauth.Auth{ID: "old", Provider: "same-provider"})
	})
	replacement, _ := oauthSessions.Get(state)
	items, _ := store.List(context.Background())
	if len(items) != 0 || replacement.Phase != "pending" || replacement.Status != "" || replacement.Metadata["flow"] != "new" {
		t.Fatalf("old generation mutated replacement: saved=%d session=%+v", len(items), replacement)
	}
	if !strings.Contains(response.Body.String(), `"phase":"unknown"`) {
		t.Fatalf("stale request reported replacement outcome: %s", response.Body.String())
	}
}

// TestDelayedCancelCannotTouchReplacement rejects an old client's cancellation after state reuse.
func TestDelayedCancelCannotTouchReplacement(t *testing.T) {
	isolateOAuthSessions(t)
	store := oauthSessions
	now := time.Now()
	store.now = func() time.Time { return now }
	oldOwner := store.Register("reused-cancel", "codex")
	oldToken := oauthAttemptToken(oldOwner)
	store.Cancel("reused-cancel")
	now = now.Add(2 * store.ttl)
	replacement := store.Register("reused-cancel", "codex")
	if oauthAttemptToken(replacement) == oldToken {
		t.Fatal("replacement reused owner token")
	}
	response := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(response)
	ctx.Request = httptest.NewRequest("POST", "/cancel-auth", strings.NewReader(fmt.Sprintf(`{"state":"reused-cancel","attempt_token":%q}`, oldToken)))
	ctx.Request.Header.Set("Content-Type", "application/json")
	handler := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: t.TempDir()}, nil)
	handler.CancelAuth(ctx)
	session, _ := store.Get("reused-cancel")
	if response.Code == 200 || session.Phase != "pending" {
		t.Fatalf("stale cancel changed replacement: code=%d phase=%s", response.Code, session.Phase)
	}
	if session, ok := store.CancelOwned("reused-cancel", oauthAttemptToken(replacement)); !ok || session.Phase != "cancelled" {
		t.Fatal("current owner cannot cancel replacement")
	}
}

// TestRegistrationDoesNotBlockOnSaveHook allows a credential save hook to register another login.
func TestRegistrationDoesNotBlockOnSaveHook(t *testing.T) {
	store := newOAuthSessionStore(time.Minute)
	owner := store.Register("saving", "codex")
	done := make(chan error, 1)
	go func() {
		_, err := store.Commit("saving", owner, func(context.Context) (string, error) {
			_, err := store.RegisterContext("from-hook", "codex", context.Background(), time.Minute)
			if err == nil {
				store.lookup("from-hook").cancel()
			}
			return "synthetic", err
		})
		done <- err
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("registration deadlocked behind its save hook's commit lock")
	}
}

// TestCancelRequiresOwnerForBuiltinAndPlugin refuses absent or wrong ownership without touching either flow.
func TestCancelRequiresOwnerForBuiltinAndPlugin(t *testing.T) {
	for _, plugin := range []bool{false, true} {
		store := newOAuthSessionStore(time.Minute)
		var owner context.Context
		var err error
		if plugin {
			owner, err = store.RegisterPluginContext("owned", "plugin", nil)
		} else {
			owner, err = store.RegisterContext("owned", "codex", context.Background(), time.Minute)
		}
		if err != nil {
			t.Fatal(err)
		}
		for _, token := range []string{"", "wrong-owner"} {
			if _, ok := store.CancelOwned("owned", token); ok {
				t.Fatal("unowned cancel accepted")
			}
			if session, _ := store.Get("owned"); session.Phase != "pending" {
				t.Fatal("unowned cancel changed flow")
			}
		}
		if len(oauthAttemptToken(owner)) != 64 {
			t.Fatal("start did not receive an opaque owner capability")
		}
		if session, ok := store.CancelOwned("owned", oauthAttemptToken(owner)); !ok || session.Phase != "cancelled" {
			t.Fatal("owned cancel refused")
		}
	}
}
