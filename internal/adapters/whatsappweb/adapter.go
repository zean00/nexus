package whatsappweb

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"io"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"nexus/internal/domain"
	"nexus/internal/ports"
)

type Adapter struct {
	BaseURL                      string
	APIKey                       string
	Session                      string
	Engine                       string
	WebhookSecret                string
	PublicBaseURL                string
	HTTP                         *http.Client
	CountSentDeliveriesSince     func(context.Context, string, time.Time) (int, error)
	HasRecentInboundMessageSince func(context.Context, string, time.Time) (bool, error)
	EnableAntiBlock              bool
	EnableSeen                   bool
	EnableTyping                 bool
	SetOfflineAfterSend          bool
	RequireRecentInbound         bool
	MinDelay                     time.Duration
	MaxDelay                     time.Duration
	HourlyMessageCap             int
	RecentInboundWindow          time.Duration
	BurstWindow                  time.Duration
	BurstMessageCap              int
	GroupBotIDs                  []string
	Sleep                        func(time.Duration)
}

type SessionStatus struct {
	Name    string          `json:"name"`
	Status  string          `json:"status"`
	Engine  map[string]any  `json:"engine,omitempty"`
	Config  map[string]any  `json:"config,omitempty"`
	Me      map[string]any  `json:"me,omitempty"`
	RawJSON json.RawMessage `json:"-"`
}

func New(baseURL, apiKey, session, engine, webhookSecret, publicBaseURL string) Adapter {
	return Adapter{
		BaseURL:              strings.TrimRight(strings.TrimSpace(baseURL), "/"),
		APIKey:               strings.TrimSpace(apiKey),
		Session:              strings.TrimSpace(session),
		Engine:               strings.TrimSpace(engine),
		WebhookSecret:        strings.TrimSpace(webhookSecret),
		PublicBaseURL:        strings.TrimRight(strings.TrimSpace(publicBaseURL), "/"),
		HTTP:                 &http.Client{Timeout: 15 * time.Second},
		EnableAntiBlock:      true,
		EnableSeen:           true,
		EnableTyping:         true,
		SetOfflineAfterSend:  true,
		RequireRecentInbound: true,
		MinDelay:             800 * time.Millisecond,
		MaxDelay:             2500 * time.Millisecond,
		HourlyMessageCap:     120,
		RecentInboundWindow:  30 * time.Minute,
		BurstWindow:          2 * time.Minute,
		BurstMessageCap:      4,
		Sleep:                time.Sleep,
	}
}

func (a Adapter) Channel() string { return "whatsapp_web" }

func (a Adapter) VerifyInbound(_ context.Context, r *http.Request, body []byte) error {
	if a.WebhookSecret == "" {
		return nil
	}
	got := strings.TrimSpace(r.Header.Get("X-Webhook-Hmac"))
	if got == "" {
		return errors.New("missing WAHA webhook signature")
	}
	mac, err := newWebhookHMAC(strings.TrimSpace(r.Header.Get("X-Webhook-Hmac-Algorithm")), []byte(a.WebhookSecret))
	if err != nil {
		return err
	}
	_, _ = mac.Write(body)
	expected := mac.Sum(nil)
	if !secureEqualHMAC(got, expected) {
		return errors.New("invalid WAHA webhook signature")
	}
	return nil
}

func newWebhookHMAC(algorithm string, secret []byte) (hash.Hash, error) {
	switch strings.ToLower(strings.TrimSpace(algorithm)) {
	case "", "sha256", "hmac-sha256":
		return hmac.New(sha256.New, secret), nil
	case "sha512", "hmac-sha512":
		return hmac.New(sha512.New, secret), nil
	case "sha1", "hmac-sha1":
		return hmac.New(sha1.New, secret), nil
	default:
		return nil, fmt.Errorf("unsupported WAHA webhook signature algorithm %q", algorithm)
	}
}

