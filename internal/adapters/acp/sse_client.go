package acp

import (
	"bufio"
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

type SSEClient struct {
	BaseURL    string
	Token      string
	HTTP       *http.Client
	StreamHTTP *http.Client
	Headers    map[string]string
}

func NewSSEClient(baseURL, token string) SSEClient {
	return SSEClient{
		BaseURL: strings.TrimRight(baseURL, "/"),
		Token:   token,
		HTTP:    &http.Client{Timeout: 60 * time.Second},
	}
}

func (c SSEClient) strict() StrictClient {
	return StrictClient{
		BaseURL: c.BaseURL,
		Token:   c.Token,
		HTTP:    c.HTTP,
		Headers: c.Headers,
	}
}

func (c SSEClient) DiscoverAgents(ctx context.Context) ([]domain.AgentManifest, error) {
	agents, err := c.strict().DiscoverAgents(ctx)
	if err != nil {
		return nil, err
	}
	for i := range agents {
		agents[i].SupportsStreaming = true
	}
	return agents, nil
}

func (c SSEClient) EnsureSession(ctx context.Context, session domain.Session) (string, error) {
	return c.strict().EnsureSession(ctx, session)
}

func (c SSEClient) StartRun(ctx context.Context, req domain.StartRunRequest) (domain.Run, domain.RunEventStream, error) {
	strict := c.strict()
	sessionID, err := strict.EnsureSession(ctx, req.Session)
	if err != nil {
		return domain.Run{}, domain.RunEventStream{}, err
	}
	parts := strictMessageParts(req.Message.Parts)
	parts = appendEmailContextPart(parts, req.Session, req.Message)
	parts = appendArtifactRefParts(parts, req.Message.Artifacts)
	body := map[string]any{
		"session_id":      sessionID,
		"agent_name":      req.RouteDecision.ACPAgentName,
		"idempotency_key": req.IdempotencyKey,
		"text":            req.Message.Text,
		"parts":           parts,
		"artifacts":       req.Message.Artifacts,
		"message": map[string]any{
			"text":      req.Message.Text,
			"parts":     parts,
			"artifacts": req.Message.Artifacts,
		},
	}
	if replyTo := compactReplyToFromRawPayload(req.Message.RawPayload); len(replyTo) > 0 {
		body["reply_to"] = replyTo
	}
	var response strictRun
	if err := strict.postJSON(ctx, "/runs", nil, body, &response, sessionHeaders(req.Session, req.IdempotencyKey, "")); err != nil {
		return domain.Run{}, domain.RunEventStream{}, err
	}
	run, _, err := strict.mapRunResponse(req.Session.ID, response)
	if err != nil {
		return domain.Run{}, domain.RunEventStream{}, err
	}
	run.ACPAgentName = req.RouteDecision.ACPAgentName
	stream, err := c.streamRunEvents(ctx, req.Session, run)
	if err != nil {
		return domain.Run{}, domain.RunEventStream{}, err
	}
	return run, stream, nil
}

func (c SSEClient) ResumeRun(ctx context.Context, await domain.Await, payload []byte) (domain.RunEventStream, error) {
	acpRunID := strings.TrimPrefix(await.RunID, "run_")
	if err := c.strict().postJSON(ctx, "/runs/"+url.PathEscape(acpRunID)+"/resume", nil, map[string]any{"payload": json.RawMessage(payload)}, nil, map[string]string{"X-Run-ID": await.RunID}); err != nil {
		return domain.RunEventStream{}, err
	}
	return c.streamRunEvents(ctx, domain.Session{ID: await.SessionID}, domain.Run{ID: await.RunID, SessionID: await.SessionID, ACPRunID: acpRunID})
}

func (c SSEClient) ResumeRunForSession(ctx context.Context, session domain.Session, await domain.Await, payload []byte) (domain.RunEventStream, error) {
	acpRunID := strings.TrimPrefix(await.RunID, "run_")
	if err := c.strict().postJSON(ctx, "/runs/"+url.PathEscape(acpRunID)+"/resume", nil, map[string]any{"payload": json.RawMessage(payload)}, nil, sessionHeaders(session, "", acpRunID)); err != nil {
		return domain.RunEventStream{}, err
	}
	return c.streamRunEvents(ctx, session, domain.Run{ID: await.RunID, SessionID: await.SessionID, ACPRunID: acpRunID})
}

func (c SSEClient) GetRun(ctx context.Context, acpRunID string) (domain.RunStatusSnapshot, error) {
	return c.strict().GetRun(ctx, acpRunID)
}

func (c SSEClient) GetRunForSession(ctx context.Context, session domain.Session, acpRunID string) (domain.RunStatusSnapshot, error) {
	return c.strict().GetRunForSession(ctx, session, acpRunID)
}

func (c SSEClient) ListVisibleEvents(ctx context.Context, session domain.Session, minOffset int64) ([]domain.VisibleSessionEvent, error) {
	return c.strict().ListVisibleEvents(ctx, session, minOffset)
}

func (c SSEClient) FindRunByIdempotencyKey(ctx context.Context, session domain.Session, idempotencyKey string) (domain.RunStatusSnapshot, bool, error) {
	return c.strict().FindRunByIdempotencyKey(ctx, session, idempotencyKey)
}

func (c SSEClient) FindLatestRunForSession(ctx context.Context, session domain.Session) (domain.RunStatusSnapshot, bool, error) {
	return c.strict().FindLatestRunForSession(ctx, session)
}

func (c SSEClient) CancelRun(ctx context.Context, run domain.Run) error {
	return c.strict().CancelRun(ctx, run)
}

func (c SSEClient) streamRunEvents(ctx context.Context, session domain.Session, run domain.Run) (domain.RunEventStream, error) {
	req, err := c.newRequest(ctx, http.MethodGet, "/runs/"+url.PathEscape(run.ACPRunID)+"/events", nil, nil)
	if err != nil {
		return domain.RunEventStream{}, err
	}
	for key, value := range sessionHeaders(session, "", run.ACPRunID) {
		if strings.TrimSpace(value) != "" {
			req.Header.Set(key, value)
		}
	}
	resp, err := c.streamHTTPClient().Do(req)
	if err != nil {
		return domain.RunEventStream{}, err
	}
	if resp.StatusCode >= 400 {
		defer resp.Body.Close()
		return domain.RunEventStream{}, fmt.Errorf("acp sse request failed: %s", resp.Status)
	}
	eventCh := make(chan domain.RunEvent)
	errCh := make(chan error, 1)
	go func() {
		defer close(eventCh)
		defer close(errCh)
		defer resp.Body.Close()
		errCh <- c.readRunEvents(resp.Body, run, eventCh)
	}()
	return domain.RunEventStream{Events: eventCh, Err: errCh}, nil
}

func (c SSEClient) readRunEvents(body io.Reader, run domain.Run, eventCh chan<- domain.RunEvent) error {
	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 1024), 1024*1024)
	var data bytes.Buffer
	flush := func() error {
		if data.Len() == 0 {
			return nil
		}
		payload := strings.TrimSpace(data.String())
		data.Reset()
		if payload == "" {
			return nil
		}
		event, err := c.mapSSEPayload(run, []byte(payload))
		if err != nil {
			return err
		}
		if event.Status == "" && strings.TrimSpace(event.Text) == "" && len(event.Artifacts) == 0 && event.AwaitSchema == nil {
			return nil
		}
		eventCh <- event
		return nil
	}
	for scanner.Scan() {
		line := strings.TrimSuffix(scanner.Text(), "\r")
		if strings.TrimSpace(line) == "" {
			if err := flush(); err != nil {
				return err
			}
			continue
		}
		if strings.HasPrefix(line, ":") || strings.HasPrefix(line, "event:") || strings.HasPrefix(line, "id:") || strings.HasPrefix(line, "retry:") {
			continue
		}
		if strings.HasPrefix(line, "data:") {
			if data.Len() > 0 {
				data.WriteByte('\n')
			}
			data.WriteString(strings.TrimSpace(strings.TrimPrefix(line, "data:")))
		}
	}
	if err := scanner.Err(); err != nil {
		return err
	}
	return flush()
}

