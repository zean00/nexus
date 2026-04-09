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

func TestDiscoverAgents(t *testing.T) {
	var gotDirectory string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/agent" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		gotDirectory = r.URL.Query().Get("directory")
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `[{"name":"build","description":"builder"}]`)
	}))
	defer server.Close()

	client := New(server.URL, "")
	client.HTTP = server.Client()
	client.Directory = "/tmp/project"

	agents, err := client.DiscoverAgents(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if gotDirectory != "/tmp/project" {
		t.Fatalf("expected directory query param, got %q", gotDirectory)
	}
	if len(agents) != 1 {
		t.Fatalf("expected 1 agent, got %d", len(agents))
	}
	if agents[0].Name != "build" || agents[0].Protocol != "opencode" || agents[0].SupportsAwaitResume || agents[0].SupportsStructuredAwait || agents[0].SupportsSessionReload || !agents[0].SupportsStreaming || !agents[0].SupportsArtifacts {
		t.Fatalf("unexpected manifest: %+v", agents[0])
	}
}

func TestDiscoverAgentsRejectsMissingName(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `[{"name":"","description":"broken"}]`)
	}))
	defer server.Close()

	client := New(server.URL, "")
	client.HTTP = server.Client()

	_, err := client.DiscoverAgents(context.Background())
	if err == nil || !strings.Contains(err.Error(), "missing name") {
		t.Fatalf("expected missing-name error, got %v", err)
	}
}

func TestEnsureSessionReusesByTitle(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/session":
			if r.URL.Query().Get("search") != "gateway:session_1" {
				t.Fatalf("unexpected search query: %s", r.URL.RawQuery)
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `[{"id":"ses_existing","title":"gateway:session_1"}]`)
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
	}))
	defer server.Close()

	client := New(server.URL, "")
	client.HTTP = server.Client()

	got, err := client.EnsureSession(context.Background(), domain.Session{ID: "session_1"})
	if err != nil {
		t.Fatal(err)
	}
	if got != "ses_existing" {
		t.Fatalf("expected existing session, got %q", got)
	}
}

func TestStartRun(t *testing.T) {
	var auth string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth = r.Header.Get("Authorization")
		if r.URL.Path != "/session/ses_1/message" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatal(err)
		}
		if payload["agent"] != "build" {
			t.Fatalf("unexpected agent payload: %+v", payload)
		}
		parts, ok := payload["parts"].([]any)
		if !ok || len(parts) != 1 {
			t.Fatalf("unexpected parts payload: %+v", payload)
		}
		part := parts[0].(map[string]any)
		if part["type"] != "text" || part["text"] != "run please" {
			t.Fatalf("unexpected part payload: %+v", part)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{
			"info":{"id":"msg_1","sessionID":"ses_1","providerID":"openai","modelID":"gpt-5.4-mini","agent":"build"},
			"parts":[
				{"id":"part_1","type":"reasoning","text":"thinking"},
				{"id":"part_2","type":"text","text":"finished"}
			]
		}`)
	}))
	defer server.Close()

	client := New(server.URL, "secret-token")
	client.HTTP = server.Client()

	run, stream, err := client.StartRun(context.Background(), domain.StartRunRequest{
		Session: domain.Session{ID: "session_1", ACPSessionID: "ses_1"},
		RouteDecision: domain.RouteDecision{
			ACPAgentName: "build",
		},
		Message:        domain.Message{Text: "run please"},
		IdempotencyKey: "queue_1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if auth != "Bearer secret-token" {
		t.Fatalf("expected auth header, got %q", auth)
	}
	if run.ID != "run_ses_1:msg_1" || run.ACPRunID != "ses_1:msg_1" || run.Status != "completed" {
		t.Fatalf("unexpected run: %+v", run)
	}
	events := collectRunEvents(t, stream)
	if len(events) != 1 || events[0].Status != "completed" || !strings.Contains(events[0].Text, "finished") {
		t.Fatalf("unexpected run events: %+v", events)
	}
}

func TestStartRunRejectsMissingRequiredFields(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"info":{"id":"","sessionID":"ses_1"},"parts":[]}`)
	}))
	defer server.Close()

	client := New(server.URL, "")
	client.HTTP = server.Client()

	_, _, err := client.StartRun(context.Background(), domain.StartRunRequest{
		Session:       domain.Session{ID: "session_1", ACPSessionID: "ses_1"},
		RouteDecision: domain.RouteDecision{ACPAgentName: "build"},
		Message:       domain.Message{Text: "run please"},
	})
	if err == nil || !strings.Contains(err.Error(), "missing required fields") {
		t.Fatalf("expected missing-fields error, got %v", err)
	}
}

