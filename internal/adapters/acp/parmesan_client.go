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

type ParmesanClient struct {
	BaseURL     string
	Token       string
	HTTP        *http.Client
	WaitTimeout time.Duration
	PollEvery   time.Duration
}

type parmesanAgentProfile struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Status      string `json:"status"`
}

type parmesanSession struct {
	ID      string                  `json:"id"`
	Channel string                  `json:"channel"`
	Summary *parmesanSessionSummary `json:"summary,omitempty"`
}

type parmesanSessionSummary struct {
	LastExecutionID string `json:"last_execution_id"`
}

type parmesanExecutionEnvelope struct {
	Execution parmesanExecution `json:"execution"`
}

type parmesanExecution struct {
	ID            string `json:"id"`
	SessionID     string `json:"session_id"`
	Status        string `json:"status"`
	BlockedReason string `json:"blocked_reason,omitempty"`
}

type parmesanEvent struct {
	ID          string                `json:"id"`
	SessionID   string                `json:"session_id"`
	Source      string                `json:"source"`
	Kind        string                `json:"kind"`
	Offset      int64                 `json:"offset"`
	ExecutionID string                `json:"execution_id"`
	Content     []parmesanContentPart `json:"content,omitempty"`
	Data        map[string]any        `json:"data,omitempty"`
	Metadata    map[string]any        `json:"metadata,omitempty"`
}

type parmesanContentPart struct {
	Type string `json:"type"`
	Text string `json:"text"`
	URL  string `json:"url"`
}

type parmesanApproval struct {
	ID          string `json:"id"`
	SessionID   string `json:"session_id"`
	ExecutionID string `json:"execution_id"`
	ToolID      string `json:"tool_id"`
	Status      string `json:"status"`
	RequestText string `json:"request_text"`
	Decision    string `json:"decision,omitempty"`
}

type parmesanAwaitPrompt struct {
	Title      string                `json:"title"`
	Body       string                `json:"body,omitempty"`
	Choices    []domain.RenderChoice `json:"choices,omitempty"`
	ApprovalID string                `json:"approval_id,omitempty"`
	ToolID     string                `json:"tool_id,omitempty"`
}

func NewParmesanClient(baseURL, token string, waitTimeout time.Duration) ParmesanClient {
	if waitTimeout <= 0 {
		waitTimeout = 15 * time.Second
	}
	return ParmesanClient{
		BaseURL:     strings.TrimRight(baseURL, "/"),
		Token:       token,
		HTTP:        &http.Client{Timeout: 60 * time.Second},
		WaitTimeout: waitTimeout,
		PollEvery:   100 * time.Millisecond,
	}
}

func (c ParmesanClient) DiscoverAgents(ctx context.Context) ([]domain.AgentManifest, error) {
	var profiles []parmesanAgentProfile
	if err := c.getJSON(ctx, "/v1/operator/agents", nil, &profiles); err != nil {
		return nil, err
	}
	out := make([]domain.AgentManifest, 0, len(profiles))
	for _, profile := range profiles {
		if strings.TrimSpace(profile.ID) == "" {
			return nil, fmt.Errorf("discover agents: profile missing id")
		}
		healthy := !strings.EqualFold(strings.TrimSpace(profile.Status), "disabled") && !strings.EqualFold(strings.TrimSpace(profile.Status), "retired")
		description := strings.TrimSpace(profile.Description)
		if description == "" {
			description = strings.TrimSpace(profile.Name)
		}
		out = append(out, domain.AgentManifest{
			Name:                    profile.ID,
			Description:             description,
			Protocol:                "acp",
			InputContentTypes:       []string{"text/plain", "application/json"},
			OutputContentTypes:      []string{"text/plain", "application/json"},
			SupportsAwaitResume:     true,
			SupportsStructuredAwait: true,
			SupportsSessionReload:   true,
			SupportsStreaming:       true,
			SupportsArtifacts:       true,
			Healthy:                 healthy,
		})
	}
	return out, nil
}

