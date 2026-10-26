package dnfbridge

import (
	"bytes"
	"context"
	"encoding/hex"
	"log/slog"
	"reflect"
	"testing"
	"time"

	proto "longheng.io/server/internal/modules/dnf/protocol"
	repo "longheng.io/server/internal/modules/dnf/repository"
)

func aikenR160OptionFixture(t *testing.T) (*Service, *gameSession, repo.Group, *bytes.Buffer) {
	t.Helper()
	s, session, g, logs, _ := aikenR142Fixture(t, "")
	s.logger = slog.New(slog.NewTextHandler(logs, nil))
	ctx := context.Background()
	inv, ok, err := g.Inventory.Load(ctx, "19")
	if err != nil || !ok {
		t.Fatalf("load inventory: ok=%v err=%v", ok, err)
	}
	inv.Slots["0:130"] = repo.ItemStack{ItemID: 10161534, Count: 100}
	inv.Slots["0:131"] = repo.ItemStack{ItemID: 10161533, Count: 100}
	inv.Slots["0:132"] = repo.ItemStack{ItemID: 10161535, Count: 100}
	if err := g.Inventory.Save(ctx, inv); err != nil {
		t.Fatal(err)
	}
	return s, session, g, logs
}

func aikenR160DispatchError(s *Service, session *gameSession, opcode uint16, body []byte) error {
	frame, err := proto.BuildChannelPacket(opcode, body, 1, 1)
	if err != nil {
		return err
	}
	return s.handleGameUpper(session, frame)
}

func aikenR160Dispatch(t *testing.T, s *Service, session *gameSession, opcode uint16, body []byte) {
	t.Helper()
	if err := aikenR160DispatchError(s, session, opcode, body); err != nil {
		t.Fatal(err)
	}
}

func aikenR160ExpectRejected(t *testing.T, session *gameSession) {
	t.Helper()
	packet, rest := splitGameServerUpperPacket(t, session.conn.(*bufferConn).write.Bytes())
	if len(rest) != 0 || packet.Header.MsgID != 1340 || packet.Header.Classification != 1 || !bytes.Equal(packet.Body, []byte{0, 0}) {
		t.Fatalf("wrong rejected reply: %+v rest=%x", packet, rest)
	}
}

func TestAikenR160RoutesOnlyCurrentClass(t *testing.T) {
	module := aikenUpgradeGameplayModule()
	if module.UpperHandlers[1340] == nil || module.LegacyHandlers[1340] == nil || module.LegacyNormalizers[1340] != nil {
		t.Fatalf("1340 must support both framings with class1; retain exact native body: %+v", module)
	}
	if module.UpperHandlers[1342] == nil || module.LegacyHandlers[1342] == nil {
		t.Fatal("existing 1342 route changed")
	}
}

// Native 10C759D..10C7657 sends one u16 slot, one u8 position and one u8
// option value for each changed option. Native 10C6650 prices the selected
// option from individualcustominfo [info] by the source item's current grade.
func TestAikenR160OptionChangeActualEntry(t *testing.T) {
	s, session, g, logs := aikenR160OptionFixture(t)
	before := aikenR142Load(t, g)
	beforeItem := before.Slots["0:9"]
	want := append([]byte(nil), beforeItem.RawEntry...)
	want[0x72] = 13

	aikenR160Dispatch(t, s, session, 1340, []byte{9, 0, 0, 13})

	after := aikenR142Load(t, g)
	if got := after.Slots["0:9"]; !bytes.Equal(got.RawEntry, want) {
		t.Fatalf("option row changed beyond selected position: got=%x want=%x packets=%x logs=%s", got.RawEntry, want, session.conn.(*bufferConn).write.Bytes(), logs.String())
	}
	if got, want := after.Slots["0:130"].Count, int64(77); got != want {
		t.Fatalf("option material count=%d want=%d", got, want)
	}
	raw := append([]byte(nil), session.conn.(*bufferConn).write.Bytes()...)
	first, rest := splitGameServerUpperPacket(t, raw)
	second, rest := splitGameServerUpperPacket(t, rest)
	if len(rest) != 0 || first.Header.MsgID != 14 || first.Header.Classification != 0 ||
		second.Header.MsgID != 1340 || second.Header.Classification != 1 || !bytes.Equal(second.Body, []byte{1}) {
		t.Fatalf("wrong update/ACK sequence: %+v %+v", first, second)
	}
	if len(first.Body) != 3+2*currentItemListEntryWireSize ||
		!bytes.Equal(first.Body[3+0x72:3+0x77], want[0x72:0x77]) {
		t.Fatal("NOTI14 did not carry the selected option before the ACK")
	}
}

