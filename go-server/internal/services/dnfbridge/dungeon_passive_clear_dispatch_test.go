package dnfbridge

import (
	"encoding/binary"
	"testing"

	"longheng.io/server/internal/modules/dnf/dungeoncmd"
	dnfproto "longheng.io/server/internal/modules/dnf/protocol"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/worldmap"
)

func newPassiveClearDispatchFixture(t *testing.T, reward bool) *seekerPassiveDropFixture {
	t.Helper()
	drop := seekerPassiveDropCase{code: 69292, questID: 3735, itemID: 10164801, dungeonID: 7156, difficulty: 1}
	f := newSeekerPassiveDropFixture(t, seekerPassiveDropFixtureOptions{drop: drop, active: reward})
	// Synthetic overlap: a clear target may also be a type3 material source.
	// The actual Great Will data and route are exercised by the real-PVF test.
	f.runtime.Dungeon.Mazes[0].ClearConditions = []worldmap.ClearCondition{{Type: "destroy object", TargetID: drop.code, Count: 1}}
	scene, ok := f.runtime.Session.Scene()
	if !ok || currentDungeonDestroyObjectClearConditionSource(f.runtime, scene, uint32(drop.code)) == "" {
		t.Fatal("fixture lacks current-room clear ownership")
	}
	return f
}

func TestPassiveClearDispatchRetriesMaterialTransactionBeforeCompletion(t *testing.T) {
	f := newPassiveClearDispatchFixture(t, true)
	broken := f.repositories
	broken.CharacterItems = &seekerQST022FailOnceItems{base: broken.CharacterItems}
	f.service.repositoryProvider = func() (dnfrepo.Group, bool) { return broken, true }
	body := seekerPassiveDropNativeBody(f.drop.code)
	if err := seekerPassiveDropSendFormal(t, f, 100, dnfproto.DefaultChannelClassification, body); err == nil {
		t.Fatal("injected inventory failure was not returned")
	}
	if f.runtime.Session.Snapshot().Run.Status != worldmap.DungeonRunActive || f.runtime.settlementEntrySent || seekerPassiveDropHeld(t, f) != 0 {
		t.Fatal("failed material transaction completed or granted")
	}
	if err := seekerPassiveDropSendFormal(t, f, 100, dnfproto.DefaultChannelClassification, body); err != nil {
		t.Fatal(err)
	}
	if f.runtime.Session.Snapshot().Run.Status != worldmap.DungeonRunCompleted || !f.runtime.settlementEntrySent || seekerPassiveDropHeld(t, f) != 1 {
		t.Fatal("same-event retry did not commit both material and completion")
	}
	f.connection.write.Reset()
	if err := seekerPassiveDropSendFormal(t, f, 100, dnfproto.DefaultChannelClassification, body); err != nil || f.connection.write.Len() != 0 || seekerPassiveDropHeld(t, f) != 1 {
		t.Fatalf("resolved retry changed effects: err=%v packets=%x", err, f.connection.write.Bytes())
	}
}