func (c ParmesanClient) EnsureSession(ctx context.Context, session domain.Session) (string, error) {
	if strings.TrimSpace(session.ACPSessionID) != "" {
		return session.ACPSessionID, nil
	}
	sessionID := strings.TrimSpace(session.ID)
	if sessionID == "" {
		return "", fmt.Errorf("ensure session: missing session id")
	}
	var existing parmesanSession
	err := c.getJSON(ctx, "/v1/acp/sessions/"+url.PathEscape(sessionID), nil, &existing)
	if err == nil {
		return existing.ID, nil
	}
	var reqErr httpStatusError
	if err != nil && (!errorAsStatus(err, &reqErr) || reqErr.StatusCode != http.StatusNotFound) {
		return "", err
	}
	body := map[string]any{
		"id":          sessionID,
		"channel":     firstNonEmpty(session.ChannelType, "acp"),
		"customer_id": session.OwnerUserID,
		"agent_id":    session.AgentProfileID,
		"metadata": map[string]any{
			"gateway_session_id":    session.ID,
			"channel_scope_key":     session.ChannelScopeKey,
			"channel_type":          session.ChannelType,
			"bridge_implementation": "parmesan",
		},
	}
	var created parmesanSession
	if err := c.postJSON(ctx, "/v1/acp/sessions", nil, body, &created); err != nil {
		return "", err
	}
	if created.ID == "" {
		return "", fmt.Errorf("ensure session: missing session id")
	}
	return created.ID, nil
}

func (c ParmesanClient) StartRun(ctx context.Context, req domain.StartRunRequest) (domain.Run, []domain.RunEvent, error) {
	sessionID := req.Session.ACPSessionID
	if sessionID == "" {
		var err error
		sessionID, err = c.EnsureSession(ctx, req.Session)
		if err != nil {
			return domain.Run{}, nil, err
		}
	}
	messageID := strings.TrimSpace(req.Message.MessageID)
	if messageID == "" {
		messageID = "msg_" + strings.TrimPrefix(req.IdempotencyKey, "queue_")
		if messageID == "msg_" {
			messageID = fmt.Sprintf("msg_%d", time.Now().UTC().UnixNano())
		}
	}
	body := map[string]any{
		"id":     messageID,
		"source": "customer",
		"text":   firstNonEmpty(joinMessageText(req.Message), req.Message.Text),
		"metadata": map[string]any{
			"idempotency_key":    req.IdempotencyKey,
			"gateway_message_id": req.Message.MessageID,
		},
	}
	var created parmesanEvent
	if err := c.postJSON(ctx, "/v1/acp/sessions/"+url.PathEscape(sessionID)+"/messages", nil, body, &created); err != nil {
		return domain.Run{}, nil, err
	}
	if strings.TrimSpace(created.ExecutionID) == "" {
		return domain.Run{}, nil, fmt.Errorf("start run: missing execution id")
	}
	run := domain.Run{
		ID:              "run_" + created.ExecutionID,
		SessionID:       req.Session.ID,
		ACPConnectionID: "parmesan",
		ACPRunID:        created.ExecutionID,
		Status:          "running",
		StartedAt:       time.Now().UTC(),
		LastEventAt:     time.Now().UTC(),
	}
	snapshot, err := c.waitForSnapshot(ctx, sessionID, created.ExecutionID, created.Offset+1)
	if err != nil {
		return domain.Run{}, nil, err
	}
	run.Status = snapshot.Status
	events := []domain.RunEvent{{
		RunID:     run.ID,
		Status:    snapshot.Status,
		Text:      snapshot.Output,
		Artifacts: snapshot.Artifacts,
	}}
	if snapshot.Await != nil {
		events[0].AwaitSchema = snapshot.Await.Schema
		events[0].AwaitPrompt = snapshot.Await.Prompt
	}
	return run, events, nil
}

func (c ParmesanClient) ResumeRun(ctx context.Context, await domain.Await, payload []byte) ([]domain.RunEvent, error) {
	approvalID, err := approvalIDFromPrompt(await.PromptRenderJSON)
	if err != nil {
		return nil, err
	}
	decision := approvalDecisionFromPayload(payload)
	if decision == "" {
		return nil, fmt.Errorf("resume run: unsupported approval payload")
	}
	if err := c.postJSON(ctx, "/v1/acp/sessions/"+url.PathEscape(await.SessionID)+"/approvals/"+url.PathEscape(approvalID), nil, map[string]any{"decision": decision}, nil); err != nil {
		return nil, err
	}
	execID := strings.TrimPrefix(await.RunID, "run_")
	snapshot, err := c.waitForSnapshot(ctx, await.SessionID, execID, 0)
	if err != nil {
		return nil, err
	}
	events := []domain.RunEvent{{
		RunID:     await.RunID,
		Status:    snapshot.Status,
		Text:      snapshot.Output,
		Artifacts: snapshot.Artifacts,
	}}
	if snapshot.Await != nil {
		events[0].AwaitSchema = snapshot.Await.Schema
		events[0].AwaitPrompt = snapshot.Await.Prompt
	}
	return events, nil
}

