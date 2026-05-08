package webpush

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/SherClockHolmes/webpush-go"

	"nexus/internal/domain"
)

type Adapter struct {
	VAPIDPublicKey  string
	VAPIDPrivateKey string
	VAPIDSubject    string
	TTLSeconds      int
	HTTP            *http.Client
	GetSubscription func(ctx context.Context, tenantID, sessionID string) (domain.WebPushSubscription, error)
}

func New(publicKey, privateKey, subject string, ttlSeconds int) Adapter {
	return Adapter{VAPIDPublicKey: strings.TrimSpace(publicKey), VAPIDPrivateKey: strings.TrimSpace(privateKey), VAPIDSubject: strings.TrimSpace(subject), TTLSeconds: ttlSeconds}
}

func (a Adapter) Channel() string { return "web_push" }

func (a Adapter) VerifyInbound(context.Context, *http.Request, []byte) error { return nil }

func (a Adapter) ParseInbound(context.Context, *http.Request, []byte, string) (domain.CanonicalInboundEvent, error) {
	return domain.CanonicalInboundEvent{}, errors.New("web_push is outbound only")
}

func (a Adapter) SendMessage(ctx context.Context, delivery domain.OutboundDelivery) (domain.DeliveryResult, error) {
	if strings.TrimSpace(a.VAPIDPublicKey) == "" || strings.TrimSpace(a.VAPIDPrivateKey) == "" {
		return domain.DeliveryResult{}, errors.New("web push vapid keys are not configured")
	}
	var payload struct {
		Endpoint string         `json:"endpoint"`
		P256DH   string         `json:"p256dh"`
		Auth     string         `json:"auth"`
		Title    string         `json:"title"`
		Body     string         `json:"body"`
		URL      string         `json:"url"`
		Tag      string         `json:"tag"`
		Data     map[string]any `json:"data"`
	}
	if err := json.Unmarshal(delivery.PayloadJSON, &payload); err != nil {
		return domain.DeliveryResult{}, fmt.Errorf("decode web push payload: %w", err)
	}
	if strings.TrimSpace(payload.Endpoint) == "" && a.GetSubscription != nil {
		sub, err := a.GetSubscription(ctx, delivery.TenantID, delivery.SessionID)
		if err != nil {
			return domain.DeliveryResult{}, err
		}
		payload.Endpoint = sub.Endpoint
		payload.P256DH = sub.P256DH
		payload.Auth = sub.Auth
	}
	if strings.TrimSpace(payload.Endpoint) == "" || strings.TrimSpace(payload.P256DH) == "" || strings.TrimSpace(payload.Auth) == "" {
		return domain.DeliveryResult{}, errors.New("web push subscription is incomplete")
	}
	if payload.Title == "" {
		payload.Title = "Wulan"
	}
	if payload.URL == "" {
		payload.URL = "/chat"
	}
	body, err := json.Marshal(map[string]any{
		"title": payload.Title,
		"body":  payload.Body,
		"url":   payload.URL,
		"tag":   payload.Tag,
		"data":  payload.Data,
	})
	if err != nil {
		return domain.DeliveryResult{}, fmt.Errorf("marshal web push notification: %w", err)
	}
	sub := &webpush.Subscription{
		Endpoint: payload.Endpoint,
		Keys: webpush.Keys{
			P256dh: payload.P256DH,
			Auth:   payload.Auth,
		},
	}
	ttl := a.TTLSeconds
	if ttl <= 0 {
		ttl = 86400
	}
	options := &webpush.Options{
		Subscriber:      firstNonEmpty(a.VAPIDSubject, "mailto:admin@example.com"),
		VAPIDPublicKey:  a.VAPIDPublicKey,
		VAPIDPrivateKey: a.VAPIDPrivateKey,
		TTL:             ttl,
	}
	if a.HTTP != nil {
		options.HTTPClient = a.HTTP
	}
	resp, err := webpush.SendNotificationWithContext(ctx, body, sub, options)
	if err != nil {
		return domain.DeliveryResult{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return domain.DeliveryResult{}, fmt.Errorf("web push provider returned %s", resp.Status)
	}
	return domain.DeliveryResult{ProviderMessageID: "web_push:" + delivery.ID, ProviderRequestID: resp.Header.Get("X-Request-ID")}, nil
}

func (a Adapter) SendAwaitPrompt(ctx context.Context, delivery domain.OutboundDelivery) (domain.DeliveryResult, error) {
	return a.SendMessage(ctx, delivery)
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
