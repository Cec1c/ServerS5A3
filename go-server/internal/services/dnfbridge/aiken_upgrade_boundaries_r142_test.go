package dnfbridge

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	proto "longheng.io/server/internal/modules/dnf/protocol"
	repo "longheng.io/server/internal/modules/dnf/repository"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"
)

func aikenR142Load(t *testing.T, g repo.Group) repo.InventoryRecord {
	t.Helper()
	v, ok, e := g.Inventory.Load(context.Background(), "19")
	if e != nil || !ok {
		t.Fatal(e)
	}
	return v
}
func aikenR142Save(t *testing.T, g repo.Group, v repo.InventoryRecord) {
	t.Helper()
	if e := g.Inventory.Save(context.Background(), v); e != nil {
		t.Fatal(e)
	}
}
func aikenR142Request(t *testing.T, s *Service, session *gameSession, slot int16) error {
	t.Helper()
	b := make([]byte, 2)
	binary.LittleEndian.PutUint16(b, uint16(slot))
	frame, e := proto.BuildChannelPacket(1342, b, 1, 1)
	if e != nil {
		t.Fatal(e)
	}
	return s.handleGameUpper(session, frame)
}
func aikenR142Outcome(t *testing.T, session *gameSession, success bool) {
	t.Helper()
	raw := session.conn.(*bufferConn).write.Bytes()
	if success {
		p, rest := splitGameServerUpperPacket(t, raw)
		if p.Header.MsgID != 14 || p.Header.Classification != 0 {
			t.Fatal("expected inventory before ACK")
		}
		raw = rest
	}
	p, rest := splitGameServerUpperPacket(t, raw)
	want := []byte{0, 0}
	if success {
		want = []byte{1}
	}
	if p.Header.MsgID != 1342 || p.Header.Classification != 1 || !bytes.Equal(p.Body, want) || len(rest) != 0 {
		t.Fatalf("unexpected ACK %+v trailing=%d", p, len(rest))
	}
}

