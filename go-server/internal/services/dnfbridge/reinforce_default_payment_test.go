package dnfbridge

import (
	"context"
	"encoding/binary"
	"fmt"
	"os"
	"strconv"
	"testing"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
	"longheng.io/server/internal/modules/dnf/dnfenum"
	"longheng.io/server/internal/modules/dnf/inventory"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	memory "longheng.io/server/internal/modules/dnf/repository/memory"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

// TestRealPVFReinforceTwelveFailurePayment verifies the assets, persisted
// inventory, ACK, and op14 updates written after a real +12 to +13
// reinforcement failure. The test derives the failure outcome, protection
// parameters, material, gold, and resulting levels from the real fixture and
// runtime PVF instead of assuming one server rule for every item or ticket.
func TestRealPVFReinforceTwelveFailurePayment(t *testing.T) {
	path := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if path == "" {
		t.Skip("set DNFBRIDGE_REAL_PVF_SMOKE to the current online PVF")
	}
	archive, err := platformpvf.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := newPVFDungeonDropCatalog(archive)
	if err != nil {
		t.Fatal(err)
	}
	service := &Service{initialEquipmentArchive: archive, pvfItemCatalog: catalog}
	basePolicyResolver := service.alignedUpgradePolicyResolver()
	protect, err := service.alignedUpgradeProtectTicketResolverForCommand(dnfenum.CmdPacketUpgradeItem)
	if err != nil {
		t.Fatal(err)
	}
	const protectTicketItemID = int64(10093371)
	_, expectedProtectRetain := realPVFActionTypeRetainLevel(t, archive, catalog, protectTicketItemID)
	protectTicket, err := protect(protectTicketItemID)
	if err != nil {
		t.Fatal(err)
	}
	if protectTicket.Mode != "reinforce" || protectTicket.UnitCost <= 0 || protectTicket.PVFPath == "" {
		t.Fatalf("real protection-ticket metadata=%+v is not a usable reinforce ticket", protectTicket)
	}

	// Empty is the production default and installs the authoritative gold quote.
	// nil session and an absent NPC trailer exercise the no-NPC-context quote.
	t.Setenv("DNFBRIDGE_NATIVE_UPGRADE_COST", "")
	t.Setenv(inventory.UpgradeProtectTicketEnv, inventory.UpgradeProtectTicketConsumeFirstMatch)
	defaultCostResolver := service.candidateUpgradeCostResolver(nil)
	if defaultCostResolver == nil {
		t.Fatal("default candidateUpgradeCostResolver(nil) is nil")
	}

	const (
		accountID       = "reinforce-twelve-payment"
		targetSlot      = int16(9)
		materialSlot    = int16(358)
		materialItemID  = int64(3037)
		currentLevel    = 12
		itemListRowSize = 0x77
	)

	type upgradeCase struct {
		name      string
		itemID    int64
		protected bool
	}
	for _, test := range []upgradeCase{
		{name: "101020667/no-protection", itemID: 101020667},
		{name: "28309/no-protection", itemID: 28309},
		{name: "101020667/protected", itemID: 101020667, protected: true},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			itemID := test.itemID
			policy, err := basePolicyResolver("reinforce", itemID, currentLevel)
			if err != nil {
				t.Fatalf("real reinforce policy rejected item=%d level=%d: %v", itemID, currentLevel, err)
			}
			if policy.SuccessWeight < 0 || policy.SuccessWeight > currentUpgradeSuccessBase {
				t.Fatalf("real policy success weight=%d", policy.SuccessWeight)
			}
			if policy.PenaltyType != 2 && policy.PenaltyType != 3 {
				t.Fatalf("real +13 failure has no applied penalty: %+v", policy)
			}
			if policy.PenaltyType == 2 && policy.PenaltyValue <= 0 {
				t.Fatalf("subtract penalty has nonpositive value: %+v", policy)
			}
			if policy.MaterialItemID != int(materialItemID) || policy.MaterialCount <= 0 {
				t.Fatalf("real policy material=(%d,%d), want item=%d with a positive count", policy.MaterialItemID, policy.MaterialCount, materialItemID)
			}

			expectedLevel, expectedResultCode, err := reinforceTwelveFailureExpectation(policy, currentLevel)
			if err != nil {
				t.Fatal(err)
			}
			if test.protected {
				if expectedResultCode != 3 {
					t.Fatalf("protection fixture does not exercise the destroy branch: result=%d policy=%+v", expectedResultCode, policy)
				}
				expectedResultCode = 2
				expectedLevel = byte(expectedProtectRetain)
				if expectedLevel > currentLevel {
					expectedLevel = currentLevel
				}
			}
			quote, err := defaultCostResolver("reinforce", itemID, currentLevel, false, 0xffff)
			if err != nil {
				t.Fatalf("default no-NPC quote rejected item=%d level=%d: %v", itemID, currentLevel, err)
			}
			if !quote.Resolved || quote.GoldCost <= 0 || quote.ConversionCount != 0 {
				t.Fatalf("default no-NPC quote=%+v is not a positive pure-gold quote", quote)
			}

			initialGold := quote.GoldCost + 12345
			if initialGold < 0 || initialGold > 0x7fffffff {
				t.Fatalf("quote does not fit the current wallet projection: %+v", quote)
			}
			initialMaterial := int64(policy.MaterialCount) + 37
			expectedGold := initialGold - quote.GoldCost
			expectedMaterial := initialMaterial - int64(policy.MaterialCount)

			t.Logf("item=%d policy=%+v quote=%+v ticket=%+v expected_level=%d expected_result=%d",
				itemID, policy, quote, protectTicket, expectedLevel, expectedResultCode)

			ctx := context.Background()
			repos := memory.NewMemoryGroup()
			if err := repos.Character.Save(ctx, dnfrepo.CharacterRecord{
				CharacterID: "77",
				AccountID:   accountID,
				Stats:       map[string]int64{"gold": initialGold},
			}); err != nil {
				t.Fatal(err)
			}
			target := realForgingWeaponStack(itemID, 45)
			target.RawEntry[0x0a] = currentLevel
			target.Extra["reinforce"] = strconv.Itoa(currentLevel)
			bagSlots := map[string]dnfrepo.ItemStack{"0:9": target}
			const (
				protectTicketSlot   = int16(130)
				initialTicketCount  = int64(3)
				expectedTicketCount = int64(2)
			)
			if test.protected {
				bagSlots["0:130"] = dnfrepo.ItemStack{
					ItemID: protectTicketItemID,
					Count:  initialTicketCount,
					Extra: map[string]string{
						"item_kind":      "stackable",
						"pvf_path":       protectTicket.PVFPath,
						"stackable_type": "[material]",
					},
				}
			}
			if err := repos.Inventory.Save(ctx, dnfrepo.InventoryRecord{
				CharacterID: "77",
				Slots:       bagSlots,
			}); err != nil {
				t.Fatal(err)
			}
			sharedKey := dnfrepo.AccountSharedInventorySlotKey(materialSlot)
			if err := repos.AccountInventory.Save(ctx, dnfrepo.AccountInventoryRecord{
				AccountID: accountID,
				Slots: map[string]dnfrepo.ItemStack{
					sharedKey: {ItemID: materialItemID, Count: initialMaterial},
				},
			}); err != nil {
				t.Fatal(err)
			}

			policyCalls := 0
			costCalls := 0
			var appliedQuote alignedcmd.UpgradeCostResolution
			body := make([]byte, 16)
			binary.LittleEndian.PutUint16(body[0:2], 0) // reinforce; no NPC trailer
			binary.LittleEndian.PutUint16(body[2:4], uint16(targetSlot))
			binary.LittleEndian.PutUint32(body[4:8], uint32(itemID))
			binary.LittleEndian.PutUint16(body[8:10], uint16(materialSlot))
			binary.LittleEndian.PutUint16(body[10:12], 0xffff)
			binary.LittleEndian.PutUint32(body[12:16], 0)

			result, err := inventory.NewHandler().Handle(ctx, alignedcmd.Request{
				Opcode:              uint16(dnfenum.CmdPacketUpgradeItem),
				Body:                body,
				AccountID:           accountID,
				SelectedCharacterID: 77,
				Repositories:        repos,
				UpgradePolicyResolver: func(mode string, resolvedItemID int64, level int) (alignedcmd.UpgradePolicyResolution, error) {
					policyCalls++
					if mode != "reinforce" || resolvedItemID != itemID || level != currentLevel {
						return alignedcmd.UpgradePolicyResolution{}, fmt.Errorf(
							"unexpected handler policy query mode=%s item=%d level=%d", mode, resolvedItemID, level)
					}
					resolved, err := basePolicyResolver(mode, resolvedItemID, level)
					resolved.SuccessWeight = 0 // force the real-table failure branch
					return resolved, err
				},
				UpgradeCostResolver: func(mode string, resolvedItemID int64, level int, npcPresent bool, npcID uint16) (alignedcmd.UpgradeCostResolution, error) {
					costCalls++
					if mode != "reinforce" || resolvedItemID != itemID || level != currentLevel || npcPresent || npcID != 0xffff {
						return alignedcmd.UpgradeCostResolution{}, fmt.Errorf(
							"unexpected handler quote query mode=%s item=%d level=%d npc=%t/%d",
							mode, resolvedItemID, level, npcPresent, npcID)
					}
					resolved, err := defaultCostResolver(mode, resolvedItemID, level, npcPresent, npcID)
					if err == nil {
						appliedQuote = resolved
					}
					return resolved, err
				},
				UpgradeProtectTicketResolver: protect,
			})
			if err != nil || !result.Handled || !result.ResponseAllowed {
				t.Fatalf("handler handled=%t response_allowed=%t err=%v reason=%q",
					result.Handled, result.ResponseAllowed, err, result.Reason)
			}
			if policyCalls != 1 || costCalls != 1 {
				t.Fatalf("policy calls=%d cost calls=%d, want one each", policyCalls, costCalls)
			}
			if appliedQuote != quote {
				t.Fatalf("handler quote=%+v differs from default no-NPC quote=%+v", appliedQuote, quote)
			}
			if test.protected {
				fact := result.UpgradeProtectTicket
				if fact == nil || !fact.Consumed || fact.Mode != "reinforce" || fact.Slot != protectTicketSlot ||
					fact.ItemID != protectTicketItemID || fact.Remaining != expectedTicketCount ||
					fact.RetainLevel != expectedProtectRetain || fact.NewLevel != expectedLevel ||
					fact.PVFPath != protectTicket.PVFPath {
					t.Fatalf("protection fact=%+v, want consumed item=%d slot=%d remaining=%d retain=%d level=%d path=%s",
						fact, protectTicketItemID, protectTicketSlot, expectedTicketCount, protectTicket.RetainLevel, expectedLevel, protectTicket.PVFPath)
				}
				if result.UpgradeDestroyBonusMerge != nil {
					t.Fatalf("protected failure reported destruction compensation: %+v", result.UpgradeDestroyBonusMerge)
				}
			} else if result.UpgradeProtectTicket != nil && result.UpgradeProtectTicket.Consumed {
				t.Fatalf("no-protection fixture consumed a ticket: %+v", result.UpgradeProtectTicket)
			}

			var ack *alignedcmd.UpperResponse
			ackIndex := -1
			for index := range result.UpperResponses {
				response := &result.UpperResponses[index]
				if response.MsgID == uint16(dnfenum.CmdPacketUpgradeItem) && response.Classification == 1 {
					if ack != nil {
						t.Fatal("duplicate op50 ACK")
					}
					ack = response
					ackIndex = index
				}
			}
			if ack == nil {
				t.Fatal("op50 failure is missing its class1 ACK")
			}
			if ackIndex != 0 {
				t.Fatalf("op50 ACK index=%d, want first response", ackIndex)
			}
			if len(ack.Body) < 17 || ack.Body[0] != 1 || ack.Body[1] != 0 {
				t.Fatalf("failure ACK header=%x", ack.Body)
			}
			// The current op80 reader always consumes the common u16/i32 tail
			// after the result. With no conversion material and no destruction,
			// the ordinary downgrade ACK is exactly 25 bytes. Destruction keeps
			// its existing optional bonus segment immediately before this tail.
			tailOffset := 19
			switch expectedResultCode {
			case 3:
				if len(ack.Body) < 20 {
					t.Fatalf("destroy ACK lacks its bonus-presence byte: %x", ack.Body)
				}
				if ack.Body[19] == 0 {
					if len(ack.Body) != 26 {
						t.Fatalf("ordinary destroy ACK len=%d, want 26: %x", len(ack.Body), ack.Body)
					}
					tailOffset = 20
				} else {
					if len(ack.Body) != 36 {
						t.Fatalf("bonus destroy ACK len=%d, want 36: %x", len(ack.Body), ack.Body)
					}
					tailOffset = 30
				}
			default:
				if len(ack.Body) != 25 {
					t.Fatalf("ordinary no-conversion ACK len=%d, want 25: %x", len(ack.Body), ack.Body)
				}
			}
			if got := int16(binary.LittleEndian.Uint16(ack.Body[2:4])); got != materialSlot {
				t.Fatalf("ACK material slot=%d, want %d", got, materialSlot)
			}
			if got := int64(int32(binary.LittleEndian.Uint32(ack.Body[4:8]))); got != expectedMaterial {
				t.Fatalf("ACK material remaining=%d, want %d", got, expectedMaterial)
			}
			if got := binary.LittleEndian.Uint16(ack.Body[8:10]); got != 0xffff {
				t.Fatalf("ACK optional ticket slot=%d, want -1", int16(got))
			}
			if ack.Body[10] != 0 || ack.Body[11] != currentLevel {
				t.Fatalf("ACK prefix=%x old_level=%d", ack.Body[:12], ack.Body[11])
			}
			if ack.Body[12] != expectedResultCode || ack.Body[13]&31 != expectedLevel || ack.Body[14] != 0 {
				t.Fatalf("ACK result=%d old=%d new=%d, want result=%d new=%d: %x",
					ack.Body[12], ack.Body[11], ack.Body[13]&31, expectedResultCode, expectedLevel, ack.Body)
			}
			if got := int16(binary.LittleEndian.Uint16(ack.Body[15:17])); got != targetSlot {
				t.Fatalf("ACK target slot=%d, want %d", got, targetSlot)
			}
			if got := int16(binary.LittleEndian.Uint16(ack.Body[17:19])); got != -1 {
				t.Fatalf("ACK optional ticket tail=%d, want -1", got)
			}
			if got := int16(binary.LittleEndian.Uint16(ack.Body[tailOffset : tailOffset+2])); got != -1 {
				t.Fatalf("ACK common-tail conversion slot=%d, want -1", got)
			}
			if got := int64(int32(binary.LittleEndian.Uint32(ack.Body[tailOffset+2 : tailOffset+6]))); got != 0 {
				t.Fatalf("ACK common-tail conversion remaining=%d, want 0", got)
			}

			character, found, err := repos.Character.Load(ctx, "77")
			if err != nil || !found {
				t.Fatalf("reload wallet found=%t err=%v", found, err)
			}
			bag, found, err := repos.Inventory.Load(ctx, "77")
			if err != nil || !found {
				t.Fatalf("reload inventory found=%t err=%v", found, err)
			}
			account, found, err := repos.AccountInventory.Load(ctx, accountID)
			if err != nil || !found {
				t.Fatalf("reload shared inventory found=%t err=%v", found, err)
			}
			if character.Stats["gold"] != expectedGold {
				t.Fatalf("stored gold=%d, want exactly one debit to %d", character.Stats["gold"], expectedGold)
			}
			if got := account.Slots[sharedKey].Count; got != expectedMaterial {
				t.Fatalf("stored shared material=%d, want exactly one debit to %d", got, expectedMaterial)
			}
			if test.protected {
				ticket, found := bag.Slots["0:130"]
				if !found || ticket.ItemID != protectTicketItemID || ticket.Count != expectedTicketCount {
					t.Fatalf("stored protection ticket=%+v found=%t, want item=%d count=%d after exactly one consumption",
						ticket, found, protectTicketItemID, expectedTicketCount)
				}
			} else if ticket, found := bag.Slots["0:130"]; found {
				t.Fatalf("no-protection fixture retained an unexpected ticket: %+v", ticket)
			}

			stored, targetFound := bag.Slots["0:9"]
			targetDestroyed := expectedResultCode == 3
			if targetDestroyed {
				if targetFound {
					t.Fatalf("destroyed target remains in inventory: %+v", stored)
				}
			} else {
				if !targetFound || stored.ItemID != itemID {
					t.Fatalf("target missing or changed: found=%t stack=%+v", targetFound, stored)
				}
				if len(stored.RawEntry) <= 0x0a {
					t.Fatalf("stored target raw row length=%d, want at least %d", len(stored.RawEntry), 0x0b)
				}
				if stored.RawEntry[0x0a]&31 != expectedLevel {
					t.Fatalf("stored raw level=%d, want %d", stored.RawEntry[0x0a]&31, expectedLevel)
				}
				extraLevel, err := strconv.Atoi(stored.Extra["reinforce"])
				if err != nil || extraLevel&31 != int(expectedLevel) || stored.Extra["upgrade_level"] != strconv.Itoa(int(expectedLevel)) {
					t.Fatalf("stored extra levels reinforce=%q upgrade_level=%q, want %d",
						stored.Extra["reinforce"], stored.Extra["upgrade_level"], expectedLevel)
				}
			}
			if test.protected && policy.DestroyBonusItemID > 0 {
				for _, stack := range bag.Slots {
					if stack.ItemID == int64(policy.DestroyBonusItemID) {
						t.Fatalf("protected failure granted destruction compensation item=%d: %+v", policy.DestroyBonusItemID, stack)
					}
				}
			}

			updates := make(map[int16][][]byte)
			walletRows := 0
			for _, response := range result.UpperResponses {
				if response.MsgID != 14 || response.Classification != 0 {
					continue
				}
				body := response.Body
				if len(body) < 3 || body[0] != 0 {
					t.Fatalf("invalid main-list update body=%x", body)
				}
				count := int(binary.LittleEndian.Uint16(body[1:3]))
				if len(body) != 3+count*itemListRowSize {
					t.Fatalf("main-list update body length=%d rows=%d", len(body), count)
				}
				for index := 0; index < count; index++ {
					start := 3 + index*itemListRowSize
					row := append([]byte(nil), body[start:start+itemListRowSize]...)
					slot := int16(binary.LittleEndian.Uint16(row[0:2]))
					updates[slot] = append(updates[slot], row)
					if slot == 0 && binary.LittleEndian.Uint32(row[2:6]) == 0 {
						walletRows++
					}
				}
			}

			targetRows := updates[targetSlot]
			if len(targetRows) != 1 {
				t.Fatalf("target projection rows=%d, want one", len(targetRows))
			}
			targetRow := targetRows[0]
			if targetDestroyed {
				if binary.LittleEndian.Uint32(targetRow[2:6]) != 0xffffffff ||
					binary.LittleEndian.Uint32(targetRow[6:10]) != 0 ||
					targetRow[0x0a] != 0 {
					t.Fatalf("destroyed target projection=%x is not an empty sentinel", targetRow)
				}
			} else {
				if binary.LittleEndian.Uint32(targetRow[2:6]) != uint32(itemID) ||
					targetRow[0x0a]&31 != expectedLevel {
					t.Fatalf("target projection item=%d level=%d, want item=%d level=%d",
						binary.LittleEndian.Uint32(targetRow[2:6]), targetRow[0x0a]&31, itemID, expectedLevel)
				}
			}

			materialRows := updates[materialSlot]
			if len(materialRows) != 1 {
				t.Fatalf("material projection rows=%d, want one", len(materialRows))
			}
			materialRow := materialRows[0]
			if binary.LittleEndian.Uint32(materialRow[2:6]) != uint32(materialItemID) ||
				int64(binary.LittleEndian.Uint32(materialRow[6:10])) != expectedMaterial {
				t.Fatalf("material projection item=%d count=%d, want item=%d count=%d",
					binary.LittleEndian.Uint32(materialRow[2:6]), binary.LittleEndian.Uint32(materialRow[6:10]),
					materialItemID, expectedMaterial)
			}
			if test.protected {
				ticketRows := updates[protectTicketSlot]
				if len(ticketRows) != 1 {
					t.Fatalf("protection-ticket projection rows=%d, want one", len(ticketRows))
				}
				ticketRow := ticketRows[0]
				if binary.LittleEndian.Uint32(ticketRow[2:6]) != uint32(protectTicketItemID) ||
					int64(binary.LittleEndian.Uint32(ticketRow[6:10])) != expectedTicketCount {
					t.Fatalf("protection-ticket projection item=%d count=%d, want item=%d count=%d",
						binary.LittleEndian.Uint32(ticketRow[2:6]), binary.LittleEndian.Uint32(ticketRow[6:10]),
						protectTicketItemID, expectedTicketCount)
				}
			} else if ticketRows := updates[protectTicketSlot]; len(ticketRows) != 0 {
				t.Fatalf("no-protection fixture sent ticket projections: %d rows", len(ticketRows))
			}

			walletProjection := updates[0]
			if walletRows != 1 || len(walletProjection) != 1 {
				t.Fatalf("wallet projection rows=%d, slot-zero rows=%d; want exactly one", walletRows, len(walletProjection))
			}
			walletRow := walletProjection[0]
			if binary.LittleEndian.Uint32(walletRow[2:6]) != 0 ||
				int64(binary.LittleEndian.Uint32(walletRow[6:10])) != expectedGold {
				t.Fatalf("wallet projection item=%d gold=%d, want item=0 gold=%d",
					binary.LittleEndian.Uint32(walletRow[2:6]), binary.LittleEndian.Uint32(walletRow[6:10]), expectedGold)
			}
		})
	}
}

func reinforceTwelveFailureExpectation(policy alignedcmd.UpgradePolicyResolution, level int) (byte, byte, error) {
	switch policy.PenaltyType {
	case 2:
		next := level - policy.PenaltyValue
		if next < 0 {
			next = 0
		}
		return byte(next), 2, nil
	case 3:
		// The current owner deletes the target row but leaves Result.NewLevel at
		// the pre-attempt level for the destroy result code.
		return byte(level), 3, nil
	default:
		return 0, 0, fmt.Errorf("real +%d failure policy has no change/destroy penalty: %+v", level+1, policy)
	}
}
