package dnfbridge

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"sync"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
	"longheng.io/server/internal/modules/dnf/dungeon"
	"longheng.io/server/internal/modules/dnf/guild"
	"longheng.io/server/internal/modules/dnf/worldmap"
)

// Shared immutable selected-layout budget and admitted roster. Only the first
// valid report's recipient snapshot is mutable, serialized independently of SQL.
type currentGuildDungeonKillPlan struct {
	mu         sync.Mutex
	targets    map[string]int64
	entries    map[string]guild.DungeonEntry
	recipients map[string][]questMaterialRecipient
}

func guildDungeonMonsterKey(coordinate worldmap.RoomCoordinate, mapID int64, index int) string {
	return fmt.Sprintf("%d/%d/%d/%d", coordinate.X, coordinate.Y, mapID, index)
}

func guildDungeonKillTargets(rooms []worldmap.DungeonRoom) (map[string]int64, error) {
	targets := map[string]int64{}
	for _, room := range rooms {
		scene, err := worldmap.NewDungeonRoomScene(room)
		if err != nil {
			return nil, err
		}
		for _, ref := range scene.ExpectedHostiles {
			if ref.Kind != worldmap.HostileMonster || ref.Index < 0 || ref.Index >= len(scene.Monsters) {
				continue
			}
			if ref.Index < len(scene.Map.Map.MonsterTeam) && scene.Map.Map.MonsterTeam[ref.Index] == 0 {
				continue
			}
			monster := scene.Monsters[ref.Index]
			actorType, err := currentDungeonMonsterActorType(monster)
			if err != nil {
				return nil, err
			}
			if monster.MonsterID <= 0 || actorType > 3 {
				continue
			}
			targets[guildDungeonMonsterKey(scene.Coordinate, scene.Map.Map.ID, ref.Index)] = monster.MonsterID
		}
	}
	if len(targets) == 0 || len(targets) > 10000 {
		return nil, dungeon.ErrRewardInvalid
	}
	return targets, nil
}

func freezeGuildDungeonKillPlan(runtimes []*runtimeDungeonState, entries []guild.DungeonEntry) error {
	if len(runtimes) == 0 || len(entries) != len(runtimes) {
		return guild.ErrPermissionDenied
	}
	if !runtimes[0].guildDungeonClear.Catalog.HasRepeatReward() {
		return nil
	}
	if runtimes[0].Session == nil {
		return dungeon.ErrRewardInvalid
	}
	targets, err := guildDungeonKillTargets(runtimes[0].Session.Rooms())
	if err != nil {
		return err
	}
	plan := &currentGuildDungeonKillPlan{targets: targets, entries: map[string]guild.DungeonEntry{}, recipients: map[string][]questMaterialRecipient{}}
	for _, entry := range entries {
		plan.entries[entry.Owner.CharacterID] = entry
	}
	for _, runtime := range runtimes {
		runtime.guildDungeonKills = plan
	}
	return nil
}

func (s *Service) prepareCurrentGuildDungeonKill(p *gameSession, rt *runtimeDungeonState, key uint32) (func() error, func() error, error) {
	if !currentGuildDungeon(rt) || rt.guildDungeonClear == nil || !rt.guildDungeonClear.Catalog.HasRepeatReward() {
		return nil, nil, nil
	}
	plan := rt.guildDungeonKills
	if plan == nil || p == nil || rt.Session == nil || rt.Character.CharacterID != strconv.Itoa(int(p.selectedCharacterID)) || rt.Character.AccountID != s.accountIDForSession(p) {
		return nil, nil, guild.ErrPermissionDenied
	}
	monster, ok := rt.Room.AnnouncedMonster(key)
	if !ok {
		if !rt.Room.HasCombatDeath(key) {
			return nil, nil, nil
		}
		target, found := rt.Room.DefeatedDeathTarget(key)
		if !found {
			return nil, nil, nil
		}
		monster, ok = rt.Room.MonsterByReference(target.Reference, runtimeDungeonMonsterDefeated)
		if !ok {
			return nil, nil, nil
		}
	}
	scene, ok := rt.Session.Scene()
	if !ok {
		return nil, nil, dungeon.ErrRewardInvalid
	}
	if ref, bound := scene.RuntimeObjects[key]; !bound || ref != monster.Reference {
		return nil, nil, errDungeonActorOwnerConflict
	}
	token := guildDungeonMonsterKey(scene.Coordinate, scene.Map.Map.ID, monster.Reference.Index)
	if monster.Reference.Kind != worldmap.HostileMonster || plan.targets[token] != monster.Spawn.MonsterID {
		return nil, nil, nil
	}
	recipients := s.questMaterialRecipients(p, rt, key)
	plan.mu.Lock()
	if previous, exists := plan.recipients[token]; exists {
		recipients = previous
	} else {
		plan.recipients[token] = recipients
	}
	plan.mu.Unlock()
	if err := s.resolveCurrentGuildDungeonRewardItems(rt); err != nil {
		return nil, nil, err
	}
	repos, ok := s.repositoryGroup()
	if !ok {
		return nil, nil, dungeon.ErrOwnerUnavailable
	}
	owner, err := dungeon.NewOwner(repos)
	if err != nil {
		return nil, nil, err
	}
	results := map[uint16]dungeon.GuildDungeonKillResult{}
	apply := func() error {
		ctx, cancel := context.WithTimeout(context.Background(), createWriteTimeout)
		defer cancel()
		for _, recipient := range recipients {
			entry, found := plan.entries[strconv.Itoa(int(recipient.id))]
			if !found || entry.Owner.AccountID != recipient.account {
				return guild.ErrPermissionDenied
			}
			clear := *rt.guildDungeonClear
			clear.Entry = entry
			result, err := owner.CommitGuildDungeonKill(ctx, dungeon.GuildDungeonKillCommand{Clear: &clear, Target: token, Total: uint32(len(plan.targets)), UpdatedAt: s.gameplayNow()})
			if err != nil {
				return err
			}
			results[recipient.id] = result
		}
		return nil
	}
	publish := func() error {
		for _, recipient := range recipients {
			result := results[recipient.id]
			if recipient.session == p {
				if err := s.sendCurrentGuildDungeonKillProgress(p, rt, result); err != nil {
					return err
				}
				continue
			}
			peer := recipient.session
			identity, bound := s.boundGameSessionCharacterSnapshot(peer)
			if !bound || identity.character != recipient.id {
				continue
			}
			// Persisted above; queue failure never loses a teammate's progress or box.
			if err := s.tryPostGameSessionCharacterEvent(peer, "guild-dungeon-kill-progress", identity.character, identity.generation, func() error {
				peer.dungeon.mu.Lock()
				defer peer.dungeon.mu.Unlock()
				if !s.boundGameSessionCharacterCurrent(identity) {
					return nil
				}
				current := peer.dungeon.runtime
				if current == nil || current.guildDungeonKills != plan {
					return nil
				}
				return s.sendCurrentGuildDungeonKillProgress(peer, current, result)
			}); err != nil {
				s.logGameEvent(p, "guild-dungeon-kill-projection-deferred", "peer_char_id", recipient.id, "error", err)
			}
		}
		return nil
	}
	return apply, publish, nil
}