func TestAikenR142ContainerExpiryAndCount(t *testing.T) {
	for _, tc := range []struct {
		name             string
		source, material int16
		expansion        uint16
		deadline         int
		locked           bool
		success          bool
	}{
		{"base_last", 40, 152, 0, 0, false, true}, {"base_closed_target", 41, 121, 0, 0, false, false}, {"base_closed_material", 9, 153, 0, 0, false, false},
		{"expand8_last", 48, 160, 8, 0, false, true}, {"expand8_closed", 49, 161, 8, 0, false, false}, {"expand16_last", 56, 168, 16, 0, false, true}, {"expand24_last", 64, 176, 24, 0, false, true},
		{"quick_target", 3, 121, 0, 0, false, true}, {"quick_material", 9, 8, 0, 0, false, true}, {"wrong_page", 65, 121, 24, 0, false, false}, {"wrong_material_page", 9, 177, 24, 0, false, false},
		{"live_expire_at", 9, 121, 0, 1, false, true}, {"exact_expire_at", 9, 121, 0, 2, false, false}, {"expired_extra", 9, 121, 0, 3, false, false}, {"live_extra", 9, 121, 0, 4, false, true},
		{"locked_material", 9, 121, 0, 0, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, session, g, _, _ := aikenR142Fixture(t, "")
			now := time.Unix(1800000000, 0).UTC()
			s.gameplayTimers = &processGameplayTimeQueue{now: func() time.Time { return now }}
			inv := aikenR142Load(t, g)
			target := inv.Slots["0:9"]
			mat := inv.Slots["0:121"]
			delete(inv.Slots, "0:9")
			delete(inv.Slots, "0:121")
			mat.RawEntry = make([]byte, 119)
			binary.LittleEndian.PutUint32(mat.RawEntry[2:], uint32(mat.ItemID))
			binary.LittleEndian.PutUint32(mat.RawEntry[6:], 100)
			mat.RawEntry[100] = 0xa8
			h := hex.EncodeToString(mat.RawEntry)
			mat.Extra = map[string]string{"amount_or_count": "100", "amount": "100", "count": "100", "stack": "100", "quantity": "100", "raw_entry_hex": "0x" + h, "current_raw_entry_77": strings.Join(strings.Split(h, ""), " "), "raw_entry_77": strings.ToUpper(h), "count_or_instance": "765"}
			switch tc.deadline {
			case 1:
				mat.ExpireAt = now.Add(time.Second)
			case 2:
				mat.ExpireAt = now
			case 3:
				mat.Extra["expire_time"] = strconv.FormatInt(now.Unix(), 10)
			case 4:
				mat.Extra["expire_unix"] = strconv.FormatInt(now.Unix()+1, 10)
			}
			if tc.locked {
				mat.Extra["equipment_lock_state"] = "1"
			}
			inv.Slots[fmt.Sprintf("0:%d", tc.source)] = target
			inv.Slots[fmt.Sprintf("0:%d", tc.material)] = mat
			aikenR142Save(t, g, inv)
			if tc.expansion != 0 {
				if e := g.Settings.Save(context.Background(), repo.SettingsRecord{Scope: repo.CharacterContainerStateScope("19"), Values: map[string]string{"main_list_param16": strconv.Itoa(int(tc.expansion)), "avatar_list_param16": "0", "personal_cargo_list_param16": "8"}}); e != nil {
					t.Fatal(e)
				}
			}
			if e := aikenR142Request(t, s, session, tc.source); e != nil {
				t.Fatal(e)
			}
			aikenR142Outcome(t, session, tc.success)
			after := aikenR142Load(t, g)
			if !tc.success {
				if !reflect.DeepEqual(inv, after) {
					t.Fatal("rejection mutated inventory")
				}
				return
			}
			got := after.Slots[fmt.Sprintf("0:%d", tc.material)]
			if got.Count != 89 || got.RawEntry[100] != 0xa8 || got.Extra["count_or_instance"] != "765" {
				t.Fatal("quantity or opaque data wrong")
			}
			for _, k := range []string{"amount_or_count", "amount", "count", "stack", "quantity"} {
				if got.Extra[k] != "89" {
					t.Fatalf("stale alias %s=%s", k, got.Extra[k])
				}
			}
			for _, k := range []string{"raw_entry_hex", "current_raw_entry_77", "raw_entry_77"} {
				b, e := hex.DecodeString(got.Extra[k])
				if e != nil || binary.LittleEndian.Uint32(b[6:10]) != 89 || b[100] != 0xa8 {
					t.Fatalf("stale raw alias %s", k)
				}
			}
		})
	}
}