func TestAikenR160OptionCostsAcrossGradesAndPositions(t *testing.T) {
	gradeNames := [...]string{"D", "C", "B", "A"}
	for _, tc := range []struct {
		grade, position byte
		cost            int64
	}{
		{0, 0, 23},
		{1, 1, 33},
		{2, 0, 48},
		{3, 1, 78},
	} {
		t.Run(gradeNames[tc.grade], func(t *testing.T) {
			s, session, g, logs := aikenR160OptionFixture(t)
			ctx := context.Background()
			inv := aikenR142Load(t, g)
			source := inv.Slots["0:9"]
			source.RawEntry[0x76] = tc.grade
			inv.Slots["0:9"] = source
			if err := g.Inventory.Save(ctx, inv); err != nil {
				t.Fatal(err)
			}
			want := append([]byte(nil), source.RawEntry...)
			want[0x72+tc.position] = 13
			body := []byte{9, 0, tc.position, 13}
			aikenR160Dispatch(t, s, session, 1340, body)
			after := aikenR142Load(t, g)
			if got := after.Slots["0:9"].RawEntry; !bytes.Equal(got, want) {
				t.Fatalf("grade=%d pos=%d row=%x want=%x packets=%x logs=%s", tc.grade, tc.position, got, want, session.conn.(*bufferConn).write.Bytes(), logs.String())
			}
			if got := after.Slots["0:130"].Count; got != 100-tc.cost {
				t.Fatalf("grade=%d cost=%d remaining=%d", tc.grade, tc.cost, got)
			}
			if reloaded := currentItemListEntryFromStack(0, 9, after.Slots["0:9"]); !bytes.Equal(reloaded.data[0x72:0x77], want[0x72:0x77]) {
				t.Fatal("cold row projection lost the selected option")
			}
		})
	}
}

func TestAikenR160OptionCostUsesSelectedGroupMaterial(t *testing.T) {
	for _, tc := range []struct {
		name      string
		position  byte
		option    byte
		materials [3]int64
	}{
		{"group1", 0, 7, [3]int64{100, 77, 100}},
		{"group2", 0, 11, [3]int64{75, 100, 100}},
		{"group3", 1, 21, [3]int64{100, 100, 75}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, session, g, _ := aikenR160OptionFixture(t)
			aikenR160Dispatch(t, s, session, 1340, []byte{9, 0, tc.position, tc.option})
			after := aikenR142Load(t, g)
			for i, slot := range []string{"0:130", "0:131", "0:132"} {
				if got := after.Slots[slot].Count; got != tc.materials[i] {
					t.Fatalf("%s slot=%s count=%d want=%d", tc.name, slot, got, tc.materials[i])
				}
			}
			if got := after.Slots["0:9"].RawEntry[0x72+tc.position]; got != tc.option {
				t.Fatalf("%s option=%d want=%d", tc.name, got, tc.option)
			}
		})
	}
}

