package app

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"time"

	"nexus/internal/config"
	"nexus/internal/domain"
	"nexus/internal/httpx"
	"nexus/internal/ports"
)

// TenantDirectory resolves tenants by ID and by admin-token hash. The
// env-configured default tenant is always present without a registry row, so
// single-tenant deployments keep working unchanged.
type TenantDirectory struct {
	Store   ports.TenantRegistry // nil → env-only (default tenant)
	Default domain.TenantRecord

	mu        sync.RWMutex
	byID      map[string]domain.TenantRecord
	byToken   map[string]string // admin token hash → tenant ID
	refreshed time.Time
	ttl       time.Duration
}

func NewTenantDirectory(store ports.TenantRegistry, cfg config.Config) *TenantDirectory {
	return &TenantDirectory{
		Store: store,
		Default: domain.TenantRecord{
			TenantID:          cfg.DefaultTenantID,
			DisplayName:       "default",
			LajuBaseURL:       strings.TrimSpace(cfg.LajuBaseURL),
			LajuBearerToken:   inboundWebhookToken(cfg),
			InboundWebhookURL: inboundWebhookURL(cfg),
			WebChatAccountKey: strings.TrimSpace(cfg.WebChatAccountKey),
		},
		byID:    map[string]domain.TenantRecord{},
		byToken: map[string]string{},
		ttl:     30 * time.Second,
	}
}

