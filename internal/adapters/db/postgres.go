package db

import (
	"context"
	"crypto/sha1"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"nexus/internal/domain"
	"nexus/internal/ports"
)

type PostgresRepository struct {
	pool *pgxpool.Pool
	conn *pgxpool.Conn
	tx   pgx.Tx
}

func New(ctx context.Context, databaseURL string) (*PostgresRepository, error) {
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		return nil, err
	}
	return &PostgresRepository{pool: pool}, nil
}

func (r *PostgresRepository) Close() {
	if r.pool != nil {
		r.pool.Close()
	}
}

func (r *PostgresRepository) InTx(ctx context.Context, fn func(ctx context.Context, repo ports.Repository) error) error {
	var (
		tx  pgx.Tx
		err error
	)
	switch {
	case r.tx != nil:
		tx, err = r.tx.Begin(ctx)
	case r.conn != nil:
		tx, err = r.conn.BeginTx(ctx, pgx.TxOptions{})
	default:
		tx, err = r.pool.BeginTx(ctx, pgx.TxOptions{})
	}
	if err != nil {
		return err
	}
	repo := &PostgresRepository{pool: r.pool, conn: r.conn, tx: tx}
	if err := fn(ctx, repo); err != nil {
		_ = tx.Rollback(ctx)
		return err
	}
	return tx.Commit(ctx)
}

func (r *PostgresRepository) RecordInboundReceipt(ctx context.Context, evt domain.CanonicalInboundEvent) (bool, error) {
	tag, err := r.exec(ctx, `
		insert into inbound_receipts (
			id, tenant_id, channel_type, provider_event_id, provider_delivery_id,
			interaction_type, actor_channel_user_id, first_seen_at, last_seen_at, duplicate_count, status
		) values ($1,$2,$3,$4,$5,$6,$7,now(),now(),0,'accepted')
		on conflict (tenant_id, channel_type, provider_event_id)
		do update set last_seen_at = now(), duplicate_count = inbound_receipts.duplicate_count + 1
		where false
	`, evt.EventID, evt.TenantID, evt.Channel, evt.ProviderEventID, evt.EventID, evt.Interaction, evt.Sender.ChannelUserID)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
}

func (r *PostgresRepository) ResolveSession(ctx context.Context, evt domain.CanonicalInboundEvent, agentProfileID string) (domain.Session, bool, error) {
	key := evt.Conversation.ChannelSurfaceKey
	if (evt.Channel == "telegram" && !strings.HasPrefix(key, "-")) || evt.Channel == "webchat" {
		session, created, err := r.resolveVirtualSurfaceSession(ctx, evt, agentProfileID)
		if err != nil {
			return domain.Session{}, false, err
		}
		return session, created, nil
	}
	row := r.queryRow(ctx, `
		select id, tenant_id, coalesce(owner_user_id,''), coalesce(agent_profile_id,''), channel_type,
		       channel_scope_key, state, last_active_at, coalesce(acp_session_id,'')
		from sessions
		where tenant_id=$1 and channel_type=$2 and channel_scope_key=$3 and state in ('open','paused')
		limit 1
	`, evt.TenantID, evt.Channel, key)
	var s domain.Session
	err := row.Scan(&s.ID, &s.TenantID, &s.OwnerUserID, &s.AgentProfileID, &s.ChannelType, &s.ChannelScopeKey, &s.State, &s.LastActiveAt, &s.ACPSessionID)
	if err == nil {
		return s, false, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return domain.Session{}, false, err
	}
	s = domain.Session{
		ID:              evt.EventID + "_session",
		TenantID:        evt.TenantID,
		OwnerUserID:     evt.Sender.ChannelUserID,
		AgentProfileID:  agentProfileID,
		ChannelType:     evt.Channel,
		ChannelScopeKey: key,
		State:           "open",
		LastActiveAt:    time.Now().UTC(),
	}
	_, err = r.exec(ctx, `
		insert into sessions (
			id, tenant_id, owner_user_id, agent_profile_id, channel_type, channel_scope_key,
			acp_connection_id, acp_server_url, acp_agent_name, acp_session_id, mode, state, last_active_at, created_at, updated_at
		) values ($1,$2,$3,$4,$5,$6,'acp_default','','','', 'per-thread','open',now(),now(),now())
	`, s.ID, s.TenantID, s.OwnerUserID, s.AgentProfileID, s.ChannelType, s.ChannelScopeKey)
	return s, true, err
}

func (r *PostgresRepository) resolveVirtualSurfaceSession(ctx context.Context, evt domain.CanonicalInboundEvent, agentProfileID string) (domain.Session, bool, error) {
	row := r.queryRow(ctx, `
		select s.id, s.tenant_id, coalesce(s.owner_user_id,''), coalesce(s.agent_profile_id,''), s.channel_type,
		       s.channel_scope_key, s.state, s.last_active_at, coalesce(s.acp_session_id,'')
		from channel_surface_state css
		join sessions s on s.id = css.active_session_id
		where css.tenant_id=$1 and css.channel_type=$2 and css.surface_key=$3 and s.owner_user_id=$4 and s.state in ('open','paused')
		limit 1
	`, evt.TenantID, evt.Channel, keyForSurface(evt), evt.Sender.ChannelUserID)
	var s domain.Session
	err := row.Scan(&s.ID, &s.TenantID, &s.OwnerUserID, &s.AgentProfileID, &s.ChannelType, &s.ChannelScopeKey, &s.State, &s.LastActiveAt, &s.ACPSessionID)
	if err == nil {
		return s, false, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return domain.Session{}, false, err
	}
	s, err = r.CreateVirtualSession(ctx, evt.TenantID, evt.Channel, keyForSurface(evt), evt.Sender.ChannelUserID, agentProfileID, "")
	if err != nil {
		return domain.Session{}, false, err
	}
	return s, true, nil
}

func (r *PostgresRepository) CreateWebAuthChallenge(ctx context.Context, challenge domain.WebAuthChallenge, minInterval time.Duration) error {
	return r.InTx(ctx, func(ctx context.Context, repo ports.Repository) error {
		txRepo, ok := repo.(*PostgresRepository)
		if !ok {
			return fmt.Errorf("unexpected repository type %T", repo)
		}
		var latest time.Time
		err := txRepo.queryRow(ctx, `
			select coalesce(max(created_at), 'epoch'::timestamptz)
			from webchat_auth_challenges
			where tenant_id=$1 and email=$2 and consumed_at is null
		`, challenge.TenantID, challenge.Email).Scan(&latest)
		if err != nil {
			return err
		}
		if !latest.IsZero() && time.Since(latest) < minInterval {
			return domain.ErrWebAuthRateLimited
		}
		if _, err := txRepo.exec(ctx, `
			update webchat_auth_challenges
			set consumed_at=$3
			where tenant_id=$1 and email=$2 and consumed_at is null
		`, challenge.TenantID, challenge.Email, challenge.CreatedAt); err != nil {
			return err
		}
		_, err = txRepo.exec(ctx, `
			insert into webchat_auth_challenges (
				id, tenant_id, email, otp_hash, link_token_hash, expires_at, consumed_at, attempt_count, created_at
			) values ($1,$2,$3,$4,$5,$6,null,0,$7)
		`, challenge.ID, challenge.TenantID, challenge.Email, challenge.OTPHash, challenge.LinkTokenHash, challenge.ExpiresAt, challenge.CreatedAt)
		return err
	})
}

func (r *PostgresRepository) ConsumeWebAuthChallengeByOTP(ctx context.Context, tenantID, email, otpHash string, now time.Time) (domain.WebAuthChallenge, error) {
	return r.consumeWebAuthChallenge(ctx, `
		select id, tenant_id, email, otp_hash, link_token_hash, expires_at, coalesce(consumed_at,'epoch'::timestamptz), attempt_count, created_at
		from webchat_auth_challenges
		where tenant_id=$1 and email=$2 and otp_hash=$3
		order by created_at desc
		limit 1
	`, tenantID, email, otpHash, now)
}

func (r *PostgresRepository) ConsumeWebAuthChallengeByLink(ctx context.Context, tenantID, linkTokenHash string, now time.Time) (domain.WebAuthChallenge, error) {
	return r.consumeWebAuthChallenge(ctx, `
		select id, tenant_id, email, otp_hash, link_token_hash, expires_at, coalesce(consumed_at,'epoch'::timestamptz), attempt_count, created_at
		from webchat_auth_challenges
		where tenant_id=$1 and link_token_hash=$2
		order by created_at desc
		limit 1
	`, tenantID, linkTokenHash, now)
}

func (r *PostgresRepository) consumeWebAuthChallenge(ctx context.Context, sql string, args ...any) (domain.WebAuthChallenge, error) {
	now := args[len(args)-1].(time.Time)
	args = args[:len(args)-1]
	row := r.queryRow(ctx, sql, args...)
	var challenge domain.WebAuthChallenge
	if err := row.Scan(&challenge.ID, &challenge.TenantID, &challenge.Email, &challenge.OTPHash, &challenge.LinkTokenHash, &challenge.ExpiresAt, &challenge.ConsumedAt, &challenge.AttemptCount, &challenge.CreatedAt); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.WebAuthChallenge{}, domain.ErrWebAuthChallengeNotFound
		}
		return domain.WebAuthChallenge{}, err
	}
	if !challenge.ConsumedAt.IsZero() && challenge.ConsumedAt.After(time.Unix(1, 0)) {
		return domain.WebAuthChallenge{}, domain.ErrWebAuthChallengeNotFound
	}
	if now.After(challenge.ExpiresAt) {
		return domain.WebAuthChallenge{}, domain.ErrWebAuthChallengeExpired
	}
	tag, err := r.exec(ctx, `update webchat_auth_challenges set consumed_at=$2 where id=$1 and consumed_at is null`, challenge.ID, now)
	if err != nil {
		return domain.WebAuthChallenge{}, err
	}
	if tag.RowsAffected() == 0 {
		return domain.WebAuthChallenge{}, domain.ErrWebAuthChallengeNotFound
	}
	challenge.ConsumedAt = now
	return challenge, nil
}

func (r *PostgresRepository) CreateWebAuthSession(ctx context.Context, session domain.WebAuthSession) error {
	_, err := r.exec(ctx, `
		insert into webchat_auth_sessions (
			id, tenant_id, email, csrf_token_hash, expires_at, last_seen_at, created_at
		) values ($1,$2,$3,$4,$5,$6,$7)
	`, session.ID, session.TenantID, session.Email, session.CSRFTokenHash, session.ExpiresAt, session.LastSeenAt, session.CreatedAt)
	return err
}

func (r *PostgresRepository) GetWebAuthSession(ctx context.Context, sessionID string, now time.Time) (domain.WebAuthSession, error) {
	row := r.queryRow(ctx, `
		select id, tenant_id, email, csrf_token_hash, expires_at, last_seen_at, created_at
		from webchat_auth_sessions
		where id=$1
	`, sessionID)
	var session domain.WebAuthSession
	if err := row.Scan(&session.ID, &session.TenantID, &session.Email, &session.CSRFTokenHash, &session.ExpiresAt, &session.LastSeenAt, &session.CreatedAt); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.WebAuthSession{}, domain.ErrWebAuthSessionNotFound
		}
		return domain.WebAuthSession{}, err
	}
	if now.After(session.ExpiresAt) {
		return domain.WebAuthSession{}, domain.ErrWebAuthSessionNotFound
	}
	_, _ = r.exec(ctx, `update webchat_auth_sessions set last_seen_at=$2 where id=$1`, sessionID, now)
	return session, nil
}

func (r *PostgresRepository) UpdateWebAuthSessionCSRFHash(ctx context.Context, sessionID, csrfHash string, now time.Time) error {
	_, err := r.exec(ctx, `update webchat_auth_sessions set csrf_token_hash=$2, last_seen_at=$3 where id=$1`, sessionID, csrfHash, now)
	return err
}

func (r *PostgresRepository) DeleteWebAuthSession(ctx context.Context, sessionID string) error {
	_, err := r.exec(ctx, `delete from webchat_auth_sessions where id=$1`, sessionID)
	return err
}

func (r *PostgresRepository) EnsureUserByEmail(ctx context.Context, tenantID, email string) (domain.User, error) {
	email = strings.ToLower(strings.TrimSpace(email))
	id := "user_" + hashText(tenantID+"|"+email)
	_, err := r.exec(ctx, `
		insert into users (id, tenant_id, primary_email, primary_email_verified, created_at, updated_at)
		values ($1,$2,$3,true,now(),now())
		on conflict (tenant_id, primary_email)
		do update set primary_email_verified=true, updated_at=now()
	`, id, tenantID, email)
	if err != nil {
		return domain.User{}, err
	}
	return r.GetUserByEmail(ctx, tenantID, email)
}

func (r *PostgresRepository) GetUser(ctx context.Context, tenantID, userID string) (domain.User, error) {
	row := r.queryRow(ctx, `
		select id, tenant_id, primary_email, primary_email_verified,
		       coalesce(primary_phone,''), coalesce(primary_phone_normalized,''), primary_phone_verified, coalesce(primary_phone_added_at,'epoch'::timestamptz),
		       coalesce(last_step_up_at,'epoch'::timestamptz), created_at, updated_at
		from users
		where tenant_id=$1 and id=$2
	`, tenantID, userID)
	return scanUser(row)
}

func (r *PostgresRepository) GetUserByEmail(ctx context.Context, tenantID, email string) (domain.User, error) {
	row := r.queryRow(ctx, `
		select id, tenant_id, primary_email, primary_email_verified,
		       coalesce(primary_phone,''), coalesce(primary_phone_normalized,''), primary_phone_verified, coalesce(primary_phone_added_at,'epoch'::timestamptz),
		       coalesce(last_step_up_at,'epoch'::timestamptz), created_at, updated_at
		from users
		where tenant_id=$1 and primary_email=$2
	`, tenantID, strings.ToLower(strings.TrimSpace(email)))
	return scanUser(row)
}

func (r *PostgresRepository) ListUsers(ctx context.Context, tenantID string, limit int) ([]domain.User, error) {
	rows, err := r.query(ctx, `
		select id, tenant_id, primary_email, primary_email_verified,
		       coalesce(primary_phone,''), coalesce(primary_phone_normalized,''), primary_phone_verified, coalesce(primary_phone_added_at,'epoch'::timestamptz),
		       coalesce(last_step_up_at,'epoch'::timestamptz), created_at, updated_at
		from users
		where tenant_id=$1
		order by updated_at desc, id desc
		limit $2
	`, tenantID, normalizeLimit(limit))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.User
	for rows.Next() {
		var user domain.User
		if err := rows.Scan(&user.ID, &user.TenantID, &user.PrimaryEmail, &user.PrimaryEmailVerified, &user.PrimaryPhone, &user.PrimaryPhoneNormalized, &user.PrimaryPhoneVerified, &user.PrimaryPhoneAddedAt, &user.LastStepUpAt, &user.CreatedAt, &user.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, user)
	}
	return out, rows.Err()
}

func (r *PostgresRepository) UpdateUserPhone(ctx context.Context, tenantID, userID, rawPhone, normalizedPhone string, verified bool, addedAt time.Time) error {
	_, err := r.exec(ctx, `
		update users
		set primary_phone=$3,
		    primary_phone_normalized=$4,
		    primary_phone_verified=$5,
		    primary_phone_added_at=$6,
		    updated_at=now()
		where tenant_id=$1 and id=$2
	`, tenantID, userID, strings.TrimSpace(rawPhone), strings.TrimSpace(normalizedPhone), verified, nullableTimeValue(addedAt))
	return err
}

func (r *PostgresRepository) ClearUserPhone(ctx context.Context, tenantID, userID string) error {
	_, err := r.exec(ctx, `
		update users
		set primary_phone=null,
		    primary_phone_normalized=null,
		    primary_phone_verified=false,
		    primary_phone_added_at=null,
		    updated_at=now()
		where tenant_id=$1 and id=$2
	`, tenantID, userID)
	return err
}

func (r *PostgresRepository) MarkUserStepUp(ctx context.Context, tenantID, userID string, at time.Time) error {
	_, err := r.exec(ctx, `update users set last_step_up_at=$3, updated_at=$3 where tenant_id=$1 and id=$2`, tenantID, userID, at)
	return err
}

func (r *PostgresRepository) HasRecentStepUp(ctx context.Context, tenantID, userID string, since time.Time) (bool, error) {
	var ok bool
	err := r.queryRow(ctx, `select exists(select 1 from users where tenant_id=$1 and id=$2 and last_step_up_at >= $3)`, tenantID, userID, since).Scan(&ok)
	return ok, err
}

func (r *PostgresRepository) CreateStepUpChallenge(ctx context.Context, challenge domain.StepUpChallenge, minInterval time.Duration) error {
	return r.InTx(ctx, func(ctx context.Context, repo ports.Repository) error {
		txRepo, ok := repo.(*PostgresRepository)
		if !ok {
			return fmt.Errorf("unexpected repository type %T", repo)
		}
		var latest time.Time
		err := txRepo.queryRow(ctx, `
			select coalesce(max(created_at), 'epoch'::timestamptz)
			from step_up_challenges
			where tenant_id=$1 and user_id=$2 and purpose=$3 and channel_type=$4 and consumed_at is null
		`, challenge.TenantID, challenge.UserID, challenge.Purpose, challenge.ChannelType).Scan(&latest)
		if err != nil {
			return err
		}
		if !latest.IsZero() && time.Since(latest) < minInterval {
			return domain.ErrWebAuthRateLimited
		}
		if _, err := txRepo.exec(ctx, `
			update step_up_challenges
			set consumed_at=$5
			where tenant_id=$1 and user_id=$2 and purpose=$3 and channel_type=$4 and consumed_at is null
		`, challenge.TenantID, challenge.UserID, challenge.Purpose, challenge.ChannelType, challenge.CreatedAt); err != nil {
			return err
		}
		_, err = txRepo.exec(ctx, `
			insert into step_up_challenges (id, tenant_id, user_id, purpose, channel_type, code_hash, expires_at, consumed_at, created_at)
			values ($1,$2,$3,$4,$5,$6,$7,null,$8)
		`, challenge.ID, challenge.TenantID, challenge.UserID, challenge.Purpose, challenge.ChannelType, challenge.CodeHash, challenge.ExpiresAt, challenge.CreatedAt)
		return err
	})
}

