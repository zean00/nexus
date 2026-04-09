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

func TestParmesanDiscoverAgents(t *testing.T) {
	var auth string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth = r.Header.Get("Authorization")
		if r.URL.Path != "/v1/operator/agents" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `[{"id":"agent_support","name":"Support","description":"Support agent","status":"active"}]`)
	}))
	defer server.Close()

	client := NewParmesanClient(server.URL, "secret", 0)
	client.HTTP = server.Client()

	agents, err := client.DiscoverAgents(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if auth != "Bearer secret" {
		t.Fatalf("auth = %q, want bearer token", auth)
	}
	if len(agents) != 1 || agents[0].Name != "agent_support" || !agents[0].SupportsAwaitResume || !agents[0].SupportsStructuredAwait || !agents[0].SupportsSessionReload {
		t.Fatalf("unexpected manifests: %+v", agents)
	}
}

func TestParmesanStartRunCompletesFromACPEvents(t *testing.T) {
	var createdSession bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/acp/sessions/session_1":
			http.Error(w, "not found", http.StatusNotFound)
		case r.Method == http.MethodPost && r.URL.Path == "/v1/acp/sessions":
			createdSession = true
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"id":"session_1","channel":"slack"}`)
		case r.Method == http.MethodPost && r.URL.Path == "/v1/acp/sessions/session_1/messages":
			var payload map[string]any
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				t.Fatal(err)
			}
			if payload["text"] != "hello there" {
				t.Fatalf("unexpected message payload: %+v", payload)
			}
			meta, _ := payload["metadata"].(map[string]any)
			if meta["idempotency_key"] != "queue_1" {
				t.Fatalf("unexpected metadata: %+v", meta)
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"id":"evt_in_1","session_id":"session_1","kind":"message","source":"customer","offset":1,"execution_id":"exec_1"}`)
		case r.Method == http.MethodGet && r.URL.Path == "/v1/executions/exec_1":
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"execution":{"id":"exec_1","session_id":"session_1","status":"succeeded"}}`)
		case r.Method == http.MethodGet && r.URL.Path == "/v1/acp/sessions/session_1/events":
			if got := r.URL.Query().Get("min_offset"); got != "2" {
				t.Fatalf("min_offset = %q, want 2", got)
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `[
				{"id":"evt_out_1","session_id":"session_1","kind":"message","source":"ai_agent","offset":2,"execution_id":"exec_1","content":[{"type":"text","text":"First reply"}]},
				{"id":"evt_out_2","session_id":"session_1","kind":"message","source":"ai_agent","offset":3,"execution_id":"exec_1","content":[{"type":"text","text":"Second reply"}]},
				{"id":"evt_status_1","session_id":"session_1","kind":"status","source":"runtime","offset":4,"execution_id":"exec_1","data":{"code":"response.delivered","state":"queued","event_ids":["evt_out_1","evt_out_2"]}}
			]`)
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.String())
		}
	}))
	defer server.Close()

	client := NewParmesanClient(server.URL, "", 0)
	client.HTTP = server.Client()
	client.PollEvery = 1

	run, events, err := client.StartRun(context.Background(), domain.StartRunRequest{
		Session:        domain.Session{ID: "session_1", ChannelType: "slack", AgentProfileID: "agent_support"},
		RouteDecision:  domain.RouteDecision{ACPAgentName: "agent_support"},
		Message:        domain.Message{Text: "hello there"},
		IdempotencyKey: "queue_1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !createdSession {
		t.Fatal("expected ACP session creation")
	}
	if run.ACPRunID != "exec_1" || run.Status != "completed" {
		t.Fatalf("unexpected run: %+v", run)
	}
	if len(events) != 1 || events[0].Status != "completed" || events[0].Text != "First reply\n\nSecond reply" {
		t.Fatalf("unexpected events: %+v", events)
	}
}

func TestParmesanGetRunMapsPendingApprovalToAwaiting(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/executions/exec_await_1":
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"execution":{"id":"exec_await_1","session_id":"session_1","status":"blocked","blocked_reason":"approval_required"}}`)
		case r.Method == http.MethodGet && r.URL.Path == "/v1/acp/sessions/session_1/events":
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `[]`)
		case r.Method == http.MethodGet && r.URL.Path == "/v1/acp/sessions/session_1/approvals":
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `[{"id":"appr_1","session_id":"session_1","execution_id":"exec_await_1","tool_id":"tool_ship","status":"pending","request_text":"Approve shipment check?"}]`)
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.String())
		}
	}))
	defer server.Close()

	client := NewParmesanClient(server.URL, "", 0)
	client.HTTP = server.Client()

	snapshot, err := client.GetRun(context.Background(), "exec_await_1")
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Status != "awaiting" || snapshot.Await == nil {
		t.Fatalf("unexpected snapshot: %+v", snapshot)
	}
	if !strings.Contains(string(snapshot.Await.Prompt), `"approval_id":"appr_1"`) {
		t.Fatalf("await prompt = %s, want approval_id", string(snapshot.Await.Prompt))
	}
}

