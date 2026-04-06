package acp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"nexus/internal/domain"
)

type Client struct {
	BaseURL   string
	Token     string
	Directory string
	HTTP      *http.Client

	state *clientState
}

type clientState struct {
	mu                    sync.Mutex
	sessionByGatewayID    map[string]string
	snapshotByIdempotency map[string]domain.RunStatusSnapshot
}

type openCodeAgent struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}

type openCodeSession struct {
	ID    string `json:"id"`
	Title string `json:"title"`
}

type openCodeAssistantMessage struct {
	ID         string `json:"id"`
	SessionID  string `json:"sessionID"`
	Role       string `json:"role"`
	ProviderID string `json:"providerID"`
	ModelID    string `json:"modelID"`
	Agent      string `json:"agent"`
	Finish     string `json:"finish"`
	Error      any    `json:"error"`
	Time       struct {
		Created int64 `json:"created"`
	} `json:"time"`
}

type openCodePromptResponse struct {
	Info  openCodeAssistantMessage `json:"info"`
	Parts []openCodePart           `json:"parts"`
}

type openCodePart struct {
	ID       string         `json:"id"`
	Type     string         `json:"type"`
	Text     string         `json:"text"`
	URL      string         `json:"url"`
	MIME     string         `json:"mime"`
	Filename string         `json:"filename"`
	State    map[string]any `json:"state"`
	Metadata map[string]any `json:"metadata"`
}

func New(baseURL, token string) Client {
	directory, _ := os.Getwd()
	return Client{
		BaseURL:   strings.TrimRight(baseURL, "/"),
		Token:     token,
		Directory: directory,
		HTTP:      &http.Client{Timeout: 60 * time.Second},
		state: &clientState{
			sessionByGatewayID:    map[string]string{},
			snapshotByIdempotency: map[string]domain.RunStatusSnapshot{},
		},
	}
}

func (c Client) DiscoverAgents(ctx context.Context) ([]domain.AgentManifest, error) {
	var agents []openCodeAgent
	if err := c.getJSON(ctx, "/agent", nil, &agents); err != nil {
		return nil, err
	}
	out := make([]domain.AgentManifest, 0, len(agents))
	for _, agent := range agents {
		if strings.TrimSpace(agent.Name) == "" {
			return nil, fmt.Errorf("discover agents: agent manifest missing name")
		}
		out = append(out, domain.AgentManifest{
			Name:                    agent.Name,
			Description:             agent.Description,
			Protocol:                "opencode",
			InputContentTypes:       []string{"text/plain", "application/json"},
			OutputContentTypes:      []string{"text/plain", "application/json"},
			SupportsAwaitResume:     false,
			SupportsStructuredAwait: false,
			SupportsSessionReload:   false,
			SupportsStreaming:       true,
			SupportsArtifacts:       true,
			Healthy:                 true,
		})
	}
	return out, nil
}

func (c Client) EnsureSession(ctx context.Context, session domain.Session) (string, error) {
	if session.ACPSessionID != "" {
		c.state.storeSession(session.ID, session.ACPSessionID)
		return session.ACPSessionID, nil
	}
	if cached, ok := c.state.loadSession(session.ID); ok {
		return cached, nil
	}
	title := c.sessionTitle(session)
	var sessions []openCodeSession
	if err := c.getJSON(ctx, "/session", map[string]string{"search": title, "limit": "20"}, &sessions); err != nil {
		return "", err
	}
	for _, existing := range sessions {
		if existing.Title == title && existing.ID != "" {
			c.state.storeSession(session.ID, existing.ID)
			return existing.ID, nil
		}
	}
	var created openCodeSession
	if err := c.postJSON(ctx, "/session", nil, map[string]any{"title": title}, &created); err != nil {
		return "", err
	}
	if created.ID == "" {
		return "", fmt.Errorf("ensure session: missing session id")
	}
	c.state.storeSession(session.ID, created.ID)
	return created.ID, nil
}

func (c Client) StartRun(ctx context.Context, req domain.StartRunRequest) (domain.Run, []domain.RunEvent, error) {
	acpSessionID := req.Session.ACPSessionID
	if acpSessionID == "" {
		var err error
		acpSessionID, err = c.EnsureSession(ctx, req.Session)
		if err != nil {
			return domain.Run{}, nil, err
		}
	}
	body := map[string]any{
		"agent": req.RouteDecision.ACPAgentName,
		"parts": c.buildPromptParts(req.Message),
	}
	var response openCodePromptResponse
	if err := c.postJSON(ctx, "/session/"+url.PathEscape(acpSessionID)+"/message", nil, body, &response); err != nil {
		return domain.Run{}, nil, err
	}
	run, events, snapshot, err := c.mapPromptResponse(req.Session.ID, response)
	if err != nil {
		return domain.Run{}, nil, err
	}
	if req.IdempotencyKey != "" {
		c.state.storeSnapshot(req.IdempotencyKey, snapshot)
	}
	return run, events, nil
}

