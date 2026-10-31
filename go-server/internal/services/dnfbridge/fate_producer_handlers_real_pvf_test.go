package dnfbridge

import (
	"context"
	"encoding/binary"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/raidcurrency"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	dnfrepomemory "longheng.io/server/internal/modules/dnf/repository/memory"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

const fateProducerHandlersPVFSHA256 = "94a2549598816198115340f84c0be2d31603ddeea58f70f27afe6f73b25e3df7"

type fateProducerHandlerCase struct {
	itemID        uint32
	stackableType string
	fateCount     uint32
}

// TestRealPVFFateProducerHandlersConsumeAndCreditCurrentPVF exercises the
// client-facing handler selected by each document's actual [stackable type].
// It deliberately does not force every document through the booster handler:
// current [booster] documents use op160, while [upgradable legacy] documents
// use the op27 lottery owner. Each subtest owns a fresh memory repository and
// one source stack, so a source decrement or Fate credit cannot leak between
// cases.
func TestRealPVFFateProducerHandlersConsumeAndCreditCurrentPVF(t *testing.T) {
	path := strings.TrimSpace(os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE"))
	if path == "" {
		t.Skip("set DNFBRIDGE_REAL_PVF_SMOKE to the fixed Script.pvf")
	}
	archive, err := platformpvf.LoadArchive(platformpvf.Options{Path: path})
	if err != nil {
		t.Fatal(err)
	}
	if got := archive.Snapshot().Checksum; !strings.EqualFold(got, fateProducerHandlersPVFSHA256) {
		t.Fatalf("PVF sha256=%s, want audited %s", got, fateProducerHandlersPVFSHA256)
	}
	catalog, err := newPVFDungeonDropCatalog(archive)
	if err != nil {
		t.Fatal(err)
	}

	// These are the eleven stackable documents in PVF94 whose configuration
	// directly names Fate 10304529. 10304528 consumes Fate and is intentionally
	// not part of this producer test.
	tests := []fateProducerHandlerCase{
		{itemID: 10162015, stackableType: "[booster]", fateCount: 30},
		{itemID: 10162016, stackableType: "[booster]", fateCount: 45},
		{itemID: 10303189, stackableType: "[booster]", fateCount: 60},
		{itemID: 10304474, stackableType: "[upgradable legacy]", fateCount: 30},
		{itemID: 10304475, stackableType: "[upgradable legacy]", fateCount: 45},
		{itemID: 10304476, stackableType: "[upgradable legacy]", fateCount: 60},
		{itemID: 10304477, stackableType: "[upgradable legacy]", fateCount: 80},
		{itemID: 10304478, stackableType: "[upgradable legacy]", fateCount: 100},
		{itemID: 10304669, stackableType: "[booster]", fateCount: 80},
		{itemID: 10304674, stackableType: "[booster]", fateCount: 100},
		{itemID: 490008037, stackableType: "[booster]", fateCount: 100},
	}
	if len(tests) != 11 {
		t.Fatalf("Fate producer table has %d entries, want 11", len(tests))
	}
	seen := make(map[uint32]struct{}, len(tests))
	for _, tc := range tests {
		if _, duplicate := seen[tc.itemID]; duplicate {
			t.Fatalf("duplicate Fate producer item=%d", tc.itemID)
		}
		seen[tc.itemID] = struct{}{}
	}

	for _, tc := range tests {
		t.Run(fmt.Sprintf("%d_%s", tc.itemID, strings.Trim(tc.stackableType, "[]")), func(t *testing.T) {
			definition, err := resolveCurrentBoosterDefinition(catalog, tc.itemID, currentBoosterRequestRandom)
			if err != nil {
				t.Fatalf("resolve current producer %d: %v", tc.itemID, err)
			}
			if definition.StackableType != tc.stackableType {
				t.Fatalf("item=%d stackable_type=%q, want %q", tc.itemID, definition.StackableType, tc.stackableType)
			}
			resolvedFate, err := fateProducerGuaranteedFateCount(definition)
			if err != nil {
				t.Fatalf("item=%d Fate reward resolution: %v", tc.itemID, err)
			}
			if resolvedFate != tc.fateCount {
				t.Fatalf("item=%d configured Fate=%d, want audited %d", tc.itemID, resolvedFate, tc.fateCount)
			}

			now := time.Now().UTC()
			group := dnfrepomemory.NewMemoryGroup()
			ctx := context.Background()
			const (
				accountID   = "fate-producer-handler-account"
				characterID = "19"
				sourceSlot  = int16(75)
			)
			if err := group.Account.Save(ctx, dnfrepo.AccountRecord{AccountID: accountID}); err != nil {
				t.Fatal(err)
			}
			if err := group.AccountInventory.Save(ctx, dnfrepo.AccountInventoryRecord{
				AccountID: accountID,
				Slots:     map[string]dnfrepo.ItemStack{},
			}); err != nil {
				t.Fatal(err)
			}
			if err := group.Character.Save(ctx, dnfrepo.CharacterRecord{
				CharacterID: characterID,
				AccountID:   accountID,
				Level:       100,
				Stats:       map[string]int64{"gold": 1_000_000_000},
			}); err != nil {
				t.Fatal(err)
			}
			sourceKey := currentCeraShopInventorySlotKey(dnfrepo.MainInventoryListType, sourceSlot)
			slots := map[string]dnfrepo.ItemStack{
				sourceKey: {
					ItemID:   int64(tc.itemID),
					Count:    2,
					ExpireAt: now.Add(365 * 24 * time.Hour),
				},
			}
			if materialID, materialCount := definition.Random.MaterialItemID, definition.Random.MaterialCountPerUse; materialID > 0 || materialCount > 0 {
				if materialID <= 0 || materialCount <= 0 {
					t.Fatalf("item=%d invalid runtime material contract item=%d count=%d", tc.itemID, materialID, materialCount)
				}
				slots[currentCeraShopInventorySlotKey(dnfrepo.MainInventoryListType, 109)] = dnfrepo.ItemStack{
					ItemID: materialID,
					Count:  materialCount,
				}
			}
			if err := group.Inventory.Save(ctx, dnfrepo.InventoryRecord{CharacterID: characterID, Slots: slots}); err != nil {
				t.Fatal(err)
			}

			service := currentBoosterTestService(catalog, group)
			service.options.currentClientVersion = "official-24.9"
			service.gameplayTimers = &processGameplayTimeQueue{now: func() time.Time { return now }}
			connection := &bufferConn{}
			session := &gameSession{
				conn:                connection,
				connID:              fmt.Sprintf("fate-producer-handler-%d", tc.itemID),
				accountID:           accountID,
				selectedCharacterID: 19,
			}

			var slotBytes [2]byte
			binary.LittleEndian.PutUint16(slotBytes[:], uint16(sourceSlot))
			switch definition.StackableType {
			case "[booster]":
				if err := service.handleCurrentBoosterItem(session, slotBytes[:]); err != nil {
					t.Fatalf("actual op160 booster handler: %v", err)
				}
			case "[upgradable legacy]":
				body := []byte{0, 0, byte(sourceSlot), 0}
				if err := service.handleCurrentLotteryItem(session, body); err != nil {
					t.Fatalf("actual op27 lottery handler: %v", err)
				}
			default:
				t.Fatalf("item=%d has unsupported producer handler type %q", tc.itemID, definition.StackableType)
			}

			inventory, found, err := group.Inventory.Load(ctx, characterID)
			if err != nil || !found {
				t.Fatalf("load inventory found=%t err=%v", found, err)
			}
			source, sourceFound := inventory.Slots[sourceKey]
			if !sourceFound || source.ItemID != int64(tc.itemID) || source.Count != 1 {
				t.Fatalf("item=%d source after one handler=%+v found=%t, want same item count=1", tc.itemID, source, sourceFound)
			}
			for key, stack := range inventory.Slots {
				if stack.ItemID == int64(raidcurrency.FateItemID) {
					t.Fatalf("item=%d created a list-0 Fate row key=%s stack=%+v", tc.itemID, key, stack)
				}
			}

			account, found, err := group.Account.Load(ctx, accountID)
			if err != nil || !found {
				t.Fatalf("load account found=%t err=%v", found, err)
			}
			state, err := raidcurrency.Read(account, now)
			if err != nil || state.Balance != tc.fateCount || state.Earned != tc.fateCount {
				t.Fatalf("item=%d account Fate state=%+v err=%v, want balance/earned=%d/%d",
					tc.itemID, state, err, tc.fateCount, tc.fateCount)
			}

			frames := splitAllGameServerUpperPackets(t, connection.write.Bytes())
			list44Frames, weeklyFrames := 0, 0
			sawCommittedBalance := false
			for _, frame := range frames {
				if fateProducerListPacketContainsFate0(frame.Header.MsgID, frame.Body) {
					t.Fatalf("item=%d emitted Fate in list0 msg=%d body=%x", tc.itemID, frame.Header.MsgID, frame.Body)
				}
				if frame.Header.MsgID == 14 && len(frame.Body) >= 3 && frame.Body[0] == currentRaidCurrencyListType {
					list44Frames++
					if len(frame.Body) == 3+currentItemListEntryWireSize && binary.LittleEndian.Uint16(frame.Body[1:3]) == 1 {
						row := frame.Body[3:]
						sawCommittedBalance = binary.LittleEndian.Uint32(row[2:6]) == raidcurrency.FateItemID &&
							binary.LittleEndian.Uint32(row[6:10]) == tc.fateCount
					}
				}
				if frame.Header.MsgID == 1456 {
					weeklyFrames++
					if len(frame.Body) != 14 || binary.LittleEndian.Uint16(frame.Body[:2]) != 1 ||
						binary.LittleEndian.Uint32(frame.Body[2:6]) != raidcurrency.FateItemID ||
						binary.LittleEndian.Uint32(frame.Body[6:10]) != tc.fateCount ||
						binary.LittleEndian.Uint32(frame.Body[10:14]) != raidcurrency.WeeklyLimit {
						t.Fatalf("item=%d weekly Fate body=%x", tc.itemID, frame.Body)
					}
				}
			}
			if list44Frames != 1 || !sawCommittedBalance {
				t.Fatalf("item=%d committed list44 frames=%d balance=%t, want exactly one balance row carrying %d",
					tc.itemID, list44Frames, sawCommittedBalance, tc.fateCount)
			}
			if weeklyFrames != 1 {
				t.Fatalf("item=%d committed weekly frames=%d, want exactly one 1456 carrying earned=%d",
					tc.itemID, weeklyFrames, tc.fateCount)
			}
		})
	}
}

func fateProducerGuaranteedFateCount(definition currentBoosterDefinition) (uint32, error) {
	if len(definition.Random.Groups) == 0 {
		return 0, fmt.Errorf("no reward groups")
	}
	var total uint64
	for groupIndex, group := range definition.Random.Groups {
		draws := group.DrawCount
		if draws < 1 {
			draws = 1
		}
		if len(group.Entries) == 0 {
			return 0, fmt.Errorf("group=%d has no entries", groupIndex)
		}
		// Several real boosters have a guaranteed Fate group followed by a
		// separate random ordinary-reward group. Keep those real groups in the
		// handler; only groups that can award Fate affect this account assertion.
		hasFate := false
		for _, entry := range group.Entries {
			if entry.ItemID == int64(raidcurrency.FateItemID) {
				hasFate = true
			}
		}
		if !hasFate {
			continue
		}
		var expectedCount int64
		for entryIndex, entry := range group.Entries {
			if entry.ItemID != int64(raidcurrency.FateItemID) || entry.Weight <= 0 || entry.Count <= 0 {
				return 0, fmt.Errorf("group=%d entry=%d is not a positive Fate row: %+v", groupIndex, entryIndex, entry)
			}
			if expectedCount == 0 {
				expectedCount = entry.Count
			} else if entry.Count != expectedCount {
				return 0, fmt.Errorf("group=%d entry=%d Fate count=%d, variable count base=%d", groupIndex, entryIndex, entry.Count, expectedCount)
			}
		}
		total += uint64(draws) * uint64(expectedCount)
	}
	if total == 0 || total > uint64(^uint32(0)) {
		return 0, fmt.Errorf("Fate total=%d is invalid", total)
	}
	return uint32(total), nil
}

func fateProducerListPacketContainsFate0(msgID uint16, body []byte) bool {
	if len(body) == 0 || body[0] != dnfrepo.MainInventoryListType {
		return false
	}
	var rowCount int
	var rowOffset int
	switch msgID {
	case 13:
		if len(body) < 5 {
			return false
		}
		rowCount = int(binary.LittleEndian.Uint16(body[3:5]))
		rowOffset = 5
	case 14:
		if len(body) < 3 {
			return false
		}
		rowCount = int(binary.LittleEndian.Uint16(body[1:3]))
		rowOffset = 3
	default:
		return false
	}
	if rowCount <= 0 || rowCount > (len(body)-rowOffset)/currentItemListEntryWireSize {
		return false
	}
	for index := 0; index < rowCount; index++ {
		row := body[rowOffset+index*currentItemListEntryWireSize : rowOffset+(index+1)*currentItemListEntryWireSize]
		if binary.LittleEndian.Uint32(row[2:6]) == raidcurrency.FateItemID {
			return true
		}
	}
	return false
}