func TestAikenR160RejectionsAreAtomic(t *testing.T) {
	for _, tc := range []struct {
		name   string
		body   []byte
		change func(*repo.InventoryRecord)
	}{
		{name: "same_option", body: []byte{9, 0, 0, 1}},
		{name: "duplicate_other_position", body: []byte{9, 0, 0, 25}},
		{name: "invalid_option", body: []byte{9, 0, 0, 8}},
		{name: "position_two", body: []byte{9, 0, 2, 13}},
		{name: "short", body: []byte{9, 0, 13}},
		{name: "extra", body: []byte{9, 0, 0, 13, 0}},
		{name: "missing_material", body: []byte{9, 0, 0, 13}, change: func(inv *repo.InventoryRecord) { delete(inv.Slots, "0:130") }},
		{name: "shortage_after_partial", body: []byte{9, 0, 0, 13}, change: func(inv *repo.InventoryRecord) {
			x := inv.Slots["0:130"]
			x.Count = 5
			inv.Slots["0:130"] = x
			inv.Slots["0:131"] = repo.ItemStack{ItemID: 10161534, Count: 10}
		}},
		{name: "locked_source", body: []byte{9, 0, 0, 13}, change: func(inv *repo.InventoryRecord) {
			x := inv.Slots["0:9"]
			x.Extra["equipment_lock_state"] = "1"
			inv.Slots["0:9"] = x
		}},
		{name: "locked_material", body: []byte{9, 0, 0, 13}, change: func(inv *repo.InventoryRecord) {
			x := inv.Slots["0:130"]
			x.Extra = map[string]string{"equipment_lock_state": "1"}
			inv.Slots["0:130"] = x
		}},
		{name: "expired_material", body: []byte{9, 0, 0, 13}, change: func(inv *repo.InventoryRecord) {
			x := inv.Slots["0:130"]
			x.ExpireAt = time.Unix(1, 0)
			inv.Slots["0:130"] = x
		}},
		{name: "source_count_two", body: []byte{9, 0, 0, 13}, change: func(inv *repo.InventoryRecord) {
			x := inv.Slots["0:9"]
			x.Count = 2
			inv.Slots["0:9"] = x
		}},
		{name: "invalid_grade", body: []byte{9, 0, 0, 13}, change: func(inv *repo.InventoryRecord) {
			x := inv.Slots["0:9"]
			x.RawEntry[0x76] = 4
			inv.Slots["0:9"] = x
		}},
		{name: "invalid_existing_option", body: []byte{9, 0, 0, 13}, change: func(inv *repo.InventoryRecord) {
			x := inv.Slots["0:9"]
			x.RawEntry[0x73] = 8
			inv.Slots["0:9"] = x
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, session, g, logs := aikenR160OptionFixture(t)
			if tc.change != nil {
				inv := aikenR142Load(t, g)
				tc.change(&inv)
				if err := g.Inventory.Save(context.Background(), inv); err != nil {
					t.Fatal(err)
				}
			}
			before := aikenR142Load(t, g)
			aikenR160Dispatch(t, s, session, 1340, tc.body)
			aikenR160ExpectRejected(t, session)
			if after := aikenR142Load(t, g); !reflect.DeepEqual(before, after) {
				t.Fatalf("rejected request mutated assets; logs=%s", logs.String())
			}
		})
	}
}

func TestAikenR160SkipsLockedMaterialStack(t *testing.T) {
	s, session, g, _ := aikenR160OptionFixture(t)
	inv := aikenR142Load(t, g)
	locked := inv.Slots["0:130"]
	locked.Count = 100
	locked.Extra = map[string]string{"equipment_lock_state": "1"}
	inv.Slots["0:130"] = locked
	inv.Slots["0:131"] = repo.ItemStack{ItemID: 10161534, Count: 30}
	if err := g.Inventory.Save(context.Background(), inv); err != nil {
		t.Fatal(err)
	}
	aikenR160Dispatch(t, s, session, 1340, []byte{9, 0, 0, 13})
	after := aikenR142Load(t, g)
	if after.Slots["0:130"].Count != 100 || after.Slots["0:131"].Count != 7 {
		t.Fatalf("locked stack accounting: locked=%d free=%d", after.Slots["0:130"].Count, after.Slots["0:131"].Count)
	}
	if after.Slots["0:9"].RawEntry[0x72] != 13 {
		t.Fatal("successful option change was not persisted")
	}
}

