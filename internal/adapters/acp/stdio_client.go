package acp

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha1"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"nexus/internal/domain"
)

const (
	stdioMaxFileContentBytes = 1 << 20
	stdioMaxWriteBytes       = 1 << 20
)

type StdioConfig struct {
	Command          string
	Args             []string
	Env              []string
	Workdir          string
	DefaultAgentName string
	StartupTimeout   time.Duration
	RPCTimeout       time.Duration
}

type StdioClient struct {
	cfg StdioConfig

	mu        sync.Mutex
	stateMu   sync.Mutex
	proc      *stdioProcess
	init      *stdioInitializeResult
	closed    bool
	startErr  error
	startedAt time.Time
}

type stdioClientState struct {
	IdempotencyRunIDs map[string]string `json:"idempotency_run_ids"`
}

type StdioRuntimeStatus struct {
	Implementation string    `json:"implementation"`
	Command        string    `json:"command"`
	Args           []string  `json:"args,omitempty"`
	Workdir        string    `json:"workdir,omitempty"`
	Running        bool      `json:"running"`
	Initialized    bool      `json:"initialized"`
	StartedAt      time.Time `json:"started_at,omitempty"`
	LastError      string    `json:"last_error,omitempty"`
	AgentName      string    `json:"agent_name,omitempty"`
	AgentVersion   string    `json:"agent_version,omitempty"`
	CallbackCounts struct {
		PermissionRequests int `json:"permission_requests"`
		FSReadTextFile     int `json:"fs_read_text_file"`
		FSWriteTextFile    int `json:"fs_write_text_file"`
		TerminalCreate     int `json:"terminal_create"`
		TerminalOutput     int `json:"terminal_output"`
		TerminalWait       int `json:"terminal_wait"`
		TerminalKill       int `json:"terminal_kill"`
		TerminalRelease    int `json:"terminal_release"`
	} `json:"callback_counts"`
}

type stdioProcess struct {
	cmd   *exec.Cmd
	stdin io.WriteCloser
	root  string

	writeMu sync.Mutex
	mu      sync.Mutex
	pending map[int64]chan stdioIncoming
	subs    map[string]map[chan stdioSessionUpdate]struct{}
	terms   map[string]*managedTerminal
	nextID  int64
	readErr error

	callbackCounts stdioCallbackCounts
}

type stdioCallbackCounts struct {
	PermissionRequests int
	FSReadTextFile     int
	FSWriteTextFile    int
	TerminalCreate     int
	TerminalOutput     int
	TerminalWait       int
	TerminalKill       int
	TerminalRelease    int
}

type stdioIncoming struct {
	result json.RawMessage
	err    *stdioRPCError
}

