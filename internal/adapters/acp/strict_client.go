package acp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"nexus/internal/domain"
)

type StrictClient struct {
	BaseURL string
	Token   string
	HTTP    *http.Client
}

type strictManifest struct {
	Name                    string   `json:"name"`
	Description             string   `json:"description"`
	InputContentTypes       []string `json:"input_content_types"`
	OutputContentTypes      []string `json:"output_content_types"`
	SupportsAwaitResume     bool     `json:"supports_await_resume"`
	SupportsStructuredAwait bool     `json:"supports_structured_await"`
	SupportsSessionReload   bool     `json:"supports_session_reload"`
	SupportsStreaming       bool     `json:"supports_streaming"`
	SupportsArtifacts       bool     `json:"supports_artifacts"`
	Healthy                 bool     `json:"healthy"`
}

type strictSession struct {
	ID string `json:"id"`
}

type strictRun struct {
	ID        string        `json:"id"`
	SessionID string        `json:"session_id"`
	Status    string        `json:"status"`
	Output    string        `json:"output"`
	Artifacts []strictAsset `json:"artifacts"`
	Await     *strictAwait  `json:"await"`
}

type strictAsset struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	MIMEType   string `json:"mime_type"`
	SizeBytes  int64  `json:"size_bytes"`
	SHA256     string `json:"sha256"`
	StorageURI string `json:"storage_uri"`
	SourceURL  string `json:"source_url"`
}

type strictAwait struct {
	Schema []byte `json:"schema"`
	Prompt []byte `json:"prompt"`
}

func NewStrictClient(baseURL, token string) StrictClient {
	return StrictClient{
		BaseURL: strings.TrimRight(baseURL, "/"),
		Token:   token,
		HTTP:    &http.Client{Timeout: 60 * time.Second},
	}
}

func (c StrictClient) DiscoverAgents(ctx context.Context) ([]domain.AgentManifest, error) {
	var manifests []strictManifest
	if err := c.getJSON(ctx, "/agents", nil, &manifests); err != nil {
		return nil, err
	}
	out := make([]domain.AgentManifest, 0, len(manifests))
	for _, manifest := range manifests {
		if strings.TrimSpace(manifest.Name) == "" {
			return nil, fmt.Errorf("discover agents: agent manifest missing name")
		}
		out = append(out, domain.AgentManifest{
			Name:                    manifest.Name,
			Description:             manifest.Description,
			InputContentTypes:       manifest.InputContentTypes,
			OutputContentTypes:      manifest.OutputContentTypes,
			SupportsAwaitResume:     manifest.SupportsAwaitResume,
			SupportsStructuredAwait: manifest.SupportsStructuredAwait,
			SupportsSessionReload:   manifest.SupportsSessionReload,
			SupportsStreaming:       manifest.SupportsStreaming,
			SupportsArtifacts:       manifest.SupportsArtifacts,
			Healthy:                 manifest.Healthy,
		})
	}
	return out, nil
}

func (c StrictClient) EnsureSession(ctx context.Context, session domain.Session) (string, error) {
	if session.ACPSessionID != "" {
		return session.ACPSessionID, nil
	}
	body := map[string]any{"gateway_session_id": session.ID}
	var created strictSession
	if err := c.postJSON(ctx, "/sessions", nil, body, &created); err != nil {
		return "", err
	}
	if created.ID == "" {
		return "", fmt.Errorf("ensure session: missing session id")
	}
	return created.ID, nil
}

func (c StrictClient) StartRun(ctx context.Context, req domain.StartRunRequest) (domain.Run, domain.RunEventStream, error) {
	sessionID := req.Session.ACPSessionID
	if sessionID == "" {
		var err error
		sessionID, err = c.EnsureSession(ctx, req.Session)
		if err != nil {
			return domain.Run{}, domain.RunEventStream{}, err
		}
	}
	body := map[string]any{
		"session_id":      sessionID,
		"agent_name":      req.RouteDecision.ACPAgentName,
		"idempotency_key": req.IdempotencyKey,
		"message": map[string]any{
			"text":      req.Message.Text,
			"parts":     req.Message.Parts,
			"artifacts": req.Message.Artifacts,
		},
	}
	var response strictRun
	if err := c.postJSON(ctx, "/runs", nil, body, &response); err != nil {
		return domain.Run{}, domain.RunEventStream{}, err
	}
	run, event, err := c.mapRunResponse(req.Session.ID, response)
	if err != nil {
		return domain.Run{}, domain.RunEventStream{}, err
	}
	return run, staticRunEventStream(event), nil
}

func (c StrictClient) ResumeRun(ctx context.Context, await domain.Await, payload []byte) (domain.RunEventStream, error) {
	acpRunID := strings.TrimPrefix(await.RunID, "run_")
	var response strictRun
	if err := c.postJSON(ctx, "/runs/"+url.PathEscape(acpRunID)+"/resume", nil, map[string]any{"payload": json.RawMessage(payload)}, &response); err != nil {
		return domain.RunEventStream{}, err
	}
	_, event, err := c.mapRunResponse(await.SessionID, response)
	if err != nil {
		return domain.RunEventStream{}, err
	}
	return staticRunEventStream(event), nil
}