func TestAikenR142RejectIsAtomic(t *testing.T) {
	for _, name := range []string{"shortage_after_partial", "locked_source", "locked_partial_material", "expired_source", "wrong_owner", "source_count2", "wrong_template", "invalid_option0", "invalid_option1", "top_grade", "invalid_grade", "raw_id_mismatch", "raw_short", "tail_conflict", "tail_short", "mode1_conflict", "endpoint_alias", "duplicate_bag", "duplicate_warehouse", "duplicate_account", "duplicate_worn"} {
		t.Run(name, func(t *testing.T) {
			s, session, g, _, _ := aikenR142Fixture(t, "")
			ctx := context.Background()
			inv := aikenR142Load(t, g)
			x := inv.Slots["0:9"]
			m := inv.Slots["0:121"]
			switch name {
			case "shortage_after_partial":
				m.Count = 5
				inv.Slots["0:122"] = repo.ItemStack{ItemID: m.ItemID, Count: 5}
			case "locked_source":
				x.Extra["equipment_lock_state"] = "1"
			case "locked_partial_material":
				m.Count = 5
				inv.Slots["0:122"] = repo.ItemStack{ItemID: m.ItemID, Count: 100, Extra: map[string]string{"equipment_lock_state": "1"}}
			case "expired_source":
				x.ExpireAt = time.Unix(1, 0)
			case "wrong_owner":
				ch, _, _ := g.Character.Load(ctx, "19")
				ch.AccountID = "other"
				if e := g.Character.Save(ctx, ch); e != nil {
					t.Fatal(e)
				}
			case "source_count2":
				x.Count = 2
			case "wrong_template":
				x.ItemID = 10161536
				binary.LittleEndian.PutUint32(x.RawEntry[2:], uint32(x.ItemID))
			case "invalid_option0":
				x.RawEntry[0x72] = 8
			case "invalid_option1":
				x.RawEntry[0x73] = 255
			case "top_grade":
				x.RawEntry[0x76] = 3
			case "invalid_grade":
				x.RawEntry[0x76] = 255
			case "raw_id_mismatch":
				binary.LittleEndian.PutUint32(x.RawEntry[2:], 1)
			case "raw_short":
				x.RawEntry = x.RawEntry[:118]
			case "tail_conflict":
				x.Extra["tail72"] = "01197ea501"
			case "tail_short":
				x.Extra["tail72"] = "0119"
			case "mode1_conflict":
				x.Extra["mode1_raw_e_hex"] = "01197ea501"
			case "endpoint_alias":
				inv.Slots["00:09"] = x
			default:
				x.Extra["native_buff_instance"] = "test-instance"
				dup := repo.ItemStack{ItemID: x.ItemID, Count: 1, Extra: map[string]string{"native_buff_instance": "test-instance"}}
				switch name {
				case "duplicate_bag":
					inv.Slots["0:10"] = dup
				case "duplicate_warehouse":
					inv.Warehouse = map[string]repo.ItemStack{"2:0": dup}
				case "duplicate_account":
					if e := g.AccountInventory.Save(ctx, repo.AccountInventoryRecord{AccountID: "account-1", Slots: map[string]repo.ItemStack{"2:0": dup}}); e != nil {
						t.Fatal(e)
					}
				case "duplicate_worn":
					if e := g.Equipment.Save(ctx, repo.EquipmentRecord{CharacterID: "19", Entries: map[string]repo.EquipmentEntry{"0": {SlotIndex: 0, ItemID: x.ItemID, Extra: dup.Extra}}}); e != nil {
						t.Fatal(e)
					}
				}
			}
			inv.Slots["0:9"] = x
			inv.Slots["0:121"] = m
			aikenR142Save(t, g, inv)
			char, _, _ := g.Character.Load(ctx, "19")
			account, _, _ := g.AccountInventory.Load(ctx, "account-1")
			equip, _, _ := g.Equipment.Load(ctx, "19")
			other, _, _ := g.Inventory.Load(ctx, "20")
			if e := aikenR142Request(t, s, session, 9); e != nil {
				t.Fatal(e)
			}
			aikenR142Outcome(t, session, false)
			after := aikenR142Load(t, g)
			c, _, _ := g.Character.Load(ctx, "19")
			a, _, _ := g.AccountInventory.Load(ctx, "account-1")
			eq, _, _ := g.Equipment.Load(ctx, "19")
			o, _, _ := g.Inventory.Load(ctx, "20")
			if !reflect.DeepEqual(inv, after) || !reflect.DeepEqual(char, c) || !reflect.DeepEqual(account, a) || !reflect.DeepEqual(equip, eq) || !reflect.DeepEqual(other, o) {
				t.Fatal("rejected operation changed assets")
			}
		})
	}
}

type aikenR142FaultUOW struct {
	repo.AccountCharacterAssetUnitOfWork
	failSave bool
	entered  *int
	saved    *int
	deadline *bool
}

