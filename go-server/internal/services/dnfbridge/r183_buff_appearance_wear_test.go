package dnfbridge

import (
	"context"
	"reflect"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
	"longheng.io/server/internal/modules/dnf/buffswap"
	repo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/skillswitch"
)

func TestR183LoginWearNoopStillClearsNativeBuff(t *testing.T) {
	for _, mode := range []string{equipAppearanceRefreshMode0, equipAppearanceRefreshMode1} {
		t.Run(mode, func(t *testing.T) {
			setDefaultTier(t)
			t.Setenv(buffSwapReapplyEnv, "on")
			f := newLoginWearAnchorFixture(t, "1", 1, map[int16]int64{12: loginWearAnchorWeaponItemID})
			f.service.options.currentClientVersion = "official-24.9"
			f.service.options.equipAppearanceRefreshMode1Rollback = mode == equipAppearanceRefreshMode1
			f.service.options.avatarAppearanceCoalesceWindowMillis = 5000
			f.service.bindGameSessionCharacter(f.session, 1)
			q := newFakeCurrentDungeonDeathTimerQueue()
			f.service.gameplayTimers = q
			f.openWindowAgo(t, 0)
			body := loginWearBridgeBody(0, loginWearAnchorBagSlot, 0, 3, 12, int32(loginWearAnchorWeaponItemID))
			for i := 0; i < 2; i++ {
				round := f.runOp19(t, body)
				if round.result.EquipmentMoveFact == nil || round.result.EquipmentMoveFact.Changed {
					t.Fatalf("login claim mutated equipment: %+v", round.result)
				}
				if i == 1 {
					_, _, active := q.counts()
					if active != 1 {
						t.Fatalf("want queued appearance, active=%d", active)
					}
					q.fire(q.task(t, 0), false)
				}
				actor, empty := 0, 0
				for stream := f.buffer.write.Bytes(); len(stream) > 0; {
					p, rest := splitLongHengGameServerUpperPacket(t, stream)
					stream = rest
					if p.Header.Classification != 0 {
						continue
					}
					if p.Header.MsgID == 2 {
						actor++
					}
					if p.Header.MsgID == currentBuffSwapSnapshotID || p.Header.MsgID == nativeBuffSwapSnapshotID {
						empty++
						if len(p.Body) != 3+5*buffswap.SlotCount || p.Body[0] != 0 || p.Body[1] != 0 || p.Body[2] != buffswap.SlotCount {
							t.Fatalf("bad login clear %x", p.Body)
						}
						for slot := 0; slot < buffswap.SlotCount; slot++ {
							if p.Body[3+5*slot] != 0 {
								t.Fatalf("bound slot in login clear %x", p.Body)
							}
						}
					}
				}
				if actor != 1 || empty != 1 {
					t.Fatalf("round %d actor=%d empty1368=%d", i, actor, empty)
				}
			}
			f.service.stopAvatarAppearanceCoalesce(f.session, "r183_login_done")
		})
	}
}

func TestR183AppearanceTailPreservesPendingBuffClear(t *testing.T) {
	for _, tc := range []struct {
		name  string
		moves []bool
		want  int
	}{
		{"wear_only", []bool{true, true}, 0},
		{"login_then_wear", []bool{false, true}, 1},
		{"wear_then_login", []bool{true, false}, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(buffSwapReapplyEnv, "on")
			f := newLoginWearAnchorFixture(t, "1", 1, nil)
			f.service.options.currentClientVersion = "official-24.9"
			f.service.options.avatarAppearanceCoalesceWindowMillis = 5000
			f.service.bindGameSessionCharacter(f.session, 1)
			q := newFakeCurrentDungeonDeathTimerQueue()
			f.service.gameplayTimers = q
			f.session.avatarAppearanceLastAt = time.Now()
			for _, changed := range tc.moves {
				result := alignedcmd.Result{Operation: "move_itemspace", EquipmentMoveFact: &alignedcmd.EquipmentMoveFact{Changed: changed}}
				if err := f.service.sendAlignedPostAction(f.session, result, alignedcmd.PostActionRefreshSelectedActorAppearance); err != nil {
					t.Fatal(err)
				}
			}
			if f.buffer.write.Len() != 0 {
				t.Fatal("burst was not queued")
			}
			q.fire(q.task(t, 0), true)
			if f.buffer.write.Len() != 0 {
				t.Fatal("replaced callback sent stale appearance")
			}
			q.fire(q.task(t, 1), false)
			actor, snapshots := 0, 0
			for stream := f.buffer.write.Bytes(); len(stream) > 0; {
				p, rest := splitLongHengGameServerUpperPacket(t, stream)
				stream = rest
				if p.Header.Classification != 0 {
					continue
				}
				if p.Header.MsgID == 2 {
					actor++
				}
				if p.Header.MsgID == currentBuffSwapSnapshotID || p.Header.MsgID == nativeBuffSwapSnapshotID {
					snapshots++
				}
			}
			if actor != 1 || snapshots != tc.want {
				t.Fatalf("actor=%d snapshots=%d want1/%d", actor, snapshots, tc.want)
			}
			f.service.stopAvatarAppearanceCoalesce(f.session, "r183_tail_done")
			if f.session.avatarAppearanceNeedsBuffClear {
				t.Fatal("clear policy leaked across stop")
			}
		})
	}
}

