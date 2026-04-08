package whatsapp

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"nexus/internal/domain"
	"nexus/internal/ports"
)

type Adapter struct {
	VerifyToken   string
	AccessToken   string
	AppSecret     string
	PhoneNumberID string
	APIBaseURL    string
	HTTP          *http.Client
	MaxMediaBytes int64
}

func New(verifyToken, accessToken, appSecret, phoneNumberID, apiBaseURL string) Adapter {
	return Adapter{
		VerifyToken:   verifyToken,
		AccessToken:   accessToken,
		AppSecret:     appSecret,
		PhoneNumberID: phoneNumberID,
		APIBaseURL:    strings.TrimRight(apiBaseURL, "/"),
		HTTP:          &http.Client{Timeout: 10 * time.Second},
		MaxMediaBytes: 10 << 20,
	}
}

func (a Adapter) Channel() string { return "whatsapp" }

func (a Adapter) VerifyInbound(_ context.Context, r *http.Request, body []byte) error {
	if r.Method == http.MethodGet {
		return nil
	}
	if a.AppSecret == "" {
		return nil
	}
	got := r.Header.Get("X-Hub-Signature-256")
	if got == "" {
		return errors.New("missing whatsapp signature")
	}
	if !strings.HasPrefix(got, "sha256=") {
		return errors.New("invalid whatsapp signature format")
	}
	sigHex := strings.TrimPrefix(got, "sha256=")
	expectedMAC, err := hex.DecodeString(sigHex)
	if err != nil {
		return errors.New("invalid whatsapp signature")
	}
	mac := hmac.New(sha256.New, []byte(a.AppSecret))
	_, _ = mac.Write(body)
	if !hmac.Equal(mac.Sum(nil), expectedMAC) {
		return errors.New("invalid whatsapp signature")
	}
	return nil
}

func (a Adapter) ParseInbound(_ context.Context, _ *http.Request, body []byte, tenantID string) (domain.CanonicalInboundEvent, error) {
	events, err := a.ParseInboundBatch(context.Background(), nil, body, tenantID)
	if err != nil {
		return domain.CanonicalInboundEvent{}, err
	}
	if len(events) == 0 {
		return domain.CanonicalInboundEvent{}, errors.New("unsupported whatsapp payload")
	}
	return events[0], nil
}

