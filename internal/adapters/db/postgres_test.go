package db

import (
	"encoding/json"
	"testing"
	"time"

	"nexus/internal/domain"
)

func TestPrepareReplacementDeliverySlack(t *testing.T) {
	delivery := domain.OutboundDelivery{
		ID:               "delivery_1",
		ChannelType:      "slack",
		DeliveryKind:     "replace",
		LogicalMessageID: "logical_status_1",
		PayloadJSON:      mustJSONMap(t, map[string]any{"channel": "C123", "text": "updated"}),
	}
	previous := &domain.OutboundDelivery{
		ID:                "delivery_prev",
		ProviderMessageID: "111.222",
		ProviderRequestID: "req_prev",
	}

	got, err := prepareReplacementDelivery(delivery, previous)
	if err != nil {
		t.Fatal(err)
	}
	if got.DeliveryKind != "update" {
		t.Fatalf("expected update delivery kind, got %s", got.DeliveryKind)
	}
	if got.ProviderMessageID != "111.222" {
		t.Fatalf("expected previous provider message id, got %s", got.ProviderMessageID)
	}
	var payload map[string]any
	if err := json.Unmarshal(got.PayloadJSON, &payload); err != nil {
		t.Fatal(err)
	}
	if payload["ts"] != "111.222" {
		t.Fatalf("expected slack ts to be injected, got %+v", payload)
	}
}

func TestPrepareReplacementDeliveryTelegram(t *testing.T) {
	delivery := domain.OutboundDelivery{
		ID:               "delivery_1",
		ChannelType:      "telegram",
		DeliveryKind:     "replace",
		LogicalMessageID: "logical_status_1",
		PayloadJSON:      mustJSONMap(t, map[string]any{"chat_id": "123", "text": "updated"}),
	}
	previous := &domain.OutboundDelivery{
		ID:                "delivery_prev",
		ProviderMessageID: "42",
		ProviderRequestID: "req_prev",
	}

	got, err := prepareReplacementDelivery(delivery, previous)
	if err != nil {
		t.Fatal(err)
	}
	if got.DeliveryKind != "update" {
		t.Fatalf("expected update delivery kind, got %s", got.DeliveryKind)
	}
	var payload map[string]any
	if err := json.Unmarshal(got.PayloadJSON, &payload); err != nil {
		t.Fatal(err)
	}
	if payload["message_id"] != float64(42) {
		t.Fatalf("expected telegram message_id to be injected, got %+v", payload)
	}
}

