package app

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io"
	"io/fs"
	"mime/multipart"
	"net"
	"net/http"
	"net/mail"
	"strconv"
	"strings"
	"time"

	"nexus/internal/config"
	"nexus/internal/domain"
	"nexus/internal/httpx"
	"nexus/internal/ports"
	webchatui "nexus/ui/webchat"
)

type webChatArtifactRepository interface {
	GetArtifactForSession(ctx context.Context, tenantID, sessionID, artifactID string) (domain.Artifact, error)
}

var webChatPageTemplate = template.Must(template.New("webchat-page").Parse(`<!doctype html>
<html lang="en">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width,initial-scale=1">
  <title>Nexus Web Chat</title>
  <link rel="stylesheet" href="/webchat/app.css">
</head>
<body>
  <div id="app"></div>
  <script>window.__NEXUS_WEBCHAT_CONFIG__ = {{ .Config }};</script>
  <script type="module" src="/webchat/app.js"></script>
</body>
</html>`))

func (a *App) handleWebChatIndex(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	configJSON, _ := json.Marshal(map[string]any{
		"baseUrl":               "/webchat",
		"interactionVisibility": a.webChatInteractionVisibilityMode(),
		"features": map[string]bool{
			"auth":    true,
			"uploads": true,
			"newChat": true,
			"logout":  true,
			"sse":     true,
		},
	})
	_ = webChatPageTemplate.Execute(w, map[string]any{
		"Config": template.JS(string(configJSON)),
	})
}

func (a *App) handleWebChatJS(w http.ResponseWriter, r *http.Request) {
	a.serveWebChatAsset(w, r, "app.js", "application/javascript; charset=utf-8")
}

func (a *App) handleWebChatCSS(w http.ResponseWriter, r *http.Request) {
	a.serveWebChatAsset(w, r, "app.css", "text/css; charset=utf-8")
}

func (a *App) serveWebChatAsset(w http.ResponseWriter, _ *http.Request, name, contentType string) {
	content, err := fs.ReadFile(webchatui.Dist(), name)
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Cache-Control", "public, max-age=300")
	_, _ = w.Write(content)
}

func (a *App) handleWebChatAuthRequest(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		httpx.Error(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if a.WebAuth == nil {
		httpx.Error(w, http.StatusInternalServerError, "web auth unavailable")
		return
	}
	var body struct {
		Email string `json:"email"`
	}
	if !decodeJSONBody(w, r, &body) {
		return
	}
	email, err := normalizeEmail(body.Email)
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, err.Error())
		return
	}
	otp := randomDigits(6)
	linkToken := randomToken(24)
	challenge := domain.WebAuthChallenge{
		ID:            "webauth_" + randomToken(8),
		TenantID:      a.Config.DefaultTenantID,
		Email:         email,
		OTPHash:       sha256Hex(otp),
		LinkTokenHash: sha256Hex(linkToken),
		ExpiresAt:     time.Now().UTC().Add(time.Duration(a.Config.WebChatOTPMinutes) * time.Minute),
		CreatedAt:     time.Now().UTC(),
	}
	if err := a.WebAuth.CreateWebAuthChallenge(r.Context(), challenge, time.Minute); err != nil {
		if errors.Is(err, domain.ErrWebAuthRateLimited) {
			httpx.Error(w, http.StatusTooManyRequests, err.Error())
			return
		}
		httpx.Error(w, http.StatusInternalServerError, err.Error())
		return
	}
	link := absoluteURL(r, "/webchat/auth/callback?token="+linkToken)
	text := fmt.Sprintf("Your Nexus verification code is %s.\n\nOr open this link:\n%s", otp, link)
	if _, err := a.Email.SendMail(r.Context(), email, "Your Nexus sign-in code", text, ""); err != nil {
		httpx.Error(w, http.StatusBadGateway, err.Error())
		return
	}
	httpx.Accepted(w, map[string]any{"status": "sent"}, map[string]any{"email": email})
}

func (a *App) handleWebChatDevSession(w http.ResponseWriter, r *http.Request) {
	if !a.webChatDevAuthEnabled(r) {
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodPost {
		httpx.Error(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if a.WebAuth == nil || a.Identity == nil {
		httpx.Error(w, http.StatusInternalServerError, "web auth unavailable")
		return
	}
	var body struct {
		Email string `json:"email"`
	}
	if !decodeJSONBody(w, r, &body) {
		return
	}
	email, err := normalizeEmail(body.Email)
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, err.Error())
		return
	}
	authSession, err := a.createWebChatAuthSession(r.Context(), email)
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, err.Error())
		return
	}
	setWebChatCookie(w, r, a.Config.WebChatCookieName, authSession.ID, authSession.ExpiresAt)
	csrfToken, err := a.issueWebChatCSRF(r.Context(), authSession.ID)
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, err.Error())
		return
	}
	session, items, err := a.loadWebChatState(r.Context(), authSession, 100)
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, err.Error())
		return
	}
	activity := buildWebChatActivity(a.Repo, r.Context(), a.Config.DefaultTenantID, session.ID, items)
	user, identities, recentStepUp, err := a.currentWebChatIdentityState(r.Context(), authSession)
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, err.Error())
		return
	}
	httpx.OK(w, map[string]any{
		"email":             authSession.Email,
		"session_id":        session.ID,
		"csrf_token":        csrfToken,
		"items":             items,
		"activity":          activity,
		"visibility_mode":   a.webChatInteractionVisibilityMode(),
		"user_id":           user.ID,
		"linked_identities": identities,
		"recent_step_up":    recentStepUp,
	}, map[string]any{"mode": "dev"})
}

func (a *App) webChatDevAuthEnabled(r *http.Request) bool {
	return a.Config.WebChatDevAuth &&
		strings.EqualFold(strings.TrimSpace(a.Config.Environment), "development") &&
		isLocalWebChatDevRequest(r)
}

func isLocalWebChatDevRequest(r *http.Request) bool {
	host := strings.TrimSpace(externalRequestHost(r))
	if host == "" {
		return false
	}
	if parsedHost, _, err := net.SplitHostPort(host); err == nil {
		host = parsedHost
	}
	host = strings.Trim(strings.ToLower(host), "[]")
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func (a *App) handleWebChatAuthVerify(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		httpx.Error(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if a.WebAuth == nil {
		httpx.Error(w, http.StatusInternalServerError, "web auth unavailable")
		return
	}
	var body struct {
		Email string `json:"email"`
		Code  string `json:"code"`
	}
	if !decodeJSONBody(w, r, &body) {
		return
	}
	email, err := normalizeEmail(body.Email)
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, err.Error())
		return
	}
	if _, err := a.WebAuth.ConsumeWebAuthChallengeByOTP(r.Context(), a.Config.DefaultTenantID, email, sha256Hex(strings.TrimSpace(body.Code)), time.Now().UTC()); err != nil {
		httpx.Error(w, http.StatusUnauthorized, err.Error())
		return
	}
	authSession, err := a.createWebChatAuthSession(r.Context(), email)
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, err.Error())
		return
	}
	setWebChatCookie(w, r, a.Config.WebChatCookieName, authSession.ID, authSession.ExpiresAt)
	httpx.OK(w, map[string]any{"authenticated": true}, map[string]any{"email": email})
}

