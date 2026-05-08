package app

import (
	"context"
	"net/http"
	"strings"

	"nexus/internal/domain"
	"nexus/internal/httpx"
)

type webPushRepository interface {
	UpsertWebPushSubscription(ctx context.Context, sub domain.WebPushSubscription) (domain.WebPushSubscription, error)
	ListWebPushSubscriptions(ctx context.Context, tenantID, userID string) ([]domain.WebPushSubscription, error)
	RevokeWebPushSubscription(ctx context.Context, tenantID, userID, endpoint string) error
}

func (a *App) handleWebPushPublicKey(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		httpx.Error(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	httpx.OK(w, map[string]any{
		"enabled":    a.Config.WebPushEnabled && strings.TrimSpace(a.Config.WebPushVAPIDPublicKey) != "",
		"public_key": a.Config.WebPushVAPIDPublicKey,
	}, actionMeta("web_push_public_key"))
}

func (a *App) handleWebPushSubscriptions(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		a.handleListWebPushSubscriptions(w, r)
	case http.MethodPost:
		a.handleUpsertWebPushSubscription(w, r)
	case http.MethodDelete:
		a.handleRevokeWebPushSubscription(w, r)
	default:
		httpx.Error(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (a *App) handleListWebPushSubscriptions(w http.ResponseWriter, r *http.Request) {
	repo, ok := a.Repo.(webPushRepository)
	if !ok {
		httpx.Error(w, http.StatusNotImplemented, "web push repository is unavailable")
		return
	}
	userID := strings.TrimSpace(r.URL.Query().Get("user_id"))
	if userID == "" {
		httpx.Error(w, http.StatusBadRequest, "user_id required")
		return
	}
	items, err := repo.ListWebPushSubscriptions(r.Context(), a.Config.DefaultTenantID, userID)
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, err.Error())
		return
	}
	active := 0
	publicItems := make([]map[string]any, 0, len(items))
	for _, item := range items {
		if item.Status == "active" {
			active++
		}
		publicItems = append(publicItems, map[string]any{
			"id":           item.ID,
			"status":       item.Status,
			"user_agent":   item.UserAgent,
			"last_seen_at": item.LastSeenAt,
			"updated_at":   item.UpdatedAt,
		})
	}
	httpx.OK(w, map[string]any{"enabled": a.Config.WebPushEnabled, "active": active > 0, "subscriptions": publicItems}, actionMeta("web_push_subscriptions"))
}

func (a *App) handleUpsertWebPushSubscription(w http.ResponseWriter, r *http.Request) {
	if !a.Config.WebPushEnabled {
		httpx.Error(w, http.StatusServiceUnavailable, "web push is disabled")
		return
	}
	repo, ok := a.Repo.(webPushRepository)
	if !ok {
		httpx.Error(w, http.StatusNotImplemented, "web push repository is unavailable")
		return
	}
	var body struct {
		UserID       string `json:"user_id"`
		ACPSessionID string `json:"acp_session_id"`
		Endpoint     string `json:"endpoint"`
		Keys         struct {
			P256DH string `json:"p256dh"`
			Auth   string `json:"auth"`
		} `json:"keys"`
		UserAgent string `json:"user_agent"`
	}
	if !decodeJSONBody(w, r, &body) {
		return
	}
	if strings.TrimSpace(body.UserID) == "" || strings.TrimSpace(body.ACPSessionID) == "" || strings.TrimSpace(body.Endpoint) == "" || strings.TrimSpace(body.Keys.P256DH) == "" || strings.TrimSpace(body.Keys.Auth) == "" {
		httpx.Error(w, http.StatusBadRequest, "user_id, acp_session_id, endpoint, and keys are required")
		return
	}
	if !strings.HasPrefix(body.Endpoint, "https://") {
		httpx.Error(w, http.StatusBadRequest, "endpoint must be https")
		return
	}
	sub, err := repo.UpsertWebPushSubscription(r.Context(), domain.WebPushSubscription{
		TenantID:     a.Config.DefaultTenantID,
		UserID:       strings.TrimSpace(body.UserID),
		ACPSessionID: strings.TrimSpace(body.ACPSessionID),
		Endpoint:     strings.TrimSpace(body.Endpoint),
		P256DH:       strings.TrimSpace(body.Keys.P256DH),
		Auth:         strings.TrimSpace(body.Keys.Auth),
		UserAgent:    strings.TrimSpace(body.UserAgent),
	})
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, err.Error())
		return
	}
	httpx.OK(w, map[string]any{"id": sub.ID, "session_id": sub.SessionID, "status": sub.Status}, actionMeta("web_push_subscribed"))
}

func (a *App) handleRevokeWebPushSubscription(w http.ResponseWriter, r *http.Request) {
	repo, ok := a.Repo.(webPushRepository)
	if !ok {
		httpx.Error(w, http.StatusNotImplemented, "web push repository is unavailable")
		return
	}
	var body struct {
		UserID   string `json:"user_id"`
		Endpoint string `json:"endpoint"`
	}
	if !decodeJSONBody(w, r, &body) {
		return
	}
	if strings.TrimSpace(body.UserID) == "" || strings.TrimSpace(body.Endpoint) == "" {
		httpx.Error(w, http.StatusBadRequest, "user_id and endpoint required")
		return
	}
	if err := repo.RevokeWebPushSubscription(r.Context(), a.Config.DefaultTenantID, strings.TrimSpace(body.UserID), strings.TrimSpace(body.Endpoint)); err != nil {
		httpx.Error(w, http.StatusInternalServerError, err.Error())
		return
	}
	httpx.OK(w, map[string]any{"status": "revoked"}, actionMeta("web_push_unsubscribed"))
}
