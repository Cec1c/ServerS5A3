package dnfbridge

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"
	"time"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
	dungeon "longheng.io/server/internal/modules/dnf/dungeon"
	guild "longheng.io/server/internal/modules/dnf/guild"
	r "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/platform/eventloop"
)

func (s *Service) currentGuildDungeonCatalog() (*guild.DungeonCatalog, error) {
	s.guildDungeonCatalogMu.Lock()
	defer s.guildDungeonCatalogMu.Unlock()
	if s.guildDungeonCatalog != nil {
		return s.guildDungeonCatalog, nil
	}
	items, err := s.currentPVFItemCatalog()
	if err != nil {
		return nil, err
	}
	catalog, err := guild.LoadDungeonCatalog(items.source)
	if err != nil {
		return nil, err
	}
	s.guildDungeonCatalog = catalog
	return catalog, nil
}

// Projection only: no PVF load and no registry writes on the login path.
func (s *Service) writeCurrentGuildDungeonSnapshot(session *gameSession) error {
	repos, ok := s.repositoryGroup()
	if !ok || repos.Settings == nil {
		return nil
	}
	ctx, cancel := s.currentGuildContext()
	defer cancel()
	reg, err := guild.Load(ctx, repos.Settings)
	if err != nil {
		return err
	}
	cycle, err := reg.DungeonPolicy.Cycle(s.gameplayNow())
	if err != nil {
		return err
	}
	snapshot := currentGuildDungeonWireSnapshot{}
	g, _, member := reg.GuildForOwner(r.GuildOwner{AccountID: s.accountIDForSession(session), CharacterID: strconv.Itoa(int(session.selectedCharacterID))})
	if member {
		snapshot.EndUnix = cycle.EndUnix
		snapshot.ClearCounts, err = guild.DungeonCounts(g, cycle)
		if err != nil {
			return err
		}
	}
	body, err := buildCurrentGuildDungeonSnapshotBody(snapshot)
	if err != nil {
		return err
	}
	return s.sendGameUpperRawClass(session, currentGuildDungeonSnapshotNotification, body, 0)
}

func currentGuildDungeon(runtime *runtimeDungeonState) bool {
	if runtime == nil {
		return false
	}
	// Catalog IDs are checked as well as the source flag so an incomplete
	// metadata projection cannot route a known exclusive map as ordinary.
	if currentGuildDungeonEntryID(runtime.Dungeon.ID) {
		return true
	}
	for _, section := range runtime.Dungeon.SourceSections {
		if strings.EqualFold(section.Name, "guild dungeon") {
			return true
		}
	}
	return runtime.Dungeon.Metadata.IntegerValues["guild dungeon"] != 0 || runtime.Dungeon.Metadata.Flags["guild dungeon"]
}

func (s *Service) freezeCurrentGuildDungeonGroup(ctx context.Context, session *gameSession, runtime *runtimeDungeonState, followers []*runtimePartyPreparedDungeonEntry) error {
	if !currentGuildDungeon(runtime) {
		return nil
	}
	if runtime.Request.SpecialMode != 0 || dungeonRequestedHell(dungeonHellRequestOption(runtime.Request)) {
		return guild.ErrPermissionDenied
	}
	if len(runtime.Dungeon.Metadata.DesignatedDifficulties) > 0 {
		allowed := false
		for _, difficulty := range runtime.Dungeon.Metadata.DesignatedDifficulties {
			allowed = allowed || difficulty == int64(runtime.Request.Difficulty)
		}
		if !allowed {
			return errDungeonDifficultyInvalid
		}
	}
	catalog, err := s.currentGuildDungeonCatalog()
	if err != nil {
		return err
	}
	repos, ok := s.repositoryGroup()
	if !ok || repos.Settings == nil {
		return guild.ErrRepositoryUnavailable
	}
	reg, err := guild.Load(ctx, repos.Settings)
	if err != nil {
		return err
	}
	sessions := []*gameSession{session}
	runtimes := []*runtimeDungeonState{runtime}
	for _, p := range followers {
		if p == nil || p.session == nil || p.runtime == nil || p.runtime.Dungeon.ID != runtime.Dungeon.ID {
			return guild.ErrPermissionDenied
		}
		sessions = append(sessions, p.session)
		runtimes = append(runtimes, p.runtime)
	}
	owners := make([]r.GuildOwner, len(sessions))
	for i, p := range sessions {
		owners[i] = r.GuildOwner{AccountID: s.accountIDForSession(p), CharacterID: strconv.Itoa(int(p.selectedCharacterID))}
		if runtimes[i].Character.CharacterID != owners[i].CharacterID || runtimes[i].Character.AccountID != owners[i].AccountID {
			return guild.ErrPermissionDenied
		}
	}
	var random [16]byte
	if _, err = rand.Read(random[:]); err != nil {
		return err
	}
	entries, err := guild.PrepareDungeonEntries(reg, catalog, uint32(runtime.Dungeon.ID), owners, hex.EncodeToString(random[:]), s.gameplayNow())
	if err != nil {
		return err
	}
	for i, entry := range entries {
		runtimes[i].guildDungeonClear = &dungeon.GuildDungeonClear{Entry: entry, Catalog: catalog, PartySize: len(entries)}
	}
	return nil
}

