package services

import (
	"context"

	"nexus/internal/domain"
	"nexus/internal/ports"
	"nexus/internal/tracex"
)

type AwaitService struct {
	Repo ports.Repository
}

func (s AwaitService) HandleResponse(ctx context.Context, evt domain.CanonicalInboundEvent) (err error) {
	ctx, end := tracex.StartSpan(ctx, "await.handle_response",
		"event_id", evt.EventID,
		"await_id", evt.Metadata.AwaitID,
		"tenant_id", evt.TenantID,
	)
	defer func() { end(err) }()
	err = s.Repo.InTx(ctx, func(ctx context.Context, repo ports.Repository) error {
		await, err := repo.ResolveAwait(ctx, evt.Metadata.AwaitID, evt.Sender.ChannelUserID, evt.Metadata.ResumePayload)
		if err != nil {
			tracex.Logger(ctx).Error("await.resolve_failed", "await_id", evt.Metadata.AwaitID, "error", err.Error())
			return err
		}
		tracex.Logger(ctx).Info("await.resolved", "await_id", await.ID, "actor_id", evt.Sender.ChannelUserID)
		return repo.EnqueueAwaitResume(ctx, domain.ResumeRequest{
			AwaitID: await.ID,
			Payload: evt.Metadata.ResumePayload,
		}, evt.TenantID)
	})
	return err
}
