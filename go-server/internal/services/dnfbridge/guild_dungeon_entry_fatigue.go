package dnfbridge

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strconv"

	dnfdungeon "longheng.io/server/internal/modules/dnf/dungeon"
	dnfguild "longheng.io/server/internal/modules/dnf/guild"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/worldmap"
)

type guildDungeonEntryFatigueMember struct {
	session *gameSession
	runtime *runtimeDungeonState
	scene   worldmap.DungeonRoomScene
}

func (s *Service) commitCurrentGuildDungeonEntryFatigue(ctx context.Context, leader *gameSession, runtime *runtimeDungeonState, scene worldmap.DungeonRoomScene, followers []*runtimePartyPreparedDungeonEntry) error {
	if leader == nil || runtime == nil {
		return errDungeonCharacterRequired
	}
	cost, startOnly, err := currentDungeonStartFatigueRule(runtime.Dungeon)
	if err != nil || !startOnly {
		return err
	}
	members, err := guildDungeonEntryFatigueMembers(leader, runtime, scene, followers)
	if err != nil {
		return err
	}
	if len(members) > 4 {
		return dnfguild.ErrPermissionDenied
	}
	repos, ok := s.repositoryGroup()
	if !ok || repos.Account == nil {
		return dnfguild.ErrRepositoryUnavailable
	}
	q := dnfguild.DungeonEntryFatigueQuery{Cost: cost, Accounts: map[string]dnfrepo.AccountRecord{}}
	if currentDungeonUsesNoFatigue(runtime.Dungeon) {
		q.Cost = 0
	}
	identities := make([]boundGameSessionCharacter, 0, len(members))
	for _, member := range members {
		clear := member.runtime.guildDungeonClear
		identity, bound := s.boundGameSessionCharacterSnapshot(member.session)
		if !bound || clear == nil || clear.Catalog == nil {
			return dnfguild.ErrPermissionDenied
		}
		owner := clear.Entry.Owner
		if owner.AccountID != s.accountIDForSession(member.session) || owner.AccountID != member.runtime.AccountID || owner.CharacterID != strconv.Itoa(int(identity.character)) || member.runtime.Character.CharacterID != owner.CharacterID || member.runtime.Character.AccountID != owner.AccountID || clear.Entry.DungeonID != uint32(member.runtime.Dungeon.ID) {
			return dnfguild.ErrPermissionDenied
		}
		identities = append(identities, identity)
		q.Entries = append(q.Entries, clear.Entry)
		if q.Catalog == nil {
			q.Catalog = clear.Catalog
		}
		account, exists, err := repos.Account.Load(ctx, owner.AccountID)
		if err != nil {
			return err
		}
		if !exists || account.AccountID != owner.AccountID {
			return errDungeonAccountMismatch
		}
		q.Accounts[owner.AccountID] = account
	}
	release, err := s.reserveCurrentGuildParticipants(ctx, identities)
	if err != nil {
		return err
	}
	defer release()
	// Fix each current dungeon while checking and charging the prepared roster.
	// No event-loop calls or packet writes occur under these locks.
	locked := append([]boundGameSessionCharacter(nil), identities...)
	sort.Slice(locked, func(i, j int) bool { return locked[i].character < locked[j].character })
	for _, identity := range locked {
		identity.session.dungeon.mu.Lock()
	}
	defer func() {
		for i := len(locked) - 1; i >= 0; i-- {
			locked[i].session.dungeon.mu.Unlock()
		}
	}()
	for _, member := range members {
		active := member.session.dungeon.runtime
		if active != nil && active != member.runtime && active.Session != nil && active.Session.Snapshot().Run.Status == worldmap.DungeonRunActive {
			return errDungeonAlreadyActive
		}
	}
	q.Revalidate = func() bool {
		for _, identity := range identities {
			if !s.boundGameSessionCharacterCurrent(identity) {
				return false
			}
		}
		return true
	}
	manager := s.runtimePartyManagerForService()
	snapshot, inParty := manager.SnapshotByUser(identities[0].character, identities[0].generation)
	if inParty {
		if snapshot.Leader != identities[0].character || len(snapshot.Members) != len(identities) {
			return dnfguild.ErrPermissionDenied
		}
		for _, identity := range identities {
			matched := false
			for _, peer := range snapshot.Members {
				matched = matched || peer.UserID == identity.character && peer.SessionGeneration == identity.generation
			}
			if !matched {
				return dnfguild.ErrPermissionDenied
			}
		}
	} else if len(identities) != 1 {
		return dnfguild.ErrPermissionDenied
	}
	var result dnfguild.DungeonEntryFatigueResult
	commit := func() error {
		var err error
		result, err = dnfguild.CommitDungeonEntryFatigue(ctx, repos.Guild, q, s.gameplayNow())
		return err
	}
	if inParty {
		err = manager.WithSnapshot(snapshot, commit)
	} else {
		err = manager.WithSoloMember(identities[0].character, commit)
	}
	if errors.Is(err, dnfguild.ErrDungeonEntryFatigue) {
		return fmt.Errorf("%w: %w", dnfdungeon.ErrEntryFatigueExhausted, err)
	}
	if err != nil {
		return err
	}
	markGuildDungeonEntryFatigueCommitted(members, result.Characters, q.Cost)
	s.logGameEvent(leader, "game-guild-dungeon-entry-fatigue-committed", "guild_id", q.Entries[0].GuildID, "dungeon_id", runtime.Dungeon.ID, "run_id", q.Entries[0].RunID, "cost", q.Cost, "member_count", len(members), "replayed", result.Replayed)
	return nil
}

func guildDungeonEntryFatigueMembers(
	leader *gameSession,
	runtime *runtimeDungeonState,
	leaderScene worldmap.DungeonRoomScene,
	followers []*runtimePartyPreparedDungeonEntry,
) ([]guildDungeonEntryFatigueMember, error) {
	members := []guildDungeonEntryFatigueMember{{session: leader, runtime: runtime, scene: leaderScene}}
	for _, prepared := range followers {
		if prepared == nil || prepared.session == nil || prepared.runtime == nil {
			return nil, dnfguild.ErrPermissionDenied
		}
		members = append(members, guildDungeonEntryFatigueMember{
			session: prepared.session,
			runtime: prepared.runtime,
			scene:   prepared.scene,
		})
	}
	seen := map[uint16]bool{}
	for _, member := range members {
		if member.session.selectedCharacterID == 0 || seen[member.session.selectedCharacterID] {
			return nil, dnfguild.ErrPermissionDenied
		}
		seen[member.session.selectedCharacterID] = true
	}
	return members, nil
}

func markGuildDungeonEntryFatigueCommitted(
	members []guildDungeonEntryFatigueMember,
	roles map[string]dnfrepo.CharacterRecord,
	cost int64,
) {
	for _, member := range members {
		if roles != nil {
			if character, found := roles[strconv.Itoa(int(member.session.selectedCharacterID))]; found {
				member.runtime.Character = character
			}
		}
		if !member.runtime.fatigueStartDebited {
			member.runtime.fatigueSpent += cost
		}
		member.runtime.fatigueStartDebited = true
		if member.runtime.fatigueRooms == nil {
			member.runtime.fatigueRooms = make(map[worldmap.RoomCoordinate]bool)
		}
		member.runtime.fatigueRooms[member.scene.Coordinate] = true
	}
}