func (a *App) handleWebChatAuthCallback(w http.ResponseWriter, r *http.Request) {
	if a.WebAuth == nil {
		httpx.Error(w, http.StatusInternalServerError, "web auth unavailable")
		return
	}
	token := strings.TrimSpace(r.URL.Query().Get("token"))
	if token == "" {
		httpx.Error(w, http.StatusBadRequest, "missing token")
		return
	}
	challenge, err := a.WebAuth.ConsumeWebAuthChallengeByLink(r.Context(), a.Config.DefaultTenantID, sha256Hex(token), time.Now().UTC())
	if err != nil {
		httpx.Error(w, http.StatusUnauthorized, err.Error())
		return
	}
	authSession, err := a.createWebChatAuthSession(r.Context(), challenge.Email)
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, err.Error())
		return
	}
	setWebChatCookie(w, r, a.Config.WebChatCookieName, authSession.ID, authSession.ExpiresAt)
	http.Redirect(w, r, "/webchat", http.StatusSeeOther)
}

func (a *App) handleWebChatAuthLogout(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		httpx.Error(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	authSession, err := a.currentWebChatSession(r)
	if err == nil && a.WebAuth != nil {
		_ = a.WebAuth.DeleteWebAuthSession(r.Context(), authSession.ID)
	}
	clearWebChatCookie(w, a.Config.WebChatCookieName)
	httpx.OK(w, map[string]any{"status": "logged_out"}, nil)
}

func (a *App) handleWebChatBootstrap(w http.ResponseWriter, r *http.Request) {
	authSession, err := a.currentWebChatSession(r)
	if err != nil {
		httpx.Error(w, http.StatusUnauthorized, err.Error())
		return
	}
	csrfToken, err := a.issueWebChatCSRF(r.Context(), authSession.ID)
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, err.Error())
		return
	}
	session, items, err := a.loadWebChatState(r.Context(), authSession, 100)
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, err.Error())
		return
	}
	activity := buildWebChatActivity(a.Repo, r.Context(), a.Config.DefaultTenantID, session.ID, items)
	user, identities, recentStepUp, err := a.currentWebChatIdentityState(r.Context(), authSession)
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, err.Error())
		return
	}
	httpx.OK(w, map[string]any{
		"email":                  authSession.Email,
		"session_id":             session.ID,
		"csrf_token":             csrfToken,
		"items":                  items,
		"activity":               activity,
		"visibility_mode":        a.webChatInteractionVisibilityMode(),
		"user_id":                user.ID,
		"primary_phone":          user.PrimaryPhone,
		"primary_phone_verified": user.PrimaryPhoneVerified,
		"linked_identities":      identities,
		"recent_step_up":         recentStepUp,
	}, nil)
}

func (a *App) handleWebChatHistory(w http.ResponseWriter, r *http.Request) {
	authSession, err := a.currentWebChatSession(r)
	if err != nil {
		httpx.Error(w, http.StatusUnauthorized, err.Error())
		return
	}
	limit, _ := strconv.Atoi(strings.TrimSpace(r.URL.Query().Get("limit")))
	if limit <= 0 {
		limit = 100
	}
	session, items, err := a.loadWebChatState(r.Context(), authSession, limit)
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, err.Error())
		return
	}
	httpx.OK(w, map[string]any{
		"items":           items,
		"activity":        buildWebChatActivity(a.Repo, r.Context(), a.Config.DefaultTenantID, session.ID, items),
		"visibility_mode": a.webChatInteractionVisibilityMode(),
	}, nil)
}

func (a *App) handleWebChatArtifact(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		httpx.Error(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	authSession, err := a.currentWebChatSession(r)
	if err != nil {
		httpx.Error(w, http.StatusUnauthorized, err.Error())
		return
	}
	session, err := a.resolveWebChatSession(r.Context(), authSession)
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, err.Error())
		return
	}
	artifactID := strings.TrimSpace(strings.TrimPrefix(r.URL.Path, "/webchat/artifacts/"))
	if artifactID == "" || strings.Contains(artifactID, "/") {
		http.NotFound(w, r)
		return
	}
	repo, ok := a.Repo.(webChatArtifactRepository)
	if !ok {
		httpx.Error(w, http.StatusInternalServerError, "artifact lookup unavailable")
		return
	}
	artifact, err := repo.GetArtifactForSession(r.Context(), a.Config.DefaultTenantID, session.ID, artifactID)
	if err != nil {
		if errors.Is(err, domain.ErrArtifactNotFound) {
			http.NotFound(w, r)
			return
		}
		httpx.Error(w, http.StatusInternalServerError, err.Error())
		return
	}
	if remoteURL := webChatRemoteArtifactURL(artifact); remoteURL != "" {
		http.Redirect(w, r, remoteURL, http.StatusTemporaryRedirect)
		return
	}
	if err := a.serveWebChatArtifact(r.Context(), w, artifact); err != nil {
		httpx.Error(w, http.StatusBadGateway, err.Error())
	}
}

func (a *App) handleWebChatEvents(w http.ResponseWriter, r *http.Request) {
	authSession, err := a.currentWebChatSession(r)
	if err != nil {
		httpx.Error(w, http.StatusUnauthorized, err.Error())
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		httpx.Error(w, http.StatusInternalServerError, "streaming unsupported")
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	session, items, err := a.loadWebChatState(r.Context(), authSession, 100)
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, err.Error())
		return
	}
	var notifyCh chan struct{}
	if a.WebChatHub != nil && session.ID != "" {
		notifyCh = a.WebChatHub.Subscribe(session.ID)
		defer a.WebChatHub.Unsubscribe(session.ID, notifyCh)
	}
	payload, _ := json.Marshal(map[string]any{
		"items":           items,
		"activity":        buildWebChatActivity(a.Repo, r.Context(), a.Config.DefaultTenantID, session.ID, items),
		"visibility_mode": a.webChatInteractionVisibilityMode(),
	})
	_, _ = fmt.Fprintf(w, "data: %s\n\n", payload)
	flusher.Flush()
	last := string(payload)
	fallbackTicker := time.NewTicker(2 * time.Second)
	defer fallbackTicker.Stop()
	keepaliveTicker := time.NewTicker(30 * time.Second)
	defer keepaliveTicker.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-notifyCh:
		case <-fallbackTicker.C:
		case <-keepaliveTicker.C:
			_, _ = io.WriteString(w, ": keepalive\n\n")
			flusher.Flush()
			continue
		}
		_, items, err = a.loadWebChatState(r.Context(), authSession, 100)
		if err != nil {
			return
		}
		payload, _ = json.Marshal(map[string]any{
			"items":           items,
			"activity":        buildWebChatActivity(a.Repo, r.Context(), a.Config.DefaultTenantID, session.ID, items),
			"visibility_mode": a.webChatInteractionVisibilityMode(),
		})
		if string(payload) == last {
			continue
		}
		_, _ = fmt.Fprintf(w, "data: %s\n\n", payload)
		flusher.Flush()
		last = string(payload)
	}
}

