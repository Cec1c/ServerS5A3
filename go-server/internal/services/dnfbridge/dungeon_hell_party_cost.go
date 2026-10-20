package dnfbridge

import (
	"context"
	"fmt"
	"strconv"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
	dnfdungeon "longheng.io/server/internal/modules/dnf/dungeon"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/worldmap"
)

// Matches WorldMap.GetHellNormalTicketNeedCount and
// DungeonEntryAdmissionApplicationService.TryAddHellTicketAlternatives.
func currentHellPartyTicketAlternatives(area worldmap.Area, dungeon worldmap.Dungeon) ([][]dnfdungeon.EntryItemRequirement, error) {
	if !area.HellDungeon.Set || area.HellDungeon.Value != 1 || !dungeon.Metadata.MinimumRequiredLevel.Set || dungeon.Metadata.MinimumRequiredLevel.Value <= 44 || dungeon.Metadata.MinimumRequiredLevel.Value > 1000 {
		return nil, fmt.Errorf("hell ticket PVF definition unavailable")
	}
	count := (40*dungeon.Metadata.MinimumRequiredLevel.Value-1800)/100 + 10
	passes := area.HellFreePassItems
	if dungeon.Metadata.Flags["inferno dungeon"] {
		passes = area.InfernoFreePassItems
		count = dungeon.Metadata.IntegerValues["hell party required item count"]
		if dungeon.Metadata.IntegerValues["hell dungeon"] != 1 || count <= 0 || count > 100000 {
			return nil, fmt.Errorf("inferno ticket PVF definition unavailable")
		}
	}
	var alternatives [][]dnfdungeon.EntryItemRequirement
	for _, pass := range passes {
		if pass.ItemID > 0 && pass.Count > 0 {
			alternatives = append(alternatives, []dnfdungeon.EntryItemRequirement{{ItemID: pass.ItemID, Count: pass.Count, ConsumeOnEntry: true}})
		}
	}
	normal := 0
	for _, id := range area.ItemConditions {
		if id > 0 {
			alternatives = append(alternatives, []dnfdungeon.EntryItemRequirement{{ItemID: id, Count: count, ConsumeOnEntry: true}})
			normal++
		}
	}
	if normal == 0 {
		return nil, fmt.Errorf("normal hell ticket PVF definition missing")
	}
	return alternatives, nil
}

// Runs after every member's topology and entry packet preflight and before any
// member runtime is published. The active-run replay returns before this point.
func currentHellEntryRequiresTickets(runtime *runtimeDungeonState) bool {
	return runtime != nil && (runtime.HellRoomSet || runtime.Dungeon.Metadata.Flags["inferno dungeon"])
}

func (s *Service) consumeCurrentHellPartyGroupEntry(ctx context.Context, leader *gameSession, runtime *runtimeDungeonState, followers []*runtimePartyPreparedDungeonEntry) error {
	if s.options.currentClientVersion != "official-24.9" || !currentHellEntryRequiresTickets(runtime) || runtime.hellEntryItemsCommitted {
		return nil
	}
	table, _, err := s.dungeonWorldMap()
	if err != nil {
		return err
	}
	area, found := table.FindAreaByDungeon(runtime.Dungeon.ID)
	if !found {
		return fmt.Errorf("hell ticket area unavailable")
	}
	alternatives, err := currentHellPartyTicketAlternatives(area, runtime.Dungeon)
	if err != nil {
		return err
	}
	sessions := []*gameSession{leader}
	runtimes := []*runtimeDungeonState{runtime}
	for _, member := range followers {
		if member == nil || member.session == nil || !currentHellEntryRequiresTickets(member.runtime) || member.runtime.Dungeon.ID != runtime.Dungeon.ID || member.runtime.hellEntryItemsCommitted {
			return fmt.Errorf("hell party entry member state mismatch")
		}
		sessions = append(sessions, member.session)
		runtimes = append(runtimes, member.runtime)
	}
	cmd := dnfdungeon.EntryItemGroupCommand{UpdatedAt: s.gameplayNow(), Project: func(slot int16, stack dnfrepo.ItemStack) (dnfrepo.ItemStack, error) {
		entry := currentItemListEntryFromStack(dnfrepo.MainInventoryListType, slot, stack)
		stack.RawEntry = append([]byte(nil), entry.data[:]...)
		return stack, nil
	}}
	for _, session := range sessions {
		cmd.Members = append(cmd.Members, dnfdungeon.EntryItemGroupMember{CharacterID: strconv.Itoa(int(selectedCharacterID(session))), AccountID: s.accountIDForSession(session), Alternatives: alternatives})
	}
	repos, ok := s.repositoryGroup()
	if !ok {
		return dnfdungeon.ErrOwnerUnavailable
	}
	results, err := dnfdungeon.ConsumeEntryItemGroup(ctx, repos, cmd)
	if err != nil {
		return err
	}
	for index, session := range sessions {
		runtimes[index].hellEntryItemsCommitted = true
		result := results[strconv.Itoa(int(selectedCharacterID(session)))]
		var refreshes []alignedcmd.ItemSlotRefresh
		for _, item := range result.Consumed {
			for _, slot := range item.Slots {
				refreshes = append(refreshes, alignedcmd.ItemSlotRefresh{ListType: dnfrepo.MainInventoryListType, SlotIndex: slot.Slot})
			}
		}
		if err := s.sendSelectedIncrementalItemSlotRefreshes(session, "hell_party_entry_group", refreshes); err != nil {
			s.logGameEvent(session, "game-hell-party-ticket-refresh-deferred", "error", err)
		}
		s.logGameEvent(session, "game-hell-party-ticket-consumed", "char_id", selectedCharacterID(session), "dungeon_id", runtime.Dungeon.ID, "consumed", result.Consumed, "transaction", "all_members")
	}
	return nil
}
