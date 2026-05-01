package app

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"nexus/internal/adapters/email"
	"nexus/internal/adapters/storage"
	"nexus/internal/adapters/webchat"
	"nexus/internal/config"
	"nexus/internal/domain"
	"nexus/internal/ports"
	"nexus/internal/services"
)

type webAuthStub struct {
	challenges map[string]domain.WebAuthChallenge
	sessions   map[string]domain.WebAuthSession
}

func (s *webAuthStub) CreateWebAuthChallenge(_ context.Context, challenge domain.WebAuthChallenge, _ time.Duration) error {
	if s.challenges == nil {
		s.challenges = map[string]domain.WebAuthChallenge{}
	}
	s.challenges[challenge.Email] = challenge
	return nil
}

func (s *webAuthStub) ConsumeWebAuthChallengeByOTP(_ context.Context, _ string, email, otpHash string, now time.Time) (domain.WebAuthChallenge, error) {
	challenge, ok := s.challenges[email]
	if !ok || challenge.OTPHash != otpHash {
		return domain.WebAuthChallenge{}, domain.ErrWebAuthChallengeNotFound
	}
	if now.After(challenge.ExpiresAt) {
		return domain.WebAuthChallenge{}, domain.ErrWebAuthChallengeExpired
	}
	challenge.ConsumedAt = now
	s.challenges[email] = challenge
	return challenge, nil
}

func (s *webAuthStub) ConsumeWebAuthChallengeByLink(_ context.Context, _ string, linkTokenHash string, now time.Time) (domain.WebAuthChallenge, error) {
	for email, challenge := range s.challenges {
		if challenge.LinkTokenHash == linkTokenHash {
			challenge.ConsumedAt = now
			s.challenges[email] = challenge
			return challenge, nil
		}
	}
	return domain.WebAuthChallenge{}, domain.ErrWebAuthChallengeNotFound
}

func (s *webAuthStub) CreateWebAuthSession(_ context.Context, session domain.WebAuthSession) error {
	if s.sessions == nil {
		s.sessions = map[string]domain.WebAuthSession{}
	}
	s.sessions[session.ID] = session
	return nil
}

func (s *webAuthStub) GetWebAuthSession(_ context.Context, sessionID string, now time.Time) (domain.WebAuthSession, error) {
	session, ok := s.sessions[sessionID]
	if !ok || now.After(session.ExpiresAt) {
		return domain.WebAuthSession{}, domain.ErrWebAuthSessionNotFound
	}
	return session, nil
}

func (s *webAuthStub) UpdateWebAuthSessionCSRFHash(_ context.Context, sessionID, csrfHash string, now time.Time) error {
	session := s.sessions[sessionID]
	session.CSRFTokenHash = csrfHash
	session.LastSeenAt = now
	s.sessions[sessionID] = session
	return nil
}

func (s *webAuthStub) DeleteWebAuthSession(_ context.Context, sessionID string) error {
	delete(s.sessions, sessionID)
	return nil
}

type webchatRepoStub struct {
	appRepoStub
	session domain.Session
}

func (r *webchatRepoStub) InTx(ctx context.Context, fn func(context.Context, ports.Repository) error) error {
	return fn(ctx, r)
}

type identityStub struct {
	users      map[string]domain.User
	identities map[string]domain.LinkedIdentity
	challenges map[string]domain.StepUpChallenge
}