func (s *Service) sendCurrentGuildDungeonKillProgress(p *gameSession, rt *runtimeDungeonState, result dungeon.GuildDungeonKillResult) error {
	if rt == nil || rt.guildDungeonClear == nil || rt.guildDungeonClear.Entry.Owner.CharacterID != strconv.Itoa(int(p.selectedCharacterID)) || rt.guildDungeonClear.Entry.Owner.AccountID != s.accountIDForSession(p) {
		return guild.ErrPermissionDenied
	}
	if len(result.Slots) > 0 {
		slots := make([]alignedcmd.ItemSlotRefresh, 0, len(result.Slots))
		for _, slot := range result.Slots {
			slots = append(slots, alignedcmd.ItemSlotRefresh{ListType: 0, SlotIndex: slot})
		}
		if err := s.sendSelectedIncrementalItemSlotRefreshes(p, "guild-dungeon-kill", slots); err != nil {
			return err
		}
	}
	if result.MailID != "" {
		if err := s.sendMailboxAlarmToOnlineRecipient(p.selectedCharacterID); err != nil {
			s.logGameEvent(p, "guild-dungeon-reward-mail-alarm-deferred", "mail_id", result.MailID, "error", err)
		}
	}
	// Commit results carry a durable revision; peer queues can deliver out of
	// order and a replica read must not replace a just-committed balance.
	if result.Revision < rt.Character.Stats[guild.DungeonRepeatRewardRevisionStat] {
		return nil
	}
	if rt.Character.Stats == nil {
		rt.Character.Stats = map[string]int64{}
	}
	rt.Character.Stats[guild.DungeonRepeatRewardProgressStat] = int64(result.Reward.Progress)
	rt.Character.Stats[guild.DungeonRepeatRewardFractionStat] = int64(result.Fraction)
	rt.Character.Stats[guild.DungeonRepeatRewardRevisionStat] = result.Revision
	progress := result.Reward.Display
	body, err := buildCurrentGuildDungeonRepeatRewardBody(progress)
	if err != nil {
		return err
	}
	return s.sendGameUpperRawClass(p, currentGuildDungeonRepeatRewardNotification, body, 0)
}

// Compose with the existing pre-death gate; never count arbitrary op39 packets.
func (s *Service) commitGuildDungeonRewardDeath(p *gameSession, rt *runtimeDungeonState, key uint32, material func() error) (runtimeDungeonDeathTarget, bool, error) {
	apply, publish, err := s.prepareCurrentGuildDungeonKill(p, rt, key)
	if err != nil {
		return runtimeDungeonDeathTarget{}, false, err
	}
	prepare := material
	if apply != nil {
		prepare = func() error {
			if material != nil {
				if err := material(); err != nil {
					return err
				}
			}
			return apply()
		}
	}
	target, cleared, err := rt.Room.commitActorDeathReportPrepared(key, rt.Session, true, prepare)
	if apply != nil && errors.Is(err, errDungeonMonsterAlreadyDefeated) {
		if e := apply(); e != nil {
			return target, cleared, e
		}
	}
	if publish != nil && (err == nil || errors.Is(err, errDungeonMonsterAlreadyDefeated)) {
		if e := publish(); e != nil {
			return target, cleared, e
		}
	}
	return target, cleared, err
}