func (a *App) handleWebChatMessage(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		httpx.Error(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	authSession, err := a.currentWebChatSession(r)
	if err != nil {
		httpx.Error(w, http.StatusUnauthorized, err.Error())
		return
	}
	if err := a.validateWebChatCSRF(r, authSession.ID); err != nil {
		httpx.Error(w, http.StatusForbidden, err.Error())
		return
	}
	if err := r.ParseMultipartForm(16 << 20); err != nil {
		httpx.Error(w, http.StatusBadRequest, err.Error())
		return
	}
	text := strings.TrimSpace(r.FormValue("text"))
	files := r.MultipartForm.File["files"]
	artifacts, err := a.readWebChatUploads(r.Context(), files)
	if err != nil {
		httpx.Error(w, http.StatusBadGateway, err.Error())
		return
	}
	if text == "" && len(artifacts) == 0 {
		httpx.Error(w, http.StatusBadRequest, "missing message body")
		return
	}
	evt := buildWebChatMessageEvent(a.Config.DefaultTenantID, authSession, text, artifacts)
	result, err := a.Inbound.Handle(r.Context(), evt)
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, err.Error())
		return
	}
	if a.WebChatHub != nil {
		a.WebChatHub.Notify(result.SessionID)
	}
	httpx.Accepted(w, result, nil)
}

func (a *App) handleWebChatAwaitRespond(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		httpx.Error(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	authSession, err := a.currentWebChatSession(r)
	if err != nil {
		httpx.Error(w, http.StatusUnauthorized, err.Error())
		return
	}
	if err := a.validateWebChatCSRF(r, authSession.ID); err != nil {
		httpx.Error(w, http.StatusForbidden, err.Error())
		return
	}
	var body struct {
		AwaitID string `json:"await_id"`
		Reply   string `json:"reply"`
	}
	if !decodeJSONBody(w, r, &body) {
		return
	}
	payload, _ := json.Marshal(map[string]string{"reply": body.Reply})
	evt := domain.CanonicalInboundEvent{
		TenantID: a.Config.DefaultTenantID,
		Channel:  "webchat",
		Sender: domain.Sender{
			ChannelUserID:     authSession.Email,
			IdentityAssurance: "first_party_session",
		},
		Metadata: domain.Metadata{
			AwaitID:       body.AwaitID,
			ResumePayload: payload,
		},
	}
	if _, err := a.authorizeAwaitResponse(r.Context(), &evt); err != nil {
		httpx.Error(w, http.StatusForbidden, err.Error())
		return
	}
	evt = domain.CanonicalInboundEvent{
		EventID:         "webchat_await_" + randomToken(8),
		TenantID:        a.Config.DefaultTenantID,
		Channel:         "webchat",
		Interaction:     "await_response",
		ProviderEventID: "webchat_await_" + randomToken(4),
		ReceivedAt:      time.Now().UTC(),
		Sender: domain.Sender{
			ChannelUserID:       authSession.Email,
			DisplayName:         authSession.Email,
			IsAuthenticated:     true,
			IdentityAssurance:   "first_party_session",
			AllowedResponderIDs: []string{authSession.Email},
		},
		Conversation: domain.Conversation{
			ChannelConversationID: authSession.Email,
			ChannelThreadID:       authSession.ID,
			ChannelSurfaceKey:     authSession.ID,
		},
		Message: domain.Message{
			MessageID:   "webchat_await_msg_" + randomToken(6),
			MessageType: "interactive",
			Text:        body.Reply,
			Parts:       []domain.Part{{ContentType: "application/json", Content: string(payload)}},
		},
		Metadata: domain.Metadata{
			AwaitID:       body.AwaitID,
			ResumePayload: payload,
			ActorUserID:   evt.Metadata.ActorUserID,
		},
	}
	err = a.Await.HandleResponse(r.Context(), evt)
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, err.Error())
		return
	}
	session, _, err := a.loadWebChatState(r.Context(), authSession, 1)
	if err == nil && a.WebChatHub != nil {
		a.WebChatHub.Notify(session.ID)
	}
	httpx.OK(w, map[string]any{"status": "accepted"}, nil)
}

func (a *App) handleWebChatIdentityLinks(w http.ResponseWriter, r *http.Request) {
	authSession, err := a.currentWebChatSession(r)
	if err != nil {
		httpx.Error(w, http.StatusUnauthorized, err.Error())
		return
	}
	user, identities, recentStepUp, err := a.currentWebChatIdentityState(r.Context(), authSession)
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, err.Error())
		return
	}
	httpx.OK(w, map[string]any{
		"user_id":                user.ID,
		"primary_phone":          user.PrimaryPhone,
		"primary_phone_verified": user.PrimaryPhoneVerified,
		"linked_identities":      identities,
		"link_hints":             buildWebChatLinkHints(user, identities),
		"recent_step_up":         recentStepUp,
		"step_up_window_min":     a.Config.StepUpWindowMinutes,
	}, nil)
}

func (a *App) handleWebChatIdentityProfile(w http.ResponseWriter, r *http.Request) {
	authSession, err := a.currentWebChatSession(r)
	if err != nil {
		httpx.Error(w, http.StatusUnauthorized, err.Error())
		return
	}
	user, identities, recentStepUp, err := a.currentWebChatIdentityState(r.Context(), authSession)
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, err.Error())
		return
	}
	httpx.OK(w, map[string]any{
		"user_id":                user.ID,
		"email":                  user.PrimaryEmail,
		"primary_phone":          user.PrimaryPhone,
		"primary_phone_verified": user.PrimaryPhoneVerified,
		"linked_identities":      identities,
		"link_hints":             buildWebChatLinkHints(user, identities),
		"recent_step_up":         recentStepUp,
	}, nil)
}