func (c ParmesanClient) GetRun(ctx context.Context, acpRunID string) (domain.RunStatusSnapshot, error) {
	exec, err := c.getExecution(ctx, acpRunID)
	if err != nil {
		return domain.RunStatusSnapshot{}, err
	}
	return c.snapshotForExecution(ctx, exec.SessionID, exec, 0)
}

func (c ParmesanClient) FindRunByIdempotencyKey(ctx context.Context, session domain.Session, idempotencyKey string) (domain.RunStatusSnapshot, bool, error) {
	if strings.TrimSpace(session.ACPSessionID) == "" || strings.TrimSpace(idempotencyKey) == "" {
		return domain.RunStatusSnapshot{}, false, nil
	}
	events, err := c.listEvents(ctx, session.ACPSessionID, 0)
	if err != nil {
		return domain.RunStatusSnapshot{}, false, err
	}
	for _, event := range events {
		if strings.TrimSpace(event.ExecutionID) == "" || event.Kind != "message" {
			continue
		}
		if textValue(event.Metadata, "idempotency_key") != idempotencyKey {
			continue
		}
		snapshot, err := c.GetRun(ctx, event.ExecutionID)
		if err != nil {
			return domain.RunStatusSnapshot{}, false, err
		}
		return snapshot, true, nil
	}
	return domain.RunStatusSnapshot{}, false, nil
}

func (c ParmesanClient) FindLatestRunForSession(ctx context.Context, session domain.Session) (domain.RunStatusSnapshot, bool, error) {
	if strings.TrimSpace(session.ACPSessionID) == "" {
		return domain.RunStatusSnapshot{}, false, nil
	}
	var current parmesanSession
	if err := c.getJSON(ctx, "/v1/acp/sessions/"+url.PathEscape(session.ACPSessionID), nil, &current); err != nil {
		return domain.RunStatusSnapshot{}, false, err
	}
	if current.Summary == nil || strings.TrimSpace(current.Summary.LastExecutionID) == "" {
		return domain.RunStatusSnapshot{}, false, nil
	}
	snapshot, err := c.GetRun(ctx, current.Summary.LastExecutionID)
	if err != nil {
		return domain.RunStatusSnapshot{}, false, err
	}
	return snapshot, true, nil
}

func (c ParmesanClient) CancelRun(ctx context.Context, run domain.Run) error {
	return c.postJSON(ctx, "/v1/operator/executions/"+url.PathEscape(run.ACPRunID)+"/abandon", nil, nil, nil)
}

func (c ParmesanClient) waitForSnapshot(ctx context.Context, sessionID, execID string, minOffset int64) (domain.RunStatusSnapshot, error) {
	deadline := time.Now().UTC().Add(c.WaitTimeout)
	if dl, ok := ctx.Deadline(); ok && dl.Before(deadline) {
		deadline = dl
	}
	for {
		exec, err := c.getExecution(ctx, execID)
		if err != nil {
			return domain.RunStatusSnapshot{}, err
		}
		snapshot, err := c.snapshotForExecution(ctx, sessionID, exec, minOffset)
		if err != nil {
			return domain.RunStatusSnapshot{}, err
		}
		if snapshot.Status == "completed" || snapshot.Status == "failed" || snapshot.Status == "canceled" || snapshot.Status == "awaiting" {
			return snapshot, nil
		}
		if time.Now().UTC().After(deadline) {
			return snapshot, nil
		}
		select {
		case <-ctx.Done():
			return domain.RunStatusSnapshot{}, ctx.Err()
		case <-time.After(c.pollEvery()):
		}
	}
}

