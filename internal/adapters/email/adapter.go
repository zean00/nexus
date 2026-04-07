package email

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"mime"
	"net/http"
	"net/mail"
	"net/smtp"
	"os"
	"strings"
	"time"

	"nexus/internal/domain"
	"nexus/internal/ports"
)

type Adapter struct {
	WebhookSecret string
	SMTPAddr      string
	SMTPUsername  string
	SMTPPassword  string
	FromAddress   string
}

func New(webhookSecret, smtpAddr, smtpUsername, smtpPassword, fromAddress string) Adapter {
	return Adapter{
		WebhookSecret: webhookSecret,
		SMTPAddr:      smtpAddr,
		SMTPUsername:  smtpUsername,
		SMTPPassword:  smtpPassword,
		FromAddress:   fromAddress,
	}
}

func (a Adapter) Channel() string { return "email" }

func (a Adapter) VerifyInbound(_ context.Context, r *http.Request, _ []byte) error {
	if a.WebhookSecret == "" {
		return nil
	}
	if r.Header.Get("X-Nexus-Email-Secret") != a.WebhookSecret {
		return errors.New("invalid email webhook secret")
	}
	return nil
}

type inboundAttachment struct {
	ID            string `json:"id"`
	Name          string `json:"name"`
	MIMEType      string `json:"mime_type"`
	ContentBase64 string `json:"content_base64"`
}

type inboundPayload struct {
	EventID     string              `json:"event_id"`
	MessageID   string              `json:"message_id"`
	From        string              `json:"from"`
	FromName    string              `json:"from_name"`
	Subject     string              `json:"subject"`
	Text        string              `json:"text"`
	HTML        string              `json:"html"`
	ThreadID    string              `json:"thread_id"`
	InReplyTo   string              `json:"in_reply_to"`
	References  []string            `json:"references"`
	Headers     map[string]string   `json:"headers"`
	AwaitID     string              `json:"await_id"`
	Attachments []inboundAttachment `json:"attachments"`
}

func (a Adapter) ParseInbound(_ context.Context, _ *http.Request, body []byte, tenantID string) (domain.CanonicalInboundEvent, error) {
	var payload inboundPayload
	if err := json.Unmarshal(body, &payload); err != nil {
		return domain.CanonicalInboundEvent{}, err
	}
	fromAddr, err := mail.ParseAddress(payload.From)
	if err != nil {
		return domain.CanonicalInboundEvent{}, err
	}
	threadID := strings.TrimSpace(payload.ThreadID)
	if threadID == "" {
		threadID = firstNonEmpty(payload.InReplyTo, payload.MessageID)
	}
	if threadID == "" {
		return domain.CanonicalInboundEvent{}, errors.New("missing email thread id")
	}
	text := strings.TrimSpace(payload.Text)
	if text == "" {
		text = stripHTML(payload.HTML)
	}
	normalizedSender := strings.ToLower(fromAddr.Address)
	subject := strings.TrimSpace(payload.Subject)
	parts := []domain.Part{{ContentType: "text/plain", Content: text}}
	if payload.HTML != "" {
		parts = append(parts, domain.Part{ContentType: "text/html", Content: payload.HTML})
	}
	artifacts := make([]domain.Artifact, 0, len(payload.Attachments))
	for _, attachment := range payload.Attachments {
		name := attachment.Name
		if name == "" {
			name = attachment.ID
		}
		artifacts = append(artifacts, domain.Artifact{
			ID:        attachment.ID,
			Name:      name,
			MIMEType:  attachment.MIMEType,
			SourceURL: "email-attachment:" + attachment.ID,
		})
	}
	event := domain.CanonicalInboundEvent{
		EventID:         firstNonEmpty(payload.EventID, "email_"+sanitizeHeaderID(payload.MessageID)),
		TenantID:        tenantID,
		Channel:         "email",
		Interaction:     "message",
		ProviderEventID: firstNonEmpty(payload.MessageID, payload.EventID),
		ReceivedAt:      time.Now().UTC(),
		Sender: domain.Sender{
			ChannelUserID:       normalizedSender,
			DisplayName:         firstNonEmpty(payload.FromName, fromAddr.Name),
			IsAuthenticated:     true,
			IdentityAssurance:   "provider_verified",
			AllowedResponderIDs: []string{normalizedSender},
		},
		Conversation: domain.Conversation{
			ChannelConversationID: normalizedSender,
			ChannelThreadID:       threadID,
			ChannelSurfaceKey:     normalizedSender + "|" + threadID,
		},
		Message: domain.Message{
			MessageID:   sanitizeHeaderID(payload.MessageID),
			MessageType: emailMessageType(text, artifacts),
			Text:        joinSubjectAndText(subject, text),
			Parts:       parts,
			Artifacts:   artifacts,
		},
		Metadata: domain.Metadata{
			ArtifactTrust: "trusted-channel-ingress",
			ResponderBinding: domain.ResponderBinding{
				Mode:                  "same-user-only",
				AllowedChannelUserIDs: []string{normalizedSender},
			},
			RawPayload: body,
		},
	}
	awaitID := firstNonEmpty(payload.AwaitID, extractAwaitID(subject, text))
	if awaitID != "" {
		resumePayload, err := json.Marshal(map[string]string{"reply": text})
		if err != nil {
			return domain.CanonicalInboundEvent{}, err
		}
		event.Interaction = "await_response"
		event.Metadata.AwaitID = awaitID
		event.Metadata.ResumePayload = resumePayload
	}
	return event, nil
}