func secureEqualHMAC(got string, expected []byte) bool {
	got = stripHMACSignaturePrefix(got)
	if got == "" {
		return false
	}

	if decoded, err := hex.DecodeString(got); err == nil {
		return hmac.Equal(decoded, expected)
	}
	if decoded, err := hex.DecodeString(strings.ToLower(got)); err == nil {
		return hmac.Equal(decoded, expected)
	}

	for _, enc := range []*base64.Encoding{
		base64.StdEncoding,
		base64.RawStdEncoding,
		base64.URLEncoding,
		base64.RawURLEncoding,
	} {
		if decoded, err := enc.DecodeString(got); err == nil && hmac.Equal(decoded, expected) {
			return true
		}
	}
	return false
}

func stripHMACSignaturePrefix(value string) string {
	value = strings.TrimSpace(value)
	for _, sep := range []string{"=", ":"} {
		prefix, signature, ok := strings.Cut(value, sep)
		if !ok {
			continue
		}
		switch strings.ToLower(strings.TrimSpace(prefix)) {
		case "sha1", "sha256", "sha512", "hmac-sha1", "hmac-sha256", "hmac-sha512":
			return strings.TrimSpace(signature)
		}
	}
	return value
}

type webhookEnvelope struct {
	ID      string          `json:"id"`
	Event   string          `json:"event"`
	Session string          `json:"session"`
	Payload json.RawMessage `json:"payload"`
}

type messagePayload struct {
	ID           string   `json:"id"`
	Timestamp    int64    `json:"timestamp"`
	From         string   `json:"from"`
	To           string   `json:"to"`
	Participant  string   `json:"participant"`
	Author       string   `json:"author"`
	FromMe       bool     `json:"fromMe"`
	Body         string   `json:"body"`
	MentionedIDs []string `json:"mentionedIds"`
	Mentions     []string `json:"mentions"`
	HasMedia     bool     `json:"hasMedia"`
	Ack          *int     `json:"ack"`
	ReplyTo      any      `json:"replyTo"`
	Media        *struct {
		URL      string `json:"url"`
		MimeType string `json:"mimetype"`
		Filename string `json:"filename"`
		Error    string `json:"error"`
	} `json:"media"`
	Location *struct {
		Latitude  float64 `json:"latitude"`
		Longitude float64 `json:"longitude"`
		Name      string  `json:"name"`
		Address   string  `json:"address"`
	} `json:"location"`
	Raw map[string]any `json:"-"`
}

func (a Adapter) ParseInbound(ctx context.Context, r *http.Request, body []byte, tenantID string) (domain.CanonicalInboundEvent, error) {
	events, err := a.ParseInboundBatch(ctx, r, body, tenantID)
	if err != nil {
		return domain.CanonicalInboundEvent{}, err
	}
	if len(events) == 0 {
		return domain.CanonicalInboundEvent{}, errors.New("unsupported WAHA payload")
	}
	return events[0], nil
}