func (c ParmesanClient) snapshotForExecution(ctx context.Context, sessionID string, exec parmesanExecution, minOffset int64) (domain.RunStatusSnapshot, error) {
	events, err := c.listEvents(ctx, sessionID, minOffset)
	if err != nil {
		return domain.RunStatusSnapshot{}, err
	}
	var outputParts []string
	var responseDelivered bool
	for _, event := range events {
		if event.ExecutionID != exec.ID {
			continue
		}
		switch event.Kind {
		case "message":
			if strings.EqualFold(strings.TrimSpace(event.Source), "ai_agent") {
				for _, part := range event.Content {
					if strings.TrimSpace(part.Text) != "" {
						outputParts = append(outputParts, strings.TrimSpace(part.Text))
					}
				}
			}
		case "status":
			if textValue(event.Data, "code") == "response.delivered" || textValue(event.Data, "code") == "response.composed" {
				responseDelivered = true
			}
		case "approval.requested":
			prompt := buildApprovalAwaitPrompt(event)
			schema := []byte(`{"type":"object","properties":{"choice":{"type":"string","enum":["approve","reject"]}},"required":["choice"]}`)
			return domain.RunStatusSnapshot{
				ACPRunID: exec.ID,
				Status:   "awaiting",
				Output:   textValue(event.Data, "message"),
				Await: &domain.AwaitSnapshot{
					Schema: schema,
					Prompt: prompt,
				},
			}, nil
		}
	}
	switch exec.Status {
	case "succeeded":
		return domain.RunStatusSnapshot{
			ACPRunID: exec.ID,
			Status:   "completed",
			Output:   strings.Join(outputParts, "\n\n"),
		}, nil
	case "failed", "abandoned":
		return domain.RunStatusSnapshot{
			ACPRunID: exec.ID,
			Status:   "failed",
			Output:   firstNonEmpty(strings.Join(outputParts, "\n\n"), exec.BlockedReason),
		}, nil
	case "blocked":
		if strings.TrimSpace(exec.BlockedReason) == "approval_required" {
			approval, ok, err := c.pendingApproval(ctx, sessionID, exec.ID)
			if err != nil {
				return domain.RunStatusSnapshot{}, err
			}
			if ok {
				return domain.RunStatusSnapshot{
					ACPRunID: exec.ID,
					Status:   "awaiting",
					Output:   approval.RequestText,
					Await: &domain.AwaitSnapshot{
						Schema: []byte(`{"type":"object","properties":{"choice":{"type":"string","enum":["approve","reject"]}},"required":["choice"]}`),
						Prompt: marshalJSON(parmesanAwaitPrompt{
							Title:      firstNonEmpty(approval.RequestText, "Approval required"),
							Choices:    []domain.RenderChoice{{ID: "approve", Label: "Approve"}, {ID: "reject", Label: "Reject"}},
							ApprovalID: approval.ID,
							ToolID:     approval.ToolID,
						}),
					},
				}, nil
			}
		}
		return domain.RunStatusSnapshot{
			ACPRunID: exec.ID,
			Status:   "failed",
			Output:   firstNonEmpty(strings.Join(outputParts, "\n\n"), exec.BlockedReason),
		}, nil
	case "pending", "running", "waiting":
		status := "running"
		if responseDelivered && len(outputParts) > 0 {
			status = "completed"
		}
		return domain.RunStatusSnapshot{
			ACPRunID: exec.ID,
			Status:   status,
			Output:   strings.Join(outputParts, "\n\n"),
		}, nil
	default:
		return domain.RunStatusSnapshot{
			ACPRunID: exec.ID,
			Status:   "running",
			Output:   strings.Join(outputParts, "\n\n"),
		}, nil
	}
}

func (c ParmesanClient) pendingApproval(ctx context.Context, sessionID, execID string) (parmesanApproval, bool, error) {
	var items []parmesanApproval
	if err := c.getJSON(ctx, "/v1/acp/sessions/"+url.PathEscape(sessionID)+"/approvals", map[string]string{"status": "pending"}, &items); err != nil {
		return parmesanApproval{}, false, err
	}
	for _, item := range items {
		if item.ExecutionID == execID && strings.EqualFold(strings.TrimSpace(item.Status), "pending") {
			return item, true, nil
		}
	}
	return parmesanApproval{}, false, nil
}

func (c ParmesanClient) getExecution(ctx context.Context, execID string) (parmesanExecution, error) {
	var envelope parmesanExecutionEnvelope
	if err := c.getJSON(ctx, "/v1/executions/"+url.PathEscape(execID), nil, &envelope); err != nil {
		return parmesanExecution{}, err
	}
	if strings.TrimSpace(envelope.Execution.ID) == "" {
		return parmesanExecution{}, fmt.Errorf("execution payload missing execution")
	}
	return envelope.Execution, nil
}

