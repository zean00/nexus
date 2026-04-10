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
	ID             string `json:"id"`
	SessionID      string `json:"session_id"`
	AgentID        string `json:"agent_id,omitempty"`
	AgentProfileID string `json:"agent_profile_id,omitempty"`
	Status         string `json:"status"`
	BlockedReason  string `json:"blocked_reason,omitempty"`
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
	Title          string                `json:"title"`
	Body           string                `json:"body,omitempty"`
	Choices        []domain.RenderChoice `json:"choices,omitempty"`
	ApprovalID     string                `json:"approval_id,omitempty"`
	ToolID         string                `json:"tool_id,omitempty"`
	AgentProfileID string                `json:"agent_profile_id,omitempty"`
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
	err := c.getJSON(ctx, parmesanSessionPath(session.AgentProfileID, sessionID), nil, &existing)
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
		"_meta":       parmesanSessionMeta(session),
	}
	var created parmesanSession
	if err := c.postJSON(ctx, parmesanSessionsPath(session.AgentProfileID), nil, body, &created); err != nil {
		return "", err
	}
	if created.ID == "" {
		return "", fmt.Errorf("ensure session: missing session id")
	}
	return created.ID, nil
}

func (c ParmesanClient) StartRun(ctx context.Context, req domain.StartRunRequest) (domain.Run, domain.RunEventStream, error) {
	sessionID := req.Session.ACPSessionID
	if sessionID == "" {
		var err error
		sessionID, err = c.EnsureSession(ctx, req.Session)
		if err != nil {
			return domain.Run{}, domain.RunEventStream{}, err
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
	if err := c.postJSON(ctx, parmesanMessagesPath(req.Session.AgentProfileID, sessionID), nil, body, &created); err != nil {
		return domain.Run{}, domain.RunEventStream{}, err
	}
	if strings.TrimSpace(created.ExecutionID) == "" {
		return domain.Run{}, domain.RunEventStream{}, fmt.Errorf("start run: missing execution id")
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
	snapshot, err := c.waitForSnapshot(ctx, req.Session.AgentProfileID, sessionID, created.ExecutionID, created.Offset+1)
	if err != nil {
		return domain.Run{}, domain.RunEventStream{}, err
	}
	run.Status = snapshot.Status
	event := domain.RunEvent{
		RunID:      run.ID,
		MessageKey: run.ACPRunID,
		Status:     snapshot.Status,
		Text:       snapshot.Output,
		Artifacts:  snapshot.Artifacts,
	}
	if snapshot.Await != nil {
		event.AwaitSchema = snapshot.Await.Schema
		event.AwaitPrompt = snapshot.Await.Prompt
	}
	return run, staticRunEventStream(event), nil
}

func (c ParmesanClient) ResumeRun(ctx context.Context, await domain.Await, payload []byte) (domain.RunEventStream, error) {
	return c.resumeRun(ctx, "", await, payload)
}

func (c ParmesanClient) ResumeRunForSession(ctx context.Context, session domain.Session, await domain.Await, payload []byte) (domain.RunEventStream, error) {
	return c.resumeRun(ctx, session.AgentProfileID, await, payload)
}

func (c ParmesanClient) resumeRun(ctx context.Context, sessionAgentID string, await domain.Await, payload []byte) (domain.RunEventStream, error) {
	prompt, err := approvalPromptFromJSON(await.PromptRenderJSON)
	if err != nil {
		return domain.RunEventStream{}, err
	}
	decision := approvalDecisionFromPayload(payload)
	if decision == "" {
		return domain.RunEventStream{}, fmt.Errorf("resume run: unsupported approval payload")
	}
	agentID := firstNonEmpty(sessionAgentID, prompt.AgentProfileID)
	if err := c.postJSON(ctx, parmesanApprovalPath(agentID, await.SessionID, prompt.ApprovalID), nil, map[string]any{"decision": decision}, nil); err != nil {
		return domain.RunEventStream{}, err
	}
	execID := strings.TrimPrefix(await.RunID, "run_")
	snapshot, err := c.waitForSnapshot(ctx, agentID, await.SessionID, execID, 0)
	if err != nil {
		return domain.RunEventStream{}, err
	}
	event := domain.RunEvent{
		RunID:      await.RunID,
		MessageKey: "resume:" + execID,
		Status:     snapshot.Status,
		Text:       snapshot.Output,
		Artifacts:  snapshot.Artifacts,
	}
	if snapshot.Await != nil {
		event.AwaitSchema = snapshot.Await.Schema
		event.AwaitPrompt = snapshot.Await.Prompt
	}
	return staticRunEventStream(event), nil
}

func (c ParmesanClient) GetRun(ctx context.Context, acpRunID string) (domain.RunStatusSnapshot, error) {
	exec, err := c.getExecution(ctx, acpRunID)
	if err != nil {
		return domain.RunStatusSnapshot{}, err
	}
	return c.snapshotForExecution(ctx, firstNonEmpty(exec.AgentProfileID, exec.AgentID), exec.SessionID, exec, 0)
}

func (c ParmesanClient) GetRunForSession(ctx context.Context, session domain.Session, acpRunID string) (domain.RunStatusSnapshot, error) {
	return c.getRunWithAgent(ctx, session.AgentProfileID, acpRunID)
}

func (c ParmesanClient) FindRunByIdempotencyKey(ctx context.Context, session domain.Session, idempotencyKey string) (domain.RunStatusSnapshot, bool, error) {
	if strings.TrimSpace(session.ACPSessionID) == "" || strings.TrimSpace(idempotencyKey) == "" {
		return domain.RunStatusSnapshot{}, false, nil
	}
	events, err := c.listEvents(ctx, session.AgentProfileID, session.ACPSessionID, 0)
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
		snapshot, err := c.getRunWithAgent(ctx, session.AgentProfileID, event.ExecutionID)
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
	if err := c.getJSON(ctx, parmesanSessionPath(session.AgentProfileID, session.ACPSessionID), nil, &current); err != nil {
		return domain.RunStatusSnapshot{}, false, err
	}
	if current.Summary == nil || strings.TrimSpace(current.Summary.LastExecutionID) == "" {
		return domain.RunStatusSnapshot{}, false, nil
	}
	snapshot, err := c.getRunWithAgent(ctx, session.AgentProfileID, current.Summary.LastExecutionID)
	if err != nil {
		return domain.RunStatusSnapshot{}, false, err
	}
	return snapshot, true, nil
}

func (c ParmesanClient) CancelRun(ctx context.Context, run domain.Run) error {
	return c.postJSON(ctx, "/v1/operator/executions/"+url.PathEscape(run.ACPRunID)+"/abandon", nil, nil, nil)
}

func (c ParmesanClient) waitForSnapshot(ctx context.Context, agentID, sessionID, execID string, minOffset int64) (domain.RunStatusSnapshot, error) {
	deadline := time.Now().UTC().Add(c.WaitTimeout)
	if dl, ok := ctx.Deadline(); ok && dl.Before(deadline) {
		deadline = dl
	}
	for {
		exec, err := c.getExecution(ctx, execID)
		if err != nil {
			return domain.RunStatusSnapshot{}, err
		}
		snapshot, err := c.snapshotForExecution(ctx, agentID, sessionID, exec, minOffset)
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

func (c ParmesanClient) snapshotForExecution(ctx context.Context, agentID, sessionID string, exec parmesanExecution, minOffset int64) (domain.RunStatusSnapshot, error) {
	events, err := c.listEvents(ctx, agentID, sessionID, minOffset)
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
			prompt := buildApprovalAwaitPrompt(event, agentID)
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
			approval, ok, err := c.pendingApproval(ctx, agentID, sessionID, exec.ID)
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
							Title:          firstNonEmpty(approval.RequestText, "Approval required"),
							Choices:        []domain.RenderChoice{{ID: "approve", Label: "Approve"}, {ID: "reject", Label: "Reject"}},
							ApprovalID:     approval.ID,
							ToolID:         approval.ToolID,
							AgentProfileID: strings.TrimSpace(agentID),
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

func (c ParmesanClient) pendingApproval(ctx context.Context, agentID, sessionID, execID string) (parmesanApproval, bool, error) {
	var items []parmesanApproval
	if err := c.getJSON(ctx, parmesanApprovalsPath(agentID, sessionID), map[string]string{"status": "pending"}, &items); err != nil {
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

func (c ParmesanClient) getRunWithAgent(ctx context.Context, agentID, acpRunID string) (domain.RunStatusSnapshot, error) {
	exec, err := c.getExecution(ctx, acpRunID)
	if err != nil {
		return domain.RunStatusSnapshot{}, err
	}
	return c.snapshotForExecution(ctx, agentID, exec.SessionID, exec, 0)
}

func (c ParmesanClient) listEvents(ctx context.Context, agentID, sessionID string, minOffset int64) ([]parmesanEvent, error) {
	query := map[string]string{}
	if minOffset > 0 {
		query["min_offset"] = fmt.Sprintf("%d", minOffset)
	}
	var events []parmesanEvent
	if err := c.getJSON(ctx, parmesanEventsPath(agentID, sessionID), query, &events); err != nil {
		return nil, err
	}
	return events, nil
}

func buildApprovalAwaitPrompt(event parmesanEvent, agentID string) []byte {
	return marshalJSON(parmesanAwaitPrompt{
		Title:          firstNonEmpty(textValue(event.Data, "message"), "Approval required"),
		Choices:        []domain.RenderChoice{{ID: "approve", Label: "Approve"}, {ID: "reject", Label: "Reject"}},
		ApprovalID:     textValue(event.Data, "approval_id"),
		ToolID:         textValue(event.Data, "tool_id"),
		AgentProfileID: strings.TrimSpace(agentID),
	})
}

func approvalPromptFromJSON(prompt []byte) (parmesanAwaitPrompt, error) {
	var parsed parmesanAwaitPrompt
	if err := json.Unmarshal(prompt, &parsed); err != nil {
		return parmesanAwaitPrompt{}, fmt.Errorf("decode await prompt: %w", err)
	}
	if strings.TrimSpace(parsed.ApprovalID) == "" {
		return parmesanAwaitPrompt{}, fmt.Errorf("await prompt missing approval_id")
	}
	parsed.ApprovalID = strings.TrimSpace(parsed.ApprovalID)
	parsed.AgentProfileID = strings.TrimSpace(parsed.AgentProfileID)
	return parsed, nil
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

func parmesanSessionMeta(session domain.Session) map[string]any {
	return map[string]any{
		"parmesan": map[string]any{
			"customer_id": strings.TrimSpace(session.OwnerUserID),
			"customer": map[string]any{
				"id":                strings.TrimSpace(session.OwnerUserID),
				"gateway_user_id":   strings.TrimSpace(session.OwnerUserID),
				"channel_scope_key": strings.TrimSpace(session.ChannelScopeKey),
				"channel_type":      strings.TrimSpace(session.ChannelType),
			},
			"gateway_session_id":    strings.TrimSpace(session.ID),
			"channel_scope_key":     strings.TrimSpace(session.ChannelScopeKey),
			"channel_type":          strings.TrimSpace(session.ChannelType),
			"bridge_implementation": "parmesan",
		},
	}
}

func parmesanSessionsPath(agentID string) string {
	agentID = strings.TrimSpace(agentID)
	if agentID == "" {
		return "/v1/acp/sessions"
	}
	return "/v1/acp/agents/" + url.PathEscape(agentID) + "/sessions"
}

func parmesanSessionPath(agentID, sessionID string) string {
	return parmesanSessionsPath(agentID) + "/" + url.PathEscape(sessionID)
}

func parmesanMessagesPath(agentID, sessionID string) string {
	return parmesanSessionPath(agentID, sessionID) + "/messages"
}

func parmesanEventsPath(agentID, sessionID string) string {
	return parmesanSessionPath(agentID, sessionID) + "/events"
}

func parmesanApprovalsPath(agentID, sessionID string) string {
	return parmesanSessionPath(agentID, sessionID) + "/approvals"
}

func parmesanApprovalPath(agentID, sessionID, approvalID string) string {
	return parmesanApprovalsPath(agentID, sessionID) + "/" + url.PathEscape(approvalID)
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
