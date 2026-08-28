package app

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"nexus/internal/config"
	"nexus/internal/domain"
	"nexus/internal/ports"
)

// memTenantRegistry is an in-memory ports.TenantRegistry for tests.
type memTenantRegistry struct {
	mu      sync.Mutex
	records map[string]domain.TenantRecord
}

func newMemTenantRegistry() *memTenantRegistry {
	return &memTenantRegistry{records: map[string]domain.TenantRecord{}}
}

func (m *memTenantRegistry) UpsertTenant(_ context.Context, record domain.TenantRecord) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	stored := record
	if existing, ok := m.records[record.TenantID]; ok && stored.CreatedAt.IsZero() {
		stored.CreatedAt = existing.CreatedAt
	}
	m.records[record.TenantID] = stored
	return nil
}

func (m *memTenantRegistry) GetTenant(_ context.Context, tenantID string) (domain.TenantRecord, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if rec, ok := m.records[tenantID]; ok {
		return rec, nil
	}
	return domain.TenantRecord{}, domain.ErrTenantNotFound
}

func (m *memTenantRegistry) GetTenantByAdminTokenHash(_ context.Context, tokenHash string) (domain.TenantRecord, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, rec := range m.records {
		if rec.AdminTokenHash == tokenHash {
			return rec, nil
		}
	}
	return domain.TenantRecord{}, domain.ErrTenantNotFound
}

func (m *memTenantRegistry) ListTenants(context.Context) ([]domain.TenantRecord, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]domain.TenantRecord, 0, len(m.records))
	for _, rec := range m.records {
		out = append(out, rec)
	}
	return out, nil
}

func (m *memTenantRegistry) DeleteTenant(_ context.Context, tenantID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.records[tenantID]; !ok {
		return domain.ErrTenantNotFound
	}
	delete(m.records, tenantID)
	return nil
}

// sessionQueryCapturingStub records the last SessionListQuery it served.
type sessionQueryCapturingStub struct {
	appRepoStub
	mu    sync.Mutex
	query domain.SessionListQuery
}

func (s *sessionQueryCapturingStub) ListSessions(_ context.Context, query domain.SessionListQuery) (domain.PagedResult[domain.Session], error) {
	s.mu.Lock()
	s.query = query
	s.mu.Unlock()
	return domain.PagedResult[domain.Session]{}, nil
}

func multiTenantTestApp(t *testing.T, store ports.TenantRegistry, operatorToken string) *App {
	t.Helper()
	cfg := config.Config{DefaultTenantID: "tenant_default"}
	if operatorToken != "" {
		cfg.AdminBearerToken = operatorToken
	}
	return &App{
		Config:  cfg,
		Repo:    &sessionQueryCapturingStub{},
		Tenants: NewTenantDirectory(store, cfg),
	}
}

func registerTestTenant(t *testing.T, store ports.TenantRegistry, tenantID, adminToken, lajuBaseURL, lajuBearer string) {
	t.Helper()
	err := store.UpsertTenant(context.Background(), domain.TenantRecord{
		TenantID:        tenantID,
		LajuBaseURL:     lajuBaseURL,
		LajuBearerToken: lajuBearer,
		AdminTokenHash:  sha256Hex(adminToken),
	})
	if err != nil {
		t.Fatal(err)
	}
}

func doAdmin(t *testing.T, app *App, method, path, bearer, body string) *httptest.ResponseRecorder {
	t.Helper()
	var reader *strings.Reader
	if body == "" {
		reader = strings.NewReader("")
	} else {
		reader = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, path, reader)
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	rec := httptest.NewRecorder()
	app.AdminHandler().ServeHTTP(rec, req)
	return rec
}

func TestAdminAuthTenantTokenScopesToTenant(t *testing.T) {
	store := newMemTenantRegistry()
	registerTestTenant(t, store, "tenant_alpha", "alpha-token", "https://alpha.example.com", "alpha-laju")
	registerTestTenant(t, store, "tenant_beta", "beta-token", "https://beta.example.com", "beta-laju")
	app := multiTenantTestApp(t, store, "op-token")
	stub := app.Repo.(*sessionQueryCapturingStub)

	if rec := doAdmin(t, app, http.MethodGet, "/admin/sessions", "", ""); rec.Code != http.StatusUnauthorized {
		t.Fatalf("missing token: expected 401, got %d", rec.Code)
	}
	if rec := doAdmin(t, app, http.MethodGet, "/admin/sessions", "wrong-token", ""); rec.Code != http.StatusUnauthorized {
		t.Fatalf("unknown token: expected 401, got %d", rec.Code)
	}
	if rec := doAdmin(t, app, http.MethodGet, "/admin/sessions", "op-token", ""); rec.Code != http.StatusOK {
		t.Fatalf("operator token: expected 200, got %d", rec.Code)
	}
	stub.mu.Lock()
	if stub.query.TenantID != "tenant_default" {
		t.Fatalf("operator should default to default tenant, got %q", stub.query.TenantID)
	}
	stub.mu.Unlock()

	if rec := doAdmin(t, app, http.MethodGet, "/admin/sessions", "alpha-token", ""); rec.Code != http.StatusOK {
		t.Fatalf("tenant token: expected 200, got %d (body %s)", rec.Code, rec.Body.String())
	}
	stub.mu.Lock()
	if stub.query.TenantID != "tenant_alpha" {
		t.Fatalf("tenant token should scope to tenant_alpha, got %q", stub.query.TenantID)
	}
	stub.mu.Unlock()

	if rec := doAdmin(t, app, http.MethodGet, "/admin/sessions", "beta-token", ""); rec.Code != http.StatusOK {
		t.Fatalf("second tenant token: expected 200, got %d", rec.Code)
	}
	stub.mu.Lock()
	if stub.query.TenantID != "tenant_beta" {
		t.Fatalf("second tenant token should scope to tenant_beta, got %q", stub.query.TenantID)
	}
	stub.mu.Unlock()

	// Tenant tokens are not operators: registry management is forbidden.
	if rec := doAdmin(t, app, http.MethodGet, "/admin/tenants", "alpha-token", ""); rec.Code != http.StatusForbidden {
		t.Fatalf("tenant token on /admin/tenants: expected 403, got %d", rec.Code)
	}
}