func (s *identityStub) EnsureUserByEmail(_ context.Context, tenantID, email string) (domain.User, error) {
	if s.users == nil {
		s.users = map[string]domain.User{}
	}
	key := tenantID + "|" + email
	if user, ok := s.users[key]; ok {
		return user, nil
	}
	user := domain.User{ID: "user_" + email, TenantID: tenantID, PrimaryEmail: email, PrimaryEmailVerified: true, CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC()}
	s.users[key] = user
	return user, nil
}
func (s *identityStub) GetUser(_ context.Context, tenantID, userID string) (domain.User, error) {
	for _, user := range s.users {
		if user.TenantID == tenantID && user.ID == userID {
			return user, nil
		}
	}
	return domain.User{}, domain.ErrIdentityUserNotFound
}
func (s *identityStub) GetUserByEmail(_ context.Context, tenantID, email string) (domain.User, error) {
	return s.EnsureUserByEmail(context.Background(), tenantID, email)
}
func (s *identityStub) ListUsers(_ context.Context, tenantID string, _ int) ([]domain.User, error) {
	var out []domain.User
	for _, user := range s.users {
		if user.TenantID == tenantID {
			out = append(out, user)
		}
	}
	return out, nil
}
func (s *identityStub) UpdateUserPhone(_ context.Context, tenantID, userID, rawPhone, normalizedPhone string, verified bool, addedAt time.Time) error {
	for key, user := range s.users {
		if user.TenantID == tenantID && user.ID == userID {
			user.PrimaryPhone = rawPhone
			user.PrimaryPhoneNormalized = normalizedPhone
			user.PrimaryPhoneVerified = verified
			user.PrimaryPhoneAddedAt = addedAt
			user.UpdatedAt = time.Now().UTC()
			s.users[key] = user
			return nil
		}
	}
	return domain.ErrIdentityUserNotFound
}
func (s *identityStub) ClearUserPhone(_ context.Context, tenantID, userID string) error {
	for key, user := range s.users {
		if user.TenantID == tenantID && user.ID == userID {
			user.PrimaryPhone = ""
			user.PrimaryPhoneNormalized = ""
			user.PrimaryPhoneVerified = false
			user.PrimaryPhoneAddedAt = time.Time{}
			user.UpdatedAt = time.Now().UTC()
			s.users[key] = user
			return nil
		}
	}
	return domain.ErrIdentityUserNotFound
}
func (s *identityStub) MarkUserStepUp(_ context.Context, tenantID, userID string, at time.Time) error {
	for key, user := range s.users {
		if user.TenantID == tenantID && user.ID == userID {
			user.LastStepUpAt = at
			s.users[key] = user
			return nil
		}
	}
	return domain.ErrIdentityUserNotFound
}
func (s *identityStub) HasRecentStepUp(_ context.Context, tenantID, userID string, since time.Time) (bool, error) {
	user, err := s.GetUser(context.Background(), tenantID, userID)
	if err != nil {
		return false, err
	}
	return !user.LastStepUpAt.IsZero() && !user.LastStepUpAt.Before(since), nil
}
func (s *identityStub) CreateStepUpChallenge(_ context.Context, challenge domain.StepUpChallenge, _ time.Duration) error {
	if s.challenges == nil {
		s.challenges = map[string]domain.StepUpChallenge{}
	}
	s.challenges[challenge.UserID+"|"+challenge.Purpose+"|"+challenge.ChannelType] = challenge
	return nil
}
func (s *identityStub) ConsumeStepUpChallenge(_ context.Context, tenantID, userID, purpose, channelType, codeHash string, now time.Time) (domain.StepUpChallenge, error) {
	for key, challenge := range s.challenges {
		if challenge.TenantID == tenantID && challenge.Purpose == purpose && challenge.ChannelType == channelType && challenge.CodeHash == codeHash && challenge.UserID == userID {
			challenge.ConsumedAt = now
			s.challenges[key] = challenge
			return challenge, nil
		}
	}
	return domain.StepUpChallenge{}, domain.ErrStepUpChallengeNotFound
}
func (s *identityStub) UpsertLinkedIdentity(_ context.Context, identity domain.LinkedIdentity) error {
	if s.identities == nil {
		s.identities = map[string]domain.LinkedIdentity{}
	}
	s.identities[identity.TenantID+"|"+identity.ChannelType+"|"+identity.ChannelUserID] = identity
	return nil
}
func (s *identityStub) GetLinkedIdentity(_ context.Context, tenantID, channelType, channelUserID string) (domain.LinkedIdentity, error) {
	item, ok := s.identities[tenantID+"|"+channelType+"|"+channelUserID]
	if !ok {
		return domain.LinkedIdentity{}, domain.ErrLinkedIdentityNotFound
	}
	return item, nil
}
func (s *identityStub) ListLinkedIdentitiesForUser(_ context.Context, tenantID, userID string) ([]domain.LinkedIdentity, error) {
	var out []domain.LinkedIdentity
	for _, item := range s.identities {
		if item.TenantID == tenantID && item.UserID == userID {
			out = append(out, item)
		}
	}
	return out, nil
}
func (s *identityStub) DeleteLinkedIdentity(_ context.Context, tenantID, channelType, channelUserID string) error {
	delete(s.identities, tenantID+"|"+channelType+"|"+channelUserID)
	return nil
}
func (s *identityStub) GetTrustPolicy(context.Context, string, string) (domain.TrustPolicy, error) {
	return domain.TrustPolicy{}, domain.ErrTrustPolicyNotFound
}
func (s *identityStub) ListTrustPolicies(context.Context, string, int) ([]domain.TrustPolicy, error) {
	return nil, nil
}
func (s *identityStub) UpsertTrustPolicy(context.Context, domain.TrustPolicy) error { return nil }
func (s *identityStub) CountLinkedIdentitiesByChannel(_ context.Context, tenantID string) (map[string]int, error) {
	out := map[string]int{}
	for _, item := range s.identities {
		if item.TenantID == tenantID {
			out[item.ChannelType]++
		}
	}
	return out, nil
}

func (r *webchatRepoStub) ResolveSession(context.Context, domain.CanonicalInboundEvent, string) (domain.Session, bool, error) {
	if r.session.ID == "" {
		r.session = domain.Session{ID: "session_webchat_1", TenantID: "tenant_default", OwnerUserID: "user@example.com", ChannelType: "webchat", ChannelScopeKey: "websess_1:session_webchat_1", State: "open"}
	}
	return r.session, false, nil
}

func (r *webchatRepoStub) GetArtifactForSession(_ context.Context, tenantID, sessionID, artifactID string) (domain.Artifact, error) {
	session, _, _ := r.ResolveSession(context.Background(), domain.CanonicalInboundEvent{}, "")
	if session.TenantID != tenantID || session.ID != sessionID {
		return domain.Artifact{}, domain.ErrArtifactNotFound
	}
	for _, artifact := range r.sessionDetail.Artifacts {
		if artifact.ID == artifactID {
			return artifact, nil
		}
	}
	return domain.Artifact{}, domain.ErrArtifactNotFound
}

func TestWebChatAuthRequestStoresChallenge(t *testing.T) {
	auth := &webAuthStub{}
	identity := &identityStub{}
	app := &App{
		Config:   config.Config{DefaultTenantID: "tenant_default", WebChatOTPMinutes: 10, WebChatCookieName: "nexus_webchat_session"},
		WebAuth:  auth,
		Identity: identity,
		Email:    email.New("secret", "", "", "", "nexus@example.com"),
	}
	req := httptest.NewRequest(http.MethodPost, "/webchat/auth/request", strings.NewReader(`{"email":"user@example.com"}`))
	rec := httptest.NewRecorder()

	app.handleWebChatAuthRequest(rec, req)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d body=%s", rec.Code, rec.Body.String())
	}
	if auth.challenges["user@example.com"].Email != "user@example.com" {
		t.Fatalf("expected stored challenge, got %+v", auth.challenges)
	}
}