func TestParmesanResumeRunRespondsApprovalAndWaitsForCompletion(t *testing.T) {
	var postedDecision string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/acp/sessions/session_1/approvals/appr_1":
			var payload map[string]any
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				t.Fatal(err)
			}
			postedDecision, _ = payload["decision"].(string)
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"id":"appr_1","status":"approved"}`)
		case r.Method == http.MethodGet && r.URL.Path == "/v1/executions/exec_1":
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"execution":{"id":"exec_1","session_id":"session_1","status":"succeeded"}}`)
		case r.Method == http.MethodGet && r.URL.Path == "/v1/acp/sessions/session_1/events":
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `[
				{"id":"evt_after_1","session_id":"session_1","kind":"message","source":"ai_agent","offset":5,"execution_id":"exec_1","content":[{"type":"text","text":"Approved, continuing now."}]},
				{"id":"evt_after_2","session_id":"session_1","kind":"status","source":"runtime","offset":6,"execution_id":"exec_1","data":{"code":"response.delivered","state":"queued"}}
			]`)
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.String())
		}
	}))
	defer server.Close()

	client := NewParmesanClient(server.URL, "", 0)
	client.HTTP = server.Client()
	client.PollEvery = 1

	events, err := client.ResumeRun(context.Background(), domain.Await{
		RunID:            "run_exec_1",
		SessionID:        "session_1",
		PromptRenderJSON: []byte(`{"approval_id":"appr_1","choices":[{"id":"approve","label":"Approve"},{"id":"reject","label":"Reject"}]}`),
	}, []byte(`{"choice":"approve"}`))
	if err != nil {
		t.Fatal(err)
	}
	if postedDecision != "approve" {
		t.Fatalf("decision = %q, want approve", postedDecision)
	}
	if len(events) != 1 || events[0].Status != "completed" || events[0].Text != "Approved, continuing now." {
		t.Fatalf("unexpected resume events: %+v", events)
	}
}

func TestParmesanFindLatestRunForSession(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/acp/sessions/session_1":
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"id":"session_1","summary":{"last_execution_id":"exec_latest_1"}}`)
		case r.Method == http.MethodGet && r.URL.Path == "/v1/executions/exec_latest_1":
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"execution":{"id":"exec_latest_1","session_id":"session_1","status":"succeeded"}}`)
		case r.Method == http.MethodGet && r.URL.Path == "/v1/acp/sessions/session_1/events":
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `[{"id":"evt_latest_1","session_id":"session_1","kind":"message","source":"ai_agent","offset":9,"execution_id":"exec_latest_1","content":[{"type":"text","text":"latest"}]}]`)
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.String())
		}
	}))
	defer server.Close()

	client := NewParmesanClient(server.URL, "", 0)
	client.HTTP = server.Client()

	snapshot, found, err := client.FindLatestRunForSession(context.Background(), domain.Session{ID: "session_1", ACPSessionID: "session_1"})
	if err != nil {
		t.Fatal(err)
	}
	if !found || snapshot.ACPRunID != "exec_latest_1" || snapshot.Status != "completed" || snapshot.Output != "latest" {
		t.Fatalf("unexpected latest snapshot: %+v found=%v", snapshot, found)
	}
}

func TestBridgeFactorySelectsParmesan(t *testing.T) {
	bridge := NewBridge(BridgeConfig{Implementation: "parmesan", BaseURL: "http://example.invalid"})
	if _, ok := bridge.(ParmesanClient); !ok {
		t.Fatalf("expected parmesan client, got %T", bridge)
	}
}