func TestResumeRun(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/session/ses_1/message" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatal(err)
		}
		parts := payload["parts"].([]any)
		part := parts[0].(map[string]any)
		if part["text"] != `{"choice":"yes"}` {
			t.Fatalf("unexpected resume payload: %+v", part)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{
			"info":{"id":"msg_2","sessionID":"ses_1","providerID":"openai","modelID":"gpt-5.4-mini","agent":"build"},
			"parts":[{"id":"part_2","type":"text","text":"resumed"}]
		}`)
	}))
	defer server.Close()

	client := New(server.URL, "")
	client.HTTP = server.Client()

	stream, err := client.ResumeRun(context.Background(), domain.Await{ID: "await_1", RunID: "run_ses_1:msg_1", SessionID: "session_1"}, []byte(`{"choice":"yes"}`))
	if err != nil {
		t.Fatal(err)
	}
	events := collectRunEvents(t, stream)
	if len(events) != 1 || events[0].Text != "resumed" || events[0].Status != "completed" {
		t.Fatalf("unexpected resume events: %+v", events)
	}
}

func TestGetRunRejectsMalformedSnapshot(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/session/ses_1/message/msg_1" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"info":{"id":"","sessionID":"ses_1"},"parts":[]}`)
	}))
	defer server.Close()

	client := New(server.URL, "")
	client.HTTP = server.Client()

	_, err := client.GetRun(context.Background(), "ses_1:msg_1")
	if err == nil || !strings.Contains(err.Error(), "missing required fields") {
		t.Fatalf("expected malformed-snapshot error, got %v", err)
	}
}

func TestFindLatestRunForSessionReturnsNotFound(t *testing.T) {
	client := New("http://example.invalid", "")
	snapshot, found, err := client.FindLatestRunForSession(context.Background(), domain.Session{ID: "session_1"})
	if err != nil {
		t.Fatal(err)
	}
	if found {
		t.Fatalf("expected not found, got snapshot %+v", snapshot)
	}
}

func TestFindLatestRunForSession(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/session/ses_1/message" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `[
			{"info":{"id":"msg_user","sessionID":"ses_1","role":"user"},"parts":[]},
			{"info":{"id":"msg_2","sessionID":"ses_1","role":"assistant","providerID":"openai","modelID":"gpt-5.4-mini","agent":"build"},"parts":[{"id":"part_2","type":"text","text":"latest"}]}
		]`)
	}))
	defer server.Close()

	client := New(server.URL, "")
	client.HTTP = server.Client()

	snapshot, found, err := client.FindLatestRunForSession(context.Background(), domain.Session{ID: "session_1", ACPSessionID: "ses_1"})
	if err != nil {
		t.Fatal(err)
	}
	if !found {
		t.Fatal("expected latest assistant run to be found")
	}
	if snapshot.ACPRunID != "ses_1:msg_2" || snapshot.Output != "latest" {
		t.Fatalf("unexpected snapshot: %+v", snapshot)
	}
}

func TestCancelRun(t *testing.T) {
	var called bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/session/ses_1/abort" {
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		called = true
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `true`)
	}))
	defer server.Close()

	client := New(server.URL, "")
	client.HTTP = server.Client()

	if err := client.CancelRun(context.Background(), domain.Run{ACPRunID: "ses_1:msg_1"}); err != nil {
		t.Fatal(err)
	}
	if !called {
		t.Fatal("expected abort request")
	}
}