func TestWebChatDevSessionDisabledByDefault(t *testing.T) {
	app := &App{
		Config:  config.Config{DefaultTenantID: "tenant_default", WebChatCookieName: "nexus_webchat_session", WebChatSessionHours: 24},
		WebAuth: &webAuthStub{},
	}
	req := httptest.NewRequest(http.MethodPost, "/webchat/dev/session", strings.NewReader(`{"email":"user@example.com"}`))
	rec := httptest.NewRecorder()

	app.handleWebChatDevSession(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestWebChatDevSessionDisabledInProduction(t *testing.T) {
	app := &App{
		Config:  config.Config{Environment: "production", WebChatDevAuth: true, DefaultTenantID: "tenant_default", WebChatCookieName: "nexus_webchat_session", WebChatSessionHours: 24},
		WebAuth: &webAuthStub{},
	}
	req := httptest.NewRequest(http.MethodPost, "/webchat/dev/session", strings.NewReader(`{"email":"user@example.com"}`))
	rec := httptest.NewRecorder()

	app.handleWebChatDevSession(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestWebChatDevSessionDisabledOutsideDevelopment(t *testing.T) {
	app := &App{
		Config:  config.Config{Environment: "staging", WebChatDevAuth: true, DefaultTenantID: "tenant_default", WebChatCookieName: "nexus_webchat_session", WebChatSessionHours: 24},
		WebAuth: &webAuthStub{},
	}
	req := httptest.NewRequest(http.MethodPost, "http://127.0.0.1/webchat/dev/session", strings.NewReader(`{"email":"user@example.com"}`))
	rec := httptest.NewRecorder()

	app.handleWebChatDevSession(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestWebChatDevSessionDisabledForNonLocalHost(t *testing.T) {
	app := &App{
		Config:  config.Config{Environment: "development", WebChatDevAuth: true, DefaultTenantID: "tenant_default", WebChatCookieName: "nexus_webchat_session", WebChatSessionHours: 24},
		WebAuth: &webAuthStub{},
	}
	req := httptest.NewRequest(http.MethodPost, "http://nexus.internal/webchat/dev/session", strings.NewReader(`{"email":"user@example.com"}`))
	rec := httptest.NewRecorder()

	app.handleWebChatDevSession(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestWebChatDevSessionCreatesSessionCookieAndCSRF(t *testing.T) {
	auth := &webAuthStub{}
	app := &App{
		Config: config.Config{
			Environment:           "development",
			WebChatDevAuth:        true,
			DefaultTenantID:       "tenant_default",
			DefaultAgentProfileID: "agent_profile_default",
			WebChatCookieName:     "nexus_webchat_session",
			WebChatSessionHours:   24,
		},
		WebAuth:  auth,
		Identity: &identityStub{},
		Repo:     &webchatRepoStub{},
	}
	req := httptest.NewRequest(http.MethodPost, "http://127.0.0.1/webchat/dev/session", strings.NewReader(`{"email":"User@Example.COM"}`))
	rec := httptest.NewRecorder()

	app.handleWebChatDevSession(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	cookies := rec.Result().Cookies()
	if len(cookies) != 1 || cookies[0].Name != "nexus_webchat_session" || cookies[0].Value == "" {
		t.Fatalf("expected auth cookie, got %+v", cookies)
	}
	var payload struct {
		Data struct {
			Email     string `json:"email"`
			SessionID string `json:"session_id"`
			CSRFToken string `json:"csrf_token"`
			UserID    string `json:"user_id"`
		} `json:"data"`
		Meta struct {
			Mode string `json:"mode"`
		} `json:"meta"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&payload); err != nil {
		t.Fatal(err)
	}
	if payload.Data.Email != "user@example.com" || payload.Data.SessionID == "" || payload.Data.CSRFToken == "" || payload.Data.UserID == "" || payload.Meta.Mode != "dev" {
		t.Fatalf("unexpected dev session payload: %+v", payload)
	}
	session := auth.sessions[cookies[0].Value]
	if session.CSRFTokenHash == "" {
		t.Fatalf("expected csrf hash to be stored: %+v", session)
	}
}

func TestWebChatArtifactRequiresAuth(t *testing.T) {
	app := &App{
		Config: config.Config{
			DefaultTenantID:       "tenant_default",
			DefaultAgentProfileID: "agent_profile_default",
			WebChatCookieName:     "nexus_webchat_session",
		},
		WebAuth: &webAuthStub{},
		Repo:    &webchatRepoStub{},
	}
	req := httptest.NewRequest(http.MethodGet, "/webchat/artifacts/artifact_1", nil)
	rec := httptest.NewRecorder()

	app.handleWebChatArtifact(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestWebChatArtifactServesOwnedLocalArtifact(t *testing.T) {
	root := t.TempDir()
	storageURI, err := storage.New("file://"+root).Save(context.Background(), "artifacts/test.png", []byte("pngdata"))
	if err != nil {
		t.Fatal(err)
	}
	auth := &webAuthStub{
		sessions: map[string]domain.WebAuthSession{
			"websess_1": {
				ID:        "websess_1",
				TenantID:  "tenant_default",
				Email:     "user@example.com",
				ExpiresAt: time.Now().UTC().Add(time.Hour),
			},
		},
	}
	repo := &webchatRepoStub{
		session: domain.Session{ID: "session_webchat_1", TenantID: "tenant_default", OwnerUserID: "user@example.com", ChannelType: "webchat", State: "open"},
	}
	repo.sessionDetail = domain.SessionDetail{
		Artifacts: []domain.Artifact{{
			ID:         "artifact_1",
			MessageID:  "msg_1",
			Name:       "test.png",
			MIMEType:   "image/png",
			StorageURI: storageURI,
		}},
	}
	app := &App{
		Config: config.Config{
			DefaultTenantID:       "tenant_default",
			DefaultAgentProfileID: "agent_profile_default",
			WebChatCookieName:     "nexus_webchat_session",
		},
		WebAuth:   auth,
		Repo:      repo,
		Artifacts: services.ArtifactService{Store: storage.New("file://" + root)},
	}
	req := httptest.NewRequest(http.MethodGet, "/webchat/artifacts/artifact_1", nil)
	req.AddCookie(&http.Cookie{Name: "nexus_webchat_session", Value: "websess_1"})
	rec := httptest.NewRecorder()

	app.handleWebChatArtifact(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Content-Type"); got != "image/png" {
		t.Fatalf("expected image/png content type, got %q", got)
	}
	if got := rec.Header().Get("Content-Disposition"); !strings.HasPrefix(got, "inline;") {
		t.Fatalf("expected inline disposition, got %q", got)
	}
	if body := rec.Body.String(); body != "pngdata" {
		t.Fatalf("unexpected artifact body %q", body)
	}
}

func TestWebChatArtifactRejectsMissingArtifact(t *testing.T) {
	auth := &webAuthStub{
		sessions: map[string]domain.WebAuthSession{
			"websess_1": {
				ID:        "websess_1",
				TenantID:  "tenant_default",
				Email:     "user@example.com",
				ExpiresAt: time.Now().UTC().Add(time.Hour),
			},
		},
	}
	app := &App{
		Config: config.Config{
			DefaultTenantID:       "tenant_default",
			DefaultAgentProfileID: "agent_profile_default",
			WebChatCookieName:     "nexus_webchat_session",
		},
		WebAuth: auth,
		Repo: &webchatRepoStub{
			appRepoStub: appRepoStub{sessionDetail: domain.SessionDetail{}},
			session:     domain.Session{ID: "session_webchat_1", TenantID: "tenant_default", OwnerUserID: "user@example.com", ChannelType: "webchat", State: "open"},
		},
	}
	req := httptest.NewRequest(http.MethodGet, "/webchat/artifacts/artifact_missing", nil)
	req.AddCookie(&http.Cookie{Name: "nexus_webchat_session", Value: "websess_1"})
	rec := httptest.NewRecorder()

	app.handleWebChatArtifact(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestWebChatArtifactRedirectsRemoteURL(t *testing.T) {
	auth := &webAuthStub{
		sessions: map[string]domain.WebAuthSession{
			"websess_1": {
				ID:        "websess_1",
				TenantID:  "tenant_default",
				Email:     "user@example.com",
				ExpiresAt: time.Now().UTC().Add(time.Hour),
			},
		},
	}
	repo := &webchatRepoStub{
		session: domain.Session{ID: "session_webchat_1", TenantID: "tenant_default", OwnerUserID: "user@example.com", ChannelType: "webchat", State: "open"},
	}
	repo.sessionDetail = domain.SessionDetail{
		Artifacts: []domain.Artifact{{
			ID:        "artifact_remote",
			MessageID: "msg_1",
			Name:      "notes.txt",
			MIMEType:  "text/plain",
			SourceURL: "https://cdn.example.com/notes.txt",
		}},
	}
	app := &App{
		Config: config.Config{
			DefaultTenantID:       "tenant_default",
			DefaultAgentProfileID: "agent_profile_default",
			WebChatCookieName:     "nexus_webchat_session",
		},
		WebAuth: auth,
		Repo:    repo,
	}
	req := httptest.NewRequest(http.MethodGet, "/webchat/artifacts/artifact_remote", nil)
	req.AddCookie(&http.Cookie{Name: "nexus_webchat_session", Value: "websess_1"})
	rec := httptest.NewRecorder()

	app.handleWebChatArtifact(rec, req)

	if rec.Code != http.StatusTemporaryRedirect {
		t.Fatalf("expected 307, got %d body=%s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Location"); got != "https://cdn.example.com/notes.txt" {
		t.Fatalf("expected redirect to remote artifact, got %q", got)
	}
}

func TestWebChatAuthVerifySetsSecureCookieForForwardedHTTPS(t *testing.T) {
	auth := &webAuthStub{
		challenges: map[string]domain.WebAuthChallenge{
			"user@example.com": {
				ID:        "challenge_1",
				TenantID:  "tenant_default",
				Email:     "user@example.com",
				OTPHash:   sha256Hex("123456"),
				ExpiresAt: time.Now().UTC().Add(5 * time.Minute),
			},
		},
	}
	app := &App{
		Config:   config.Config{DefaultTenantID: "tenant_default", WebChatSessionHours: 24, WebChatCookieName: "nexus_webchat_session"},
		WebAuth:  auth,
		Identity: &identityStub{},
	}
	req := httptest.NewRequest(http.MethodPost, "/webchat/auth/verify", strings.NewReader(`{"email":"user@example.com","code":"123456"}`))
	req.Header.Set("X-Forwarded-Proto", "https")
	rec := httptest.NewRecorder()

	app.handleWebChatAuthVerify(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	cookies := rec.Result().Cookies()
	if len(cookies) == 0 || !cookies[0].Secure {
		t.Fatalf("expected secure auth cookie, got %+v", cookies)
	}
}

func TestWebChatAuthVerifySetsCookie(t *testing.T) {
	auth := &webAuthStub{
		challenges: map[string]domain.WebAuthChallenge{
			"user@example.com": {
				ID:        "challenge_1",
				TenantID:  "tenant_default",
				Email:     "user@example.com",
				OTPHash:   sha256Hex("123456"),
				ExpiresAt: time.Now().UTC().Add(5 * time.Minute),
			},
		},
	}
	app := &App{
		Config:   config.Config{DefaultTenantID: "tenant_default", WebChatSessionHours: 24, WebChatCookieName: "nexus_webchat_session"},
		WebAuth:  auth,
		Identity: &identityStub{},
	}
	req := httptest.NewRequest(http.MethodPost, "/webchat/auth/verify", strings.NewReader(`{"email":"user@example.com","code":"123456"}`))
	rec := httptest.NewRecorder()

	app.handleWebChatAuthVerify(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	if len(rec.Result().Cookies()) == 0 {
		t.Fatal("expected auth cookie")
	}
}

func TestWebChatBootstrapReturnsTimelineAndCSRF(t *testing.T) {
	auth := &webAuthStub{
		sessions: map[string]domain.WebAuthSession{
			"websess_1": {
				ID:        "websess_1",
				TenantID:  "tenant_default",
				Email:     "user@example.com",
				ExpiresAt: time.Now().UTC().Add(time.Hour),
			},
		},
	}
	repo := &webchatRepoStub{}
	repo.sessionDetail = domain.SessionDetail{
		Session: repo.session,
		Messages: []domain.Message{
			{MessageID: "msg_user", Direction: "inbound", Role: "user", Text: "hello"},
			{MessageID: "msg_bot", Direction: "outbound", Role: "assistant", Text: "hi", RawPayload: []byte(`{"is_partial":true}`)},
		},
		Runs: []domain.Run{{ID: "run_1", SessionID: "session_web", Status: "running", StartedAt: time.Now().UTC(), LastEventAt: time.Now().UTC()}},
	}
	app := &App{
		Config:   config.Config{DefaultTenantID: "tenant_default", DefaultAgentProfileID: "agent_profile_default", WebChatCookieName: "nexus_webchat_session", WebChatInteractionVisibility: "simple"},
		Repo:     repo,
		WebAuth:  auth,
		Identity: &identityStub{},
	}
	req := httptest.NewRequest(http.MethodGet, "/webchat/bootstrap", nil)
	req.AddCookie(&http.Cookie{Name: "nexus_webchat_session", Value: "websess_1"})
	rec := httptest.NewRecorder()

	app.handleWebChatBootstrap(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	var payload struct {
		Data struct {
			Email          string                  `json:"email"`
			CSRFToken      string                  `json:"csrf_token"`
			Items          []domain.WebChatItem    `json:"items"`
			Activity       *domain.WebChatActivity `json:"activity"`
			VisibilityMode string                  `json:"visibility_mode"`
		} `json:"data"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&payload); err != nil {
		t.Fatal(err)
	}
	if payload.Data.Email != "user@example.com" || payload.Data.CSRFToken == "" || len(payload.Data.Items) != 2 {
		t.Fatalf("unexpected bootstrap payload %+v", payload)
	}
	if !payload.Data.Items[0].Partial {
		t.Fatalf("expected latest assistant item to be partial, got %+v", payload.Data.Items[0])
	}
	if payload.Data.Activity == nil || payload.Data.Activity.Phase != "typing" {
		t.Fatalf("expected typing activity, got %+v", payload.Data.Activity)
	}
	if payload.Data.VisibilityMode != "simple" {
		t.Fatalf("expected simple visibility mode, got %+v", payload)
	}
}

func TestWebChatBootstrapIncludesPhone(t *testing.T) {
	auth := &webAuthStub{
		sessions: map[string]domain.WebAuthSession{
			"websess_1": {
				ID:        "websess_1",
				TenantID:  "tenant_default",
				Email:     "user@example.com",
				ExpiresAt: time.Now().UTC().Add(time.Hour),
			},
		},
	}
	identity := &identityStub{
		users: map[string]domain.User{
			"tenant_default|user@example.com": {
				ID:                     "user_user@example.com",
				TenantID:               "tenant_default",
				PrimaryEmail:           "user@example.com",
				PrimaryEmailVerified:   true,
				PrimaryPhone:           "+628123456789",
				PrimaryPhoneNormalized: "+628123456789",
				CreatedAt:              time.Now().UTC(),
				UpdatedAt:              time.Now().UTC(),
			},
		},
	}
	repo := &webchatRepoStub{}
	app := &App{
		Config:   config.Config{DefaultTenantID: "tenant_default", DefaultAgentProfileID: "agent_profile_default", WebChatCookieName: "nexus_webchat_session"},
		Repo:     repo,
		WebAuth:  auth,
		Identity: identity,
	}
	req := httptest.NewRequest(http.MethodGet, "/webchat/bootstrap", nil)
	req.AddCookie(&http.Cookie{Name: "nexus_webchat_session", Value: "websess_1"})
	rec := httptest.NewRecorder()

	app.handleWebChatBootstrap(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	var payload struct {
		Data struct {
			PrimaryPhone string `json:"primary_phone"`
		} `json:"data"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&payload); err != nil {
		t.Fatal(err)
	}
	if payload.Data.PrimaryPhone != "+628123456789" {
		t.Fatalf("unexpected phone payload %+v", payload)
	}
}

func TestWebChatIdentityPhoneUpdateAndDelete(t *testing.T) {
	auth := &webAuthStub{
		sessions: map[string]domain.WebAuthSession{
			"websess_1": {
				ID:            "websess_1",
				TenantID:      "tenant_default",
				Email:         "user@example.com",
				CSRFTokenHash: sha256Hex("csrf-token"),
				ExpiresAt:     time.Now().UTC().Add(time.Hour),
			},
		},
	}
	identity := &identityStub{}
	app := &App{
		Config:   config.Config{DefaultTenantID: "tenant_default", WebChatCookieName: "nexus_webchat_session"},
		Repo:     &webchatRepoStub{},
		WebAuth:  auth,
		Identity: identity,
	}

	req := httptest.NewRequest(http.MethodPost, "/webchat/identity/phone", strings.NewReader(`{"phone":"+62 812-3456-789"}`))
	req.AddCookie(&http.Cookie{Name: "nexus_webchat_session", Value: "websess_1"})
	req.Header.Set("X-CSRF-Token", "csrf-token")
	rec := httptest.NewRecorder()
	app.handleWebChatIdentityPhone(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	user, err := identity.GetUser(context.Background(), "tenant_default", "user_user@example.com")
	if err != nil {
		t.Fatal(err)
	}
	if user.PrimaryPhone != "+62 812-3456-789" || user.PrimaryPhoneNormalized != "+628123456789" {
		t.Fatalf("unexpected stored phone %+v", user)
	}
	if user.PrimaryPhoneAddedAt.IsZero() || user.PrimaryPhoneAddedAt.Equal(time.Unix(0, 0).UTC()) {
		t.Fatalf("expected real phone added timestamp, got %+v", user)
	}

	req = httptest.NewRequest(http.MethodPost, "/webchat/identity/phone/delete", nil)
	req.AddCookie(&http.Cookie{Name: "nexus_webchat_session", Value: "websess_1"})
	req.Header.Set("X-CSRF-Token", "csrf-token")
	rec = httptest.NewRecorder()
	app.handleWebChatIdentityPhoneDelete(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	user, err = identity.GetUser(context.Background(), "tenant_default", "user_user@example.com")
	if err != nil {
		t.Fatal(err)
	}
	if user.PrimaryPhone != "" || user.PrimaryPhoneNormalized != "" {
		t.Fatalf("expected phone to be cleared, got %+v", user)
	}
}

func TestBuildWebChatLinkHintsWhatsAppMatchAnyIdentity(t *testing.T) {
	user := domain.User{
		PrimaryPhone:           "+62 812-3456-789",
		PrimaryPhoneNormalized: "+628123456789",
	}
	identities := []domain.LinkedIdentity{
		{ChannelType: "whatsapp", ChannelUserID: "628123456789"},
		{ChannelType: "whatsapp", ChannelUserID: "628000000000"},
	}

	hints := buildWebChatLinkHints(user, identities)

	whatsapp, ok := hints["whatsapp"]
	if !ok {
		t.Fatalf("expected whatsapp hints, got %+v", hints)
	}
	if matched, _ := whatsapp["phone_match"].(bool); !matched {
		t.Fatalf("expected whatsapp phone_match to stay true when any identity matches, got %+v", whatsapp)
	}
}

func TestWebChatMessageRequiresValidCSRF(t *testing.T) {
	auth := &webAuthStub{
		sessions: map[string]domain.WebAuthSession{
			"websess_1": {
				ID:            "websess_1",
				TenantID:      "tenant_default",
				Email:         "user@example.com",
				CSRFTokenHash: sha256Hex("csrf-token"),
				ExpiresAt:     time.Now().UTC().Add(time.Hour),
			},
		},
	}
	repo := &webchatRepoStub{}
	app := &App{
		Config: config.Config{DefaultTenantID: "tenant_default", DefaultAgentProfileID: "agent_profile_default", WebChatCookieName: "nexus_webchat_session"},
		Repo:   repo,
		Inbound: services.InboundService{
			Repo:   repo,
			Router: testRouter{},
		},
		Artifacts: services.ArtifactService{},
		WebAuth:   auth,
		Identity:  &identityStub{},
		WebChat:   webchat.New(),
	}
	req := httptest.NewRequest(http.MethodPost, "/webchat/messages", strings.NewReader("--x\r\nContent-Disposition: form-data; name=\"text\"\r\n\r\nhello\r\n--x--\r\n"))
	req.Header.Set("Content-Type", "multipart/form-data; boundary=x")
	req.Header.Set("X-CSRF-Token", "csrf-token")
	req.AddCookie(&http.Cookie{Name: "nexus_webchat_session", Value: "websess_1"})
	rec := httptest.NewRecorder()

	app.handleWebChatMessage(rec, req)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d body=%s", rec.Code, rec.Body.String())
	}
	if repo.receiptCount != 1 {
		t.Fatalf("expected inbound receipt, got %d", repo.receiptCount)
	}
}

func TestWebChatSessionHubNotifiesSubscribers(t *testing.T) {
	hub := NewWebChatSessionHub()
	sub := hub.Subscribe("session_1")
	defer hub.Unsubscribe("session_1", sub)

	hub.Notify("session_1")

	select {
	case <-sub:
	case <-time.After(time.Second):
		t.Fatal("expected hub notification")
	}
}

func TestWebChatSessionHubNotifiesUserSubscribers(t *testing.T) {
	hub := NewWebChatSessionHub()
	sub := hub.SubscribeUser("tenant_default", "user_1")
	defer hub.UnsubscribeUser("tenant_default", "user_1", sub)

	hub.NotifyUser("tenant_default", "user_1")

	select {
	case <-sub:
	case <-time.After(time.Second):
		t.Fatal("expected user hub notification")
	}
}

func TestWebChatMessageNotifiesSessionSubscribers(t *testing.T) {
	auth := &webAuthStub{
		sessions: map[string]domain.WebAuthSession{
			"websess_1": {
				ID:            "websess_1",
				TenantID:      "tenant_default",
				Email:         "user@example.com",
				CSRFTokenHash: sha256Hex("csrf-token"),
				ExpiresAt:     time.Now().UTC().Add(time.Hour),
			},
		},
	}
	repo := &webchatRepoStub{}
	hub := NewWebChatSessionHub()
	session, _, err := repo.ResolveSession(context.Background(), domain.CanonicalInboundEvent{}, "")
	if err != nil {
		t.Fatal(err)
	}
	sessionCh := hub.Subscribe(session.ID)
	defer hub.Unsubscribe(session.ID, sessionCh)
	app := &App{
		Config: config.Config{DefaultTenantID: "tenant_default", DefaultAgentProfileID: "agent_profile_default", WebChatCookieName: "nexus_webchat_session"},
		Repo:   repo,
		Inbound: services.InboundService{
			Repo:   repo,
			Router: testRouter{},
		},
		Artifacts:  services.ArtifactService{},
		WebAuth:    auth,
		Identity:   &identityStub{},
		WebChat:    webchat.New(),
		WebChatHub: hub,
	}
	req := httptest.NewRequest(http.MethodPost, "/webchat/messages", strings.NewReader("--x\r\nContent-Disposition: form-data; name=\"text\"\r\n\r\nhello\r\n--x--\r\n"))
	req.Header.Set("Content-Type", "multipart/form-data; boundary=x")
	req.Header.Set("X-CSRF-Token", "csrf-token")
	req.AddCookie(&http.Cookie{Name: "nexus_webchat_session", Value: "websess_1"})
	rec := httptest.NewRecorder()

	app.handleWebChatMessage(rec, req)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d body=%s", rec.Code, rec.Body.String())
	}
	select {
	case <-sessionCh:
	case <-time.After(time.Second):
		t.Fatal("expected session notification after message submit")
	}
}

func TestWebChatIndexServesReactShell(t *testing.T) {
	app := &App{Config: config.Config{WebChatInteractionVisibility: "minimal"}}
	req := httptest.NewRequest(http.MethodGet, "/webchat", nil)
	rec := httptest.NewRecorder()

	app.handleWebChatIndex(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "/webchat/app.js") || !strings.Contains(body, "window.__NEXUS_WEBCHAT_CONFIG__") {
		t.Fatalf("expected webchat react shell, got %s", body)
	}
	if !strings.Contains(body, `"interactionVisibility":"minimal"`) {
		t.Fatalf("expected interaction visibility in config payload, got %s", body)
	}
}

func TestBuildWebChatItemsMarksPartialAssistantMessages(t *testing.T) {
	items := buildWebChatItems(domain.SessionDetail{
		Messages: []domain.Message{
			{MessageID: "msg_partial", Direction: "outbound", Role: "assistant", Text: "hel", RawPayload: []byte(`{"is_partial":true}`)},
		},
	}, "session_web")
	if len(items) != 1 || !items[0].Partial {
		t.Fatalf("expected one partial item, got %+v", items)
	}
}

func TestBuildWebChatItemsDeduplicatesPayloadAndPersistedArtifacts(t *testing.T) {
	artifact := domain.Artifact{
		ID:         "artifact_1",
		MessageID:  "msg_1",
		Name:       "report.txt",
		MIMEType:   "text/plain",
		SizeBytes:  42,
		SHA256:     "sha256_1",
		StorageURI: "file:///tmp/report.txt",
	}
	items := buildWebChatItems(domain.SessionDetail{
		Messages: []domain.Message{{
			MessageID: "msg_1",
			SessionID: "session_web",
			Direction: "outbound",
			Role:      "assistant",
			Text:      "see attached",
			Artifacts: []domain.Artifact{artifact},
		}},
		Artifacts: []domain.Artifact{artifact},
	}, "session_web")
	if len(items) != 1 {
		t.Fatalf("expected one item, got %+v", items)
	}
	if len(items[0].Artifacts) != 1 {
		t.Fatalf("expected duplicate artifacts to be collapsed, got %+v", items[0].Artifacts)
	}
}

func TestLoadWebChatStateLinkedChannelsIncludesExternalMessagesReadOnly(t *testing.T) {
	authSession := domain.WebAuthSession{
		ID:        "websess_1",
		TenantID:  "tenant_default",
		Email:     "user@example.com",
		ExpiresAt: time.Now().UTC().Add(time.Hour),
	}
	identity := &identityStub{}
	user, err := identity.EnsureUserByEmail(context.Background(), "tenant_default", authSession.Email)
	if err != nil {
		t.Fatal(err)
	}
	if err := identity.UpsertLinkedIdentity(context.Background(), domain.LinkedIdentity{
		TenantID:      "tenant_default",
		UserID:        user.ID,
		ChannelType:   "whatsapp",
		ChannelUserID: "15551234567",
		Status:        "linked",
	}); err != nil {
		t.Fatal(err)
	}
	if err := identity.UpsertLinkedIdentity(context.Background(), domain.LinkedIdentity{
		TenantID:      "tenant_default",
		UserID:        user.ID,
		ChannelType:   "webchat",
		ChannelUserID: authSession.Email,
		Status:        "linked",
	}); err != nil {
		t.Fatal(err)
	}
	repo := &webchatRepoStub{
		session: domain.Session{ID: "session_webchat_1", TenantID: "tenant_default", OwnerUserID: authSession.Email, ChannelType: "webchat", ChannelScopeKey: "websess_1:session_webchat_1", State: "open"},
	}
	repo.sessionDetail = domain.SessionDetail{
		Messages: []domain.Message{
			{MessageID: "msg_wa_1", SessionID: "session_wa_1", ChannelType: "whatsapp", Direction: "inbound", Role: "user", Text: "from whatsapp"},
			{MessageID: "msg_web_1", SessionID: "session_webchat_1", ChannelType: "webchat", Direction: "inbound", Role: "user", Text: "from webchat"},
		},
	}
	app := &App{
		Config:   config.Config{DefaultTenantID: "tenant_default", DefaultAgentProfileID: "agent_profile_default", WebChatHistoryScope: "linked_channels"},
		Repo:     repo,
		Identity: identity,
	}
	_, items, err := app.loadWebChatState(context.Background(), authSession, 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 {
		t.Fatalf("expected two timeline items, got %+v", items)
	}
	var external domain.WebChatItem
	for _, item := range items {
		if item.ID == "msg_wa_1" {
			external = item
		}
	}
	if external.Meta["channel_type"] != "whatsapp" || external.Meta["read_only_external"] != "true" {
		t.Fatalf("expected read-only whatsapp metadata, got %+v", external.Meta)
	}
	if len(repo.lastMessageQuery.SessionIDs) != 1 || repo.lastMessageQuery.SessionIDs[0] != "session_webchat_1" {
		t.Fatalf("expected current webchat session in query, got %+v", repo.lastMessageQuery)
	}
	if len(repo.lastMessageQuery.ChannelIdentities) != 1 || repo.lastMessageQuery.ChannelIdentities[0].ChannelType != "whatsapp" {
		t.Fatalf("expected linked whatsapp identity in query, got %+v", repo.lastMessageQuery)
	}
}

func TestLoadWebChatStateUserScopeIncludesLinkedExternalIdentities(t *testing.T) {
	authSession := domain.WebAuthSession{
		ID:        "websess_1",
		TenantID:  "tenant_default",
		Email:     "user@example.com",
		ExpiresAt: time.Now().UTC().Add(time.Hour),
	}
	identity := &identityStub{}
	user, err := identity.EnsureUserByEmail(context.Background(), "tenant_default", authSession.Email)
	if err != nil {
		t.Fatal(err)
	}
	if err := identity.UpsertLinkedIdentity(context.Background(), domain.LinkedIdentity{
		TenantID:      "tenant_default",
		UserID:        user.ID,
		ChannelType:   "telegram",
		ChannelUserID: "12345",
		Status:        "linked",
	}); err != nil {
		t.Fatal(err)
	}
	repo := &webchatRepoStub{
		session: domain.Session{ID: "session_webchat_1", TenantID: "tenant_default", OwnerUserID: authSession.Email, ChannelType: "webchat", ChannelScopeKey: "websess_1:session_webchat_1", State: "open"},
	}
	app := &App{
		Config:   config.Config{DefaultTenantID: "tenant_default", DefaultAgentProfileID: "agent_profile_default", WebChatHistoryScope: "user"},
		Repo:     repo,
		Identity: identity,
	}
	_, _, err = app.loadWebChatState(context.Background(), authSession, 50)
	if err != nil {
		t.Fatal(err)
	}
	if repo.lastMessageQuery.OwnerUserID != user.ID {
		t.Fatalf("expected canonical user owner filter, got %+v", repo.lastMessageQuery)
	}
	if len(repo.lastMessageQuery.ChannelIdentities) != 1 || repo.lastMessageQuery.ChannelIdentities[0].ChannelType != "telegram" {
		t.Fatalf("expected linked telegram identity in user-scope query, got %+v", repo.lastMessageQuery)
	}
}

func TestBuildWebChatActivitySuppressesStatusForPendingAwait(t *testing.T) {
	repo := &webchatRepoStub{}
	activity := buildWebChatActivity(repo, context.Background(), "tenant_default", "session_web", []domain.WebChatItem{{
		ID:     "await_1",
		Type:   "await",
		Status: "pending",
	}})
	if activity != nil {
		t.Fatalf("expected nil activity for pending await, got %+v", activity)
	}
}

func TestBuildWebChatActivityDetectsWorkingRun(t *testing.T) {
	repo := &webchatRepoStub{}
	repo.sessionDetail = domain.SessionDetail{
		Runs: []domain.Run{{
			ID:          "run_1",
			SessionID:   "session_web",
			Status:      "running",
			StartedAt:   time.Now().UTC().Add(-time.Second),
			LastEventAt: time.Now().UTC(),
		}},
	}
	activity := buildWebChatActivity(repo, context.Background(), "tenant_default", "session_web", []domain.WebChatItem{{
		ID:   "msg_1",
		Type: "message",
		Role: "assistant",
		Text: "done soon",
	}})
	if activity == nil || activity.Phase != "working" {
		t.Fatalf("expected working activity, got %+v", activity)
	}
}

func TestWebChatStaticAssetsServed(t *testing.T) {
	app := &App{}

	jsReq := httptest.NewRequest(http.MethodGet, "/webchat/app.js", nil)
	jsRec := httptest.NewRecorder()
	app.handleWebChatJS(jsRec, jsReq)
	if jsRec.Code != http.StatusOK {
		t.Fatalf("expected js 200, got %d body=%s", jsRec.Code, jsRec.Body.String())
	}
	if !strings.Contains(jsRec.Body.String(), "window.__NEXUS_WEBCHAT_CONFIG__") && !strings.Contains(jsRec.Body.String(), "nexus-webchat-shell") {
		t.Fatalf("expected webchat js bundle, got %s", jsRec.Body.String())
	}

	cssReq := httptest.NewRequest(http.MethodGet, "/webchat/app.css", nil)
	cssRec := httptest.NewRecorder()
	app.handleWebChatCSS(cssRec, cssReq)
	if cssRec.Code != http.StatusOK {
		t.Fatalf("expected css 200, got %d body=%s", cssRec.Code, cssRec.Body.String())
	}
	if !strings.Contains(cssRec.Body.String(), ".nexus-webchat-shell") {
		t.Fatalf("expected webchat css bundle, got %s", cssRec.Body.String())
	}
}

func TestBuildWebChatMessageEventIncludesRawPayload(t *testing.T) {
	evt := buildWebChatMessageEvent("tenant_default", domain.WebAuthSession{
		ID:    "websess_1",
		Email: "user@example.com",
	}, "hello from webchat", nil)
	if len(evt.Metadata.RawPayload) == 0 {
		t.Fatal("expected raw payload to be populated")
	}
	var payload map[string]any
	if err := json.Unmarshal(evt.Metadata.RawPayload, &payload); err != nil {
		t.Fatalf("expected valid raw payload json: %v", err)
	}
	if payload["text"] != "hello from webchat" {
		t.Fatalf("unexpected raw payload: %+v", payload)
	}
}

func TestAbsoluteURLUsesForwardedSchemeAndHost(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/webchat", nil)
	req.Host = "internal:8080"
	req.Header.Set("X-Forwarded-Proto", "https")
	req.Header.Set("X-Forwarded-Host", "chat.example.com")

	got := absoluteURL(req, "/webchat/auth/callback?token=abc")
	want := "https://chat.example.com/webchat/auth/callback?token=abc"
	if got != want {
		t.Fatalf("expected %s, got %s", want, got)
	}
}