func (a *App) handleWebChatIdentityPhone(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		httpx.Error(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	authSession, err := a.currentWebChatSession(r)
	if err != nil {
		httpx.Error(w, http.StatusUnauthorized, err.Error())
		return
	}
	if err := a.validateWebChatCSRF(r, authSession.ID); err != nil {
		httpx.Error(w, http.StatusForbidden, err.Error())
		return
	}
	var body struct {
		Phone string `json:"phone"`
	}
	if !decodeJSONBody(w, r, &body) {
		return
	}
	rawPhone, normalizedPhone, err := normalizePhone(body.Phone)
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, err.Error())
		return
	}
	user, err := a.Identity.EnsureUserByEmail(r.Context(), a.Config.DefaultTenantID, authSession.Email)
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, err.Error())
		return
	}
	eventType := "trust.phone_updated"
	if user.PrimaryPhone == "" {
		eventType = "trust.phone_added"
	}
	addedAt := user.PrimaryPhoneAddedAt
	if addedAt.IsZero() || addedAt.Equal(time.Unix(0, 0).UTC()) {
		addedAt = time.Now().UTC()
	}
	if err := a.Identity.UpdateUserPhone(r.Context(), a.Config.DefaultTenantID, user.ID, rawPhone, normalizedPhone, false, addedAt); err != nil {
		httpx.Error(w, http.StatusInternalServerError, err.Error())
		return
	}
	user, err = a.Identity.GetUser(r.Context(), a.Config.DefaultTenantID, user.ID)
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, err.Error())
		return
	}
	_ = a.Repo.Audit(r.Context(), domain.AuditEvent{
		ID:            trustAdminAuditID(strings.TrimPrefix(eventType, "trust."), a.Config.DefaultTenantID, user.ID, normalizedPhone, time.Now().UTC().Format(time.RFC3339Nano)),
		TenantID:      a.Config.DefaultTenantID,
		AggregateType: "user",
		AggregateID:   user.ID,
		EventType:     eventType,
		PayloadJSON:   mustJSON(map[string]any{"primary_phone": user.PrimaryPhone, "primary_phone_verified": user.PrimaryPhoneVerified}),
		CreatedAt:     time.Now().UTC(),
	})
	httpx.OK(w, map[string]any{
		"user_id":                user.ID,
		"email":                  user.PrimaryEmail,
		"primary_phone":          user.PrimaryPhone,
		"primary_phone_verified": user.PrimaryPhoneVerified,
		"link_hints":             buildWebChatLinkHints(user, nil),
	}, actionMeta("webchat_phone_saved"))
}

func (a *App) handleWebChatIdentityPhoneDelete(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		httpx.Error(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	authSession, err := a.currentWebChatSession(r)
	if err != nil {
		httpx.Error(w, http.StatusUnauthorized, err.Error())
		return
	}
	if err := a.validateWebChatCSRF(r, authSession.ID); err != nil {
		httpx.Error(w, http.StatusForbidden, err.Error())
		return
	}
	user, err := a.Identity.EnsureUserByEmail(r.Context(), a.Config.DefaultTenantID, authSession.Email)
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, err.Error())
		return
	}
	if err := a.Identity.ClearUserPhone(r.Context(), a.Config.DefaultTenantID, user.ID); err != nil {
		httpx.Error(w, http.StatusInternalServerError, err.Error())
		return
	}
	_ = a.Repo.Audit(r.Context(), domain.AuditEvent{
		ID:            trustAdminAuditID("phone_removed", a.Config.DefaultTenantID, user.ID, time.Now().UTC().Format(time.RFC3339Nano)),
		TenantID:      a.Config.DefaultTenantID,
		AggregateType: "user",
		AggregateID:   user.ID,
		EventType:     "trust.phone_removed",
		PayloadJSON:   mustJSON(map[string]any{"primary_phone": ""}),
		CreatedAt:     time.Now().UTC(),
	})
	httpx.OK(w, map[string]any{"status": "removed"}, actionMeta("webchat_phone_removed"))
}

func (a *App) handleWebChatIdentityLinkCode(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		httpx.Error(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	authSession, err := a.currentWebChatSession(r)
	if err != nil {
		httpx.Error(w, http.StatusUnauthorized, err.Error())
		return
	}
	if err := a.validateWebChatCSRF(r, authSession.ID); err != nil {
		httpx.Error(w, http.StatusForbidden, err.Error())
		return
	}
	var body struct {
		Channel string `json:"channel"`
	}
	if !decodeJSONBody(w, r, &body) {
		return
	}
	channel := strings.TrimSpace(strings.ToLower(body.Channel))
	switch channel {
	case "slack", "telegram", "whatsapp", "email":
	default:
		httpx.Error(w, http.StatusBadRequest, "unsupported channel")
		return
	}
	user, err := a.Identity.EnsureUserByEmail(r.Context(), a.Config.DefaultTenantID, authSession.Email)
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, err.Error())
		return
	}
	code := randomDigits(8)
	challenge := domain.StepUpChallenge{
		ID:          "link_" + randomToken(8),
		TenantID:    a.Config.DefaultTenantID,
		UserID:      user.ID,
		Purpose:     "link",
		ChannelType: channel,
		CodeHash:    sha256Hex(code),
		ExpiresAt:   time.Now().UTC().Add(time.Duration(a.Config.IdentityLinkMinutes) * time.Minute),
		CreatedAt:   time.Now().UTC(),
	}
	if err := a.Identity.CreateStepUpChallenge(r.Context(), challenge, time.Minute); err != nil {
		if errors.Is(err, domain.ErrWebAuthRateLimited) {
			httpx.Error(w, http.StatusTooManyRequests, err.Error())
			return
		}
		httpx.Error(w, http.StatusInternalServerError, err.Error())
		return
	}
	_ = a.Repo.Audit(r.Context(), domain.AuditEvent{
		ID:            "audit_trust_link_code_" + randomToken(6),
		TenantID:      a.Config.DefaultTenantID,
		AggregateType: "user",
		AggregateID:   user.ID,
		EventType:     "trust.link_code_issued",
		PayloadJSON:   mustJSON(map[string]any{"channel": channel, "expires_at": challenge.ExpiresAt}),
		CreatedAt:     time.Now().UTC(),
	})
	httpx.OK(w, map[string]any{
		"channel":    channel,
		"code":       code,
		"link_code":  user.ID + "." + code,
		"expires_at": challenge.ExpiresAt,
	}, nil)
}

