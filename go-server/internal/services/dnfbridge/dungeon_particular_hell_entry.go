package dnfbridge

import (
	"context"
	"encoding/binary"
	"fmt"
	"strconv"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
	dnfdungeon "longheng.io/server/internal/modules/dnf/dungeon"
	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/worldmap"
)

const currentRiftDungeonID int64 = 316

// Explicit server rule requested by the operator; PVF originally permits unlimited entry.
const currentRiftDailyLimit int64 = 3
const currentParticularHellInfoMsgID uint16 = 829
const currentParticularHellDungeonMsgID uint16 = 830

func (s *Service) currentRiftEntryEnabled(dungeonID int64) bool {
	return s != nil && s.options.currentClientVersion == "official-24.9" && dungeonID == currentRiftDungeonID
}

func (s *Service) currentRiftRequiredItemWeight() (float64, error) {
	s.initialEquipmentMu.Lock()
	archive, err := s.initialEquipmentArchiveLocked()
	s.initialEquipmentMu.Unlock()
	if err != nil {
		return 0, err
	}
	if archive == nil {
		return 0, fmt.Errorf("particular hell PVF unavailable")
	}
	text, _, err := readInitialPVFText(archive, "etc/particularhellparty.etc")
	if err != nil {
		return 0, err
	}
	doc, err := dnfpvf.Parse("etc/particularhellparty.etc", text)
	if err != nil {
		return 0, err
	}
	weight, ok := doc.Number("required item weight")
	if !ok {
		return 0, fmt.Errorf("particular hell required item weight missing")
	}
	return weight, nil
}

func (s *Service) currentRiftFreePasses(dungeonID int64) ([]dnfdungeon.EntryItemRequirement, error) {
	table, _, err := s.dungeonWorldMap()
	if err != nil {
		return nil, err
	}
	area, found := table.FindAreaByDungeon(dungeonID)
	if !found {
		return nil, fmt.Errorf("rift PVF area unavailable")
	}
	passes := make([]dnfdungeon.EntryItemRequirement, 0, len(area.ParticularHellFreePassItems))
	for _, item := range area.ParticularHellFreePassItems {
		if item.ItemID <= 0 || item.Count <= 0 {
			return nil, dnfdungeon.ErrEntryItemConsumeInvalid
		}
		passes = append(passes, dnfdungeon.EntryItemRequirement{ItemID: item.ItemID, Count: item.Count, ConsumeOnEntry: true})
	}
	return passes, nil
}

func (s *Service) validateCurrentRiftEntry(ctx context.Context, character dnfrepo.CharacterRecord, dungeon worldmap.Dungeon, session *gameSession) error {
	if err := validateDungeonEntry(character, dungeon, session); err != nil {
		return err
	}
	used, err := dnfdungeon.DailyEntryUsed(character, dungeon.ID, s.gameplayNow())
	if err != nil {
		return err
	}
	if used >= currentRiftDailyLimit {
		return dnfdungeon.ErrDailyEntryLimit
	}
	weight, err := s.currentRiftRequiredItemWeight()
	if err != nil {
		return err
	}
	base := currentTournamentEntryItems(dungeon)
	if len(base) == 0 {
		return fmt.Errorf("rift PVF entry tickets missing")
	}
	passes, err := s.currentRiftFreePasses(dungeon.ID)
	if err != nil {
		return err
	}
	alternatives, err := dnfdungeon.DailyEntryAlternatives(base, passes, used, weight)
	if err != nil {
		return err
	}
	counts, err := s.currentCharacterItemCounts(ctx, sessionSelectedCharacterID(session))
	if err != nil {
		return err
	}
	for _, alternative := range alternatives {
		valid := true
		for _, item := range alternative {
			if counts[item.ItemID] < item.Count {
				valid = false
				break
			}
		}
		if valid {
			return nil
		}
	}
	return dnfdungeon.ErrEntryRequiredItemMissing
}

