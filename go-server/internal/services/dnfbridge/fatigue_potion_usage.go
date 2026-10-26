package dnfbridge

import (
	"context"
	"encoding/binary"
	"sort"
	"strconv"
	"strings"
	"time"

	dnffatigue "longheng.io/server/internal/modules/dnf/fatigue"
	dnfpremium "longheng.io/server/internal/modules/dnf/premium"
	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

const (
	currentFatiguePotionUsageMsgID uint16 = 541
	fatiguePotionUsedKeyPrefix            = "fatigue_potion_used_"
	fatiguePotionDayKeyPrefix             = "fatigue_potion_day_"
	maxFatiguePotionUsageRows             = 255
)

type fatiguePotionUsageCatalog interface {
	ResolveItem(uint32) (dungeonDropItemDefinition, error)
}

type fatiguePotionUsageSource interface {
	ReadText(string) (string, error)
}

func hasCurrentFatiguePotionUsageHistory(character dnfrepo.CharacterRecord) bool {
	for key := range character.Stats {
		if strings.HasPrefix(key, fatiguePotionUsedKeyPrefix) || strings.HasPrefix(key, dnffatigue.PotionUsageItemKeyPrefix) {
			return true
		}
	}
	return false
}

func resolveCurrentFatiguePotionUsageDefinition(catalog fatiguePotionUsageCatalog, source fatiguePotionUsageSource, itemID uint32) (dnffatigue.PotionDefinition, bool) {
	if catalog == nil || source == nil || itemID == 0 {
		return dnffatigue.PotionDefinition{}, false
	}
	item, err := catalog.ResolveItem(itemID)
	if err != nil || item.Kind != dungeonDropItemStackable || item.PVFPath == "" {
		return dnffatigue.PotionDefinition{}, false
	}
	text, err := source.ReadText(item.PVFPath)
	if err != nil {
		return dnffatigue.PotionDefinition{}, false
	}
	document, err := dnfpvf.Parse(item.PVFPath, text)
	if err != nil {
		return dnffatigue.PotionDefinition{}, false
	}
	definition, ok := fatiguePotionDefinition(int64(itemID), item.PVFPath, document)
	return definition, ok
}

func fatiguePotionUsageCandidateID(raw string) (uint32, bool) {
	value, err := strconv.ParseUint(strings.TrimSpace(raw), 10, 32)
	if err != nil || value == 0 {
		return 0, false
	}
	return uint32(value), true
}

// buildCurrentFatiguePotionUsageBodies projects the durable group counters into
// class0/541. Item-index keys only restore the item-to-group mapping; they are
// never added to the used count.
func buildCurrentFatiguePotionUsageBodies(
	character dnfrepo.CharacterRecord,
	inventory dnfrepo.InventoryRecord,
	now time.Time,
	catalog fatiguePotionUsageCatalog,
	source fatiguePotionUsageSource,
) ([][]byte, bool) {
	if catalog == nil || source == nil || len(character.Stats) == 0 {
		return nil, false
	}
	indexed := make(map[uint32]struct{})
	legacy := make(map[uint32]struct{})
	historyGroups := make(map[string]struct{})
	for key := range character.Stats {
		switch {
		case strings.HasPrefix(key, dnffatigue.PotionUsageItemKeyPrefix):
			if itemID, ok := fatiguePotionUsageCandidateID(strings.TrimPrefix(key, dnffatigue.PotionUsageItemKeyPrefix)); ok {
				indexed[itemID] = struct{}{}
			}
		case strings.HasPrefix(key, fatiguePotionUsedKeyPrefix):
			group := strings.TrimPrefix(key, fatiguePotionUsedKeyPrefix)
			if group == "" {
				continue
			}
			historyGroups[group] = struct{}{}
			if itemID, ok := fatiguePotionUsageCandidateID(group); ok {
				legacy[itemID] = struct{}{}
			}
		}
	}
	if len(indexed) == 0 && len(historyGroups) == 0 {
		return nil, false
	}

	candidates := make(map[uint32]struct{}, len(indexed)+len(legacy))
	for itemID := range indexed {
		candidates[itemID] = struct{}{}
	}
	for itemID := range legacy {
		candidates[itemID] = struct{}{}
	}
	definitions := make(map[uint32]dnffatigue.PotionDefinition, len(candidates))
	visited := make(map[uint32]bool, len(candidates))
	for itemID := range candidates {
		visited[itemID] = true
		definition, ok := resolveCurrentFatiguePotionUsageDefinition(catalog, source, itemID)
		if !ok {
			continue
		}
		definitions[itemID] = definition
		if _, fromIndex := indexed[itemID]; fromIndex {
			historyGroups[definition.UsageGroup()] = struct{}{}
		}
	}

	// A numeric counter suffix can itself be a cooltime group rather than an
	// item ID. An unremoved inventory item restores that legacy mapping only for
	// groups that already have a counter; inventory alone never creates history.
	for _, stack := range inventory.Slots {
		if stack.ItemID <= 0 || stack.ItemID > int64(^uint32(0)) {
			continue
		}
		itemID := uint32(stack.ItemID)
		if visited[itemID] {
			continue
		}
		visited[itemID] = true
		definition, ok := resolveCurrentFatiguePotionUsageDefinition(catalog, source, itemID)
		if !ok {
			continue
		}
		if _, tracked := historyGroups[definition.UsageGroup()]; !tracked {
			continue
		}
		definitions[itemID] = definition
	}

	itemIDs := make([]uint32, 0, len(definitions))
	for itemID, definition := range definitions {
		if definition.DailyLimit <= 0 {
			continue
		}
		if _, tracked := historyGroups[definition.UsageGroup()]; !tracked {
			continue
		}
		itemIDs = append(itemIDs, itemID)
	}
	if len(itemIDs) == 0 {
		return nil, false
	}
	sort.Slice(itemIDs, func(i, j int) bool { return itemIDs[i] < itemIDs[j] })
	serviceDay := dnfpremium.ServiceDayID(now)
	bodies := make([][]byte, 0, (len(itemIDs)+maxFatiguePotionUsageRows-1)/maxFatiguePotionUsageRows)
	var body []byte
	for _, itemID := range itemIDs {
		if body == nil || body[0] == maxFatiguePotionUsageRows {
			if body != nil {
				bodies = append(bodies, append(body, 0))
			}
			body = make([]byte, 1, 1+maxFatiguePotionUsageRows*5+1)
		}
		group := definitions[itemID].UsageGroup()
		used := int64(0)
		if character.Stats[fatiguePotionDayKeyPrefix+group] == serviceDay {
			used = character.Stats[fatiguePotionUsedKeyPrefix+group]
		}
		if used < 0 {
			used = 0
		}
		if used > maxFatiguePotionUsageRows {
			used = maxFatiguePotionUsageRows
		}
		var row [5]byte
		binary.LittleEndian.PutUint32(row[:4], itemID)
		row[4] = byte(used)
		body = append(body, row[:]...)
		body[0]++
	}
	// Nonempty daily pages update entries without clearing previous pages.
	// Weekly potion counts are not implemented server-side.
	bodies = append(bodies, append(body, 0))
	return bodies, true
}

// Existing fatigue/login/timer entrypoints now project all durable item-use
// counters together so a fatigue refresh cannot erase weekly box counts.
func (s *Service) sendCurrentFatiguePotionUsage(
	_ context.Context,
	session *gameSession,
	character dnfrepo.CharacterRecord,
	inventory dnfrepo.InventoryRecord,
	source string,
) error {
	if !hasCurrentItemUsageHistory(character) {
		return nil
	}
	catalog, err := s.currentPVFItemCatalog()
	if err != nil || catalog == nil {
		s.logGameEvent(session, "game-fatigue-potion-usage-projection-deferred", "source", source, "error", err)
		return nil
	}
	bodies, ok := buildCurrentItemUsageBodies(character, inventory, s.gameplayNow(), catalog, catalog.source)
	if !ok {
		s.logGameEvent(session, "game-fatigue-potion-usage-projection-skipped",
			"char_id", character.CharacterID,
			"source", source,
			"reason", "no_current_item_usage_definition")
		return nil
	}
	for _, body := range bodies {
		if err := s.sendGameUpperRawClass(session, currentFatiguePotionUsageMsgID, body, 0); err != nil {
			return err
		}
		s.logGameEvent(session, "game-fatigue-potion-usage-projected",
			"char_id", character.CharacterID,
			"source", source,
			"daily_count", body[0],
			"weekly_count", body[1+int(body[0])*5],
			"body_len", len(body))
	}
	return nil
}

func (s *Service) sendCurrentFatiguePotionUsageForCharacter(
	ctx context.Context,
	session *gameSession,
	character dnfrepo.CharacterRecord,
	source string,
) error {
	if !hasCurrentItemUsageHistory(character) {
		return nil
	}
	repos, ok := s.repositoryGroup()
	if !ok || repos.Inventory == nil {
		return nil
	}
	inventory, found, err := repos.Inventory.Load(ctx, character.CharacterID)
	if err != nil {
		return err
	}
	if !found {
		inventory = dnfrepo.InventoryRecord{CharacterID: character.CharacterID}
	}
	return s.sendCurrentFatiguePotionUsage(ctx, session, character, inventory, source)
}
