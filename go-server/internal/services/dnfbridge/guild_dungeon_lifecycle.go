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
	return s.writeCurrentGuildDungeonSnapshotForGuilds(session, nil)
}

func (s *Service) writeCurrentGuildDungeonSnapshotForGuild(session *gameSession, expectedGuildID uint32) error {
	if expectedGuildID == 0 {
		return s.writeCurrentGuildDungeonSnapshotForGuilds(session, nil)
	}
	return s.writeCurrentGuildDungeonSnapshotForGuilds(session, []uint32{expectedGuildID})
}

func (s *Service) writeCurrentGuildDungeonSnapshotForGuilds(session *gameSession, expectedGuildIDs []uint32) error {
	repos, ok := s.repositoryGroup()
	if !ok || repos.Settings == nil {
		if len(expectedGuildIDs) > 0 {
			return guild.ErrRepositoryUnavailable
		}
		return nil
	}
	identity, bound := s.boundGameSessionCharacterSnapshot(session)
	if len(expectedGuildIDs) > 0 {
		if !bound || session.loginRevoked.Load() || (session.ctx != nil && session.ctx.Err() != nil) {
			return nil
		}
		ctx, cancel := s.currentGuildContext()
		defer cancel()
		// Acquire wireMu before reading the authoritative dungeon view so a
		// wire wait overlapping a leave/kick/rebind drops stale NOTI741 writes.
		if hook := testBeforeGuildPeerWireLock; hook != nil {
			hook("dungeon")
		}
		if err := s.lockGameWire(session, ctx); err != nil {
			return err
		}
		defer session.wireMu.Unlock()
		if err := ctx.Err(); err != nil {
			return err
		}
		if session.channelExitWireClosed || session.loginRevoked.Load() || !s.boundGameSessionCharacterCurrent(identity) {
			return nil
		}
		character, err := s.currentGuildOwnedSelectedIdentity(session)
		if err != nil {
			return err
		}
		view, err := guild.ReadDungeonView(ctx, repos.Settings)
		if err != nil {
			return err
		}
		owner := r.GuildOwner{AccountID: character.AccountID, CharacterID: character.CharacterID}
		cycle, counts, member, err := view.CountsForGuilds(owner, expectedGuildIDs, s.gameplayNow())
		if err != nil {
			return err
		}
		if !member || session.channelExitWireClosed || session.loginRevoked.Load() || !s.boundGameSessionCharacterCurrent(identity) {
			return nil
		}
		body, err := buildCurrentGuildDungeonSnapshotBody(currentGuildDungeonWireSnapshot{
			EndUnix:     cycle.EndUnix,
			ClearCounts: counts,
		})
		if err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		// Keep the TCP write budget independent of the 3s storage/prep context.
		finishWrite, err := beginGameWriteBudgetLocked(session, nil)
		if err != nil {
			return err
		}
		defer finishWrite()
		if err := s.sendGameUpperRawClassCodecLocked(session, currentGuildDungeonSnapshotNotification, body, 0, true); err != nil {
			session.closeGameTransport()
			return err
		}
		return nil
	}
	owner := r.GuildOwner{AccountID: s.accountIDForSession(session), CharacterID: strconv.Itoa(int(session.selectedCharacterID))}
	ctx, cancel := s.currentGuildContext()
	defer cancel()
	view, err := guild.ReadDungeonView(ctx, repos.Settings)
	if err != nil {
		return err
	}
	cycle, counts, member, err := view.Counts(owner, s.gameplayNow())
	if err != nil {
		return err
	}
	snapshot := currentGuildDungeonWireSnapshot{}
	if member {
		snapshot.EndUnix = cycle.EndUnix
		snapshot.ClearCounts = counts
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
	view, err := guild.ReadDungeonView(ctx, repos.Settings)
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
	entries, err := view.PrepareEntries(catalog, uint32(runtime.Dungeon.ID), owners, hex.EncodeToString(random[:]), s.gameplayNow())
	if err != nil {
		return err
	}
	for i, entry := range entries {
		runtimes[i].guildDungeonClear = &dungeon.GuildDungeonClear{Entry: entry, Catalog: catalog, PartySize: len(entries)}
	}
	return freezeGuildDungeonKillPlan(runtimes, entries)
}

func (s *Service) freezeCurrentGuildDungeonRewards(runtime *runtimeDungeonState) error {
	if !currentGuildDungeon(runtime) {
		return nil
	}
	clear := runtime.guildDungeonClear
	if clear == nil || clear.Entry.DungeonID != uint32(runtime.Dungeon.ID) || !runtime.ordinaryFinalRoomClearAccepted || runtime.clearMapCompletionKey == "" {
		return guild.ErrPermissionDenied
	}
	return s.resolveCurrentGuildDungeonRewardItems(runtime)
}

func (s *Service) resolveCurrentGuildDungeonRewardItems(runtime *runtimeDungeonState) error {
	clear := runtime.guildDungeonClear
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
	if len(plan.GuildDungeonRepeatRewardBody) > 0 {
		if err := s.sendGameUpperRawClass(session, currentGuildDungeonRepeatRewardNotification, plan.GuildDungeonRepeatRewardBody, 0); err != nil {
			return err
		}
	}
	runtime.settlementGuildDungeonSent = true
	s.queueCurrentGuildDungeonRefresh(runtime.guildDungeonClear.Entry.GuildID, session.selectedCharacterID)
	s.logGameEvent(session, "game-guild-dungeon-clear-projected", "guild_id", runtime.guildDungeonClear.Entry.GuildID, "dungeon_id", runtime.Dungeon.ID, "cycle", runtime.guildDungeonClear.Entry.Cycle.Key, "completion", runtime.guildDungeonClear.Entry.CompletionKey())
	return nil
}

// Bounded per-session coalesce token for guild dungeon NOTI741 peer refreshes.
// All mutable fields are guarded by session.guild.mu; no IO or queue submit holds it.
type currentGuildDungeonRefresh struct {
	peer            boundGameSessionCharacter
	guildIDs        []uint32
	revision        uint64
	queued, running bool
	coalesceTimer   *time.Timer
	stopContext     func() bool
}

func clearCurrentGuildDungeonRefreshLocked(session *gameSession) {
	if p := session.guild.dungeonRefresh; p != nil {
		if p.coalesceTimer != nil {
			p.coalesceTimer.Stop()
		}
		if p.stopContext != nil {
			p.stopContext()
		}
	}
	session.guild.dungeonRefresh = nil
}

func (s *Service) discardCurrentGuildDungeonRefresh(p *currentGuildDungeonRefresh) {
	session := p.peer.session
	session.guild.mu.Lock()
	if session.guild.dungeonRefresh == p {
		clearCurrentGuildDungeonRefreshLocked(session)
	}
	session.guild.mu.Unlock()
}

func (s *Service) requestCurrentGuildDungeonRefresh(peer boundGameSessionCharacter, guildID uint32) {
	if peer.session == nil || peer.session.events == nil || peer.session.events.loop == nil {
		return
	}
	s.mu.Lock()
	if s.gameSessions[peer.character] != peer.session || peer.session.selectedCharacterID != peer.character || peer.session.characterGeneration != peer.generation {
		s.mu.Unlock()
		return
	}
	session := peer.session
	session.guild.mu.Lock()
	s.mu.Unlock()
	if session.guild.presenceRefreshClosed || session.loginRevoked.Load() ||
		!session.events.active.Load() ||
		(session.ctx != nil && session.ctx.Err() != nil) {
		session.guild.mu.Unlock()
		return
	}
	p := session.guild.dungeonRefresh
	if p == nil || p.peer != peer {
		clearCurrentGuildDungeonRefreshLocked(session)
		p = &currentGuildDungeonRefresh{peer: peer}
		session.guild.dungeonRefresh = p
		if session.ctx != nil {
			p.stopContext = context.AfterFunc(session.ctx, func() { s.discardCurrentGuildDungeonRefresh(p) })
		}
	}
	p.guildIDs = appendUniqueGuildID(p.guildIDs, guildID)
	p.revision++
	if p.queued || p.running || p.coalesceTimer != nil {
		session.guild.mu.Unlock()
		return
	}
	if window := s.guildPresenceCoalesceWindow(); window > 0 && !session.guild.lastDungeonRefreshAt.IsZero() {
		if elapsed := s.guildRosterNow().Sub(session.guild.lastDungeonRefreshAt); elapsed >= 0 && elapsed < window {
			p.coalesceTimer = s.armCurrentGuildDungeonCoalesceTimerLocked(session, p, window-elapsed)
			session.guild.mu.Unlock()
			return
		}
	}
	p.queued = true
	session.guild.mu.Unlock()
	s.submitCurrentGuildDungeonRefresh(p)
}

func (s *Service) armCurrentGuildDungeonCoalesceTimerLocked(session *gameSession, p *currentGuildDungeonRefresh, delay time.Duration) *time.Timer {
	return time.AfterFunc(delay, func() {
		session.guild.mu.Lock()
		if session.guild.dungeonRefresh != p || session.guild.presenceRefreshClosed ||
			session.loginRevoked.Load() ||
			(session.events != nil && !session.events.active.Load()) ||
			(session.ctx != nil && session.ctx.Err() != nil) {
			if session.guild.dungeonRefresh == p {
				clearCurrentGuildDungeonRefreshLocked(session)
			}
			session.guild.mu.Unlock()
			return
		}
		p.coalesceTimer = nil
		if p.queued || p.running {
			session.guild.mu.Unlock()
			return
		}
		p.queued = true
		session.guild.mu.Unlock()
		s.submitCurrentGuildDungeonRefresh(p)
	})
}

func (s *Service) submitCurrentGuildDungeonRefresh(p *currentGuildDungeonRefresh) {
	if !s.boundGameSessionCharacterCurrent(p.peer) {
		s.discardCurrentGuildDungeonRefresh(p)
		return
	}
	session := p.peer.session
	if session.events == nil || session.events.loop == nil {
		s.discardCurrentGuildDungeonRefresh(p)
		return
	}
	const source = "guild-dungeon-progress-refresh"
	err := s.tryPostGameSessionCharacterEventWithDiscard(session, source, p.peer.character, p.peer.generation,
		func() error { return s.runCurrentGuildDungeonRefresh(p) },
		func() { s.discardCurrentGuildDungeonRefresh(p) })
	if err == nil {
		return
	}
	session.guild.mu.Lock()
	if session.guild.dungeonRefresh == p {
		p.queued = false
		if !p.running && p.coalesceTimer == nil {
			clearCurrentGuildDungeonRefreshLocked(session)
		}
	}
	session.guild.mu.Unlock()
	s.logGameEvent(session, "game-guild-dungeon-peer-refresh-deferred", "error", err)
}

func (s *Service) runCurrentGuildDungeonRefresh(p *currentGuildDungeonRefresh) error {
	session := p.peer.session
	session.guild.mu.Lock()
	if session.guild.dungeonRefresh != p {
		session.guild.mu.Unlock()
		return nil
	}
	p.queued, p.running = false, true
	revision := p.revision
	guildIDs := append([]uint32(nil), p.guildIDs...)
	session.guild.mu.Unlock()
	executedRealRefresh := false
	defer func() {
		session.guild.mu.Lock()
		if session.guild.dungeonRefresh != p {
			session.guild.mu.Unlock()
			return
		}
		p.running = false
		if executedRealRefresh {
			session.guild.lastDungeonRefreshAt = s.guildRosterNow()
		}
		followup := executedRealRefresh && p.revision != revision &&
			!session.guild.presenceRefreshClosed && !session.loginRevoked.Load() &&
			(session.events == nil || session.events.active.Load()) &&
			(session.ctx == nil || session.ctx.Err() == nil)
		submitNow := false
		if followup {
			if window := s.guildPresenceCoalesceWindow(); window > 0 && session.events != nil && session.events.loop != nil {
				p.coalesceTimer = s.armCurrentGuildDungeonCoalesceTimerLocked(session, p, window)
			} else {
				p.queued = true
				submitNow = true
			}
		} else {
			clearCurrentGuildDungeonRefreshLocked(session)
		}
		session.guild.mu.Unlock()
		if submitNow {
			s.submitCurrentGuildDungeonRefresh(p)
		}
	}()
	if !s.boundGameSessionCharacterCurrent(p.peer) {
		return nil
	}
	executedRealRefresh = true
	return s.writeCurrentGuildDungeonSnapshotForGuilds(session, guildIDs)
}

// Called with the clearer's dungeon lock held. Queue without waiting for a
// peer or taking its town/dungeon lock; a full queue is repaired by the next
// login/selector projection. Rewards never depend on projection delivery.
func (s *Service) queueCurrentGuildDungeonRefresh(guildID uint32, skip uint16) {
	ctx, cancel := s.currentGuildContext()
	defer cancel()
	_, reg, err := s.readCurrentGuildDirectory(ctx)
	if err != nil {
		s.logInfo("guild dungeon peer snapshot deferred", "error", err)
		return
	}
	for _, member := range reg.Members(guildID) {
		id := member.CharacterID
		numeric, err := strconv.ParseUint(id, 10, 16)
		if err != nil || uint16(numeric) == skip {
			continue
		}
		peer, ok := s.onlineGameSessionCharacterSnapshot(uint16(numeric))
		if !ok || s.accountIDForSession(peer.session) != member.AccountID || peer.session.events == nil || peer.session.events.loop == nil {
			continue
		}
		s.requestCurrentGuildDungeonRefresh(peer, guildID)
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
