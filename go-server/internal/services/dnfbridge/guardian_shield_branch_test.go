package dnfbridge

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sort"
	"testing"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
	dnfguardianshield "longheng.io/server/internal/modules/dnf/guardianshield"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	dnfrepomemory "longheng.io/server/internal/modules/dnf/repository/memory"
)

func TestGuardianShieldPaladinDragonLoginDoesNotPublishWarehouseDeck(t *testing.T) {
	for _, grow := range []int64{3, 4, 0x13, 0x14, 0x23, 0x24} {
		t.Run(fmt.Sprintf("grow_%02x", grow), func(t *testing.T) {
			repos := dnfrepomemory.NewMemoryGroup()
			if err := repos.Character.Save(context.Background(), dnfrepo.CharacterRecord{
				CharacterID: "77", AccountID: "acc", Job: "12", Level: 90,
				Stats: map[string]int64{"grow_type": grow, "guardian_shield_deck_0": -1},
			}); err != nil {
				t.Fatal(err)
			}
			conn := &bufferConn{}
			service := &Service{options: options{accountID: "acc", gameUpperBodyCodec: gameUpperBodyCodecPlain},
				repositoryProvider: func() (dnfrepo.Group, bool) { return repos, true }}
			session := &gameSession{conn: conn, accountID: "acc", selectedCharacterID: 77}
			for i := 0; i < 2; i++ {
				sent, err := service.sendSelectedGuardianShieldDeckInfo(session, "职业范围回归")
				if sent || err != nil || conn.write.Len() != 0 || session.initialTownGuardianShieldDeckSent {
					t.Fatalf("新职业不应加载或发送换盾仓库：sent=%t err=%v bytes=%x", sent, err, conn.write.Bytes())
				}
			}
		})
	}
}

func TestRealPVFGuardianShieldEveryCatalogRowUnlockEquipReloadAndRemove(t *testing.T) {
	path := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if path == "" {
		t.Skip("需要现用PVF")
	}
	ctx := context.Background()
	service := &Service{options: options{pvfPath: path}}
	resolver, err := service.newCurrentGuardianShieldResolver(ctx)
	if err != nil {
		t.Fatal(err)
	}
	text, err := service.initialEquipmentArchive.ReadText(currentGuardianShieldCatalogPath)
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := parseCurrentGuardianShieldCatalog(text)
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, entries := range catalog {
		count += len(entries)
	}
	if count != 25 {
		t.Fatalf("现用盾牌目录变更：共%d项，期望25项", count)
	}
	for _, grow := range []byte{1, 2} {
		ids := make([]int, 0, count)
		for group, entries := range catalog {
			if group != 0 && group != grow {
				continue
			}
			for id := range entries {
				ids = append(ids, int(id))
			}
		}
		sort.Ints(ids)
		for _, rawID := range ids {
			id := uint32(rawID)
			t.Run(fmt.Sprintf("grow_%d_item_%d", grow, id), func(t *testing.T) {
				resolution, err := resolver(ctx, id, grow)
				if err != nil {
					t.Fatal(err)
				}
				repos := dnfrepomemory.NewMemoryGroup()
				character := dnfrepo.CharacterRecord{CharacterID: "77", AccountID: "acc", Job: "12", Level: 90,
					Stats: map[string]int64{"grow_type": int64(grow)}}
				if resolution.UnlockKind == alignedcmd.GuardianShieldUnlockLevel {
					character.Level = int(resolution.MinimumLevel) - 1
				}
				if err := repos.Character.Save(ctx, character); err != nil {
					t.Fatal(err)
				}
				owner, err := dnfguardianshield.NewOwner(repos, resolver)
				if err != nil {
					t.Fatal(err)
				}
				command := dnfguardianshield.MoveCommand{CharacterID: 77, AccountID: "acc", SourceListType: 34,
					SourceShieldItemID: id, DestinationListType: 33}
				wantLocked := dnfguardianshield.ErrClearQuestIncomplete
				if resolution.UnlockKind == alignedcmd.GuardianShieldUnlockLevel {
					wantLocked = dnfguardianshield.ErrLevelInsufficient
				}
				if _, err := owner.Move(ctx, command); !errors.Is(err, wantLocked) {
					t.Fatalf("未解锁盾牌应拒绝：err=%v want=%v", err, wantLocked)
				}
				persisted, _, _ := repos.Character.Load(ctx, "77")
				if dnfguardianshield.DeckFromCharacter(persisted) != (dnfguardianshield.Deck{}) {
					t.Fatal("未解锁盾牌写入了盾栏")
				}
				if resolution.UnlockKind == alignedcmd.GuardianShieldUnlockLevel {
					character.Level = int(resolution.MinimumLevel)
					if err := repos.Character.Save(ctx, character); err != nil {
						t.Fatal(err)
					}
				} else {
					if err := repos.Quest.Save(ctx, dnfrepo.QuestRecord{CharacterID: "77", States: map[int64]dnfrepo.QuestState{
						resolution.ClearQuestID: {Status: "completed"},
					}}); err != nil {
						t.Fatal(err)
					}
					if _, err := resolver(ctx, id, 3-grow); !errors.Is(err, errGuardianShieldNotInCatalog) {
						t.Fatalf("其它职业分支获得任务专属盾牌：%v", err)
					}
				}
				result, err := owner.Move(ctx, command)
				if err != nil || result.Deck != (dnfguardianshield.Deck{id}) || !result.MainChanged {
					t.Fatalf("解锁后穿戴失败：result=%+v err=%v", result, err)
				}
				persisted, _, _ = repos.Character.Load(ctx, "77")
				deck, err := dnfguardianshield.LoadDeck(persisted)
				equipment, _, _ := repos.Equipment.Load(ctx, "77")
				mainID, equipped := dnfguardianshield.MainProjectionItemID(equipment)
				if err != nil || deck != (dnfguardianshield.Deck{id}) || !equipped || mainID != id {
					t.Fatalf("重新读取后盾栏与主盾不一致：deck=%v main=%d err=%v", deck, mainID, err)
				}
				if saved, err := owner.SaveDeck(ctx, 77, "acc", deck); err != nil || saved.Changed || saved.MainChanged {
					t.Fatalf("重复保存不应再次改变主盾：result=%+v err=%v", saved, err)
				}
				removed, err := owner.Move(ctx, dnfguardianshield.MoveCommand{CharacterID: 77, AccountID: "acc",
					SourceListType: 33, DestinationListType: 34, DestinationSlotIndex: 5})
				if err != nil || removed.Deck != (dnfguardianshield.Deck{}) {
					t.Fatalf("卸下盾牌失败：result=%+v err=%v", removed, err)
				}
				equipment, _, _ = repos.Equipment.Load(ctx, "77")
				if _, exists := equipment.Entries["24"]; exists {
					t.Fatal("卸下后主盾投影未移除")
				}
				inventory, _, _ := repos.Inventory.Load(ctx, "77")
				if len(inventory.Slots) != 0 {
					t.Fatal("盾牌变成了背包实物")
				}
			})
		}
	}
}
