package dnfbridge

import (
	"context"
	cryptorand "crypto/rand"
	"fmt"
	"longheng.io/server/internal/modules/dnf/alignedcmd"
	dnfdungeon "longheng.io/server/internal/modules/dnf/dungeon"
	onlineevent "longheng.io/server/internal/modules/dnf/onlineevent"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/worldmap"
	"math"
	"math/big"
	"strconv"
	"strings"
)

type currentEpicEntry struct {
	characterID       uint16
	generation        uint64
	rawAccountID      string
	command           dnfdungeon.EpicEntryCommand
	accountDailyLimit int64
}

func (s *Service) currentEpicEntryEnabled(id int64) bool {
	_, ok := dnfdungeon.EpicEntryTicketCount(id)
	return s != nil && s.options.currentClientVersion == "official-24.9" && (ok || id == dnfdungeon.SilentForestDungeonID)
}

func (s *Service) currentEpicEntryAlternatives(ctx context.Context, dungeonID int64) ([][]dnfdungeon.EntryItemRequirement, error) {
	catalog, err := s.currentEpicCatalog(ctx)
	if err != nil {
		return nil, err
	}
	var alternatives [][]dnfdungeon.EntryItemRequirement
	for _, row := range catalog.EntryAlternatives[dungeonID] {
		var items []dnfdungeon.EntryItemRequirement
		for _, item := range row {
			items = append(items, dnfdungeon.EntryItemRequirement{ItemID: item.ItemID, Count: item.Count, ConsumeOnEntry: true})
		}
		alternatives = append(alternatives, items)
	}
	if len(alternatives) == 0 {
		return nil, dnfdungeon.ErrEntryItemConsumeInvalid
	}
	return alternatives, nil
}