func (a *App) handleWebChatIdentityUnlink(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		httpx.Error(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	authSession, err := a.currentWebChatSession(r)
	if err != nil {
		httpx.Error(w, http.StatusUnauthorized, err.Error())
		return
	}
	if err := a.validateWebChatCSRF(r, authSession.ID); err != nil {
		httpx.Error(w, http.StatusForbidden, err.Error())
		return
	}
	var body struct {
		Channel       string `json:"channel"`
		ChannelUserID string `json:"channel_user_id"`
	}
	if !decodeJSONBody(w, r, &body) {
		return
	}
	user, err := a.Identity.EnsureUserByEmail(r.Context(), a.Config.DefaultTenantID, authSession.Email)
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, err.Error())
		return
	}
	identity, err := a.Identity.GetLinkedIdentity(r.Context(), a.Config.DefaultTenantID, strings.ToLower(strings.TrimSpace(body.Channel)), strings.TrimSpace(body.ChannelUserID))
	if err != nil {
		httpx.Error(w, http.StatusNotFound, err.Error())
		return
	}
	if identity.UserID != user.ID {
		httpx.Error(w, http.StatusForbidden, "identity not owned by current user")
		return
	}
	if err := a.Identity.DeleteLinkedIdentity(r.Context(), a.Config.DefaultTenantID, identity.ChannelType, identity.ChannelUserID); err != nil {
		httpx.Error(w, http.StatusInternalServerError, err.Error())
		return
	}
	_ = a.Repo.Audit(r.Context(), domain.AuditEvent{
		ID:            "audit_trust_unlink_" + randomToken(6),
		TenantID:      a.Config.DefaultTenantID,
		AggregateType: "user",
		AggregateID:   user.ID,
		EventType:     "trust.identity_unlinked",
		PayloadJSON:   mustJSON(identity),
		CreatedAt:     time.Now().UTC(),
	})
	httpx.OK(w, map[string]any{"status": "unlinked"}, nil)
}

func (a *App) handleWebChatStepUpRequest(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		httpx.Error(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	authSession, err := a.currentWebChatSession(r)
	if err != nil {
		httpx.Error(w, http.StatusUnauthorized, err.Error())
		return
	}
	if err := a.validateWebChatCSRF(r, authSession.ID); err != nil {
		httpx.Error(w, http.StatusForbidden, err.Error())
		return
	}
	user, err := a.Identity.EnsureUserByEmail(r.Context(), a.Config.DefaultTenantID, authSession.Email)
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, err.Error())
		return
	}
	code := randomDigits(6)
	challenge := domain.StepUpChallenge{
		ID:        "stepup_" + randomToken(8),
		TenantID:  a.Config.DefaultTenantID,
		UserID:    user.ID,
		Purpose:   "step_up",
		CodeHash:  sha256Hex(code),
		ExpiresAt: time.Now().UTC().Add(time.Duration(a.Config.StepUpOTPMinutes) * time.Minute),
		CreatedAt: time.Now().UTC(),
	}
	if err := a.Identity.CreateStepUpChallenge(r.Context(), challenge, time.Minute); err != nil {
		if errors.Is(err, domain.ErrWebAuthRateLimited) {
			httpx.Error(w, http.StatusTooManyRequests, err.Error())
			return
		}
		httpx.Error(w, http.StatusInternalServerError, err.Error())
		return
	}
	text := fmt.Sprintf("Your Nexus step-up code is %s.", code)
	if _, err := a.Email.SendMail(r.Context(), authSession.Email, "Your Nexus step-up code", text, ""); err != nil {
		httpx.Error(w, http.StatusBadGateway, err.Error())
		return
	}
	_ = a.Repo.Audit(r.Context(), domain.AuditEvent{
		ID:            "audit_trust_stepup_request_" + randomToken(6),
		TenantID:      a.Config.DefaultTenantID,
		AggregateType: "user",
		AggregateID:   user.ID,
		EventType:     "trust.step_up_requested",
		PayloadJSON:   mustJSON(map[string]any{"email": authSession.Email}),
		CreatedAt:     time.Now().UTC(),
	})
	httpx.Accepted(w, map[string]any{"status": "sent"}, nil)
}

func (a *App) handleWebChatStepUpVerify(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		httpx.Error(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	authSession, err := a.currentWebChatSession(r)
	if err != nil {
		httpx.Error(w, http.StatusUnauthorized, err.Error())
		return
	}
	if err := a.validateWebChatCSRF(r, authSession.ID); err != nil {
		httpx.Error(w, http.StatusForbidden, err.Error())
		return
	}
	var body struct {
		Code string `json:"code"`
	}
	if !decodeJSONBody(w, r, &body) {
		return
	}
	user, err := a.Identity.EnsureUserByEmail(r.Context(), a.Config.DefaultTenantID, authSession.Email)
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, err.Error())
		return
	}
	if _, err := a.Identity.ConsumeStepUpChallenge(r.Context(), a.Config.DefaultTenantID, user.ID, "step_up", "", sha256Hex(strings.TrimSpace(body.Code)), time.Now().UTC()); err != nil {
		_ = a.Repo.Audit(r.Context(), domain.AuditEvent{
			ID:            "audit_trust_stepup_rejected_" + randomToken(6),
			TenantID:      a.Config.DefaultTenantID,
			AggregateType: "user",
			AggregateID:   user.ID,
			EventType:     "trust.step_up_rejected",
			PayloadJSON:   mustJSON(map[string]any{"email": authSession.Email}),
			CreatedAt:     time.Now().UTC(),
		})
		httpx.Error(w, http.StatusUnauthorized, err.Error())
		return
	}
	if err := a.Identity.MarkUserStepUp(r.Context(), a.Config.DefaultTenantID, user.ID, time.Now().UTC()); err != nil {
		httpx.Error(w, http.StatusInternalServerError, err.Error())
		return
	}
	_ = a.Repo.Audit(r.Context(), domain.AuditEvent{
		ID:            "audit_trust_stepup_verified_" + randomToken(6),
		TenantID:      a.Config.DefaultTenantID,
		AggregateType: "user",
		AggregateID:   user.ID,
		EventType:     "trust.step_up_verified",
		PayloadJSON:   mustJSON(map[string]any{"email": authSession.Email}),
		CreatedAt:     time.Now().UTC(),
	})
	httpx.OK(w, map[string]any{"recent_step_up": true}, nil)
}

func (a *App) handleWebChatNewChat(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		httpx.Error(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	authSession, err := a.currentWebChatSession(r)
	if err != nil {
		httpx.Error(w, http.StatusUnauthorized, err.Error())
		return
	}
	if err := a.validateWebChatCSRF(r, authSession.ID); err != nil {
		httpx.Error(w, http.StatusForbidden, err.Error())
		return
	}
	session, err := a.Repo.CreateVirtualSession(r.Context(), a.Config.DefaultTenantID, "webchat", authSession.ID, authSession.Email, a.Config.DefaultAgentProfileID, "")
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, err.Error())
		return
	}
	httpx.OK(w, session, nil)
}

