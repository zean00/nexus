package app

import (
	"net/http"
	"strings"

	"nexus/internal/adapters/whatsappweb"
	"nexus/internal/httpx"
)

func (a *App) handleWhatsAppWebWebhook(w http.ResponseWriter, r *http.Request) {
	if !a.WhatsAppWebEnabled {
		http.NotFound(w, r)
		return
	}
	a.handleChannelWebhook(w, r, a.WhatsAppWeb)
}

func (a *App) handleWhatsAppWebSessionStatus(w http.ResponseWriter, r *http.Request) {
	if !a.WhatsAppWebEnabled {
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodGet {
		httpx.Error(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	status, err := a.whatsAppWebSession(r).GetSessionStatus(r.Context())
	if err != nil {
		httpx.Error(w, http.StatusBadGateway, err.Error())
		return
	}
	httpx.OK(w, status, actionMeta("whatsapp_web_session_status"))
}

func (a *App) handleWhatsAppWebSessionStart(w http.ResponseWriter, r *http.Request) {
	if !a.WhatsAppWebEnabled {
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodPost {
		httpx.Error(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	status, err := a.whatsAppWebSession(r).StartSession(r.Context())
	if err != nil {
		httpx.Error(w, http.StatusBadGateway, err.Error())
		return
	}
	httpx.OK(w, status, actionMeta("whatsapp_web_session_started"))
}

func (a *App) handleWhatsAppWebSessionStop(w http.ResponseWriter, r *http.Request) {
	if !a.WhatsAppWebEnabled {
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodPost {
		httpx.Error(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	status, err := a.whatsAppWebSession(r).StopSession(r.Context())
	if err != nil {
		httpx.Error(w, http.StatusBadGateway, err.Error())
		return
	}
	httpx.OK(w, status, actionMeta("whatsapp_web_session_stopped"))
}

func (a *App) handleWhatsAppWebSessionQR(w http.ResponseWriter, r *http.Request) {
	if !a.WhatsAppWebEnabled {
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodGet {
		httpx.Error(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	qr, err := a.whatsAppWebSession(r).GetQRCode(r.Context())
	if err != nil {
		httpx.Error(w, http.StatusBadGateway, err.Error())
		return
	}
	httpx.OK(w, qr, actionMeta("whatsapp_web_session_qr"))
}

func (a *App) handleWhatsAppWebWebhookSync(w http.ResponseWriter, r *http.Request) {
	if !a.WhatsAppWebEnabled {
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodPost {
		httpx.Error(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	status, err := a.whatsAppWebSession(r).SyncWebhook(r.Context())
	if err != nil {
		httpx.Error(w, http.StatusBadGateway, err.Error())
		return
	}
	httpx.OK(w, status, actionMeta("whatsapp_web_webhook_synced"))
}

func (a *App) whatsAppWebSession(r *http.Request) whatsappweb.Adapter {
	if session := strings.TrimSpace(r.URL.Query().Get("session")); session != "" {
		return a.WhatsAppWeb.WithSession(session)
	}
	return a.WhatsAppWeb
}
