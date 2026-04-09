package acp

import "nexus/internal/domain"

func staticRunEventStream(events ...domain.RunEvent) domain.RunEventStream {
	return domain.StaticRunEventStream(events...)
}
