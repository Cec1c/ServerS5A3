package guild

import (
	"context"
	"sort"
	"strings"
	"time"

	r "longheng.io/server/internal/modules/dnf/repository"
)

// DirectoryReadView is a bounded version for routing and display only. A later
// delivery, admission or post-commit refresh must acquire a new exact version.
type DirectoryReadView struct{ hall *HallReadView }

type DirectoryMember struct {
	GuildID     uint32
	CharacterID string
	AccountID   string
	JoinedAt    time.Time
	Grade       byte
}

func ReadDirectoryView(ctx context.Context, settings r.SettingsRepository) (*DirectoryReadView, error) {
	hall, err := ReadHallView(ctx, settings)
	if err != nil {
		return nil, err
	}
	return &DirectoryReadView{hall: hall}, nil
}

func (view *DirectoryReadView) Member(characterID string) (DirectoryMember, bool) {
	if view == nil || view.hall == nil || view.hall.snapshot == nil {
		return DirectoryMember{}, false
	}
	characterID = strings.TrimSpace(characterID)
	id, found := view.hall.snapshot.owners[characterID]
	if !found {
		return DirectoryMember{}, false
	}
	g := view.hall.snapshot.registry.Guilds[id]
	m, found := g.Members[characterID]
	if !found {
		return DirectoryMember{}, false
	}
	return DirectoryMember{GuildID: id, CharacterID: m.CharacterID, AccountID: m.AccountID, JoinedAt: m.JoinedAt, Grade: m.Grade}, true
}

func (view *DirectoryReadView) OwnedMember(owner r.GuildOwner) (DirectoryMember, bool) {
	m, found := view.Member(owner.CharacterID)
	return m, found && owner.AccountID != "" && owner.AccountID == m.AccountID
}

// Members copies only the values needed for fanout. It exposes no progression
// or receipt pointers and preserves the existing grade/character sort order.
func (view *DirectoryReadView) Members(guildID uint32) []DirectoryMember {
	if view == nil || view.hall == nil || view.hall.snapshot == nil {
		return nil
	}
	g := view.hall.snapshot.registry.Guilds[guildID]
	if g == nil {
		return nil
	}
	result := make([]DirectoryMember, 0, len(g.Members))
	for _, member := range g.Members {
		result = append(result, DirectoryMember{GuildID: g.ID, CharacterID: member.CharacterID, AccountID: member.AccountID, JoinedAt: member.JoinedAt, Grade: member.Grade})
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].Grade != result[j].Grade {
			return result[i].Grade < result[j].Grade
		}
		return result[i].CharacterID < result[j].CharacterID
	})
	return result
}

// OwnedRoster retains the detached management packet projection contract. It
// omits storage and histories and must never be saved as authoritative state.
func (view *DirectoryReadView) OwnedRoster(owner r.GuildOwner) (*Guild, error) {
	if view == nil || view.hall == nil || view.hall.snapshot == nil {
		return nil, nil
	}
	owner.CharacterID = strings.TrimSpace(owner.CharacterID)
	if owner.AccountID == "" {
		return nil, nil
	}
	membership := &GuildReadMembership{snapshot: view.hall.snapshot, characterID: owner.CharacterID}
	return membership.RosterProjection(owner)
}
