package dnfbridge

import (
	"encoding/binary"
	"sort"
	"strings"
	"sync/atomic"
	"time"

	"longheng.io/server/internal/modules/dnf/dungeoncmd"
	"longheng.io/server/internal/modules/dnf/worldmap"
)

const (
	ekernDeathDiagnosticEnv = "DNFBRIDGE_EKERN_DEATH_DIAGNOSTIC"
	ekernDiagnosticMessage  = "dnfbridge ekern death diagnostic"
	ekernDiagnosticLimit    = 64
	ekernDiagnosticTTL      = 10 * time.Minute
)

var ekernDiagnosticIDs atomic.Uint64

// Owned by the session dungeon mutex. This budget is not room/death authority.
type ekernDiagnosticBudget struct {
	id      uint64
	count   int
	started time.Time
}

type ekernDiagnosticTrace struct {
	fields                  []any
	targetKey               uint32
	ordinaryNotifyAttempted bool
	ordinaryNotifyWritten   bool
}

// All callers hold session.dungeon.mu, including the deferred finish calls.
// Disabled/non-Ekern requests do not allocate, snapshot, log, or change state.
// No packet bytes, integrity footer, account/character identity or error text
// is copied to the ordinary log. The run ID is a process-local counter.
func (s *Service) beginEkernDiagnosticLocked(rt *runtimeDungeonState, event string, bodyLen int) *ekernDiagnosticTrace {
	if s == nil || !s.options.ekernDeathDiagnostic || s.logger == nil ||
		rt == nil || rt.Room == nil || rt.Session == nil ||
		!strings.HasPrefix(normalizeDungeonRuntimePath(rt.Dungeon.Path), "dungeon/legend/ekern/") {
		return nil
	}
	now := s.gameplayNow()
	if rt.ekernDiagnostic == nil {
		rt.ekernDiagnostic = &ekernDiagnosticBudget{id: ekernDiagnosticIDs.Add(1), started: now}
	}
	budget := rt.ekernDiagnostic
	if budget.count >= ekernDiagnosticLimit || !now.Before(budget.started.Add(ekernDiagnosticTTL)) {
		return nil
	}
	budget.count++
	trace := &ekernDiagnosticTrace{fields: []any{
		"run", budget.id, "sample", budget.count, "event", event,
		"dungeon_id", rt.Dungeon.ID, "maze_index", rt.MazeIndex,
		"body_len", bodyLen,
	}}
	trace.fields = append(trace.fields, ekernDiagnosticRoomState(rt, "before")...)
	return trace
}

func (d *ekernDiagnosticTrace) addDeathRequest(s *Service, p *gameSession, rt *runtimeDungeonState, request dungeoncmd.DieMonsterRequest) {
	scene, _ := rt.Session.Scene()
	d.targetKey = request.RuntimeObjectKey
	d.fields = append(d.fields,
		"object_key", request.RuntimeObjectKey, "owner_key", request.OwnerObjectKey,
		"layout", request.Layout, "contribution_count", request.CombatEntryCount,
		"owner_is_player", request.OwnerObjectKey == currentSceneActorObjectKey(p.selectedCharacterID),
		"owner_is_announced_actor", dungeonRuntimeContainsDeathOwnerObjectKey(rt, uint32(request.OwnerObjectKey)),
		"friendly_apc_proven", s.currentDungeonFriendlyAPCDeath(rt, scene, request),
		"friendly_owner_proven", s.currentDungeonFriendlyOwnerDeath(rt, scene, request),
		"room_proof_enabled", s.dungeonDeathRoomProofActive(),
		"target_announced_live", currentDungeonRoomAnnouncedLiveHostile(rt, scene, request.RuntimeObjectKey),
	)
	for _, m := range rt.Room.Snapshot().Monsters {
		if m.ObjectKey != request.RuntimeObjectKey {
			continue
		}
		team := int64(-1)
		if index := m.Reference.Index; index >= 0 && index < len(scene.Map.Map.MonsterTeam) {
			team = scene.Map.Map.MonsterTeam[index]
		}
		d.fields = append(d.fields, "target_monster_id", m.Spawn.MonsterID,
			"target_pvf_team", team, "target_state", string(m.State))
		break
	}
	// Only parse the independently established official contribution layout.
	// The diagnostic does not invent semantics for legacy/unknown entry widths.
	if s.options.currentClientVersion != "official-24.9" {
		return
	}
	const width = dungeoncmd.Official249DieMonsterEntrySize
	if len(request.CombatEntries) != int(request.CombatEntryCount)*width {
		return
	}
	count := min(int(request.CombatEntryCount), 8)
	kinds, keys, nonzero, teams := []uint16{}, []uint16{}, []bool{}, []int64{}
	for i := 0; i < count; i++ {
		entry := request.CombatEntries[i*width:]
		kind, key := binary.LittleEndian.Uint16(entry), binary.LittleEndian.Uint16(entry[2:])
		kinds, keys = append(kinds, kind), append(keys, key)
		nonzero = append(nonzero, binary.LittleEndian.Uint64(entry[4:]) != 0)
		team := int64(-1) // -1 means no same-index current-room PVF team proof.
		if kind == 0x211 {
			if m, ok := rt.Room.AnnouncedMonster(uint32(key)); ok {
				index := m.Reference.Index
				if index >= 0 && index < len(scene.Map.Map.MonsterTeam) {
					team = scene.Map.Map.MonsterTeam[index]
				}
			}
		}
		teams = append(teams, team)
	}
	d.fields = append(d.fields, "contributor_kinds", kinds, "contributor_keys", keys,
		"contributor_nonzero", nonzero, "contributor_pvf_teams", teams,
		"contributors_truncated", int(request.CombatEntryCount) > count)
}