func (u aikenR142FaultUOW) WithinAccountCharacterAssets(ctx context.Context, a, c string, f func(repo.AccountInventoryRepository, repo.CharacterRepository, repo.InventoryRepository, repo.EquipmentRepository) error) error {
	_, *u.deadline = ctx.Deadline()
	return u.AccountCharacterAssetUnitOfWork.WithinAccountCharacterAssets(ctx, a, c, func(ar repo.AccountInventoryRepository, cr repo.CharacterRepository, ir repo.InventoryRepository, er repo.EquipmentRepository) error {
		*u.entered++
		if u.failSave {
			ir = aikenR142FaultInventory{ir, u.saved}
		}
		if e := f(ar, cr, ir, er); e != nil {
			return e
		}
		return errors.New("isolated precommit fault")
	})
}

type aikenR142FaultInventory struct {
	repo.InventoryRepository
	calls *int
}

func (i aikenR142FaultInventory) Save(context.Context, repo.InventoryRecord) error {
	*i.calls++
	return errors.New("isolated save fault")
}
func TestAikenR142SaveAndCommitRollback(t *testing.T) {
	for _, save := range []bool{true, false} {
		t.Run(fmt.Sprintf("save=%v", save), func(t *testing.T) {
			s, session, g, _, _ := aikenR142Fixture(t, "")
			provided := g
			entered, saved := 0, 0
			deadline := false
			provided.AccountAssets = aikenR142FaultUOW{g.AccountAssets, save, &entered, &saved, &deadline}
			s.repositoryProvider = func() (repo.Group, bool) { return provided, true }
			before := aikenR142Load(t, g)
			if e := aikenR142Request(t, s, session, 9); e != nil {
				t.Fatal(e)
			}
			aikenR142Outcome(t, session, false)
			if entered != 1 || !deadline || save && saved != 1 {
				t.Fatalf("fault not reached: %d %d deadline=%v", entered, saved, deadline)
			}
			if !reflect.DeepEqual(before, aikenR142Load(t, g)) {
				t.Fatal("failed transaction persisted assets")
			}
		})
	}
}

type aikenR142FailWrite struct {
	bufferConn
	writes, fail int
}

func (c *aikenR142FailWrite) Write(b []byte) (int, error) {
	c.writes++
	if c.writes == c.fail {
		return 0, errors.New("isolated socket fault")
	}
	return c.bufferConn.Write(b)
}
func TestAikenR142NotifyFailureDoesNotRecommit(t *testing.T) {
	for _, fail := range []int{1, 2} {
		t.Run(fmt.Sprintf("write%d", fail), func(t *testing.T) {
			s, session, g, _, _ := aikenR142Fixture(t, "")
			conn := &aikenR142FailWrite{fail: fail}
			session.conn = conn
			if e := aikenR142Request(t, s, session, 9); e == nil {
				t.Fatal("missing write failure")
			}
			after := aikenR142Load(t, g)
			if after.Slots["0:121"].Count != 89 || after.Slots["0:9"].RawEntry[0x76] != 1 || conn.writes != fail {
				t.Fatal("wrong asset commit or extra retry")
			}
			if fail == 1 && conn.write.Len() != 0 {
				t.Fatal("ACK after inventory failure")
			}
			if fail == 2 {
				p, rest := splitGameServerUpperPacket(t, conn.write.Bytes())
				if p.Header.MsgID != 14 || len(rest) != 0 {
					t.Fatal("unexpected successful ACK")
				}
			}
		})
	}
}

func TestAikenR142RepeatedClicksChargeEachCurrentGrade(t *testing.T) {
	s, session, g, _, _ := aikenR142Fixture(t, "")
	for i, want := range []int64{89, 71, 37, 37} {
		session.conn.(*bufferConn).write.Reset()
		if e := aikenR142Request(t, s, session, 9); e != nil {
			t.Fatal(e)
		}
		aikenR142Outcome(t, session, i < 3)
		after := aikenR142Load(t, g)
		if after.Slots["0:121"].Count != want {
			t.Fatal("same legal click swallowed or top grade charged")
		}
	}
}