func (a Adapter) ParseInboundBatch(_ context.Context, _ *http.Request, body []byte, tenantID string) ([]domain.CanonicalInboundEvent, error) {
	var envelope struct {
		Entry []struct {
			ID      string `json:"id"`
			Changes []struct {
				Field string `json:"field"`
				Value struct {
					Metadata struct {
						PhoneNumberID string `json:"phone_number_id"`
					} `json:"metadata"`
					Contacts []struct {
						WaID    string `json:"wa_id"`
						Profile struct {
							Name string `json:"name"`
						} `json:"profile"`
					} `json:"contacts"`
					Messages []struct {
						ID        string `json:"id"`
						From      string `json:"from"`
						Timestamp string `json:"timestamp"`
						Type      string `json:"type"`
						Text      struct {
							Body string `json:"body"`
						} `json:"text"`
						Button *struct {
							Text    string `json:"text"`
							Payload string `json:"payload"`
						} `json:"button"`
						Interactive *struct {
							ButtonReply *struct {
								ID    string `json:"id"`
								Title string `json:"title"`
							} `json:"button_reply"`
							ListReply *struct {
								ID    string `json:"id"`
								Title string `json:"title"`
							} `json:"list_reply"`
						} `json:"interactive"`
						Image *struct {
							ID       string `json:"id"`
							MimeType string `json:"mime_type"`
							SHA256   string `json:"sha256"`
						} `json:"image"`
						Document *struct {
							ID       string `json:"id"`
							Filename string `json:"filename"`
							MimeType string `json:"mime_type"`
							SHA256   string `json:"sha256"`
						} `json:"document"`
						Audio *struct {
							ID       string `json:"id"`
							MimeType string `json:"mime_type"`
							SHA256   string `json:"sha256"`
						} `json:"audio"`
					} `json:"messages"`
				} `json:"value"`
			} `json:"changes"`
		} `json:"entry"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		return nil, err
	}
	events := make([]domain.CanonicalInboundEvent, 0)
	for _, entry := range envelope.Entry {
		for _, change := range entry.Changes {
			if a.PhoneNumberID != "" && change.Value.Metadata.PhoneNumberID != "" && change.Value.Metadata.PhoneNumberID != a.PhoneNumberID {
				return nil, errors.New("unexpected whatsapp phone number id")
			}
			for _, msg := range change.Value.Messages {
				displayName := ""
				if len(change.Value.Contacts) > 0 {
					displayName = change.Value.Contacts[0].Profile.Name
				}
				evt, err := parseMessage(body, tenantID, displayName, change.Value.Metadata.PhoneNumberID, msg)
				if err != nil {
					return nil, err
				}
				events = append(events, evt)
			}
		}
	}
	if len(events) == 0 {
		return nil, errors.New("unsupported whatsapp payload")
	}
	return events, nil
}

func parseMessage(raw []byte, tenantID, displayName, phoneNumberID string, msg struct {
	ID        string `json:"id"`
	From      string `json:"from"`
	Timestamp string `json:"timestamp"`
	Type      string `json:"type"`
	Text      struct {
		Body string `json:"body"`
	} `json:"text"`
	Button *struct {
		Text    string `json:"text"`
		Payload string `json:"payload"`
	} `json:"button"`
	Interactive *struct {
		ButtonReply *struct {
			ID    string `json:"id"`
			Title string `json:"title"`
		} `json:"button_reply"`
		ListReply *struct {
			ID    string `json:"id"`
			Title string `json:"title"`
		} `json:"list_reply"`
	} `json:"interactive"`
	Image *struct {
		ID       string `json:"id"`
		MimeType string `json:"mime_type"`
		SHA256   string `json:"sha256"`
	} `json:"image"`
	Document *struct {
		ID       string `json:"id"`
		Filename string `json:"filename"`
		MimeType string `json:"mime_type"`
		SHA256   string `json:"sha256"`
	} `json:"document"`
	Audio *struct {
		ID       string `json:"id"`
		MimeType string `json:"mime_type"`
		SHA256   string `json:"sha256"`
	} `json:"audio"`
}) (domain.CanonicalInboundEvent, error) {
	receivedAt := time.Now().UTC()
	if ts, err := strconvParseInt(msg.Timestamp); err == nil && ts > 0 {
		receivedAt = time.Unix(ts, 0).UTC()
	}
	evt := domain.CanonicalInboundEvent{
		EventID:         "wa_" + msg.ID,
		TenantID:        tenantID,
		Channel:         "whatsapp",
		Interaction:     "message",
		ProviderEventID: msg.ID,
		ReceivedAt:      receivedAt,
		Sender: domain.Sender{
			ChannelUserID:       msg.From,
			DisplayName:         displayName,
			IsAuthenticated:     true,
			IdentityAssurance:   "provider_verified",
			AllowedResponderIDs: []string{msg.From},
		},
		Conversation: domain.Conversation{
			ChannelConversationID: msg.From,
			ChannelThreadID:       msg.From,
			ChannelSurfaceKey:     msg.From,
		},
		Metadata: domain.Metadata{
			ArtifactTrust: "trusted-channel-ingress",
			ResponderBinding: domain.ResponderBinding{
				Mode:                  "same-user-only",
				AllowedChannelUserIDs: []string{msg.From},
			},
			RawPayload: raw,
		},
	}
	switch {
	case msg.Button != nil:
		payload, err := parseAwaitChoice(msg.Button.Payload)
		if err != nil {
			return domain.CanonicalInboundEvent{}, err
		}
		resumePayload, err := json.Marshal(map[string]string{"choice": payload.Choice})
		if err != nil {
			return domain.CanonicalInboundEvent{}, err
		}
		evt.Interaction = "await_response"
		evt.Message = domain.Message{
			MessageID:   "wa_btn_" + msg.ID,
			MessageType: "interactive",
			Text:        payload.Choice,
			Parts:       []domain.Part{{ContentType: "application/json", Content: string(resumePayload)}},
		}
		evt.Metadata.AwaitID = payload.AwaitID
		evt.Metadata.ResumePayload = resumePayload
	case msg.Interactive != nil && msg.Interactive.ButtonReply != nil:
		choiceID, err := parseAwaitChoiceID(msg.Interactive.ButtonReply.ID)
		if err != nil {
			return domain.CanonicalInboundEvent{}, err
		}
		resumePayload, err := json.Marshal(map[string]string{"choice": choiceID.Choice})
		if err != nil {
			return domain.CanonicalInboundEvent{}, err
		}
		evt.Interaction = "await_response"
		evt.Message = domain.Message{
			MessageID:   "wa_interactive_" + msg.ID,
			MessageType: "interactive",
			Text:        choiceID.Choice,
			Parts:       []domain.Part{{ContentType: "application/json", Content: string(resumePayload)}},
		}
		evt.Metadata.AwaitID = choiceID.AwaitID
		evt.Metadata.ResumePayload = resumePayload
	case msg.Interactive != nil && msg.Interactive.ListReply != nil:
		choiceID, err := parseAwaitChoiceID(msg.Interactive.ListReply.ID)
		if err != nil {
			return domain.CanonicalInboundEvent{}, err
		}
		resumePayload, err := json.Marshal(map[string]string{"choice": choiceID.Choice})
		if err != nil {
			return domain.CanonicalInboundEvent{}, err
		}
		evt.Interaction = "await_response"
		evt.Message = domain.Message{
			MessageID:   "wa_interactive_" + msg.ID,
			MessageType: "interactive",
			Text:        choiceID.Choice,
			Parts:       []domain.Part{{ContentType: "application/json", Content: string(resumePayload)}},
		}
		evt.Metadata.AwaitID = choiceID.AwaitID
		evt.Metadata.ResumePayload = resumePayload
	default:
		if parsed, ok := parseAwaitTextReply(msg.Text.Body); ok {
			resumePayload, err := json.Marshal(map[string]string{"choice": parsed.Choice})
			if err != nil {
				return domain.CanonicalInboundEvent{}, err
			}
			evt.Interaction = "await_response"
			evt.Message = domain.Message{
				MessageID:   "wa_text_" + msg.ID,
				MessageType: "interactive",
				Text:        parsed.Choice,
				Parts:       []domain.Part{{ContentType: "application/json", Content: string(resumePayload)}},
			}
			evt.Metadata.AwaitID = parsed.AwaitID
			evt.Metadata.ResumePayload = resumePayload
			break
		}
		text := strings.TrimSpace(msg.Text.Body)
		artifacts := whatsappArtifacts(msg)
		evt.Message = domain.Message{
			MessageID:   "wa_msg_" + msg.ID,
			MessageType: messageType(text, artifacts),
			Text:        text,
			Parts:       []domain.Part{{ContentType: "text/plain", Content: text}},
			Artifacts:   artifacts,
		}
	}
	_ = phoneNumberID
	return evt, nil
}

type awaitChoice struct {
	AwaitID string `json:"await_id"`
	Choice  string `json:"choice"`
}

func parseAwaitChoice(payload string) (awaitChoice, error) {
	var parsed awaitChoice
	if err := json.Unmarshal([]byte(payload), &parsed); err != nil {
		return awaitChoice{}, err
	}
	if parsed.AwaitID == "" || parsed.Choice == "" {
		return awaitChoice{}, errors.New("invalid whatsapp await payload")
	}
	return parsed, nil
}

func parseAwaitChoiceID(value string) (awaitChoice, error) {
	parts := strings.SplitN(value, ":", 3)
	if len(parts) != 3 || parts[0] != "await" {
		return awaitChoice{}, errors.New("invalid whatsapp choice id")
	}
	return awaitChoice{AwaitID: parts[1], Choice: parts[2]}, nil
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

func whatsappArtifacts(msg struct {
	ID        string `json:"id"`
	From      string `json:"from"`
	Timestamp string `json:"timestamp"`
	Type      string `json:"type"`
	Text      struct {
		Body string `json:"body"`
	} `json:"text"`
	Button *struct {
		Text    string `json:"text"`
		Payload string `json:"payload"`
	} `json:"button"`
	Interactive *struct {
		ButtonReply *struct {
			ID    string `json:"id"`
			Title string `json:"title"`
		} `json:"button_reply"`
		ListReply *struct {
			ID    string `json:"id"`
			Title string `json:"title"`
		} `json:"list_reply"`
	} `json:"interactive"`
	Image *struct {
		ID       string `json:"id"`
		MimeType string `json:"mime_type"`
		SHA256   string `json:"sha256"`
	} `json:"image"`
	Document *struct {
		ID       string `json:"id"`
		Filename string `json:"filename"`
		MimeType string `json:"mime_type"`
		SHA256   string `json:"sha256"`
	} `json:"document"`
	Audio *struct {
		ID       string `json:"id"`
		MimeType string `json:"mime_type"`
		SHA256   string `json:"sha256"`
	} `json:"audio"`
}) []domain.Artifact {
	var artifacts []domain.Artifact
	if msg.Image != nil {
		artifacts = append(artifacts, domain.Artifact{ID: msg.Image.ID, Name: msg.Image.ID + ".bin", MIMEType: msg.Image.MimeType, SHA256: msg.Image.SHA256, SourceURL: "whatsapp-media:" + msg.Image.ID})
	}
	if msg.Document != nil {
		artifacts = append(artifacts, domain.Artifact{ID: msg.Document.ID, Name: msg.Document.Filename, MIMEType: msg.Document.MimeType, SHA256: msg.Document.SHA256, SourceURL: "whatsapp-media:" + msg.Document.ID})
	}
	if msg.Audio != nil {
		artifacts = append(artifacts, domain.Artifact{ID: msg.Audio.ID, Name: msg.Audio.ID + ".audio", MIMEType: msg.Audio.MimeType, SHA256: msg.Audio.SHA256, SourceURL: "whatsapp-media:" + msg.Audio.ID})
	}
	return artifacts
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

func (a Adapter) SendMessage(ctx context.Context, delivery domain.OutboundDelivery) (domain.DeliveryResult, error) {
	return a.send(ctx, delivery.PayloadJSON)
}

func (a Adapter) SendAwaitPrompt(ctx context.Context, delivery domain.OutboundDelivery) (domain.DeliveryResult, error) {
	return a.send(ctx, delivery.PayloadJSON)
}

func (a Adapter) send(ctx context.Context, payload []byte) (domain.DeliveryResult, error) {
	if a.AccessToken == "" || a.PhoneNumberID == "" {
		return domain.DeliveryResult{}, nil
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, a.APIBaseURL+"/"+a.PhoneNumberID+"/messages", bytes.NewReader(payload))
	if err != nil {
		return domain.DeliveryResult{}, err
	}
	req.Header.Set("Authorization", "Bearer "+a.AccessToken)
	req.Header.Set("Content-Type", "application/json")
	resp, err := a.HTTP.Do(req)
	if err != nil {
		return domain.DeliveryResult{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return domain.DeliveryResult{}, fmt.Errorf("whatsapp send failed: status=%d body=%s", resp.StatusCode, string(raw))
	}
	var out struct {
		Messages []struct {
			ID string `json:"id"`
		} `json:"messages"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return domain.DeliveryResult{}, err
	}
	if len(out.Messages) == 0 {
		return domain.DeliveryResult{}, errors.New("whatsapp response missing message id")
	}
	return domain.DeliveryResult{ProviderMessageID: out.Messages[0].ID}, nil
}

