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
			{MessageID: "msg_bot", Direction: "outbound", Role: "assistant", Text: "hi"},
		},
	}
	app := &App{
		Config:   config.Config{DefaultTenantID: "tenant_default", DefaultAgentProfileID: "agent_profile_default", WebChatCookieName: "nexus_webchat_session"},
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
			Email     string               `json:"email"`
			CSRFToken string               `json:"csrf_token"`
			Items     []domain.WebChatItem `json:"items"`
		} `json:"data"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&payload); err != nil {
		t.Fatal(err)
	}
	if payload.Data.Email != "user@example.com" || payload.Data.CSRFToken == "" || len(payload.Data.Items) != 2 {
		t.Fatalf("unexpected bootstrap payload %+v", payload)
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
	app := &App{}
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
}

func TestWebChatStaticAssetsServed(t *testing.T) {
	app := &App{}

	jsReq := httptest.NewRequest(http.MethodGet, "/webchat/app.js", nil)
	jsRec := httptest.NewRecorder()
	app.handleWebChatJS(jsRec, jsReq)
	if jsRec.Code != http.StatusOK {
		t.Fatalf("expected js 200, got %d body=%s", jsRec.Code, jsRec.Body.String())
	}
	if !strings.Contains(jsRec.Body.String(), "Nexus Web Chat") {
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