func TestAikenR142AllRealTemplates(t *testing.T) {
	ids := []int64{100300856, 100200602, 100230541, 100220536, 100210567, 100240376, 100390016, 100050746, 100080591, 100070568, 100060616, 100090403, 100352922, 100100658, 100130573, 100120561, 100110602, 100140398, 100322439, 100250621, 100280536, 100270532, 100260583, 100290389, 100150627, 100180541, 100170536, 100160543, 100190369, 100344655, 100312549}
	if len(ids) != 31 {
		t.Fatal("expected all31 observed templates")
	}
	for _, id := range ids {
		for grade := byte(0); grade < 3; grade++ {
			t.Run(fmt.Sprintf("%d/grade%d", id, grade), func(t *testing.T) {
				s, session, g, _, _ := aikenR142Fixture(t, "")
				inv := aikenR142Load(t, g)
				x := inv.Slots["0:9"]
				x.ItemID = id
				binary.LittleEndian.PutUint32(x.RawEntry[2:], uint32(id))
				x.RawEntry[0x76] = grade
				inv.Slots["0:9"] = x
				aikenR142Save(t, g, inv)
				if e := aikenR142Request(t, s, session, 9); e != nil {
					t.Fatal(e)
				}
				aikenR142Outcome(t, session, true)
				after := aikenR142Load(t, g)
				if after.Slots["0:9"].RawEntry[0x76] != grade+1 || after.Slots["0:121"].Count != 100-[]int64{11, 18, 34}[grade] {
					t.Fatal("wrong grade/material")
				}
			})
		}
	}
}

func TestAikenR142EveryOptionByteInBothPositions(t *testing.T) {
	valid := map[byte]bool{}
	for _, v := range []byte{1, 2, 3, 4, 5, 6, 7, 11, 12, 13, 14, 21, 22, 23, 24, 25} {
		valid[v] = true
	}
	for position := 0; position < 2; position++ {
		for option := 0; option < 256; option++ {
			t.Run(fmt.Sprintf("option%d/%d", position, option), func(t *testing.T) {
				s, session, g, _, _ := aikenR142Fixture(t, "")
				before := aikenR142Load(t, g)
				x := before.Slots["0:9"]
				x.RawEntry[0x72+position] = byte(option)
				before.Slots["0:9"] = x
				aikenR142Save(t, g, before)
				if e := aikenR142Request(t, s, session, 9); e != nil {
					t.Fatal(e)
				}
				aikenR142Outcome(t, session, valid[byte(option)])
				after := aikenR142Load(t, g)
				if valid[byte(option)] {
					if after.Slots["0:9"].RawEntry[0x72+position] != byte(option) || after.Slots["0:9"].RawEntry[0x76] != 1 {
						t.Fatal("option not preserved")
					}
				} else if !reflect.DeepEqual(before, after) {
					t.Fatal("invalid option mutated assets")
				}
			})
		}
	}
}