func (c Client) ResumeRun(ctx context.Context, await domain.Await, payload []byte) ([]domain.RunEvent, error) {
	acpRunID := strings.TrimPrefix(await.RunID, "run_")
	sessionID, _, err := splitACPRunID(acpRunID)
	if err != nil {
		return nil, err
	}
	body := map[string]any{
		"parts": []map[string]any{{
			"type": "text",
			"text": resumePayloadText(payload),
		}},
	}
	var response openCodePromptResponse
	if err := c.postJSON(ctx, "/session/"+url.PathEscape(sessionID)+"/message", nil, body, &response); err != nil {
		return nil, err
	}
	_, events, _, err := c.mapPromptResponse(await.SessionID, response)
	return events, err
}

func (c Client) GetRun(ctx context.Context, acpRunID string) (domain.RunStatusSnapshot, error) {
	sessionID, messageID, err := splitACPRunID(acpRunID)
	if err != nil {
		return domain.RunStatusSnapshot{}, err
	}
	var response openCodePromptResponse
	if err := c.getJSON(ctx, "/session/"+url.PathEscape(sessionID)+"/message/"+url.PathEscape(messageID), nil, &response); err != nil {
		return domain.RunStatusSnapshot{}, err
	}
	snapshot, err := c.mapSnapshot(response)
	if err != nil {
		return domain.RunStatusSnapshot{}, err
	}
	if snapshot.ACPRunID == "" {
		snapshot.ACPRunID = composeACPRunID(sessionID, messageID)
	}
	return snapshot, nil
}

func (c Client) FindRunByIdempotencyKey(_ context.Context, _ domain.Session, idempotencyKey string) (domain.RunStatusSnapshot, bool, error) {
	if strings.TrimSpace(idempotencyKey) == "" {
		return domain.RunStatusSnapshot{}, false, nil
	}
	snapshot, ok := c.state.loadSnapshot(idempotencyKey)
	return snapshot, ok, nil
}

func (c Client) FindLatestRunForSession(ctx context.Context, session domain.Session) (domain.RunStatusSnapshot, bool, error) {
	acpSessionID := session.ACPSessionID
	if acpSessionID == "" {
		if cached, ok := c.state.loadSession(session.ID); ok {
			acpSessionID = cached
		}
	}
	if acpSessionID == "" {
		return domain.RunStatusSnapshot{}, false, nil
	}
	var messages []openCodePromptResponse
	if err := c.getJSON(ctx, "/session/"+url.PathEscape(acpSessionID)+"/message", map[string]string{"limit": "20"}, &messages); err != nil {
		return domain.RunStatusSnapshot{}, false, err
	}
	for _, message := range messages {
		if message.Info.Role != "assistant" || message.Info.ID == "" {
			continue
		}
		snapshot, err := c.mapSnapshot(message)
		if err != nil {
			return domain.RunStatusSnapshot{}, false, err
		}
		return snapshot, true, nil
	}
	return domain.RunStatusSnapshot{}, false, nil
}

func (c Client) CancelRun(ctx context.Context, run domain.Run) error {
	sessionID, _, err := splitACPRunID(run.ACPRunID)
	if err != nil {
		return err
	}
	return c.postJSON(ctx, "/session/"+url.PathEscape(sessionID)+"/abort", nil, nil, nil)
}