func (a Adapter) ParseInboundBatch(ctx context.Context, _ *http.Request, body []byte, tenantID string) ([]domain.CanonicalInboundEvent, error) {
	var env webhookEnvelope
	if err := json.Unmarshal(body, &env); err != nil {
		return nil, err
	}
	if strings.TrimSpace(env.Session) != "" && a.Session != "" && env.Session != a.Session {
		return nil, fmt.Errorf("unexpected WAHA session %q", env.Session)
	}
	if env.Event != "message" && env.Event != "message.any" {
		return nil, nil
	}
	var payload messagePayload
	if err := json.Unmarshal(env.Payload, &payload); err != nil {
		return nil, err
	}
	_ = json.Unmarshal(env.Payload, &payload.Raw)
	if payload.FromMe {
		return nil, nil
	}
	isGroup := strings.HasSuffix(strings.TrimSpace(payload.From), "@g.us")
	participantJID := firstNonEmpty(payload.Participant, payload.Author, rawStringPath(payload.Raw, "participant"), rawStringPath(payload.Raw, "author"), rawStringPath(payload.Raw, "_data", "author"))
	channelUserID := normalizeWAHAIdentity(payload.From)
	if isGroup && strings.TrimSpace(participantJID) != "" {
		channelUserID = normalizeWAHAIdentity(participantJID)
	} else if resolved := a.resolveWAHAContactIdentity(ctx, payload.From); resolved != "" {
		channelUserID = resolved
	}
	conversationID := normalizeWAHAConversation(payload.From)
	surfaceKey := strings.TrimSpace(payload.From)
	if surfaceKey == "" {
		surfaceKey = firstNonEmpty(conversationID, channelUserID)
	}
	if conversationID == "" {
		conversationID = channelUserID
	}
	receivedAt := time.Now().UTC()
	if payload.Timestamp > 0 {
		receivedAt = time.Unix(payload.Timestamp, 0).UTC()
	}
	text := strings.TrimSpace(payload.Body)
	parts := []domain.Part{}
	if text != "" {
		parts = append(parts, domain.Part{ContentType: "text/plain", Content: text})
	}
	if payload.Location != nil {
		location := domain.Location{
			Latitude:  payload.Location.Latitude,
			Longitude: payload.Location.Longitude,
			Name:      strings.TrimSpace(payload.Location.Name),
			Address:   strings.TrimSpace(payload.Location.Address),
		}
		parts = append(parts, domain.NewLocationPart(location))
		if text == "" {
			text = domain.LocationText(location)
		}
	}
	artifacts := wahaArtifacts(payload)
	mentionedIDs := normalizeMentionedIDs(append(append([]string{}, payload.MentionedIDs...), payload.Mentions...), payload.Raw)
	mentionsBot := isGroup && mentionsAnyBot(mentionedIDs, a.GroupBotIDs)
	if isGroup {
		if raw, err := json.Marshal(map[string]any{
			"kind":           "whatsapp_group_message",
			"group_id":       conversationID,
			"participant_id": channelUserID,
			"mentioned_ids":  mentionedIDs,
			"mentions_bot":   mentionsBot,
		}); err == nil {
			parts = append(parts, domain.Part{ContentType: "application/vnd.nexus.structured-data+json", Content: string(raw)})
		}
	}
	evt := domain.CanonicalInboundEvent{
		EventID:         "waha_" + firstNonEmpty(env.ID, payload.ID),
		TenantID:        tenantID,
		Channel:         "whatsapp_web",
		Interaction:     "message",
		ProviderEventID: firstNonEmpty(payload.ID, env.ID),
		ReceivedAt:      receivedAt,
		Sender: domain.Sender{
			ChannelUserID:       channelUserID,
			IsAuthenticated:     true,
			IdentityAssurance:   "provider_verified",
			AllowedResponderIDs: []string{channelUserID},
		},
		Conversation: domain.Conversation{
			ChannelConversationID: conversationID,
			ChannelThreadID:       conversationID,
			ChannelSurfaceKey:     surfaceKey,
		},
		Metadata: domain.Metadata{
			MentionsBot:        mentionsBot,
			IsGroup:            isGroup,
			GroupID:            conversationID,
			GroupParticipantID: channelUserID,
			GroupMentionedIDs:  mentionedIDs,
			AccountKey:         firstNonEmpty(env.Session, a.Session, surfaceKey),
			ArtifactTrust:      "trusted-channel-ingress",
			ResponderBinding: domain.ResponderBinding{
				Mode:                  "same-user-only",
				AllowedChannelUserIDs: []string{channelUserID},
			},
			RawPayload: body,
		},
	}
	if parsed, ok := parseAwaitTextReply(text); ok {
		resumePayload, err := json.Marshal(map[string]string{"choice": parsed.Choice})
		if err != nil {
			return nil, err
		}
		evt.Interaction = "await_response"
		evt.Message = domain.Message{
			MessageID:   "waha_await_" + payload.ID,
			MessageType: "interactive",
			Text:        parsed.Choice,
			Parts:       []domain.Part{{ContentType: "application/json", Content: string(resumePayload)}},
		}
		evt.Metadata.AwaitID = parsed.AwaitID
		evt.Metadata.ResumePayload = resumePayload
		return []domain.CanonicalInboundEvent{evt}, nil
	}
	evt.Message = domain.Message{
		MessageID:   "waha_msg_" + payload.ID,
		MessageType: messageType(text, artifacts),
		Text:        text,
		Parts:       parts,
		Artifacts:   artifacts,
	}
	return []domain.CanonicalInboundEvent{evt}, nil
}