func TestAikenR142CanonicalAliasesAndColdProjection(t *testing.T) {
	for _, name := range []string{"all_aliases", "raw_id_zero", "tail_metadata_only", "mode1_metadata_only", "exact_material", "split_material_skip_locked"} {
		t.Run(name, func(t *testing.T) {
			s, session, g, _, _ := aikenR142Fixture(t, "")
			ctx := context.Background()
			inv := aikenR142Load(t, g)
			x := inv.Slots["0:9"]
			tail := append([]byte(nil), x.RawEntry[0x72:]...)
			switch name {
			case "all_aliases":
				x.RawEntry[0x1a] = 0x5a
				x.RawEntry[0x24] = 0x6b
				x.RawEntry[0x38] = 0x7c
				for _, k := range []string{"raw_entry_hex", "current_raw_entry_77", "raw_entry_77"} {
					x.Extra[k] = "0x" + strings.ToUpper(hex.EncodeToString(x.RawEntry))
				}
				for _, k := range []string{"tail_data_72", "tail72", "mode1_raw_e_hex", "mode1_state_188_192_raw_hex"} {
					x.Extra[k] = hex.EncodeToString(tail)
				}
			case "raw_id_zero":
				binary.LittleEndian.PutUint32(x.RawEntry[2:], 0)
			case "tail_metadata_only", "mode1_metadata_only":
				x.RawEntry = nil
				x.Extra["quality_seed"] = "203004656"
				x.Extra["durability"] = "28"
				if name == "tail_metadata_only" {
					x.Extra["tail_data_72"] = hex.EncodeToString(tail)
				} else {
					x.Extra["mode1_raw_e_hex"] = hex.EncodeToString(tail)
				}
			case "exact_material":
				m := inv.Slots["0:121"]
				m.Count = 11
				inv.Slots["0:121"] = m
			case "split_material_skip_locked":
				m := inv.Slots["0:121"]
				m.Count = 5
				inv.Slots["0:121"] = m
				inv.Slots["0:122"] = repo.ItemStack{ItemID: m.ItemID, Count: 99, Extra: map[string]string{"equipment_lock_state": "1"}}
				inv.Slots["0:123"] = repo.ItemStack{ItemID: m.ItemID, Count: 6}
			}
			inv.Slots["0:9"] = x
			aikenR142Save(t, g, inv)
			if e := aikenR142Request(t, s, session, 9); e != nil {
				t.Fatal(e)
			}
			aikenR142Outcome(t, session, true)
			after := aikenR142Load(t, g)
			x = after.Slots["0:9"]
			tail[4] = 1
			if !bytes.Equal(x.RawEntry[0x72:], tail) {
				t.Fatal("raw not coherent")
			}
			for _, k := range []string{"raw_entry_hex", "current_raw_entry_77", "raw_entry_77"} {
				if v := x.Extra[k]; v != "" {
					b, e := hex.DecodeString(v)
					if e != nil || !bytes.Equal(b[0x72:], tail) {
						t.Fatal("raw alias not coherent")
					}
				}
			}
			for _, k := range []string{"tail_data_72", "tail72", "mode1_raw_e_hex", "mode1_state_188_192_raw_hex"} {
				if v := x.Extra[k]; v != "" && v != hex.EncodeToString(tail) {
					t.Fatal("tail not coherent")
				}
			}
			if name == "all_aliases" && (x.RawEntry[0x1a] != 0x5a || x.RawEntry[0x24] != 0x6b || x.RawEntry[0x38] != 0x7c) {
				t.Fatal("enchant/socket/opaque bytes changed")
			}
			if name == "exact_material" {
				if _, ok := after.Slots["0:121"]; ok {
					t.Fatal("exact consumption left material")
				}
			}
			if name == "split_material_skip_locked" {
				if _, ok := after.Slots["0:121"]; ok {
					t.Fatal("partial still present")
				}
				if after.Slots["0:122"].Count != 99 {
					t.Fatal("locked material consumed")
				}
				if _, ok := after.Slots["0:123"]; ok {
					t.Fatal("second material still present")
				}
			}
			// Isolated repository roundtrip of the committed instance into a worn row.
			// This checks existing cold projection; it is not a live equip/GUI assertion.
			if e := g.Equipment.Save(ctx, repo.EquipmentRecord{CharacterID: "19", Entries: map[string]repo.EquipmentEntry{"13": {SlotIndex: 13, ItemID: x.ItemID, Bind: x.Bind, ExpireAt: x.ExpireAt, RawEntry: x.RawEntry, Extra: x.Extra}}}); e != nil {
				t.Fatal(e)
			}
			reader := csharpLegacyUserInfoReader{ctx: ctx, service: s, characterID: "19"}
			rows := reader.currentMode1EquipmentObjectRows()
			if len(rows) != 1 || !bytes.Equal(rows[0].lateRawE, tail) {
				t.Fatalf("mode1 cold projection tail: %+v", rows)
			}
			var w packetWriter
			writeCurrentMode1EquipmentCreateRow(&w, rows[0])
			wire := w.bytes()
			if !bytes.Equal(wire[len(wire)-9:], append([]byte{5, 0, 0, 0}, tail...)) {
				t.Fatal("mode1 serialized carrier differs")
			}
		})
	}
}
