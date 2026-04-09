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
	"sync"
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

type stdioHelperStore struct {
	Sessions    map[string]*stdioHelperSession `json:"sessions"`
	NextSession int                            `json:"next_session"`
	NextMessage int                            `json:"next_message"`
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

	run, stream, err := client.StartRun(ctx, domain.StartRunRequest{
		Session:       domain.Session{ID: "session_1", ACPSessionID: sessionID},
		RouteDecision: domain.RouteDecision{ACPAgentName: "build"},
		Message:       domain.Message{Text: "hello stdio"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if run.ACPRunID == "" || (run.Status != "running" && run.Status != "completed") {
		t.Fatalf("unexpected stdio run: %+v", run)
	}
	events := collectRunEvents(t, stream)
	if len(events) == 0 || !strings.Contains(events[len(events)-1].Text, "hello stdio") {
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

func TestStdioClientManifestRequiresLoadSessionForRecovery(t *testing.T) {
	client := NewStdioClient(StdioConfig{DefaultAgentName: "build"})
	init := &stdioInitializeResult{}
	init.AgentInfo.Name = "Helper ACP"
	init.AgentCapabilities.SessionCapabilities.Resume = map[string]any{}

	manifest := client.manifestFromInitialize(init)
	if manifest.SupportsSessionReload {
		t.Fatalf("expected stdio manifest without session reload support, got %+v", manifest)
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
	return newHelperBackedStdioClientWithWorkdir(t, t.TempDir())
}

func newHelperBackedStdioClientWithWorkdir(t *testing.T, workdir string) *StdioClient {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=TestStdioACPHelperProcess", "--")
	cmd.Env = append(os.Environ(), "NEXUS_STDIO_HELPER=1")
	client := NewStdioClient(StdioConfig{
		Command:          cmd.Path,
		Args:             cmd.Args[1:],
		Env:              []string{"NEXUS_STDIO_HELPER=1"},
		Workdir:          workdir,
		DefaultAgentName: "build",
		StartupTimeout:   5 * time.Second,
		RPCTimeout:       20 * time.Second,
	})
	return client
}

func TestStdioClientReplayAfterProcessRestart(t *testing.T) {
	workdir := t.TempDir()
	client := newHelperBackedStdioClientWithWorkdir(t, workdir)
	ctx := context.Background()

	sessionID, err := client.EnsureSession(ctx, domain.Session{ID: "session_restart"})
	if err != nil {
		t.Fatal(err)
	}
	run, _, err := client.StartRun(ctx, domain.StartRunRequest{
		Session:       domain.Session{ID: "session_restart", ACPSessionID: sessionID},
		RouteDecision: domain.RouteDecision{ACPAgentName: "build"},
		Message:       domain.Message{Text: "restart replay"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := client.Close(); err != nil {
		t.Fatal(err)
	}

	restarted := newHelperBackedStdioClientWithWorkdir(t, workdir)
	snapshot, err := restarted.GetRun(ctx, run.ACPRunID)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.ACPRunID != run.ACPRunID || snapshot.Status != "completed" || !strings.Contains(snapshot.Output, "restart replay") {
		t.Fatalf("unexpected replay snapshot after restart: %+v", snapshot)
	}
	latest, found, err := restarted.FindLatestRunForSession(ctx, domain.Session{ID: "session_restart", ACPSessionID: sessionID})
	if err != nil {
		t.Fatal(err)
	}
	if !found || latest.ACPRunID != run.ACPRunID {
		t.Fatalf("unexpected latest replay snapshot after restart: %+v found=%v", latest, found)
	}
}

func TestBuildStdioPromptPartsMapsArtifactsToACPContentBlocks(t *testing.T) {
	parts := buildStdioPromptParts(domain.Message{
		Text: "hello",
		Artifacts: []domain.Artifact{
			{
				ID:         "artifact_file",
				Name:       "report.txt",
				MIMEType:   "text/plain",
				StorageURI: "file:///tmp/report.txt",
			},
			{
				ID:         "artifact_image",
				Name:       "image.png",
				MIMEType:   "image/png",
				StorageURI: "data:image/png;base64,aGVsbG8=",
			},
		},
	})
	if len(parts) != 3 {
		t.Fatalf("expected 3 prompt parts, got %+v", parts)
	}
	if parts[1]["type"] != "resource_link" || parts[1]["uri"] != "file:///tmp/report.txt" || parts[1]["mimeType"] != "text/plain" {
		t.Fatalf("unexpected file artifact prompt block: %+v", parts[1])
	}
	if parts[2]["type"] != "image" || parts[2]["mimeType"] != "image/png" || parts[2]["data"] != "aGVsbG8=" {
		t.Fatalf("unexpected image artifact prompt block: %+v", parts[2])
	}
}

func TestCollectReplayCapturesResourceLinkArtifacts(t *testing.T) {
	ch := make(chan stdioSessionUpdate, 3)
	ch <- stdioSessionUpdate{
		SessionUpdate: "agent_message_chunk",
		MessageID:     "msg_1",
		Content: &struct {
			Type     string `json:"type"`
			Text     string `json:"text,omitempty"`
			Data     string `json:"data,omitempty"`
			MIMEType string `json:"mimeType,omitempty"`
			URI      string `json:"uri,omitempty"`
			Name     string `json:"name,omitempty"`
		}{
			Type: "text",
			Text: "artifact summary",
		},
	}
	ch <- stdioSessionUpdate{
		SessionUpdate: "agent_message_chunk",
		MessageID:     "msg_1",
		Content: &struct {
			Type     string `json:"type"`
			Text     string `json:"text,omitempty"`
			Data     string `json:"data,omitempty"`
			MIMEType string `json:"mimeType,omitempty"`
			URI      string `json:"uri,omitempty"`
			Name     string `json:"name,omitempty"`
		}{
			Type:     "resource_link",
			URI:      "file:///tmp/report.txt",
			Name:     "report.txt",
			MIMEType: "text/plain",
		},
	}
	close(ch)

	replay := collectReplay(ch)
	status, output, artifacts := replay.snapshot("msg_1", "end_turn")
	if status != "completed" || output != "artifact summary" {
		t.Fatalf("unexpected replay snapshot status=%q output=%q", status, output)
	}
	if len(artifacts) != 1 || artifacts[0].StorageURI != "file:///tmp/report.txt" || artifacts[0].Name != "report.txt" {
		t.Fatalf("unexpected replay artifacts: %+v", artifacts)
	}
}

func TestCollectReplayCapturesInlineMediaArtifacts(t *testing.T) {
	ch := make(chan stdioSessionUpdate, 1)
	ch <- stdioSessionUpdate{
		SessionUpdate: "agent_message_chunk",
		MessageID:     "msg_2",
		Content: &struct {
			Type     string `json:"type"`
			Text     string `json:"text,omitempty"`
			Data     string `json:"data,omitempty"`
			MIMEType string `json:"mimeType,omitempty"`
			URI      string `json:"uri,omitempty"`
			Name     string `json:"name,omitempty"`
		}{
			Type:     "image",
			Data:     "aGVsbG8=",
			MIMEType: "image/png",
		},
	}
	close(ch)

	replay := collectReplay(ch)
	if !replay.has("msg_2") {
		t.Fatal("expected replay to retain artifact-only message")
	}
	_, _, artifacts := replay.snapshot("msg_2", "end_turn")
	if len(artifacts) != 1 || !strings.HasPrefix(artifacts[0].StorageURI, "data:image/png;base64,") {
		t.Fatalf("unexpected inline artifact snapshot: %+v", artifacts)
	}
}

func TestStdioClientFindRunByIdempotencyKeyAfterRestart(t *testing.T) {
	workdir := t.TempDir()
	client := newHelperBackedStdioClientWithWorkdir(t, workdir)
	ctx := context.Background()

	sessionID, err := client.EnsureSession(ctx, domain.Session{ID: "session_restart"})
	if err != nil {
		t.Fatal(err)
	}
	firstRun, _, err := client.StartRun(ctx, domain.StartRunRequest{
		Session:        domain.Session{ID: "session_restart", ACPSessionID: sessionID},
		RouteDecision:  domain.RouteDecision{ACPAgentName: "build"},
		Message:        domain.Message{Text: "first replay"},
		IdempotencyKey: "queue_1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := client.StartRun(ctx, domain.StartRunRequest{
		Session:        domain.Session{ID: "session_restart", ACPSessionID: sessionID},
		RouteDecision:  domain.RouteDecision{ACPAgentName: "build"},
		Message:        domain.Message{Text: "second replay"},
		IdempotencyKey: "queue_2",
	}); err != nil {
		t.Fatal(err)
	}
	if err := client.Close(); err != nil {
		t.Fatal(err)
	}

	restarted := newHelperBackedStdioClientWithWorkdir(t, workdir)
	snapshot, found, err := restarted.FindRunByIdempotencyKey(ctx, domain.Session{ID: "session_restart", ACPSessionID: sessionID}, "queue_1")
	if err != nil {
		t.Fatal(err)
	}
	if !found {
		t.Fatal("expected run snapshot for idempotency key")
	}
	if snapshot.ACPRunID != firstRun.ACPRunID || !strings.Contains(snapshot.Output, "first replay") {
		t.Fatalf("unexpected idempotency snapshot after restart: %+v", snapshot)
	}
}

func TestStdioClientStoreIdempotencyRunIDConcurrent(t *testing.T) {
	workdir := t.TempDir()
	client := NewStdioClient(StdioConfig{Workdir: workdir, DefaultAgentName: "build"})

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		if err := client.storeIdempotencyRunID("queue_1", "ses_1:msg_1"); err != nil {
			t.Errorf("store queue_1: %v", err)
		}
	}()
	go func() {
		defer wg.Done()
		if err := client.storeIdempotencyRunID("queue_2", "ses_1:msg_2"); err != nil {
			t.Errorf("store queue_2: %v", err)
		}
	}()
	wg.Wait()

	got1, ok1, err := client.loadIdempotencyRunID("queue_1")
	if err != nil {
		t.Fatal(err)
	}
	got2, ok2, err := client.loadIdempotencyRunID("queue_2")
	if err != nil {
		t.Fatal(err)
	}
	if !ok1 || got1 != "ses_1:msg_1" || !ok2 || got2 != "ses_1:msg_2" {
		t.Fatalf("expected both mappings to persist, got queue_1=%q ok=%v queue_2=%q ok=%v", got1, ok1, got2, ok2)
	}
}

func TestStdioACPHelperProcess(t *testing.T) {
	if os.Getenv("NEXUS_STDIO_HELPER") != "1" {
		return
	}

	storePath := filepath.Join(".", ".nexus-stdio-helper-store.json")
	store := stdioHelperStore{
		Sessions:    map[string]*stdioHelperSession{},
		NextSession: 1,
		NextMessage: 1,
	}
	if data, err := os.ReadFile(storePath); err == nil {
		_ = json.Unmarshal(data, &store)
		if store.Sessions == nil {
			store.Sessions = map[string]*stdioHelperSession{}
		}
		if store.NextSession == 0 {
			store.NextSession = 1
		}
		if store.NextMessage == 0 {
			store.NextMessage = 1
		}
	}
	scanner := bufio.NewScanner(os.Stdin)
	scanner.Buffer(make([]byte, 0, 64*1024), 10*1024*1024)
	enc := json.NewEncoder(os.Stdout)
	persist := func() {
		data, err := json.Marshal(store)
		if err == nil {
			_ = os.WriteFile(storePath, data, 0o644)
		}
	}

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
			sessionID := fmt.Sprintf("ses_helper_%d", store.NextSession)
			store.NextSession++
			store.Sessions[sessionID] = &stdioHelperSession{ID: sessionID}
			persist()
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
			assistantID := fmt.Sprintf("msg_helper_%d", store.NextMessage)
			store.NextMessage++
			assistantText := "echo: " + userText
			session := store.Sessions[sessionID]
			if session == nil {
				session = &stdioHelperSession{ID: sessionID}
				store.Sessions[sessionID] = session
			}
			session.Messages = append(session.Messages, stdioHelperMessage{
				UserText:      userText,
				AssistantID:   assistantID,
				AssistantText: assistantText,
			})
			persist()
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
			session := store.Sessions[sessionID]
			if session == nil {
				session = &stdioHelperSession{ID: sessionID}
			}
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
			items := make([]map[string]any, 0, len(store.Sessions))
			for _, session := range store.Sessions {
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
