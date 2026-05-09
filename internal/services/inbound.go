package services

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"nexus/internal/domain"
	"nexus/internal/ports"
	"nexus/internal/tracex"
)

var ErrDuplicateEvent = errors.New("duplicate inbound event")

type InboundService struct {
	Repo                   ports.Repository
	Router                 ports.Router
	Identity               ports.IdentityRepository
	MultipleAgentMode      bool
	AgentProfiles          map[string]domain.AgentProfile
	AllowedAgentsByChannel map[string][]string
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
		if evt.Metadata.Command == "" {
			evt.Metadata.Command = commandFromText(evt.Message.Text)
		}
		inserted, err := repo.RecordInboundReceipt(ctx, evt)
		if err != nil {
			return err
		}
		if !inserted {
			return ErrDuplicateEvent
		}

		route, err := s.Router.Route(ctx, evt, domain.Session{})
		if err != nil {
			return err
		}
		session, _, err := resolveSessionForRoute(ctx, repo, evt, route, s.MultipleAgentMode)
		if err != nil {
			return err
		}
		if handled, commandResult, err := s.handleIdentityCommand(ctx, evt); err != nil {
			return err
		} else if handled {
			result = commandResult
			return nil
		}
		if handled, commandResult, err := s.handleAgentCommand(ctx, repo, evt, session, route); err != nil {
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

type routeSessionResolver interface {
	ResolveSessionForRoute(ctx context.Context, evt domain.CanonicalInboundEvent, route domain.RouteDecision, multipleMode bool) (domain.Session, bool, error)
}

func resolveSessionForRoute(ctx context.Context, repo ports.Repository, evt domain.CanonicalInboundEvent, route domain.RouteDecision, multipleMode bool) (domain.Session, bool, error) {
	if resolver, ok := repo.(routeSessionResolver); ok {
		return resolver.ResolveSessionForRoute(ctx, evt, route, multipleMode)
	}
	return repo.ResolveSession(ctx, evt, route.AgentProfileID)
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
	actualChannelUserID := normalizeLinkedIdentityUserID(evt.Channel, evt.Sender.ChannelUserID)
	challenge, err := consumeIdentityLinkChallenge(ctx, s.Identity, evt.TenantID, userID, evt.Channel, hash, actualChannelUserID, time.Now().UTC())
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

func consumeIdentityLinkChallenge(ctx context.Context, identity ports.IdentityRepository, tenantID, userID, channelType, codeHash, actualChannelUserID string, now time.Time) (domain.StepUpChallenge, error) {
	var lastErr error
	for _, candidate := range pairingChallengeChannels(channelType) {
		challenge, err := identity.ConsumeStepUpChallenge(ctx, tenantID, userID, "link", candidate, codeHash, actualChannelUserID, now)
		if err == nil {
			return challenge, nil
		}
		lastErr = err
		if !errors.Is(err, domain.ErrStepUpChallengeNotFound) {
			return domain.StepUpChallenge{}, err
		}
	}
	if lastErr != nil {
		return domain.StepUpChallenge{}, lastErr
	}
	return domain.StepUpChallenge{}, domain.ErrStepUpChallengeNotFound
}

func pairingChallengeChannels(channelType string) []string {
	switch channelType {
	case "whatsapp":
		return []string{"whatsapp", "whatsapp_web"}
	case "whatsapp_web":
		return []string{"whatsapp_web", "whatsapp"}
	default:
		return []string{channelType}
	}
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
	digits := b.String()
	if strings.HasPrefix(digits, "08") {
		digits = "62" + strings.TrimPrefix(digits, "0")
	}
	return digits
}

func parseIdentityCommand(text string) (string, string) {
	fields := strings.Fields(strings.TrimSpace(text))
	if len(fields) < 2 {
		return "", ""
	}
	command := strings.TrimPrefix(strings.ToLower(fields[0]), "/")
	return command, fields[1]
}

func commandFromText(text string) string {
	fields := strings.Fields(strings.TrimSpace(text))
	if len(fields) == 0 || !strings.HasPrefix(fields[0], "/") {
		return ""
	}
	return strings.ToLower(strings.TrimSpace(fields[0]))
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
	if !sessionCommandsSupported(evt) || evt.Metadata.Command == "" || strings.HasPrefix(evt.Conversation.ChannelSurfaceKey, "-") {
		return false, InboundResult{}, nil
	}
	command := evt.Metadata.Command
	args := strings.Fields(strings.TrimSpace(evt.Message.Text))
	alias := ""
	if len(args) > 1 {
		alias = args[1]
	}
	surfaceKey := evt.Conversation.ChannelSurfaceKey
	if s.MultipleAgentMode && session.AgentProfileID != "" {
		surfaceKey = scopedAgentSurfaceKey(surfaceKey, session.AgentProfileID)
	}
	switch command {
	case "/new":
		newSession, err := repo.CreateVirtualSession(ctx, evt.TenantID, evt.Channel, surfaceKey, evt.Sender.ChannelUserID, session.AgentProfileID, alias)
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
		switched, err := repo.SwitchActiveSession(ctx, evt.TenantID, evt.Channel, surfaceKey, evt.Sender.ChannelUserID, target)
		if err != nil {
			return false, InboundResult{}, err
		}
		if err := repo.EnqueueDelivery(ctx, buildControlDelivery(evt, switched.ID, "Switched to "+switched.ID, "telegram")); err != nil {
			return false, InboundResult{}, err
		}
		return true, InboundResult{SessionID: switched.ID, Status: "accepted"}, nil
	case "/sessions":
		sessions, err := repo.ListSurfaceSessions(ctx, evt.TenantID, evt.Channel, surfaceKey, evt.Sender.ChannelUserID, 10)
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
		closed, err := repo.CloseActiveSession(ctx, evt.TenantID, evt.Channel, surfaceKey, evt.Sender.ChannelUserID)
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

type agentRouteOverrideStore interface {
	SetAgentRouteOverride(ctx context.Context, tenantID, channelType, surfaceKey, ownerUserID, agentProfileID string) error
	GetAgentRouteOverride(ctx context.Context, tenantID, channelType, surfaceKey, ownerUserID string) (string, error)
}

func (s InboundService) handleAgentCommand(ctx context.Context, repo ports.Repository, evt domain.CanonicalInboundEvent, session domain.Session, route domain.RouteDecision) (bool, InboundResult, error) {
	if evt.Metadata.Command != "/agent" || !agentCommandsSupported(evt.Channel) {
		return false, InboundResult{}, nil
	}
	if !s.MultipleAgentMode {
		return true, InboundResult{SessionID: session.ID, Status: "accepted"}, s.sendControl(ctx, repo, evt, session, "Active agent: "+route.AgentProfileID)
	}
	args := strings.Fields(strings.TrimSpace(evt.Message.Text))
	if len(args) == 1 {
		return true, InboundResult{SessionID: session.ID, Status: "accepted"}, s.sendControl(ctx, repo, evt, session, s.agentListText(evt.Channel, route.AgentProfileID))
	}
	target := strings.TrimSpace(args[1])
	if _, ok := s.AgentProfiles[target]; !ok {
		return true, InboundResult{SessionID: session.ID, Status: "accepted"}, s.sendControl(ctx, repo, evt, session, "Agent "+target+" is not configured.")
	}
	if !s.agentAllowedOnChannel(evt.Channel, target) {
		return true, InboundResult{SessionID: session.ID, Status: "accepted"}, s.sendControl(ctx, repo, evt, session, "Agent "+target+" is not available on this channel.")
	}
	store, ok := repo.(agentRouteOverrideStore)
	if !ok {
		return false, InboundResult{}, fmt.Errorf("agent route overrides are not supported by repository")
	}
	if err := store.SetAgentRouteOverride(ctx, evt.TenantID, evt.Channel, evt.Conversation.ChannelSurfaceKey, evt.Sender.ChannelUserID, target); err != nil {
		return false, InboundResult{}, err
	}
	return true, InboundResult{SessionID: session.ID, Status: "accepted"}, s.sendControl(ctx, repo, evt, session, "Switched agent to "+target)
}

func (s InboundService) sendControl(ctx context.Context, repo ports.Repository, evt domain.CanonicalInboundEvent, session domain.Session, text string) error {
	if evt.Channel == "webchat" {
		raw, _ := json.Marshal(map[string]any{"status": "completed", "text": text, "message_key": "control_" + evt.EventID})
		_, err := repo.StoreOutboundMessage(ctx, session, "control_"+evt.EventID, "control_"+evt.EventID, text, raw)
		return err
	}
	return repo.EnqueueDelivery(ctx, buildControlDelivery(evt, session.ID, text, evt.Channel))
}

func (s InboundService) agentListText(channel, active string) string {
	ids := s.availableAgents(channel)
	lines := []string{"Agents:"}
	for _, id := range ids {
		line := id
		if id == active {
			line += " (active)"
		}
		lines = append(lines, line)
	}
	if len(ids) == 0 {
		lines = append(lines, "No agents available.")
	}
	return strings.Join(lines, "\n")
}

func (s InboundService) availableAgents(channel string) []string {
	allowed := s.AllowedAgentsByChannel[strings.ToLower(strings.TrimSpace(channel))]
	if len(allowed) > 0 {
		return append([]string(nil), allowed...)
	}
	out := make([]string, 0, len(s.AgentProfiles))
	for id := range s.AgentProfiles {
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}

func (s InboundService) agentAllowedOnChannel(channel, profileID string) bool {
	for _, id := range s.availableAgents(channel) {
		if id == profileID {
			return true
		}
	}
	return false
}

func agentCommandsSupported(channel string) bool {
	switch channel {
	case "telegram", "webchat", "slack", "whatsapp", "whatsapp_web":
		return true
	default:
		return false
	}
}

func sessionCommandsSupported(evt domain.CanonicalInboundEvent) bool {
	return evt.Channel == "telegram" || evt.Channel == "webchat"
}

func scopedAgentSurfaceKey(surfaceKey, agentProfileID string) string {
	if agentProfileID == "" {
		return surfaceKey
	}
	return surfaceKey + ":agent:" + agentProfileID
}

func buildControlDelivery(evt domain.CanonicalInboundEvent, sessionID, text, channelType string) domain.OutboundDelivery {
	payload := map[string]any{"text": text}
	switch channelType {
	case "telegram":
		payload["chat_id"] = evt.Conversation.ChannelConversationID
	case "slack":
		payload["channel"] = evt.Conversation.ChannelConversationID
		payload["thread_ts"] = evt.Conversation.ChannelThreadID
	case "whatsapp":
		payload["messaging_product"] = "whatsapp"
		payload["to"] = evt.Sender.ChannelUserID
		payload["type"] = "text"
		payload["text"] = map[string]any{"body": text}
	case "whatsapp_web":
		payload["chatId"] = evt.Conversation.ChannelConversationID
		if payload["chatId"] == "" {
			payload["chatId"] = evt.Sender.ChannelUserID
		}
		payload["text"] = text
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