type contactPayload struct {
	ID     string `json:"id"`
	Number string `json:"number"`
}

func (a Adapter) resolveWAHAContactIdentity(ctx context.Context, jid string) string {
	jid = strings.TrimSpace(jid)
	if jid == "" || !strings.HasSuffix(jid, "@lid") || a.BaseURL == "" {
		return ""
	}
	session := strings.TrimSpace(a.Session)
	if session == "" {
		session = "default"
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, a.BaseURL+"/api/"+url.PathEscape(session)+"/contacts/"+url.PathEscape(jid), nil)
	if err != nil {
		return ""
	}
	if a.APIKey != "" {
		req.Header.Set("X-Api-Key", a.APIKey)
	}
	client := a.HTTP
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return ""
	}
	var contact contactPayload
	if err := json.NewDecoder(io.LimitReader(resp.Body, 64*1024)).Decode(&contact); err != nil {
		return ""
	}
	for _, candidate := range []string{contact.Number, contact.ID} {
		if normalized := normalizeWAHAIdentity(candidate); normalized != "" {
			return normalized
		}
	}
	return ""
}

func wahaArtifacts(payload messagePayload) []domain.Artifact {
	if !payload.HasMedia || payload.Media == nil || strings.TrimSpace(payload.Media.URL) == "" {
		return nil
	}
	name := strings.TrimSpace(payload.Media.Filename)
	if name == "" {
		ext := extensionFromMIME(payload.Media.MimeType)
		name = payload.ID + ext
	}
	return []domain.Artifact{{
		ID:        payload.ID,
		Name:      name,
		MIMEType:  strings.TrimSpace(payload.Media.MimeType),
		SourceURL: strings.TrimSpace(payload.Media.URL),
	}}
}

func extensionFromMIME(mimeType string) string {
	if mimeType == "" {
		return ".bin"
	}
	if exts, _ := mime.ExtensionsByType(mimeType); len(exts) > 0 {
		return exts[0]
	}
	return ".bin"
}

func messageType(text string, artifacts []domain.Artifact) string {
	switch {
	case len(artifacts) > 0 && text != "":
		return "mixed"
	case len(artifacts) > 0:
		return "artifact"
	default:
		return "text"
	}
}

type awaitChoice struct {
	AwaitID string `json:"await_id"`
	Choice  string `json:"choice"`
}

func parseAwaitTextReply(text string) (awaitChoice, bool) {
	text = strings.TrimSpace(text)
	if !strings.HasPrefix(text, "[await:") {
		return awaitChoice{}, false
	}
	end := strings.Index(text, "]")
	if end <= 7 {
		return awaitChoice{}, false
	}
	awaitID := text[7:end]
	choice := strings.TrimSpace(text[end+1:])
	if awaitID == "" || choice == "" {
		return awaitChoice{}, false
	}
	return awaitChoice{AwaitID: awaitID, Choice: choice}, true
}

func normalizeWAHAIdentity(in string) string {
	in = strings.TrimSpace(in)
	switch {
	case strings.HasSuffix(in, "@c.us"), strings.HasSuffix(in, "@s.whatsapp.net"), strings.HasSuffix(in, "@lid"):
		return normalizePhoneish(strings.Split(in, "@")[0])
	default:
		return in
	}
}

func normalizeWAHAConversation(in string) string {
	in = strings.TrimSpace(in)
	switch {
	case strings.HasSuffix(in, "@g.us"), strings.HasSuffix(in, "@newsletter"):
		return in
	default:
		return normalizeWAHAIdentity(in)
	}
}