func (s *Service) freezeCurrentGuildDungeonRewards(runtime *runtimeDungeonState) error {
	if !currentGuildDungeon(runtime) {
		return nil
	}
	clear := runtime.guildDungeonClear
	if clear == nil || clear.Entry.DungeonID != uint32(runtime.Dungeon.ID) || !runtime.ordinaryFinalRoomClearAccepted || runtime.clearMapCompletionKey == "" {
		return guild.ErrPermissionDenied
	}
	if clear.Items != nil {
		return nil
	}
	catalog, err := s.currentPVFItemCatalog()
	if err != nil {
		return err
	}
	now := s.gameplayNow().UTC()
	items := map[uint32]dungeon.CardItemReward{}
	for _, id := range clear.Catalog.ItemIDs() {
		definition, err := catalog.ResolveItem(id)
		if err != nil {
			return err
		}
		if definition.Kind != dungeonDropItemStackable {
			return errDungeonPickupItemInvalid
		}
		item, err := currentDungeonCardItemReward(newCurrentDungeonDropLCG(runtime.Seed), catalog, id, now)
		if err != nil {
			return err
		}
		item.Extra["source"] = "guild_dungeon_clear"
		items[id] = dungeon.CardItemReward{Stack: r.ItemStack{ItemID: item.ItemID, Count: 1, Bind: item.Bind, RawEntry: append([]byte(nil), item.RawEntry...), Extra: cloneDungeonCardStringMap(item.Extra)}, Stackable: item.Stackable, SlotStart: item.SlotStart, SlotEnd: item.SlotEnd, ExpireAt: item.ExpireAt, AllowQuickSlots: item.AllowQuickSlots, PreferQuickSlots: item.PreferQuickSlots, StackLimit: item.StackLimit}
	}
	clear.Items = items
	clear.Project = func(stack r.ItemStack, expiration time.Time) (r.ItemStack, error) {
		stack, _ = applyCurrentStackableExpirationAt(stack, expiration, now)
		return stack, nil
	}
	return nil
}

func (s *Service) sendCurrentGuildDungeonSettlement(session *gameSession, runtime *runtimeDungeonState, plan *currentDungeonSettlementPacketPlan) error {
	if runtime.guildDungeonClear == nil || runtime.settlementGuildDungeonSent {
		return nil
	}
	if len(plan.GuildDungeonItemSlots) > 0 {
		if err := s.sendSelectedIncrementalItemSlotRefreshes(session, "guild-dungeon-settlement", plan.GuildDungeonItemSlots); err != nil {
			return err
		}
	}
	if err := s.writeCurrentGuildDungeonSnapshot(session); err != nil {
		return err
	}
	runtime.settlementGuildDungeonSent = true
	s.queueCurrentGuildDungeonRefresh(runtime.guildDungeonClear.Entry.GuildID, session.selectedCharacterID)
	s.logGameEvent(session, "game-guild-dungeon-clear-projected", "guild_id", runtime.guildDungeonClear.Entry.GuildID, "dungeon_id", runtime.Dungeon.ID, "cycle", runtime.guildDungeonClear.Entry.Cycle.Key, "completion", runtime.guildDungeonClear.Entry.CompletionKey())
	return nil
}

// Called with the clearer's dungeon lock held. Queue without waiting for a
// peer or taking its town/dungeon lock; a full queue is repaired by the next
// login/selector projection. Rewards never depend on projection delivery.
func (s *Service) queueCurrentGuildDungeonRefresh(guildID uint32, skip uint16) {
	ctx, cancel := s.currentGuildContext()
	defer cancel()
	_, reg, err := s.loadCurrentGuildRegistry(ctx)
	if err != nil {
		s.logInfo("guild dungeon peer snapshot deferred", "error", err)
		return
	}
	g := reg.Guilds[guildID]
	if g == nil {
		return
	}
	for id, member := range g.Members {
		numeric, err := strconv.ParseUint(id, 10, 16)
		if err != nil || uint16(numeric) == skip {
			continue
		}
		peer, ok := s.onlineGameSessionCharacterSnapshot(uint16(numeric))
		if !ok || s.accountIDForSession(peer.session) != member.AccountID || peer.session.events == nil || peer.session.events.loop == nil {
			continue
		}
		const source = "guild-dungeon-progress-refresh"
		err = peer.session.events.loop.Submit(eventloop.Event{Type: source, Priority: eventloop.PriorityMedium, Data: gameSessionEventTask{source: source, characterID: peer.character, characterGeneration: peer.generation, async: true, run: func() error {
			if !s.boundGameSessionCharacterCurrent(peer) {
				return nil
			}
			current, err := s.currentGuildForSelectedCharacter(peer.session)
			if err != nil {
				return err
			}
			if current == nil || current.ID != guildID {
				return nil
			}
			return s.writeCurrentGuildDungeonSnapshot(peer.session)
		}}})
		if err != nil {
			s.logGameEvent(peer.session, "game-guild-dungeon-peer-refresh-deferred", "error", err)
		}
	}
}

func guildDungeonRefreshSlots(items []dungeon.GuildDungeonGrantedItem) []alignedcmd.ItemSlotRefresh {
	slots := make([]alignedcmd.ItemSlotRefresh, 0, len(items))
	for _, item := range items {
		slots = append(slots, alignedcmd.ItemSlotRefresh{ListType: 0, SlotIndex: item.Slot})
	}
	return slots
}

func validatePreparedGuildDungeon(runtime *runtimeDungeonState) error {
	if currentGuildDungeon(runtime) && (runtime.guildDungeonClear == nil || runtime.guildDungeonClear.Entry.DungeonID != uint32(runtime.Dungeon.ID)) {
		return fmt.Errorf("guild dungeon admission was not frozen")
	}
	return nil
}