func (c ParmesanClient) listEvents(ctx context.Context, sessionID string, minOffset int64) ([]parmesanEvent, error) {
	query := map[string]string{}
	if minOffset > 0 {
		query["min_offset"] = fmt.Sprintf("%d", minOffset)
	}
	var events []parmesanEvent
	if err := c.getJSON(ctx, "/v1/acp/sessions/"+url.PathEscape(sessionID)+"/events", query, &events); err != nil {
		return nil, err
	}
	return events, nil
}

func buildApprovalAwaitPrompt(event parmesanEvent) []byte {
	return marshalJSON(parmesanAwaitPrompt{
		Title:      firstNonEmpty(textValue(event.Data, "message"), "Approval required"),
		Choices:    []domain.RenderChoice{{ID: "approve", Label: "Approve"}, {ID: "reject", Label: "Reject"}},
		ApprovalID: textValue(event.Data, "approval_id"),
		ToolID:     textValue(event.Data, "tool_id"),
	})
}

func approvalIDFromPrompt(prompt []byte) (string, error) {
	var parsed parmesanAwaitPrompt
	if err := json.Unmarshal(prompt, &parsed); err != nil {
		return "", fmt.Errorf("decode await prompt: %w", err)
	}
	if strings.TrimSpace(parsed.ApprovalID) == "" {
		return "", fmt.Errorf("await prompt missing approval_id")
	}
	return strings.TrimSpace(parsed.ApprovalID), nil
}

func approvalDecisionFromPayload(payload []byte) string {
	var body map[string]any
	if err := json.Unmarshal(payload, &body); err != nil {
		return ""
	}
	for _, key := range []string{"choice", "decision", "reply", "text"} {
		value := strings.ToLower(strings.TrimSpace(textValue(body, key)))
		switch value {
		case "approve", "approved", "yes":
			return "approve"
		case "reject", "rejected", "no":
			return "reject"
		}
	}
	return ""
}

func joinMessageText(message domain.Message) string {
	parts := make([]string, 0, len(message.Parts))
	for _, part := range message.Parts {
		if strings.TrimSpace(part.Content) != "" {
			parts = append(parts, strings.TrimSpace(part.Content))
		}
	}
	if len(parts) == 0 {
		return strings.TrimSpace(message.Text)
	}
	return strings.Join(parts, "\n")
}

func (c ParmesanClient) pollEvery() time.Duration {
	if c.PollEvery > 0 {
		return c.PollEvery
	}
	return 100 * time.Millisecond
}

func (c ParmesanClient) getJSON(ctx context.Context, path string, query map[string]string, out any) error {
	req, err := c.newRequest(ctx, http.MethodGet, path, query, nil)
	if err != nil {
		return err
	}
	return c.do(req, out)
}

func (c ParmesanClient) postJSON(ctx context.Context, path string, query map[string]string, body any, out any) error {
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

func (c ParmesanClient) newRequest(ctx context.Context, method, path string, query map[string]string, body []byte) (*http.Request, error) {
	u, err := url.Parse(c.BaseURL + path)
	if err != nil {
		return nil, err
	}
	values := u.Query()
	for k, v := range query {
		values.Set(k, v)
	}
	u.RawQuery = values.Encode()
	reader := bytes.NewReader(body)
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

type httpStatusError struct {
	StatusCode int
	Status     string
	Body       string
}

func (e httpStatusError) Error() string {
	if strings.TrimSpace(e.Body) == "" {
		return fmt.Sprintf("acp request failed: %s", e.Status)
	}
	return fmt.Sprintf("acp request failed: %s: %s", e.Status, e.Body)
}

func errorAsStatus(err error, target *httpStatusError) bool {
	typed, ok := err.(httpStatusError)
	if !ok {
		return false
	}
	*target = typed
	return true
}

func (c ParmesanClient) do(req *http.Request, out any) error {
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
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return httpStatusError{StatusCode: resp.StatusCode, Status: resp.Status, Body: strings.TrimSpace(string(body))}
	}
	if out == nil {
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

func textValue(values map[string]any, key string) string {
	if values == nil {
		return ""
	}
	value, ok := values[key]
	if !ok || value == nil {
		return ""
	}
	switch typed := value.(type) {
	case string:
		return strings.TrimSpace(typed)
	default:
		return strings.TrimSpace(fmt.Sprint(typed))
	}
}

func marshalJSON(v any) []byte {
	out, _ := json.Marshal(v)
	return out
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
