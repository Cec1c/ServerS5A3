package dnfbridge

import (
	"fmt"
	"math"
	"strings"

	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
	"longheng.io/server/internal/modules/dnf/worldmap"
)

type currentPassiveItemChoice struct {
	item   uint32
	weight int
}
type currentPassiveItemPlan struct {
	objectIndex int
	itemID      uint32
}

// The DGN owns weighted item groups; MAP [item] children refer to a group and
// specify the number of draws. Never assign an item to an object without these
// two PVF records (in particular, an ordinary hell pillar has no [item] child).
func currentDungeonPVFPassiveItemPlan(dungeon worldmap.Dungeon, scene worldmap.DungeonRoomScene, seed uint32) ([]currentPassiveItemPlan, error) {
	hasItems := false
	for _, object := range scene.SpecialPassiveObjects {
		for _, spawn := range object.Spawns {
			if strings.Trim(strings.ToLower(spawn.Kind), "[] ") == "item" && spawn.Level > 0 {
				hasItems = true
			}
		}
	}
	if !hasItems {
		return nil, nil
	}
	groups := make(map[int64][]currentPassiveItemChoice)
	for _, section := range dungeon.SourceSections {
		if strings.Trim(strings.ToLower(section.Name), "[] ") != "special passive object item" {
			continue
		}
		values := make([]int64, 0, len(section.Tokens))
		for _, token := range section.Tokens {
			if token.Kind != dnfpvf.TokenInt {
				return nil, fmt.Errorf("invalid passive item group token")
			}
			values = append(values, token.Int)
		}
		for len(values) > 0 {
			if len(values) < 3 || values[0] < 0 || values[1] < -1 || values[2] < 0 || values[2] > int64((len(values)-3)/2) {
				return nil, fmt.Errorf("invalid passive item group header")
			}
			id, count := values[0], int(values[2])
			values = values[3:]
			if _, duplicate := groups[id]; duplicate {
				return nil, fmt.Errorf("duplicate passive item group %d", id)
			}
			choices := make([]currentPassiveItemChoice, 0, count)
			total := int64(0)
			for i := 0; i < count; i++ {
				item, weight := values[2*i], values[2*i+1]
				if item <= 0 || item > math.MaxUint32 || weight < 0 || weight > math.MaxInt32-total {
					return nil, fmt.Errorf("invalid passive item choice")
				}
				total += weight
				choices = append(choices, currentPassiveItemChoice{uint32(item), int(weight)})
			}
			groups[id] = choices
			values = values[count*2:]
		}
	}
	rng := newCurrentDungeonDropLCG(seed)
	var plan []currentPassiveItemPlan
	for index, object := range scene.SpecialPassiveObjects {
		for _, spawn := range object.Spawns {
			if strings.Trim(strings.ToLower(spawn.Kind), "[] ") != "item" || spawn.Level <= 0 {
				continue
			}
			if index > math.MaxUint8 || spawn.Level > math.MaxUint8 {
				return nil, errDungeonStartMapExtraCount
			}
			choices := groups[spawn.Code]
			total := 0
			for _, choice := range choices {
				total += choice.weight
			}
			if total == 0 {
				continue
			}
			for n := int64(0); n < spawn.Level; n++ {
				pick := rng.Next(total)
				for _, choice := range choices {
					if pick < choice.weight {
						plan = append(plan, currentPassiveItemPlan{index, choice.item})
						break
					}
					pick -= choice.weight
				}
			}
			if len(plan) > math.MaxUint8 {
				return nil, errDungeonStartMapExtraCount
			}
		}
	}
	return plan, nil
}

func (s *Service) prepareCurrentDungeonPVFPassiveItemDrops(session *gameSession, runtime *runtimeDungeonState, scene worldmap.DungeonRoomScene, source string) error {
	room := runtimeDungeonRoomKeyFromScene(scene)
	if runtime.passiveItemDropsPlanned[room] {
		return nil
	}
	actor := uint16(0)
	if session != nil {
		actor = currentSceneActorObjectKey(session.selectedCharacterID)
	}
	// Each member has a separate stable stream and ledger. Rebuilding START_MAP
	// cannot roll another reward, and does not advance the combat drop stream.
	seed := runtime.Seed ^ uint32(actor)*0x9e3779b9 ^ uint32(scene.Map.Map.ID)
	plan, err := currentDungeonPVFPassiveItemPlan(runtime.Dungeon, scene, seed)
	if err != nil {
		return err
	}
	if len(plan) == 0 {
		return nil
	}
	if actor == 0 {
		return errDungeonCharacterRequired
	}
	catalog, err := s.dungeonMonsterCatalog()
	if err != nil {
		return err
	}
	drops, err := catalog.DropCatalog()
	if err != nil {
		return err
	}
	definitions := make([]dungeonDropItemDefinition, len(plan))
	for i, row := range plan {
		definitions[i], err = drops.ResolveItem(row.itemID)
		if err != nil {
			return err
		}
	}
	instance := runtime.lockPartyDungeonInstance()
	defer unlockPartyDungeonInstance(instance)
	owner := runtime.DropOwner
	if shared := partySharedDungeonDropOwnerLocked(runtime); shared != nil {
		owner = shared
	}
	if owner == nil {
		owner = newRuntimeDungeonDropOwner()
	}
	nextKey := runtime.NextObjectKey
	if instance != nil {
		nextKey = instance.nextObjectKey
	}
	nextSlot := owner.nextSceneSlot
	if nextKey == 0 || nextKey+uint32(len(plan)) > math.MaxUint16 || nextSlot == 0 || int(nextSlot)+len(plan) > math.MaxUint16 {
		return errDungeonDropObjectKeyRange
	}
	batch := make([]*runtimeDungeonDrop, 0, len(plan))
	rows := make([]*currentDungeonPassiveObjectDropRow, 0, len(plan))
	for i, row := range plan {
		drop := &runtimeDungeonDrop{ObjectKey: nextKey + uint32(i), SceneSlot: nextSlot + uint16(i), Item: definitions[i], Amount: 1, OwnerActorObjectKey: actor, Room: room, Status: runtimeDungeonDropAvailable, MapOwnedPassiveObject: true, MapOwnedPassiveIndex: row.objectIndex}
		batch = append(batch, drop)
		rows = append(rows, &currentDungeonPassiveObjectDropRow{NativeObjectKey: true, ObjectIndex: row.objectIndex, SceneSlot: drop.SceneSlot, ObjectKey: drop.ObjectKey, ItemID: row.itemID, StackCount: 1})
	}
	if err := owner.registerBatch(batch, nextSlot+uint16(len(batch))); err != nil {
		return err
	}
	runtime.DropOwner = owner
	runtime.commitDungeonNextObjectKeyLocked(nextKey + uint32(len(batch)))
	registration := runtime.registerCurrentDungeonMapOwnedPassiveObjects(scene)
	registration.Rows = append(registration.Rows, rows...)
	// Current EXE RVA 194833E..19483A8 reads u8, THREE u32, u16, u8,
	// u16, u16, u8: 21 bytes. This is not the older C# 19-byte layout.
	registration.VendorWidth = false
	if runtime.passiveItemDropsPlanned == nil {
		runtime.passiveItemDropsPlanned = make(map[runtimeDungeonRoomKey]bool)
	}
	runtime.passiveItemDropsPlanned[room] = true
	s.logGameEvent(session, "game-dungeon-pvf-passive-item-drops", "source", source, "map_id", scene.Map.Map.ID, "owner_actor", actor, "rows", len(rows), "row_bytes", 21)
	return nil
}