func (a *App) handleWebChatCloseChat(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		httpx.Error(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	authSession, err := a.currentWebChatSession(r)
	if err != nil {
		httpx.Error(w, http.StatusUnauthorized, err.Error())
		return
	}
	if err := a.validateWebChatCSRF(r, authSession.ID); err != nil {
		httpx.Error(w, http.StatusForbidden, err.Error())
		return
	}
	session, err := a.Repo.CloseActiveSession(r.Context(), a.Config.DefaultTenantID, "webchat", authSession.ID, authSession.Email)
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, err.Error())
		return
	}
	httpx.OK(w, session, nil)
}

func (a *App) createWebChatAuthSession(ctx context.Context, email string) (domain.WebAuthSession, error) {
	session := domain.WebAuthSession{
		ID:         "websess_" + randomToken(16),
		TenantID:   a.Config.DefaultTenantID,
		Email:      email,
		ExpiresAt:  time.Now().UTC().Add(time.Duration(a.Config.WebChatSessionHours) * time.Hour),
		LastSeenAt: time.Now().UTC(),
		CreatedAt:  time.Now().UTC(),
	}
	return session, a.WebAuth.CreateWebAuthSession(ctx, session)
}

func (a *App) currentWebChatIdentityState(ctx context.Context, authSession domain.WebAuthSession) (domain.User, []domain.LinkedIdentity, bool, error) {
	user, err := a.Identity.EnsureUserByEmail(ctx, a.Config.DefaultTenantID, authSession.Email)
	if err != nil {
		return domain.User{}, nil, false, err
	}
	if err := a.Identity.UpsertLinkedIdentity(ctx, domain.LinkedIdentity{
		TenantID:       a.Config.DefaultTenantID,
		UserID:         user.ID,
		ChannelType:    "webchat",
		ChannelUserID:  authSession.Email,
		Status:         "linked",
		LinkedAt:       time.Now().UTC(),
		LastVerifiedAt: time.Now().UTC(),
	}); err != nil {
		return domain.User{}, nil, false, err
	}
	if err := a.Identity.UpsertLinkedIdentity(ctx, domain.LinkedIdentity{
		TenantID:       a.Config.DefaultTenantID,
		UserID:         user.ID,
		ChannelType:    "email",
		ChannelUserID:  authSession.Email,
		Status:         "linked",
		LinkedAt:       time.Now().UTC(),
		LastVerifiedAt: time.Now().UTC(),
	}); err != nil {
		return domain.User{}, nil, false, err
	}
	identities, err := a.Identity.ListLinkedIdentitiesForUser(ctx, a.Config.DefaultTenantID, user.ID)
	if err != nil {
		return domain.User{}, nil, false, err
	}
	recent, err := a.Identity.HasRecentStepUp(ctx, a.Config.DefaultTenantID, user.ID, time.Now().UTC().Add(-time.Duration(a.Config.StepUpWindowMinutes)*time.Minute))
	if err != nil {
		return domain.User{}, nil, false, err
	}
	return user, identities, recent, nil
}

func (a *App) currentWebChatSession(r *http.Request) (domain.WebAuthSession, error) {
	if a.WebAuth == nil {
		return domain.WebAuthSession{}, domain.ErrWebAuthSessionNotFound
	}
	cookie, err := r.Cookie(a.Config.WebChatCookieName)
	if err != nil {
		return domain.WebAuthSession{}, domain.ErrWebAuthSessionNotFound
	}
	session, err := a.WebAuth.GetWebAuthSession(r.Context(), cookie.Value, time.Now().UTC())
	if err != nil {
		return domain.WebAuthSession{}, err
	}
	return session, nil
}

func (a *App) resolveWebChatSession(ctx context.Context, authSession domain.WebAuthSession) (domain.Session, error) {
	session, _, err := a.Repo.ResolveSession(ctx, domain.CanonicalInboundEvent{
		EventID:  "webchat_bootstrap_" + authSession.ID,
		TenantID: a.Config.DefaultTenantID,
		Channel:  "webchat",
		Sender: domain.Sender{
			ChannelUserID: authSession.Email,
			DisplayName:   authSession.Email,
		},
		Conversation: domain.Conversation{
			ChannelConversationID: authSession.Email,
			ChannelThreadID:       authSession.ID,
			ChannelSurfaceKey:     authSession.ID,
		},
	}, a.Config.DefaultAgentProfileID)
	return session, err
}

func (a *App) issueWebChatCSRF(ctx context.Context, sessionID string) (string, error) {
	if a.WebAuth == nil {
		return "", domain.ErrWebAuthSessionNotFound
	}
	csrf := randomToken(18)
	if err := a.WebAuth.UpdateWebAuthSessionCSRFHash(ctx, sessionID, sha256Hex(csrf), time.Now().UTC()); err != nil {
		return "", err
	}
	return csrf, nil
}

func (a *App) webChatInteractionVisibilityMode() string {
	mode, err := config.NormalizeWebChatInteractionVisibility(a.Config.WebChatInteractionVisibility)
	if err != nil {
		return "full"
	}
	return mode
}

func (a *App) validateWebChatCSRF(r *http.Request, sessionID string) error {
	if a.WebAuth == nil {
		return domain.ErrWebAuthSessionNotFound
	}
	token := strings.TrimSpace(r.Header.Get("X-CSRF-Token"))
	if token == "" {
		return fmt.Errorf("missing csrf token")
	}
	session, err := a.WebAuth.GetWebAuthSession(r.Context(), sessionID, time.Now().UTC())
	if err != nil {
		return err
	}
	if session.CSRFTokenHash == "" || session.CSRFTokenHash != sha256Hex(token) {
		return fmt.Errorf("invalid csrf token")
	}
	return nil
}

func (a *App) loadWebChatState(ctx context.Context, authSession domain.WebAuthSession, limit int) (domain.Session, []domain.WebChatItem, error) {
	session, err := a.resolveWebChatSession(ctx, authSession)
	if err != nil {
		return domain.Session{}, nil, err
	}
	detail, err := a.Repo.GetSessionDetail(ctx, session.ID, limit)
	if err != nil {
		return domain.Session{}, nil, err
	}
	return session, buildWebChatItems(detail), nil
}

func (a *App) serveWebChatArtifact(ctx context.Context, w http.ResponseWriter, artifact domain.Artifact) error {
	if a.Artifacts.Store == nil {
		return fmt.Errorf("artifact store unavailable")
	}
	content, err := a.Artifacts.Store.Read(ctx, artifact.StorageURI)
	if err != nil {
		return err
	}
	setWebChatArtifactHeaders(w, artifact)
	w.Header().Set("Content-Length", strconv.Itoa(len(content)))
	_, err = w.Write(content)
	return err
}