func (s *Service) validateCurrentEpicEntryItems(ctx context.Context, character dnfrepo.CharacterRecord, dungeon worldmap.Dungeon, session *gameSession) error {
	if dungeon.ID == dnfdungeon.SilentForestDungeonID {
		return s.validateCurrentSilentForestEntry(ctx, character, dungeon, session)
	}
	if err := validateCurrentEpicEntryContract(dungeon); err != nil {
		return err
	}
	alternatives, err := s.currentEpicEntryAlternatives(ctx, dungeon.ID)
	if err != nil {
		return err
	}
	repos, ok := s.repositoryGroup()
	if !ok || repos.Inventory == nil {
		return dnfdungeon.ErrOwnerUnavailable
	}
	bag, found, err := repos.Inventory.Load(ctx, character.CharacterID)
	if err != nil {
		return err
	}
	if !found || bag.CharacterID != character.CharacterID {
		return dnfdungeon.ErrEntryItemConsumeInsufficient
	}
	if _, err = dnfdungeon.EpicEntryItems(bag, alternatives, s.gameplayNow()); err != nil {
		return err
	}
	// Retain the established Epic Road FP1 admission policy; the DGN start-only
	// fatigue tag is not part of this ticket-priority change.
	fatigue, known := dnfdungeon.CharacterStat(character.Stats, "fatigue", "fp", "疲劳")
	return dnfdungeon.ValidateEntry(dnfdungeon.EntryPolicy{
		CharacterLevel: character.Level, MinimumLevel: dungeon.Metadata.MinimumRequiredLevel.Value,
		MinimumLevelSet: dungeon.Metadata.MinimumRequiredLevel.Set, PartyCount: currentDungeonPartyCount(session),
		PartyLimit: dungeon.Metadata.LimitPartyCount.Value, PartyLimitSet: dungeon.Metadata.LimitPartyCount.Set,
		NoFatigue: dungeon.Metadata.NoFatigue, EnterWithoutFatigue: dungeon.Metadata.Flags["enter without fatigue"],
		Fatigue: fatigue, FatigueKnown: known,
	})
}
func validateCurrentEpicEntryContract(d worldmap.Dungeon) error {
	if d.ID == dnfdungeon.SilentForestDungeonID {
		if !d.Metadata.MinimumRequiredLevel.Set || d.Metadata.MinimumRequiredLevel.Value != 17 || !d.Metadata.NoFatigue || len(d.Metadata.RequiredItems) != 0 {
			return fmt.Errorf("%w: silent forest PVF entry contract", dnfdungeon.ErrEntryItemConsumeInvalid)
		}
		return nil
	}
	count, ok := dnfdungeon.EpicEntryTicketCount(d.ID)
	if !ok || !d.Metadata.MinimumRequiredLevel.Set || d.Metadata.MinimumRequiredLevel.Value != 85 || d.Metadata.NoFatigue || len(d.Metadata.RequiredItems) != 1 {
		return fmt.Errorf("%w: epic PVF entry contract", dnfdungeon.ErrEntryItemConsumeInvalid)
	}
	r := d.Metadata.RequiredItems[0]
	if r.ItemID != 3330 || r.Count != count || !r.ConsumeOnEntry {
		return fmt.Errorf("%w: epic PVF ticket contract", dnfdungeon.ErrEntryItemConsumeInvalid)
	}
	return nil
}
func (s *Service) freezeCurrentEpicEntry(session *gameSession, runtime *runtimeDungeonState) error {
	if !s.currentEpicEntryEnabled(runtime.Dungeon.ID) {
		return nil
	}
	if err := validateCurrentEpicEntryContract(runtime.Dungeon); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), createWriteTimeout)
	defer cancel()
	var alternatives [][]dnfdungeon.EntryItemRequirement
	var accountDailyLimit int64
	var err error
	if runtime.Dungeon.ID == dnfdungeon.SilentForestDungeonID {
		accountDailyLimit, err = s.currentSilentForestEntryLimit()
	} else {
		alternatives, err = s.currentEpicEntryAlternatives(ctx, runtime.Dungeon.ID)
	}
	if err != nil {
		return err
	}
	nonce, err := cryptorand.Int(cryptorand.Reader, big.NewInt(math.MaxInt64))
	if err != nil {
		return err
	}
	if nonce.Sign() == 0 {
		return dnfdungeon.ErrEntryItemConsumeInvalid
	}
	var campaign *onlineevent.EpicCampaign
	if s.currentEpicBenefitsEnabled() && runtime.Dungeon.ID != dnfdungeon.SilentForestDungeonID {
		c, e := s.currentEpicCampaignForEntry(session, runtime)
		if e != nil {
			return e
		}
		campaign = &c
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if strconv.Itoa(int(session.selectedCharacterID)) != runtime.Character.CharacterID || runtime.Character.AccountID != runtime.AccountID || s.gameSessions[session.selectedCharacterID] != session {
		return errDungeonAccountMismatch
	}
	runtime.epicEntry = &currentEpicEntry{characterID: session.selectedCharacterID, generation: session.characterGeneration, rawAccountID: strings.TrimSpace(session.accountID), command: dnfdungeon.EpicEntryCommand{CharacterID: runtime.Character.CharacterID, AccountID: runtime.AccountID, DungeonID: runtime.Dungeon.ID, ExpectedSequence: runtime.Character.Stats[dnfdungeon.EpicEntrySequenceKey], Nonce: nonce.Int64(), Campaign: campaign, Alternatives: alternatives}}
	runtime.epicEntry.accountDailyLimit = accountDailyLimit
	return nil
}

