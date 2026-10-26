package dnfbridge

import (
	"bytes"
	"context"
	"encoding/binary"
	"reflect"
	"strconv"
	"testing"

	aiken "longheng.io/server/internal/modules/dnf/aikenupgrade"
	proto "longheng.io/server/internal/modules/dnf/protocol"
	repo "longheng.io/server/internal/modules/dnf/repository"
)

func aikenR193Fixture(t *testing.T) (*Service, *gameSession, repo.Group) {
	t.Helper()
	s, g, repos, _, _ := aikenR142Fixture(t, "")
	if err := repos.Settings.Save(context.Background(), repo.SettingsRecord{Scope: repo.CharacterContainerStateScope("19"), Values: map[string]string{"main_list_param16": "24", "avatar_list_param16": "0", "personal_cargo_list_param16": "8"}}); err != nil {
		t.Fatal(err)
	}
	inv := aikenR142Load(t, repos)
	for slot, id := range map[int]int64{9: 100120561, 10: 100220536, 11: 100352922} {
		raw := make([]byte, 119)
		binary.LittleEndian.PutUint16(raw, uint16(slot))
		binary.LittleEndian.PutUint32(raw[2:], uint32(id))
		binary.LittleEndian.PutUint32(raw[6:], 554884773)
		raw[0x76] = 3
		inv.Slots["0:"+strconv.Itoa(slot)] = repo.ItemStack{ItemID: id, Count: 1, RawEntry: raw, Extra: map[string]string{"item_kind": "equipment", "native_buff_instance": strconv.Itoa(slot), "quality_seed": "554884773"}}
	}
	delete(inv.Slots, "0:25")
	inv.Slots["0:158"] = repo.ItemStack{ItemID: 10161537, Count: 5000}
	aikenR142Save(t, repos, inv)
	return s, g, repos
}

func TestAikenR193RealSetCreateReplaceAndRelog(t *testing.T) {
	s, g, repos := aikenR193Fixture(t)
	before := aikenR142Load(t, repos)
	rules, err := currentAikenSetRules(s.pvfItemCatalog)
	if err != nil {
		t.Fatal(err)
	}
	items := []repo.ItemStack{before.Slots["0:9"], before.Slots["0:10"], before.Slots["0:11"]}
	plan, err := rules.Plan(items, []byte{34})
	if err != nil {
		t.Fatal(err)
	}
	if plan.Record.Record0 != 1 || plan.Record.Record1 != 3 || plan.Record.Record18 != 3 || len(plan.Costs) != 1 || plan.Costs[0].Count != 118 {
		t.Fatalf("unexpected real plan %+v", plan)
	}
	body := []byte{1, 34, 3, 9, 0, 10, 0, 11, 0}
	aikenSetDispatch(t, s, g, 1341, body)
	packets := splitAllGameServerUpperPackets(t, g.conn.(*bufferConn).write.Bytes())
	if len(packets) != 3 || packets[0].Header.MsgID != 14 || packets[1].Header.MsgID != 1278 || packets[2].Header.MsgID != 1341 || packets[2].Header.Classification != 1 || !bytes.Equal(packets[2].Body, []byte{1}) {
		t.Fatalf("set must publish item, record, then success: %+v", packets)
	}
	plan.Record.Selector = uint32(g.selectedCharacterID)
	if !bytes.Equal(packets[1].Body, plan.Record.Encode()) {
		t.Fatalf("set record differs from committed plan: %x", packets[1].Body)
	}
	after := aikenR142Load(t, repos)
	if after.Slots["0:158"].Count != 5000-118 {
		t.Fatalf("charge %+v", after.Slots["0:158"])
	}
	for _, key := range []string{"0:9", "0:10", "0:11"} {
		r, ok, e := currentAikenReadSet(after.Slots[key])
		if e != nil || !ok || r.Record0 != 1 || r.Slots[0] != 34 {
			t.Fatalf("%s %+v %v %v", key, r, ok, e)
		}
	}
	g.conn.(*bufferConn).write.Reset()
	if err := s.sendCurrentAikenSetSnapshot(context.Background(), g); err != nil {
		t.Fatal(err)
	}
	packets = splitAllGameServerUpperPackets(t, g.conn.(*bufferConn).write.Bytes())
	if len(packets) != 1 || packets[0].Header.MsgID != 1278 || !bytes.Equal(packets[0].Body, plan.Record.Encode()) {
		t.Fatalf("snapshot must restore one deduplicated record: %+v", packets)
	}
	g.conn.(*bufferConn).write.Reset()
	aikenSetDispatch(t, s, g, 1341, body)
	final := aikenR142Load(t, repos)
	// 118 - trunc(float32(118)*0.9) = 12, not another full price.
	if final.Slots["0:158"].Count != 5000-118-12 {
		t.Fatalf("replacement charge %+v", final.Slots["0:158"])
	}
}

func TestAikenR193RealSetInsufficientAndMalformed(t *testing.T) {
	for _, name := range []string{"insufficient", "same_group", "missing_target", "duplicate_target", "invalid_option", "too_many_options"} {
		t.Run(name, func(t *testing.T) {
			s, g, repos := aikenR193Fixture(t)
			inv := aikenR142Load(t, repos)
			body := []byte{1, 34, 3, 9, 0, 10, 0, 11, 0}
			switch name {
			case "insufficient":
				delete(inv.Slots, "0:158")
			case "same_group":
				body = []byte{2, 34, 35, 3, 9, 0, 10, 0, 11, 0}
			case "missing_target":
				delete(inv.Slots, "0:11")
			case "duplicate_target":
				body[7] = 10
			case "invalid_option":
				body[1] = 255
			case "too_many_options":
				body = []byte{5, 34, 56, 100, 122, 152, 3, 9, 0, 10, 0, 11, 0}
			}
			aikenR142Save(t, repos, inv)
			before := aikenR142Load(t, repos)
			aikenSetDispatch(t, s, g, 1341, body)
			packets := splitAllGameServerUpperPackets(t, g.conn.(*bufferConn).write.Bytes())
			code := byte(1)
			if name == "insufficient" {
				code = 8
			}
			if len(packets) != 1 || packets[0].Header.MsgID != 1341 || packets[0].Header.Classification != 1 || !bytes.Equal(packets[0].Body, []byte{0, code}) {
				t.Fatalf("reject emitted asset notifications or wrong ACK: %+v", packets)
			}
			if !reflect.DeepEqual(before, aikenR142Load(t, repos)) {
				t.Fatal("invalid request changed assets")
			}
		})
	}
}

func TestAikenR193RealCatalogCostRange(t *testing.T) {
	text, err := aikenR142Archive(t).ReadText("etc/customability/partsetcustominfo.etc")
	if err != nil {
		t.Fatal(err)
	}
	c, err := aiken.ParseSetCatalog(text)
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Spend) != 252 || len(c.Points) != 1 {
		t.Fatalf("catalog sizes %+v", c)
	}
	for grade := 0; grade < 4; grade++ {
		for count := 3; count <= 6; count++ {
			for option := range c.Spend {
				_, err := c.PlanCost(1, grade, count, []byte{option})
				if err != nil && err != aiken.ErrInsufficientSetPoints {
					t.Fatalf("grade=%d count=%d option=%d: %v", grade, count, option, err)
				}
			}
		}
	}
}

func aikenSetDispatch(t *testing.T, s *Service, session *gameSession, opcode uint16, body []byte) {
	t.Helper()
	frame, err := proto.BuildChannelPacket(opcode, body, 1, 1)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.handleGameUpper(session, frame); err != nil {
		t.Fatal(err)
	}
}