func (d *TenantDirectory) refresh(ctx context.Context) {
	if d.Store == nil {
		return
	}
	d.mu.RLock()
	fresh := time.Since(d.refreshed) < d.ttl
	d.mu.RUnlock()
	if fresh {
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if time.Since(d.refreshed) < d.ttl {
		return
	}
	records, err := d.Store.ListTenants(ctx)
	if err != nil {
		// keep serving the previous snapshot
		return
	}
	byID := map[string]domain.TenantRecord{d.Default.TenantID: d.Default}
	byToken := map[string]string{}
	for _, rec := range records {
		byID[rec.TenantID] = rec
		if rec.AdminTokenHash != "" {
			byToken[rec.AdminTokenHash] = rec.TenantID
		}
	}
	d.byID = byID
	d.byToken = byToken
	d.refreshed = time.Now()
}

// Resolve returns the tenant record for tenantID, including the env default.
func (d *TenantDirectory) Resolve(ctx context.Context, tenantID string) (domain.TenantRecord, bool) {
	if tenantID == "" || tenantID == d.Default.TenantID {
		return d.Default, true
	}
	d.refresh(ctx)
	d.mu.RLock()
	rec, ok := d.byID[tenantID]
	d.mu.RUnlock()
	return rec, ok
}

// ResolveByAdminToken maps a plaintext bearer token to its tenant.
func (d *TenantDirectory) ResolveByAdminToken(ctx context.Context, token string) (domain.TenantRecord, bool) {
	if token == "" {
		return domain.TenantRecord{}, false
	}
	hash := sha256Hex(token)
	d.refresh(ctx)
	d.mu.RLock()
	tenantID, ok := d.byToken[hash]
	rec := d.byID[tenantID]
	d.mu.RUnlock()
	return rec, ok
}

// InboundTarget returns the laju URL and bearer token that inbound-forward
// events for tenantID are delivered to.
func (d *TenantDirectory) InboundTarget(ctx context.Context, tenantID string) (url string, token string, ok bool) {
	rec, ok := d.Resolve(ctx, tenantID)
	if !ok {
		return "", "", false
	}
	return rec.InboundWebhookEndpoint(), rec.LajuBearerToken, true
}

// HasRegisteredTenants reports whether any DB tenants carry an admin token.
func (d *TenantDirectory) HasRegisteredTenants(ctx context.Context) bool {
	if d.Store == nil {
		return false
	}
	d.refresh(ctx)
	d.mu.RLock()
	defer d.mu.RUnlock()
	return len(d.byToken) > 0
}

// Invalidate drops the cached snapshot so the next lookup re-reads the store.
func (d *TenantDirectory) Invalidate() {
	d.mu.Lock()
	d.refreshed = time.Time{}
	d.mu.Unlock()
}

type tenantScopeKey struct{}

// tenantScope binds a request to one tenant. It is set by the admin auth
// middleware (tenant tokens) and by the /t/{tenantId} gateway prefix.
type tenantScope struct {
	tenantID   string
	accountKey string
	operator   bool
}

func withTenantScope(ctx context.Context, scope tenantScope) context.Context {
	return context.WithValue(ctx, tenantScopeKey{}, scope)
}

// tenantID returns the tenant bound to this request context, falling back to
// the configured default tenant (pre-multi-tenancy behavior).
func (a *App) tenantID(ctx context.Context) string {
	if scope, ok := ctx.Value(tenantScopeKey{}).(tenantScope); ok && scope.tenantID != "" {
		return scope.tenantID
	}
	return a.Config.DefaultTenantID
}

// operator reports whether the caller is the nexus operator (env bearer or
// unauthenticated dev mode) rather than a tenant-scoped token.
func (a *App) operator(ctx context.Context) bool {
	scope, ok := ctx.Value(tenantScopeKey{}).(tenantScope)
	return !ok || scope.operator
}

// webChatAccountKey returns the tenant's webchat routing key when the request
// arrived tenant-scoped, else the env-configured key.
func (a *App) webChatAccountKey(ctx context.Context) string {
	if scope, ok := ctx.Value(tenantScopeKey{}).(tenantScope); ok && scope.accountKey != "" {
		return scope.accountKey
	}
	return a.Config.WebChatAccountKey
}

// inboundTargetFor resolves the delivery target for outbound laju forwards.
func (a *App) inboundTargetFor(ctx context.Context, tenantID string) (string, string, bool) {
	if a.Tenants == nil {
		url := inboundWebhookURL(a.Config)
		if url == "" {
			return "", "", false
		}
		return url, inboundWebhookToken(a.Config), true
	}
	return a.Tenants.InboundTarget(ctx, tenantID)
}

func (a *App) requireOperator(w http.ResponseWriter, r *http.Request) bool {
	if a.operator(r.Context()) {
		return true
	}
	httpx.Error(w, http.StatusForbidden, "operator token required")
	return false
}

type tenantPublicView struct {
	TenantID          string    `json:"tenantId"`
	DisplayName       string    `json:"displayName"`
	LajuBaseURL       string    `json:"lajuBaseUrl"`
	HasLajuCredential bool      `json:"hasLajuCredential"`
	HasAdminToken     bool      `json:"hasAdminToken"`
	WebChatAccountKey string    `json:"webchatAccountKey,omitempty"`
	CreatedAt         time.Time `json:"createdAt"`
	UpdatedAt         time.Time `json:"updatedAt"`
}

func tenantView(rec domain.TenantRecord) tenantPublicView {
	return tenantPublicView{
		TenantID:          rec.TenantID,
		DisplayName:       rec.DisplayName,
		LajuBaseURL:       rec.LajuBaseURL,
		HasLajuCredential: rec.LajuBearerToken != "",
		HasAdminToken:     rec.AdminTokenHash != "",
		WebChatAccountKey: rec.WebChatAccountKey,
		CreatedAt:         rec.CreatedAt,
		UpdatedAt:         rec.UpdatedAt,
	}
}

type tenantUpsertRequest struct {
	TenantID          string `json:"tenantId"`
	DisplayName       string `json:"displayName"`
	LajuBaseURL       string `json:"lajuBaseUrl"`
	LajuBearerToken   string `json:"lajuBearerToken"`
	AdminToken        string `json:"adminToken"`
	InboundWebhookURL string `json:"inboundWebhookUrl"`
	WebChatAccountKey string `json:"webchatAccountKey"`
}

// handleTenantRegistry serves GET (list) and POST (register/update) on
// /admin/tenants. Operator token required; secrets are never returned.
func (a *App) handleTenantRegistry(w http.ResponseWriter, r *http.Request) {
	if !a.requireOperator(w, r) {
		return
	}
	switch r.Method {
	case http.MethodGet:
		if a.Tenants == nil || a.Tenants.Store == nil {
			httpx.OK(w, map[string]any{"tenants": []tenantPublicView{tenantView(a.Tenants.Default)}}, nil)
			return
		}
		records, err := a.Tenants.Store.ListTenants(r.Context())
		if err != nil {
			httpx.Error(w, http.StatusInternalServerError, "list tenants failed")
			return
		}
		views := make([]tenantPublicView, 0, len(records)+1)
		views = append(views, tenantView(a.Tenants.Default))
		for _, rec := range records {
			if rec.TenantID == a.Tenants.Default.TenantID {
				continue
			}
			views = append(views, tenantView(rec))
		}
		httpx.OK(w, map[string]any{"tenants": views}, nil)
	case http.MethodPost:
		a.handleTenantUpsert(w, r)
	default:
		httpx.Error(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (a *App) handleTenantUpsert(w http.ResponseWriter, r *http.Request) {
	if a.Tenants == nil || a.Tenants.Store == nil {
		httpx.Error(w, http.StatusServiceUnavailable, "tenant registry unavailable")
		return
	}
	var req tenantUpsertRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		httpx.Error(w, http.StatusBadRequest, "invalid json body")
		return
	}
	req.TenantID = strings.TrimSpace(req.TenantID)
	if req.TenantID == "" || len(req.TenantID) > 128 {
		httpx.Error(w, http.StatusBadRequest, "tenantId is required (max 128 chars)")
		return
	}
	if req.TenantID == a.Tenants.Default.TenantID {
		httpx.Error(w, http.StatusBadRequest, "tenantId collides with the env default tenant")
		return
	}
	if strings.TrimSpace(req.LajuBaseURL) == "" && strings.TrimSpace(req.InboundWebhookURL) == "" {
		httpx.Error(w, http.StatusBadRequest, "lajuBaseUrl or inboundWebhookUrl is required")
		return
	}
	record := domain.TenantRecord{
		TenantID:          req.TenantID,
		DisplayName:       strings.TrimSpace(req.DisplayName),
		LajuBaseURL:       strings.TrimRight(strings.TrimSpace(req.LajuBaseURL), "/"),
		LajuBearerToken:   strings.TrimSpace(req.LajuBearerToken),
		InboundWebhookURL: strings.TrimSpace(req.InboundWebhookURL),
		WebChatAccountKey: strings.TrimSpace(req.WebChatAccountKey),
	}
	if strings.TrimSpace(req.AdminToken) != "" {
		record.AdminTokenHash = sha256Hex(strings.TrimSpace(req.AdminToken))
	} else if existing, err := a.Tenants.Store.GetTenant(r.Context(), req.TenantID); err == nil {
		record.AdminTokenHash = existing.AdminTokenHash // preserve on update
	}
	if err := a.Tenants.Store.UpsertTenant(r.Context(), record); err != nil {
		httpx.Error(w, http.StatusInternalServerError, "upsert tenant failed")
		return
	}
	saved, err := a.Tenants.Store.GetTenant(r.Context(), req.TenantID)
	if err != nil {
		saved = record
	}
	a.Tenants.Invalidate()
	httpx.OK(w, map[string]any{"tenant": tenantView(saved)}, nil)
}

// handleTenantItem serves DELETE /admin/tenants/{tenantId}.
func (a *App) handleTenantItem(w http.ResponseWriter, r *http.Request) {
	if !a.requireOperator(w, r) {
		return
	}
	if r.Method != http.MethodDelete {
		httpx.Error(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if a.Tenants == nil || a.Tenants.Store == nil {
		httpx.Error(w, http.StatusServiceUnavailable, "tenant registry unavailable")
		return
	}
	tenantID := strings.Trim(strings.TrimPrefix(r.URL.Path, "/admin/tenants/"), "/")
	if tenantID == "" || strings.Contains(tenantID, "/") {
		httpx.Error(w, http.StatusBadRequest, "invalid tenant id")
		return
	}
	if tenantID == a.Tenants.Default.TenantID {
		httpx.Error(w, http.StatusBadRequest, "cannot delete the env default tenant")
		return
	}
	if err := a.Tenants.Store.DeleteTenant(r.Context(), tenantID); err != nil {
		if err == domain.ErrTenantNotFound {
			httpx.Error(w, http.StatusNotFound, "tenant not found")
			return
		}
		httpx.Error(w, http.StatusInternalServerError, "delete tenant failed")
		return
	}
	a.Tenants.Invalidate()
	httpx.OK(w, map[string]any{"deleted": tenantID}, nil)
}