func normalizeMentionedIDs(ids []string, raw map[string]any) []string {
	for _, key := range []string{"mentionedIds", "mentioned_ids", "mentions"} {
		ids = append(ids, stringSliceFromAny(raw[key])...)
	}
	for _, path := range [][]string{{"_data", "mentionedJidList"}, {"_data", "mentionedIds"}, {"message", "extendedTextMessage", "contextInfo", "mentionedJid"}} {
		ids = append(ids, stringSliceFromAny(rawPath(raw, path...))...)
	}
	seen := map[string]struct{}{}
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		for _, candidate := range []string{strings.TrimSpace(id), normalizeWAHAIdentity(id)} {
			if candidate == "" {
				continue
			}
			key := strings.ToLower(candidate)
			if _, ok := seen[key]; ok {
				continue
			}
			seen[key] = struct{}{}
			out = append(out, candidate)
		}
	}
	return out
}

func mentionsAnyBot(mentionedIDs, botIDs []string) bool {
	bots := map[string]struct{}{}
	for _, id := range botIDs {
		for _, candidate := range []string{strings.TrimSpace(id), normalizeWAHAIdentity(id)} {
			if candidate != "" {
				bots[strings.ToLower(candidate)] = struct{}{}
			}
		}
	}
	if len(bots) == 0 {
		return false
	}
	for _, id := range mentionedIDs {
		if _, ok := bots[strings.ToLower(strings.TrimSpace(id))]; ok {
			return true
		}
	}
	return false
}

func rawStringPath(raw map[string]any, path ...string) string {
	if value, _ := rawPath(raw, path...).(string); strings.TrimSpace(value) != "" {
		return strings.TrimSpace(value)
	}
	return ""
}

func rawPath(raw map[string]any, path ...string) any {
	var cur any = raw
	for _, key := range path {
		obj, ok := cur.(map[string]any)
		if !ok {
			return nil
		}
		cur = obj[key]
	}
	return cur
}

func stringSliceFromAny(value any) []string {
	switch v := value.(type) {
	case []string:
		return v
	case []any:
		out := make([]string, 0, len(v))
		for _, item := range v {
			if s, _ := item.(string); strings.TrimSpace(s) != "" {
				out = append(out, strings.TrimSpace(s))
			}
		}
		return out
	default:
		return nil
	}
}

func normalizePhoneish(in string) string {
	var b strings.Builder
	for i, ch := range strings.TrimSpace(in) {
		if ch >= '0' && ch <= '9' {
			b.WriteRune(ch)
			continue
		}
		if ch == '+' && i == 0 {
			continue
		}
	}
	return b.String()
}

func (a Adapter) HydrateInboundArtifacts(ctx context.Context, evt *domain.CanonicalInboundEvent, store ports.InboundArtifactStore) error {
	if len(evt.Message.Artifacts) == 0 {
		return nil
	}
	out := make([]domain.Artifact, 0, len(evt.Message.Artifacts))
	for _, artifact := range evt.Message.Artifacts {
		content, err := a.downloadURL(ctx, artifact.SourceURL)
		if err != nil {
			return err
		}
		saved, err := store.SaveInbound(ctx, artifact.Name, artifact.MIMEType, content)
		if err != nil {
			return err
		}
		saved.ID = artifact.ID
		saved.SourceURL = artifact.SourceURL
		out = append(out, saved)
	}
	evt.Message.Artifacts = out
	return nil
}

func (a Adapter) downloadURL(ctx context.Context, rawURL string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("X-Api-Key", a.APIKey)
	resp, err := a.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("WAHA media download failed: status=%d body=%s", resp.StatusCode, string(body))
	}
	return io.ReadAll(resp.Body)
}

func (a Adapter) SendMessage(ctx context.Context, delivery domain.OutboundDelivery) (domain.DeliveryResult, error) {
	return a.send(ctx, delivery, false)
}

func (a Adapter) SendAwaitPrompt(ctx context.Context, delivery domain.OutboundDelivery) (domain.DeliveryResult, error) {
	return a.send(ctx, delivery, true)
}

