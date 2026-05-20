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
	Headers map[string]string
}

type strictManifest struct {
	Name                    string   `json:"name"`
	Description             string   `json:"description"`
	Capabilities            []string `json:"capabilities"`
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
	ID              string `json:"id"`
	SessionID       string `json:"session_id"`
	GreetingRunID   string `json:"greeting_run_id"`
	GreetingState   string `json:"greeting_state"`
	GreetingSkipped string `json:"greeting_skipped"`
}

type strictRun struct {
	ID             string         `json:"id"`
	SessionID      string         `json:"session_id"`
	State          string         `json:"state"`
	Status         string         `json:"status"`
	Output         string         `json:"output"`
	IdempotencyKey string         `json:"idempotency_key"`
	Metadata       map[string]any `json:"metadata"`
	Artifacts      []strictAsset  `json:"artifacts"`
	Await          *strictAwait   `json:"await"`
}

type strictSessionEvent struct {
	ID          string         `json:"id"`
	SessionID   string         `json:"session_id"`
	Source      string         `json:"source"`
	Kind        string         `json:"kind"`
	Offset      int64          `json:"offset"`
	ExecutionID string         `json:"execution_id"`
	Text        string         `json:"text"`
	Body        string         `json:"body"`
	Content     []strictPart   `json:"content"`
	Metadata    map[string]any `json:"metadata"`
}

