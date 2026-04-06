package acp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"nexus/internal/domain"
)

type Client struct {
	BaseURL string
	Token   string
	HTTP    *http.Client
}

func New(baseURL, token string) Client {
	return Client{
		BaseURL: strings.TrimRight(baseURL, "/"),
		Token:   token,
		HTTP:    &http.Client{Timeout: 15 * time.Second},
	}
}

func (c Client) DiscoverAgents(ctx context.Context) ([]domain.AgentManifest, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.BaseURL+"/agents", nil)
	if err != nil {
		return nil, err
	}
	if c.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("acp discover agents failed: status=%d body=%s", resp.StatusCode, string(raw))
	}
	var parsed struct {
		Agents []struct {
			Name               string   `json:"name"`
			Description        string   `json:"description"`
			InputContentTypes  []string `json:"input_content_types"`
			OutputContentTypes []string `json:"output_content_types"`
			Capabilities       struct {
				AwaitResume bool `json:"await_resume"`
				Streaming   bool `json:"streaming"`
				Artifacts   bool `json:"artifacts"`
			} `json:"capabilities"`
			Healthy bool `json:"healthy"`
		} `json:"agents"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return nil, err
	}
	manifests := make([]domain.AgentManifest, 0, len(parsed.Agents))
	for _, agent := range parsed.Agents {
		if strings.TrimSpace(agent.Name) == "" {
			return nil, fmt.Errorf("acp discover agents returned agent with missing name")
		}
		manifests = append(manifests, domain.AgentManifest{
			Name:                agent.Name,
			Description:         agent.Description,
			InputContentTypes:   agent.InputContentTypes,
			OutputContentTypes:  agent.OutputContentTypes,
			SupportsAwaitResume: agent.Capabilities.AwaitResume,
			SupportsStreaming:   agent.Capabilities.Streaming,
			SupportsArtifacts:   agent.Capabilities.Artifacts,
			Healthy:             agent.Healthy,
		})
	}
	return manifests, nil
}

func (c Client) EnsureSession(_ context.Context, session domain.Session) (string, error) {
	if session.ACPSessionID != "" {
		return session.ACPSessionID, nil
	}
	return "acp_" + session.ID, nil
}

func (c Client) StartRun(ctx context.Context, req domain.StartRunRequest) (domain.Run, []domain.RunEvent, error) {
	payload := map[string]any{
		"session_id": req.Session.ACPSessionID,
		"agent_name": req.RouteDecision.ACPAgentName,
		"text":       req.Message.Text,
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return domain.Run{}, nil, err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+"/runs", bytes.NewReader(body))
	if err != nil {
		return domain.Run{}, nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if c.Token != "" {
		httpReq.Header.Set("Authorization", "Bearer "+c.Token)
	}
	resp, err := c.HTTP.Do(httpReq)
	if err != nil {
		return domain.Run{}, nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return domain.Run{}, nil, fmt.Errorf("acp start run failed: status=%d body=%s", resp.StatusCode, string(raw))
	}
	var parsed struct {
		RunID     string `json:"run_id"`
		Status    string `json:"status"`
		Output    string `json:"output"`
		Artifacts []struct {
			ID         string `json:"id"`
			Name       string `json:"name"`
			MimeType   string `json:"mime_type"`
			StorageURI string `json:"storage_uri"`
		} `json:"artifacts"`
		Await struct {
			Schema json.RawMessage `json:"schema"`
			Prompt json.RawMessage `json:"prompt"`
		} `json:"await"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return domain.Run{}, nil, err
	}
	if strings.TrimSpace(parsed.RunID) == "" || strings.TrimSpace(parsed.Status) == "" {
		return domain.Run{}, nil, fmt.Errorf("acp start run response missing required fields")
	}
	run := domain.Run{
		ID:              "run_" + parsed.RunID,
		SessionID:       req.Session.ID,
		ACPConnectionID: req.RouteDecision.ACPConnectionID,
		ACPRunID:        parsed.RunID,
		Status:          parsed.Status,
		StartedAt:       time.Now().UTC(),
		LastEventAt:     time.Now().UTC(),
	}
	return run, []domain.RunEvent{toRunEvent(run.ID, parsed.Status, parsed.Output, parsedArtifacts(parsed.Artifacts), parsed.Await.Schema, parsed.Await.Prompt)}, nil
}