func (c Client) buildPromptParts(message domain.Message) []map[string]any {
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

func (c Client) sessionTitle(session domain.Session) string {
	return "gateway:" + session.ID
}

func (c Client) mapPromptResponse(sessionID string, response openCodePromptResponse) (domain.Run, []domain.RunEvent, domain.RunStatusSnapshot, error) {
	snapshot, err := c.mapSnapshot(response)
	if err != nil {
		return domain.Run{}, nil, domain.RunStatusSnapshot{}, err
	}
	if snapshot.ACPRunID == "" {
		return domain.Run{}, nil, domain.RunStatusSnapshot{}, fmt.Errorf("start run: missing message id")
	}
	run := domain.Run{
		ID:              "run_" + snapshot.ACPRunID,
		SessionID:       sessionID,
		ACPConnectionID: "opencode",
		ACPRunID:        snapshot.ACPRunID,
		Status:          snapshot.Status,
		StartedAt:       time.Now().UTC(),
		LastEventAt:     time.Now().UTC(),
	}
	event := domain.RunEvent{
		RunID:     run.ID,
		Status:    snapshot.Status,
		Text:      snapshot.Output,
		Artifacts: snapshot.Artifacts,
	}
	if snapshot.Await != nil {
		event.AwaitSchema = snapshot.Await.Schema
		event.AwaitPrompt = snapshot.Await.Prompt
	}
	return run, []domain.RunEvent{event}, snapshot, nil
}

func (c Client) mapSnapshot(response openCodePromptResponse) (domain.RunStatusSnapshot, error) {
	if response.Info.ID == "" || response.Info.SessionID == "" {
		return domain.RunStatusSnapshot{}, fmt.Errorf("run snapshot: missing required fields")
	}
	output, artifacts := parseOpenCodeParts(response.Parts)
	status := mapMessageStatus(response.Info)
	return domain.RunStatusSnapshot{
		ACPRunID:  composeACPRunID(response.Info.SessionID, response.Info.ID),
		Status:    status,
		Output:    output,
		Artifacts: artifacts,
	}, nil
}

func parseOpenCodeParts(parts []openCodePart) (string, []domain.Artifact) {
	var textParts []string
	var artifacts []domain.Artifact
	for _, part := range parts {
		switch part.Type {
		case "text", "reasoning":
			if strings.TrimSpace(part.Text) != "" {
				textParts = append(textParts, part.Text)
			}
		case "file":
			artifacts = append(artifacts, domain.Artifact{
				ID:         "artifact_" + part.ID,
				Name:       part.Filename,
				MIMEType:   part.MIME,
				StorageURI: part.URL,
				SourceURL:  part.URL,
			})
		case "tool":
			attachments := parseToolAttachments(part.State)
			artifacts = append(artifacts, attachments...)
		}
	}
	return strings.TrimSpace(strings.Join(textParts, "\n\n")), artifacts
}

func parseToolAttachments(state map[string]any) []domain.Artifact {
	output, ok := state["attachments"].([]any)
	if !ok {
		return nil
	}
	artifacts := make([]domain.Artifact, 0, len(output))
	for _, raw := range output {
		item, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		id, _ := item["id"].(string)
		urlValue, _ := item["url"].(string)
		mime, _ := item["mime"].(string)
		filename, _ := item["filename"].(string)
		if id == "" || urlValue == "" {
			continue
		}
		artifacts = append(artifacts, domain.Artifact{
			ID:         "artifact_" + id,
			Name:       filename,
			MIMEType:   mime,
			StorageURI: urlValue,
			SourceURL:  urlValue,
		})
	}
	return artifacts
}

func mapMessageStatus(message openCodeAssistantMessage) string {
	if message.Error != nil {
		return "failed"
	}
	return "completed"
}

func composeACPRunID(sessionID, messageID string) string {
	return sessionID + ":" + messageID
}

func splitACPRunID(acpRunID string) (string, string, error) {
	parts := strings.SplitN(acpRunID, ":", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", fmt.Errorf("invalid OpenCode run id %q", acpRunID)
	}
	return parts[0], parts[1], nil
}

func resumePayloadText(payload []byte) string {
	if len(bytes.TrimSpace(payload)) == 0 {
		return ""
	}
	var decoded any
	if err := json.Unmarshal(payload, &decoded); err == nil {
		if text, ok := decoded.(string); ok {
			return text
		}
		normalized, err := json.Marshal(decoded)
		if err == nil {
			return string(normalized)
		}
	}
	return string(payload)
}

func (c Client) getJSON(ctx context.Context, path string, query map[string]string, out any) error {
	req, err := c.newRequest(ctx, http.MethodGet, path, query, nil)
	if err != nil {
		return err
	}
	return c.doJSON(req, out)
}

func (c Client) postJSON(ctx context.Context, path string, query map[string]string, body any, out any) error {
	var payload []byte
	var err error
	if body != nil {
		payload, err = json.Marshal(body)
		if err != nil {
			return err
		}
	}
	req, err := c.newRequest(ctx, http.MethodPost, path, query, payload)
	if err != nil {
		return err
	}
	return c.doJSON(req, out)
}

func (c Client) newRequest(ctx context.Context, method, path string, query map[string]string, body []byte) (*http.Request, error) {
	base, err := url.Parse(c.BaseURL)
	if err != nil {
		return nil, err
	}
	rel, err := url.Parse(path)
	if err != nil {
		return nil, err
	}
	full := base.ResolveReference(rel)
	values := full.Query()
	if c.Directory != "" {
		values.Set("directory", c.Directory)
	}
	for key, value := range query {
		if value != "" {
			values.Set(key, value)
		}
	}
	full.RawQuery = values.Encode()
	req, err := http.NewRequestWithContext(ctx, method, full.String(), bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	if len(body) > 0 {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}
	return req, nil
}

func (c Client) doJSON(req *http.Request, out any) error {
	client := c.HTTP
	if client == nil {
		client = &http.Client{Timeout: 60 * time.Second}
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		var body bytes.Buffer
		_, _ = body.ReadFrom(resp.Body)
		return fmt.Errorf("%s %s: status %d: %s", req.Method, req.URL.Path, resp.StatusCode, strings.TrimSpace(body.String()))
	}
	if out == nil {
		return nil
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return err
	}
	return nil
}

func (s *clientState) loadSession(gatewaySessionID string) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	value, ok := s.sessionByGatewayID[gatewaySessionID]
	return value, ok
}

func (s *clientState) storeSession(gatewaySessionID, openCodeSessionID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sessionByGatewayID[gatewaySessionID] = openCodeSessionID
}

func (s *clientState) storeSnapshot(key string, snapshot domain.RunStatusSnapshot) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.snapshotByIdempotency[key] = snapshot
}

func (s *clientState) loadSnapshot(key string) (domain.RunStatusSnapshot, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	snapshot, ok := s.snapshotByIdempotency[key]
	return snapshot, ok
}