// Called only after the entire runtime is prepared and its active-run guard is
// held. Retransmitted SELECT_DUNGEON cannot charge another entry on an active run.
func (s *Service) consumeCurrentRiftEntry(session *gameSession, runtime *runtimeDungeonState) error {
	if !s.currentRiftEntryEnabled(runtime.Dungeon.ID) {
		return nil
	}
	repos, ok := s.repositoryGroup()
	if !ok || repos.CharacterAssets == nil {
		return dnfdungeon.ErrOwnerUnavailable
	}
	weight, err := s.currentRiftRequiredItemWeight()
	if err != nil {
		return err
	}
	passes, err := s.currentRiftFreePasses(runtime.Dungeon.ID)
	if err != nil {
		return err
	}
	owner, err := dnfdungeon.NewOwner(repos)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), createWriteTimeout)
	defer cancel()
	result, err := owner.ConsumeDailyEntry(ctx, dnfdungeon.DailyEntryCommand{
		CharacterID: strconv.Itoa(int(session.selectedCharacterID)), DungeonID: runtime.Dungeon.ID, Limit: currentRiftDailyLimit,
		RequiredItemWeight: weight, Requirements: currentTournamentEntryItems(runtime.Dungeon), FreePasses: passes, UpdatedAt: s.gameplayNow(),
		Project: func(slot int16, stack dnfrepo.ItemStack) (dnfrepo.ItemStack, error) {
			entry := currentItemListEntryFromStack(dnfrepo.MainInventoryListType, slot, stack)
			stack.RawEntry = append([]byte(nil), entry.data[:]...)
			return stack, nil
		},
	})
	if err != nil {
		return err
	}
	runtime.Character = result.Character
	refreshes := []alignedcmd.ItemSlotRefresh{}
	for _, item := range result.Consumed {
		for _, slot := range item.Slots {
			refreshes = append(refreshes, alignedcmd.ItemSlotRefresh{ListType: dnfrepo.MainInventoryListType, SlotIndex: slot.Slot})
		}
	}
	if err := s.sendSelectedIncrementalItemSlotRefreshes(session, "particular_hell_entry", refreshes); err != nil {
		s.logGameEvent(session, "game-rift-entry-item-refresh-deferred", "error", err)
	}
	body := make([]byte, 8)
	binary.LittleEndian.PutUint32(body, uint32(runtime.Dungeon.ID))
	binary.LittleEndian.PutUint32(body[4:], uint32(result.Used))
	if err := s.sendGameUpperRawClass(session, currentParticularHellDungeonMsgID, body, 0); err != nil {
		s.logGameEvent(session, "game-rift-entry-counter-refresh-deferred", "error", err)
	}
	s.logGameEvent(session, "game-rift-daily-entry-committed", "char_id", session.selectedCharacterID, "dungeon_id", runtime.Dungeon.ID, "used", result.Used, "limit", currentRiftDailyLimit, "reset", "06:00_UTC+8", "consumed", result.Consumed)
	return nil
}

// Native NOTI 829 (203B5A0) reads u32 limit, 260 raw bytes: one ignored
// u32 followed by 32 (u32 dungeon, u32 used) pairs. NOTI 830 updates one pair.
// The first raw word stays zero; it is never interpreted by this handler.
func buildCurrentParticularHellInfoBody(used uint32) []byte {
	body := make([]byte, 264)
	binary.LittleEndian.PutUint32(body, uint32(currentRiftDailyLimit))
	binary.LittleEndian.PutUint32(body[8:], uint32(currentRiftDungeonID))
	binary.LittleEndian.PutUint32(body[12:], used)
	return body
}

func (s *Service) sendCurrentParticularHellInfo(session *gameSession, character dnfrepo.CharacterRecord) error {
	if !s.currentRiftEntryEnabled(currentRiftDungeonID) {
		return nil
	}
	used, err := dnfdungeon.DailyEntryUsed(character, currentRiftDungeonID, s.gameplayNow())
	if err != nil {
		return err
	}
	if used > currentRiftDailyLimit {
		used = currentRiftDailyLimit
	}
	return s.sendGameUpperRawClass(session, currentParticularHellInfoMsgID, buildCurrentParticularHellInfoBody(uint32(used)), 0)
}