func (c Client) ResumeRun(ctx context.Context, await domain.Await, payload []byte) ([]domain.RunEvent, error) {
	body, err := json.Marshal(map[string]any{
		"await_id": await.ID,
		"payload":  json.RawMessage(payload),
	})
	if err != nil {
		return nil, err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+"/runs/"+await.RunID+"/resume", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if c.Token != "" {
		httpReq.Header.Set("Authorization", "Bearer "+c.Token)
	}
	resp, err := c.HTTP.Do(httpReq)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("acp resume failed: status=%d body=%s", resp.StatusCode, string(raw))
	}
	var parsed struct {
		Status    string `json:"status"`
		Output    string `json:"output"`
		Artifacts []struct {
			ID         string `json:"id"`
			Name       string `json:"name"`
			MimeType   string `json:"mime_type"`
			StorageURI string `json:"storage_uri"`
		} `json:"artifacts"`
		Await struct {
			Schema json.RawMessage `json:"schema"`
			Prompt json.RawMessage `json:"prompt"`
		} `json:"await"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return nil, err
	}
	if strings.TrimSpace(parsed.Status) == "" {
		return nil, fmt.Errorf("acp resume response missing required fields")
	}
	return []domain.RunEvent{toRunEvent(await.RunID, parsed.Status, parsed.Output, parsedArtifacts(parsed.Artifacts), parsed.Await.Schema, parsed.Await.Prompt)}, nil
}

func (c Client) GetRun(ctx context.Context, acpRunID string) (domain.RunStatusSnapshot, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.BaseURL+"/runs/"+acpRunID, nil)
	if err != nil {
		return domain.RunStatusSnapshot{}, err
	}
	if c.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return domain.RunStatusSnapshot{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return domain.RunStatusSnapshot{}, fmt.Errorf("acp get run failed: status=%d body=%s", resp.StatusCode, string(raw))
	}
	return decodeRunSnapshot(resp.Body)
}

func (c Client) FindRunByIdempotencyKey(ctx context.Context, key string) (domain.RunStatusSnapshot, bool, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.BaseURL+"/runs?idempotency_key="+url.QueryEscape(key), nil)
	if err != nil {
		return domain.RunStatusSnapshot{}, false, err
	}
	if c.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return domain.RunStatusSnapshot{}, false, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return domain.RunStatusSnapshot{}, false, nil
	}
	if resp.StatusCode >= 300 {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return domain.RunStatusSnapshot{}, false, fmt.Errorf("acp find run failed: status=%d body=%s", resp.StatusCode, string(raw))
	}
	snapshot, err := decodeRunSnapshot(resp.Body)
	if err != nil {
		return domain.RunStatusSnapshot{}, false, err
	}
	return snapshot, true, nil
}

func (c Client) CancelRun(_ context.Context, _ domain.Run) error {
	return nil
}

func decodeRunSnapshot(body io.Reader) (domain.RunStatusSnapshot, error) {
	var parsed struct {
		RunID     string `json:"run_id"`
		Status    string `json:"status"`
		Output    string `json:"output"`
		Artifacts []struct {
			ID         string `json:"id"`
			Name       string `json:"name"`
			MimeType   string `json:"mime_type"`
			StorageURI string `json:"storage_uri"`
		} `json:"artifacts"`
		Await *struct {
			Schema json.RawMessage `json:"schema"`
			Prompt json.RawMessage `json:"prompt"`
		} `json:"await"`
	}
	if err := json.NewDecoder(body).Decode(&parsed); err != nil {
		return domain.RunStatusSnapshot{}, err
	}
	if parsed.RunID == "" || parsed.Status == "" {
		return domain.RunStatusSnapshot{}, fmt.Errorf("acp run snapshot missing required fields")
	}
	snapshot := domain.RunStatusSnapshot{
		ACPRunID:  parsed.RunID,
		Status:    normalizeStatus(parsed.Status),
		Output:    parsed.Output,
		Artifacts: parsedArtifacts(parsed.Artifacts),
	}
	if parsed.Await != nil {
		snapshot.Await = &domain.AwaitSnapshot{Schema: parsed.Await.Schema, Prompt: parsed.Await.Prompt}
	}
	return snapshot, nil
}

func normalizeStatus(in string) string {
	switch in {
	case "created", "accepted":
		return "running"
	case "in-progress":
		return "running"
	case "awaiting":
		return "awaiting"
	case "done":
		return "completed"
	default:
		if in == "" {
			return "completed"
		}
		return in
	}
}

func toRunEvent(runID, status, output string, artifacts []domain.Artifact, schema, prompt json.RawMessage) domain.RunEvent {
	return domain.RunEvent{
		RunID:       runID,
		Status:      normalizeStatus(status),
		Text:        output,
		Artifacts:   artifacts,
		AwaitSchema: schema,
		AwaitPrompt: prompt,
	}
}

func parsedArtifacts(items []struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	MimeType   string `json:"mime_type"`
	StorageURI string `json:"storage_uri"`
}) []domain.Artifact {
	out := make([]domain.Artifact, 0, len(items))
	for _, item := range items {
		out = append(out, domain.Artifact{
			ID:         item.ID,
			Name:       item.Name,
			MIMEType:   item.MimeType,
			StorageURI: item.StorageURI,
		})
	}
	return out
}
