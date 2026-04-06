package acp

import (
	"bufio"
	"bytes"
	"context"
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
	proc      *stdioProcess
	init      *stdioInitializeResult
	closed    bool
	startErr  error
	startedAt time.Time
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
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content,omitempty"`
}

type stdioReplay struct {
	runID    string
	output   strings.Builder
	status   string
	lastSeen string
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
	manifest := domain.AgentManifest{
		Name:                    c.cfg.DefaultAgentName,
		Description:             init.AgentInfo.Name,
		Protocol:                "acp",
		InputContentTypes:       []string{"text/plain", "application/json"},
		OutputContentTypes:      []string{"text/plain", "application/json"},
		SupportsAwaitResume:     init.AgentCapabilities.SessionCapabilities.Resume != nil,
		SupportsStructuredAwait: init.AgentCapabilities.SessionCapabilities.Resume != nil,
		SupportsStreaming:       true,
		SupportsArtifacts:       true,
		Healthy:                 true,
	}
	return []domain.AgentManifest{manifest}, nil
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

func (c *StdioClient) StartRun(ctx context.Context, req domain.StartRunRequest) (domain.Run, []domain.RunEvent, error) {
	sessionID := req.Session.ACPSessionID
	if sessionID == "" {
		var err error
		sessionID, err = c.EnsureSession(ctx, req.Session)
		if err != nil {
			return domain.Run{}, nil, err
		}
	}
	proc, err := c.ensureProcess(ctx)
	if err != nil {
		return domain.Run{}, nil, err
	}
	updates := proc.subscribe(sessionID)
	defer proc.unsubscribe(sessionID, updates)
	var response stdioPromptResponse
	if err := proc.call(ctx, c.cfg.RPCTimeout, "session/prompt", map[string]any{
		"sessionId": sessionID,
		"prompt":    buildStdioPromptParts(req.Message),
	}, &response); err != nil {
		return domain.Run{}, nil, err
	}
	replay := collectReplay(updates)
	runID, status, output := replay.finalize(response.StopReason)
	if runID == "" {
		return domain.Run{}, nil, fmt.Errorf("session/prompt: missing assistant message id")
	}
	run := domain.Run{
		ID:              "run_" + composeACPRunID(sessionID, runID),
		SessionID:       req.Session.ID,
		ACPConnectionID: "stdio",
		ACPRunID:        composeACPRunID(sessionID, runID),
		Status:          status,
		StartedAt:       time.Now().UTC(),
		LastEventAt:     time.Now().UTC(),
	}
	return run, []domain.RunEvent{{
		RunID:  run.ID,
		Status: status,
		Text:   output,
	}}, nil
}

func (c *StdioClient) ResumeRun(ctx context.Context, await domain.Await, payload []byte) ([]domain.RunEvent, error) {
	sessionID, _, err := splitACPRunID(strings.TrimPrefix(await.RunID, "run_"))
	if err != nil {
		return nil, err
	}
	proc, err := c.ensureProcess(ctx)
	if err != nil {
		return nil, err
	}
	updates := proc.subscribe(sessionID)
	defer proc.unsubscribe(sessionID, updates)
	var response stdioPromptResponse
	if err := proc.call(ctx, c.cfg.RPCTimeout, "session/prompt", map[string]any{
		"sessionId": sessionID,
		"prompt": []map[string]any{{
			"type": "text",
			"text": resumePayloadText(payload),
		}},
	}, &response); err != nil {
		return nil, err
	}
	replay := collectReplay(updates)
	runID, status, output := replay.finalize(response.StopReason)
	if runID == "" {
		return nil, fmt.Errorf("session/prompt resume: missing assistant message id")
	}
	return []domain.RunEvent{{
		RunID:  "run_" + composeACPRunID(sessionID, runID),
		Status: status,
		Text:   output,
	}}, nil
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
	if replay.lastSeen == "" {
		return domain.RunStatusSnapshot{}, fmt.Errorf("session/load: no assistant message found")
	}
	if messageID != replay.lastSeen && replay.runID != messageID {
		return domain.RunStatusSnapshot{}, fmt.Errorf("session/load: run %s not found", acpRunID)
	}
	_, status, output := replay.finalize("")
	return domain.RunStatusSnapshot{
		ACPRunID:  composeACPRunID(sessionID, messageID),
		Status:    status,
		Output:    output,
		Artifacts: nil,
	}, nil
}

func (c *StdioClient) FindRunByIdempotencyKey(context.Context, domain.Session, string) (domain.RunStatusSnapshot, bool, error) {
	return domain.RunStatusSnapshot{}, false, nil
}

func (c *StdioClient) FindLatestRunForSession(ctx context.Context, session domain.Session) (domain.RunStatusSnapshot, bool, error) {
	if session.ACPSessionID == "" {
		return domain.RunStatusSnapshot{}, false, nil
	}
	replay, err := c.loadReplay(ctx, session.ACPSessionID)
	if err != nil {
		return domain.RunStatusSnapshot{}, false, err
	}
	runID, status, output := replay.finalize("")
	if runID == "" {
		return domain.RunStatusSnapshot{}, false, nil
	}
	return domain.RunStatusSnapshot{
		ACPRunID:  composeACPRunID(session.ACPSessionID, runID),
		Status:    status,
		Output:    output,
		Artifacts: nil,
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
	if selected == "" && len(req.Options) > 0 {
		selected, _ = req.Options[0]["id"].(string)
	}
	if selected == "" {
		selected = "allow_once"
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
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	content := string(data)
	if req.Line > 0 || req.Limit > 0 {
		lines := strings.SplitAfter(content, "\n")
		start := 0
		if req.Line > 1 {
			start = req.Line - 1
			if start > len(lines) {
				start = len(lines)
			}
		}
		end := len(lines)
		if req.Limit > 0 && start+req.Limit < end {
			end = start + req.Limit
		}
		content = strings.Join(lines[start:end], "")
	}
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
	replay := stdioReplay{}
	for {
		select {
		case update, ok := <-ch:
			if !ok {
				return replay
			}
			switch update.SessionUpdate {
			case "agent_message_chunk":
				replay.runID = update.MessageID
				replay.lastSeen = update.MessageID
				if update.Content != nil {
					replay.output.WriteString(update.Content.Text)
				}
			}
		default:
			return replay
		}
	}
}

func (r stdioReplay) finalize(stopReason string) (string, string, string) {
	runID := r.runID
	if runID == "" {
		runID = r.lastSeen
	}
	return runID, mapStopReason(stopReason), strings.TrimSpace(r.output.String())
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
		parts = append(parts, map[string]any{
			"type": "text",
			"text": part.Content,
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
		fileURL := artifact.StorageURI
		if fileURL == "" {
			fileURL = artifact.SourceURL
		}
		parts = append(parts, map[string]any{
			"type":     "file",
			"mime":     artifact.MIMEType,
			"filename": artifact.Name,
			"url":      fileURL,
		})
	}
	if len(parts) == 0 {
		parts = append(parts, map[string]any{
			"type": "text",
			"text": "",
		})
	}
	return parts
}