// Caller holds dungeon.mu. Reserve the exact canonical binding across physical
// commit and runtime installation; do not hold s.mu across the asset UOW.
func (s *Service) commitCurrentEpicEntryLocked(session *gameSession, runtime *runtimeDungeonState) error {
	entry := runtime.epicEntry
	if entry == nil || runtime.lifecycleToken != 0 || runtime.Session.Snapshot().Run.Status != worldmap.DungeonRunActive {
		return dnfdungeon.ErrEntryItemConsumeInvalid
	}
	if err := validateCurrentEpicEntryContract(runtime.Dungeon); err != nil {
		return err
	}
	if old := session.dungeon.runtime; old != nil && old.Session != nil && old.Session.Snapshot().Run.Status == worldmap.DungeonRunActive {
		return errDungeonAlreadyActive
	}
	scene, ok := runtime.Session.Scene()
	if !ok {
		return errDungeonWorldMapUnavailable
	}
	repos, ok := s.repositoryGroup()
	if !ok || repos.CharacterAssets == nil {
		return dnfdungeon.ErrEntryItemConsumeInvalid
	}
	owner, err := dnfdungeon.NewOwner(repos)
	if err != nil {
		return err
	}
	parent := session.ctx
	if parent == nil {
		parent = context.Background()
	}
	ctx, cancel := context.WithTimeout(parent, createWriteTimeout)
	defer cancel()
	cmd := entry.command
	cmd.UpdatedAt = s.gameplayNow()
	cmd.Project = func(slot int16, stack dnfrepo.ItemStack) (dnfrepo.ItemStack, error) {
		item := currentItemListEntryFromStack(dnfrepo.MainInventoryListType, slot, stack)
		stack.RawEntry = append([]byte(nil), item.data[:]...)
		return stack, nil
	}
	reservation, err := s.reserveCurrentCharacterAssets(ctx, boundGameSessionCharacter{session: session, character: entry.characterID, generation: entry.generation}, entry.rawAccountID)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return dnfdungeon.ErrEntryItemConsumeInvalid
	}
	defer s.releaseCurrentCharacterAssets(reservation)
	var result dnfdungeon.EpicEntryResult
	var old *runtimeDungeonState
	err = func() error {
		cmd.Validate = func() error {
			s.mu.Lock()
			defer s.mu.Unlock()
			if !s.currentCharacterAssetReservationValidLocked(reservation) {
				return dnfdungeon.ErrEntryItemConsumeInvalid
			}
			if cmd.Campaign != nil && !s.currentEpicBenefitsEnabled() {
				return dnfdungeon.ErrEntryItemConsumeInvalid
			}
			if err := ctx.Err(); err != nil {
				return err
			}
			if session.selectedCharacterID != entry.characterID || session.characterGeneration != entry.generation || strings.TrimSpace(session.accountID) != entry.rawAccountID || runtime.Character.CharacterID != cmd.CharacterID || runtime.AccountID != cmd.AccountID || runtime.Character.AccountID != cmd.AccountID || runtime.Dungeon.ID != cmd.DungeonID || runtime.Session.Snapshot().Run.DungeonID != cmd.DungeonID || runtime.Request.DungeonID != uint32(cmd.DungeonID) {
				return dnfdungeon.ErrEntryItemConsumeInvalid
			}
			if canonical := s.gameSessions[entry.characterID]; canonical != session {
				return dnfdungeon.ErrEntryItemConsumeInvalid
			}
			return nil
		}
		if err = cmd.Validate(); err != nil {
			return err
		}
		if cmd.DungeonID == dnfdungeon.SilentForestDungeonID {
			result, err = owner.ConsumeSilentForestEntry(ctx, cmd, entry.accountDailyLimit)
		} else {
			result, err = owner.ConsumeEpicEntry(ctx, cmd)
		}
		if err != nil {
			return err
		}
		// Canonical writers wait for this reservation until the paid runtime is installed.
		s.mu.Lock()
		defer s.mu.Unlock()
		old = session.dungeon.runtime
		session.dungeon.runToken = nextCurrentDungeonDeathGeneration(session.dungeon.runToken)
		runtime.lifecycleToken = session.dungeon.runToken
		runtime.ridableReportChannelID = session.channel.ID
		runtime.ridableReportChannelType = session.channel.Type
		runtime.Character = result.Character
		runtime.fatigueSpent += result.FatigueSpent
		if runtime.fatigueRooms == nil {
			runtime.fatigueRooms = make(map[worldmap.RoomCoordinate]bool)
		}
		runtime.fatigueRooms[scene.Coordinate] = true
		session.dungeon.runtime = runtime
		s.finishCurrentCharacterAssetReservationLocked(reservation)
		return nil
	}()
	if err != nil {
		return err
	}
	// These retirement/projection operations cannot reject the paid admission.
	if old != nil {
		s.cancelCurrentDungeonDeathReturnLocked(session, old, "epic_runtime_replaced_after_admission")
		s.cancelCurrentCrystalContractTimerLocked(session, old, "epic_runtime_replaced_after_admission")
		s.cancelCurrentDungeonCardAutoFlipLocked(session, old, "epic_runtime_replaced_after_admission")
		s.releasePartyDungeonInstance(old, entry.characterID, "epic_runtime_replaced_after_admission")
	}
	s.adoptPartyDungeonInstance(session, runtime, scene)
	attachCurrentSemesterPartyMember(session, runtime)
	var refreshes []alignedcmd.ItemSlotRefresh
	for _, c := range result.Consumed {
		for _, slot := range c.Slots {
			refreshes = append(refreshes, alignedcmd.ItemSlotRefresh{ListType: dnfrepo.MainInventoryListType, SlotIndex: slot.Slot})
		}
	}
	if len(refreshes) > 0 {
		if err = s.sendSelectedIncrementalItemSlotRefreshes(session, "epic-entry-committed", refreshes); err != nil {
			s.logGameEvent(session, "game-epic-entry-refresh-deferred", "error", err)
		}
	}
	if cmd.Campaign != nil {
		if e := s.sendCurrentEpicState(session); e != nil {
			s.logGameEvent(session, "game-epic-state-deferred", "error", e)
		}
	}
	return nil
}