type stdioRPCEnvelope struct {
	JSONRPC string          `json:"jsonrpc,omitempty"`
	ID      *int64          `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *stdioRPCError  `json:"error,omitempty"`
}

type stdioRPCError struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

func (e *stdioRPCError) Error() string {
	if e == nil {
		return ""
	}
	if len(bytes.TrimSpace(e.Data)) == 0 {
		return fmt.Sprintf("rpc error %d: %s", e.Code, e.Message)
	}
	return fmt.Sprintf("rpc error %d: %s (%s)", e.Code, e.Message, string(e.Data))
}

type stdioInitializeResult struct {
	ProtocolVersion   int `json:"protocolVersion"`
	AgentCapabilities struct {
		LoadSession         bool `json:"loadSession"`
		SessionCapabilities struct {
			Fork   map[string]any `json:"fork"`
			List   map[string]any `json:"list"`
			Resume map[string]any `json:"resume"`
		} `json:"sessionCapabilities"`
	} `json:"agentCapabilities"`
	AgentInfo struct {
		Name    string `json:"name"`
		Version string `json:"version"`
	} `json:"agentInfo"`
}

type stdioSessionNewResponse struct {
	SessionID string `json:"sessionId"`
	Modes     struct {
		AvailableModes []struct {
			ID          string `json:"id"`
			Name        string `json:"name"`
			Description string `json:"description"`
		} `json:"availableModes"`
		CurrentModeID string `json:"currentModeId"`
	} `json:"modes"`
}

type stdioSessionListResponse struct {
	Sessions []struct {
		SessionID string `json:"sessionId"`
		Cwd       string `json:"cwd"`
		Title     string `json:"title"`
		UpdatedAt string `json:"updatedAt"`
	} `json:"sessions"`
}

type stdioPromptResponse struct {
	StopReason string `json:"stopReason"`
}

type stdioSessionUpdateEnvelope struct {
	SessionID string             `json:"sessionId"`
	Update    stdioSessionUpdate `json:"update"`
}

type stdioSessionUpdate struct {
	SessionUpdate string `json:"sessionUpdate"`
	MessageID     string `json:"messageId"`
	Content       *struct {
		Type     string `json:"type"`
		Text     string `json:"text,omitempty"`
		Data     string `json:"data,omitempty"`
		MIMEType string `json:"mimeType,omitempty"`
		URI      string `json:"uri,omitempty"`
		Name     string `json:"name,omitempty"`
	} `json:"content,omitempty"`
}

type stdioReplay struct {
	outputs   map[string]*strings.Builder
	artifacts map[string][]domain.Artifact
	lastSeen  string
}

type managedTerminal struct {
	cmd         *exec.Cmd
	output      []byte
	outputLimit int
	truncated   bool
	done        chan struct{}
	exitCode    *int
	mu          sync.Mutex
}

func NewStdioClient(cfg StdioConfig) *StdioClient {
	if strings.TrimSpace(cfg.Command) == "" {
		cfg.Command = "opencode"
	}
	if len(cfg.Args) == 0 {
		cfg.Args = []string{"acp"}
	}
	if cfg.StartupTimeout <= 0 {
		cfg.StartupTimeout = 15 * time.Second
	}
	if cfg.RPCTimeout <= 0 {
		cfg.RPCTimeout = 2 * time.Minute
	}
	if strings.TrimSpace(cfg.DefaultAgentName) == "" {
		cfg.DefaultAgentName = "default-agent"
	}
	return &StdioClient{cfg: cfg}
}

func (c *StdioClient) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.closed = true
	if c.proc == nil || c.proc.cmd == nil || c.proc.cmd.Process == nil {
		return nil
	}
	err := c.proc.cmd.Process.Kill()
	c.proc = nil
	c.init = nil
	return err
}

func (c *StdioClient) RuntimeStatus() StdioRuntimeStatus {
	c.mu.Lock()
	defer c.mu.Unlock()

	status := StdioRuntimeStatus{
		Implementation: "stdio",
		Command:        c.cfg.Command,
		Args:           append([]string(nil), c.cfg.Args...),
		Workdir:        c.cfg.Workdir,
		Running:        c.proc != nil && c.proc.err() == nil,
		Initialized:    c.init != nil,
		StartedAt:      c.startedAt,
	}
	if c.startErr != nil {
		status.LastError = c.startErr.Error()
	} else if c.proc != nil && c.proc.err() != nil {
		status.LastError = c.proc.err().Error()
	}
	if c.init != nil {
		status.AgentName = c.init.AgentInfo.Name
		status.AgentVersion = c.init.AgentInfo.Version
	}
	if c.proc != nil {
		counts := c.proc.callbackStatus()
		status.CallbackCounts.PermissionRequests = counts.PermissionRequests
		status.CallbackCounts.FSReadTextFile = counts.FSReadTextFile
		status.CallbackCounts.FSWriteTextFile = counts.FSWriteTextFile
		status.CallbackCounts.TerminalCreate = counts.TerminalCreate
		status.CallbackCounts.TerminalOutput = counts.TerminalOutput
		status.CallbackCounts.TerminalWait = counts.TerminalWait
		status.CallbackCounts.TerminalKill = counts.TerminalKill
		status.CallbackCounts.TerminalRelease = counts.TerminalRelease
	}
	return status
}

func (c *StdioClient) DiscoverAgents(ctx context.Context) ([]domain.AgentManifest, error) {
	init, err := c.ensureInitialized(ctx)
	if err != nil {
		return nil, err
	}
	return []domain.AgentManifest{c.manifestFromInitialize(init)}, nil
}

func (c *StdioClient) manifestFromInitialize(init *stdioInitializeResult) domain.AgentManifest {
	return domain.AgentManifest{
		Name:                    c.cfg.DefaultAgentName,
		Description:             init.AgentInfo.Name,
		Protocol:                "acp",
		InputContentTypes:       []string{"text/plain", "application/json"},
		OutputContentTypes:      []string{"text/plain", "application/json"},
		SupportsAwaitResume:     init.AgentCapabilities.SessionCapabilities.Resume != nil,
		SupportsStructuredAwait: init.AgentCapabilities.SessionCapabilities.Resume != nil,
		SupportsSessionReload:   init.AgentCapabilities.LoadSession,
		SupportsStreaming:       true,
		SupportsArtifacts:       true,
		Healthy:                 true,
	}
}

func (c *StdioClient) EnsureSession(ctx context.Context, session domain.Session) (string, error) {
	if session.ACPSessionID != "" {
		return session.ACPSessionID, nil
	}
	proc, err := c.ensureProcess(ctx)
	if err != nil {
		return "", err
	}
	var response stdioSessionNewResponse
	params := map[string]any{
		"cwd":        c.cfg.Workdir,
		"mcpServers": []any{},
	}
	if c.cfg.DefaultAgentName != "" {
		params["mode"] = c.cfg.DefaultAgentName
	}
	if err := proc.call(ctx, c.cfg.RPCTimeout, "session/new", params, &response); err != nil {
		return "", err
	}
	if response.SessionID == "" {
		return "", fmt.Errorf("session/new: missing sessionId")
	}
	return response.SessionID, nil
}

func (c *StdioClient) StartRun(ctx context.Context, req domain.StartRunRequest) (domain.Run, domain.RunEventStream, error) {
	sessionID := req.Session.ACPSessionID
	if sessionID == "" {
		var err error
		sessionID, err = c.EnsureSession(ctx, req.Session)
		if err != nil {
			return domain.Run{}, domain.RunEventStream{}, err
		}
	}
	proc, err := c.ensureProcess(ctx)
	if err != nil {
		return domain.Run{}, domain.RunEventStream{}, err
	}
	updates := proc.subscribe(sessionID)
	eventCh := make(chan domain.RunEvent, 128)
	errCh := make(chan error, 1)
	runCh := make(chan domain.Run, 1)
	startErrCh := make(chan error, 1)
	go c.streamPrompt(ctx, proc, sessionID, updates, map[string]any{
		"sessionId": sessionID,
		"prompt":    buildStdioPromptParts(req.Message),
	}, req.Session.ID, req.IdempotencyKey, req.RouteDecision.ACPAgentName, "", eventCh, errCh, runCh, startErrCh)

	select {
	case run := <-runCh:
		return run, domain.RunEventStream{Events: eventCh, Err: errCh}, nil
	case err := <-startErrCh:
		return domain.Run{}, domain.RunEventStream{}, err
	case <-ctx.Done():
		return domain.Run{}, domain.RunEventStream{}, ctx.Err()
	}
}

func (c *StdioClient) ResumeRun(ctx context.Context, await domain.Await, payload []byte) (domain.RunEventStream, error) {
	sessionID, _, err := splitACPRunID(strings.TrimPrefix(await.RunID, "run_"))
	if err != nil {
		return domain.RunEventStream{}, err
	}
	proc, err := c.ensureProcess(ctx)
	if err != nil {
		return domain.RunEventStream{}, err
	}
	updates := proc.subscribe(sessionID)
	eventCh := make(chan domain.RunEvent, 128)
	errCh := make(chan error, 1)
	go c.streamResume(ctx, proc, sessionID, updates, map[string]any{
		"sessionId": sessionID,
		"prompt": []map[string]any{{
			"type": "text",
			"text": resumePayloadText(payload),
		}},
	}, await.RunID, eventCh, errCh)
	return domain.RunEventStream{Events: eventCh, Err: errCh}, nil
}

func (c *StdioClient) streamPrompt(
	ctx context.Context,
	proc *stdioProcess,
	sessionID string,
	updates chan stdioSessionUpdate,
	params map[string]any,
	gatewaySessionID string,
	idempotencyKey string,
	agentName string,
	existingRunID string,
	eventCh chan<- domain.RunEvent,
	errCh chan<- error,
	runCh chan<- domain.Run,
	startErrCh chan<- error,
) {
	defer close(eventCh)
	defer close(errCh)
	unsubscribed := false
	unsubscribe := func() {
		if unsubscribed {
			return
		}
		proc.unsubscribe(sessionID, updates)
		unsubscribed = true
	}
	defer unsubscribe()

	type promptResult struct {
		response stdioPromptResponse
		err      error
	}
	callDone := make(chan promptResult, 1)
	go func() {
		var response stdioPromptResponse
		err := proc.call(ctx, c.cfg.RPCTimeout, "session/prompt", params, &response)
		callDone <- promptResult{response: response, err: err}
	}()

	replay := stdioReplay{outputs: map[string]*strings.Builder{}}
	var (
		run           domain.Run
		lastText      string
		lastArtifacts int
		runPublished  bool
	)
	publishRun := func(messageID string) error {
		if runPublished {
			return nil
		}
		composed := composeACPRunID(sessionID, messageID)
		runID := existingRunID
		if runID == "" {
			runID = "run_" + composed
		}
		run = domain.Run{
			ID:              runID,
			SessionID:       gatewaySessionID,
			ACPConnectionID: "stdio",
			ACPAgentName:    agentName,
			ACPRunID:        composed,
			Status:          "running",
			StartedAt:       time.Now().UTC(),
			LastEventAt:     time.Now().UTC(),
		}
		if idempotencyKey != "" {
			if err := c.storeIdempotencyRunID(idempotencyKey, composed); err != nil {
				return err
			}
		}
		runPublished = true
		if runCh != nil {
			runCh <- run
		}
		return nil
	}
	handleUpdate := func(update stdioSessionUpdate) error {
		if update.SessionUpdate != "agent_message_chunk" || update.Content == nil {
			return nil
		}
		replay.lastSeen = update.MessageID
		if replay.outputs == nil {
			replay.outputs = map[string]*strings.Builder{}
		}
		if replay.artifacts == nil {
			replay.artifacts = map[string][]domain.Artifact{}
		}
		artifact := stdioArtifactFromContent(update.MessageID, update.Content)
		if artifact.ID != "" {
			replay.storeArtifact(update.MessageID, artifact)
		}
		if update.Content.Type == "text" {
			if replay.outputs[update.MessageID] == nil {
				replay.outputs[update.MessageID] = &strings.Builder{}
			}
			replay.outputs[update.MessageID].WriteString(update.Content.Text)
		}
		if err := publishRun(update.MessageID); err != nil {
			return err
		}
		text := ""
		if replay.outputs[update.MessageID] != nil {
			text = strings.TrimSpace(replay.outputs[update.MessageID].String())
		}
		artifacts := replay.artifactsFor(update.MessageID)
		if text != "" && text != lastText {
			lastText = text
			eventCh <- domain.RunEvent{
				RunID:      run.ID,
				MessageKey: run.ACPRunID,
				Status:     "running",
				Text:       text,
				IsPartial:  true,
			}
		} else if len(artifacts) > lastArtifacts {
			lastArtifacts = len(artifacts)
		}
		lastArtifacts = len(artifacts)
		return nil
	}

	for {
		select {
		case update, ok := <-updates:
			if !ok {
				reportStdioStartError(startErrCh, errCh, runPublished, fmt.Errorf("stdio prompt stream closed early"))
				return
			}
			if err := handleUpdate(update); err != nil {
				reportStdioStartError(startErrCh, errCh, runPublished, err)
				return
			}
		case result := <-callDone:
			unsubscribe()
			for update := range updates {
				if err := handleUpdate(update); err != nil {
					reportStdioStartError(startErrCh, errCh, runPublished, err)
					return
				}
			}
			if result.err != nil {
				reportStdioStartError(startErrCh, errCh, runPublished, result.err)
				return
			}
			runID, status, output, artifacts := replay.latest(result.response.StopReason)
			if !runPublished {
				if runID == "" {
					reportStdioStartError(startErrCh, errCh, false, fmt.Errorf("session/prompt: missing assistant message id"))
					return
				}
				if err := publishRun(runID); err != nil {
					reportStdioStartError(startErrCh, errCh, false, err)
					return
				}
			}
			if output != lastText || status != "running" || len(artifacts) != lastArtifacts {
				eventCh <- domain.RunEvent{
					RunID:      run.ID,
					MessageKey: run.ACPRunID,
					Status:     status,
					Text:       output,
					IsPartial:  false,
					Artifacts:  artifacts,
				}
			}
			return
		}
	}
}

func (c *StdioClient) streamResume(
	ctx context.Context,
	proc *stdioProcess,
	sessionID string,
	updates chan stdioSessionUpdate,
	params map[string]any,
	runID string,
	eventCh chan<- domain.RunEvent,
	errCh chan<- error,
) {
	c.streamPrompt(ctx, proc, sessionID, updates, params, "", "", "", runID, eventCh, errCh, nil, nil)
}

func reportStdioStartError(startErrCh chan<- error, errCh chan<- error, runPublished bool, err error) {
	if err == nil {
		return
	}
	if !runPublished && startErrCh != nil {
		startErrCh <- err
		return
	}
	errCh <- err
}

func (c *StdioClient) GetRun(ctx context.Context, acpRunID string) (domain.RunStatusSnapshot, error) {
	sessionID, messageID, err := splitACPRunID(acpRunID)
	if err != nil {
		return domain.RunStatusSnapshot{}, err
	}
	replay, err := c.loadReplay(ctx, sessionID)
	if err != nil {
		return domain.RunStatusSnapshot{}, err
	}
	if !replay.has(messageID) {
		return domain.RunStatusSnapshot{}, fmt.Errorf("session/load: run %s not found", acpRunID)
	}
	status, output, artifacts := replay.snapshot(messageID, "")
	return domain.RunStatusSnapshot{
		ACPRunID:  composeACPRunID(sessionID, messageID),
		Status:    status,
		Output:    output,
		Artifacts: artifacts,
	}, nil
}

func (c *StdioClient) FindRunByIdempotencyKey(ctx context.Context, session domain.Session, idempotencyKey string) (domain.RunStatusSnapshot, bool, error) {
	if strings.TrimSpace(idempotencyKey) == "" {
		return domain.RunStatusSnapshot{}, false, nil
	}
	acpRunID, ok, err := c.loadIdempotencyRunID(idempotencyKey)
	if err != nil || !ok {
		return domain.RunStatusSnapshot{}, ok, err
	}
	if session.ACPSessionID != "" {
		sessionID, _, splitErr := splitACPRunID(acpRunID)
		if splitErr != nil {
			return domain.RunStatusSnapshot{}, false, splitErr
		}
		if sessionID != session.ACPSessionID {
			return domain.RunStatusSnapshot{}, false, nil
		}
	}
	snapshot, err := c.GetRun(ctx, acpRunID)
	if err != nil {
		return domain.RunStatusSnapshot{}, false, err
	}
	return snapshot, true, nil
}

func (c *StdioClient) FindLatestRunForSession(ctx context.Context, session domain.Session) (domain.RunStatusSnapshot, bool, error) {
	if session.ACPSessionID == "" {
		return domain.RunStatusSnapshot{}, false, nil
	}
	replay, err := c.loadReplay(ctx, session.ACPSessionID)
	if err != nil {
		return domain.RunStatusSnapshot{}, false, err
	}
	runID, status, output, artifacts := replay.latest("")
	if runID == "" {
		return domain.RunStatusSnapshot{}, false, nil
	}
	return domain.RunStatusSnapshot{
		ACPRunID:  composeACPRunID(session.ACPSessionID, runID),
		Status:    status,
		Output:    output,
		Artifacts: artifacts,
	}, true, nil
}

func (c *StdioClient) CancelRun(ctx context.Context, run domain.Run) error {
	sessionID, _, err := splitACPRunID(run.ACPRunID)
	if err != nil {
		return err
	}
	proc, err := c.ensureProcess(ctx)
	if err != nil {
		return err
	}
	return proc.call(ctx, c.cfg.RPCTimeout, "session/cancel", map[string]any{"sessionId": sessionID}, nil)
}

func (c *StdioClient) ensureInitialized(ctx context.Context) (*stdioInitializeResult, error) {
	proc, err := c.ensureProcess(ctx)
	if err != nil {
		return nil, err
	}
	c.mu.Lock()
	if c.init != nil {
		defer c.mu.Unlock()
		return c.init, nil
	}
	c.mu.Unlock()

	var response stdioInitializeResult
	if err := proc.call(ctx, c.cfg.StartupTimeout, "initialize", map[string]any{
		"protocolVersion": 1,
		"clientCapabilities": map[string]any{
			"fs": map[string]bool{
				"readTextFile":  true,
				"writeTextFile": true,
			},
			"terminal": true,
		},
		"clientInfo": map[string]any{
			"name":    "nexus",
			"version": "0.1.0",
		},
	}, &response); err != nil {
		return nil, err
	}
	c.mu.Lock()
	c.init = &response
	c.mu.Unlock()
	return &response, nil
}

func (c *StdioClient) ensureProcess(ctx context.Context) (*stdioProcess, error) {
	_ = ctx
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return nil, errors.New("stdio acp client is closed")
	}
	if c.proc != nil && c.proc.err() == nil {
		return c.proc, nil
	}
	proc, err := startStdioProcess(c.cfg)
	c.startErr = err
	if err != nil {
		return nil, err
	}
	c.startedAt = time.Now().UTC()
	c.proc = proc
	c.init = nil
	return proc, nil
}

func (c *StdioClient) loadReplay(ctx context.Context, sessionID string) (stdioReplay, error) {
	proc, err := c.ensureProcess(ctx)
	if err != nil {
		return stdioReplay{}, err
	}
	updates := proc.subscribe(sessionID)
	defer proc.unsubscribe(sessionID, updates)
	var ignored map[string]any
	if err := proc.call(ctx, c.cfg.RPCTimeout, "session/load", map[string]any{
		"sessionId":  sessionID,
		"cwd":        c.cfg.Workdir,
		"mcpServers": []any{},
	}, &ignored); err != nil {
		return stdioReplay{}, err
	}
	return collectReplay(updates), nil
}

func startStdioProcess(cfg StdioConfig) (*stdioProcess, error) {
	cmd := exec.Command(cfg.Command, cfg.Args...)
	cmd.Dir = cfg.Workdir
	if len(cfg.Env) > 0 {
		cmd.Env = append(os.Environ(), cfg.Env...)
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	proc := &stdioProcess{
		cmd:     cmd,
		stdin:   stdin,
		root:    cfg.Workdir,
		pending: map[int64]chan stdioIncoming{},
		subs:    map[string]map[chan stdioSessionUpdate]struct{}{},
		terms:   map[string]*managedTerminal{},
	}
	go proc.readLoop(stdout)
	go io.Copy(io.Discard, stderr)
	go func() {
		_ = cmd.Wait()
		proc.mu.Lock()
		if proc.readErr == nil {
			proc.readErr = fmt.Errorf("stdio acp subprocess exited")
		}
		for id, ch := range proc.pending {
			ch <- stdioIncoming{err: &stdioRPCError{Code: -32000, Message: proc.readErr.Error()}}
			close(ch)
			delete(proc.pending, id)
		}
		proc.mu.Unlock()
	}()
	return proc, nil
}

func (c *StdioClient) stateFilePath() string {
	root := strings.TrimSpace(c.cfg.Workdir)
	if root == "" {
		root = "."
	}
	return filepath.Join(root, ".nexus-stdio-client-state.json")
}

func (c *StdioClient) loadState() (stdioClientState, error) {
	path := c.stateFilePath()
	state := stdioClientState{IdempotencyRunIDs: map[string]string{}}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return state, nil
	}
	if err != nil {
		return stdioClientState{}, err
	}
	if err := json.Unmarshal(data, &state); err != nil {
		return stdioClientState{}, err
	}
	if state.IdempotencyRunIDs == nil {
		state.IdempotencyRunIDs = map[string]string{}
	}
	return state, nil
}

func (c *StdioClient) saveState(state stdioClientState) error {
	path := c.stateFilePath()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := json.Marshal(state)
	if err != nil {
		return err
	}
	tempPath := path + ".tmp"
	if err := os.WriteFile(tempPath, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tempPath, path)
}

func (c *StdioClient) storeIdempotencyRunID(idempotencyKey, acpRunID string) error {
	c.stateMu.Lock()
	defer c.stateMu.Unlock()
	state, err := c.loadState()
	if err != nil {
		return err
	}
	state.IdempotencyRunIDs[idempotencyKey] = acpRunID
	return c.saveState(state)
}

func (c *StdioClient) loadIdempotencyRunID(idempotencyKey string) (string, bool, error) {
	c.stateMu.Lock()
	defer c.stateMu.Unlock()
	state, err := c.loadState()
	if err != nil {
		return "", false, err
	}
	acpRunID, ok := state.IdempotencyRunIDs[idempotencyKey]
	return acpRunID, ok, nil
}

func (p *stdioProcess) call(ctx context.Context, timeout time.Duration, method string, params any, out any) error {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	id := atomic.AddInt64(&p.nextID, 1)
	ch := make(chan stdioIncoming, 1)
	p.mu.Lock()
	p.pending[id] = ch
	p.mu.Unlock()

	req := map[string]any{
		"jsonrpc": "2.0",
		"id":      id,
		"method":  method,
		"params":  params,
	}
	line, err := json.Marshal(req)
	if err != nil {
		return err
	}
	p.writeMu.Lock()
	_, err = p.stdin.Write(append(line, '\n'))
	p.writeMu.Unlock()
	if err != nil {
		return err
	}

	select {
	case msg := <-ch:
		if msg.err != nil {
			return msg.err
		}
		if out == nil {
			return nil
		}
		if len(msg.result) == 0 || bytes.Equal(bytes.TrimSpace(msg.result), []byte("null")) {
			return nil
		}
		return json.Unmarshal(msg.result, out)
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (p *stdioProcess) subscribe(sessionID string) chan stdioSessionUpdate {
	ch := make(chan stdioSessionUpdate, 128)
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.subs[sessionID] == nil {
		p.subs[sessionID] = map[chan stdioSessionUpdate]struct{}{}
	}
	p.subs[sessionID][ch] = struct{}{}
	return ch
}

func (p *stdioProcess) unsubscribe(sessionID string, ch chan stdioSessionUpdate) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if subs := p.subs[sessionID]; subs != nil {
		delete(subs, ch)
		if len(subs) == 0 {
			delete(p.subs, sessionID)
		}
	}
	close(ch)
}

func (p *stdioProcess) readLoop(stdout io.Reader) {
	scanner := bufio.NewScanner(stdout)
	buf := make([]byte, 0, 64*1024)
	scanner.Buffer(buf, 10*1024*1024)
	for scanner.Scan() {
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}
		var env stdioRPCEnvelope
		if err := json.Unmarshal(line, &env); err != nil {
			continue
		}
		if env.ID != nil && (len(env.Result) > 0 || env.Error != nil) {
			p.mu.Lock()
			ch := p.pending[*env.ID]
			delete(p.pending, *env.ID)
			p.mu.Unlock()
			if ch != nil {
				ch <- stdioIncoming{result: env.Result, err: env.Error}
				close(ch)
			}
			continue
		}
		if env.ID != nil && env.Method != "" {
			go p.handleRequest(*env.ID, env.Method, env.Params)
			continue
		}
		if env.Method == "session/update" {
			var update stdioSessionUpdateEnvelope
			if err := json.Unmarshal(env.Params, &update); err != nil {
				continue
			}
			p.mu.Lock()
			subs := p.subs[update.SessionID]
			for ch := range subs {
				select {
				case ch <- update.Update:
				default:
				}
			}
			p.mu.Unlock()
		}
	}
	if err := scanner.Err(); err != nil {
		p.mu.Lock()
		p.readErr = err
		p.mu.Unlock()
	}
}

func (p *stdioProcess) handleRequest(id int64, method string, params json.RawMessage) {
	var (
		result any
		err    error
	)
	switch method {
	case "session/request_permission":
		p.recordCallback(func(c *stdioCallbackCounts) { c.PermissionRequests++ })
		result, err = p.handlePermission(params)
	case "fs/read_text_file":
		p.recordCallback(func(c *stdioCallbackCounts) { c.FSReadTextFile++ })
		result, err = p.handleReadTextFile(params)
	case "fs/write_text_file":
		p.recordCallback(func(c *stdioCallbackCounts) { c.FSWriteTextFile++ })
		result, err = p.handleWriteTextFile(params)
	case "terminal/create":
		p.recordCallback(func(c *stdioCallbackCounts) { c.TerminalCreate++ })
		result, err = p.handleTerminalCreate(params)
	case "terminal/output":
		p.recordCallback(func(c *stdioCallbackCounts) { c.TerminalOutput++ })
		result, err = p.handleTerminalOutput(params)
	case "terminal/wait_for_exit":
		p.recordCallback(func(c *stdioCallbackCounts) { c.TerminalWait++ })
		result, err = p.handleTerminalWait(params)
	case "terminal/kill":
		p.recordCallback(func(c *stdioCallbackCounts) { c.TerminalKill++ })
		result, err = p.handleTerminalKill(params)
	case "terminal/release":
		p.recordCallback(func(c *stdioCallbackCounts) { c.TerminalRelease++ })
		result, err = p.handleTerminalRelease(params)
	default:
		err = &stdioRPCError{Code: -32601, Message: "method not found"}
	}
	resp := map[string]any{
		"jsonrpc": "2.0",
		"id":      id,
	}
	if err != nil {
		if rpcErr, ok := err.(*stdioRPCError); ok {
			resp["error"] = rpcErr
		} else {
			resp["error"] = &stdioRPCError{Code: -32000, Message: err.Error()}
		}
	} else {
		resp["result"] = result
	}
	line, marshalErr := json.Marshal(resp)
	if marshalErr != nil {
		return
	}
	p.writeMu.Lock()
	_, _ = p.stdin.Write(append(line, '\n'))
	p.writeMu.Unlock()
}

func (p *stdioProcess) handlePermission(params json.RawMessage) (any, error) {
	var req struct {
		Options []map[string]any `json:"options"`
	}
	if err := json.Unmarshal(params, &req); err != nil {
		return nil, err
	}
	selected := ""
	for _, option := range req.Options {
		id, _ := option["id"].(string)
		if id == "allow_once" || id == "allow_always" || id == "allow" {
			selected = id
			break
		}
	}
	if selected == "" {
		for _, option := range req.Options {
			id, _ := option["id"].(string)
			if id == "deny_once" || id == "deny" || id == "reject" {
				selected = id
				break
			}
		}
	}
	if selected == "" {
		return nil, &stdioRPCError{Code: -32000, Message: "no explicit permission decision available"}
	}
	return map[string]any{"optionId": selected}, nil
}

func (p *stdioProcess) handleReadTextFile(params json.RawMessage) (any, error) {
	var req struct {
		Path  string `json:"path"`
		Line  int    `json:"line"`
		Limit int    `json:"limit"`
	}
	if err := json.Unmarshal(params, &req); err != nil {
		return nil, err
	}
	path, err := normalizeWorkspacePath(p.root, req.Path)
	if err != nil {
		return nil, err
	}
	if req.Line > 0 || req.Limit > 0 {
		content, err := readTextFileWindow(path, req.Line, req.Limit, stdioMaxFileContentBytes)
		if err != nil {
			return nil, err
		}
		return map[string]any{"content": content}, nil
	}
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if info.Size() > stdioMaxFileContentBytes {
		return nil, fmt.Errorf("read_text_file exceeds %d bytes", stdioMaxFileContentBytes)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	content := string(data)
	return map[string]any{"content": content}, nil
}

func (p *stdioProcess) handleWriteTextFile(params json.RawMessage) (any, error) {
	var req struct {
		Path    string `json:"path"`
		Content string `json:"content"`
	}
	if err := json.Unmarshal(params, &req); err != nil {
		return nil, err
	}
	if strings.TrimSpace(req.Path) == "" {
		return nil, fmt.Errorf("write_text_file: missing path")
	}
	if len(req.Content) > stdioMaxWriteBytes {
		return nil, fmt.Errorf("write_text_file exceeds %d bytes", stdioMaxWriteBytes)
	}
	path, err := normalizeWorkspacePath(p.root, req.Path)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	if err := os.WriteFile(path, []byte(req.Content), 0o644); err != nil {
		return nil, err
	}
	return nil, nil
}

func (p *stdioProcess) handleTerminalCreate(params json.RawMessage) (any, error) {
	var req struct {
		Command         string   `json:"command"`
		Args            []string `json:"args"`
		Cwd             string   `json:"cwd"`
		OutputByteLimit int      `json:"outputByteLimit"`
	}
	if err := json.Unmarshal(params, &req); err != nil {
		return nil, err
	}
	if req.Command == "" {
		return nil, fmt.Errorf("terminal/create: missing command")
	}
	cwd := req.Cwd
	if cwd == "" {
		cwd = "."
	}
	cwd, err := normalizeWorkspacePath(p.root, cwd)
	if err != nil {
		return nil, err
	}
	cmd := exec.Command(req.Command, req.Args...)
	cmd.Dir = cwd
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	terminalID := fmt.Sprintf("term_%d", atomic.AddInt64(&p.nextID, 1))
	term := &managedTerminal{
		cmd:         cmd,
		outputLimit: req.OutputByteLimit,
		done:        make(chan struct{}),
	}
	if term.outputLimit <= 0 {
		term.outputLimit = 1024 * 1024
	}
	p.mu.Lock()
	p.terms[terminalID] = term
	p.mu.Unlock()
	go term.capture(stdout)
	go term.capture(stderr)
	go term.wait()
	return map[string]any{"terminalId": terminalID}, nil
}

func (p *stdioProcess) handleTerminalOutput(params json.RawMessage) (any, error) {
	term, err := p.lookupTerminal(params)
	if err != nil {
		return nil, err
	}
	term.mu.Lock()
	defer term.mu.Unlock()
	result := map[string]any{
		"output":    string(term.output),
		"truncated": term.truncated,
	}
	if term.exitCode != nil {
		result["exitStatus"] = map[string]any{"exitCode": *term.exitCode, "signal": nil}
	}
	return result, nil
}

func (p *stdioProcess) handleTerminalWait(params json.RawMessage) (any, error) {
	term, err := p.lookupTerminal(params)
	if err != nil {
		return nil, err
	}
	<-term.done
	term.mu.Lock()
	defer term.mu.Unlock()
	exitCode := any(nil)
	if term.exitCode != nil {
		exitCode = *term.exitCode
	}
	return map[string]any{"exitCode": exitCode, "signal": nil}, nil
}

func (p *stdioProcess) handleTerminalKill(params json.RawMessage) (any, error) {
	term, err := p.lookupTerminal(params)
	if err != nil {
		return nil, err
	}
	if term.cmd.Process != nil {
		_ = term.cmd.Process.Kill()
	}
	return map[string]any{}, nil
}

func (p *stdioProcess) handleTerminalRelease(params json.RawMessage) (any, error) {
	var req struct {
		TerminalID string `json:"terminalId"`
	}
	if err := json.Unmarshal(params, &req); err != nil {
		return nil, err
	}
	p.mu.Lock()
	term := p.terms[req.TerminalID]
	delete(p.terms, req.TerminalID)
	p.mu.Unlock()
	if term == nil {
		return nil, fmt.Errorf("terminal not found")
	}
	if term.cmd.Process != nil && !term.exited() {
		_ = term.cmd.Process.Kill()
	}
	return map[string]any{}, nil
}

func (p *stdioProcess) lookupTerminal(params json.RawMessage) (*managedTerminal, error) {
	var req struct {
		TerminalID string `json:"terminalId"`
	}
	if err := json.Unmarshal(params, &req); err != nil {
		return nil, err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	term := p.terms[req.TerminalID]
	if term == nil {
		return nil, fmt.Errorf("terminal not found")
	}
	return term, nil
}

func normalizeWorkspacePath(root, path string) (string, error) {
	if !filepath.IsAbs(path) {
		path = filepath.Join(root, path)
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	root, err = filepath.Abs(root)
	if err != nil {
		return "", err
	}
	rel, err := filepath.Rel(root, abs)
	if err != nil {
		return "", err
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("path %q escapes workspace root", path)
	}
	return abs, nil
}

func readTextFileWindow(path string, startLine, limit, maxBytes int) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()

	if startLine <= 0 {
		startLine = 1
	}
	currentLine := 1
	linesRead := 0
	reader := bufio.NewReader(file)
	var out strings.Builder
	for {
		chunk, readErr := reader.ReadString('\n')
		if currentLine >= startLine {
			if out.Len()+len(chunk) > maxBytes {
				return "", fmt.Errorf("read_text_file window exceeds %d bytes", maxBytes)
			}
			out.WriteString(chunk)
			linesRead++
			if limit > 0 && linesRead >= limit {
				return out.String(), nil
			}
		}
		if errors.Is(readErr, io.EOF) {
			if currentLine >= startLine {
				return out.String(), nil
			}
			return "", nil
		}
		if readErr != nil {
			return "", readErr
		}
		currentLine++
	}
}

func (t *managedTerminal) capture(reader io.Reader) {
	data, _ := io.ReadAll(reader)
	t.mu.Lock()
	defer t.mu.Unlock()
	t.output = append(t.output, data...)
	if len(t.output) > t.outputLimit {
		t.output = append([]byte(nil), t.output[len(t.output)-t.outputLimit:]...)
		t.truncated = true
	}
}

func (t *managedTerminal) wait() {
	err := t.cmd.Wait()
	t.mu.Lock()
	if t.cmd.ProcessState != nil {
		code := t.cmd.ProcessState.ExitCode()
		t.exitCode = &code
	} else if err != nil {
		code := -1
		t.exitCode = &code
	}
	t.mu.Unlock()
	close(t.done)
}

func (t *managedTerminal) exited() bool {
	select {
	case <-t.done:
		return true
	default:
		return false
	}
}

func (p *stdioProcess) err() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.readErr
}

func (p *stdioProcess) recordCallback(fn func(*stdioCallbackCounts)) {
	p.mu.Lock()
	defer p.mu.Unlock()
	fn(&p.callbackCounts)
}

func (p *stdioProcess) callbackStatus() stdioCallbackCounts {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.callbackCounts
}

func collectReplay(ch <-chan stdioSessionUpdate) stdioReplay {
	replay := stdioReplay{
		outputs:   map[string]*strings.Builder{},
		artifacts: map[string][]domain.Artifact{},
	}
	for {
		select {
		case update, ok := <-ch:
			if !ok {
				return replay
			}
			switch update.SessionUpdate {
			case "agent_message_chunk":
				replay.lastSeen = update.MessageID
				if update.Content != nil {
					if artifact := stdioArtifactFromContent(update.MessageID, update.Content); artifact.ID != "" {
						replay.storeArtifact(update.MessageID, artifact)
					}
					if update.Content.Type == "text" {
						if replay.outputs[update.MessageID] == nil {
							replay.outputs[update.MessageID] = &strings.Builder{}
						}
						replay.outputs[update.MessageID].WriteString(update.Content.Text)
					}
				}
			}
		default:
			return replay
		}
	}
}

func (r stdioReplay) latest(stopReason string) (string, string, string, []domain.Artifact) {
	if r.lastSeen == "" {
		return "", mapStopReason(stopReason), "", nil
	}
	status, output, artifacts := r.snapshot(r.lastSeen, stopReason)
	return r.lastSeen, status, output, artifacts
}

func (r stdioReplay) snapshot(messageID, stopReason string) (string, string, []domain.Artifact) {
	if messageID == "" {
		return mapStopReason(stopReason), "", nil
	}
	output := ""
	if r.outputs[messageID] != nil {
		output = strings.TrimSpace(r.outputs[messageID].String())
	}
	return mapStopReason(stopReason), output, r.artifactsFor(messageID)
}

func (r stdioReplay) has(messageID string) bool {
	return messageID != "" && (r.outputs[messageID] != nil || len(r.artifacts[messageID]) > 0)
}

func (r *stdioReplay) storeArtifact(messageID string, artifact domain.Artifact) {
	if artifact.ID == "" {
		return
	}
	items := r.artifacts[messageID]
	for idx := range items {
		if items[idx].ID == artifact.ID {
			items[idx] = artifact
			r.artifacts[messageID] = items
			return
		}
	}
	r.artifacts[messageID] = append(items, artifact)
}

func (r stdioReplay) artifactsFor(messageID string) []domain.Artifact {
	if len(r.artifacts[messageID]) == 0 {
		return nil
	}
	out := make([]domain.Artifact, len(r.artifacts[messageID]))
	copy(out, r.artifacts[messageID])
	return out
}

func mapStopReason(stopReason string) string {
	switch stopReason {
	case "", "end_turn":
		return "completed"
	case "cancelled", "canceled":
		return "canceled"
	case "awaiting", "pause_turn", "resume":
		return "awaiting"
	default:
		return "completed"
	}
}

func buildStdioPromptParts(message domain.Message) []map[string]any {
	parts := make([]map[string]any, 0, len(message.Parts)+len(message.Artifacts)+1)
	for _, part := range message.Parts {
		if strings.TrimSpace(part.Content) == "" {
			continue
		}
		text := part.Content
		if part.ContentType == domain.LocationContentType {
			var location domain.Location
			if err := json.Unmarshal([]byte(part.Content), &location); err == nil {
				text = domain.LocationText(location)
			}
		}
		parts = append(parts, map[string]any{
			"type": "text",
			"text": text,
		})
	}
	if len(parts) == 0 && strings.TrimSpace(message.Text) != "" {
		parts = append(parts, map[string]any{
			"type": "text",
			"text": message.Text,
		})
	}
	for _, artifact := range message.Artifacts {
		if artifact.StorageURI == "" && artifact.SourceURL == "" {
			continue
		}
		contentBlock := stdioPromptContentBlock(artifact)
		if contentBlock == nil {
			continue
		}
		parts = append(parts, contentBlock)
	}
	if len(parts) == 0 {
		parts = append(parts, map[string]any{
			"type": "text",
			"text": "",
		})
	}
	return parts
}

func stdioPromptContentBlock(artifact domain.Artifact) map[string]any {
	fileURL := strings.TrimSpace(artifact.StorageURI)
	if fileURL == "" {
		fileURL = strings.TrimSpace(artifact.SourceURL)
	}
	if fileURL == "" {
		return nil
	}
	mimeType := strings.TrimSpace(artifact.MIMEType)
	if mimeType == "" {
		mimeType = "application/octet-stream"
	}
	name := strings.TrimSpace(artifact.Name)
	if strings.HasPrefix(fileURL, "data:") {
		encodedMime, encodedData, ok := splitDataURL(fileURL)
		if !ok {
			return nil
		}
		if encodedMime != "" {
			mimeType = encodedMime
		}
		switch {
		case strings.HasPrefix(mimeType, "image/"):
			return map[string]any{
				"type":     "image",
				"data":     encodedData,
				"mimeType": mimeType,
			}
		case strings.HasPrefix(mimeType, "audio/"):
			return map[string]any{
				"type":     "audio",
				"data":     encodedData,
				"mimeType": mimeType,
			}
		default:
			return map[string]any{
				"type": "resource",
				"resource": map[string]any{
					"mimeType": mimeType,
					"blob":     encodedData,
					"uri":      dataResourceURI(name, mimeType),
				},
			}
		}
	}
	block := map[string]any{
		"type":     "resource_link",
		"uri":      fileURL,
		"mimeType": mimeType,
	}
	if name != "" {
		block["name"] = name
	}
	return block
}

func splitDataURL(value string) (string, string, bool) {
	if !strings.HasPrefix(value, "data:") {
		return "", "", false
	}
	comma := strings.IndexByte(value, ',')
	if comma < 0 {
		return "", "", false
	}
	meta := strings.TrimPrefix(value[:comma], "data:")
	payload := value[comma+1:]
	if !strings.Contains(meta, ";base64") {
		return "", "", false
	}
	mimeType := strings.TrimSuffix(meta, ";base64")
	if mimeType == "" {
		mimeType = "application/octet-stream"
	}
	return mimeType, payload, true
}

func dataResourceURI(name, mimeType string) string {
	filename := strings.TrimSpace(name)
	if filename == "" {
		return "data:attachment"
	}
	return "data:" + mimeType + ";name=" + filename
}

func stdioArtifactFromContent(messageID string, content *struct {
	Type     string `json:"type"`
	Text     string `json:"text,omitempty"`
	Data     string `json:"data,omitempty"`
	MIMEType string `json:"mimeType,omitempty"`
	URI      string `json:"uri,omitempty"`
	Name     string `json:"name,omitempty"`
}) domain.Artifact {
	if content == nil {
		return domain.Artifact{}
	}
	switch content.Type {
	case "resource_link":
		uri := strings.TrimSpace(content.URI)
		if uri == "" {
			return domain.Artifact{}
		}
		name := strings.TrimSpace(content.Name)
		if name == "" {
			name = filepath.Base(strings.TrimPrefix(uri, "file://"))
			if name == "." || name == "/" || name == "" {
				name = "artifact"
			}
		}
		return domain.Artifact{
			ID:         "artifact_" + stdioArtifactID(messageID+"|"+uri),
			MessageID:  messageID,
			Name:       name,
			MIMEType:   strings.TrimSpace(content.MIMEType),
			StorageURI: uri,
			SourceURL:  uri,
		}
	case "image", "audio":
		data := strings.TrimSpace(content.Data)
		if data == "" {
			return domain.Artifact{}
		}
		mimeType := strings.TrimSpace(content.MIMEType)
		if mimeType == "" {
			if content.Type == "image" {
				mimeType = "image/png"
			} else {
				mimeType = "audio/wav"
			}
		}
		prefix := "data:" + mimeType + ";base64,"
		url := prefix + data
		decoded, err := base64.StdEncoding.DecodeString(data)
		if err != nil {
			decoded = nil
		}
		return domain.Artifact{
			ID:         "artifact_" + stdioArtifactID(messageID+"|"+url),
			MessageID:  messageID,
			Name:       defaultArtifactName(content.Type, mimeType),
			MIMEType:   mimeType,
			SizeBytes:  int64(len(decoded)),
			StorageURI: url,
			SourceURL:  url,
		}
	default:
		return domain.Artifact{}
	}
}

func defaultArtifactName(kind, mimeType string) string {
	switch {
	case kind == "image":
		return "image" + extensionForMIME(mimeType, ".png")
	case kind == "audio":
		return "audio" + extensionForMIME(mimeType, ".wav")
	default:
		return "artifact"
	}
}

func extensionForMIME(mimeType, fallback string) string {
	switch strings.ToLower(strings.TrimSpace(mimeType)) {
	case "image/png":
		return ".png"
	case "image/jpeg":
		return ".jpg"
	case "image/gif":
		return ".gif"
	case "image/webp":
		return ".webp"
	case "audio/mpeg":
		return ".mp3"
	case "audio/wav", "audio/x-wav":
		return ".wav"
	case "audio/ogg":
		return ".ogg"
	default:
		return fallback
	}
}

func stdioArtifactID(input string) string {
	sum := sha1.Sum([]byte(input))
	return hex.EncodeToString(sum[:16])
}