func TestPassiveClearDispatchPreservesMaterialEffectAndDuplicateGuards(t *testing.T) {
	for _, mode := range []string{"legacy", "register"} {
		t.Run(mode, func(t *testing.T) {
			t.Setenv(mapOwnedPassiveObjectModeEnvName, mode)
			for _, reward := range []bool{false, true} {
				name := "no_reward"
				if reward {
					name = "material_and_clear"
				}
				t.Run(name, func(t *testing.T) {
					f := newPassiveClearDispatchFixture(t, reward)
					body := seekerPassiveDropNativeBody(f.drop.code)
					if err := seekerPassiveDropSendFormal(t, f, 100, dnfproto.DefaultChannelClassification, body); err != nil {
						t.Fatal(err)
					}
					if f.runtime.Session.Snapshot().Run.Status != worldmap.DungeonRunCompleted || !f.runtime.settlementEntrySent {
						t.Fatal("passive report did not complete its explicit clear condition")
					}
					want := int64(0)
					if reward {
						want = 1
					}
					if got := seekerPassiveDropHeld(t, f); got != want {
						t.Fatalf("material count=%d want=%d", got, want)
					}
					clear, entry := 0, 0
					for _, p := range splitAllFinishBridgePackets(t, f.connection.write.Bytes()) {
						switch p.Header.MsgID {
						case 38:
							t.Fatalf("passive definition received actor death ACK: %x", p.Body)
						case 115:
							clear++
						case currentDungeonSettlementEntryMsgID:
							entry++
						}
					}
					if clear != 1 || entry != 1 {
						t.Fatalf("clear=%d entry=%d", clear, entry)
					}
					// Same event replay and a second sequence for the same one-placement
					// source both retain the task-drop quota and completion receipt.
					for _, seq := range []uint32{100, 101} {
						f.connection.write.Reset()
						if err := seekerPassiveDropSendFormal(t, f, seq, dnfproto.DefaultChannelClassification, body); err != nil {
							t.Fatal(err)
						}
						if f.connection.write.Len() != 0 || seekerPassiveDropHeld(t, f) != want {
							t.Fatalf("duplicate sequence=%d output=%x material=%d", seq, f.connection.write.Bytes(), seekerPassiveDropHeld(t, f))
						}
					}
				})
			}
		})
	}
}

func TestPassiveClearDispatchKeepsCurrentRoomAndRequestBoundaries(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*seekerPassiveDropFixture, []byte) []byte
	}{
		{"wrong_definition", func(_ *seekerPassiveDropFixture, b []byte) []byte { binary.LittleEndian.PutUint32(b, 69293); return b }},
		{"wrong_owner", func(_ *seekerPassiveDropFixture, b []byte) []byte { binary.LittleEndian.PutUint16(b[4:], 77); return b }},
		{"combat_entries", func(_ *seekerPassiveDropFixture, b []byte) []byte {
			b = append(b[:31], append(make([]byte, dungeoncmd.Official249DieMonsterEntrySize), b[31:]...)...)
			b[30] = 1
			return b
		}},
		{"opaque_tail", func(_ *seekerPassiveDropFixture, b []byte) []byte { return append(b, 0) }},
		{"wrong_room_map", func(f *seekerPassiveDropFixture, b []byte) []byte { f.runtime.Room.mapID++; return b }},
		{"wrong_boss_coordinate", func(f *seekerPassiveDropFixture, b []byte) []byte { f.runtime.BossCoordinate.X++; return b }},
		{"missing_clear_condition", func(f *seekerPassiveDropFixture, b []byte) []byte {
			f.runtime.Dungeon.Mazes[0].ClearConditions = nil
			return b
		}},
		{"different_target", func(f *seekerPassiveDropFixture, b []byte) []byte {
			f.runtime.Dungeon.Mazes[0].ClearConditions[0].TargetID++
			return b
		}},
		{"count_mismatch", func(f *seekerPassiveDropFixture, b []byte) []byte {
			f.runtime.Dungeon.Mazes[0].ClearConditions[0].Count = 2
			return b
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := newPassiveClearDispatchFixture(t, false)
			body := tc.mutate(f, seekerPassiveDropNativeBody(f.drop.code))
			if err := seekerPassiveDropSendFormal(t, f, 100, dnfproto.DefaultChannelClassification, body); err != nil {
				t.Fatal(err)
			}
			if f.runtime.Session.Snapshot().Run.Status != worldmap.DungeonRunActive || f.runtime.settlementEntrySent || f.runtime.ordinaryFinalRoomClearAccepted {
				t.Fatal("unproven report completed the dungeon")
			}
			for _, p := range splitAllFinishBridgePackets(t, f.connection.write.Bytes()) {
				if p.Header.MsgID == 38 || p.Header.MsgID == 115 || p.Header.MsgID == currentDungeonSettlementEntryMsgID {
					t.Fatalf("unexpected response %d", p.Header.MsgID)
				}
			}
		})
	}
}