func (a Adapter) HydrateInboundArtifacts(ctx context.Context, evt *domain.CanonicalInboundEvent, store ports.InboundArtifactStore) error {
	if len(evt.Message.Artifacts) == 0 {
		return nil
	}
	var payload inboundPayload
	if err := json.Unmarshal(evt.Metadata.RawPayload, &payload); err != nil {
		return err
	}
	attachments := make(map[string]inboundAttachment, len(payload.Attachments))
	for _, attachment := range payload.Attachments {
		attachments[attachment.ID] = attachment
	}
	stored := make([]domain.Artifact, 0, len(evt.Message.Artifacts))
	for _, artifact := range evt.Message.Artifacts {
		src, ok := attachments[artifact.ID]
		if !ok {
			return fmt.Errorf("missing email attachment %s", artifact.ID)
		}
		content, err := base64.StdEncoding.DecodeString(src.ContentBase64)
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

func (a Adapter) SendMessage(ctx context.Context, delivery domain.OutboundDelivery) (domain.DeliveryResult, error) {
	return a.send(ctx, delivery.PayloadJSON)
}

func (a Adapter) SendAwaitPrompt(ctx context.Context, delivery domain.OutboundDelivery) (domain.DeliveryResult, error) {
	return a.send(ctx, delivery.PayloadJSON)
}

func (a Adapter) SendMail(ctx context.Context, to, subject, text, html string) (domain.DeliveryResult, error) {
	payload, err := json.Marshal(map[string]any{
		"to":      to,
		"subject": subject,
		"text":    text,
		"html":    html,
	})
	if err != nil {
		return domain.DeliveryResult{}, err
	}
	return a.send(ctx, payload)
}

func (a Adapter) send(_ context.Context, payload []byte) (domain.DeliveryResult, error) {
	if a.SMTPAddr == "" {
		return domain.DeliveryResult{}, nil
	}
	var body map[string]any
	if err := json.Unmarshal(payload, &body); err != nil {
		return domain.DeliveryResult{}, err
	}
	to, _ := body["to"].(string)
	subject, _ := body["subject"].(string)
	text, _ := body["text"].(string)
	html, _ := body["html"].(string)
	if to == "" {
		return domain.DeliveryResult{}, errors.New("missing email recipient")
	}
	raw, messageID, err := buildMessage(a.FromAddress, to, subject, text, html, body)
	if err != nil {
		return domain.DeliveryResult{}, err
	}
	host := a.SMTPAddr
	if idx := strings.Index(host, ":"); idx >= 0 {
		host = host[:idx]
	}
	var auth smtp.Auth
	if a.SMTPUsername != "" {
		auth = smtp.PlainAuth("", a.SMTPUsername, a.SMTPPassword, host)
	}
	if err := smtp.SendMail(a.SMTPAddr, auth, a.FromAddress, []string{to}, raw); err != nil {
		return domain.DeliveryResult{}, err
	}
	return domain.DeliveryResult{ProviderMessageID: messageID, ProviderRequestID: to}, nil
}

func buildMessage(from, to, subject, text, html string, payload map[string]any) ([]byte, string, error) {
	messageID := fmt.Sprintf("<nexus-%d@local>", time.Now().UTC().UnixNano())
	var body bytes.Buffer
	headers := map[string]string{
		"From":         from,
		"To":           to,
		"Subject":      subject,
		"Message-ID":   messageID,
		"MIME-Version": "1.0",
	}
	if threadID, _ := payload["thread_id"].(string); threadID != "" {
		headers["In-Reply-To"] = threadID
		headers["References"] = threadID
	}
	storageURI, _ := payload["storage_uri"].(string)
	fileName, _ := payload["file_name"].(string)
	if kind, _ := payload["kind"].(string); kind == "artifact_upload" && storageURI != "" {
		boundary := fmt.Sprintf("mixed_%d", time.Now().UTC().UnixNano())
		headers["Content-Type"] = "multipart/mixed; boundary=" + boundary
		writeHeaders(&body, headers)
		fmt.Fprintf(&body, "--%s\r\nContent-Type: text/plain; charset=utf-8\r\n\r\n%s\r\n", boundary, text)
		content, err := readStorageURI(storageURI)
		if err != nil {
			return nil, "", err
		}
		mimeType := mime.TypeByExtension(filepathExt(fileName))
		if mimeType == "" {
			mimeType = "application/octet-stream"
		}
		fmt.Fprintf(&body, "--%s\r\nContent-Type: %s\r\nContent-Transfer-Encoding: base64\r\nContent-Disposition: attachment; filename=%q\r\n\r\n%s\r\n--%s--\r\n", boundary, mimeType, fileName, base64.StdEncoding.EncodeToString(content), boundary)
		return body.Bytes(), messageID, nil
	}
	if html != "" {
		boundary := fmt.Sprintf("alt_%d", time.Now().UTC().UnixNano())
		headers["Content-Type"] = "multipart/alternative; boundary=" + boundary
		writeHeaders(&body, headers)
		fmt.Fprintf(&body, "--%s\r\nContent-Type: text/plain; charset=utf-8\r\n\r\n%s\r\n", boundary, text)
		fmt.Fprintf(&body, "--%s\r\nContent-Type: text/html; charset=utf-8\r\n\r\n%s\r\n--%s--\r\n", boundary, html, boundary)
		return body.Bytes(), messageID, nil
	}
	headers["Content-Type"] = "text/plain; charset=utf-8"
	writeHeaders(&body, headers)
	body.WriteString(text)
	return body.Bytes(), messageID, nil
}

func writeHeaders(body *bytes.Buffer, headers map[string]string) {
	order := []string{"From", "To", "Subject", "Message-ID", "In-Reply-To", "References", "MIME-Version", "Content-Type"}
	for _, key := range order {
		if value := headers[key]; value != "" {
			fmt.Fprintf(body, "%s: %s\r\n", key, value)
		}
	}
	body.WriteString("\r\n")
}

func emailMessageType(text string, artifacts []domain.Artifact) string {
	switch {
	case len(artifacts) > 0 && text != "":
		return "mixed"
	case len(artifacts) > 0:
		return "artifact"
	default:
		return "text"
	}
}

func joinSubjectAndText(subject, text string) string {
	if subject == "" {
		return text
	}
	if text == "" {
		return subject
	}
	return subject + "\n\n" + text
}

func extractAwaitID(subject, text string) string {
	for _, candidate := range []string{subject, text} {
		start := strings.Index(candidate, "[await:")
		if start < 0 {
			continue
		}
		rest := candidate[start+7:]
		end := strings.Index(rest, "]")
		if end > 0 {
			return rest[:end]
		}
	}
	return ""
}

func sanitizeHeaderID(in string) string {
	in = strings.TrimSpace(in)
	in = strings.TrimPrefix(in, "<")
	in = strings.TrimSuffix(in, ">")
	in = strings.ReplaceAll(in, "@", "_")
	in = strings.ReplaceAll(in, "/", "_")
	return strings.ReplaceAll(in, ":", "_")
}

func stripHTML(in string) string {
	var out strings.Builder
	inTag := false
	for _, ch := range in {
		switch ch {
		case '<':
			inTag = true
		case '>':
			inTag = false
		default:
			if !inTag {
				out.WriteRune(ch)
			}
		}
	}
	return strings.TrimSpace(out.String())
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func filepathExt(path string) string {
	if idx := strings.LastIndex(path, "."); idx >= 0 {
		return path[idx:]
	}
	return ""
}

func readStorageURI(uri string) ([]byte, error) {
	if !strings.HasPrefix(uri, "file://") {
		return nil, fmt.Errorf("unsupported storage uri: %s", uri)
	}
	return os.ReadFile(strings.TrimPrefix(uri, "file://"))
}

func HashThreadID(input string) string {
	sum := sha256.Sum256([]byte(input))
	return hex.EncodeToString(sum[:8])
}
