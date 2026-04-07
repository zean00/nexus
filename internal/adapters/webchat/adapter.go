package webchat

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"nexus/internal/domain"
)

type Adapter struct{}

func New() Adapter { return Adapter{} }

func (Adapter) Channel() string { return "webchat" }

func (Adapter) VerifyInbound(context.Context, *http.Request, []byte) error { return nil }

func (Adapter) ParseInbound(_ context.Context, _ *http.Request, body []byte, tenantID string) (domain.CanonicalInboundEvent, error) {
	var payload struct {
		EventID    string `json:"event_id"`
		Email      string `json:"email"`
		SurfaceKey string `json:"surface_key"`
		Text       string `json:"text"`
		AwaitID    string `json:"await_id"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return domain.CanonicalInboundEvent{}, err
	}
	evt := domain.CanonicalInboundEvent{
		EventID:         payload.EventID,
		TenantID:        tenantID,
		Channel:         "webchat",
		Interaction:     "message",
		ProviderEventID: payload.EventID,
		ReceivedAt:      time.Now().UTC(),
		Sender: domain.Sender{
			ChannelUserID:       payload.Email,
			DisplayName:         payload.Email,
			IsAuthenticated:     true,
			IdentityAssurance:   "first_party_session",
			AllowedResponderIDs: []string{payload.Email},
		},
		Conversation: domain.Conversation{
			ChannelConversationID: payload.Email,
			ChannelThreadID:       payload.SurfaceKey,
			ChannelSurfaceKey:     payload.SurfaceKey,
		},
		Message: domain.Message{
			MessageID:   "webchat_msg_" + payload.EventID,
			MessageType: "text",
			Text:        payload.Text,
			Parts:       []domain.Part{{ContentType: "text/plain", Content: payload.Text}},
		},
	}
	if payload.AwaitID != "" {
		raw, _ := json.Marshal(map[string]string{"reply": payload.Text})
		evt.Interaction = "await_response"
		evt.Metadata.AwaitID = payload.AwaitID
		evt.Metadata.ResumePayload = raw
	}
	return evt, nil
}

func (Adapter) SendMessage(_ context.Context, delivery domain.OutboundDelivery) (domain.DeliveryResult, error) {
	return domain.DeliveryResult{ProviderMessageID: "webchat:" + delivery.ID}, nil
}

func (Adapter) SendAwaitPrompt(_ context.Context, delivery domain.OutboundDelivery) (domain.DeliveryResult, error) {
	return domain.DeliveryResult{ProviderMessageID: "webchat:" + delivery.ID}, nil
}
