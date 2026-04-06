package acp

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"nexus/internal/domain"
)

func TestStrictDiscoverAgents(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/agents" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `[{"name":"strict-agent","supports_await_resume":true,"supports_structured_await":true,"supports_session_reload":false,"supports_streaming":true,"supports_artifacts":true,"healthy":true}]`)
	}))
	defer server.Close()

	client := NewStrictClient(server.URL, "")
	client.HTTP = server.Client()

	agents, err := client.DiscoverAgents(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(agents) != 1 || agents[0].Name != "strict-agent" || agents[0].Protocol != "" || !agents[0].SupportsAwaitResume || agents[0].SupportsSessionReload {
		t.Fatalf("unexpected strict manifests: %+v", agents)
	}
}

func TestStrictStartRun(t *testing.T) {
	var auth string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth = r.Header.Get("Authorization")
		if r.URL.Path != "/runs" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatal(err)
		}
		if payload["agent_name"] != "strict-agent" || payload["idempotency_key"] != "queue_1" {
			t.Fatalf("unexpected strict run payload: %+v", payload)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"run_1","session_id":"ses_1","status":"completed","output":"done"}`)
	}))
	defer server.Close()

	client := NewStrictClient(server.URL, "secret")
	client.HTTP = server.Client()

	run, events, err := client.StartRun(context.Background(), domain.StartRunRequest{
		Session:        domain.Session{ID: "session_1", ACPSessionID: "ses_1"},
		RouteDecision:  domain.RouteDecision{ACPAgentName: "strict-agent"},
		Message:        domain.Message{Text: "run please"},
		IdempotencyKey: "queue_1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if auth != "Bearer secret" {
		t.Fatalf("expected auth header, got %q", auth)
	}
	if run.ACPRunID != "run_1" || run.Status != "completed" {
		t.Fatalf("unexpected strict run: %+v", run)
	}
	if len(events) != 1 || events[0].Text != "done" || events[0].Status != "completed" {
		t.Fatalf("unexpected strict events: %+v", events)
	}
}

func TestStrictResumeRun(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/runs/run_1/resume" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"run_1","session_id":"ses_1","status":"completed","output":"resumed"}`)
	}))
	defer server.Close()

	client := NewStrictClient(server.URL, "")
	client.HTTP = server.Client()

	events, err := client.ResumeRun(context.Background(), domain.Await{RunID: "run_run_1", SessionID: "session_1"}, []byte(`{"choice":"yes"}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].Text != "resumed" {
		t.Fatalf("unexpected strict resume events: %+v", events)
	}
}

func TestStrictGetRunAndFindLatest(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/runs/run_1":
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"id":"run_1","session_id":"ses_1","status":"awaiting","output":"waiting","await":{"schema":"c2NoZW1h","prompt":"cHJvbXB0"}}`)
		case "/sessions/ses_1/runs":
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `[{"id":"run_2","session_id":"ses_1","status":"completed","output":"latest"}]`)
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer server.Close()

	client := NewStrictClient(server.URL, "")
	client.HTTP = server.Client()

	snapshot, err := client.GetRun(context.Background(), "run_1")
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.ACPRunID != "run_1" || snapshot.Status != "awaiting" {
		t.Fatalf("unexpected strict snapshot: %+v", snapshot)
	}
	latest, found, err := client.FindLatestRunForSession(context.Background(), domain.Session{ACPSessionID: "ses_1"})
	if err != nil {
		t.Fatal(err)
	}
	if !found || latest.ACPRunID != "run_2" || latest.Status != "completed" {
		t.Fatalf("unexpected latest strict snapshot: %+v found=%v", latest, found)
	}
}

func TestBridgeFactorySelectsStrict(t *testing.T) {
	bridge := NewBridge(BridgeConfig{Implementation: "strict", BaseURL: "http://example.invalid"})
	if _, ok := bridge.(StrictClient); !ok {
		t.Fatalf("expected strict client, got %T", bridge)
	}
	bridge = NewBridge(BridgeConfig{Implementation: "opencode", BaseURL: "http://example.invalid"})
	if _, ok := bridge.(Client); !ok {
		t.Fatalf("expected opencode bridge client, got %T", bridge)
	}
}

func TestStrictDiscoverRejectsMissingName(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `[{"name":"","healthy":true}]`)
	}))
	defer server.Close()

	client := NewStrictClient(server.URL, "")
	client.HTTP = server.Client()

	_, err := client.DiscoverAgents(context.Background())
	if err == nil || !strings.Contains(err.Error(), "missing name") {
		t.Fatalf("expected missing-name error, got %v", err)
	}
}
