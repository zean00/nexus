package services

import (
	"context"

	"nexus/internal/domain"
	"nexus/internal/ports"
)

type AwaitService struct {
	Repo ports.Repository
}

func (s AwaitService) HandleResponse(ctx context.Context, evt domain.CanonicalInboundEvent) error {
	return s.Repo.InTx(ctx, func(ctx context.Context, repo ports.Repository) error {
		await, err := repo.ResolveAwait(ctx, evt.Metadata.AwaitID, evt.Sender.ChannelUserID, evt.Metadata.ResumePayload)
		if err != nil {
			return err
		}
		return repo.EnqueueAwaitResume(ctx, domain.ResumeRequest{
			AwaitID: await.ID,
			Payload: evt.Metadata.ResumePayload,
		}, evt.TenantID)
	})
}