func (a Adapter) HydrateInboundArtifacts(ctx context.Context, evt *domain.CanonicalInboundEvent, store ports.InboundArtifactStore) error {
	if len(evt.Message.Artifacts) == 0 {
		return nil
	}
	stored := make([]domain.Artifact, 0, len(evt.Message.Artifacts))
	for _, artifact := range evt.Message.Artifacts {
		content, err := a.downloadMedia(ctx, artifact.ID)
		if err != nil {
			return err
		}
		saved, err := store.SaveInbound(ctx, artifact.Name, artifact.MIMEType, content)
		if err != nil {
			return err
		}
		saved.ID = artifact.ID
		saved.SourceURL = artifact.SourceURL
		stored = append(stored, saved)
	}
	evt.Message.Artifacts = stored
	return nil
}

func (a Adapter) downloadMedia(ctx context.Context, mediaID string) ([]byte, error) {
	if a.AccessToken == "" {
		return nil, errors.New("missing whatsapp access token")
	}
	metaReq, err := http.NewRequestWithContext(ctx, http.MethodGet, a.APIBaseURL+"/"+mediaID, nil)
	if err != nil {
		return nil, err
	}
	metaReq.Header.Set("Authorization", "Bearer "+a.AccessToken)
	metaResp, err := a.HTTP.Do(metaReq)
	if err != nil {
		return nil, err
	}
	defer metaResp.Body.Close()
	if metaResp.StatusCode >= 300 {
		raw, _ := io.ReadAll(io.LimitReader(metaResp.Body, 4096))
		return nil, fmt.Errorf("whatsapp media lookup failed: status=%d body=%s", metaResp.StatusCode, string(raw))
	}
	var meta struct {
		URL string `json:"url"`
	}
	if err := json.NewDecoder(metaResp.Body).Decode(&meta); err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, meta.URL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+a.AccessToken)
	resp, err := a.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("whatsapp media download failed: status=%d body=%s", resp.StatusCode, string(raw))
	}
	if a.MaxMediaBytes > 0 && resp.ContentLength > a.MaxMediaBytes {
		return nil, fmt.Errorf("whatsapp media too large: %d", resp.ContentLength)
	}
	content, err := io.ReadAll(io.LimitReader(resp.Body, maxInt64(a.MaxMediaBytes, 1)+1))
	if err != nil {
		return nil, err
	}
	if a.MaxMediaBytes > 0 && int64(len(content)) > a.MaxMediaBytes {
		return nil, errors.New("whatsapp media too large")
	}
	return content, nil
}

func (a Adapter) WriteVerification(w http.ResponseWriter, r *http.Request) bool {
	if r.Method != http.MethodGet {
		return false
	}
	query := r.URL.Query()
	if query.Get("hub.mode") != "subscribe" || query.Get("hub.verify_token") != a.VerifyToken {
		http.Error(w, "forbidden", http.StatusForbidden)
		return true
	}
	_, _ = io.WriteString(w, query.Get("hub.challenge"))
	return true
}

func strconvParseInt(in string) (int64, error) {
	var n int64
	for _, ch := range in {
		if ch < '0' || ch > '9' {
			return 0, fmt.Errorf("invalid integer %q", in)
		}
		n = n*10 + int64(ch-'0')
	}
	return n, nil
}

func readStorageURI(uri string) ([]byte, error) {
	if !strings.HasPrefix(uri, "file://") {
		return nil, fmt.Errorf("unsupported storage uri: %s", uri)
	}
	return os.ReadFile(strings.TrimPrefix(uri, "file://"))
}

func maxInt64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}