func TestAdminAuthDevModeFallbacks(t *testing.T) {
	// No operator token, no registered tenants → auth fully open (back-compat).
	app := multiTenantTestApp(t, nil, "")
	if rec := doAdmin(t, app, http.MethodGet, "/admin/sessions", "", ""); rec.Code != http.StatusOK {
		t.Fatalf("dev mode: expected 200 without auth, got %d", rec.Code)
	}

	// No operator token but registered tenants → a tenant token is required.
	store := newMemTenantRegistry()
	registerTestTenant(t, store, "tenant_alpha", "alpha-token", "https://alpha.example.com", "alpha-laju")
	app = multiTenantTestApp(t, store, "")
	if rec := doAdmin(t, app, http.MethodGet, "/admin/sessions", "", ""); rec.Code != http.StatusUnauthorized {
		t.Fatalf("no token with tenants registered: expected 401, got %d", rec.Code)
	}
	if rec := doAdmin(t, app, http.MethodGet, "/admin/sessions", "alpha-token", ""); rec.Code != http.StatusOK {
		t.Fatalf("tenant token with no operator token: expected 200, got %d", rec.Code)
	}
}

func TestTenantRegistryEndpoints(t *testing.T) {
	store := newMemTenantRegistry()
	app := multiTenantTestApp(t, store, "op-token")

	body := `{"tenantId":"tenant_alpha","displayName":"Alpha","lajuBaseUrl":"https://alpha.example.com/","lajuBearerToken":"alpha-laju","adminToken":"alpha-token"}`
	rec := doAdmin(t, app, http.MethodPost, "/admin/tenants", "op-token", body)
	if rec.Code != http.StatusOK {
		t.Fatalf("upsert: expected 200, got %d (body %s)", rec.Code, rec.Body.String())
	}
	var created struct {
		Data struct {
			Tenant struct {
				TenantID      string `json:"tenantId"`
				LajuBaseURL   string `json:"lajuBaseUrl"`
				HasAdminToken bool   `json:"hasAdminToken"`
			} `json:"tenant"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	if created.Data.Tenant.TenantID != "tenant_alpha" || created.Data.Tenant.LajuBaseURL != "https://alpha.example.com" || !created.Data.Tenant.HasAdminToken {
		t.Fatalf("unexpected upsert response %+v", created.Data.Tenant)
	}
	if strings.Contains(rec.Body.String(), "alpha-laju") || strings.Contains(rec.Body.String(), "alpha-token") {
		t.Fatalf("upsert response leaked secrets: %s", rec.Body.String())
	}

	// The freshly registered tenant token must authenticate immediately.
	if rec := doAdmin(t, app, http.MethodGet, "/admin/sessions", "alpha-token", ""); rec.Code != http.StatusOK {
		t.Fatalf("registered tenant token: expected 200, got %d", rec.Code)
	}

	rec = doAdmin(t, app, http.MethodGet, "/admin/tenants", "op-token", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("list: expected 200, got %d", rec.Code)
	}
	var listed struct {
		Data struct {
			Tenants []struct {
				TenantID string `json:"tenantId"`
			} `json:"tenants"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &listed); err != nil {
		t.Fatal(err)
	}
	if len(listed.Data.Tenants) != 2 {
		t.Fatalf("expected default + 1 tenant, got %+v", listed.Data.Tenants)
	}

	// Validation: default tenant id is reserved.
	rec = doAdmin(t, app, http.MethodPost, "/admin/tenants", "op-token", `{"tenantId":"tenant_default","lajuBaseUrl":"https://x.example.com"}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("default tenant id: expected 400, got %d", rec.Code)
	}
	// Validation: a target is required.
	rec = doAdmin(t, app, http.MethodPost, "/admin/tenants", "op-token", `{"tenantId":"tenant_gamma"}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("missing target: expected 400, got %d", rec.Code)
	}

	rec = doAdmin(t, app, http.MethodDelete, "/admin/tenants/tenant_alpha", "op-token", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("delete: expected 200, got %d (body %s)", rec.Code, rec.Body.String())
	}
	if rec := doAdmin(t, app, http.MethodGet, "/admin/sessions", "alpha-token", ""); rec.Code != http.StatusUnauthorized {
		t.Fatalf("deleted tenant token: expected 401, got %d", rec.Code)
	}
	if rec := doAdmin(t, app, http.MethodDelete, "/admin/tenants/tenant_alpha", "op-token", ""); rec.Code != http.StatusNotFound {
		t.Fatalf("double delete: expected 404, got %d", rec.Code)
	}
}

func TestGatewayTenantScopedPrefix(t *testing.T) {
	store := newMemTenantRegistry()
	registerTestTenant(t, store, "tenant_alpha", "alpha-token", "https://alpha.example.com", "alpha-laju")
	app := multiTenantTestApp(t, store, "op-token")

	do := func(path string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		rec := httptest.NewRecorder()
		app.GatewayHandler().ServeHTTP(rec, req)
		return rec
	}
	if rec := do("/t/tenant_alpha/healthz"); rec.Code != http.StatusOK {
		t.Fatalf("tenant-scoped healthz: expected 200, got %d", rec.Code)
	}
	if rec := do("/t/tenant_default/healthz"); rec.Code != http.StatusOK {
		t.Fatalf("default tenant via prefix: expected 200, got %d", rec.Code)
	}
	if rec := do("/t/unknown_tenant/healthz"); rec.Code != http.StatusNotFound {
		t.Fatalf("unknown tenant: expected 404, got %d", rec.Code)
	}
	if rec := do("/t/"); rec.Code != http.StatusNotFound {
		t.Fatalf("empty tenant id: expected 404, got %d", rec.Code)
	}
	if rec := do("/healthz"); rec.Code != http.StatusOK {
		t.Fatalf("unprefixed healthz: expected 200, got %d", rec.Code)
	}
}

func TestTenantScopeHelpers(t *testing.T) {
	store := newMemTenantRegistry()
	app := multiTenantTestApp(t, store, "op-token")
	app.Config.WebChatAccountKey = "env-key"

	ctx := context.Background()
	if got := app.tenantID(ctx); got != "tenant_default" {
		t.Fatalf("fallback tenant: %q", got)
	}
	if got := app.webChatAccountKey(ctx); got != "env-key" {
		t.Fatalf("fallback account key: %q", got)
	}
	if !app.operator(ctx) {
		t.Fatal("no scope should read as operator")
	}

	scoped := withTenantScope(ctx, tenantScope{tenantID: "tenant_alpha", accountKey: "alpha-key"})
	if got := app.tenantID(scoped); got != "tenant_alpha" {
		t.Fatalf("scoped tenant: %q", got)
	}
	if got := app.webChatAccountKey(scoped); got != "alpha-key" {
		t.Fatalf("scoped account key: %q", got)
	}
	if app.operator(scoped) {
		t.Fatal("tenant scope must not be operator")
	}
}

func TestInboundTargetResolution(t *testing.T) {
	store := newMemTenantRegistry()
	registerTestTenant(t, store, "tenant_alpha", "alpha-token", "https://alpha.example.com", "alpha-laju")
	cfg := config.Config{
		DefaultTenantID:  "tenant_default",
		LajuBaseURL:      "https://default.example.com",
		LajuBearerToken:  "default-laju",
		AdminBearerToken: "op-token",
	}
	app := &App{Config: cfg, Repo: &sessionQueryCapturingStub{}, Tenants: NewTenantDirectory(store, cfg)}

	url, token, ok := app.inboundTargetFor(context.Background(), "tenant_alpha")
	if !ok || url != "https://alpha.example.com/api/integrations/nexus/inbound" || token != "alpha-laju" {
		t.Fatalf("tenant target: %q %q %v", url, token, ok)
	}
	url, token, ok = app.inboundTargetFor(context.Background(), "tenant_default")
	if !ok || url != "https://default.example.com/api/integrations/nexus/inbound" || token != "default-laju" {
		t.Fatalf("default target: %q %q %v", url, token, ok)
	}
	if _, _, ok := app.inboundTargetFor(context.Background(), "tenant_ghost"); ok {
		t.Fatal("unknown tenant should not resolve")
	}

	// App built without a directory (unit-test style) falls back to env wiring.
	legacy := &App{Config: config.Config{LajuBaseURL: "https://legacy.example.com", LajuBearerToken: "legacy-laju"}}
	url, token, ok = legacy.inboundTargetFor(context.Background(), "anything")
	if !ok || url != "https://legacy.example.com/api/integrations/nexus/inbound" || token != "legacy-laju" {
		t.Fatalf("legacy target: %q %q %v", url, token, ok)
	}
	legacy.Config.LajuBaseURL = ""
	if _, _, ok := legacy.inboundTargetFor(context.Background(), "anything"); ok {
		t.Fatal("legacy without laju URL should not resolve")
	}
}