func (s *Service) finishEkernDiagnosticLocked(rt *runtimeDungeonState, d *ekernDiagnosticTrace, reason string, err error) {
	if d == nil {
		return
	}
	args := append(d.fields, "outcome", reason, "handler_error", err != nil,
		"ordinary_op38_attempted", d.ordinaryNotifyAttempted,
		"ordinary_op38_written", d.ordinaryNotifyWritten)
	if d.targetKey != 0 {
		_, dead := rt.Room.DefeatedDeathTarget(d.targetKey)
		args = append(args, "after_target_defeated", dead)
	}
	args = append(args, ekernDiagnosticRoomState(rt, "after")...)
	s.logInfo(ekernDiagnosticMessage, args...)
}

// This helper is only used before the handlers take the dungeon mutex.
func (s *Service) logEkernRejectedRequestDiagnostic(p *gameSession, body []byte, event, reason string) {
	if s == nil || !s.options.ekernDeathDiagnostic || p == nil {
		return
	}
	p.dungeon.mu.Lock()
	defer p.dungeon.mu.Unlock()
	rt := p.dungeon.runtime
	d := s.beginEkernDiagnosticLocked(rt, event, len(body))
	if d != nil && event == "death" && len(body) >= 4 {
		d.targetKey = binary.LittleEndian.Uint32(body[:4])
		d.fields = append(d.fields, "object_key", d.targetKey)
	}
	s.finishEkernDiagnosticLocked(rt, d, reason, nil)
}

func ekernDiagnosticRoomState(rt *runtimeDungeonState, prefix string) []any {
	scene, ok := rt.Session.Scene()
	if !ok {
		return []any{prefix + "_scene_present", false}
	}
	room := rt.Room.Snapshot()
	remaining := []uint32{}
	bound := map[worldmap.HostileReference]bool{}
	for key, ref := range scene.RuntimeObjects {
		bound[ref] = true
		if runtimeSceneContainsHostile(scene.BlockingHostiles, ref) && !dungeonSceneObjectDefeated(scene.DefeatedObjects, key) {
			remaining = append(remaining, key)
		}
	}
	unbound := 0
	for _, ref := range scene.BlockingHostiles {
		if !bound[ref] {
			unbound++
		}
	}
	sort.Slice(remaining, func(i, j int) bool { return remaining[i] < remaining[j] })
	remainingCount := len(remaining)
	remaining = remaining[:min(remainingCount, 16)]
	announced, defeated := 0, 0
	for _, m := range room.Monsters {
		if m.State == runtimeDungeonMonsterAnnounced {
			announced++
		} else if m.State == runtimeDungeonMonsterDefeated {
			defeated++
		}
	}
	return []any{
		prefix + "_map_id", scene.Map.Map.ID, prefix + "_room_x", scene.Coordinate.X, prefix + "_room_y", scene.Coordinate.Y,
		prefix + "_room_cleared", scene.Cleared,
		prefix + "_announced_monsters", announced, prefix + "_defeated_monsters", defeated,
		prefix + "_remaining_blocking_count", remainingCount, prefix + "_remaining_blocking_keys", remaining,
		prefix + "_blocking_keys_truncated", remainingCount > len(remaining), prefix + "_unbound_blockers", unbound,
	}
}