func setWebChatArtifactHeaders(w http.ResponseWriter, artifact domain.Artifact) {
	contentType := strings.TrimSpace(artifact.MIMEType)
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	disposition := "attachment"
	if isInlineArtifactMIME(contentType) {
		disposition = "inline"
	}
	filename := strings.TrimSpace(artifact.Name)
	if filename == "" {
		filename = artifact.ID
	}
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Content-Disposition", fmt.Sprintf("%s; filename=%q", disposition, filename))
	w.Header().Set("Cache-Control", "private, no-store")
}

func webChatRemoteArtifactURL(artifact domain.Artifact) string {
	for _, candidate := range []string{artifact.StorageURI, artifact.SourceURL} {
		value := strings.TrimSpace(candidate)
		if strings.HasPrefix(value, "https://") || strings.HasPrefix(value, "http://") {
			return value
		}
	}
	return ""
}

func isInlineArtifactMIME(mimeType string) bool {
	mimeType = strings.ToLower(strings.TrimSpace(mimeType))
	return strings.HasPrefix(mimeType, "image/") ||
		strings.HasPrefix(mimeType, "audio/") ||
		strings.HasPrefix(mimeType, "video/")
}

func buildWebChatItems(detail domain.SessionDetail) []domain.WebChatItem {
	artifactByMessage := map[string][]domain.Artifact{}
	for _, artifact := range detail.Artifacts {
		artifactByMessage[artifact.MessageID] = append(artifactByMessage[artifact.MessageID], artifact)
	}
	items := make([]domain.WebChatItem, 0, len(detail.Messages)+len(detail.Awaits))
	for i := len(detail.Messages) - 1; i >= 0; i-- {
		msg := detail.Messages[i]
		role := msg.Role
		if role == "" {
			if msg.Direction == "inbound" {
				role = "user"
			} else {
				role = "assistant"
			}
		}
		items = append(items, domain.WebChatItem{
			ID:        msg.MessageID,
			Type:      "message",
			Role:      role,
			Text:      msg.Text,
			Partial:   webChatMessagePartial(msg),
			Artifacts: artifactByMessage[msg.MessageID],
		})
	}
	for _, await := range detail.Awaits {
		if await.Status != "pending" {
			continue
		}
		var model struct {
			Title   string                `json:"title"`
			Body    string                `json:"body"`
			Choices []domain.RenderChoice `json:"choices"`
		}
		_ = json.Unmarshal(await.PromptRenderJSON, &model)
		text := strings.TrimSpace(strings.Join([]string{model.Title, model.Body}, "\n\n"))
		if text == "" {
			text = "The agent needs your input to continue."
		}
		items = append(items, domain.WebChatItem{
			ID:      await.ID,
			Type:    "await",
			Role:    "assistant",
			Text:    text,
			AwaitID: await.ID,
			Choices: model.Choices,
			Status:  await.Status,
		})
	}
	return items
}

func buildWebChatActivity(repo ports.Repository, ctx context.Context, tenantID, sessionID string, items []domain.WebChatItem) *domain.WebChatActivity {
	if tenantID == "" || sessionID == "" {
		return nil
	}
	for _, item := range items {
		if item.Type == "await" && item.Status == "pending" {
			return nil
		}
	}
	startingRuns, err := repo.ListRuns(ctx, domain.RunListQuery{
		TenantID:   tenantID,
		SessionID:  sessionID,
		Status:     "starting",
		CursorPage: domain.CursorPage{Limit: 1},
	})
	if err == nil && len(startingRuns.Items) > 0 {
		return &domain.WebChatActivity{Phase: "thinking", UpdatedAt: webChatActivityTime(startingRuns.Items[0])}
	}
	runningRuns, err := repo.ListRuns(ctx, domain.RunListQuery{
		TenantID:   tenantID,
		SessionID:  sessionID,
		Status:     "running",
		CursorPage: domain.CursorPage{Limit: 1},
	})
	if err != nil || len(runningRuns.Items) == 0 {
		return nil
	}
	updatedAt := webChatActivityTime(runningRuns.Items[0])
	for _, item := range items {
		if item.Type != "message" || item.Role != "assistant" {
			continue
		}
		if item.Partial {
			return &domain.WebChatActivity{Phase: "typing", UpdatedAt: updatedAt}
		}
		break
	}
	return &domain.WebChatActivity{Phase: "working", UpdatedAt: updatedAt}
}

func webChatMessagePartial(msg domain.Message) bool {
	if len(msg.RawPayload) == 0 {
		return false
	}
	var payload struct {
		IsPartial bool `json:"is_partial"`
	}
	if err := json.Unmarshal(msg.RawPayload, &payload); err != nil {
		return false
	}
	return payload.IsPartial
}

func webChatActivityTime(run domain.Run) time.Time {
	if !run.LastEventAt.IsZero() {
		return run.LastEventAt
	}
	return run.StartedAt
}

func buildWebChatMessageEvent(tenantID string, authSession domain.WebAuthSession, text string, artifacts []domain.Artifact) domain.CanonicalInboundEvent {
	eventID := "webchat_evt_" + randomToken(8)
	messageType := "text"
	switch {
	case len(artifacts) > 0 && text != "":
		messageType = "mixed"
	case len(artifacts) > 0:
		messageType = "artifact"
	}
	parts := []domain.Part{}
	if text != "" {
		parts = append(parts, domain.Part{ContentType: "text/plain", Content: text})
	}
	rawPayload, _ := json.Marshal(map[string]any{
		"event_id": eventID,
		"text":     text,
		"artifacts": func() []map[string]any {
			out := make([]map[string]any, 0, len(artifacts))
			for _, artifact := range artifacts {
				out = append(out, map[string]any{
					"id":          artifact.ID,
					"name":        artifact.Name,
					"mime_type":   artifact.MIMEType,
					"size_bytes":  artifact.SizeBytes,
					"storage_uri": artifact.StorageURI,
				})
			}
			return out
		}(),
	})
	return domain.CanonicalInboundEvent{
		EventID:         eventID,
		TenantID:        tenantID,
		Channel:         "webchat",
		Interaction:     "message",
		ProviderEventID: eventID,
		ReceivedAt:      time.Now().UTC(),
		Sender: domain.Sender{
			ChannelUserID:       authSession.Email,
			DisplayName:         authSession.Email,
			IsAuthenticated:     true,
			IdentityAssurance:   "first_party_session",
			AllowedResponderIDs: []string{authSession.Email},
		},
		Conversation: domain.Conversation{
			ChannelConversationID: authSession.Email,
			ChannelThreadID:       authSession.ID,
			ChannelSurfaceKey:     authSession.ID,
		},
		Message: domain.Message{
			MessageID:   eventID + "_msg",
			MessageType: messageType,
			Text:        text,
			Parts:       parts,
			Artifacts:   artifacts,
		},
		Metadata: domain.Metadata{
			ArtifactTrust: "first-party-webchat",
			ResponderBinding: domain.ResponderBinding{
				Mode:                  "same-user-only",
				AllowedChannelUserIDs: []string{authSession.Email},
			},
			RawPayload: rawPayload,
		},
	}
}

