package services

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"nexus/internal/domain"
	"nexus/internal/ports"
	"nexus/internal/tracex"
)

var ErrDuplicateEvent = errors.New("duplicate inbound event")

type InboundService struct {
	Repo     ports.Repository
	Router   ports.Router
	Identity ports.IdentityRepository
}

type InboundResult struct {
	SessionID string `json:"session_id"`
	Status    string `json:"status"`
	QueueID   string `json:"queue_id,omitempty"`
}

func (s InboundService) Handle(ctx context.Context, evt domain.CanonicalInboundEvent) (result InboundResult, err error) {
	ctx, end := tracex.StartSpan(ctx, "inbound.handle",
		"event_id", evt.EventID,
		"tenant_id", evt.TenantID,
		"channel", evt.Channel,
		"interaction", evt.Interaction,
	)
	defer func() { end(err) }()
	err = s.Repo.InTx(ctx, func(ctx context.Context, repo ports.Repository) error {
		inserted, err := repo.RecordInboundReceipt(ctx, evt)
		if err != nil {
			return err
		}
		if !inserted {
			return ErrDuplicateEvent
		}

		session, _, err := repo.ResolveSession(ctx, evt, "")
		if err != nil {
			return err
		}
		if handled, commandResult, err := s.handleIdentityCommand(ctx, evt); err != nil {
			return err
		} else if handled {
			result = commandResult
			return nil
		}
		if handled, commandResult, err := s.handleSessionCommand(ctx, repo, evt, session); err != nil {
			return err
		} else if handled {
			result = commandResult
			return nil
		}
		route, err := s.Router.Route(ctx, evt, session)
		if err != nil {
			return err
		}
		if session.AgentProfileID == "" {
			session.AgentProfileID = route.AgentProfileID
		}
		identityRepo := s.Identity
		if txIdentity, ok := repo.(ports.IdentityRepository); ok {
			identityRepo = txIdentity
		}
		if route.RequireLinkedIdentityForExecution {
			if _, err := s.resolveExistingCanonicalUser(ctx, identityRepo, evt); err != nil {
				if errors.Is(err, domain.ErrLinkedIdentityNotFound) || errors.Is(err, domain.ErrIdentityUserNotFound) {
					return domain.ErrLinkedIdentityRequired
				}
				return err
			}
		}

		inboundMessageID, err := repo.StoreInboundMessage(ctx, evt, session.ID)
		if err != nil {
			return err
		}
		if len(evt.Message.Artifacts) > 0 {
			if err := repo.StoreArtifacts(ctx, inboundMessageID, "inbound", evt.Message.Artifacts); err != nil {
				return err
			}
		}
		active, err := repo.HasActiveRun(ctx, session.ID)
		if err != nil {
			return err
		}
		queueItem, _, err := repo.EnqueueMessage(ctx, evt, session, route, inboundMessageID, !active)
		if err != nil {
			return err
		}
		result = InboundResult{
			SessionID: session.ID,
			Status:    "accepted",
			QueueID:   queueItem.ID,
		}
		if active {
			result.Status = "queued"
		}
		return nil
	})
	if errors.Is(err, ErrDuplicateEvent) {
		tracex.Logger(ctx).Info("inbound.duplicate", "event_id", evt.EventID, "provider_event_id", evt.ProviderEventID)
		return InboundResult{Status: "duplicate"}, nil
	}
	if err != nil {
		tracex.Logger(ctx).Error("inbound.failed", "event_id", evt.EventID, "error", err.Error())
		err = fmt.Errorf("handle inbound at %s: %w", time.Now().Format(time.RFC3339), err)
		return InboundResult{}, err
	}
	tracex.Logger(ctx).Info("inbound.accepted", "event_id", evt.EventID, "session_id", result.SessionID, "queue_id", result.QueueID, "status", result.Status)
	return result, nil
}

func (s InboundService) resolveCanonicalUser(ctx context.Context, identityRepo ports.IdentityRepository, evt domain.CanonicalInboundEvent) (domain.User, error) {
	if identityRepo == nil {
		return domain.User{}, domain.ErrIdentityUserNotFound
	}
	switch evt.Channel {
	case "webchat", "email":
		user, err := identityRepo.EnsureUserByEmail(ctx, evt.TenantID, evt.Sender.ChannelUserID)
		if err != nil {
			return domain.User{}, err
		}
		if err := identityRepo.UpsertLinkedIdentity(ctx, domain.LinkedIdentity{
			TenantID:       evt.TenantID,
			UserID:         user.ID,
			ChannelType:    evt.Channel,
			ChannelUserID:  strings.ToLower(strings.TrimSpace(evt.Sender.ChannelUserID)),
			Status:         "linked",
			LinkedAt:       time.Now().UTC(),
			LastVerifiedAt: time.Now().UTC(),
		}); err != nil {
			return domain.User{}, err
		}
		return user, nil
	default:
		identity, err := identityRepo.GetLinkedIdentity(ctx, evt.TenantID, evt.Channel, evt.Sender.ChannelUserID)
		if err != nil {
			for _, candidate := range relatedChannelIdentities(evt.Channel, evt.Sender.ChannelUserID) {
				identity, err = identityRepo.GetLinkedIdentity(ctx, evt.TenantID, candidate.ChannelType, candidate.ChannelUserID)
				if err == nil {
					return identityRepo.GetUser(ctx, evt.TenantID, identity.UserID)
				}
			}
			return domain.User{}, err
		}
		return identityRepo.GetUser(ctx, evt.TenantID, identity.UserID)
	}
}

