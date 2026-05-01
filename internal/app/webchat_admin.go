package app

import (
	"net/http"
	"strings"
	"time"

	"nexus/internal/domain"
	"nexus/internal/httpx"
)

func (a *App) handleAdminWebChatSession(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		httpx.Error(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if a.WebAuth == nil || a.Identity == nil {
		httpx.Error(w, http.StatusServiceUnavailable, "webchat auth unavailable")
		return
	}
	var body struct {
		Email               string `json:"email"`
		SessionID           string `json:"session_id"`
		LinkedChannelType   string `json:"linked_channel_type"`
		LinkedChannelUserID string `json:"linked_channel_user_id"`
	}
	if !decodeJSONBody(w, r, &body) {
		return
	}
	email := strings.ToLower(strings.TrimSpace(body.Email))
	if email == "" || !strings.Contains(email, "@") {
		httpx.Error(w, http.StatusBadRequest, "email is required")
		return
	}
	sessionID := strings.TrimSpace(body.SessionID)
	if sessionID == "" {
		sessionID = "websess_" + randomToken(16)
	}
	now := time.Now().UTC()
	session := domain.WebAuthSession{
		ID:         sessionID,
		TenantID:   a.Config.DefaultTenantID,
		Email:      email,
		ExpiresAt:  now.Add(time.Duration(a.Config.WebChatSessionHours) * time.Hour),
		LastSeenAt: now,
		CreatedAt:  now,
	}
	if err := a.WebAuth.CreateWebAuthSession(r.Context(), session); err != nil {
		httpx.Error(w, http.StatusInternalServerError, err.Error())
		return
	}
	user, err := a.Identity.EnsureUserByEmail(r.Context(), a.Config.DefaultTenantID, email)
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, err.Error())
		return
	}
	for _, identity := range []domain.LinkedIdentity{
		{TenantID: a.Config.DefaultTenantID, UserID: user.ID, ChannelType: "webchat", ChannelUserID: email, Status: "linked", LinkedAt: now, LastVerifiedAt: now},
		{TenantID: a.Config.DefaultTenantID, UserID: user.ID, ChannelType: "email", ChannelUserID: email, Status: "linked", LinkedAt: now, LastVerifiedAt: now},
	} {
		if err := a.Identity.UpsertLinkedIdentity(r.Context(), identity); err != nil {
			httpx.Error(w, http.StatusInternalServerError, err.Error())
			return
		}
	}
	for _, linked := range adminWebChatLinkedIdentities(body.LinkedChannelType, body.LinkedChannelUserID) {
		if err := a.Identity.UpsertLinkedIdentity(r.Context(), domain.LinkedIdentity{
			TenantID:       a.Config.DefaultTenantID,
			UserID:         user.ID,
			ChannelType:    linked.ChannelType,
			ChannelUserID:  linked.ChannelUserID,
			Status:         "linked",
			LinkedAt:       now,
			LastVerifiedAt: now,
		}); err != nil {
			httpx.Error(w, http.StatusInternalServerError, err.Error())
			return
		}
	}
	httpx.OK(w, map[string]any{
		"session_id":  session.ID,
		"expires_at":  session.ExpiresAt,
		"cookie_name": a.Config.WebChatCookieName,
		"user_id":     user.ID,
	}, nil)
}

func adminWebChatLinkedIdentities(channelType, channelUserID string) []domain.LinkedIdentity {
	channelType = strings.ToLower(strings.TrimSpace(channelType))
	channelUserID = strings.TrimSpace(channelUserID)
	if channelType == "" || channelUserID == "" || channelType == "webchat" {
		return nil
	}
	if channelType == "whatsapp" || channelType == "whatsapp_web" {
		return []domain.LinkedIdentity{
			{ChannelType: "whatsapp", ChannelUserID: channelUserID},
			{ChannelType: "whatsapp_web", ChannelUserID: channelUserID},
		}
	}
	return []domain.LinkedIdentity{{ChannelType: channelType, ChannelUserID: channelUserID}}
}