func (a Adapter) send(ctx context.Context, delivery domain.OutboundDelivery, _ bool) (domain.DeliveryResult, error) {
	if strings.TrimSpace(a.BaseURL) == "" || strings.TrimSpace(a.Session) == "" {
		return domain.DeliveryResult{}, nil
	}
	var body map[string]any
	if err := json.Unmarshal(delivery.PayloadJSON, &body); err != nil {
		return domain.DeliveryResult{}, err
	}
	chatID, _ := body["chatId"].(string)
	if strings.TrimSpace(chatID) == "" {
		return domain.DeliveryResult{}, errors.New("missing WAHA chatId")
	}
	if err := a.enforceRateLimit(ctx, delivery.SessionID); err != nil {
		return domain.DeliveryResult{}, err
	}
	if err := a.enforceRecentInbound(ctx, delivery.SessionID); err != nil {
		return domain.DeliveryResult{}, err
	}
	if a.EnableAntiBlock {
		if a.EnableSeen {
			_ = a.postJSON(ctx, "/api/sendSeen", map[string]any{
				"session": a.Session,
				"chatId":  chatID,
			}, nil)
		}
		if a.EnableTyping {
			_ = a.postJSON(ctx, "/api/"+url.PathEscape(a.Session)+"/presence", map[string]any{
				"chatId":   chatID,
				"presence": "typing",
			}, nil)
		}
		delay := a.deliveryDelay(body)
		if delay > 0 && a.Sleep != nil {
			a.Sleep(delay)
		}
		if a.EnableTyping {
			_ = a.postJSON(ctx, "/api/"+url.PathEscape(a.Session)+"/presence", map[string]any{
				"chatId":   chatID,
				"presence": "paused",
			}, nil)
		}
	}
	switch kind, _ := body["kind"].(string); kind {
	case "artifact_upload":
		result, err := a.sendArtifact(ctx, body)
		if err != nil {
			return domain.DeliveryResult{}, err
		}
		a.markOffline(ctx)
		return result, nil
	default:
		var response struct {
			ID any `json:"id"`
		}
		if err := a.postJSON(ctx, "/api/sendText", map[string]any{
			"session":  a.Session,
			"chatId":   chatID,
			"text":     firstNonEmpty(asString(body["text"]), nestedString(body, "text", "body")),
			"reply_to": asString(body["reply_to"]),
		}, &response); err != nil {
			return domain.DeliveryResult{}, err
		}
		a.markOffline(ctx)
		return domain.DeliveryResult{ProviderMessageID: wahaMessageID(response.ID)}, nil
	}
}

func (a Adapter) enforceRateLimit(ctx context.Context, sessionID string) error {
	if a.CountSentDeliveriesSince == nil || strings.TrimSpace(sessionID) == "" {
		return nil
	}
	now := time.Now().UTC()
	if a.HourlyMessageCap > 0 {
		count, err := a.CountSentDeliveriesSince(ctx, sessionID, now.Add(-time.Hour))
		if err != nil {
			return err
		}
		if count >= a.HourlyMessageCap {
			return fmt.Errorf("whatsapp_web hourly delivery cap reached for session %s", sessionID)
		}
	}
	if a.BurstMessageCap > 0 && a.BurstWindow > 0 {
		count, err := a.CountSentDeliveriesSince(ctx, sessionID, now.Add(-a.BurstWindow))
		if err != nil {
			return err
		}
		if count >= a.BurstMessageCap {
			return fmt.Errorf("whatsapp_web burst delivery cap reached for session %s", sessionID)
		}
	}
	return nil
}

func (a Adapter) enforceRecentInbound(ctx context.Context, sessionID string) error {
	if !a.RequireRecentInbound || a.HasRecentInboundMessageSince == nil || strings.TrimSpace(sessionID) == "" || a.RecentInboundWindow <= 0 {
		return nil
	}
	ok, err := a.HasRecentInboundMessageSince(ctx, sessionID, time.Now().UTC().Add(-a.RecentInboundWindow))
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("whatsapp_web recent inbound activity required for session %s", sessionID)
	}
	return nil
}

func (a Adapter) deliveryDelay(body map[string]any) time.Duration {
	minDelay := a.MinDelay
	maxDelay := a.MaxDelay
	if maxDelay < minDelay {
		maxDelay = minDelay
	}
	text := firstNonEmpty(asString(body["text"]), nestedString(body, "text", "body"), asString(body["caption"]))
	extra := time.Duration(len([]rune(text))/48) * 250 * time.Millisecond
	delay := minDelay + extra
	if delay > maxDelay {
		delay = maxDelay
	}
	return delay
}