func (c SSEClient) mapSSEPayload(run domain.Run, payload []byte) (domain.RunEvent, error) {
	var response strictRun
	if err := json.Unmarshal(payload, &response); err != nil {
		return domain.RunEvent{}, fmt.Errorf("decode acp sse event: %w", err)
	}
	var streaming struct {
		Partial   bool `json:"partial"`
		IsPartial bool `json:"is_partial"`
	}
	_ = json.Unmarshal(payload, &streaming)
	if response.ID == "" {
		response.ID = run.ACPRunID
	}
	if response.SessionID == "" {
		response.SessionID = run.SessionID
	}
	if response.Status == "" {
		response.Status = response.State
	}
	if response.Output == "" {
		var fallback struct {
			Text string `json:"text"`
		}
		_ = json.Unmarshal(payload, &fallback)
		response.Output = fallback.Text
	}
	_, event, err := c.strict().mapRunResponse(run.SessionID, response)
	if err != nil {
		return domain.RunEvent{}, err
	}
	event.RunID = run.ID
	event.IsPartial = streaming.Partial || streaming.IsPartial
	return event, nil
}

func (c SSEClient) newRequest(ctx context.Context, method, path string, query map[string]string, body []byte) (*http.Request, error) {
	u, err := url.Parse(c.BaseURL + path)
	if err != nil {
		return nil, err
	}
	values := u.Query()
	for k, v := range query {
		values.Set(k, v)
	}
	u.RawQuery = values.Encode()
	req, err := http.NewRequestWithContext(ctx, method, u.String(), bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "text/event-stream")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}
	for key, value := range c.Headers {
		if strings.TrimSpace(key) != "" && strings.TrimSpace(value) != "" {
			req.Header.Set(key, value)
		}
	}
	return req, nil
}

func (c SSEClient) httpClient() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return http.DefaultClient
}

func (c SSEClient) streamHTTPClient() *http.Client {
	if c.StreamHTTP != nil {
		return c.StreamHTTP
	}
	return c.httpClient()
}