func TestR183VisibleWeaponWearPreservesSeparateBuffLoadout(t *testing.T) {
	for _, mode := range []string{equipAppearanceRefreshMode0, equipAppearanceRefreshMode1} {
		for _, stored := range []bool{false, true} {
			name := mode + "/absent"
			if stored {
				name = mode + "/explicit_skill_only"
			}
			t.Run(name, func(t *testing.T) {
				f := p94Fixture(t)
				f.service.options.equipAppearanceRefreshMode1Rollback = mode == equipAppearanceRefreshMode1
				f.service.options.avatarAppearanceCoalesceOff = true
				ctx := context.Background()
				definition, err := f.service.pvfItemCatalog.ResolveItem(uint32(loginWearAnchorWeaponItemID))
				if err != nil {
					t.Fatal(err)
				}
				equipment, _, err := f.group.Equipment.Load(ctx, "1")
				if err != nil {
					t.Fatal(err)
				}
				equipment.Entries["12"] = repo.EquipmentEntry{
					SlotIndex: 12, ItemID: loginWearAnchorWeaponItemID,
					RawEntry: loginWearAnchorRawEntry(12, loginWearAnchorWeaponItemID, 203004656, 48),
					Extra: map[string]string{"item_kind": "equipment", "pvf_path": definition.PVFPath,
						"equipment_type": definition.EquipmentType, "current_exe_runtime_move": "1",
						"current_exe_equipment_type": "12", "equipped_slot": "12", "equipment_slot": "12"},
				}
				if err := f.group.Equipment.Save(ctx, equipment); err != nil {
					t.Fatal(err)
				}
				seedR167Loadout(t, f.service, f.session, f.buffer, f.group)
				before, _, err := f.group.Settings.Load(ctx, skillswitch.Scope("1"))
				if err != nil {
					t.Fatal(err)
				}
				if stored {
					if err := buffswap.ApplyCurrent(ctx, f.group, "1", "dnf:10011", buffswap.Command{Skill: 38, Slot: 33, List: 44, Index: 0xffff}); err != nil {
						t.Fatal(err)
					}
				}
				for _, body := range [][]byte{
					flaggedBody(t, loginWearBridgeBody(0, 15, 0, 3, 12, int32(loginWearAnchorWeaponItemID)), op19CurrentUIPlayerFlag),
					flaggedBody(t, loginWearBridgeBody(0, 15, int32(loginWearAnchorWeaponItemID), 3, 12, 0), op19CurrentUIPlayerFlag),
				} {
					f.service.stopAvatarAppearanceCoalesce(f.session, "r183_separate_manual_move")
					p94Move(t, f, body)
					actor, ack, native := 0, 0, 0
					for stream := f.buffer.write.Bytes(); len(stream) > 0; {
						packet, rest := splitCurrentGameServerUpperPacketAuto(t, stream)
						stream = rest
						if packet.Header.Classification == 1 && packet.Header.MsgID == 19 {
							ack++
						}
						if packet.Header.Classification != 0 {
							continue
						}
						switch packet.Header.MsgID {
						case 2:
							actor++
						case currentBuffSwapSnapshotID, currentBuffSwapRowsID, nativeBuffSwapSnapshotID:
							native++
						case currentSkillSwitchInventoryOpcode:
							t.Fatal("wear enabled unproved loadout downlink")
						}
					}
					want := 0
					if stored {
						want = 1
					}
					if actor == 0 || ack != 1 || native != want {
						t.Fatalf("visible weapon move actor=%d ack=%d native=%d; want actor>0 ack1 native%d", actor, ack, native, want)
					}
					after, _, err := f.group.Settings.Load(ctx, skillswitch.Scope("1"))
					if err != nil || !reflect.DeepEqual(before, after) {
						t.Fatal("wear changed separate loadout", err)
					}
				}
			})
		}
	}
}
