package acp

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"nexus/internal/domain"
)

func TestDiscoverAgents(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/agents" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{
			"agents": [{
				"name": "coder",
				"description": "Coding agent",
				"input_content_types": ["text/plain"],
				"output_content_types": ["text/plain", "application/json"],
				"capabilities": {
					"await_resume": true,
					"streaming": true,
					"artifacts": true
				},
				"healthy": true
			}]
		}`)
	}))
	defer server.Close()

	client := New(server.URL, "")
	client.HTTP = server.Client()

	agents, err := client.DiscoverAgents(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(agents) != 1 {
		t.Fatalf("expected 1 agent, got %d", len(agents))
	}
	if agents[0].Name != "coder" {
		t.Fatalf("expected coder, got %s", agents[0].Name)
	}
	if !agents[0].SupportsAwaitResume || !agents[0].SupportsStreaming || !agents[0].SupportsArtifacts {
		t.Fatal("expected full capability flags")
	}
}

func TestDiscoverAgentsRejectsMissingName(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"agents":[{"name":"","healthy":true}]}`)
	}))
	defer server.Close()

	client := New(server.URL, "")
	client.HTTP = server.Client()

	_, err := client.DiscoverAgents(t.Context())
	if err == nil || !strings.Contains(err.Error(), "missing name") {
		t.Fatalf("expected missing-name error, got %v", err)
	}
}

func TestStartRun(t *testing.T) {
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
		if payload["session_id"] != "acp_session_1" || payload["agent_name"] != "agent_a" || payload["text"] != "run please" {
			t.Fatalf("unexpected payload: %+v", payload)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"run_id":"r123","status":"done","output":"finished"}`)
	}))
	defer server.Close()

	client := New(server.URL, "secret-token")
	client.HTTP = server.Client()

	run, events, err := client.StartRun(t.Context(), domain.StartRunRequest{
		Session: domain.Session{ID: "session_1", ACPSessionID: "acp_session_1"},
		RouteDecision: domain.RouteDecision{
			ACPConnectionID: "acp_default",
			ACPAgentName:    "agent_a",
		},
		Message: domain.Message{Text: "run please"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if auth != "Bearer secret-token" {
		t.Fatalf("expected auth header, got %q", auth)
	}
	if run.ID != "run_r123" || run.ACPRunID != "r123" || run.Status != "done" {
		t.Fatalf("unexpected run: %+v", run)
	}
	if len(events) != 1 || events[0].Status != "completed" || events[0].Text != "finished" {
		t.Fatalf("unexpected run events: %+v", events)
	}
}

func TestStartRunRejectsMissingRequiredFields(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"run_id":"","status":"","output":"broken"}`)
	}))
	defer server.Close()

	client := New(server.URL, "")
	client.HTTP = server.Client()

	_, _, err := client.StartRun(t.Context(), domain.StartRunRequest{
		Session:       domain.Session{ID: "session_1", ACPSessionID: "acp_session_1"},
		RouteDecision: domain.RouteDecision{ACPAgentName: "agent_a"},
		Message:       domain.Message{Text: "run please"},
	})
	if err == nil || !strings.Contains(err.Error(), "missing required fields") {
		t.Fatalf("expected missing-fields error, got %v", err)
	}
}

func TestResumeRunRejectsMissingStatus(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/runs/run_1/resume" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"status":"","output":"broken"}`)
	}))
	defer server.Close()

	client := New(server.URL, "")
	client.HTTP = server.Client()

	_, err := client.ResumeRun(t.Context(), domain.Await{ID: "await_1", RunID: "run_1"}, []byte(`{"choice":"yes"}`))
	if err == nil || !strings.Contains(err.Error(), "missing required fields") {
		t.Fatalf("expected missing-fields error, got %v", err)
	}
}

func TestGetRunRejectsMalformedSnapshot(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/runs/acp_run_1" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"run_id":"","status":"running"}`)
	}))
	defer server.Close()

	client := New(server.URL, "")
	client.HTTP = server.Client()

	_, err := client.GetRun(t.Context(), "acp_run_1")
	if err == nil || !strings.Contains(err.Error(), "missing required fields") {
		t.Fatalf("expected malformed-snapshot error, got %v", err)
	}
}

func TestFindRunByIdempotencyKeyReturnsNotFound(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/runs" || r.URL.Query().Get("idempotency_key") != "queue_1" {
			t.Fatalf("unexpected request: path=%s query=%s", r.URL.Path, r.URL.RawQuery)
		}
		http.NotFound(w, r)
	}))
	defer server.Close()

	client := New(server.URL, "")
	client.HTTP = server.Client()

	snapshot, found, err := client.FindRunByIdempotencyKey(t.Context(), "queue_1")
	if err != nil {
		t.Fatal(err)
	}
	if found {
		t.Fatalf("expected not found, got snapshot %+v", snapshot)
	}
}

func TestFindRunByIdempotencyKeyRejectsMalformedSnapshot(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"run_id":"", "status":""}`)
	}))
	defer server.Close()

	client := New(server.URL, "")
	client.HTTP = server.Client()

	_, found, err := client.FindRunByIdempotencyKey(t.Context(), "queue_1")
	if err == nil || !strings.Contains(err.Error(), "missing required fields") {
		t.Fatalf("expected malformed-snapshot error, got %v", err)
	}
	if found {
		t.Fatal("expected found=false on malformed snapshot")
	}
}