func (c StrictClient) GetRun(ctx context.Context, acpRunID string) (domain.RunStatusSnapshot, error) {
	var response strictRun
	if err := c.getJSON(ctx, "/runs/"+url.PathEscape(acpRunID), nil, &response); err != nil {
		return domain.RunStatusSnapshot{}, err
	}
	return c.mapSnapshot(response)
}

func (c StrictClient) FindRunByIdempotencyKey(context.Context, domain.Session, string) (domain.RunStatusSnapshot, bool, error) {
	return domain.RunStatusSnapshot{}, false, nil
}

func (c StrictClient) FindLatestRunForSession(ctx context.Context, session domain.Session) (domain.RunStatusSnapshot, bool, error) {
	if session.ACPSessionID == "" {
		return domain.RunStatusSnapshot{}, false, nil
	}
	var runs []strictRun
	if err := c.getJSON(ctx, "/sessions/"+url.PathEscape(session.ACPSessionID)+"/runs", map[string]string{"limit": "20"}, &runs); err != nil {
		return domain.RunStatusSnapshot{}, false, err
	}
	if len(runs) == 0 {
		return domain.RunStatusSnapshot{}, false, nil
	}
	snapshot, err := c.mapSnapshot(runs[0])
	if err != nil {
		return domain.RunStatusSnapshot{}, false, err
	}
	return snapshot, true, nil
}

func (c StrictClient) CancelRun(ctx context.Context, run domain.Run) error {
	return c.postJSON(ctx, "/runs/"+url.PathEscape(run.ACPRunID)+"/cancel", nil, nil, nil)
}

func (c StrictClient) mapRunResponse(sessionID string, response strictRun) (domain.Run, domain.RunEvent, error) {
	if response.ID == "" || response.SessionID == "" || response.Status == "" {
		return domain.Run{}, domain.RunEvent{}, fmt.Errorf("run response missing required fields")
	}
	run := domain.Run{
		ID:        "run_" + response.ID,
		SessionID: sessionID,
		ACPRunID:  response.ID,
		Status:    response.Status,
		StartedAt: time.Now().UTC(),
	}
	event := domain.RunEvent{
		RunID:      run.ID,
		MessageKey: response.ID,
		Status:     response.Status,
		Text:       response.Output,
		Artifacts:  mapStrictArtifacts(response.Artifacts),
	}
	if response.Await != nil {
		event.AwaitSchema = response.Await.Schema
		event.AwaitPrompt = response.Await.Prompt
	}
	return run, event, nil
}

func (c StrictClient) mapSnapshot(response strictRun) (domain.RunStatusSnapshot, error) {
	if response.ID == "" || response.Status == "" {
		return domain.RunStatusSnapshot{}, fmt.Errorf("run snapshot missing required fields")
	}
	snapshot := domain.RunStatusSnapshot{
		ACPRunID:  response.ID,
		Status:    response.Status,
		Output:    response.Output,
		Artifacts: mapStrictArtifacts(response.Artifacts),
	}
	if response.Await != nil {
		snapshot.Await = &domain.AwaitSnapshot{
			Schema: response.Await.Schema,
			Prompt: response.Await.Prompt,
		}
	}
	return snapshot, nil
}

func mapStrictArtifacts(in []strictAsset) []domain.Artifact {
	out := make([]domain.Artifact, 0, len(in))
	for _, item := range in {
		out = append(out, domain.Artifact{
			ID:         item.ID,
			Name:       item.Name,
			MIMEType:   item.MIMEType,
			SizeBytes:  item.SizeBytes,
			SHA256:     item.SHA256,
			StorageURI: item.StorageURI,
			SourceURL:  item.SourceURL,
		})
	}
	return out
}

func (c StrictClient) getJSON(ctx context.Context, path string, query map[string]string, out any) error {
	req, err := c.newRequest(ctx, http.MethodGet, path, query, nil)
	if err != nil {
		return err
	}
	return c.do(req, out)
}

func (c StrictClient) postJSON(ctx context.Context, path string, query map[string]string, body any, out any) error {
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
	return c.do(req, out)
}

func (c StrictClient) newRequest(ctx context.Context, method, path string, query map[string]string, body []byte) (*http.Request, error) {
	u, err := url.Parse(c.BaseURL + path)
	if err != nil {
		return nil, err
	}
	values := u.Query()
	for k, v := range query {
		values.Set(k, v)
	}
	u.RawQuery = values.Encode()
	var reader *bytes.Reader
	if body == nil {
		reader = bytes.NewReader(nil)
	} else {
		reader = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, u.String(), reader)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}
	return req, nil
}

func (c StrictClient) do(req *http.Request, out any) error {
	client := c.HTTP
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return fmt.Errorf("acp request failed: %s", resp.Status)
	}
	if out == nil {
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
}