func TestPrepareReplacementDeliveryFallsBackToSendWithoutPreviousMessage(t *testing.T) {
	delivery := domain.OutboundDelivery{
		ID:               "delivery_1",
		ChannelType:      "telegram",
		DeliveryKind:     "replace",
		LogicalMessageID: "logical_status_1",
		PayloadJSON:      mustJSONMap(t, map[string]any{"chat_id": "123", "text": "updated"}),
	}

	got, err := prepareReplacementDelivery(delivery, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got.DeliveryKind != "send" {
		t.Fatalf("expected send delivery kind without previous provider message, got %s", got.DeliveryKind)
	}
	var payload map[string]any
	if err := json.Unmarshal(got.PayloadJSON, &payload); err != nil {
		t.Fatal(err)
	}
	if _, ok := payload["message_id"]; ok {
		t.Fatalf("did not expect message_id to be injected without previous delivery, got %+v", payload)
	}
}

func TestValidateTelegramAccessResolution(t *testing.T) {
	if err := validateTelegramAccessResolution(domain.TelegramUserAccess{Status: "pending"}); err != nil {
		t.Fatalf("expected pending request to be resolvable, got %v", err)
	}
	if err := validateTelegramAccessResolution(domain.TelegramUserAccess{Status: "approved"}); err != domain.ErrTelegramAccessRequestNotPending {
		t.Fatalf("expected approved request to be rejected, got %v", err)
	}
	if err := validateTelegramAccessResolution(domain.TelegramUserAccess{Status: "denied"}); err != domain.ErrTelegramAccessRequestNotPending {
		t.Fatalf("expected denied request to be rejected, got %v", err)
	}
}

func TestRequestTelegramAccessExistingStateRules(t *testing.T) {
	repo := &PostgresRepository{}

	pending, err := repo.requestTelegramAccessWithExisting(domain.TelegramUserAccess{
		TenantID:       "tenant_default",
		TelegramUserID: "123",
	}, &domain.TelegramUserAccess{TenantID: "tenant_default", TelegramUserID: "123", Status: "pending"})
	if err != nil {
		t.Fatal(err)
	}
	if pending.Status != "pending" {
		t.Fatalf("expected pending record to be returned unchanged, got %+v", pending)
	}

	_, err = repo.requestTelegramAccessWithExisting(domain.TelegramUserAccess{
		TenantID:       "tenant_default",
		TelegramUserID: "123",
	}, &domain.TelegramUserAccess{TenantID: "tenant_default", TelegramUserID: "123", Status: "approved"})
	if err != domain.ErrTelegramAccessRequestAlreadyFinal {
		t.Fatalf("expected approved record to be rejected, got %v", err)
	}

	created, err := repo.requestTelegramAccessWithExisting(domain.TelegramUserAccess{
		TenantID:       "tenant_default",
		TelegramUserID: "456",
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if created.Status != "pending" || created.Allowed {
		t.Fatalf("expected fresh pending request, got %+v", created)
	}
}

func TestNormalizeLimit(t *testing.T) {
	if got := normalizeLimit(0); got != 50 {
		t.Fatalf("expected zero limit to normalize to 50, got %d", got)
	}
	if got := normalizeLimit(-10); got != 50 {
		t.Fatalf("expected negative limit to normalize to 50, got %d", got)
	}
	if got := normalizeLimit(25); got != 25 {
		t.Fatalf("expected in-range limit to remain unchanged, got %d", got)
	}
	if got := normalizeLimit(250); got != 200 {
		t.Fatalf("expected large limit to cap at 200, got %d", got)
	}
}

func TestParseAndFormatCursor(t *testing.T) {
	ts := time.Date(2026, 4, 6, 12, 34, 56, 789, time.UTC)
	cursor := formatCursor(ts, "item_123")
	parsedTime, parsedID, hasCursor, err := parseCursor(cursor)
	if err != nil {
		t.Fatal(err)
	}
	if !hasCursor {
		t.Fatal("expected formatted cursor to parse as present")
	}
	if !parsedTime.Equal(ts) || parsedID != "item_123" {
		t.Fatalf("unexpected parsed cursor values: %v %q", parsedTime, parsedID)
	}
}

func TestParseCursorEmptyAndInvalid(t *testing.T) {
	if _, _, hasCursor, err := parseCursor(""); err != nil || hasCursor {
		t.Fatalf("expected empty cursor to be treated as absent, got hasCursor=%v err=%v", hasCursor, err)
	}
	if _, _, _, err := parseCursor("bad-cursor"); err == nil {
		t.Fatal("expected malformed cursor to fail")
	}
	if _, _, _, err := parseCursor("not-a-time|id_1"); err == nil {
		t.Fatal("expected invalid cursor time to fail")
	}
}

func TestFinalizePage(t *testing.T) {
	t1 := time.Date(2026, 4, 6, 10, 0, 0, 0, time.UTC)
	t2 := time.Date(2026, 4, 6, 9, 0, 0, 0, time.UTC)
	t3 := time.Date(2026, 4, 6, 8, 0, 0, 0, time.UTC)
	result := finalizePage(
		[]string{"a", "b", "c"},
		[]cursorValue{
			{Time: t1, ID: "a"},
			{Time: t2, ID: "b"},
			{Time: t3, ID: "c"},
		},
		2,
	)
	if len(result.Items) != 2 || result.Items[0] != "a" || result.Items[1] != "b" {
		t.Fatalf("unexpected page items: %+v", result.Items)
	}
	if result.NextCursor != formatCursor(t2, "b") {
		t.Fatalf("unexpected next cursor: %q", result.NextCursor)
	}
}

func TestFinalizePageWithoutOverflow(t *testing.T) {
	t1 := time.Date(2026, 4, 6, 10, 0, 0, 0, time.UTC)
	result := finalizePage(
		[]string{"a"},
		[]cursorValue{{Time: t1, ID: "a"}},
		5,
	)
	if len(result.Items) != 1 || result.Items[0] != "a" {
		t.Fatalf("unexpected page items: %+v", result.Items)
	}
	if result.NextCursor != "" {
		t.Fatalf("did not expect next cursor when page does not overflow, got %q", result.NextCursor)
	}
}

func TestNonZeroTimeAndNullableHelpers(t *testing.T) {
	zero := time.Time{}
	if got := nonZeroTime(zero); got.IsZero() {
		t.Fatal("expected nonZeroTime to replace zero time")
	}
	now := time.Date(2026, 4, 6, 11, 0, 0, 0, time.UTC)
	if got := nonZeroTime(now); !got.Equal(now) {
		t.Fatalf("expected nonZeroTime to preserve non-zero time, got %v", got)
	}
	if got := nullableTime(zero); got != nil {
		t.Fatalf("expected nullableTime(zero) to return nil, got %#v", got)
	}
	if got := nullableTime(now); got == nil {
		t.Fatal("expected nullableTime(non-zero) to return a value")
	}
	if got := nullableTimePtr(nil); got != nil {
		t.Fatalf("expected nullableTimePtr(nil) to return nil, got %#v", got)
	}
	if got := nullableTimePtr(&zero); got != nil {
		t.Fatalf("expected nullableTimePtr(zero) to return nil, got %#v", got)
	}
	if got := nullableTimePtr(&now); got == nil {
		t.Fatal("expected nullableTimePtr(non-zero) to return a value")
	}
}

func TestNonEmptyStatusAndContains(t *testing.T) {
	if got := nonEmptyStatus(""); got != "approved" {
		t.Fatalf("expected empty status to default to approved, got %q", got)
	}
	if got := nonEmptyStatus("denied"); got != "denied" {
		t.Fatalf("expected non-empty status to remain unchanged, got %q", got)
	}
	if !contains([]string{"a", "b", "c"}, "b") {
		t.Fatal("expected contains to find existing item")
	}
	if contains([]string{"a", "b", "c"}, "z") {
		t.Fatal("expected contains to reject missing item")
	}
}

func TestKeyForSurface(t *testing.T) {
	evt := domain.CanonicalInboundEvent{
		Conversation: domain.Conversation{ChannelSurfaceKey: "telegram:dm:123"},
	}
	if got := keyForSurface(evt); got != "telegram:dm:123" {
		t.Fatalf("unexpected surface key: %q", got)
	}
}

func mustJSONMap(t *testing.T, v map[string]any) []byte {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}
