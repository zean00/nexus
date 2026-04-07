package services

import (
	"context"
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
	Repo   ports.Repository
	Router ports.Router
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