func (a Adapter) sendArtifact(ctx context.Context, body map[string]any) (domain.DeliveryResult, error) {
	storageURI := strings.TrimSpace(asString(body["storage_uri"]))
	if storageURI == "" {
		storageURI = strings.TrimSpace(asString(body["source_url"]))
	}
	content, err := readStorageURI(storageURI)
	if err != nil {
		return domain.DeliveryResult{}, err
	}
	fileName := firstNonEmpty(asString(body["file_name"]), filepath.Base(strings.TrimPrefix(storageURI, "file://")))
	mimeType := strings.TrimSpace(asString(body["mime_type"]))
	chatID := strings.TrimSpace(asString(body["chatId"]))
	if chatID == "" {
		chatID = strings.TrimSpace(asString(body["to"]))
	}
	var response struct {
		ID any `json:"id"`
	}
	payload := map[string]any{
		"session":  a.Session,
		"chatId":   chatID,
		"file":     bytesToDataURL(content, firstNonEmpty(mimeType, "application/octet-stream")),
		"fileName": fileName,
		"caption":  asString(body["caption"]),
	}
	if err := a.postJSON(ctx, "/api/sendFile", payload, &response); err != nil {
		return domain.DeliveryResult{}, err
	}
	return domain.DeliveryResult{ProviderMessageID: wahaMessageID(response.ID)}, nil
}

func (a Adapter) markOffline(ctx context.Context) {
	if !a.SetOfflineAfterSend {
		return
	}
	_ = a.postJSON(ctx, "/api/"+url.PathEscape(a.Session)+"/presence", map[string]any{
		"presence": "offline",
	}, nil)
}

func bytesToDataURL(content []byte, mimeType string) string {
	return "data:" + mimeType + ";base64," + encodeBase64(content)
}

func encodeBase64(content []byte) string {
	return (&base64Encoding{}).EncodeToString(content)
}

type base64Encoding struct{}

func (*base64Encoding) EncodeToString(src []byte) string {
	const table = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/"
	if len(src) == 0 {
		return ""
	}
	out := make([]byte, 0, ((len(src)+2)/3)*4)
	for len(src) >= 3 {
		out = append(out,
			table[src[0]>>2],
			table[((src[0]&0x03)<<4)|(src[1]>>4)],
			table[((src[1]&0x0f)<<2)|(src[2]>>6)],
			table[src[2]&0x3f],
		)
		src = src[3:]
	}
	switch len(src) {
	case 1:
		out = append(out,
			table[src[0]>>2],
			table[(src[0]&0x03)<<4],
			'=',
			'=',
		)
	case 2:
		out = append(out,
			table[src[0]>>2],
			table[((src[0]&0x03)<<4)|(src[1]>>4)],
			table[(src[1]&0x0f)<<2],
			'=',
		)
	}
	return string(out)
}

func readStorageURI(uri string) ([]byte, error) {
	if strings.HasPrefix(uri, "file://") {
		return os.ReadFile(strings.TrimPrefix(uri, "file://"))
	}
	return nil, fmt.Errorf("unsupported storage uri: %s", uri)
}

func (a Adapter) postJSON(ctx context.Context, path string, payload any, out any) error {
	return a.writeJSON(ctx, http.MethodPost, path, payload, out)
}

func (a Adapter) putJSON(ctx context.Context, path string, payload any, out any) error {
	return a.writeJSON(ctx, http.MethodPut, path, payload, out)
}

func (a Adapter) writeJSON(ctx context.Context, method, path string, payload any, out any) error {
	raw, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, method, a.BaseURL+path, bytes.NewReader(raw))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if a.APIKey != "" {
		req.Header.Set("X-Api-Key", a.APIKey)
	}
	resp, err := a.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("WAHA request failed: path=%s status=%d body=%s", path, resp.StatusCode, string(body))
	}
	if out == nil {
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

func (a Adapter) getJSON(ctx context.Context, path string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, a.BaseURL+path, nil)
	if err != nil {
		return err
	}
	if a.APIKey != "" {
		req.Header.Set("X-Api-Key", a.APIKey)
	}
	resp, err := a.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("WAHA request failed: path=%s status=%d body=%s", path, resp.StatusCode, string(body))
	}
	if out == nil {
		return nil
	}
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if status, ok := out.(*SessionStatus); ok {
		status.RawJSON = append(status.RawJSON[:0], raw...)
	}
	return json.Unmarshal(raw, out)
}

