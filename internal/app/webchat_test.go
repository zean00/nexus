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

func (r *webchatRepoStub) ResolveSession(context.Context, domain.CanonicalInboundEvent, string) (domain.Session, bool, error) {
	if r.session.ID == "" {
		r.session = domain.Session{ID: "session_webchat_1", TenantID: "tenant_default", OwnerUserID: "user@example.com", ChannelType: "webchat", ChannelScopeKey: "websess_1:session_webchat_1", State: "open"}
	}
	return r.session, false, nil
}

func TestWebChatAuthRequestStoresChallenge(t *testing.T) {
	auth := &webAuthStub{}
	app := &App{
		Config:  config.Config{DefaultTenantID: "tenant_default", WebChatOTPMinutes: 10, WebChatCookieName: "nexus_webchat_session"},
		WebAuth: auth,
		Email:   email.New("secret", "", "", "", "nexus@example.com"),
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
		Config:  config.Config{DefaultTenantID: "tenant_default", WebChatSessionHours: 24, WebChatCookieName: "nexus_webchat_session"},
		WebAuth: auth,
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
		Config:  config.Config{DefaultTenantID: "tenant_default", WebChatSessionHours: 24, WebChatCookieName: "nexus_webchat_session"},
		WebAuth: auth,
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
		Config:  config.Config{DefaultTenantID: "tenant_default", DefaultAgentProfileID: "agent_profile_default", WebChatCookieName: "nexus_webchat_session"},
		Repo:    repo,
		WebAuth: auth,
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
