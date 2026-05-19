package acp

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"nexus/internal/domain"
)

func TestSSEStartRunStreamsEvents(t *testing.T) {
	var runPayload map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/runs":
			if r.Method != http.MethodPost {
				t.Fatalf("expected POST /runs, got %s", r.Method)
			}
			if err := json.NewDecoder(r.Body).Decode(&runPayload); err != nil {
				t.Fatal(err)
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"id":"run_1","session_id":"acp_session_1","status":"running"}`)
		case "/runs/run_1/events":
			if r.Header.Get("Accept") != "text/event-stream" {
				t.Fatalf("expected SSE accept header, got %q", r.Header.Get("Accept"))
			}
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = io.WriteString(w, ": heartbeat\n\n")
			_, _ = io.WriteString(w, "event: message\n")
			_, _ = io.WriteString(w, `data: {"id":"run_1","session_id":"acp_session_1","status":"running","text":"hel","partial":true}`+"\n\n")
			_, _ = io.WriteString(w, `data: {"id":"run_1","session_id":"acp_session_1","status":"completed","output":"hello"}`+"\n\n")
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer server.Close()

	client := NewSSEClient(server.URL, "")
	client.HTTP = server.Client()
	run, stream, err := client.StartRun(context.Background(), domain.StartRunRequest{
		Session:        domain.Session{ID: "session_1", ACPSessionID: "acp_session_1"},
		RouteDecision:  domain.RouteDecision{ACPAgentName: "support"},
		Message:        domain.Message{Text: "hi"},
		IdempotencyKey: "queue_1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if run.ACPRunID != "run_1" || run.Status != "running" {
		t.Fatalf("unexpected run: %+v", run)
	}
	if runPayload["agent_name"] != "support" || runPayload["idempotency_key"] != "queue_1" {
		t.Fatalf("unexpected run payload: %+v", runPayload)
	}
	events := collectRunEvents(t, stream)
	if len(events) != 2 || events[0].RunID != run.ID || events[0].Text != "hel" || !events[0].IsPartial || events[1].Text != "hello" || events[1].Status != "completed" || events[1].IsPartial {
		t.Fatalf("unexpected SSE events: %+v", events)
	}
}

func TestSSEResumeRunForSessionUsesHeaders(t *testing.T) {
	var resumeHeader string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/runs/acp_run_1/resume":
			resumeHeader = r.Header.Get("X-Agent-Instance-ID")
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"id":"acp_run_1","session_id":"acp_session_1","status":"running"}`)
		case r.Method == http.MethodGet && r.URL.Path == "/runs/acp_run_1/events":
			if r.Header.Get("X-Run-ID") != "acp_run_1" {
				t.Fatalf("expected run header on stream, got %q", r.Header.Get("X-Run-ID"))
			}
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = io.WriteString(w, `data: {"id":"acp_run_1","session_id":"acp_session_1","status":"completed","output":"resumed"}`+"\n\n")
		default:
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
	}))
	defer server.Close()

	client := NewSSEClient(server.URL, "")
	client.HTTP = server.Client()
	stream, err := client.ResumeRunForSession(context.Background(),
		domain.Session{ID: "session_1", TenantID: "tenant_default", ChannelType: "webchat", ChannelScopeKey: "surface_1", OwnerUserID: "user_1", AgentProfileID: "support"},
		domain.Await{RunID: "run_acp_run_1", SessionID: "session_1"},
		[]byte(`{"choice":"yes"}`),
	)
	if err != nil {
		t.Fatal(err)
	}
	if resumeHeader != "support" {
		t.Fatalf("expected scoped resume header, got %q", resumeHeader)
	}
	events := collectRunEvents(t, stream)
	if len(events) != 1 || events[0].Text != "resumed" || events[0].RunID != "run_acp_run_1" {
		t.Fatalf("unexpected resume events: %+v", events)
	}
}

func TestSSEStreamsCRLFDelimitedEvents(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/runs":
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"id":"run_crlf","session_id":"acp_session_1","status":"running"}`)
		case "/runs/run_crlf/events":
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = io.WriteString(w, "data: {\"id\":\"run_crlf\",\"session_id\":\"acp_session_1\",\"status\":\"completed\",\"output\":\"done\"}\r\n\r\n")
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer server.Close()

	client := NewSSEClient(server.URL, "")
	client.HTTP = server.Client()
	client.StreamHTTP = server.Client()
	_, stream, err := client.StartRun(context.Background(), domain.StartRunRequest{
		Session: domain.Session{ID: "session_1", ACPSessionID: "acp_session_1"},
	})
	if err != nil {
		t.Fatal(err)
	}
	events := collectRunEvents(t, stream)
	if len(events) != 1 || events[0].Text != "done" || events[0].Status != "completed" {
		t.Fatalf("unexpected CRLF SSE events: %+v", events)
	}
}

func TestSSEMapsStrictAwaitPrompt(t *testing.T) {
	prompt := base64.StdEncoding.EncodeToString([]byte(`{"body":"Continue?"}`))
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/runs":
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"id":"run_await","session_id":"acp_session_1","status":"running"}`)
		case "/runs/run_await/events":
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = io.WriteString(w, `data: {"id":"run_await","session_id":"acp_session_1","status":"awaiting","await":{"prompt":"`+prompt+`"}}`+"\n\n")
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer server.Close()

	client := NewSSEClient(server.URL, "")
	client.HTTP = server.Client()
	_, stream, err := client.StartRun(context.Background(), domain.StartRunRequest{
		Session: domain.Session{ID: "session_1", ACPSessionID: "acp_session_1"},
	})
	if err != nil {
		t.Fatal(err)
	}
	events := collectRunEvents(t, stream)
	if len(events) != 1 || events[0].Status != "awaiting" || string(events[0].AwaitPrompt) != `{"body":"Continue?"}` {
		t.Fatalf("unexpected await SSE events: %+v", events)
	}
}

func TestSSEMalformedEventReportsError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/runs":
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"id":"run_bad","session_id":"acp_session_1","status":"running"}`)
		case "/runs/run_bad/events":
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = io.WriteString(w, "data: {bad json}\n\n")
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer server.Close()

	client := NewSSEClient(server.URL, "")
	client.HTTP = server.Client()
	_, stream, err := client.StartRun(context.Background(), domain.StartRunRequest{
		Session: domain.Session{ID: "session_1", ACPSessionID: "acp_session_1"},
	})
	if err != nil {
		t.Fatal(err)
	}
	for range stream.Events {
	}
	if err, ok := <-stream.Err; !ok || err == nil || !strings.Contains(err.Error(), "decode acp sse event") {
		t.Fatalf("expected malformed SSE error, got %v ok=%v", err, ok)
	}
}

func TestFactoryReturnsSSEClientAndUnknownImplementationFallsBack(t *testing.T) {
	sseBridge := NewBridge(BridgeConfig{Implementation: "sse", BaseURL: "http://example.invalid"})
	if _, ok := sseBridge.(SSEClient); !ok {
		t.Fatalf("expected SSE client, got %T", sseBridge)
	}
	unknownBridge := NewBridge(BridgeConfig{Implementation: "unknown-special", BaseURL: "http://example.invalid"})
	if _, ok := unknownBridge.(SSEClient); ok {
		t.Fatalf("did not expect unknown implementation to alias SSE")
	}
}