func (r *PostgresRepository) ConsumeStepUpChallenge(ctx context.Context, tenantID, userID, purpose, channelType, codeHash string, now time.Time) (domain.StepUpChallenge, error) {
	row := r.queryRow(ctx, `
		select id, tenant_id, user_id, purpose, channel_type, code_hash, expires_at, coalesce(consumed_at,'epoch'::timestamptz), created_at
		from step_up_challenges
		where tenant_id=$1 and user_id=$2 and purpose=$3 and channel_type=$4 and code_hash=$5
		order by created_at desc
		limit 1
	`, tenantID, userID, purpose, channelType, codeHash)
	var challenge domain.StepUpChallenge
	if err := row.Scan(&challenge.ID, &challenge.TenantID, &challenge.UserID, &challenge.Purpose, &challenge.ChannelType, &challenge.CodeHash, &challenge.ExpiresAt, &challenge.ConsumedAt, &challenge.CreatedAt); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.StepUpChallenge{}, domain.ErrStepUpChallengeNotFound
		}
		return domain.StepUpChallenge{}, err
	}
	if !challenge.ConsumedAt.IsZero() && challenge.ConsumedAt.After(time.Unix(1, 0)) {
		return domain.StepUpChallenge{}, domain.ErrStepUpChallengeNotFound
	}
	if now.After(challenge.ExpiresAt) {
		return domain.StepUpChallenge{}, domain.ErrStepUpChallengeExpired
	}
	tag, err := r.exec(ctx, `update step_up_challenges set consumed_at=$2 where id=$1 and consumed_at is null`, challenge.ID, now)
	if err != nil {
		return domain.StepUpChallenge{}, err
	}
	if tag.RowsAffected() == 0 {
		return domain.StepUpChallenge{}, domain.ErrStepUpChallengeNotFound
	}
	challenge.ConsumedAt = now
	return challenge, nil
}

func (r *PostgresRepository) UpsertLinkedIdentity(ctx context.Context, identity domain.LinkedIdentity) error {
	if identity.ID == "" {
		identity.ID = "link_" + hashText(identity.TenantID+"|"+identity.ChannelType+"|"+identity.ChannelUserID)
	}
	if identity.Status == "" {
		identity.Status = "linked"
	}
	if identity.LinkedAt.IsZero() {
		identity.LinkedAt = time.Now().UTC()
	}
	if identity.LastVerifiedAt.IsZero() {
		identity.LastVerifiedAt = identity.LinkedAt
	}
	_, err := r.exec(ctx, `
		insert into linked_identities (id, tenant_id, user_id, channel_type, channel_user_id, status, linked_at, last_verified_at)
		values ($1,$2,$3,$4,$5,$6,$7,$8)
		on conflict (tenant_id, channel_type, channel_user_id)
		do update set user_id=excluded.user_id, status=excluded.status, linked_at=excluded.linked_at, last_verified_at=excluded.last_verified_at
	`, identity.ID, identity.TenantID, identity.UserID, identity.ChannelType, identity.ChannelUserID, identity.Status, identity.LinkedAt, identity.LastVerifiedAt)
	return err
}

func (r *PostgresRepository) GetLinkedIdentity(ctx context.Context, tenantID, channelType, channelUserID string) (domain.LinkedIdentity, error) {
	row := r.queryRow(ctx, `
		select id, tenant_id, user_id, channel_type, channel_user_id, status, linked_at, coalesce(last_verified_at,'epoch'::timestamptz)
		from linked_identities
		where tenant_id=$1 and channel_type=$2 and channel_user_id=$3
	`, tenantID, channelType, channelUserID)
	var identity domain.LinkedIdentity
	if err := row.Scan(&identity.ID, &identity.TenantID, &identity.UserID, &identity.ChannelType, &identity.ChannelUserID, &identity.Status, &identity.LinkedAt, &identity.LastVerifiedAt); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.LinkedIdentity{}, domain.ErrLinkedIdentityNotFound
		}
		return domain.LinkedIdentity{}, err
	}
	return identity, nil
}

