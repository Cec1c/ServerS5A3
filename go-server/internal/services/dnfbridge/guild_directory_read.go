package dnfbridge

import (
	"context"

	"longheng.io/server/internal/modules/dnf/guild"
	r "longheng.io/server/internal/modules/dnf/repository"
)

func (s *Service) readCurrentGuildDirectory(ctx context.Context) (r.Group, *guild.DirectoryReadView, error) {
	repositories, ok := s.repositoryGroup()
	if !ok || repositories.Settings == nil || repositories.Character == nil {
		return r.Group{}, nil, guild.ErrRepositoryUnavailable
	}
	started := s.performanceStart()
	view, err := guild.ReadDirectoryView(ctx, repositories.Settings)
	s.performanceEnd(perfGuildRegistryRead, started, err)
	return repositories, view, err
}
