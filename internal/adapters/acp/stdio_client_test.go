package acp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"nexus/internal/domain"
)

type stdioHelperSession struct {
	ID       string
	Messages []stdioHelperMessage
}

type stdioHelperMessage struct {
	UserText      string
	AssistantID   string
	AssistantText string
}

func TestStdioClientRoundTrip(t *testing.T) {
	client := newHelperBackedStdioClient(t)
	ctx := context.Background()

	agents, err := client.DiscoverAgents(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(agents) != 1 || agents[0].Name != "build" || agents[0].Protocol != "acp" || !agents[0].SupportsAwaitResume {
		t.Fatalf("unexpected stdio manifests: %+v", agents)
	}

	sessionID, err := client.EnsureSession(ctx, domain.Session{ID: "session_1"})
	if err != nil {
		t.Fatal(err)
	}
	if sessionID == "" {
		t.Fatal("expected session id from stdio EnsureSession")
	}

	run, events, err := client.StartRun(ctx, domain.StartRunRequest{
		Session:       domain.Session{ID: "session_1", ACPSessionID: sessionID},
		RouteDecision: domain.RouteDecision{ACPAgentName: "build"},
		Message:       domain.Message{Text: "hello stdio"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if run.ACPRunID == "" || run.Status != "completed" {
		t.Fatalf("unexpected stdio run: %+v", run)
	}
	if len(events) != 1 || !strings.Contains(events[0].Text, "hello stdio") {
		t.Fatalf("unexpected stdio events: %+v", events)
	}

	snapshot, err := client.GetRun(ctx, run.ACPRunID)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.ACPRunID != run.ACPRunID || snapshot.Status != "completed" {
		t.Fatalf("unexpected stdio snapshot: %+v", snapshot)
	}
	if !strings.Contains(snapshot.Output, "hello stdio") {
		t.Fatalf("expected snapshot output from replay, got %+v", snapshot)
	}

	latest, found, err := client.FindLatestRunForSession(ctx, domain.Session{ID: "session_1", ACPSessionID: sessionID})
	if err != nil {
		t.Fatal(err)
	}
	if !found || latest.ACPRunID != run.ACPRunID {
		t.Fatalf("unexpected latest stdio snapshot: %+v found=%v", latest, found)
	}

	if err := client.CancelRun(ctx, run); err != nil {
		t.Fatal(err)
	}
}

func TestStdioProcessPermissionHandler(t *testing.T) {
	proc := &stdioProcess{}
	result, err := proc.handlePermission([]byte(`{"options":[{"id":"deny"},{"id":"allow_once"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	m, _ := result.(map[string]any)
	if m["optionId"] != "allow_once" {
		t.Fatalf("expected allow_once selection, got %+v", result)
	}
}

func TestStdioProcessFilesystemHandlers(t *testing.T) {
	root := t.TempDir()
	proc := &stdioProcess{root: root}
	path := filepath.Join(root, "nested", "file.txt")
	if _, err := proc.handleWriteTextFile(mustJSON(t, map[string]any{
		"path":    path,
		"content": "callback-content",
	})); err != nil {
		t.Fatal(err)
	}
	result, err := proc.handleReadTextFile(mustJSON(t, map[string]any{
		"path": path,
	}))
	if err != nil {
		t.Fatal(err)
	}
	m, _ := result.(map[string]any)
	if m["content"] != "callback-content" {
		t.Fatalf("unexpected read result: %+v", result)
	}
}

func TestStdioProcessTerminalHandlers(t *testing.T) {
	root := t.TempDir()
	proc := &stdioProcess{root: root, terms: map[string]*managedTerminal{}}
	created, err := proc.handleTerminalCreate(mustJSON(t, map[string]any{
		"command":         "sh",
		"args":            []string{"-lc", "printf callback-ok"},
		"cwd":             root,
		"outputByteLimit": 1024,
	}))
	if err != nil {
		t.Fatal(err)
	}
	createMap, _ := created.(map[string]any)
	terminalID, _ := createMap["terminalId"].(string)
	if terminalID == "" {
		t.Fatalf("expected terminal id, got %+v", created)
	}
	waitResult, err := proc.handleTerminalWait(mustJSON(t, map[string]any{
		"terminalId": terminalID,
	}))
	if err != nil {
		t.Fatal(err)
	}
	waitMap, _ := waitResult.(map[string]any)
	if waitMap["exitCode"] != 0 {
		t.Fatalf("unexpected wait result: %+v", waitResult)
	}
	outputResult, err := proc.handleTerminalOutput(mustJSON(t, map[string]any{
		"terminalId": terminalID,
	}))
	if err != nil {
		t.Fatal(err)
	}
	outputMap, _ := outputResult.(map[string]any)
	if strings.TrimSpace(outputMap["output"].(string)) != "callback-ok" {
		t.Fatalf("unexpected terminal output: %+v", outputResult)
	}
	if _, err := proc.handleTerminalRelease(mustJSON(t, map[string]any{
		"terminalId": terminalID,
	})); err != nil {
		t.Fatal(err)
	}
}

func TestBridgeFactorySelectsStdio(t *testing.T) {
	bridge := NewBridge(BridgeConfig{
		Implementation:   "stdio",
		Command:          "helper",
		Args:             []string{"acp"},
		DefaultAgentName: "build",
	})
	if _, ok := bridge.(*StdioClient); !ok {
		t.Fatalf("expected stdio client, got %T", bridge)
	}
}

func newHelperBackedStdioClient(t *testing.T) *StdioClient {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=TestStdioACPHelperProcess", "--")
	cmd.Env = append(os.Environ(), "NEXUS_STDIO_HELPER=1")
	client := NewStdioClient(StdioConfig{
		Command:          cmd.Path,
		Args:             cmd.Args[1:],
		Env:              []string{"NEXUS_STDIO_HELPER=1"},
		Workdir:          t.TempDir(),
		DefaultAgentName: "build",
		StartupTimeout:   5 * time.Second,
		RPCTimeout:       20 * time.Second,
	})
	return client
}

func TestStdioACPHelperProcess(t *testing.T) {
	if os.Getenv("NEXUS_STDIO_HELPER") != "1" {
		return
	}

	sessions := map[string]*stdioHelperSession{}
	nextSession := 1
	nextMessage := 1
	scanner := bufio.NewScanner(os.Stdin)
	scanner.Buffer(make([]byte, 0, 64*1024), 10*1024*1024)
	enc := json.NewEncoder(os.Stdout)

	for scanner.Scan() {
		var req map[string]any
		if err := json.Unmarshal(scanner.Bytes(), &req); err != nil {
			continue
		}
		id := req["id"]
		method, _ := req["method"].(string)
		params, _ := req["params"].(map[string]any)

		switch method {
		case "initialize":
			_ = enc.Encode(map[string]any{
				"jsonrpc": "2.0",
				"id":      id,
				"result": map[string]any{
					"protocolVersion": 1,
					"agentCapabilities": map[string]any{
						"loadSession": true,
						"sessionCapabilities": map[string]any{
							"list":   map[string]any{},
							"resume": map[string]any{},
						},
					},
					"agentInfo": map[string]any{
						"name":    "Helper ACP",
						"version": "test",
					},
				},
			})
		case "session/new":
			sessionID := fmt.Sprintf("ses_helper_%d", nextSession)
			nextSession++
			sessions[sessionID] = &stdioHelperSession{ID: sessionID}
			_ = enc.Encode(map[string]any{
				"jsonrpc": "2.0",
				"id":      id,
				"result": map[string]any{
					"sessionId": sessionID,
					"modes": map[string]any{
						"availableModes": []map[string]any{{"id": "build", "name": "build"}},
						"currentModeId":  "build",
					},
				},
			})
			_ = enc.Encode(map[string]any{
				"jsonrpc": "2.0",
				"method":  "session/update",
				"params": map[string]any{
					"sessionId": sessionID,
					"update": map[string]any{
						"sessionUpdate": "available_commands_update",
					},
				},
			})
		case "session/prompt":
			sessionID, _ := params["sessionId"].(string)
			prompt, _ := params["prompt"].([]any)
			userText := ""
			if len(prompt) > 0 {
				if first, ok := prompt[0].(map[string]any); ok {
					userText, _ = first["text"].(string)
				}
			}
			assistantID := fmt.Sprintf("msg_helper_%d", nextMessage)
			nextMessage++
			assistantText := "echo: " + userText
			session := sessions[sessionID]
			session.Messages = append(session.Messages, stdioHelperMessage{
				UserText:      userText,
				AssistantID:   assistantID,
				AssistantText: assistantText,
			})
			_ = enc.Encode(map[string]any{
				"jsonrpc": "2.0",
				"method":  "session/update",
				"params": map[string]any{
					"sessionId": sessionID,
					"update": map[string]any{
						"sessionUpdate": "user_message_chunk",
						"messageId":     "user_" + assistantID,
						"content":       map[string]any{"type": "text", "text": userText},
					},
				},
			})
			_ = enc.Encode(map[string]any{
				"jsonrpc": "2.0",
				"method":  "session/update",
				"params": map[string]any{
					"sessionId": sessionID,
					"update": map[string]any{
						"sessionUpdate": "agent_message_chunk",
						"messageId":     assistantID,
						"content":       map[string]any{"type": "text", "text": assistantText},
					},
				},
			})
			_ = enc.Encode(map[string]any{
				"jsonrpc": "2.0",
				"id":      id,
				"result": map[string]any{
					"stopReason": "end_turn",
				},
			})
		case "session/load":
			sessionID, _ := params["sessionId"].(string)
			session := sessions[sessionID]
			for _, message := range session.Messages {
				_ = enc.Encode(map[string]any{
					"jsonrpc": "2.0",
					"method":  "session/update",
					"params": map[string]any{
						"sessionId": sessionID,
						"update": map[string]any{
							"sessionUpdate": "user_message_chunk",
							"messageId":     "user_" + message.AssistantID,
							"content":       map[string]any{"type": "text", "text": message.UserText},
						},
					},
				})
				_ = enc.Encode(map[string]any{
					"jsonrpc": "2.0",
					"method":  "session/update",
					"params": map[string]any{
						"sessionId": sessionID,
						"update": map[string]any{
							"sessionUpdate": "agent_message_chunk",
							"messageId":     message.AssistantID,
							"content":       map[string]any{"type": "text", "text": message.AssistantText},
						},
					},
				})
			}
			_ = enc.Encode(map[string]any{
				"jsonrpc": "2.0",
				"id":      id,
				"result":  map[string]any{"sessionId": sessionID},
			})
		case "session/list":
			items := make([]map[string]any, 0, len(sessions))
			for _, session := range sessions {
				items = append(items, map[string]any{
					"sessionId": session.ID,
					"cwd":       ".",
					"title":     session.ID,
					"updatedAt": time.Now().UTC().Format(time.RFC3339),
				})
			}
			_ = enc.Encode(map[string]any{
				"jsonrpc": "2.0",
				"id":      id,
				"result":  map[string]any{"sessions": items},
			})
		case "session/cancel":
			_ = enc.Encode(map[string]any{
				"jsonrpc": "2.0",
				"id":      id,
				"result":  map[string]any{},
			})
		default:
			_ = enc.Encode(map[string]any{
				"jsonrpc": "2.0",
				"id":      id,
				"error": map[string]any{
					"code":    -32601,
					"message": "method not found",
				},
			})
		}
	}
	os.Exit(0)
}

func helperRPC(scanner *bufio.Scanner, enc *json.Encoder, method string, params any) (any, error) {
	reqID := time.Now().UnixNano()
	if err := enc.Encode(map[string]any{
		"jsonrpc": "2.0",
		"id":      reqID,
		"method":  method,
		"params":  params,
	}); err != nil {
		return nil, err
	}
	for scanner.Scan() {
		var resp map[string]any
		if err := json.Unmarshal(scanner.Bytes(), &resp); err != nil {
			return nil, err
		}
		id, ok := resp["id"].(float64)
		if !ok || int64(id) != reqID {
			continue
		}
		if rawErr, ok := resp["error"]; ok && rawErr != nil {
			return nil, fmt.Errorf("callback rpc error: %+v", rawErr)
		}
		return resp["result"], nil
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return nil, fmt.Errorf("callback rpc ended without response")
}

func mustJSON(t *testing.T, value any) []byte {
	t.Helper()
	out, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return out
}