func (r *PostgresRepository) ListLinkedIdentitiesForUser(ctx context.Context, tenantID, userID string) ([]domain.LinkedIdentity, error) {
	rows, err := r.query(ctx, `
		select id, tenant_id, user_id, channel_type, channel_user_id, status, linked_at, coalesce(last_verified_at,'epoch'::timestamptz)
		from linked_identities
		where tenant_id=$1 and user_id=$2
		order by channel_type, channel_user_id
	`, tenantID, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var items []domain.LinkedIdentity
	for rows.Next() {
		var item domain.LinkedIdentity
		if err := rows.Scan(&item.ID, &item.TenantID, &item.UserID, &item.ChannelType, &item.ChannelUserID, &item.Status, &item.LinkedAt, &item.LastVerifiedAt); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (r *PostgresRepository) DeleteLinkedIdentity(ctx context.Context, tenantID, channelType, channelUserID string) error {
	_, err := r.exec(ctx, `delete from linked_identities where tenant_id=$1 and channel_type=$2 and channel_user_id=$3`, tenantID, channelType, channelUserID)
	return err
}

func (r *PostgresRepository) GetTrustPolicy(ctx context.Context, tenantID, agentProfileID string) (domain.TrustPolicy, error) {
	row := r.queryRow(ctx, `
		select tenant_id, agent_profile_id, require_linked_identity_for_execution, require_linked_identity_for_approval,
		       require_recent_step_up_for_approval, allowed_approval_channels_json, updated_at
		from trust_policies
		where tenant_id=$1 and agent_profile_id=$2
	`, tenantID, agentProfileID)
	var policy domain.TrustPolicy
	var allowedJSON []byte
	if err := row.Scan(&policy.TenantID, &policy.AgentProfileID, &policy.RequireLinkedIdentityForExecution, &policy.RequireLinkedIdentityForApproval, &policy.RequireRecentStepUpForApproval, &allowedJSON, &policy.UpdatedAt); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.TrustPolicy{}, domain.ErrTrustPolicyNotFound
		}
		return domain.TrustPolicy{}, err
	}
	_ = json.Unmarshal(allowedJSON, &policy.AllowedApprovalChannels)
	return policy, nil
}

func (r *PostgresRepository) ListTrustPolicies(ctx context.Context, tenantID string, limit int) ([]domain.TrustPolicy, error) {
	rows, err := r.query(ctx, `
		select tenant_id, agent_profile_id, require_linked_identity_for_execution, require_linked_identity_for_approval,
		       require_recent_step_up_for_approval, allowed_approval_channels_json, updated_at
		from trust_policies
		where tenant_id=$1
		order by agent_profile_id
		limit $2
	`, tenantID, normalizeLimit(limit))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.TrustPolicy
	for rows.Next() {
		var item domain.TrustPolicy
		var allowedJSON []byte
		if err := rows.Scan(&item.TenantID, &item.AgentProfileID, &item.RequireLinkedIdentityForExecution, &item.RequireLinkedIdentityForApproval, &item.RequireRecentStepUpForApproval, &allowedJSON, &item.UpdatedAt); err != nil {
			return nil, err
		}
		_ = json.Unmarshal(allowedJSON, &item.AllowedApprovalChannels)
		out = append(out, item)
	}
	return out, rows.Err()
}

func (r *PostgresRepository) UpsertTrustPolicy(ctx context.Context, policy domain.TrustPolicy) error {
	allowedJSON, _ := json.Marshal(policy.AllowedApprovalChannels)
	if policy.UpdatedAt.IsZero() {
		policy.UpdatedAt = time.Now().UTC()
	}
	_, err := r.exec(ctx, `
		insert into trust_policies (
			tenant_id, agent_profile_id, require_linked_identity_for_execution, require_linked_identity_for_approval,
			require_recent_step_up_for_approval, allowed_approval_channels_json, updated_at
		) values ($1,$2,$3,$4,$5,$6,$7)
		on conflict (tenant_id, agent_profile_id) do update
		set require_linked_identity_for_execution=excluded.require_linked_identity_for_execution,
		    require_linked_identity_for_approval=excluded.require_linked_identity_for_approval,
		    require_recent_step_up_for_approval=excluded.require_recent_step_up_for_approval,
		    allowed_approval_channels_json=excluded.allowed_approval_channels_json,
		    updated_at=excluded.updated_at
	`, policy.TenantID, policy.AgentProfileID, policy.RequireLinkedIdentityForExecution, policy.RequireLinkedIdentityForApproval, policy.RequireRecentStepUpForApproval, allowedJSON, policy.UpdatedAt)
	return err
}

func (r *PostgresRepository) CountLinkedIdentitiesByChannel(ctx context.Context, tenantID string) (map[string]int, error) {
	rows, err := r.query(ctx, `
		select channel_type, count(*)
		from linked_identities
		where tenant_id=$1
		group by channel_type
	`, tenantID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]int{}
	for rows.Next() {
		var channel string
		var count int
		if err := rows.Scan(&channel, &count); err != nil {
			return nil, err
		}
		out[channel] = count
	}
	return out, rows.Err()
}

func (r *PostgresRepository) RecordWhatsAppInbound(ctx context.Context, tenantID, channelUserID string, inboundAt time.Time, window time.Duration) error {
	if inboundAt.IsZero() {
		inboundAt = time.Now().UTC()
	}
	if window <= 0 {
		window = 24 * time.Hour
	}
	_, err := r.exec(ctx, `
		insert into whatsapp_contact_policies (
			tenant_id, channel_user_id, last_inbound_at, window_expires_at, consent_status, created_at, updated_at
		) values ($1,$2,$3,$4,'unknown',now(),now())
		on conflict (tenant_id, channel_user_id) do update
		set last_inbound_at=greatest(coalesce(whatsapp_contact_policies.last_inbound_at, '-infinity'::timestamptz), excluded.last_inbound_at),
		    window_expires_at=greatest(coalesce(whatsapp_contact_policies.window_expires_at, '-infinity'::timestamptz), excluded.window_expires_at),
		    updated_at=now()
	`, tenantID, channelUserID, inboundAt, inboundAt.Add(window))
	return err
}

func (r *PostgresRepository) GetWhatsAppContactPolicy(ctx context.Context, tenantID, channelUserID string) (domain.WhatsAppContactPolicy, error) {
	var item domain.WhatsAppContactPolicy
	var lastInbound, expires, consentAt, templateAt, blockedAt sql.NullTime
	err := r.queryRow(ctx, `
		select tenant_id, channel_user_id, last_inbound_at, window_expires_at, consent_status,
		       consent_updated_at, last_template_sent_at, last_policy_blocked_at, created_at, updated_at
		from whatsapp_contact_policies
		where tenant_id=$1 and channel_user_id=$2
	`, tenantID, channelUserID).Scan(&item.TenantID, &item.ChannelUserID, &lastInbound, &expires, &item.ConsentStatus, &consentAt, &templateAt, &blockedAt, &item.CreatedAt, &item.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.WhatsAppContactPolicy{}, domain.ErrWhatsAppContactPolicyNotFound
	}
	if err != nil {
		return domain.WhatsAppContactPolicy{}, err
	}
	item.LastInboundAt = lastInbound.Time
	item.WindowExpiresAt = expires.Time
	item.ConsentUpdatedAt = consentAt.Time
	item.LastTemplateSentAt = templateAt.Time
	item.LastPolicyBlockedAt = blockedAt.Time
	return item, nil
}

func (r *PostgresRepository) SetWhatsAppConsentStatus(ctx context.Context, tenantID, channelUserID, status string, at time.Time) error {
	if at.IsZero() {
		at = time.Now().UTC()
	}
	if status == "" {
		status = "unknown"
	}
	_, err := r.exec(ctx, `
		insert into whatsapp_contact_policies (
			tenant_id, channel_user_id, consent_status, consent_updated_at, created_at, updated_at
		) values ($1,$2,$3,$4,now(),now())
		on conflict (tenant_id, channel_user_id) do update
		set consent_status=excluded.consent_status,
		    consent_updated_at=excluded.consent_updated_at,
		    updated_at=now()
	`, tenantID, channelUserID, status, at)
	return err
}

func (r *PostgresRepository) RecordWhatsAppTemplateSent(ctx context.Context, tenantID, channelUserID string, at time.Time) error {
	if at.IsZero() {
		at = time.Now().UTC()
	}
	_, err := r.exec(ctx, `
		insert into whatsapp_contact_policies (
			tenant_id, channel_user_id, last_template_sent_at, created_at, updated_at
		) values ($1,$2,$3,now(),now())
		on conflict (tenant_id, channel_user_id) do update
		set last_template_sent_at=excluded.last_template_sent_at,
		    updated_at=now()
	`, tenantID, channelUserID, at)
	return err
}

func (r *PostgresRepository) RecordWhatsAppPolicyBlocked(ctx context.Context, tenantID, channelUserID string, at time.Time) error {
	if at.IsZero() {
		at = time.Now().UTC()
	}
	_, err := r.exec(ctx, `
		insert into whatsapp_contact_policies (
			tenant_id, channel_user_id, last_policy_blocked_at, created_at, updated_at
		) values ($1,$2,$3,now(),now())
		on conflict (tenant_id, channel_user_id) do update
		set last_policy_blocked_at=excluded.last_policy_blocked_at,
		    updated_at=now()
	`, tenantID, channelUserID, at)
	return err
}

func (r *PostgresRepository) CountWhatsAppContacts(ctx context.Context, query domain.WhatsAppPolicyListQuery) (int, error) {
	clauses, args, err := whatsappPolicyClauses(query)
	if err != nil {
		return 0, err
	}
	var count int
	err = r.queryRow(ctx, `select count(*) from whatsapp_contact_policies where `+strings.Join(clauses, " and "), args...).Scan(&count)
	return count, err
}

func (r *PostgresRepository) ListWhatsAppContacts(ctx context.Context, query domain.WhatsAppPolicyListQuery) (domain.PagedResult[domain.WhatsAppContactPolicy], error) {
	limit := normalizeLimit(query.Limit)
	clauses, args, err := whatsappPolicyClauses(query)
	if err != nil {
		return domain.PagedResult[domain.WhatsAppContactPolicy]{}, err
	}
	if cursorTime, cursorID, ok, err := parseCursor(query.After); err != nil {
		return domain.PagedResult[domain.WhatsAppContactPolicy]{}, err
	} else if ok {
		args = append(args, cursorTime, cursorID)
		clauses = append(clauses, fmt.Sprintf("(updated_at, channel_user_id) < ($%d, $%d)", len(args)-1, len(args)))
	}
	args = append(args, limit+1)
	rows, err := r.query(ctx, `
		select tenant_id, channel_user_id, last_inbound_at, window_expires_at, consent_status,
		       consent_updated_at, last_template_sent_at, last_policy_blocked_at, created_at, updated_at
		from whatsapp_contact_policies
		where `+strings.Join(clauses, " and ")+`
		order by updated_at desc, channel_user_id desc
		limit $`+strconv.Itoa(len(args)), args...)
	if err != nil {
		return domain.PagedResult[domain.WhatsAppContactPolicy]{}, err
	}
	defer rows.Close()
	items := []domain.WhatsAppContactPolicy{}
	cursors := []cursorValue{}
	for rows.Next() {
		var item domain.WhatsAppContactPolicy
		var lastInbound, expires, consentAt, templateAt, blockedAt sql.NullTime
		if err := rows.Scan(&item.TenantID, &item.ChannelUserID, &lastInbound, &expires, &item.ConsentStatus, &consentAt, &templateAt, &blockedAt, &item.CreatedAt, &item.UpdatedAt); err != nil {
			return domain.PagedResult[domain.WhatsAppContactPolicy]{}, err
		}
		item.LastInboundAt = lastInbound.Time
		item.WindowExpiresAt = expires.Time
		item.ConsentUpdatedAt = consentAt.Time
		item.LastTemplateSentAt = templateAt.Time
		item.LastPolicyBlockedAt = blockedAt.Time
		items = append(items, item)
		cursors = append(cursors, cursorValue{Time: item.UpdatedAt, ID: item.ChannelUserID})
	}
	if err := rows.Err(); err != nil {
		return domain.PagedResult[domain.WhatsAppContactPolicy]{}, err
	}
	return finalizePage(items, cursors, limit), nil
}

func whatsappPolicyClauses(query domain.WhatsAppPolicyListQuery) ([]string, []any, error) {
	tenantID := strings.TrimSpace(query.TenantID)
	if tenantID == "" {
		return nil, nil, fmt.Errorf("tenant_id required")
	}
	clauses := []string{"tenant_id=$1"}
	args := []any{tenantID}
	if query.ConsentStatus != "" {
		args = append(args, query.ConsentStatus)
		clauses = append(clauses, fmt.Sprintf("consent_status=$%d", len(args)))
	}
	switch query.WindowState {
	case "open":
		clauses = append(clauses, "window_expires_at > now()")
	case "closed":
		clauses = append(clauses, "(window_expires_at is null or window_expires_at <= now())")
	case "":
	default:
		return nil, nil, fmt.Errorf("invalid window_state")
	}
	if query.Contains != "" {
		args = append(args, "%"+query.Contains+"%")
		clauses = append(clauses, fmt.Sprintf("channel_user_id ilike $%d", len(args)))
	}
	return clauses, args, nil
}

func (r *PostgresRepository) HasActiveRun(ctx context.Context, sessionID string) (bool, error) {
	row := r.queryRow(ctx, `select exists(select 1 from runs where session_id=$1 and status in ('starting','running','awaiting'))`, sessionID)
	var exists bool
	return exists, row.Scan(&exists)
}

func (r *PostgresRepository) StoreInboundMessage(ctx context.Context, evt domain.CanonicalInboundEvent, sessionID string) (string, error) {
	id := evt.Message.MessageID
	raw := evt.Metadata.RawPayload
	_, err := r.exec(ctx, `
		insert into messages (
			id, tenant_id, session_id, direction, channel_type, channel_message_id, role, text_preview, raw_payload_json, created_at
		) values ($1,$2,$3,'inbound',$4,$5,'user',$6,$7,now())
	`, id, evt.TenantID, sessionID, evt.Channel, evt.Message.MessageID, evt.Message.Text, raw)
	return id, err
}

func (r *PostgresRepository) StoreOutboundMessage(ctx context.Context, session domain.Session, runID string, messageKey string, text string, rawPayload []byte) (string, error) {
	key := strings.TrimSpace(messageKey)
	if key == "" {
		key = runID
	}
	id := fmt.Sprintf("msg_out_%s", hashText(session.ID+"|"+runID+"|"+key))
	_, err := r.exec(ctx, `
		insert into messages (
			id, tenant_id, session_id, direction, channel_type, channel_message_id, role, text_preview, raw_payload_json, created_at
		) values ($1,$2,$3,'outbound',$4,$5,'assistant',$6,$7,now())
		on conflict (id) do update
		set text_preview=case
				when excluded.text_preview is not null and btrim(excluded.text_preview) <> '' then excluded.text_preview
				else messages.text_preview
			end,
		    raw_payload_json=excluded.raw_payload_json
	`, id, session.TenantID, session.ID, session.ChannelType, id, text, rawPayload)
	return id, err
}

func (r *PostgresRepository) StoreArtifacts(ctx context.Context, messageID string, direction string, artifacts []domain.Artifact) error {
	for _, artifact := range artifacts {
		_, err := r.exec(ctx, `
			insert into artifacts (
				id, message_id, direction, name, mime_type, size_bytes, sha256, storage_uri, created_at
			) values ($1,$2,$3,$4,$5,$6,$7,$8,now())
			on conflict (id) do update
			set storage_uri=excluded.storage_uri,
			    sha256=excluded.sha256,
			    size_bytes=excluded.size_bytes,
			    mime_type=excluded.mime_type,
			    name=excluded.name
		`, artifact.ID, messageID, direction, artifact.Name, artifact.MIMEType, artifact.SizeBytes, artifact.SHA256, artifact.StorageURI)
		if err != nil {
			return err
		}
	}
	return nil
}

func (r *PostgresRepository) EnqueueMessage(ctx context.Context, evt domain.CanonicalInboundEvent, session domain.Session, route domain.RouteDecision, inboundMessageID string, startNow bool) (domain.QueueItem, *domain.OutboxEvent, error) {
	var position int
	row := r.queryRow(ctx, `select coalesce(max(queue_position), 0)+1 from session_queue_items where session_id=$1 and status in ('queued','starting','running','awaiting')`, session.ID)
	if err := row.Scan(&position); err != nil {
		return domain.QueueItem{}, nil, err
	}
	queue := domain.QueueItem{
		ID:               "queue_" + evt.EventID,
		SessionID:        session.ID,
		InboundMessageID: inboundMessageID,
		Status:           "queued",
		QueuePosition:    position,
		EnqueuedAt:       time.Now().UTC(),
		ExpiresAt:        time.Now().UTC().Add(24 * time.Hour),
	}
	routeJSON, err := json.Marshal(route)
	if err != nil {
		return domain.QueueItem{}, nil, err
	}
	_, err = r.exec(ctx, `
		insert into session_queue_items (
			id, tenant_id, session_id, inbound_message_id, queue_position, status, route_decision_json, enqueued_at, expires_at
		) values ($1,$2,$3,$4,$5,$6,$7,now(),$8)
	`, queue.ID, evt.TenantID, session.ID, inboundMessageID, queue.QueuePosition, queue.Status, routeJSON, queue.ExpiresAt)
	if err != nil {
		return domain.QueueItem{}, nil, err
	}
	if !startNow {
		return queue, nil, nil
	}
	outbox := &domain.OutboxEvent{
		ID:             "outbox_" + queue.ID,
		TenantID:       evt.TenantID,
		EventType:      "queue.start",
		AggregateType:  "session_queue_item",
		AggregateID:    queue.ID,
		IdempotencyKey: queue.ID,
		Status:         "queued",
		AvailableAt:    time.Now().UTC(),
	}
	_, err = r.exec(ctx, `
		insert into outbox_events (
			id, tenant_id, event_type, aggregate_type, aggregate_id, idempotency_key, payload_json, status, available_at, attempt_count
		) values ($1,$2,$3,$4,$5,$6,'{}','queued',now(),0)
	`, outbox.ID, outbox.TenantID, outbox.EventType, outbox.AggregateType, outbox.AggregateID, outbox.IdempotencyKey)
	return queue, outbox, err
}

func (r *PostgresRepository) CreateRun(ctx context.Context, run domain.Run) error {
	_, err := r.exec(ctx, `
		insert into runs (
			id, session_id, acp_run_id, agent_name, mode, status, started_at, completed_at, last_event_at
		) values ($1,$2,$3,$4,'async',$5,$6,null,$7)
	`, run.ID, run.SessionID, run.ACPRunID, run.ACPAgentName, run.Status, run.StartedAt, run.LastEventAt)
	return err
}

func (r *PostgresRepository) UpdateRunStatus(ctx context.Context, runID, status string) error {
	_, err := r.exec(ctx, `update runs set status=$2, last_event_at=now(), completed_at = case when $2 in ('completed','failed','canceled','expired') then now() else completed_at end where id=$1`, runID, status)
	return err
}

func (r *PostgresRepository) UpdateQueueItemStatus(ctx context.Context, queueItemID, status string) error {
	_, err := r.exec(ctx, `
		update session_queue_items
		set status=$2,
		    started_at = case when $2 in ('starting','running','awaiting') and started_at is null then now() else started_at end,
		    completed_at = case when $2 in ('completed','failed','canceled','expired') then now() else completed_at end
		where id=$1
	`, queueItemID, status)
	return err
}

func (r *PostgresRepository) UpdateActiveQueueItemStatus(ctx context.Context, sessionID, status string) error {
	_, err := r.exec(ctx, `
		update session_queue_items
		set status=$2,
		    completed_at = case when $2 in ('completed','failed','canceled','expired') then now() else completed_at end
		where id = (
			select id
			from session_queue_items
			where session_id=$1 and status in ('starting','running','awaiting')
			order by queue_position asc
			limit 1
		)
	`, sessionID, status)
	return err
}

func (r *PostgresRepository) EnqueueNextQueueItem(ctx context.Context, sessionID string) (*domain.OutboxEvent, error) {
	row := r.queryRow(ctx, `
		select id, tenant_id
		from session_queue_items
		where session_id=$1 and status='queued'
		order by queue_position asc
		limit 1
	`, sessionID)
	var queueID, tenantID string
	if err := row.Scan(&queueID, &tenantID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	outbox := &domain.OutboxEvent{
		ID:             "outbox_resume_" + queueID + "_" + time.Now().UTC().Format("20060102150405"),
		TenantID:       tenantID,
		EventType:      "queue.start",
		AggregateType:  "session_queue_item",
		AggregateID:    queueID,
		IdempotencyKey: queueID + ":next",
		Status:         "queued",
		AvailableAt:    time.Now().UTC(),
	}
	_, err := r.exec(ctx, `
		insert into outbox_events (
			id, tenant_id, event_type, aggregate_type, aggregate_id, idempotency_key, payload_json, status, available_at, attempt_count
		) values ($1,$2,$3,$4,$5,$6,'{}','queued',now(),0)
		on conflict (idempotency_key) do nothing
	`, outbox.ID, outbox.TenantID, outbox.EventType, outbox.AggregateType, outbox.AggregateID, outbox.IdempotencyKey)
	if err != nil {
		return nil, err
	}
	return outbox, nil
}

func (r *PostgresRepository) StoreAwait(ctx context.Context, await domain.Await) error {
	allowed, _ := json.Marshal(await.AllowedResponderIDs)
	_, err := r.exec(ctx, `
		insert into awaits (
			id, run_id, session_id, channel_type, status, schema_json, prompt_render_model_json,
			allowed_responder_ids_json, trust_policy_json, expires_at
		) values ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)
	`, await.ID, await.RunID, await.SessionID, await.ChannelType, await.Status, await.SchemaJSON, await.PromptRenderJSON, allowed, await.TrustPolicyJSON, await.ExpiresAt)
	return err
}

func (r *PostgresRepository) ResolveAwait(ctx context.Context, awaitID string, actorID, actorUserID, actorIdentityAssurance string, payload []byte) (domain.Await, error) {
	var await domain.Await
	var allowedJSON []byte
	row := r.queryRow(ctx, `
		select id, run_id, session_id, channel_type, status, schema_json, prompt_render_model_json, allowed_responder_ids_json, trust_policy_json, expires_at
		from awaits where id=$1
	`, awaitID)
	if err := row.Scan(&await.ID, &await.RunID, &await.SessionID, &await.ChannelType, &await.Status, &await.SchemaJSON, &await.PromptRenderJSON, &allowedJSON, &await.TrustPolicyJSON, &await.ExpiresAt); err != nil {
		return domain.Await{}, err
	}
	if err := json.Unmarshal(allowedJSON, &await.AllowedResponderIDs); err != nil {
		return domain.Await{}, err
	}
	if time.Now().UTC().After(await.ExpiresAt) {
		return domain.Await{}, fmt.Errorf("await expired")
	}
	if len(await.AllowedResponderIDs) > 0 && !contains(await.AllowedResponderIDs, actorID) {
		return domain.Await{}, fmt.Errorf("actor %s is not allowed to resolve await", actorID)
	}
	_, err := r.exec(ctx, `
		insert into await_responses (id, await_id, actor_channel_user_id, actor_user_id, actor_identity_assurance, response_payload_json, idempotency_key, accepted_at, rejected_reason)
		values ($1,$2,$3,$4,$5,$6,$7,now(),'')
	`, "await_response_"+awaitID, awaitID, actorID, actorUserID, actorIdentityAssurance, payload, awaitID+":"+actorID)
	if err != nil {
		return domain.Await{}, err
	}
	_, err = r.exec(ctx, `update awaits set status='resolved', resolved_at=now() where id=$1`, awaitID)
	return await, err
}

func (r *PostgresRepository) GetAwait(ctx context.Context, awaitID string) (domain.Await, error) {
	var await domain.Await
	var allowedJSON []byte
	row := r.queryRow(ctx, `
		select id, run_id, session_id, channel_type, status, schema_json, prompt_render_model_json, allowed_responder_ids_json, trust_policy_json, expires_at
		from awaits where id=$1
	`, awaitID)
	if err := row.Scan(&await.ID, &await.RunID, &await.SessionID, &await.ChannelType, &await.Status, &await.SchemaJSON, &await.PromptRenderJSON, &allowedJSON, &await.TrustPolicyJSON, &await.ExpiresAt); err != nil {
		return domain.Await{}, err
	}
	_ = json.Unmarshal(allowedJSON, &await.AllowedResponderIDs)
	return await, nil
}

func (r *PostgresRepository) EnqueueAwaitResume(ctx context.Context, req domain.ResumeRequest, tenantID string) error {
	payload, err := json.Marshal(req)
	if err != nil {
		return err
	}
	_, err = r.exec(ctx, `
		insert into outbox_events (
			id, tenant_id, event_type, aggregate_type, aggregate_id, idempotency_key, payload_json, status, available_at, attempt_count
		) values ($1,$2,'await.resume','await',$3,$4,$5,'queued',now(),0)
	`, "outbox_await_"+req.AwaitID, tenantID, req.AwaitID, req.AwaitID, payload)
	return err
}

func (r *PostgresRepository) EnqueueDelivery(ctx context.Context, delivery domain.OutboundDelivery) error {
	var err error
	if delivery.DeliveryKind == "replace" {
		previous, err := r.GetLatestDeliveryByLogicalMessage(ctx, delivery.LogicalMessageID)
		if err != nil {
			return err
		}
		delivery, err = prepareReplacementDelivery(delivery, previous)
		if err != nil {
			return err
		}
	}
	_, err = r.exec(ctx, `
		insert into outbound_deliveries (
			id, tenant_id, session_id, run_id, await_id, logical_message_id, channel_type,
			delivery_kind, provider_message_id, provider_request_id, status, attempt_count, last_error, payload_json, created_at, updated_at
		) values ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,0,'',$12,now(),now())
	`, delivery.ID, delivery.TenantID, delivery.SessionID, delivery.RunID, delivery.AwaitID, delivery.LogicalMessageID, delivery.ChannelType, delivery.DeliveryKind, delivery.ProviderMessageID, delivery.ProviderRequestID, delivery.Status, delivery.PayloadJSON)
	if err != nil {
		return err
	}
	_, err = r.exec(ctx, `
		insert into outbox_events (
			id, tenant_id, event_type, aggregate_type, aggregate_id, idempotency_key, payload_json, status, available_at, attempt_count
		) values ($1,$2,'delivery.send','outbound_delivery',$3,$4,$5,'queued',now(),0)
	`, "outbox_delivery_"+delivery.ID, delivery.TenantID, delivery.ID, delivery.ID, delivery.PayloadJSON)
	return err
}

func prepareReplacementDelivery(delivery domain.OutboundDelivery, previous *domain.OutboundDelivery) (domain.OutboundDelivery, error) {
	if previous == nil || previous.ProviderMessageID == "" {
		delivery.DeliveryKind = "send"
		return delivery, nil
	}
	var payload map[string]any
	if err := json.Unmarshal(delivery.PayloadJSON, &payload); err != nil {
		return domain.OutboundDelivery{}, err
	}
	switch delivery.ChannelType {
	case "telegram":
		if messageID, err := strconv.ParseInt(previous.ProviderMessageID, 10, 64); err == nil {
			payload["message_id"] = messageID
		} else {
			payload["message_id"] = previous.ProviderMessageID
		}
	default:
		payload["ts"] = previous.ProviderMessageID
	}
	updatedPayload, err := json.Marshal(payload)
	if err != nil {
		return domain.OutboundDelivery{}, err
	}
	delivery.PayloadJSON = updatedPayload
	delivery.DeliveryKind = "update"
	delivery.ProviderMessageID = previous.ProviderMessageID
	delivery.ProviderRequestID = previous.ProviderRequestID
	return delivery, nil
}

func (r *PostgresRepository) ClaimOutbox(ctx context.Context, now time.Time, limit int) ([]domain.OutboxEvent, error) {
	rows, err := r.query(ctx, `
		with claimed as (
			select id
			from outbox_events
			where status='queued' and available_at <= $1
			order by available_at
			limit $2
			for update skip locked
		)
		update outbox_events o
		set status='processing', claimed_at=now(), attempt_count = attempt_count + 1
		from claimed
		where o.id = claimed.id
		returning o.id, o.tenant_id, o.event_type, o.aggregate_type, o.aggregate_id, o.idempotency_key, o.payload_json, o.status, o.available_at, o.attempt_count
	`, now, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.OutboxEvent
	for rows.Next() {
		var evt domain.OutboxEvent
		if err := rows.Scan(&evt.ID, &evt.TenantID, &evt.EventType, &evt.AggregateType, &evt.AggregateID, &evt.IdempotencyKey, &evt.PayloadJSON, &evt.Status, &evt.AvailableAt, &evt.AttemptCount); err != nil {
			return nil, err
		}
		out = append(out, evt)
	}
	return out, rows.Err()
}

func (r *PostgresRepository) MarkOutboxDone(ctx context.Context, eventID string) error {
	_, err := r.exec(ctx, `update outbox_events set status='processed', processed_at=now() where id=$1`, eventID)
	return err
}

func (r *PostgresRepository) MarkOutboxFailed(ctx context.Context, eventID string, err error, next time.Time) error {
	_, execErr := r.exec(ctx, `update outbox_events set status='queued', last_error=$2, available_at=$3 where id=$1`, eventID, err.Error(), next)
	return execErr
}

func (r *PostgresRepository) GetQueueItem(ctx context.Context, queueItemID string) (domain.QueueItem, error) {
	row := r.queryRow(ctx, `select id, session_id, inbound_message_id, status, queue_position, enqueued_at, expires_at from session_queue_items where id=$1`, queueItemID)
	var item domain.QueueItem
	err := row.Scan(&item.ID, &item.SessionID, &item.InboundMessageID, &item.Status, &item.QueuePosition, &item.EnqueuedAt, &item.ExpiresAt)
	return item, err
}

func (r *PostgresRepository) GetQueueStartIdempotencyKey(ctx context.Context, queueItemID string) (string, error) {
	var key string
	err := r.pool.QueryRow(ctx, `
		select idempotency_key
		from outbox_events
		where aggregate_type='session_queue_item' and aggregate_id=$1 and event_type='queue.start'
		order by available_at desc, id desc
		limit 1
	`, queueItemID).Scan(&key)
	if err != nil {
		return "", err
	}
	return key, nil
}

func (r *PostgresRepository) GetSession(ctx context.Context, sessionID string) (domain.Session, error) {
	row := r.queryRow(ctx, `
		select id, tenant_id, coalesce(owner_user_id,''), coalesce(agent_profile_id,''), channel_type, channel_scope_key, state, last_active_at, coalesce(acp_session_id,'')
		from sessions where id=$1
	`, sessionID)
	var s domain.Session
	err := row.Scan(&s.ID, &s.TenantID, &s.OwnerUserID, &s.AgentProfileID, &s.ChannelType, &s.ChannelScopeKey, &s.State, &s.LastActiveAt, &s.ACPSessionID)
	return s, err
}

func (r *PostgresRepository) UpdateSessionACPSessionID(ctx context.Context, sessionID, acpSessionID string) error {
	_, err := r.exec(ctx, `
		update sessions
		set acp_session_id=$2, updated_at=now()
		where id=$1 and coalesce(acp_session_id,'') <> $2
	`, sessionID, acpSessionID)
	return err
}

func (r *PostgresRepository) GetRouteDecision(ctx context.Context, queueItemID string) (domain.RouteDecision, error) {
	row := r.queryRow(ctx, `select route_decision_json from session_queue_items where id=$1`, queueItemID)
	var raw []byte
	if err := row.Scan(&raw); err != nil {
		return domain.RouteDecision{}, err
	}
	var route domain.RouteDecision
	return route, json.Unmarshal(raw, &route)
}

func (r *PostgresRepository) GetInboundMessage(ctx context.Context, messageID string) (domain.Message, error) {
	row := r.queryRow(ctx, `select id, session_id, text_preview, role, direction, raw_payload_json from messages where id=$1`, messageID)
	var msg domain.Message
	err := row.Scan(&msg.MessageID, &msg.SessionID, &msg.Text, &msg.Role, &msg.Direction, &msg.RawPayload)
	msg.MessageType = "text"
	msg.Parts = []domain.Part{{ContentType: "text/plain", Content: msg.Text}}
	return msg, err
}

func (r *PostgresRepository) ListMessages(ctx context.Context, query domain.MessageListQuery) (domain.PagedResult[domain.Message], error) {
	limit := normalizeLimit(query.Limit)
	clauses := []string{"tenant_id=$1"}
	args := []any{query.TenantID}
	if query.Type != "" {
		args = append(args, query.Type)
		clauses = append(clauses, fmt.Sprintf("role=$%d", len(args)))
	}
	if query.Contains != "" {
		args = append(args, "%"+query.Contains+"%")
		clauses = append(clauses, fmt.Sprintf("text_preview ilike $%d", len(args)))
	}
	if query.SessionID != "" {
		args = append(args, query.SessionID)
		clauses = append(clauses, fmt.Sprintf("session_id=$%d", len(args)))
	}
	cursorTime, cursorID, hasCursor, err := parseCursor(query.After)
	if err != nil {
		return domain.PagedResult[domain.Message]{}, err
	}
	if hasCursor {
		args = append(args, cursorTime, cursorID)
		clauses = append(clauses, fmt.Sprintf("(created_at, id) < ($%d, $%d)", len(args)-1, len(args)))
	}
	args = append(args, limit+1)
	rows, err := r.query(ctx, fmt.Sprintf(`
		select id, session_id, text_preview, role, direction, raw_payload_json, created_at
		from messages
		where %s
		order by created_at desc, id desc
		limit $%d
	`, strings.Join(clauses, " and "), len(args)), args...)
	if err != nil {
		return domain.PagedResult[domain.Message]{}, err
	}
	defer rows.Close()
	var out []domain.Message
	var cursor []cursorValue
	for rows.Next() {
		var msg domain.Message
		var createdAt time.Time
		if err := rows.Scan(&msg.MessageID, &msg.SessionID, &msg.Text, &msg.Role, &msg.Direction, &msg.RawPayload, &createdAt); err != nil {
			return domain.PagedResult[domain.Message]{}, err
		}
		msg.MessageType = msg.Role
		msg.Parts = []domain.Part{{ContentType: "text/plain", Content: msg.Text}}
		out = append(out, msg)
		cursor = append(cursor, cursorValue{Time: createdAt, ID: msg.MessageID})
	}
	if err := rows.Err(); err != nil {
		return domain.PagedResult[domain.Message]{}, err
	}
	return finalizePage(out, cursor, limit), nil
}

func (r *PostgresRepository) CountMessages(ctx context.Context, query domain.MessageListQuery) (int, error) {
	clauses := []string{"tenant_id=$1"}
	args := []any{query.TenantID}
	if query.Type != "" {
		args = append(args, query.Type)
		clauses = append(clauses, fmt.Sprintf("role=$%d", len(args)))
	}
	if query.Contains != "" {
		args = append(args, "%"+query.Contains+"%")
		clauses = append(clauses, fmt.Sprintf("text_preview ilike $%d", len(args)))
	}
	if query.SessionID != "" {
		args = append(args, query.SessionID)
		clauses = append(clauses, fmt.Sprintf("session_id=$%d", len(args)))
	}
	row := r.queryRow(ctx, `select count(*) from messages where `+strings.Join(clauses, " and "), args...)
	var count int
	err := row.Scan(&count)
	return count, err
}

func (r *PostgresRepository) ListArtifacts(ctx context.Context, query domain.ArtifactListQuery) (domain.PagedResult[domain.Artifact], error) {
	limit := normalizeLimit(query.Limit)
	clauses := []string{"m.tenant_id=$1"}
	args := []any{query.TenantID}
	if query.MIMEType != "" {
		args = append(args, query.MIMEType)
		clauses = append(clauses, fmt.Sprintf("a.mime_type=$%d", len(args)))
	}
	if query.NameContains != "" {
		args = append(args, "%"+query.NameContains+"%")
		clauses = append(clauses, fmt.Sprintf("a.name ilike $%d", len(args)))
	}
	if query.SessionID != "" {
		args = append(args, query.SessionID)
		clauses = append(clauses, fmt.Sprintf("m.session_id=$%d", len(args)))
	}
	if query.Direction != "" {
		args = append(args, query.Direction)
		clauses = append(clauses, fmt.Sprintf("a.direction=$%d", len(args)))
	}
	if query.StoragePrefix != "" {
		args = append(args, query.StoragePrefix+"%")
		clauses = append(clauses, fmt.Sprintf("a.storage_uri like $%d", len(args)))
	}
	cursorTime, cursorID, hasCursor, err := parseCursor(query.After)
	if err != nil {
		return domain.PagedResult[domain.Artifact]{}, err
	}
	if hasCursor {
		args = append(args, cursorTime, cursorID)
		clauses = append(clauses, fmt.Sprintf("(a.created_at, a.id) < ($%d, $%d)", len(args)-1, len(args)))
	}
	args = append(args, limit+1)
	rows, err := r.query(ctx, fmt.Sprintf(`
		select a.id, a.message_id, a.name, a.mime_type, a.size_bytes, a.sha256, a.storage_uri, a.created_at
		from artifacts a
		join messages m on m.id = a.message_id
		where %s
		order by a.created_at desc, a.id desc
		limit $%d
	`, strings.Join(clauses, " and "), len(args)), args...)
	if err != nil {
		return domain.PagedResult[domain.Artifact]{}, err
	}
	defer rows.Close()
	var out []domain.Artifact
	var cursor []cursorValue
	for rows.Next() {
		var artifact domain.Artifact
		var createdAt time.Time
		if err := rows.Scan(&artifact.ID, &artifact.MessageID, &artifact.Name, &artifact.MIMEType, &artifact.SizeBytes, &artifact.SHA256, &artifact.StorageURI, &createdAt); err != nil {
			return domain.PagedResult[domain.Artifact]{}, err
		}
		out = append(out, artifact)
		cursor = append(cursor, cursorValue{Time: createdAt, ID: artifact.ID})
	}
	if err := rows.Err(); err != nil {
		return domain.PagedResult[domain.Artifact]{}, err
	}
	return finalizePage(out, cursor, limit), nil
}

func (r *PostgresRepository) CountArtifacts(ctx context.Context, query domain.ArtifactListQuery) (int, error) {
	clauses := []string{"m.tenant_id=$1"}
	args := []any{query.TenantID}
	if query.MIMEType != "" {
		args = append(args, query.MIMEType)
		clauses = append(clauses, fmt.Sprintf("a.mime_type=$%d", len(args)))
	}
	if query.NameContains != "" {
		args = append(args, "%"+query.NameContains+"%")
		clauses = append(clauses, fmt.Sprintf("a.name ilike $%d", len(args)))
	}
	if query.SessionID != "" {
		args = append(args, query.SessionID)
		clauses = append(clauses, fmt.Sprintf("m.session_id=$%d", len(args)))
	}
	if query.Direction != "" {
		args = append(args, query.Direction)
		clauses = append(clauses, fmt.Sprintf("a.direction=$%d", len(args)))
	}
	if query.StoragePrefix != "" {
		args = append(args, query.StoragePrefix+"%")
		clauses = append(clauses, fmt.Sprintf("a.storage_uri like $%d", len(args)))
	}
	row := r.queryRow(ctx, `select count(*) from artifacts a join messages m on m.id = a.message_id where `+strings.Join(clauses, " and "), args...)
	var count int
	err := row.Scan(&count)
	return count, err
}

func (r *PostgresRepository) GetSessionDetail(ctx context.Context, sessionID string, limit int) (domain.SessionDetail, error) {
	session, err := r.GetSession(ctx, sessionID)
	if err != nil {
		return domain.SessionDetail{}, err
	}
	messages, err := r.ListMessages(ctx, domain.MessageListQuery{TenantID: session.TenantID, SessionID: sessionID, CursorPage: domain.CursorPage{Limit: limit}})
	if err != nil {
		return domain.SessionDetail{}, err
	}
	artifacts, err := r.ListArtifacts(ctx, domain.ArtifactListQuery{TenantID: session.TenantID, SessionID: sessionID, CursorPage: domain.CursorPage{Limit: limit}})
	if err != nil {
		return domain.SessionDetail{}, err
	}
	runs, err := r.ListRuns(ctx, domain.RunListQuery{TenantID: session.TenantID, SessionID: sessionID, CursorPage: domain.CursorPage{Limit: limit}})
	if err != nil {
		return domain.SessionDetail{}, err
	}
	deliveries, err := r.ListDeliveries(ctx, domain.DeliveryListQuery{TenantID: session.TenantID, SessionID: sessionID, CursorPage: domain.CursorPage{Limit: limit}})
	if err != nil {
		return domain.SessionDetail{}, err
	}
	awaits, err := r.ListAwaits(ctx, domain.AwaitListQuery{TenantID: session.TenantID, SessionID: sessionID, CursorPage: domain.CursorPage{Limit: limit}})
	if err != nil {
		return domain.SessionDetail{}, err
	}
	audit, err := r.ListAuditEvents(ctx, domain.AuditEventListQuery{TenantID: session.TenantID, SessionID: sessionID, CursorPage: domain.CursorPage{Limit: limit}})
	if err != nil {
		return domain.SessionDetail{}, err
	}

	return domain.SessionDetail{
		Session:    session,
		Messages:   messages.Items,
		Artifacts:  artifacts.Items,
		Runs:       runs.Items,
		Awaits:     awaits.Items,
		Deliveries: deliveries.Items,
		Audit:      audit.Items,
	}, nil
}

func (r *PostgresRepository) GetArtifactForSession(ctx context.Context, tenantID, sessionID, artifactID string) (domain.Artifact, error) {
	row := r.queryRow(ctx, `
		select a.id, a.message_id, a.name, a.mime_type, a.size_bytes, a.sha256, coalesce(a.storage_uri, '')
		from artifacts a
		join messages m on m.id = a.message_id
		where m.tenant_id=$1 and m.session_id=$2 and a.id=$3
	`, tenantID, sessionID, artifactID)
	var artifact domain.Artifact
	if err := row.Scan(
		&artifact.ID,
		&artifact.MessageID,
		&artifact.Name,
		&artifact.MIMEType,
		&artifact.SizeBytes,
		&artifact.SHA256,
		&artifact.StorageURI,
	); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return domain.Artifact{}, domain.ErrArtifactNotFound
		}
		return domain.Artifact{}, err
	}
	return artifact, nil
}

func (r *PostgresRepository) GetDelivery(ctx context.Context, deliveryID string) (domain.OutboundDelivery, error) {
	row := r.queryRow(ctx, `
		select id, tenant_id, session_id, run_id, coalesce(await_id,''), logical_message_id, channel_type,
		       delivery_kind, status, attempt_count, last_error, coalesce(provider_message_id,''), coalesce(provider_request_id,''), payload_json
		from outbound_deliveries where id=$1
	`, deliveryID)
	var delivery domain.OutboundDelivery
	err := row.Scan(
		&delivery.ID,
		&delivery.TenantID,
		&delivery.SessionID,
		&delivery.RunID,
		&delivery.AwaitID,
		&delivery.LogicalMessageID,
		&delivery.ChannelType,
		&delivery.DeliveryKind,
		&delivery.Status,
		&delivery.AttemptCount,
		&delivery.LastError,
		&delivery.ProviderMessageID,
		&delivery.ProviderRequestID,
		&delivery.PayloadJSON,
	)
	return delivery, err
}

func (r *PostgresRepository) GetLatestDeliveryByLogicalMessage(ctx context.Context, logicalMessageID string) (*domain.OutboundDelivery, error) {
	row := r.queryRow(ctx, `
		select id, tenant_id, session_id, run_id, coalesce(await_id,''), logical_message_id, channel_type,
		       delivery_kind, status, attempt_count, last_error, coalesce(provider_message_id,''), coalesce(provider_request_id,''), payload_json
		from outbound_deliveries
		where logical_message_id=$1 and provider_message_id <> ''
		order by updated_at desc
		limit 1
	`, logicalMessageID)
	var delivery domain.OutboundDelivery
	err := row.Scan(
		&delivery.ID,
		&delivery.TenantID,
		&delivery.SessionID,
		&delivery.RunID,
		&delivery.AwaitID,
		&delivery.LogicalMessageID,
		&delivery.ChannelType,
		&delivery.DeliveryKind,
		&delivery.Status,
		&delivery.AttemptCount,
		&delivery.LastError,
		&delivery.ProviderMessageID,
		&delivery.ProviderRequestID,
		&delivery.PayloadJSON,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return &delivery, nil
}

func (r *PostgresRepository) UpdateDeliveryPayload(ctx context.Context, deliveryID string, payload []byte) error {
	_, err := r.exec(ctx, `
		update outbound_deliveries
		set payload_json=$2, updated_at=now()
		where id=$1
	`, deliveryID, payload)
	return err
}

func (r *PostgresRepository) CountSentDeliveriesSince(ctx context.Context, sessionID string, since time.Time) (int, error) {
	var count int
	err := r.queryRow(ctx, `
		select count(*)
		from outbound_deliveries
		where session_id=$1 and status='sent' and updated_at >= $2
	`, sessionID, since).Scan(&count)
	return count, err
}

func (r *PostgresRepository) HasRecentInboundMessageSince(ctx context.Context, sessionID string, since time.Time) (bool, error) {
	var ok bool
	err := r.queryRow(ctx, `
		select exists(
			select 1
			from messages
			where session_id=$1 and direction='inbound' and created_at >= $2
		)
	`, sessionID, since).Scan(&ok)
	return ok, err
}

func (r *PostgresRepository) MarkDeliverySent(ctx context.Context, deliveryID string, result domain.DeliveryResult) error {
	_, err := r.exec(ctx, `
		update outbound_deliveries
		set status='sent',
		    provider_message_id=$2,
		    provider_request_id=$3,
		    updated_at=now(),
		    last_error=''
		where id=$1
	`, deliveryID, result.ProviderMessageID, result.ProviderRequestID)
	return err
}

func (r *PostgresRepository) MarkDeliverySending(ctx context.Context, deliveryID string) error {
	_, err := r.exec(ctx, `
		update outbound_deliveries
		set status='sending', updated_at=now()
		where id=$1
	`, deliveryID)
	return err
}

func (r *PostgresRepository) MarkDeliveryFailed(ctx context.Context, deliveryID string, err error) error {
	_, execErr := r.exec(ctx, `
		update outbound_deliveries
		set status='failed',
		    updated_at=now(),
		    last_error=$2,
		    attempt_count = attempt_count + 1
		where id=$1
	`, deliveryID, err.Error())
	return execErr
}

func (r *PostgresRepository) ListSessions(ctx context.Context, query domain.SessionListQuery) (domain.PagedResult[domain.Session], error) {
	limit := normalizeLimit(query.Limit)
	clauses := []string{"tenant_id=$1"}
	args := []any{query.TenantID}
	if query.State != "" {
		args = append(args, query.State)
		clauses = append(clauses, fmt.Sprintf("state=$%d", len(args)))
	}
	if query.ChannelType != "" {
		args = append(args, query.ChannelType)
		clauses = append(clauses, fmt.Sprintf("channel_type=$%d", len(args)))
	}
	if query.OwnerUserID != "" {
		args = append(args, query.OwnerUserID)
		clauses = append(clauses, fmt.Sprintf("owner_user_id=$%d", len(args)))
	}
	cursorTime, cursorID, hasCursor, err := parseCursor(query.After)
	if err != nil {
		return domain.PagedResult[domain.Session]{}, err
	}
	if hasCursor {
		args = append(args, cursorTime, cursorID)
		clauses = append(clauses, fmt.Sprintf("(updated_at, id) < ($%d, $%d)", len(args)-1, len(args)))
	}
	args = append(args, limit+1)
	rows, err := r.query(ctx, fmt.Sprintf(`
		select id, tenant_id, coalesce(owner_user_id,''), coalesce(agent_profile_id,''), channel_type, channel_scope_key, state, last_active_at, coalesce(acp_session_id,''), updated_at
		from sessions where %s order by updated_at desc, id desc limit $%d
	`, strings.Join(clauses, " and "), len(args)), args...)
	if err != nil {
		return domain.PagedResult[domain.Session]{}, err
	}
	defer rows.Close()
	var out []domain.Session
	var cursor []cursorValue
	for rows.Next() {
		var s domain.Session
		var updatedAt time.Time
		if err := rows.Scan(&s.ID, &s.TenantID, &s.OwnerUserID, &s.AgentProfileID, &s.ChannelType, &s.ChannelScopeKey, &s.State, &s.LastActiveAt, &s.ACPSessionID, &updatedAt); err != nil {
			return domain.PagedResult[domain.Session]{}, err
		}
		out = append(out, s)
		cursor = append(cursor, cursorValue{Time: updatedAt, ID: s.ID})
	}
	if err := rows.Err(); err != nil {
		return domain.PagedResult[domain.Session]{}, err
	}
	return finalizePage(out, cursor, limit), nil
}

func (r *PostgresRepository) CountSessions(ctx context.Context, query domain.SessionListQuery) (int, error) {
	clauses := []string{"tenant_id=$1"}
	args := []any{query.TenantID}
	if query.State != "" {
		args = append(args, query.State)
		clauses = append(clauses, fmt.Sprintf("state=$%d", len(args)))
	}
	if query.ChannelType != "" {
		args = append(args, query.ChannelType)
		clauses = append(clauses, fmt.Sprintf("channel_type=$%d", len(args)))
	}
	if query.OwnerUserID != "" {
		args = append(args, query.OwnerUserID)
		clauses = append(clauses, fmt.Sprintf("owner_user_id=$%d", len(args)))
	}
	row := r.queryRow(ctx, `select count(*) from sessions where `+strings.Join(clauses, " and "), args...)
	var count int
	err := row.Scan(&count)
	return count, err
}

func (r *PostgresRepository) ListDeliveries(ctx context.Context, query domain.DeliveryListQuery) (domain.PagedResult[domain.OutboundDelivery], error) {
	limit := normalizeLimit(query.Limit)
	clauses := []string{"tenant_id=$1"}
	args := []any{query.TenantID}
	if query.Status != "" {
		args = append(args, query.Status)
		clauses = append(clauses, fmt.Sprintf("status=$%d", len(args)))
	}
	if query.SessionID != "" {
		args = append(args, query.SessionID)
		clauses = append(clauses, fmt.Sprintf("session_id=$%d", len(args)))
	}
	if query.RunID != "" {
		args = append(args, query.RunID)
		clauses = append(clauses, fmt.Sprintf("run_id=$%d", len(args)))
	}
	if query.DeliveryKind != "" {
		args = append(args, query.DeliveryKind)
		clauses = append(clauses, fmt.Sprintf("delivery_kind=$%d", len(args)))
	}
	if query.LogicalPrefix != "" {
		args = append(args, query.LogicalPrefix+"%")
		clauses = append(clauses, fmt.Sprintf("logical_message_id like $%d", len(args)))
	}
	cursorTime, cursorID, hasCursor, err := parseCursor(query.After)
	if err != nil {
		return domain.PagedResult[domain.OutboundDelivery]{}, err
	}
	if hasCursor {
		args = append(args, cursorTime, cursorID)
		clauses = append(clauses, fmt.Sprintf("(updated_at, id) < ($%d, $%d)", len(args)-1, len(args)))
	}
	args = append(args, limit+1)
	rows, err := r.query(ctx, fmt.Sprintf(`
		select id, tenant_id, session_id, run_id, coalesce(await_id,''), logical_message_id, channel_type,
		       delivery_kind, status, attempt_count, last_error, coalesce(provider_message_id,''), coalesce(provider_request_id,''), payload_json, updated_at
		from outbound_deliveries
		where %s
		order by updated_at desc, id desc
		limit $%d
	`, strings.Join(clauses, " and "), len(args)), args...)
	if err != nil {
		return domain.PagedResult[domain.OutboundDelivery]{}, err
	}
	defer rows.Close()
	var out []domain.OutboundDelivery
	var cursor []cursorValue
	for rows.Next() {
		var delivery domain.OutboundDelivery
		var updatedAt time.Time
		if err := rows.Scan(
			&delivery.ID,
			&delivery.TenantID,
			&delivery.SessionID,
			&delivery.RunID,
			&delivery.AwaitID,
			&delivery.LogicalMessageID,
			&delivery.ChannelType,
			&delivery.DeliveryKind,
			&delivery.Status,
			&delivery.AttemptCount,
			&delivery.LastError,
			&delivery.ProviderMessageID,
			&delivery.ProviderRequestID,
			&delivery.PayloadJSON,
			&updatedAt,
		); err != nil {
			return domain.PagedResult[domain.OutboundDelivery]{}, err
		}
		out = append(out, delivery)
		cursor = append(cursor, cursorValue{Time: updatedAt, ID: delivery.ID})
	}
	if err := rows.Err(); err != nil {
		return domain.PagedResult[domain.OutboundDelivery]{}, err
	}
	return finalizePage(out, cursor, limit), nil
}

func (r *PostgresRepository) CountDeliveries(ctx context.Context, query domain.DeliveryListQuery) (int, error) {
	clauses := []string{"tenant_id=$1"}
	args := []any{query.TenantID}
	if query.Status != "" {
		args = append(args, query.Status)
		clauses = append(clauses, fmt.Sprintf("status=$%d", len(args)))
	}
	if query.SessionID != "" {
		args = append(args, query.SessionID)
		clauses = append(clauses, fmt.Sprintf("session_id=$%d", len(args)))
	}
	if query.RunID != "" {
		args = append(args, query.RunID)
		clauses = append(clauses, fmt.Sprintf("run_id=$%d", len(args)))
	}
	if query.DeliveryKind != "" {
		args = append(args, query.DeliveryKind)
		clauses = append(clauses, fmt.Sprintf("delivery_kind=$%d", len(args)))
	}
	if query.LogicalPrefix != "" {
		args = append(args, query.LogicalPrefix+"%")
		clauses = append(clauses, fmt.Sprintf("logical_message_id like $%d", len(args)))
	}
	row := r.queryRow(ctx, `select count(*) from outbound_deliveries where `+strings.Join(clauses, " and "), args...)
	var count int
	err := row.Scan(&count)
	return count, err
}

func (r *PostgresRepository) ListRuns(ctx context.Context, query domain.RunListQuery) (domain.PagedResult[domain.Run], error) {
	limit := normalizeLimit(query.Limit)
	clauses := []string{"s.tenant_id=$1"}
	args := []any{query.TenantID}
	if query.Status != "" {
		args = append(args, query.Status)
		clauses = append(clauses, fmt.Sprintf("r.status=$%d", len(args)))
	}
	if query.SessionID != "" {
		args = append(args, query.SessionID)
		clauses = append(clauses, fmt.Sprintf("r.session_id=$%d", len(args)))
	}
	cursorTime, cursorID, hasCursor, err := parseCursor(query.After)
	if err != nil {
		return domain.PagedResult[domain.Run]{}, err
	}
	if hasCursor {
		args = append(args, cursorTime, cursorID)
		clauses = append(clauses, fmt.Sprintf("(r.started_at, r.id) < ($%d, $%d)", len(args)-1, len(args)))
	}
	args = append(args, limit+1)
	rows, err := r.query(ctx, fmt.Sprintf(`
		select r.id, r.session_id, r.agent_name, r.acp_run_id, r.status, r.started_at, r.last_event_at
		from runs r join sessions s on s.id = r.session_id
		where %s order by r.started_at desc, r.id desc limit $%d
	`, strings.Join(clauses, " and "), len(args)), args...)
	if err != nil {
		return domain.PagedResult[domain.Run]{}, err
	}
	defer rows.Close()
	var out []domain.Run
	var cursor []cursorValue
	for rows.Next() {
		var run domain.Run
		if err := rows.Scan(&run.ID, &run.SessionID, &run.ACPAgentName, &run.ACPRunID, &run.Status, &run.StartedAt, &run.LastEventAt); err != nil {
			return domain.PagedResult[domain.Run]{}, err
		}
		out = append(out, run)
		cursor = append(cursor, cursorValue{Time: run.StartedAt, ID: run.ID})
	}
	if err := rows.Err(); err != nil {
		return domain.PagedResult[domain.Run]{}, err
	}
	return finalizePage(out, cursor, limit), nil
}

func (r *PostgresRepository) CountRuns(ctx context.Context, query domain.RunListQuery) (int, error) {
	clauses := []string{"s.tenant_id=$1"}
	args := []any{query.TenantID}
	if query.Status != "" {
		args = append(args, query.Status)
		clauses = append(clauses, fmt.Sprintf("r.status=$%d", len(args)))
	}
	if query.SessionID != "" {
		args = append(args, query.SessionID)
		clauses = append(clauses, fmt.Sprintf("r.session_id=$%d", len(args)))
	}
	row := r.queryRow(ctx, `select count(*) from runs r join sessions s on s.id = r.session_id where `+strings.Join(clauses, " and "), args...)
	var count int
	err := row.Scan(&count)
	return count, err
}

func (r *PostgresRepository) CountAwaits(ctx context.Context, query domain.AwaitListQuery) (int, error) {
	clauses := []string{"s.tenant_id=$1"}
	args := []any{query.TenantID}
	if query.Status != "" {
		args = append(args, query.Status)
		clauses = append(clauses, fmt.Sprintf("a.status=$%d", len(args)))
	}
	if query.SessionID != "" {
		args = append(args, query.SessionID)
		clauses = append(clauses, fmt.Sprintf("a.session_id=$%d", len(args)))
	}
	if query.RunID != "" {
		args = append(args, query.RunID)
		clauses = append(clauses, fmt.Sprintf("a.run_id=$%d", len(args)))
	}
	row := r.queryRow(ctx, `select count(*) from awaits a join sessions s on s.id = a.session_id where `+strings.Join(clauses, " and "), args...)
	var count int
	err := row.Scan(&count)
	return count, err
}

func (r *PostgresRepository) ListAwaits(ctx context.Context, query domain.AwaitListQuery) (domain.PagedResult[domain.Await], error) {
	limit := normalizeLimit(query.Limit)
	clauses := []string{"s.tenant_id=$1"}
	args := []any{query.TenantID}
	if query.Status != "" {
		args = append(args, query.Status)
		clauses = append(clauses, fmt.Sprintf("a.status=$%d", len(args)))
	}
	if query.SessionID != "" {
		args = append(args, query.SessionID)
		clauses = append(clauses, fmt.Sprintf("a.session_id=$%d", len(args)))
	}
	if query.RunID != "" {
		args = append(args, query.RunID)
		clauses = append(clauses, fmt.Sprintf("a.run_id=$%d", len(args)))
	}
	cursorTime, cursorID, hasCursor, err := parseCursor(query.After)
	if err != nil {
		return domain.PagedResult[domain.Await]{}, err
	}
	if hasCursor {
		args = append(args, cursorTime, cursorID)
		clauses = append(clauses, fmt.Sprintf("(a.expires_at, a.id) < ($%d, $%d)", len(args)-1, len(args)))
	}
	args = append(args, limit+1)
	rows, err := r.query(ctx, fmt.Sprintf(`
		select a.id, a.run_id, a.session_id, a.channel_type, a.status, a.schema_json, a.prompt_render_model_json, a.allowed_responder_ids_json, a.trust_policy_json, a.expires_at
		from awaits a join sessions s on s.id = a.session_id
		where %s
		order by a.expires_at desc, a.id desc
		limit $%d
	`, strings.Join(clauses, " and "), len(args)), args...)
	if err != nil {
		return domain.PagedResult[domain.Await]{}, err
	}
	defer rows.Close()
	var out []domain.Await
	var cursor []cursorValue
	for rows.Next() {
		var item domain.Await
		var allowedJSON []byte
		if err := rows.Scan(&item.ID, &item.RunID, &item.SessionID, &item.ChannelType, &item.Status, &item.SchemaJSON, &item.PromptRenderJSON, &allowedJSON, &item.TrustPolicyJSON, &item.ExpiresAt); err != nil {
			return domain.PagedResult[domain.Await]{}, err
		}
		_ = json.Unmarshal(allowedJSON, &item.AllowedResponderIDs)
		out = append(out, item)
		cursor = append(cursor, cursorValue{Time: item.ExpiresAt, ID: item.ID})
	}
	if err := rows.Err(); err != nil {
		return domain.PagedResult[domain.Await]{}, err
	}
	return finalizePage(out, cursor, limit), nil
}

func (r *PostgresRepository) ListAuditEvents(ctx context.Context, query domain.AuditEventListQuery) (domain.PagedResult[domain.AuditEvent], error) {
	limit := normalizeLimit(query.Limit)
	clauses := []string{"tenant_id=$1"}
	args := []any{query.TenantID}
	if query.SessionID != "" {
		args = append(args, query.SessionID)
		clauses = append(clauses, fmt.Sprintf("session_id=$%d", len(args)))
	}
	if query.RunID != "" {
		args = append(args, query.RunID)
		clauses = append(clauses, fmt.Sprintf("run_id=$%d", len(args)))
	}
	if query.AwaitID != "" {
		args = append(args, query.AwaitID)
		clauses = append(clauses, fmt.Sprintf("await_id=$%d", len(args)))
	}
	if query.AggregateType != "" {
		args = append(args, query.AggregateType)
		clauses = append(clauses, fmt.Sprintf("aggregate_type=$%d", len(args)))
	}
	if query.AggregateID != "" {
		args = append(args, query.AggregateID)
		clauses = append(clauses, fmt.Sprintf("aggregate_id=$%d", len(args)))
	}
	if query.EventType != "" {
		args = append(args, query.EventType)
		clauses = append(clauses, fmt.Sprintf("event_type=$%d", len(args)))
	}
	cursorTime, cursorID, hasCursor, err := parseCursor(query.After)
	if err != nil {
		return domain.PagedResult[domain.AuditEvent]{}, err
	}
	if hasCursor {
		args = append(args, cursorTime, cursorID)
		clauses = append(clauses, fmt.Sprintf("(created_at, id) < ($%d, $%d)", len(args)-1, len(args)))
	}
	args = append(args, limit+1)
	rows, err := r.query(ctx, fmt.Sprintf(`
		select id, tenant_id, coalesce(session_id,''), coalesce(run_id,''), coalesce(await_id,''), aggregate_type, aggregate_id, event_type, payload_json, created_at
		from audit_events
		where %s
		order by created_at desc, id desc
		limit $%d
	`, strings.Join(clauses, " and "), len(args)), args...)
	if err != nil {
		return domain.PagedResult[domain.AuditEvent]{}, err
	}
	defer rows.Close()
	var out []domain.AuditEvent
	var cursor []cursorValue
	for rows.Next() {
		var item domain.AuditEvent
		if err := rows.Scan(&item.ID, &item.TenantID, &item.SessionID, &item.RunID, &item.AwaitID, &item.AggregateType, &item.AggregateID, &item.EventType, &item.PayloadJSON, &item.CreatedAt); err != nil {
			return domain.PagedResult[domain.AuditEvent]{}, err
		}
		out = append(out, item)
		cursor = append(cursor, cursorValue{Time: item.CreatedAt, ID: item.ID})
	}
	if err := rows.Err(); err != nil {
		return domain.PagedResult[domain.AuditEvent]{}, err
	}
	return finalizePage(out, cursor, limit), nil
}

func (r *PostgresRepository) CountAuditEvents(ctx context.Context, query domain.AuditEventListQuery) (int, error) {
	clauses := []string{"tenant_id=$1"}
	args := []any{query.TenantID}
	if query.SessionID != "" {
		args = append(args, query.SessionID)
		clauses = append(clauses, fmt.Sprintf("session_id=$%d", len(args)))
	}
	if query.RunID != "" {
		args = append(args, query.RunID)
		clauses = append(clauses, fmt.Sprintf("run_id=$%d", len(args)))
	}
	if query.AwaitID != "" {
		args = append(args, query.AwaitID)
		clauses = append(clauses, fmt.Sprintf("await_id=$%d", len(args)))
	}
	if query.AggregateType != "" {
		args = append(args, query.AggregateType)
		clauses = append(clauses, fmt.Sprintf("aggregate_type=$%d", len(args)))
	}
	if query.AggregateID != "" {
		args = append(args, query.AggregateID)
		clauses = append(clauses, fmt.Sprintf("aggregate_id=$%d", len(args)))
	}
	if query.EventType != "" {
		args = append(args, query.EventType)
		clauses = append(clauses, fmt.Sprintf("event_type=$%d", len(args)))
	}
	row := r.queryRow(ctx, `select count(*) from audit_events where `+strings.Join(clauses, " and "), args...)
	var count int
	err := row.Scan(&count)
	return count, err
}

func (r *PostgresRepository) GetRun(ctx context.Context, runID string) (domain.Run, error) {
	row := r.queryRow(ctx, `select id, session_id, agent_name, acp_run_id, status, started_at, last_event_at from runs where id=$1`, runID)
	var run domain.Run
	err := row.Scan(&run.ID, &run.SessionID, &run.ACPAgentName, &run.ACPRunID, &run.Status, &run.StartedAt, &run.LastEventAt)
	return run, err
}

func (r *PostgresRepository) GetRunByACP(ctx context.Context, acpRunID string) (domain.Run, error) {
	row := r.queryRow(ctx, `select id, session_id, agent_name, acp_run_id, status, started_at, last_event_at from runs where acp_run_id=$1`, acpRunID)
	var run domain.Run
	err := row.Scan(&run.ID, &run.SessionID, &run.ACPAgentName, &run.ACPRunID, &run.Status, &run.StartedAt, &run.LastEventAt)
	return run, err
}

func (r *PostgresRepository) GetAwaitsForRun(ctx context.Context, runID string, limit int) ([]domain.Await, error) {
	rows, err := r.query(ctx, `
		select id, run_id, session_id, channel_type, status, schema_json, prompt_render_model_json, allowed_responder_ids_json, trust_policy_json, expires_at
		from awaits where run_id=$1 order by expires_at desc, id desc limit $2
	`, runID, normalizeLimit(limit))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.Await
	for rows.Next() {
		var item domain.Await
		var allowedJSON []byte
		if err := rows.Scan(&item.ID, &item.RunID, &item.SessionID, &item.ChannelType, &item.Status, &item.SchemaJSON, &item.PromptRenderJSON, &allowedJSON, &item.TrustPolicyJSON, &item.ExpiresAt); err != nil {
			return nil, err
		}
		_ = json.Unmarshal(allowedJSON, &item.AllowedResponderIDs)
		out = append(out, item)
	}
	return out, rows.Err()
}

func (r *PostgresRepository) GetAwaitResponses(ctx context.Context, awaitID string, limit int) ([]domain.AwaitResponse, error) {
	rows, err := r.query(ctx, `
		select id, await_id, actor_channel_user_id, actor_user_id, actor_identity_assurance, response_payload_json, idempotency_key, accepted_at, rejected_reason
		from await_responses
		where await_id=$1
		order by accepted_at desc, id desc
		limit $2
	`, awaitID, normalizeLimit(limit))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.AwaitResponse
	for rows.Next() {
		var item domain.AwaitResponse
		if err := rows.Scan(&item.ID, &item.AwaitID, &item.ActorChannelUserID, &item.ActorUserID, &item.ActorIdentityAssurance, &item.ResponsePayloadJSON, &item.IdempotencyKey, &item.AcceptedAt, &item.RejectedReason); err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

func (r *PostgresRepository) GetRunDetail(ctx context.Context, runID string, limit int) (domain.RunDetail, error) {
	run, err := r.GetRun(ctx, runID)
	if err != nil {
		return domain.RunDetail{}, err
	}
	session, err := r.GetSession(ctx, run.SessionID)
	if err != nil {
		return domain.RunDetail{}, err
	}
	messages, err := r.ListMessages(ctx, domain.MessageListQuery{TenantID: session.TenantID, SessionID: session.ID, CursorPage: domain.CursorPage{Limit: limit}})
	if err != nil {
		return domain.RunDetail{}, err
	}
	artifacts, err := r.ListArtifacts(ctx, domain.ArtifactListQuery{TenantID: session.TenantID, SessionID: session.ID, CursorPage: domain.CursorPage{Limit: limit}})
	if err != nil {
		return domain.RunDetail{}, err
	}
	awaits, err := r.GetAwaitsForRun(ctx, runID, limit)
	if err != nil {
		return domain.RunDetail{}, err
	}
	deliveries, err := r.ListDeliveries(ctx, domain.DeliveryListQuery{TenantID: session.TenantID, RunID: runID, CursorPage: domain.CursorPage{Limit: limit}})
	if err != nil {
		return domain.RunDetail{}, err
	}
	audit, err := r.ListAuditEvents(ctx, domain.AuditEventListQuery{TenantID: session.TenantID, RunID: runID, CursorPage: domain.CursorPage{Limit: limit}})
	if err != nil {
		return domain.RunDetail{}, err
	}
	return domain.RunDetail{
		Run:        run,
		Messages:   messages.Items,
		Artifacts:  artifacts.Items,
		Awaits:     awaits,
		Deliveries: deliveries.Items,
		Audit:      audit.Items,
	}, nil
}

func (r *PostgresRepository) GetAwaitDetail(ctx context.Context, awaitID string, limit int) (domain.AwaitDetail, error) {
	await, err := r.GetAwait(ctx, awaitID)
	if err != nil {
		return domain.AwaitDetail{}, err
	}
	session, err := r.GetSession(ctx, await.SessionID)
	if err != nil {
		return domain.AwaitDetail{}, err
	}
	responses, err := r.GetAwaitResponses(ctx, awaitID, limit)
	if err != nil {
		return domain.AwaitDetail{}, err
	}
	audit, err := r.ListAuditEvents(ctx, domain.AuditEventListQuery{TenantID: session.TenantID, AwaitID: awaitID, CursorPage: domain.CursorPage{Limit: limit}})
	if err != nil {
		return domain.AwaitDetail{}, err
	}
	return domain.AwaitDetail{Await: await, Responses: responses, Audit: audit.Items}, nil
}

func (r *PostgresRepository) ForceCancelRun(ctx context.Context, runID string) error {
	run, err := r.GetRun(ctx, runID)
	if err != nil {
		return err
	}
	session, err := r.GetSession(ctx, run.SessionID)
	if err != nil {
		return err
	}
	_, err = r.exec(ctx, `update runs set status='canceled', completed_at=now(), last_event_at=now() where id=$1`, runID)
	if err != nil {
		return err
	}
	return r.Audit(ctx, domain.AuditEvent{
		ID:            fmt.Sprintf("audit_run_cancel_%s_%d", runID, time.Now().UTC().UnixNano()),
		TenantID:      session.TenantID,
		SessionID:     session.ID,
		RunID:         run.ID,
		AggregateType: "run",
		AggregateID:   runID,
		EventType:     "run.force_canceled",
		PayloadJSON:   []byte(`{"source":"admin"}`),
		CreatedAt:     time.Now().UTC(),
	})
}

func (r *PostgresRepository) RetryDelivery(ctx context.Context, deliveryID string) error {
	delivery, err := r.GetDelivery(ctx, deliveryID)
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	_, err = r.exec(ctx, `update outbound_deliveries set status='queued', updated_at=now() where id=$1`, deliveryID)
	if err != nil {
		return err
	}
	_, err = r.exec(ctx, `
		insert into outbox_events (id, tenant_id, event_type, aggregate_type, aggregate_id, idempotency_key, payload_json, status, available_at, attempt_count)
		select $2, tenant_id, 'delivery.send', 'outbound_delivery', id, $3, payload_json, 'queued', now(), 0
		from outbound_deliveries where id=$1
	`, deliveryID, fmt.Sprintf("outbox_retry_%s_%d", deliveryID, now.UnixNano()), fmt.Sprintf("%s:retry:%d", deliveryID, now.UnixNano()))
	if err != nil {
		return err
	}
	return r.Audit(ctx, domain.AuditEvent{
		ID:            fmt.Sprintf("audit_delivery_retry_%s_%d", deliveryID, time.Now().UTC().UnixNano()),
		TenantID:      delivery.TenantID,
		SessionID:     delivery.SessionID,
		RunID:         delivery.RunID,
		AwaitID:       delivery.AwaitID,
		AggregateType: "outbound_delivery",
		AggregateID:   deliveryID,
		EventType:     "delivery.retry_requested",
		PayloadJSON:   []byte(`{"source":"admin"}`),
		CreatedAt:     time.Now().UTC(),
	})
}

func (r *PostgresRepository) ListStaleClaimedOutbox(ctx context.Context, before time.Time, limit int) ([]domain.OutboxEvent, error) {
	rows, err := r.query(ctx, `
		select id, tenant_id, event_type, aggregate_type, aggregate_id, idempotency_key, payload_json, status, available_at, attempt_count
		from outbox_events
		where status='processing' and claimed_at < $1
		order by claimed_at asc
		limit $2
	`, before, normalizeLimit(limit))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.OutboxEvent
	for rows.Next() {
		var evt domain.OutboxEvent
		if err := rows.Scan(&evt.ID, &evt.TenantID, &evt.EventType, &evt.AggregateType, &evt.AggregateID, &evt.IdempotencyKey, &evt.PayloadJSON, &evt.Status, &evt.AvailableAt, &evt.AttemptCount); err != nil {
			return nil, err
		}
		out = append(out, evt)
	}
	return out, rows.Err()
}

func (r *PostgresRepository) RequeueOutbox(ctx context.Context, eventID string) error {
	_, err := r.exec(ctx, `update outbox_events set status='queued', claimed_at=null, available_at=now() where id=$1`, eventID)
	return err
}

func (r *PostgresRepository) RequeueQueueStartOutbox(ctx context.Context, queueItemID, tenantID string) error {
	tag, err := r.exec(ctx, `
		update outbox_events
		set status='queued', claimed_at=null, available_at=now(), last_error=''
		where aggregate_id=$1 and event_type='queue.start' and status <> 'processed'
	`, queueItemID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() > 0 {
		return nil
	}
	now := time.Now().UTC()
	_, err = r.exec(ctx, `
		insert into outbox_events (
			id, tenant_id, event_type, aggregate_type, aggregate_id, idempotency_key, payload_json, status, available_at, attempt_count
		) values ($1,$2,'queue.start','session_queue_item',$3,$4,'{}','queued',now(),0)
	`,
		fmt.Sprintf("outbox_repair_%s_%d", queueItemID, now.UnixNano()),
		tenantID,
		queueItemID,
		fmt.Sprintf("%s:repair:%d", queueItemID, now.UnixNano()),
	)
	return err
}

func (r *PostgresRepository) ListStuckQueueItems(ctx context.Context, before time.Time, limit int) ([]domain.QueueItem, error) {
	rows, err := r.query(ctx, `
		select id, session_id, inbound_message_id, status, queue_position, enqueued_at, expires_at
		from session_queue_items
		where status='starting' and coalesce(started_at, enqueued_at) < $1
		order by coalesce(started_at, enqueued_at) asc
		limit $2
	`, before, normalizeLimit(limit))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.QueueItem
	for rows.Next() {
		var item domain.QueueItem
		if err := rows.Scan(&item.ID, &item.SessionID, &item.InboundMessageID, &item.Status, &item.QueuePosition, &item.EnqueuedAt, &item.ExpiresAt); err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

func (r *PostgresRepository) ListStaleRuns(ctx context.Context, before time.Time, limit int) ([]domain.Run, error) {
	rows, err := r.query(ctx, `
		select id, session_id, agent_name, acp_run_id, status, started_at, last_event_at
		from runs
		where status in ('starting','running','awaiting') and last_event_at < $1
		order by last_event_at asc
		limit $2
	`, before, normalizeLimit(limit))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.Run
	for rows.Next() {
		var run domain.Run
		if err := rows.Scan(&run.ID, &run.SessionID, &run.ACPAgentName, &run.ACPRunID, &run.Status, &run.StartedAt, &run.LastEventAt); err != nil {
			return nil, err
		}
		out = append(out, run)
	}
	return out, rows.Err()
}

func (r *PostgresRepository) ListExpiredAwaits(ctx context.Context, before time.Time, limit int) ([]domain.Await, error) {
	rows, err := r.query(ctx, `
		select id, run_id, session_id, channel_type, status, schema_json, prompt_render_model_json, allowed_responder_ids_json, trust_policy_json, expires_at
		from awaits
		where status='pending' and expires_at < $1
		order by expires_at asc
		limit $2
	`, before, normalizeLimit(limit))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.Await
	for rows.Next() {
		var item domain.Await
		var allowedJSON []byte
		if err := rows.Scan(&item.ID, &item.RunID, &item.SessionID, &item.ChannelType, &item.Status, &item.SchemaJSON, &item.PromptRenderJSON, &allowedJSON, &item.TrustPolicyJSON, &item.ExpiresAt); err != nil {
			return nil, err
		}
		_ = json.Unmarshal(allowedJSON, &item.AllowedResponderIDs)
		out = append(out, item)
	}
	return out, rows.Err()
}

func (r *PostgresRepository) ListStaleDeliveries(ctx context.Context, before time.Time, maxAttempts int, limit int) ([]domain.OutboundDelivery, error) {
	rows, err := r.query(ctx, `
		select id, tenant_id, session_id, run_id, coalesce(await_id,''), logical_message_id, channel_type,
		       delivery_kind, status, attempt_count, last_error, coalesce(provider_message_id,''), coalesce(provider_request_id,''), payload_json
		from outbound_deliveries
		where status in ('queued','sending') and updated_at < $1 and attempt_count < $2
		order by updated_at asc
		limit $3
	`, before, maxAttempts, normalizeLimit(limit))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.OutboundDelivery
	for rows.Next() {
		var delivery domain.OutboundDelivery
		if err := rows.Scan(
			&delivery.ID, &delivery.TenantID, &delivery.SessionID, &delivery.RunID, &delivery.AwaitID,
			&delivery.LogicalMessageID, &delivery.ChannelType, &delivery.DeliveryKind, &delivery.Status,
			&delivery.AttemptCount, &delivery.LastError, &delivery.ProviderMessageID, &delivery.ProviderRequestID, &delivery.PayloadJSON,
		); err != nil {
			return nil, err
		}
		out = append(out, delivery)
	}
	return out, rows.Err()
}

func (r *PostgresRepository) ExpireAwait(ctx context.Context, awaitID string) error {
	_, err := r.exec(ctx, `update awaits set status='expired', resolved_at=now() where id=$1 and status='pending'`, awaitID)
	return err
}

func (r *PostgresRepository) RepairRunFromSnapshot(ctx context.Context, queueItem domain.QueueItem, snapshot domain.RunStatusSnapshot) (domain.Run, error) {
	run, err := r.GetRunByACP(ctx, snapshot.ACPRunID)
	if err == nil {
		if updateErr := r.UpdateRunStatus(ctx, run.ID, snapshot.Status); updateErr != nil {
			return domain.Run{}, updateErr
		}
		return r.GetRun(ctx, run.ID)
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return domain.Run{}, err
	}
	run = domain.Run{
		ID:          "run_" + snapshot.ACPRunID,
		SessionID:   queueItem.SessionID,
		ACPRunID:    snapshot.ACPRunID,
		Status:      snapshot.Status,
		StartedAt:   time.Now().UTC(),
		LastEventAt: time.Now().UTC(),
	}
	if route, routeErr := r.GetRouteDecision(ctx, queueItem.ID); routeErr == nil {
		run.ACPAgentName = route.ACPAgentName
	}
	if run.ACPAgentName == "" {
		run.ACPAgentName = "default-agent"
	}
	if err := r.CreateRun(ctx, run); err != nil {
		return domain.Run{}, err
	}
	return run, nil
}

func (r *PostgresRepository) CreateVirtualSession(ctx context.Context, tenantID, channelType, surfaceKey, ownerUserID, agentProfileID, alias string) (domain.Session, error) {
	id := fmt.Sprintf("session_%s_%d", channelType, time.Now().UTC().UnixNano())
	session := domain.Session{
		ID:              id,
		TenantID:        tenantID,
		OwnerUserID:     ownerUserID,
		AgentProfileID:  agentProfileID,
		ChannelType:     channelType,
		ChannelScopeKey: surfaceKey + ":" + id,
		State:           "open",
		LastActiveAt:    time.Now().UTC(),
	}
	_, err := r.exec(ctx, `
		insert into sessions (
			id, tenant_id, owner_user_id, agent_profile_id, channel_type, channel_scope_key,
			acp_connection_id, acp_server_url, acp_agent_name, acp_session_id, mode, state, last_active_at, created_at, updated_at
		) values ($1,$2,$3,$4,$5,$6,'acp_default','','','', 'per-user','open',now(),now(),now())
	`, session.ID, session.TenantID, session.OwnerUserID, session.AgentProfileID, session.ChannelType, session.ChannelScopeKey)
	if err != nil {
		return domain.Session{}, err
	}
	if err := r.upsertSurfaceState(ctx, tenantID, channelType, surfaceKey, session.ID); err != nil {
		return domain.Session{}, err
	}
	if alias != "" {
		if err := r.upsertSessionAlias(ctx, tenantID, channelType, surfaceKey, session.ID, alias); err != nil {
			return domain.Session{}, err
		}
	}
	return session, nil
}

func (r *PostgresRepository) EnsureNotificationSession(ctx context.Context, tenantID, channelType, surfaceKey, ownerUserID string) (domain.Session, error) {
	scopeKey := surfaceKey + ":notice"
	row := r.queryRow(ctx, `
		select id, tenant_id, coalesce(owner_user_id,''), coalesce(agent_profile_id,''), channel_type, channel_scope_key, state, last_active_at, coalesce(acp_session_id,'')
		from sessions
		where tenant_id=$1 and channel_type=$2 and channel_scope_key=$3
		limit 1
	`, tenantID, channelType, scopeKey)
	var session domain.Session
	if err := row.Scan(&session.ID, &session.TenantID, &session.OwnerUserID, &session.AgentProfileID, &session.ChannelType, &session.ChannelScopeKey, &session.State, &session.LastActiveAt, &session.ACPSessionID); err == nil {
		return session, nil
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return domain.Session{}, err
	}
	session = domain.Session{
		ID:              notificationSessionID(tenantID, channelType, surfaceKey, ownerUserID),
		TenantID:        tenantID,
		OwnerUserID:     ownerUserID,
		ChannelType:     channelType,
		ChannelScopeKey: scopeKey,
		State:           "open",
		LastActiveAt:    time.Now().UTC(),
	}
	_, err := r.exec(ctx, `
		insert into sessions (
			id, tenant_id, owner_user_id, agent_profile_id, channel_type, channel_scope_key,
			acp_connection_id, acp_server_url, acp_agent_name, acp_session_id, mode, state, last_active_at, created_at, updated_at
		) values ($1,$2,$3,'',$4,$5,'acp_default','','','', 'notification','open',now(),now(),now())
	`, session.ID, session.TenantID, session.OwnerUserID, session.ChannelType, session.ChannelScopeKey)
	if err != nil {
		return domain.Session{}, err
	}
	return session, nil
}

func (r *PostgresRepository) SwitchActiveSession(ctx context.Context, tenantID, channelType, surfaceKey, ownerUserID, aliasOrID string) (domain.Session, error) {
	row := r.queryRow(ctx, `
		select s.id, s.tenant_id, coalesce(s.owner_user_id,''), coalesce(s.agent_profile_id,''), s.channel_type, s.channel_scope_key, s.state, s.last_active_at, coalesce(s.acp_session_id,'')
		from sessions s
		left join session_aliases sa on sa.session_id = s.id and sa.tenant_id=$1 and sa.channel_type=$2 and sa.surface_key=$3
		where s.tenant_id=$1 and s.channel_type=$2 and s.owner_user_id=$4 and s.state in ('open','paused')
		  and (s.id=$5 or sa.alias=$5)
		limit 1
	`, tenantID, channelType, surfaceKey, ownerUserID, aliasOrID)
	var session domain.Session
	if err := row.Scan(&session.ID, &session.TenantID, &session.OwnerUserID, &session.AgentProfileID, &session.ChannelType, &session.ChannelScopeKey, &session.State, &session.LastActiveAt, &session.ACPSessionID); err != nil {
		return domain.Session{}, err
	}
	if err := r.upsertSurfaceState(ctx, tenantID, channelType, surfaceKey, session.ID); err != nil {
		return domain.Session{}, err
	}
	return session, nil
}

func (r *PostgresRepository) ListSurfaceSessions(ctx context.Context, tenantID, channelType, surfaceKey, ownerUserID string, limit int) ([]domain.SurfaceSession, error) {
	rows, err := r.query(ctx, `
		select s.id, s.tenant_id, coalesce(s.owner_user_id,''), coalesce(s.agent_profile_id,''), s.channel_type, s.channel_scope_key, s.state, s.last_active_at, coalesce(s.acp_session_id,''), coalesce(sa.alias,'')
		from sessions s
		left join session_aliases sa on sa.session_id = s.id and sa.tenant_id=$1 and sa.channel_type=$2 and sa.surface_key=$3
		where s.tenant_id=$1 and s.channel_type=$2 and s.owner_user_id=$4
		order by s.updated_at desc
		limit $5
	`, tenantID, channelType, surfaceKey, ownerUserID, normalizeLimit(limit))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.SurfaceSession
	for rows.Next() {
		var item domain.SurfaceSession
		if err := rows.Scan(&item.Session.ID, &item.Session.TenantID, &item.Session.OwnerUserID, &item.Session.AgentProfileID, &item.Session.ChannelType, &item.Session.ChannelScopeKey, &item.Session.State, &item.Session.LastActiveAt, &item.Session.ACPSessionID, &item.Alias); err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

func (r *PostgresRepository) CloseActiveSession(ctx context.Context, tenantID, channelType, surfaceKey, ownerUserID string) (domain.Session, error) {
	row := r.queryRow(ctx, `
		select s.id, s.tenant_id, coalesce(s.owner_user_id,''), coalesce(s.agent_profile_id,''), s.channel_type, s.channel_scope_key, s.state, s.last_active_at, coalesce(s.acp_session_id,'')
		from channel_surface_state css
		join sessions s on s.id = css.active_session_id
		where css.tenant_id=$1 and css.channel_type=$2 and css.surface_key=$3 and s.owner_user_id=$4
		limit 1
	`, tenantID, channelType, surfaceKey, ownerUserID)
	var session domain.Session
	if err := row.Scan(&session.ID, &session.TenantID, &session.OwnerUserID, &session.AgentProfileID, &session.ChannelType, &session.ChannelScopeKey, &session.State, &session.LastActiveAt, &session.ACPSessionID); err != nil {
		return domain.Session{}, err
	}
	if _, err := r.exec(ctx, `update sessions set state='archived', updated_at=now() where id=$1`, session.ID); err != nil {
		return domain.Session{}, err
	}
	if _, err := r.exec(ctx, `update channel_surface_state set active_session_id=null, updated_at=now() where tenant_id=$1 and channel_type=$2 and surface_key=$3`, tenantID, channelType, surfaceKey); err != nil {
		return domain.Session{}, err
	}
	session.State = "archived"
	return session, nil
}

func (r *PostgresRepository) IsTelegramUserAllowed(ctx context.Context, tenantID, telegramUserID string) (bool, error) {
	row := r.queryRow(ctx, `select allowed, status from telegram_user_access where tenant_id=$1 and telegram_user_id=$2`, tenantID, telegramUserID)
	var allowed bool
	var status string
	if err := row.Scan(&allowed, &status); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return false, nil
		}
		return false, err
	}
	return allowed && status == "approved", nil
}

func (r *PostgresRepository) CountTelegramUserAccess(ctx context.Context, tenantID, status string) (int, error) {
	clauses := []string{"tenant_id=$1"}
	args := []any{tenantID}
	if status != "" {
		args = append(args, status)
		clauses = append(clauses, fmt.Sprintf("status=$%d", len(args)))
	}
	row := r.queryRow(ctx, `select count(*) from telegram_user_access where `+strings.Join(clauses, " and "), args...)
	var count int
	err := row.Scan(&count)
	return count, err
}

func (r *PostgresRepository) ListTelegramUserAccess(ctx context.Context, tenantID string, limit int) ([]domain.TelegramUserAccess, error) {
	page, err := r.ListTelegramUserAccessPage(ctx, domain.TelegramUserAccessListQuery{
		TenantID: tenantID,
		CursorPage: domain.CursorPage{
			Limit: limit,
		},
	})
	return page.Items, err
}

func (r *PostgresRepository) ListTelegramUserAccessByStatus(ctx context.Context, tenantID, status string, limit int) ([]domain.TelegramUserAccess, error) {
	page, err := r.ListTelegramUserAccessPage(ctx, domain.TelegramUserAccessListQuery{
		TenantID: tenantID,
		Status:   status,
		CursorPage: domain.CursorPage{
			Limit: limit,
		},
	})
	return page.Items, err
}

func (r *PostgresRepository) ListTelegramUserAccessPage(ctx context.Context, query domain.TelegramUserAccessListQuery) (domain.PagedResult[domain.TelegramUserAccess], error) {
	limit := normalizeLimit(query.Limit)
	clauses := []string{"tenant_id=$1"}
	args := []any{query.TenantID}
	orderField := "updated_at"
	if query.Status != "" {
		args = append(args, query.Status)
		clauses = append(clauses, fmt.Sprintf("status=$%d", len(args)))
		switch query.Status {
		case "pending":
			orderField = "coalesce(requested_at, created_at)"
		case "approved", "denied":
			orderField = "coalesce(decided_at, updated_at)"
		}
	}
	cursorTime, cursorID, hasCursor, err := parseCursor(query.After)
	if err != nil {
		return domain.PagedResult[domain.TelegramUserAccess]{}, err
	}
	if hasCursor {
		args = append(args, cursorTime, cursorID)
		clauses = append(clauses, fmt.Sprintf("(%s, telegram_user_id) < ($%d, $%d)", orderField, len(args)-1, len(args)))
	}
	args = append(args, limit+1)
	rows, err := r.query(ctx, `
		select tenant_id, telegram_user_id, display_name, allowed, status, added_by, coalesce(requested_at, created_at), decided_at, created_at, updated_at, `+orderField+` as sort_at
		from telegram_user_access
		where `+strings.Join(clauses, " and ")+`
		order by `+orderField+` desc, telegram_user_id asc
		limit $`+strconv.Itoa(len(args))+`
	`, args...)
	if err != nil {
		return domain.PagedResult[domain.TelegramUserAccess]{}, err
	}
	defer rows.Close()
	var out []domain.TelegramUserAccess
	var cursors []cursorValue
	for rows.Next() {
		var item domain.TelegramUserAccess
		var sortAt time.Time
		if err := rows.Scan(&item.TenantID, &item.TelegramUserID, &item.DisplayName, &item.Allowed, &item.Status, &item.AddedBy, &item.RequestedAt, &item.DecidedAt, &item.CreatedAt, &item.UpdatedAt, &sortAt); err != nil {
			return domain.PagedResult[domain.TelegramUserAccess]{}, err
		}
		out = append(out, item)
		cursors = append(cursors, cursorValue{Time: sortAt, ID: item.TelegramUserID})
	}
	if err := rows.Err(); err != nil {
		return domain.PagedResult[domain.TelegramUserAccess]{}, err
	}
	return finalizePage(out, cursors, limit), nil
}

func (r *PostgresRepository) GetTelegramUserAccess(ctx context.Context, tenantID, telegramUserID string) (domain.TelegramUserAccess, error) {
	row := r.queryRow(ctx, `
		select tenant_id, telegram_user_id, display_name, allowed, status, added_by, coalesce(requested_at, created_at), decided_at, created_at, updated_at
		from telegram_user_access
		where tenant_id=$1 and telegram_user_id=$2
	`, tenantID, telegramUserID)
	var item domain.TelegramUserAccess
	err := row.Scan(&item.TenantID, &item.TelegramUserID, &item.DisplayName, &item.Allowed, &item.Status, &item.AddedBy, &item.RequestedAt, &item.DecidedAt, &item.CreatedAt, &item.UpdatedAt)
	return item, err
}

func (r *PostgresRepository) UpsertTelegramUserAccess(ctx context.Context, entry domain.TelegramUserAccess) error {
	status := nonEmptyStatus(entry.Status)
	decidedAt := entry.DecidedAt
	if decidedAt == nil && (status == "approved" || status == "denied") {
		now := time.Now().UTC()
		decidedAt = &now
	}
	_, err := r.exec(ctx, `
		insert into telegram_user_access (tenant_id, telegram_user_id, display_name, allowed, status, added_by, requested_at, decided_at, created_at, updated_at)
		values ($1,$2,$3,$4,$5,$6,$7,$8,coalesce($9, now()),now())
		on conflict (tenant_id, telegram_user_id)
		do update set display_name=excluded.display_name, allowed=excluded.allowed, status=excluded.status, added_by=excluded.added_by, requested_at=coalesce(excluded.requested_at, telegram_user_access.requested_at), decided_at=coalesce(excluded.decided_at, telegram_user_access.decided_at), updated_at=now()
	`, entry.TenantID, entry.TelegramUserID, entry.DisplayName, entry.Allowed, status, entry.AddedBy, nullableTime(entry.RequestedAt), nullableTimePtr(decidedAt), nullableTime(entry.CreatedAt))
	return err
}

func (r *PostgresRepository) DeleteTelegramUserAccess(ctx context.Context, tenantID, telegramUserID string) error {
	_, err := r.exec(ctx, `delete from telegram_user_access where tenant_id=$1 and telegram_user_id=$2`, tenantID, telegramUserID)
	return err
}

func (r *PostgresRepository) RequestTelegramAccess(ctx context.Context, entry domain.TelegramUserAccess) (domain.TelegramUserAccess, error) {
	existing, err := r.GetTelegramUserAccess(ctx, entry.TenantID, entry.TelegramUserID)
	if err == nil {
		if existing.Status == "pending" {
			return existing, nil
		}
		entry, err = r.requestTelegramAccessWithExisting(entry, &existing)
		if err != nil {
			return domain.TelegramUserAccess{}, err
		}
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return domain.TelegramUserAccess{}, err
	}
	if errors.Is(err, pgx.ErrNoRows) {
		entry, err = r.requestTelegramAccessWithExisting(entry, nil)
		if err != nil {
			return domain.TelegramUserAccess{}, err
		}
	}
	if err := r.UpsertTelegramUserAccess(ctx, entry); err != nil {
		return domain.TelegramUserAccess{}, err
	}
	return entry, nil
}

func (r *PostgresRepository) requestTelegramAccessWithExisting(entry domain.TelegramUserAccess, existing *domain.TelegramUserAccess) (domain.TelegramUserAccess, error) {
	if existing != nil {
		switch existing.Status {
		case "pending":
			return *existing, nil
		case "approved", "denied":
			return domain.TelegramUserAccess{}, domain.ErrTelegramAccessRequestAlreadyFinal
		}
	}
	now := time.Now().UTC()
	entry.Status = "pending"
	entry.Allowed = false
	entry.RequestedAt = now
	entry.DecidedAt = nil
	entry.CreatedAt = now
	entry.UpdatedAt = now
	return entry, nil
}

func (r *PostgresRepository) ResolveTelegramAccessRequest(ctx context.Context, tenantID, telegramUserID, status, addedBy string) (domain.TelegramUserAccess, error) {
	existing, err := r.GetTelegramUserAccess(ctx, tenantID, telegramUserID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.TelegramUserAccess{}, domain.ErrTelegramAccessRequestNotFound
		}
		return domain.TelegramUserAccess{}, err
	}
	if err := validateTelegramAccessResolution(existing); err != nil {
		return domain.TelegramUserAccess{}, err
	}
	allowed := status == "approved"
	_, err = r.exec(ctx, `
		update telegram_user_access
		set allowed=$3, status=$4, added_by=$5, decided_at=now(), updated_at=now()
		where tenant_id=$1 and telegram_user_id=$2
	`, tenantID, telegramUserID, allowed, status, addedBy)
	if err != nil {
		return domain.TelegramUserAccess{}, err
	}
	row := r.queryRow(ctx, `
		select tenant_id, telegram_user_id, display_name, allowed, status, added_by, coalesce(requested_at, created_at), decided_at, created_at, updated_at
		from telegram_user_access where tenant_id=$1 and telegram_user_id=$2
	`, tenantID, telegramUserID)
	var item domain.TelegramUserAccess
	if err := row.Scan(&item.TenantID, &item.TelegramUserID, &item.DisplayName, &item.Allowed, &item.Status, &item.AddedBy, &item.RequestedAt, &item.DecidedAt, &item.CreatedAt, &item.UpdatedAt); err != nil {
		return domain.TelegramUserAccess{}, err
	}
	return item, nil
}

func validateTelegramAccessResolution(existing domain.TelegramUserAccess) error {
	if existing.Status != "pending" {
		return domain.ErrTelegramAccessRequestNotPending
	}
	return nil
}

func notificationSessionID(tenantID, channelType, surfaceKey, ownerUserID string) string {
	sum := sha1.Sum([]byte(strings.Join([]string{tenantID, channelType, surfaceKey, ownerUserID}, "|")))
	return fmt.Sprintf("session_notice_%s_%s_%s", channelType, ownerUserID, hex.EncodeToString(sum[:6]))
}

func (r *PostgresRepository) Audit(ctx context.Context, event domain.AuditEvent) error {
	_, err := r.exec(ctx, `
		insert into audit_events (
			id, tenant_id, session_id, run_id, await_id, aggregate_type, aggregate_id, event_type, payload_json, created_at
		) values ($1,$2,nullif($3,''),nullif($4,''),nullif($5,''),$6,$7,$8,$9,$10)
	`, event.ID, event.TenantID, event.SessionID, event.RunID, event.AwaitID, event.AggregateType, event.AggregateID, event.EventType, event.PayloadJSON, nonZeroTime(event.CreatedAt))
	return err
}

func (r *PostgresRepository) upsertSurfaceState(ctx context.Context, tenantID, channelType, surfaceKey, sessionID string) error {
	_, err := r.exec(ctx, `
		insert into channel_surface_state (id, tenant_id, channel_type, surface_key, active_session_id, created_at, updated_at)
		values ($1,$2,$3,$4,nullif($5,''),now(),now())
		on conflict (tenant_id, channel_type, surface_key)
		do update set active_session_id = nullif(excluded.active_session_id,''), updated_at=now()
	`, fmt.Sprintf("surface_%s_%s", channelType, surfaceKey), tenantID, channelType, surfaceKey, sessionID)
	return err
}

func (r *PostgresRepository) upsertSessionAlias(ctx context.Context, tenantID, channelType, surfaceKey, sessionID, alias string) error {
	_, err := r.exec(ctx, `
		insert into session_aliases (id, tenant_id, session_id, channel_type, surface_key, alias, created_at)
		values ($1,$2,$3,$4,$5,$6,now())
		on conflict (tenant_id, channel_type, surface_key, alias)
		do update set session_id=excluded.session_id
	`, fmt.Sprintf("alias_%s_%s", sessionID, alias), tenantID, sessionID, channelType, surfaceKey, alias)
	return err
}

func (r *PostgresRepository) WithRetentionLock(ctx context.Context, fn func(ctx context.Context, repo ports.RetentionRepository) error) error {
	if r.conn != nil {
		var locked bool
		if err := r.queryRow(ctx, `select pg_try_advisory_lock($1)`, int64(8745123901)).Scan(&locked); err != nil {
			return err
		}
		if !locked {
			return domain.ErrRetentionLockBusy
		}
		defer func() {
			_ = r.queryRow(context.Background(), `select pg_advisory_unlock($1)`, int64(8745123901)).Scan(&locked)
		}()
		return fn(ctx, r)
	}
	conn, err := r.pool.Acquire(ctx)
	if err != nil {
		return err
	}
	repo := &PostgresRepository{pool: r.pool, conn: conn}
	var locked bool
	if err := repo.queryRow(ctx, `select pg_try_advisory_lock($1)`, int64(8745123901)).Scan(&locked); err != nil {
		conn.Release()
		return err
	}
	if !locked {
		conn.Release()
		return domain.ErrRetentionLockBusy
	}
	defer func() {
		_ = repo.queryRow(context.Background(), `select pg_advisory_unlock($1)`, int64(8745123901)).Scan(&locked)
		conn.Release()
	}()
	return fn(ctx, repo)
}

func (r *PostgresRepository) ListRetentionTenantIDs(ctx context.Context) ([]string, error) {
	rows, err := r.query(ctx, `
		with tenant_ids as (
			select tenant_id from sessions
			union
			select tenant_id from inbound_receipts
			union
			select tenant_id from messages
			union
			select tenant_id from outbound_deliveries
			union
			select tenant_id from outbox_events
			union
			select tenant_id from audit_events
			union
			select tenant_id from telegram_user_access
			union
			select tenant_id from retention_policies
		)
		select tenant_id from tenant_ids where tenant_id <> '' order by tenant_id
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var tenantID string
		if err := rows.Scan(&tenantID); err != nil {
			return nil, err
		}
		out = append(out, tenantID)
	}
	return out, rows.Err()
}

func (r *PostgresRepository) GetRetentionPolicy(ctx context.Context, tenantID string) (domain.RetentionPolicy, error) {
	row := r.queryRow(ctx, `
		select tenant_id, enabled, payload_days, artifact_days, audit_days, relational_grace_days, updated_at
		from retention_policies
		where tenant_id=$1
	`, tenantID)
	var (
		policy   domain.RetentionPolicy
		enabled  *bool
		payload  *int
		artifact *int
		audit    *int
		grace    *int
	)
	err := row.Scan(&policy.TenantID, &enabled, &payload, &artifact, &audit, &grace, &policy.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.RetentionPolicy{TenantID: tenantID}, nil
	}
	if err != nil {
		return domain.RetentionPolicy{}, err
	}
	policy.Enabled = enabled
	policy.PayloadDays = payload
	policy.ArtifactDays = artifact
	policy.AuditDays = audit
	policy.RelationalGraceDays = grace
	return policy, nil
}

func (r *PostgresRepository) UpsertRetentionPolicy(ctx context.Context, policy domain.RetentionPolicy) error {
	_, err := r.exec(ctx, `
		insert into retention_policies (
			tenant_id, enabled, payload_days, artifact_days, audit_days, relational_grace_days, updated_at
		) values ($1,$2,$3,$4,$5,$6,now())
		on conflict (tenant_id) do update set
			enabled=excluded.enabled,
			payload_days=excluded.payload_days,
			artifact_days=excluded.artifact_days,
			audit_days=excluded.audit_days,
			relational_grace_days=excluded.relational_grace_days,
			updated_at=now()
	`, policy.TenantID, policy.Enabled, policy.PayloadDays, policy.ArtifactDays, policy.AuditDays, policy.RelationalGraceDays)
	return err
}

func (r *PostgresRepository) DeleteRetentionPolicy(ctx context.Context, tenantID string) error {
	_, err := r.exec(ctx, `delete from retention_policies where tenant_id=$1`, tenantID)
	return err
}

func (r *PostgresRepository) CountRetentionCandidates(ctx context.Context, tenantID string, cutoffs domain.RetentionCutoffs) (domain.RetentionCounts, error) {
	var counts domain.RetentionCounts
	if err := r.queryRow(ctx, `select count(*) from messages where tenant_id=$1 and created_at < $2 and payload_purged_at is null`, tenantID, cutoffs.PayloadBefore).Scan(&counts.MessagePayloads); err != nil {
		return counts, err
	}
	if err := r.queryRow(ctx, `select count(*) from outbound_deliveries where tenant_id=$1 and created_at < $2 and payload_purged_at is null`, tenantID, cutoffs.PayloadBefore).Scan(&counts.DeliveryPayloads); err != nil {
		return counts, err
	}
	if err := r.queryRow(ctx, `select count(*) from outbox_events where tenant_id=$1 and available_at < $2 and payload_purged_at is null`, tenantID, cutoffs.PayloadBefore).Scan(&counts.OutboxPayloads); err != nil {
		return counts, err
	}
	if err := r.queryRow(ctx, `
		select count(*) from awaits a
		join sessions s on s.id = a.session_id
		where s.tenant_id=$1 and a.expires_at < $2 and a.payload_purged_at is null
	`, tenantID, cutoffs.PayloadBefore).Scan(&counts.AwaitPayloads); err != nil {
		return counts, err
	}
	if err := r.queryRow(ctx, `
		select count(*) from await_responses ar
		join awaits a on a.id = ar.await_id
		join sessions s on s.id = a.session_id
		where s.tenant_id=$1 and ar.accepted_at < $2 and ar.payload_purged_at is null
	`, tenantID, cutoffs.PayloadBefore).Scan(&counts.AwaitResponsePayloads); err != nil {
		return counts, err
	}
	if err := r.queryRow(ctx, `
		select count(*) from artifacts ar
		join messages m on m.id = ar.message_id
		where m.tenant_id=$1 and ar.created_at < $2 and ar.blob_purged_at is null and coalesce(ar.storage_uri,'') <> ''
	`, tenantID, cutoffs.ArtifactBefore).Scan(&counts.ArtifactBlobs); err != nil {
		return counts, err
	}
	if err := r.queryRow(ctx, `select count(*) from audit_events where tenant_id=$1 and created_at < $2`, tenantID, cutoffs.AuditBefore).Scan(&counts.AuditRows); err != nil {
		return counts, err
	}
	sessionIDs, err := r.retentionCandidateSessionIDs(ctx, tenantID, cutoffs.RelationalBefore, 1000000)
	if err != nil {
		return counts, err
	}
	counts.Sessions = len(sessionIDs)
	if len(sessionIDs) == 0 {
		return counts, nil
	}
	if counts.HistoryRows, err = r.retentionHistoryRowCount(ctx, sessionIDs); err != nil {
		return counts, err
	}
	return counts, nil
}

func (r *PostgresRepository) ApplyRetention(ctx context.Context, tenantID string, cutoffs domain.RetentionCutoffs, batchSize int, deleteBlob func(context.Context, string) error) (domain.RetentionCounts, error) {
	var counts domain.RetentionCounts
	var err error
	if counts.MessagePayloads, err = r.redactMessagePayloads(ctx, tenantID, cutoffs.PayloadBefore, batchSize); err != nil {
		return counts, err
	}
	if counts.DeliveryPayloads, err = r.redactDeliveryPayloads(ctx, tenantID, cutoffs.PayloadBefore, batchSize); err != nil {
		return counts, err
	}
	if counts.OutboxPayloads, err = r.redactOutboxPayloads(ctx, tenantID, cutoffs.PayloadBefore, batchSize); err != nil {
		return counts, err
	}
	if counts.AwaitPayloads, err = r.redactAwaitPayloads(ctx, tenantID, cutoffs.PayloadBefore, batchSize); err != nil {
		return counts, err
	}
	if counts.AwaitResponsePayloads, err = r.redactAwaitResponsePayloads(ctx, tenantID, cutoffs.PayloadBefore, batchSize); err != nil {
		return counts, err
	}
	if counts.ArtifactBlobs, err = r.purgeArtifactBlobs(ctx, tenantID, cutoffs.ArtifactBefore, batchSize, deleteBlob); err != nil {
		return counts, err
	}
	if counts.AuditRows, err = r.deleteAuditRows(ctx, tenantID, cutoffs.AuditBefore, batchSize); err != nil {
		return counts, err
	}
	sessionIDs, err := r.retentionCandidateSessionIDs(ctx, tenantID, cutoffs.RelationalBefore, batchSize)
	if err != nil {
		return counts, err
	}
	counts.Sessions = len(sessionIDs)
	if len(sessionIDs) == 0 {
		return counts, nil
	}
	if counts.HistoryRows, err = r.deleteRetentionSessions(ctx, sessionIDs); err != nil {
		return counts, err
	}
	return counts, nil
}

func (r *PostgresRepository) redactMessagePayloads(ctx context.Context, tenantID string, before time.Time, limit int) (int, error) {
	return r.redactRetentionCTE(ctx, `
		with target as (
			select id from messages
			where tenant_id=$1 and created_at < $2 and payload_purged_at is null
			order by created_at, id
			limit $3
		)
		update messages m
		set raw_payload_json='{}'::jsonb, payload_purged_at=now()
		from target
		where m.id = target.id
	`, tenantID, before, limit)
}

func (r *PostgresRepository) redactDeliveryPayloads(ctx context.Context, tenantID string, before time.Time, limit int) (int, error) {
	return r.redactRetentionCTE(ctx, `
		with target as (
			select id from outbound_deliveries
			where tenant_id=$1 and created_at < $2 and payload_purged_at is null
			order by created_at, id
			limit $3
		)
		update outbound_deliveries d
		set payload_json='{}'::jsonb, payload_purged_at=now()
		from target
		where d.id = target.id
	`, tenantID, before, limit)
}

func (r *PostgresRepository) redactOutboxPayloads(ctx context.Context, tenantID string, before time.Time, limit int) (int, error) {
	return r.redactRetentionCTE(ctx, `
		with target as (
			select id from outbox_events
			where tenant_id=$1 and available_at < $2 and payload_purged_at is null
			order by available_at, id
			limit $3
		)
		update outbox_events o
		set payload_json='{}'::jsonb, payload_purged_at=now()
		from target
		where o.id = target.id
	`, tenantID, before, limit)
}

func (r *PostgresRepository) redactAwaitPayloads(ctx context.Context, tenantID string, before time.Time, limit int) (int, error) {
	return r.redactRetentionCTE(ctx, `
		with target as (
			select a.id from awaits a
			join sessions s on s.id = a.session_id
			where s.tenant_id=$1 and a.expires_at < $2 and a.payload_purged_at is null
			order by a.expires_at, a.id
			limit $3
		)
		update awaits a
		set schema_json='{}'::jsonb,
		    prompt_render_model_json='{}'::jsonb,
		    allowed_responder_ids_json='[]'::jsonb,
		    payload_purged_at=now()
		from target
		where a.id = target.id
	`, tenantID, before, limit)
}

func (r *PostgresRepository) redactAwaitResponsePayloads(ctx context.Context, tenantID string, before time.Time, limit int) (int, error) {
	return r.redactRetentionCTE(ctx, `
		with target as (
			select ar.id from await_responses ar
			join awaits a on a.id = ar.await_id
			join sessions s on s.id = a.session_id
			where s.tenant_id=$1 and ar.accepted_at < $2 and ar.payload_purged_at is null
			order by ar.accepted_at, ar.id
			limit $3
		)
		update await_responses ar
		set response_payload_json='{}'::jsonb, payload_purged_at=now()
		from target
		where ar.id = target.id
	`, tenantID, before, limit)
}

func (r *PostgresRepository) purgeArtifactBlobs(ctx context.Context, tenantID string, before time.Time, limit int, deleteBlob func(context.Context, string) error) (int, error) {
	rows, err := r.query(ctx, `
		select ar.id, ar.storage_uri
		from artifacts ar
		join messages m on m.id = ar.message_id
		where m.tenant_id=$1 and ar.created_at < $2 and ar.blob_purged_at is null and coalesce(ar.storage_uri,'') <> ''
		order by ar.created_at, ar.id
		limit $3
	`, tenantID, before, limit)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	type artifactBlob struct {
		ID         string
		StorageURI string
	}
	var artifacts []artifactBlob
	for rows.Next() {
		var item artifactBlob
		if err := rows.Scan(&item.ID, &item.StorageURI); err != nil {
			return 0, err
		}
		artifacts = append(artifacts, item)
	}
	if err := rows.Err(); err != nil {
		return 0, err
	}
	count := 0
	for _, item := range artifacts {
		if deleteBlob != nil {
			if err := deleteBlob(ctx, item.StorageURI); err != nil {
				return count, err
			}
		}
		if _, err := r.exec(ctx, `update artifacts set storage_uri=null, blob_purged_at=now() where id=$1 and blob_purged_at is null`, item.ID); err != nil {
			return count, err
		}
		count++
	}
	return count, nil
}

func (r *PostgresRepository) deleteAuditRows(ctx context.Context, tenantID string, before time.Time, limit int) (int, error) {
	tag, err := r.exec(ctx, `
		with target as (
			select id from audit_events
			where tenant_id=$1 and created_at < $2
			order by created_at, id
			limit $3
		)
		delete from audit_events a
		using target
		where a.id = target.id
	`, tenantID, before, limit)
	return int(tag.RowsAffected()), err
}

func (r *PostgresRepository) retentionCandidateSessionIDs(ctx context.Context, tenantID string, before time.Time, limit int) ([]string, error) {
	rows, err := r.query(ctx, `
		select s.id
		from sessions s
		where s.tenant_id=$1
		  and s.last_active_at < $2
		  and not exists (
			select 1 from session_queue_items qi
			where qi.session_id = s.id and qi.status not in ('completed','failed','canceled','expired')
		  )
		  and not exists (
			select 1 from runs r
			where r.session_id = s.id and r.status not in ('completed','failed','canceled','expired')
		  )
		  and not exists (
			select 1 from awaits a
			where a.session_id = s.id and a.status not in ('resolved','expired')
		  )
		  and not exists (
			select 1 from outbound_deliveries d
			where d.session_id = s.id and d.status not in ('sent','failed','canceled')
		  )
		order by s.last_active_at, s.id
		limit $3
	`, tenantID, before, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

func (r *PostgresRepository) retentionHistoryRowCount(ctx context.Context, sessionIDs []string) (int, error) {
	var count int
	for _, query := range []string{
		`select count(*) from await_responses where await_id in (select id from awaits where session_id = any($1))`,
		`select count(*) from awaits where session_id = any($1)`,
		`select count(*) from outbound_deliveries where session_id = any($1)`,
		`select count(*) from artifacts where message_id in (select id from messages where session_id = any($1))`,
		`select count(*) from runs where session_id = any($1)`,
		`select count(*) from session_queue_items where session_id = any($1)`,
		`select count(*) from messages where session_id = any($1)`,
		`select count(*) from session_aliases where session_id = any($1)`,
	} {
		var n int
		if err := r.queryRow(ctx, query, sessionIDs).Scan(&n); err != nil {
			return 0, err
		}
		count += n
	}
	return count, nil
}

func (r *PostgresRepository) deleteRetentionSessions(ctx context.Context, sessionIDs []string) (int, error) {
	count, err := r.retentionHistoryRowCount(ctx, sessionIDs)
	if err != nil {
		return 0, err
	}
	for _, stmt := range []string{
		`delete from await_responses where await_id in (select id from awaits where session_id = any($1))`,
		`delete from awaits where session_id = any($1)`,
		`delete from outbound_deliveries where session_id = any($1)`,
		`delete from artifacts where message_id in (select id from messages where session_id = any($1))`,
		`delete from runs where session_id = any($1)`,
		`delete from session_queue_items where session_id = any($1)`,
		`delete from messages where session_id = any($1)`,
		`delete from session_aliases where session_id = any($1)`,
		`update channel_surface_state set active_session_id = null, updated_at=now() where active_session_id = any($1)`,
		`delete from sessions where id = any($1)`,
	} {
		if _, err := r.exec(ctx, stmt, sessionIDs); err != nil {
			return 0, err
		}
	}
	return count, nil
}

func (r *PostgresRepository) redactRetentionCTE(ctx context.Context, sql string, args ...any) (int, error) {
	tag, err := r.exec(ctx, sql, args...)
	return int(tag.RowsAffected()), err
}

func contains(items []string, want string) bool {
	for _, item := range items {
		if item == want {
			return true
		}
	}
	return false
}

type cursorValue struct {
	Time time.Time
	ID   string
}

func normalizeLimit(limit int) int {
	if limit <= 0 {
		return 50
	}
	if limit > 200 {
		return 200
	}
	return limit
}

func parseCursor(cursor string) (time.Time, string, bool, error) {
	if cursor == "" {
		return time.Time{}, "", false, nil
	}
	parts := strings.SplitN(cursor, "|", 2)
	if len(parts) != 2 {
		return time.Time{}, "", false, fmt.Errorf("invalid cursor")
	}
	t, err := time.Parse(time.RFC3339Nano, parts[0])
	if err != nil {
		return time.Time{}, "", false, fmt.Errorf("invalid cursor time: %w", err)
	}
	return t, parts[1], true, nil
}

func formatCursor(ts time.Time, id string) string {
	return ts.UTC().Format(time.RFC3339Nano) + "|" + id
}

func finalizePage[T any](items []T, cursors []cursorValue, limit int) domain.PagedResult[T] {
	result := domain.PagedResult[T]{Items: items}
	if len(items) <= limit {
		return result
	}
	result.Items = items[:limit]
	last := cursors[limit-1]
	result.NextCursor = formatCursor(last.Time, last.ID)
	return result
}

func nonZeroTime(ts time.Time) time.Time {
	if ts.IsZero() {
		return time.Now().UTC()
	}
	return ts
}

func keyForSurface(evt domain.CanonicalInboundEvent) string {
	return evt.Conversation.ChannelSurfaceKey
}

func nullableTime(ts time.Time) any {
	if ts.IsZero() {
		return nil
	}
	return ts
}

func nullableTimePtr(ts *time.Time) any {
	if ts == nil || ts.IsZero() {
		return nil
	}
	return *ts
}

func nullableTimeValue(ts time.Time) any {
	if ts.IsZero() {
		return nil
	}
	return ts
}

func scanUser(row pgx.Row) (domain.User, error) {
	var user domain.User
	if err := row.Scan(&user.ID, &user.TenantID, &user.PrimaryEmail, &user.PrimaryEmailVerified, &user.PrimaryPhone, &user.PrimaryPhoneNormalized, &user.PrimaryPhoneVerified, &user.PrimaryPhoneAddedAt, &user.LastStepUpAt, &user.CreatedAt, &user.UpdatedAt); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.User{}, domain.ErrIdentityUserNotFound
		}
		return domain.User{}, err
	}
	return user, nil
}

func hashText(input string) string {
	sum := sha1.Sum([]byte(input))
	return hex.EncodeToString(sum[:8])
}

func nonEmptyStatus(status string) string {
	if status == "" {
		return "approved"
	}
	return status
}

func (r *PostgresRepository) exec(ctx context.Context, sql string, args ...any) (pgconnCommandTag, error) {
	if r.tx != nil {
		tag, err := r.tx.Exec(ctx, sql, args...)
		return pgconnCommandTag(tag), err
	}
	if r.conn != nil {
		tag, err := r.conn.Exec(ctx, sql, args...)
		return pgconnCommandTag(tag), err
	}
	tag, err := r.pool.Exec(ctx, sql, args...)
	return pgconnCommandTag(tag), err
}

func (r *PostgresRepository) query(ctx context.Context, sql string, args ...any) (pgx.Rows, error) {
	if r.tx != nil {
		return r.tx.Query(ctx, sql, args...)
	}
	if r.conn != nil {
		return r.conn.Query(ctx, sql, args...)
	}
	return r.pool.Query(ctx, sql, args...)
}

func (r *PostgresRepository) queryRow(ctx context.Context, sql string, args ...any) pgx.Row {
	if r.tx != nil {
		return r.tx.QueryRow(ctx, sql, args...)
	}
	if r.conn != nil {
		return r.conn.QueryRow(ctx, sql, args...)
	}
	return r.pool.QueryRow(ctx, sql, args...)
}

type pgconnCommandTag pgconn.CommandTag

func (t pgconnCommandTag) RowsAffected() int64 {
	return pgconn.CommandTag(t).RowsAffected()
}