func (s InboundService) resolveExistingCanonicalUser(ctx context.Context, identityRepo ports.IdentityRepository, evt domain.CanonicalInboundEvent) (domain.User, error) {
	if identityRepo == nil {
		return domain.User{}, domain.ErrIdentityUserNotFound
	}
	switch evt.Channel {
	case "webchat", "email":
		identity, err := identityRepo.GetLinkedIdentity(ctx, evt.TenantID, evt.Channel, strings.ToLower(strings.TrimSpace(evt.Sender.ChannelUserID)))
		if err != nil {
			return domain.User{}, err
		}
		return identityRepo.GetUser(ctx, evt.TenantID, identity.UserID)
	default:
		return s.resolveCanonicalUser(ctx, identityRepo, evt)
	}
}

func (s InboundService) handleIdentityCommand(ctx context.Context, evt domain.CanonicalInboundEvent) (bool, InboundResult, error) {
	if s.Identity == nil {
		return false, InboundResult{}, nil
	}
	command, token := parseIdentityCommand(evt.Message.Text)
	if command != "link" || token == "" || evt.Channel == "webchat" {
		return false, InboundResult{}, nil
	}
	userID, code := parseLinkToken(token)
	if userID == "" || code == "" {
		return true, InboundResult{Status: "identity_link_rejected"}, domain.ErrStepUpChallengeNotFound
	}
	hash := sha256Hex(code)
	challenge, err := s.Identity.ConsumeStepUpChallenge(ctx, evt.TenantID, userID, "link", evt.Channel, hash, time.Now().UTC())
	if err != nil {
		return true, InboundResult{Status: "identity_link_rejected"}, err
	}
	if err := s.Identity.UpsertLinkedIdentity(ctx, domain.LinkedIdentity{
		TenantID:       evt.TenantID,
		UserID:         challenge.UserID,
		ChannelType:    evt.Channel,
		ChannelUserID:  evt.Sender.ChannelUserID,
		Status:         "linked",
		LinkedAt:       time.Now().UTC(),
		LastVerifiedAt: time.Now().UTC(),
	}); err != nil {
		return true, InboundResult{Status: "identity_link_rejected"}, err
	}
	for _, identity := range relatedChannelIdentities(evt.Channel, evt.Sender.ChannelUserID) {
		if err := s.Identity.UpsertLinkedIdentity(ctx, domain.LinkedIdentity{
			TenantID:       evt.TenantID,
			UserID:         challenge.UserID,
			ChannelType:    identity.ChannelType,
			ChannelUserID:  identity.ChannelUserID,
			Status:         "linked",
			LinkedAt:       time.Now().UTC(),
			LastVerifiedAt: time.Now().UTC(),
		}); err != nil {
			return true, InboundResult{Status: "identity_link_rejected"}, err
		}
	}
	if auditRepo, ok := s.Repo.(interface {
		Audit(context.Context, domain.AuditEvent) error
	}); ok {
		_ = auditRepo.Audit(ctx, domain.AuditEvent{
			ID:            trustAuditID("identity_linked", evt.TenantID, challenge.UserID, evt.Channel, evt.Sender.ChannelUserID, evt.EventID),
			TenantID:      evt.TenantID,
			AggregateType: "user",
			AggregateID:   challenge.UserID,
			EventType:     "trust.identity_linked",
			PayloadJSON:   mustJSON(map[string]any{"channel": evt.Channel, "channel_user_id": evt.Sender.ChannelUserID}),
			CreatedAt:     time.Now().UTC(),
		})
	}
	return true, InboundResult{Status: "identity_linked"}, nil
}

func relatedChannelIdentities(channelType, channelUserID string) []domain.LinkedIdentity {
	normalized := normalizeLinkedIdentityUserID(channelType, channelUserID)
	switch channelType {
	case "whatsapp", "whatsapp_web":
		if normalized == "" {
			return nil
		}
		return []domain.LinkedIdentity{
			{ChannelType: "whatsapp", ChannelUserID: normalized},
			{ChannelType: "whatsapp_web", ChannelUserID: normalized},
		}
	default:
		return nil
	}
}

func normalizeLinkedIdentityUserID(channelType, channelUserID string) string {
	channelUserID = strings.TrimSpace(channelUserID)
	if channelType != "whatsapp" && channelType != "whatsapp_web" {
		return channelUserID
	}
	var b strings.Builder
	for _, ch := range channelUserID {
		if ch >= '0' && ch <= '9' {
			b.WriteRune(ch)
		}
	}
	return b.String()
}