func TestAikenR160SaveAndPrecommitRollback(t *testing.T) {
	s, session, g, _ := aikenR160OptionFixture(t)
	provided := g
	entered, saved := 0, 0
	deadline := false
	provided.AccountAssets = aikenR142FaultUOW{g.AccountAssets, true, &entered, &saved, &deadline}
	s.repositoryProvider = func() (repo.Group, bool) { return provided, true }
	before := aikenR142Load(t, g)
	aikenR160Dispatch(t, s, session, 1340, []byte{9, 0, 0, 13})
	aikenR160ExpectRejected(t, session)
	if entered != 1 || saved != 1 || !deadline {
		t.Fatalf("save fault not reached: entered=%d saved=%d deadline=%v", entered, saved, deadline)
	}
	if after := aikenR142Load(t, g); !reflect.DeepEqual(before, after) {
		t.Fatal("save failure persisted part of the option transaction")
	}
}

func TestAikenR160NotifyFailureDoesNotRecommit(t *testing.T) {
	for _, fail := range []int{1, 2} {
		t.Run(string(rune('0'+fail)), func(t *testing.T) {
			s, session, g, _ := aikenR160OptionFixture(t)
			conn := &aikenR142FailWrite{fail: fail}
			session.conn = conn
			if err := aikenR160DispatchError(s, session, 1340, []byte{9, 0, 0, 13}); err == nil {
				t.Fatal("missing output failure")
			}
			after := aikenR142Load(t, g)
			if after.Slots["0:130"].Count != 77 || after.Slots["0:9"].RawEntry[0x72] != 13 || conn.writes != fail {
				t.Fatalf("wrong commit/retry: material=%d option=%d writes=%d", after.Slots["0:130"].Count, after.Slots["0:9"].RawEntry[0x72], conn.writes)
			}
			if fail == 1 && conn.write.Len() != 0 {
				t.Fatal("ACK or update appeared after first notification failure")
			}
			if fail == 2 {
				packet, rest := splitGameServerUpperPacket(t, conn.write.Bytes())
				if len(rest) != 0 || packet.Header.MsgID != 14 {
					t.Fatal("second write included an ACK or unexpected frame")
				}
			}
		})
	}
}

func TestAikenR160UpdatesAllStateCarriers(t *testing.T) {
	s, session, g, _ := aikenR160OptionFixture(t)
	ctx := context.Background()
	inv := aikenR142Load(t, g)
	source := inv.Slots["0:9"]
	tail := append([]byte(nil), source.RawEntry[0x72:0x77]...)
	source.Extra["raw_entry_hex"] = hex.EncodeToString(source.RawEntry)
	source.Extra["tail_data_72"] = hex.EncodeToString(tail)
	source.Extra["mode1_raw_e_hex"] = hex.EncodeToString(tail)
	inv.Slots["0:9"] = source
	if err := g.Inventory.Save(ctx, inv); err != nil {
		t.Fatal(err)
	}
	aikenR160Dispatch(t, s, session, 1340, []byte{9, 0, 1, 13})
	after := aikenR142Load(t, g).Slots["0:9"]
	wantTail := []byte{1, 13, 0x7e, 0xa5, 0}
	for _, key := range []string{"tail_data_72", "mode1_raw_e_hex"} {
		if got := after.Extra[key]; got != hex.EncodeToString(wantTail) {
			t.Fatalf("%s=%s want=%x", key, got, wantTail)
		}
	}
	raw, err := aikenExtraBytes(after.Extra["raw_entry_hex"])
	if err != nil || len(raw) != currentItemListEntryWireSize || !bytes.Equal(raw[0x72:0x77], wantTail) {
		t.Fatalf("raw_entry_hex was not synchronized: len=%d err=%v", len(raw), err)
	}
	if !bytes.Equal(after.RawEntry[0x72:0x77], wantTail) {
		t.Fatalf("durable tail=%x want=%x", after.RawEntry[0x72:0x77], wantTail)
	}
}