func (a *App) readWebChatUploads(ctx context.Context, files []*multipart.FileHeader) ([]domain.Artifact, error) {
	out := make([]domain.Artifact, 0, len(files))
	for _, file := range files {
		handle, err := file.Open()
		if err != nil {
			return nil, err
		}
		content, err := io.ReadAll(handle)
		_ = handle.Close()
		if err != nil {
			return nil, err
		}
		mimeType := file.Header.Get("Content-Type")
		saved, err := a.Artifacts.SaveInbound(ctx, file.Filename, mimeType, content)
		if err != nil {
			return nil, err
		}
		out = append(out, saved)
	}
	return out, nil
}

func normalizeEmail(input string) (string, error) {
	addr, err := mail.ParseAddress(strings.TrimSpace(input))
	if err != nil {
		return "", fmt.Errorf("invalid email")
	}
	return strings.ToLower(strings.TrimSpace(addr.Address)), nil
}

func normalizePhone(input string) (string, string, error) {
	raw := strings.TrimSpace(input)
	if raw == "" {
		return "", "", fmt.Errorf("missing phone")
	}
	var b strings.Builder
	for i, r := range raw {
		switch {
		case r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == '+' && i == 0:
			b.WriteRune(r)
		case r == ' ' || r == '-' || r == '(' || r == ')' || r == '.':
		default:
			return "", "", fmt.Errorf("invalid phone")
		}
	}
	normalized := b.String()
	if normalized == "" {
		return "", "", fmt.Errorf("invalid phone")
	}
	if normalized[0] != '+' {
		normalized = "+" + normalized
	}
	digits := strings.TrimPrefix(normalized, "+")
	if len(digits) < 8 || len(digits) > 15 {
		return "", "", fmt.Errorf("invalid phone")
	}
	return raw, normalized, nil
}

func buildWebChatLinkHints(user domain.User, identities []domain.LinkedIdentity) map[string]map[string]any {
	hints := map[string]map[string]any{}
	for _, channel := range []string{"telegram", "whatsapp"} {
		hint := map[string]any{}
		if user.PrimaryPhone != "" {
			hint["primary_phone"] = user.PrimaryPhone
			hint["primary_phone_verified"] = user.PrimaryPhoneVerified
			hint["phone_hint"] = fmt.Sprintf("Use the %s account that matches %s when pairing.", channel, user.PrimaryPhone)
		}
		if channel == "whatsapp" && user.PrimaryPhoneNormalized != "" {
			matched := false
			for _, identity := range identities {
				if identity.ChannelType != "whatsapp" {
					continue
				}
				if normalizePhoneDigits(identity.ChannelUserID) == normalizePhoneDigits(user.PrimaryPhoneNormalized) {
					matched = true
					break
				}
			}
			hint["phone_match"] = matched
		}
		if len(hint) > 0 {
			hints[channel] = hint
		}
	}
	return hints
}

func normalizePhoneDigits(input string) string {
	var b strings.Builder
	for _, r := range input {
		if r >= '0' && r <= '9' {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func setWebChatCookie(w http.ResponseWriter, r *http.Request, name, value string, expiresAt time.Time) {
	http.SetCookie(w, &http.Cookie{
		Name:     name,
		Value:    value,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   externalRequestScheme(r) == "https",
		Expires:  expiresAt,
	})
}

func clearWebChatCookie(w http.ResponseWriter, name string) {
	http.SetCookie(w, &http.Cookie{Name: name, Value: "", Path: "/", MaxAge: -1, HttpOnly: true, SameSite: http.SameSiteLaxMode})
}

func absoluteURL(r *http.Request, path string) string {
	scheme := externalRequestScheme(r)
	host := externalRequestHost(r)
	return scheme + "://" + host + path
}

func externalRequestScheme(r *http.Request) string {
	if forwarded := strings.TrimSpace(r.Header.Get("Forwarded")); forwarded != "" {
		for _, part := range strings.Split(forwarded, ";") {
			part = strings.TrimSpace(part)
			if key, value, ok := strings.Cut(part, "="); ok && strings.EqualFold(strings.TrimSpace(key), "proto") {
				value = strings.Trim(strings.TrimSpace(value), "\"")
				if value != "" {
					return strings.ToLower(value)
				}
			}
		}
	}
	if proto := strings.TrimSpace(r.Header.Get("X-Forwarded-Proto")); proto != "" {
		if first, _, _ := strings.Cut(proto, ","); strings.TrimSpace(first) != "" {
			return strings.ToLower(strings.TrimSpace(first))
		}
	}
	if r.TLS != nil {
		return "https"
	}
	return "http"
}

func externalRequestHost(r *http.Request) string {
	if forwarded := strings.TrimSpace(r.Header.Get("Forwarded")); forwarded != "" {
		for _, part := range strings.Split(forwarded, ";") {
			part = strings.TrimSpace(part)
			if key, value, ok := strings.Cut(part, "="); ok && strings.EqualFold(strings.TrimSpace(key), "host") {
				value = strings.Trim(strings.TrimSpace(value), "\"")
				if value != "" {
					return value
				}
			}
		}
	}
	if host := strings.TrimSpace(r.Header.Get("X-Forwarded-Host")); host != "" {
		if first, _, _ := strings.Cut(host, ","); strings.TrimSpace(first) != "" {
			return strings.TrimSpace(first)
		}
	}
	return r.Host
}

func randomToken(n int) string {
	buf := make([]byte, n)
	_, _ = rand.Read(buf)
	return base64.RawURLEncoding.EncodeToString(buf)
}

func randomDigits(n int) string {
	const digits = "0123456789"
	buf := make([]byte, n)
	_, _ = rand.Read(buf)
	out := make([]byte, n)
	for i := range buf {
		out[i] = digits[int(buf[i])%len(digits)]
	}
	return string(out)
}

func sha256Hex(input string) string {
	sum := sha256.Sum256([]byte(input))
	return hex.EncodeToString(sum[:])
}