func parseIdentityCommand(text string) (string, string) {
	fields := strings.Fields(strings.TrimSpace(text))
	if len(fields) < 2 {
		return "", ""
	}
	command := strings.TrimPrefix(strings.ToLower(fields[0]), "/")
	return command, fields[1]
}

func parseLinkToken(token string) (string, string) {
	parts := strings.SplitN(strings.TrimSpace(token), ".", 2)
	if len(parts) != 2 {
		return "", ""
	}
	return strings.TrimSpace(parts[0]), strings.TrimSpace(parts[1])
}

func sha256Hex(input string) string {
	sum := sha256.Sum256([]byte(input))
	return hex.EncodeToString(sum[:])
}

func trustAuditID(parts ...string) string {
	return "audit_" + sha256Hex(strings.Join(parts, "|"))[:24]
}

func (s InboundService) handleSessionCommand(ctx context.Context, repo ports.Repository, evt domain.CanonicalInboundEvent, session domain.Session) (bool, InboundResult, error) {
	if evt.Channel != "telegram" || evt.Metadata.Command == "" || strings.HasPrefix(evt.Conversation.ChannelSurfaceKey, "-") {
		return false, InboundResult{}, nil
	}
	command := evt.Metadata.Command
	args := strings.Fields(strings.TrimSpace(evt.Message.Text))
	alias := ""
	if len(args) > 1 {
		alias = args[1]
	}
	switch command {
	case "/new":
		newSession, err := repo.CreateVirtualSession(ctx, evt.TenantID, evt.Channel, evt.Conversation.ChannelSurfaceKey, evt.Sender.ChannelUserID, session.AgentProfileID, alias)
		if err != nil {
			return false, InboundResult{}, err
		}
		if err := repo.EnqueueDelivery(ctx, buildControlDelivery(evt, newSession.ID, "Created session "+newSession.ID, "telegram")); err != nil {
			return false, InboundResult{}, err
		}
		return true, InboundResult{SessionID: newSession.ID, Status: "accepted"}, nil
	case "/switch":
		target := alias
		if target == "" {
			return true, InboundResult{SessionID: session.ID, Status: "accepted"}, repo.EnqueueDelivery(ctx, buildControlDelivery(evt, session.ID, "Usage: /switch <alias-or-id>", "telegram"))
		}
		switched, err := repo.SwitchActiveSession(ctx, evt.TenantID, evt.Channel, evt.Conversation.ChannelSurfaceKey, evt.Sender.ChannelUserID, target)
		if err != nil {
			return false, InboundResult{}, err
		}
		if err := repo.EnqueueDelivery(ctx, buildControlDelivery(evt, switched.ID, "Switched to "+switched.ID, "telegram")); err != nil {
			return false, InboundResult{}, err
		}
		return true, InboundResult{SessionID: switched.ID, Status: "accepted"}, nil
	case "/sessions":
		sessions, err := repo.ListSurfaceSessions(ctx, evt.TenantID, evt.Channel, evt.Conversation.ChannelSurfaceKey, evt.Sender.ChannelUserID, 10)
		if err != nil {
			return false, InboundResult{}, err
		}
		lines := []string{"Sessions:"}
		for _, item := range sessions {
			line := item.Session.ID
			if item.Alias != "" {
				line += " (" + item.Alias + ")"
			}
			line += " [" + item.Session.State + "]"
			lines = append(lines, line)
		}
		if len(sessions) == 0 {
			lines = append(lines, "No sessions found.")
		}
		if err := repo.EnqueueDelivery(ctx, buildControlDelivery(evt, session.ID, strings.Join(lines, "\n"), "telegram")); err != nil {
			return false, InboundResult{}, err
		}
		return true, InboundResult{SessionID: session.ID, Status: "accepted"}, nil
	case "/close":
		closed, err := repo.CloseActiveSession(ctx, evt.TenantID, evt.Channel, evt.Conversation.ChannelSurfaceKey, evt.Sender.ChannelUserID)
		if err != nil {
			return false, InboundResult{}, err
		}
		if err := repo.EnqueueDelivery(ctx, buildControlDelivery(evt, closed.ID, "Closed "+closed.ID, "telegram")); err != nil {
			return false, InboundResult{}, err
		}
		return true, InboundResult{SessionID: closed.ID, Status: "accepted"}, nil
	default:
		return false, InboundResult{}, nil
	}
}

func buildControlDelivery(evt domain.CanonicalInboundEvent, sessionID, text, channelType string) domain.OutboundDelivery {
	payload := map[string]any{"text": text}
	if channelType == "telegram" {
		payload["chat_id"] = evt.Conversation.ChannelConversationID
	} else {
		payload["channel"] = evt.Conversation.ChannelConversationID
		payload["thread_ts"] = evt.Conversation.ChannelThreadID
	}
	raw, _ := json.Marshal(payload)
	return domain.OutboundDelivery{
		ID:               "delivery_control_" + evt.EventID,
		TenantID:         evt.TenantID,
		SessionID:        sessionID,
		ChannelType:      channelType,
		DeliveryKind:     "send",
		Status:           "queued",
		LogicalMessageID: "logical_control_" + evt.EventID,
		PayloadJSON:      raw,
	}
}
