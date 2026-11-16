package guild

import (
	"slices"
	"strings"

	r "longheng.io/server/internal/modules/dnf/repository"
)

func (view *DirectoryReadView) source() *Registry {
	if view == nil || view.hall == nil || view.hall.snapshot == nil {
		return nil
	}
	return &view.hall.snapshot.registry
}

func (view *DirectoryReadView) NameAvailable(name string) bool {
	reg := view.source()
	return reg != nil && reg.NameAvailable(name)
}

func (view *DirectoryReadView) RosterByName(name string) (*Guild, bool) {
	reg := view.source()
	if reg == nil {
		return nil, false
	}
	g, found := reg.GuildByName(name)
	if !found {
		return nil, false
	}
	return detachedRosterProjection(g), true
}

func detachedDirectoryRows(rows []*Guild) []*Guild {
	if rows == nil {
		return nil
	}
	result := make([]*Guild, len(rows))
	for i, g := range rows {
		result[i] = detachedRosterProjection(g)
	}
	return result
}

func (view *DirectoryReadView) SearchRosters(query string, page, pageSize int) []*Guild {
	reg := view.source()
	if reg == nil {
		return nil
	}
	return detachedDirectoryRows(reg.Search(query, page, pageSize))
}

func (view *DirectoryReadView) SearchDirectoryRosters(filter SearchFilter, page, pageSize int) (int, []*Guild, error) {
	reg := view.source()
	if reg == nil {
		return 0, nil, ErrRepositoryUnavailable
	}
	total, rows, err := reg.SearchDirectory(filter, page, pageSize)
	if err != nil {
		return 0, nil, err
	}
	return total, detachedDirectoryRows(rows), nil
}

func (view *DirectoryReadView) ApplicationRosters(owner r.GuildOwner) []*Guild {
	reg := view.source()
	if reg == nil || owner.AccountID == "" {
		return nil
	}
	owner.CharacterID = strings.TrimSpace(owner.CharacterID)
	var result []*Guild
	for _, g := range reg.Search("", 0, len(reg.Guilds)) {
		if app, found := g.Applications[owner.CharacterID]; found && app.AccountID == owner.AccountID {
			result = append(result, detachedRosterProjection(g))
		}
	}
	return result
}

func (view *DirectoryReadView) Applications(owner r.GuildOwner) ([]Application, error) {
	if view == nil || view.hall == nil {
		return nil, ErrNotMember
	}
	g := view.hall.owned(owner)
	if g == nil {
		return nil, ErrNotMember
	}
	if g.Members[strings.TrimSpace(owner.CharacterID)].Grade > 2 {
		return nil, ErrPermissionDenied
	}
	var result []Application
	for _, app := range SortedApplications(g) {
		if _, joined := view.Member(app.CharacterID); !joined {
			result = append(result, app)
		}
	}
	return result, nil
}

func (view *DirectoryReadView) MileageHistory(owner r.GuildOwner) ([]MileageHistoryEntry, bool) {
	if view == nil || view.hall == nil {
		return nil, false
	}
	g := view.hall.owned(owner)
	if g == nil {
		return nil, false
	}
	return slices.Clone(g.MileageHistory), true
}

func (view *DirectoryReadView) CargoMembers(guildID uint32) []DirectoryMember {
	reg := view.source()
	if reg == nil || reg.Guilds[guildID] == nil {
		return nil
	}
	g := reg.Guilds[guildID]
	members := view.Members(guildID)
	result := members[:0]
	for _, member := range members {
		if GradePermissionsFor(g, member.Grade)&CargoPermission != 0 {
			result = append(result, member)
		}
	}
	return result
}