func (a Adapter) GetSessionStatus(ctx context.Context) (SessionStatus, error) {
	var status SessionStatus
	err := a.getJSON(ctx, "/api/sessions/"+url.PathEscape(a.Session), &status)
	return status, err
}

func (a Adapter) EnsureSession(ctx context.Context) (SessionStatus, error) {
	status, err := a.GetSessionStatus(ctx)
	if err == nil {
		return status, nil
	}
	payload := map[string]any{
		"name":  a.Session,
		"start": true,
		"config": map[string]any{
			"webhooks": []map[string]any{a.webhookConfig()},
		},
	}
	if a.Engine != "" {
		payload["config"].(map[string]any)["engine"] = a.Engine
	}
	var created SessionStatus
	if postErr := a.postJSON(ctx, "/api/sessions", payload, &created); postErr != nil {
		return SessionStatus{}, postErr
	}
	return created, nil
}

func (a Adapter) StartSession(ctx context.Context) (SessionStatus, error) {
	if _, err := a.EnsureSession(ctx); err != nil {
		return SessionStatus{}, err
	}
	if err := a.postJSON(ctx, "/api/sessions/"+url.PathEscape(a.Session)+"/start", map[string]any{}, nil); err != nil {
		if !strings.Contains(err.Error(), "already started") {
			return SessionStatus{}, err
		}
	}
	return a.GetSessionStatus(ctx)
}

func (a Adapter) StopSession(ctx context.Context) (SessionStatus, error) {
	if err := a.postJSON(ctx, "/api/sessions/"+url.PathEscape(a.Session)+"/stop", map[string]any{}, nil); err != nil {
		return SessionStatus{}, err
	}
	return a.GetSessionStatus(ctx)
}

func (a Adapter) SyncWebhook(ctx context.Context) (SessionStatus, error) {
	payload := map[string]any{
		"name":   a.Session,
		"config": map[string]any{"webhooks": []map[string]any{a.webhookConfig()}},
	}
	if a.Engine != "" {
		payload["config"].(map[string]any)["engine"] = a.Engine
	}
	var status SessionStatus
	if err := a.postJSON(ctx, "/api/sessions", payload, &status); err != nil {
		if !strings.Contains(err.Error(), "already exists") {
			return SessionStatus{}, err
		}
		if err := a.putJSON(ctx, "/api/sessions/"+url.PathEscape(a.Session), payload, &status); err != nil {
			return SessionStatus{}, err
		}
	}
	return status, nil
}

func (a Adapter) webhookConfig() map[string]any {
	cfg := map[string]any{
		"url":    a.PublicBaseURL + "/webhooks/whatsapp-web",
		"events": []string{"message"},
	}
	if a.WebhookSecret != "" {
		cfg["hmac"] = map[string]any{"key": a.WebhookSecret}
	}
	return cfg
}

func (a Adapter) GetQRCode(ctx context.Context) (map[string]any, error) {
	var out map[string]any
	err := a.getJSON(ctx, "/api/"+url.PathEscape(a.Session)+"/auth/qr?format=raw", &out)
	return out, err
}

func asString(v any) string {
	s, _ := v.(string)
	return s
}

func nestedString(body map[string]any, key, nested string) string {
	v, ok := body[key].(map[string]any)
	if !ok {
		return ""
	}
	return asString(v[nested])
}

func wahaMessageID(raw any) string {
	switch v := raw.(type) {
	case string:
		return strings.TrimSpace(v)
	case map[string]any:
		for _, key := range []string{"_serialized", "serialized", "id"} {
			if id := strings.TrimSpace(asString(v[key])); id != "" {
				return id
			}
		}
	}
	return ""
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