type strictPart struct {
	Type string `json:"type"`
	Text string `json:"text"`
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
		var wrapped struct {
			Agents []strictManifest `json:"agents"`
		}
		if wrappedErr := c.getJSON(ctx, "/agents", nil, &wrapped); wrappedErr != nil {
			return nil, err
		}
		manifests = wrapped.Agents
	}
	out := make([]domain.AgentManifest, 0, len(manifests))
	for _, manifest := range manifests {
		if strings.TrimSpace(manifest.Name) == "" {
			return nil, fmt.Errorf("discover agents: agent manifest missing name")
		}
		applyStrictCapabilities(&manifest)
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

func applyStrictCapabilities(manifest *strictManifest) {
	for _, capability := range manifest.Capabilities {
		switch strings.TrimSpace(capability) {
		case "runs":
			manifest.Healthy = true
			manifest.SupportsSessionReload = true
		case "resume":
			manifest.SupportsAwaitResume = true
			manifest.SupportsStructuredAwait = true
		case "events":
			manifest.SupportsStreaming = true
		case "artifacts":
			manifest.SupportsArtifacts = true
		}
	}
}

func (c StrictClient) EnsureSession(ctx context.Context, session domain.Session) (string, error) {
	created, err := c.ensureSession(ctx, session, domain.SessionGreetingOptions{})
	return created.ID, err
}

func (c StrictClient) EnsureSessionWithGreeting(ctx context.Context, session domain.Session, options domain.SessionGreetingOptions) (map[string]any, error) {
	created, err := c.ensureSession(ctx, session, options)
	if err != nil {
		return nil, err
	}
	out := map[string]any{"session_id": created.ID, "state": "ready"}
	if created.GreetingRunID != "" {
		out["greeting_run_id"] = created.GreetingRunID
	}
	if created.GreetingState != "" {
		out["greeting_state"] = created.GreetingState
	}
	if created.GreetingSkipped != "" {
		out["greeting_skipped"] = created.GreetingSkipped
	}
	return out, nil
}

func (c StrictClient) ensureSession(ctx context.Context, session domain.Session, options domain.SessionGreetingOptions) (strictSession, error) {
	metadata := strictSessionMetadata(session)
	if session.ACPSessionID != "" && !options.SendGreeting && len(metadata) == 0 {
		return strictSession{ID: session.ACPSessionID}, nil
	}
	body := map[string]any{
		"gateway_session_id": session.ID,
		"metadata":           metadata,
	}
	if session.ACPSessionID != "" {
		body["session_id"] = session.ACPSessionID
	}
	if options.SendGreeting {
		body["send_greeting"] = true
		body["greeting_channels"] = options.GreetingChannels
		body["nickname"] = options.Nickname
		if language, name := strictGreetingLanguage(options.PreferredLanguage); language != "" {
			body["preferred_language"] = language
			body["language"] = language
			body["locale"] = language
			body["language_preference"] = language
			if name != "" {
				body["preferred_language_name"] = name
				body["greeting_instruction"] = "Write the greeting in " + name + "."
			}
		}
	}
	var created strictSession
	targetSessionID := session.ID
	if strings.TrimSpace(session.ACPSessionID) != "" {
		targetSessionID = session.ACPSessionID
	}
	if err := c.putJSON(ctx, "/sessions/"+url.PathEscape(targetSessionID), nil, body, &created, sessionHeaders(session, "", "")); err != nil {
		if postErr := c.postJSON(ctx, "/sessions", nil, body, &created, sessionHeaders(session, "", "")); postErr != nil {
			return strictSession{}, err
		}
	}
	if created.ID == "" {
		created.ID = created.SessionID
	}
	if created.ID == "" && session.ACPSessionID != "" {
		created.ID = session.ACPSessionID
	}
	if created.ID == "" {
		return strictSession{}, fmt.Errorf("ensure session: missing session id")
	}
	return created, nil
}

func strictSessionMetadata(session domain.Session) map[string]any {
	metadata := map[string]any{}
	if mode := strings.ToLower(strings.TrimSpace(session.Mode)); mode != "" {
		metadata["runtime_mode"] = mode
		metadata["agent_mode"] = mode
	}
	if session.AllowFirstMessageResponse {
		metadata["allow_first_message_response"] = true
	}
	if agentName := strings.TrimSpace(session.ACPAgentName); agentName != "" {
		metadata["agent_name"] = agentName
	}
	if profileID := strings.TrimSpace(session.ACPProfileID); profileID != "" {
		metadata["agent_profile_id"] = profileID
	}
	return metadata
}

func strictGreetingLanguage(value string) (string, string) {
	clean := strings.ToLower(strings.TrimSpace(value))
	clean = strings.ReplaceAll(clean, "_", "-")
	switch clean {
	case "":
		return "", ""
	case "id", "id-id", "indonesia", "indonesian", "bahasa indonesia":
		return "id", "Indonesian"
	case "en", "en-us", "en-gb", "english":
		return "en", "English"
	default:
		return strings.TrimSpace(value), ""
	}
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
	if err := c.postJSON(ctx, "/runs", nil, body, &response, sessionHeaders(req.Session, req.IdempotencyKey, "")); err != nil {
		return domain.Run{}, domain.RunEventStream{}, err
	}
	run, event, err := c.mapRunResponse(req.Session.ID, response)
	if err != nil {
		return domain.Run{}, domain.RunEventStream{}, err
	}
	run.ACPAgentName = req.RouteDecision.ACPAgentName
	return run, staticRunEventStream(event), nil
}

func compactReplyToFromRawPayload(raw []byte) map[string]any {
	if len(raw) == 0 {
		return nil
	}
	var payload struct {
		ReplyTo map[string]any `json:"reply_to"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil || len(payload.ReplyTo) == 0 {
		return nil
	}
	out := map[string]any{}
	for _, key := range []string{"message_id", "external_message_id", "run_id", "role", "source", "kind"} {
		if value, _ := payload.ReplyTo[key].(string); strings.TrimSpace(value) != "" {
			out[key] = strings.TrimSpace(value)
		}
	}
	if ids := runInputStringsFromAny(payload.ReplyTo["artifact_ids"]); len(ids) > 0 {
		out["artifact_ids"] = ids
	} else if ids := runInputStringsFromAny(payload.ReplyTo["artifacts"]); len(ids) > 0 {
		out["artifact_ids"] = ids
	}
	if strings.TrimSpace(fmt.Sprint(out["message_id"])) == "" && strings.TrimSpace(fmt.Sprint(out["external_message_id"])) == "" && strings.TrimSpace(fmt.Sprint(out["run_id"])) == "" {
		return nil
	}
	return out
}

func runInputStringsFromAny(value any) []string {
	switch v := value.(type) {
	case []string:
		out := make([]string, 0, len(v))
		for _, item := range v {
			if item = strings.TrimSpace(item); item != "" {
				out = append(out, item)
			}
		}
		return out
	case []any:
		out := make([]string, 0, len(v))
		for _, item := range v {
			if text := strings.TrimSpace(fmt.Sprint(item)); text != "" {
				out = append(out, text)
			}
		}
		return out
	default:
		return nil
	}
}

func strictMessageParts(parts []domain.Part) []map[string]any {
	out := make([]map[string]any, 0, len(parts))
	for _, part := range parts {
		contentType := strings.TrimSpace(part.ContentType)
		content := part.Content
		switch {
		case contentType == "application/vnd.nexus.structured-data+json":
			var data map[string]any
			if err := json.Unmarshal([]byte(content), &data); err == nil && len(data) > 0 {
				out = append(out, map[string]any{"type": "structured_data", "data": data})
			}
		case contentType == "", strings.HasPrefix(contentType, "text/"):
			out = append(out, map[string]any{"type": "text", "text": content})
		default:
			out = append(out, map[string]any{"type": contentType, "text": content})
		}
	}
	return out
}

func appendEmailContextPart(parts []map[string]any, session domain.Session, message domain.Message) []map[string]any {
	if !strings.EqualFold(strings.TrimSpace(session.ChannelType), "email") || len(message.RawPayload) == 0 {
		return parts
	}
	var payload struct {
		MessageID  string   `json:"message_id"`
		From       string   `json:"from"`
		FromName   string   `json:"from_name"`
		Subject    string   `json:"subject"`
		ThreadID   string   `json:"thread_id"`
		InReplyTo  string   `json:"in_reply_to"`
		References []string `json:"references"`
	}
	if err := json.Unmarshal(message.RawPayload, &payload); err != nil {
		return parts
	}
	data := map[string]any{"kind": "email_context"}
	add := func(key, value string) {
		if value = strings.TrimSpace(value); value != "" {
			data[key] = value
		}
	}
	add("message_id", payload.MessageID)
	add("from", payload.From)
	add("from_name", payload.FromName)
	add("subject", payload.Subject)
	add("thread_id", payload.ThreadID)
	add("in_reply_to", payload.InReplyTo)
	if len(payload.References) > 0 {
		data["references"] = payload.References
	}
	if len(data) == 1 {
		return parts
	}
	return append(parts, map[string]any{"type": "structured_data", "data": data})
}

func appendArtifactRefParts(parts []map[string]any, artifacts []domain.Artifact) []map[string]any {
	for _, artifact := range artifacts {
		id := strings.TrimSpace(artifact.ID)
		if id == "" {
			continue
		}
		data := map[string]any{"artifact_id": id}
		if artifact.Name != "" {
			data["name"] = artifact.Name
		}
		if artifact.MIMEType != "" {
			data["mime_type"] = artifact.MIMEType
		}
		if artifact.SizeBytes > 0 {
			data["size_bytes"] = artifact.SizeBytes
		}
		parts = append(parts, map[string]any{"type": "artifact_ref", "data": data})
	}
	return parts
}

func (c StrictClient) ResumeRun(ctx context.Context, await domain.Await, payload []byte) (domain.RunEventStream, error) {
	acpRunID := strings.TrimPrefix(await.RunID, "run_")
	var response strictRun
	if err := c.postJSON(ctx, "/runs/"+url.PathEscape(acpRunID)+"/resume", nil, map[string]any{"payload": json.RawMessage(payload)}, &response, map[string]string{"X-Run-ID": await.RunID}); err != nil {
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

func (c StrictClient) GetRunForSession(ctx context.Context, session domain.Session, acpRunID string) (domain.RunStatusSnapshot, error) {
	var response strictRun
	if err := c.getJSON(ctx, "/runs/"+url.PathEscape(acpRunID), nil, &response, sessionHeaders(session, "", acpRunID)); err != nil {
		return domain.RunStatusSnapshot{}, err
	}
	return c.mapSnapshot(response)
}

func (c StrictClient) ListVisibleEvents(ctx context.Context, session domain.Session, minOffset int64) ([]domain.VisibleSessionEvent, error) {
	sessionID := strings.TrimSpace(session.ACPSessionID)
	if sessionID == "" {
		sessionID = strings.TrimSpace(session.ID)
	}
	if sessionID == "" {
		return nil, nil
	}
	query := map[string]string{}
	if minOffset > 0 {
		query["min_offset"] = fmt.Sprint(minOffset)
	}
	var events []strictSessionEvent
	if err := c.getJSON(ctx, "/sessions/"+url.PathEscape(sessionID)+"/events", query, &events, sessionHeaders(session, "", "")); err != nil {
		return nil, err
	}
	out := make([]domain.VisibleSessionEvent, 0, len(events))
	for _, event := range events {
		mapped, ok := mapStrictVisibleSessionEvent(event)
		if ok {
			out = append(out, mapped)
		}
	}
	return out, nil
}

func mapStrictVisibleSessionEvent(event strictSessionEvent) (domain.VisibleSessionEvent, bool) {
	source := strings.TrimSpace(event.Source)
	switch source {
	case "human_agent", "human_operator", "operator", "human_agent_on_behalf_of_ai_agent":
	case "ai_agent", "assistant":
		if !strictEventMetadataBool(event.Metadata, "response_review_approved") {
			return domain.VisibleSessionEvent{}, false
		}
	default:
		return domain.VisibleSessionEvent{}, false
	}
	kind := strings.TrimSpace(event.Kind)
	if kind != "" && kind != "message" && kind != "agent.message" && kind != "operator.message" {
		return domain.VisibleSessionEvent{}, false
	}
	text := strictSessionEventText(event)
	if strings.TrimSpace(event.ID) == "" || strings.TrimSpace(text) == "" {
		return domain.VisibleSessionEvent{}, false
	}
	return domain.VisibleSessionEvent{
		ID:          event.ID,
		SessionID:   event.SessionID,
		Source:      source,
		Kind:        firstNonEmpty(kind, "message"),
		Offset:      event.Offset,
		ExecutionID: event.ExecutionID,
		Text:        text,
		Metadata:    event.Metadata,
	}, true
}

func strictSessionEventText(event strictSessionEvent) string {
	if text := strings.TrimSpace(event.Text); text != "" {
		return text
	}
	if text := strings.TrimSpace(event.Body); text != "" {
		return text
	}
	var parts []string
	for _, part := range event.Content {
		if text := strings.TrimSpace(part.Text); text != "" && (part.Type == "" || part.Type == "text") {
			parts = append(parts, text)
		}
	}
	return strings.TrimSpace(strings.Join(parts, "\n"))
}

func strictEventMetadataBool(metadata map[string]any, key string) bool {
	if metadata == nil {
		return false
	}
	value, ok := metadata[key]
	if !ok {
		return false
	}
	switch v := value.(type) {
	case bool:
		return v
	case string:
		return strings.EqualFold(strings.TrimSpace(v), "true")
	default:
		return false
	}
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			return value
		}
	}
	return ""
}

func (c StrictClient) FindRunByIdempotencyKey(ctx context.Context, session domain.Session, idempotencyKey string) (domain.RunStatusSnapshot, bool, error) {
	if session.ACPSessionID == "" || strings.TrimSpace(idempotencyKey) == "" {
		return domain.RunStatusSnapshot{}, false, nil
	}
	var runs []strictRun
	if err := c.getJSON(ctx, "/sessions/"+url.PathEscape(session.ACPSessionID)+"/runs", map[string]string{"limit": "50"}, &runs, sessionHeaders(session, idempotencyKey, "")); err != nil {
		return domain.RunStatusSnapshot{}, false, err
	}
	for _, run := range runs {
		if run.IdempotencyKey != idempotencyKey {
			if value, _ := run.Metadata["idempotency_key"].(string); value != idempotencyKey {
				continue
			}
		}
		snapshot, err := c.mapSnapshot(run)
		if err != nil {
			return domain.RunStatusSnapshot{}, false, err
		}
		return snapshot, true, nil
	}
	return domain.RunStatusSnapshot{}, false, nil
}

func (c StrictClient) FindLatestRunForSession(ctx context.Context, session domain.Session) (domain.RunStatusSnapshot, bool, error) {
	if session.ACPSessionID == "" {
		return domain.RunStatusSnapshot{}, false, nil
	}
	var runs []strictRun
	if err := c.getJSON(ctx, "/sessions/"+url.PathEscape(session.ACPSessionID)+"/runs", map[string]string{"limit": "20"}, &runs, sessionHeaders(session, "", "")); err != nil {
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
	if response.Status == "" {
		response.Status = response.State
	}
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
		Metadata:   response.Metadata,
	}
	if response.Await != nil {
		event.AwaitSchema = response.Await.Schema
		event.AwaitPrompt = response.Await.Prompt
	}
	return run, event, nil
}

func (c StrictClient) mapSnapshot(response strictRun) (domain.RunStatusSnapshot, error) {
	if response.Status == "" {
		response.Status = response.State
	}
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

func (c StrictClient) getJSON(ctx context.Context, path string, query map[string]string, out any, headers ...map[string]string) error {
	req, err := c.newRequest(ctx, http.MethodGet, path, query, nil)
	if err != nil {
		return err
	}
	for _, headerSet := range headers {
		for key, value := range headerSet {
			if strings.TrimSpace(value) != "" {
				req.Header.Set(key, value)
			}
		}
	}
	return c.do(req, out)
}

func (c StrictClient) postJSON(ctx context.Context, path string, query map[string]string, body any, out any, headers ...map[string]string) error {
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
	for _, headerSet := range headers {
		for key, value := range headerSet {
			if strings.TrimSpace(value) != "" {
				req.Header.Set(key, value)
			}
		}
	}
	return c.do(req, out)
}

func (c StrictClient) putJSON(ctx context.Context, path string, query map[string]string, body any, out any, headers ...map[string]string) error {
	var payload []byte
	var err error
	if body != nil {
		payload, err = json.Marshal(body)
		if err != nil {
			return err
		}
	}
	req, err := c.newRequest(ctx, http.MethodPut, path, query, payload)
	if err != nil {
		return err
	}
	for _, headerSet := range headers {
		for key, value := range headerSet {
			if strings.TrimSpace(value) != "" {
				req.Header.Set(key, value)
			}
		}
	}
	return c.do(req, out)
}

func sessionHeaders(session domain.Session, idempotencyKey, runID string) map[string]string {
	userID := strings.TrimSpace(session.OwnerUserID)
	if userID == "" {
		userID = strings.TrimSpace(session.ChannelScopeKey)
	}
	return map[string]string{
		"X-Customer-ID":             session.TenantID,
		"X-User-ID":                 userID,
		"X-Agent-Instance-ID":       session.AgentProfileID,
		"X-Session-ID":              session.ID,
		"X-Request-ID":              session.ID,
		"X-Idempotency-Key":         idempotencyKey,
		"X-Run-ID":                  runID,
		"X-Channel-Type":            session.ChannelType,
		"X-Channel-User-ID":         userID,
		"X-Channel-Conversation-ID": session.ChannelScopeKey,
	}
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
	for key, value := range c.Headers {
		if strings.TrimSpace(key) != "" && strings.TrimSpace(value) != "" {
			req.Header.Set(key, value)
		}
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
