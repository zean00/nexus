package app

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"nexus/internal/domain"
	"nexus/internal/httpx"
)

func (a *App) handleAdminIdentityLinkCode(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		httpx.Error(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if a.Identity == nil {
		httpx.Error(w, http.StatusServiceUnavailable, "identity unavailable")
		return
	}
	var body struct {
		UserID  string `json:"user_id"`
		Email   string `json:"email"`
		Channel string `json:"channel"`
	}
	if !decodeJSONBody(w, r, &body) {
		return
	}
	channel := normalizeAdminIdentityChannel(body.Channel)
	if channel == "" {
		httpx.Error(w, http.StatusBadRequest, "unsupported channel")
		return
	}
	user, err := a.adminIdentityUser(r, strings.TrimSpace(body.UserID), strings.TrimSpace(body.Email))
	if err != nil {
		status := http.StatusInternalServerError
		if errors.Is(err, domain.ErrIdentityUserNotFound) {
			status = http.StatusNotFound
		}
		httpx.Error(w, status, err.Error())
		return
	}
	code := randomDigits(8)
	now := time.Now().UTC()
	challenge := domain.StepUpChallenge{
		ID:          "link_" + randomToken(8),
		TenantID:    a.Config.DefaultTenantID,
		UserID:      user.ID,
		Purpose:     "link",
		ChannelType: channel,
		CodeHash:    sha256Hex(code),
		ExpiresAt:   now.Add(time.Duration(a.Config.IdentityLinkMinutes) * time.Minute),
		CreatedAt:   now,
	}
	if err := a.Identity.CreateStepUpChallenge(r.Context(), challenge, time.Minute); err != nil {
		if errors.Is(err, domain.ErrWebAuthRateLimited) {
			httpx.Error(w, http.StatusTooManyRequests, err.Error())
			return
		}
		httpx.Error(w, http.StatusInternalServerError, err.Error())
		return
	}
	if a.Repo != nil {
		_ = a.Repo.Audit(r.Context(), domain.AuditEvent{
			ID:            "audit_trust_link_code_" + randomToken(6),
			TenantID:      a.Config.DefaultTenantID,
			AggregateType: "user",
			AggregateID:   user.ID,
			EventType:     "trust.link_code_issued",
			PayloadJSON:   mustJSON(map[string]any{"channel": channel, "expires_at": challenge.ExpiresAt, "source": "admin"}),
			CreatedAt:     now,
		})
	}
	httpx.OK(w, map[string]any{
		"channel":    channel,
		"code":       code,
		"link_code":  user.ID + "." + code,
		"expires_at": challenge.ExpiresAt,
		"user_id":    user.ID,
	}, nil)
}

func (a *App) handleAdminIdentityLinkStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		httpx.Error(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if a.Identity == nil {
		httpx.Error(w, http.StatusServiceUnavailable, "identity unavailable")
		return
	}
	var body struct {
		UserID  string `json:"user_id"`
		Email   string `json:"email"`
		Channel string `json:"channel"`
	}
	if !decodeJSONBody(w, r, &body) {
		return
	}
	channel := normalizeAdminIdentityChannel(body.Channel)
	if channel == "" {
		httpx.Error(w, http.StatusBadRequest, "unsupported channel")
		return
	}
	user, err := a.adminIdentityUser(r, strings.TrimSpace(body.UserID), strings.TrimSpace(body.Email))
	if err != nil {
		status := http.StatusInternalServerError
		if errors.Is(err, domain.ErrIdentityUserNotFound) {
			status = http.StatusNotFound
		}
		httpx.Error(w, status, err.Error())
		return
	}
	links, err := a.Identity.ListLinkedIdentitiesForUser(r.Context(), a.Config.DefaultTenantID, user.ID)
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, err.Error())
		return
	}
	for _, link := range links {
		if !adminIdentityChannelMatches(channel, link.ChannelType) || link.Status != "linked" {
			continue
		}
		httpx.OK(w, map[string]any{
			"status":          "linked",
			"user_id":         user.ID,
			"channel":         link.ChannelType,
			"channel_user_id": link.ChannelUserID,
			"linked_at":       link.LinkedAt,
		}, nil)
		return
	}
	httpx.OK(w, map[string]any{"status": "unlinked", "user_id": user.ID, "channel": channel}, nil)
}

func (a *App) adminIdentityUser(r *http.Request, userID, email string) (domain.User, error) {
	if userID != "" {
		return a.Identity.GetUser(r.Context(), a.Config.DefaultTenantID, userID)
	}
	email = strings.ToLower(strings.TrimSpace(email))
	if email == "" || !strings.Contains(email, "@") {
		return domain.User{}, domain.ErrIdentityUserNotFound
	}
	return a.Identity.EnsureUserByEmail(r.Context(), a.Config.DefaultTenantID, email)
}

func normalizeAdminIdentityChannel(channel string) string {
	switch strings.ToLower(strings.TrimSpace(channel)) {
	case "whatsapp", "whatsapp_web":
		return strings.ToLower(strings.TrimSpace(channel))
	default:
		return ""
	}
}

func adminIdentityChannelMatches(requested, actual string) bool {
	if requested == actual {
		return true
	}
	return (requested == "whatsapp" || requested == "whatsapp_web") && (actual == "whatsapp" || actual == "whatsapp_web")
}
