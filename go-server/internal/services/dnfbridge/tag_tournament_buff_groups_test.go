package dnfbridge

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/buffswap"
	repo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/skillcmd"
	"longheng.io/server/internal/modules/dnf/tagtournament"
)

// The original red diagnostic is preserved in tag-buff-groups-linux01/source.
// The paired path must project the selected group rather than the last edit.
// It checks the server body, not native cast behavior or actual buff strength.
func TestTagTournamentBuffGroupsCourageGroupZeroSurvivesOtherGroupEdit(t *testing.T) {
	const courage, otherSkill uint16 = 14, 10
	for _, tc := range []struct {
		name string
		last byte
	}{{"group0_last_control", 0}, {"group1_last_regression", 1}} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			s, p, g := tagPlayFixture(t)
			ch, found, err := g.Character.Load(ctx, "60")
			if err != nil || !found {
				t.Fatalf("load secondary actor: found=%t err=%v", found, err)
			}
			ch.Job = "14" // female priest; synthetic character, not player data
			ch.Stats["grow_type"] = 0x21
			if err := g.Character.Save(ctx, ch); err != nil {
				t.Fatal(err)
			}
			// Skill 10 is only a distinct learned fixture skill. This test does
			// not identify it as a hymn or infer any PVF effect from its number.
			skills := repo.SkillRecord{CharacterID: ch.CharacterID, Skills: map[int64]repo.SkillState{
				int64(courage): {Level: 1}, int64(otherSkill): {Level: 1},
			}, Layouts: map[int]repo.SkillLayout{0: {198: courage, 199: otherSkill}}}
			if err := g.Skill.Save(ctx, skills); err != nil {
				t.Fatal(err)
			}
			if err := g.Inventory.Save(ctx, repo.InventoryRecord{CharacterID: ch.CharacterID, Slots: map[string]repo.ItemStack{
				"0:21": {ItemID: 501, Count: 1}, "0:22": {ItemID: 502, Count: 1},
			}}); err != nil {
				t.Fatal(err)
			}
			selected := [2]uint16{courage, otherSkill}
			for group, skill := range selected {
				if err := buffswap.ApplyCurrentGroup(ctx, g, ch.CharacterID, p.accountID, byte(group), buffswap.Command{
					Skill: skill, Slot: 12, List: 0, Index: uint16(21 + group),
				}); err != nil {
					t.Fatal(err)
				}
			}
			// Only edit order changes: both selected groups and both owned
			// item trees remain identical in the control and regression cases.
			if err := buffswap.ApplyCurrentGroup(ctx, g, ch.CharacterID, p.accountID, tc.last, buffswap.Command{
				Skill: selected[tc.last], Slot: 33, List: 44, Index: 0xffff,
			}); err != nil {
				t.Fatal(err)
			}
			state, found, err := buffswap.Load(ctx, g, ch.CharacterID, p.accountID)
			if err != nil || !found || state.SelectedSkills[0] != courage || state.SelectedSkills[1] != otherSkill ||
				state.CurrentGroup != tc.last || state.Skill != selected[tc.last] || len(state.SkillSlots) != 2 {
				t.Fatalf("invalid two-group precondition: found=%t state=%+v err=%v", found, state, err)
			}
			for group, skill := range selected {
				binding, ok := state.SkillSlots[skill][12]
				if !ok || binding.List != 0 || binding.Index != uint16(21+group) || binding.ItemID != int64(501+group) || binding.Instance == "" {
					t.Fatalf("group%d lacks owned registration: %+v", group, binding)
				}
			}
			if state.SkillSlots[courage][12].Instance == state.SkillSlots[otherSkill][12].Instance ||
				!reflect.DeepEqual(state.Slots, state.SkillSlots[selected[tc.last]]) {
				t.Fatal("fixture has aliased instances or an inconsistent compatibility tree")
			}
			// Verify that group0 can resolve its own item independently. This
			// rules out a missing/stale binding as the reason for missing 14.
			group0, err := buffswap.LoadResolvedOwnedGroup(ctx, g, ch.CharacterID, p.accountID, 0)
			if err != nil || group0.State.Skill != courage || len(group0.Items) != 1 || group0.Items[12].Binding.ItemID != 501 {
				t.Fatalf("group0 source unavailable: %+v err=%v", group0, err)
			}
			beforeSettings, found, err := g.Settings.Load(ctx, buffswap.Scope(ch.CharacterID))
			if err != nil || !found {
				t.Fatalf("settings before: found=%t err=%v", found, err)
			}
			beforeInventory, found, err := g.Inventory.Load(ctx, ch.CharacterID)
			if err != nil || !found {
				t.Fatalf("inventory before: found=%t err=%v", found, err)
			}
			selectedCharacter, generation := p.selectedCharacterID, p.characterGeneration
			// Supply the learned page at this function's boundary; do not
			// replace the production buff loader or serializer with a stub.
			row := currentTagTournamentCharacterProjection{character: ch, skillRecord: skills, job: 14, grow: 0x21,
				skills: []currentTagTournamentSkillWire{{slot: 6, id: courage, level: 1}, {slot: 7, id: otherSkill, level: 1}}}
			group := byte(0)
			body, source, err := s.currentTagTournamentBuffSwapBodyForGroup(ctx, p, row, &group)
			if err != nil {
				t.Fatal(err)
			}
			// One ordinary bag row: u16 skill/u8 count/slot/list/u16 index/
			// 119-byte full item. No avatar optional blobs in this fixture.
			if len(body) != 3+4+119 || body[2] != 1 {
				t.Fatalf("unexpected body shape: len=%d hex=%x", len(body), body)
			}
			wireSkill := binary.LittleEndian.Uint16(body[:2])
			wireSlot, wireList := body[3], body[4]
			wireIndex := binary.LittleEndian.Uint16(body[5:7])
			itemIndex := binary.LittleEndian.Uint16(body[7:9])
			itemID := binary.LittleEndian.Uint32(body[9:13])
			t.Logf("actor=%s selected[0]=%d selected[1]=%d currentGroup=%d compatibilitySkill=%d sourceSkill=%d wireSkill=%d slot=%d list=%d index=%d itemIndex=%d itemID=%d body=%x",
				ch.CharacterID, state.SelectedSkills[0], state.SelectedSkills[1], state.CurrentGroup, state.Skill, source.State.Skill,
				wireSkill, wireSlot, wireList, wireIndex, itemIndex, itemID, body)
			afterSettings, found, err := g.Settings.Load(ctx, buffswap.Scope(ch.CharacterID))
			if err != nil || !found || !reflect.DeepEqual(beforeSettings, afterSettings) {
				t.Fatalf("projection changed settings: found=%t err=%v", found, err)
			}
			afterInventory, found, err := g.Inventory.Load(ctx, ch.CharacterID)
			if err != nil || !found || !reflect.DeepEqual(beforeInventory, afterInventory) || p.selectedCharacterID != selectedCharacter || p.characterGeneration != generation {
				t.Fatalf("projection changed inventory/session: found=%t err=%v", found, err)
			}
			if wireSkill != courage || wireSlot != 12 || wireList != 0 || wireIndex != 21 || itemIndex != 21 || itemID != 501 {
				t.Errorf("group0 Courage registration lost after editing group%d: wire skill=%d slot=%d list=%d index=%d itemIndex=%d itemID=%d; want skill=14 slot=12 list=0 index=21 itemIndex=21 itemID=501", tc.last, wireSkill, wireSlot, wireList, wireIndex, itemIndex, itemID)
			}
		})
	}
}

// Exercise the production caller, not a direct skillState serializer call:
// creation sends the paired registration once; landing and tagging reuse it.
func TestTagTournamentBuffGroupsProductionEntry(t *testing.T) {
	t.Setenv("DNFBRIDGE_BUFF_GROUP_SYNC", "on")
	s, p, g, _ := tagBuffGroupsFixture(t)
	ctx := context.Background()
	identity, bound := s.boundGameSessionCharacterSnapshot(p)
	if !bound {
		t.Fatal("fixture has no bound login identity")
	}
	var savedSettings [2]repo.SettingsRecord
	var savedInventory [2]repo.InventoryRecord
	for i, id := range []string{"29", "60"} {
		var found bool
		var err error
		savedSettings[i], found, err = g.Settings.Load(ctx, buffswap.Scope(id))
		if err != nil || !found {
			t.Fatalf("settings actor%s: found=%t err=%v", id, found, err)
		}
		savedInventory[i], found, err = g.Inventory.Load(ctx, id)
		if err != nil || !found {
			t.Fatalf("inventory actor%s: found=%t err=%v", id, found, err)
		}
	}
	conn := p.conn.(*bufferConn)
	conn.write.Reset()
	if err := s.handleCurrentTagTournamentEnter(p, []byte{byte(tagtournament.Normal)}); err != nil {
		t.Fatal(err)
	}
	b := p.tagTournamentBattle
	if b == nil || b.prepared == nil {
		t.Fatal("production entry did not create a prepared battle")
	}
	prepared := b.prepared
	// Marshal detached sources now, so subsequent in-place mutation cannot
	// be hidden by a shallow copy of slice/map fields in buffSources.
	frozenSources, err := json.Marshal(prepared.skillState.buffSources)
	if err != nil {
		t.Fatal(err)
	}
	frozenBody := append([]byte(nil), prepared.skillState.buffSwap...)
	seen := map[uint16]int{}
	var emitted []byte
	for rest := append([]byte(nil), conn.write.Bytes()...); len(rest) != 0; {
		packet, tail := splitCurrentGameServerUpperPacketAuto(t, rest)
		seen[packet.Header.MsgID]++
		if packet.Header.MsgID == 1393 {
			if packet.Header.Classification != 0 {
				t.Fatal("1393 must use class0")
			}
			emitted = append([]byte(nil), packet.Body...)
		}
		if packet.Header.MsgID == 1458 && !bytes.Equal(packet.Body, []byte{1}) {
			t.Fatalf("entry rejected: %x", packet.Body)
		}
		rest = tail
	}
	if seen[1393] != 1 || seen[1389] != 1 || seen[1390] != 1 || seen[1458] != 1 {
		t.Fatalf("production entry packets=%v", seen)
	}
	if !bytes.Equal(emitted, frozenBody) {
		t.Fatal("actual conn1393 differs from prepared body")
	}
	want := []tagBuffGroupWire{
		{0, 38, 1, 12, 0, 21, 21, 500}, {128, 39, 1, 12, 0, 22, 22, 501},
		{1, 14, 1, 12, 0, 21, 21, 510}, {129, 10, 1, 12, 0, 22, 22, 511},
	}
	if decoded := decodeTagBuffGroups(t, emitted); !reflect.DeepEqual(decoded, want) {
		t.Fatalf("conn1393 groups=%+v want=%+v", decoded, want)
	}
	for actor, id := range []string{"29", "60"} {
		if len(prepared.skillState.buffSources[actor]) != 2 {
			t.Fatal("prepared actor lacks two group sources")
		}
		for group := 0; group < 2; group++ {
			source := prepared.skillState.buffSources[actor][group]
			item, ok := source.Items[12]
			w := want[actor*2+group]
			if source.Owner.CharacterID != id || source.State.Skill != w.skill || !ok || !item.HasCurrentStack || item.Binding.Instance == "" || item.Binding.ItemID != int64(w.itemID) {
				t.Fatalf("prepared actor/group source mismatch: actor=%s group=%d source=%+v", id, group, source)
			}
			entry := currentItemListEntryFromStack(0, int16(w.index), item.CurrentStack)
			// count + preceding127-byte entries + role/skill/count + row header
			off := 1 + (actor*2+group)*127 + 8
			if !bytes.Equal(emitted[off:off+119], entry.data[:]) {
				t.Fatalf("conn full119 differs from owned item actor=%s group=%d", id, group)
			}
		}
	}
	assertSourcesKept := func(stage string) {
		t.Helper()
		if p.tagTournamentBattle != b || b.prepared != prepared {
			t.Fatalf("%s replaced battle/prepared", stage)
		}
		sources, err := json.Marshal(prepared.skillState.buffSources)
		if err != nil || !bytes.Equal(sources, frozenSources) || !bytes.Equal(prepared.skillState.buffSwap, frozenBody) {
			t.Fatalf("%s altered retained buff state: %v", stage, err)
		}
		if !s.boundGameSessionCharacterCurrent(identity) {
			t.Fatalf("%s changed login identity", stage)
		}
		for i, id := range []string{"29", "60"} {
			settings, found, err := g.Settings.Load(ctx, buffswap.Scope(id))
			if err != nil || !found || !reflect.DeepEqual(settings, savedSettings[i]) {
				t.Fatalf("%s changed actor%s buff settings: %v", stage, id, err)
			}
			inv, found, err := g.Inventory.Load(ctx, id)
			if err != nil || !found || !reflect.DeepEqual(inv, savedInventory[i]) {
				t.Fatalf("%s changed actor%s inventory: %v", stage, id, err)
			}
		}
	}
	assertNoRebuild := func(stage string, wantTag *byte) {
		t.Helper()
		tags := 0
		for rest := append([]byte(nil), conn.write.Bytes()...); len(rest) != 0; {
			packet, tail := splitCurrentGameServerUpperPacketAuto(t, rest)
			switch packet.Header.MsgID {
			case 1389, 1390, 1391, 1392, 1393:
				t.Fatalf("%s resent actor/skill initialization opcode%d", stage, packet.Header.MsgID)
			case 1402:
				tags++
				if wantTag == nil || packet.Header.Classification != 0 || !bytes.Equal(packet.Body, []byte{*wantTag}) {
					t.Fatalf("%s wrong switch notification: %x", stage, packet.Body)
				}
			}
			rest = tail
		}
		if wantTag != nil && tags != 1 {
			t.Fatalf("%s switch notification count=%d", stage, tags)
		}
		assertSourcesKept(stage)
	}
	assertSourcesKept("entry")
	conn.write.Reset()
	if err := s.handleDungeonSelectUpper(p, encodeCurrentDungeonSelectRequest(prepared.runtime.Request)); err != nil {
		t.Fatal(err)
	}
	if !prepared.entered || p.dungeon.runtime != prepared.runtime {
		t.Fatal("selectUpper did not enter prepared runtime")
	}
	// Landing explicitly chooses the initial active actor through 1402;
	// this is not a recreation of either actor's Buff registration.
	initialActor := byte(b.run.Snapshot().Active)
	assertNoRebuild("selectUpper", &initialActor)
	// Follow the adjacent tagPlay flow into the first fighting room before
	// switching. Advance only the battle's clock for the return cooldown.
	tagPlayAdvanceClock(t, s, p)
	conn.write.Reset()
	tagPlayMoveNext(t, s, p)
	assertNoRebuild("moveNext", nil)
	now := b.now()
	b.now = func() time.Time { return now }
	for _, target := range []byte{1, 0} {
		conn.write.Reset()
		if err := s.handleCurrentTagTournamentTag(p, []byte{target}); err != nil {
			t.Fatal(err)
		}
		if b.run.Snapshot().Active != int(target) {
			t.Fatalf("switch target%d not active: %+v", target, b.run.Snapshot())
		}
		assertNoRebuild(fmt.Sprintf("tag%d", target), &target)
		now = now.Add(5 * time.Second)
	}
}

func tagBuffGroupsFixture(t *testing.T) (*Service, *gameSession, repo.Group, [2]currentTagTournamentCharacterProjection) {
	t.Setenv("DNFBRIDGE_TAG_BUFF_GROUP_SYNC", "on")
	t.Helper()
	ctx := context.Background()
	s, p, g := tagPlayFixture(t)
	for actor, id := range []string{"29", "60"} {
		selected := [2]uint16{38, 39}
		if actor == 1 {
			selected = [2]uint16{14, 10}
			ch, found, err := g.Character.Load(ctx, id)
			if err != nil || !found {
				t.Fatalf("actor load: found=%t err=%v", found, err)
			}
			ch.Job, ch.Stats["grow_type"] = "14", 0x21
			if err := g.Character.Save(ctx, ch); err != nil {
				t.Fatal(err)
			}
		}
		sk := repo.SkillRecord{CharacterID: id, Skills: map[int64]repo.SkillState{
			int64(selected[0]): {Level: 1}, int64(selected[1]): {Level: 2},
		}, Layouts: map[int]repo.SkillLayout{0: {198: selected[0], 199: selected[1]}}}
		if err := g.Skill.Save(ctx, sk); err != nil {
			t.Fatal(err)
		}
		inv := repo.InventoryRecord{CharacterID: id, Slots: map[string]repo.ItemStack{}}
		for item := 0; item < 3; item++ {
			inv.Slots[fmt.Sprintf("0:%d", 21+item)] = repo.ItemStack{ItemID: int64(500 + actor*10 + item), Count: 1}
		}
		if err := g.Inventory.Save(ctx, inv); err != nil {
			t.Fatal(err)
		}
		for group, skill := range selected {
			if err := buffswap.ApplyCurrentGroup(ctx, g, id, p.accountID, byte(group), buffswap.Command{Skill: skill, Slot: 12, List: 0, Index: uint16(21 + group)}); err != nil {
				t.Fatal(err)
			}
		}
	}
	rows, err := s.prepareCurrentTagTournamentCharacters(ctx, p, p.tagTournamentSentRosterCharactersForTest(t, g))
	if err != nil {
		t.Fatal(err)
	}
	return s, p, g, rows
}

type tagBuffGroupWire struct {
	role             byte
	skill            uint16
	count            byte
	slot, list       byte
	index, itemIndex uint16
	itemID           uint32
}

// Fixtures contain at most one list0 item per entry, with no optional blobs.
func decodeTagBuffGroups(t *testing.T, body []byte) []tagBuffGroupWire {
	t.Helper()
	if len(body) == 0 {
		t.Fatal("missing 1393 count")
	}
	var rows []tagBuffGroupWire
	off := 1
	for i := 0; i < int(body[0]); i++ {
		if len(body)-off < 4 {
			t.Fatal("truncated role/skill/count")
		}
		r := tagBuffGroupWire{role: body[off], skill: binary.LittleEndian.Uint16(body[off+1:]), count: body[off+3]}
		off += 4
		switch r.count {
		case 0:
		case 1:
			if len(body)-off < 123 {
				t.Fatal("truncated full item")
			}
			r.slot, r.list = body[off], body[off+1]
			r.index = binary.LittleEndian.Uint16(body[off+2:])
			r.itemIndex = binary.LittleEndian.Uint16(body[off+4:])
			r.itemID = binary.LittleEndian.Uint32(body[off+6:])
			if r.list != 0 {
				t.Fatal("decoder fixture must use ordinary list0")
			}
			off += 123
		default:
			t.Fatalf("unexpected fixture row count %d", r.count)
		}
		rows = append(rows, r)
	}
	if off != len(body) {
		t.Fatalf("1393 trailing bytes: consumed=%d len=%d", off, len(body))
	}
	return rows
}

func TestTagTournamentBuffGroupsFourEntriesIndependentOfEditOrder(t *testing.T) {
	t.Setenv("DNFBRIDGE_BUFF_GROUP_SYNC", "on")
	s, p, g, rows := tagBuffGroupsFixture(t)
	ctx := context.Background()
	var baseline []byte
	var baselineSources [2][]buffswap.OwnedProjection
	for _, last := range []byte{0, 1} {
		for _, row := range rows {
			state, _, err := buffswap.Load(ctx, g, row.character.CharacterID, p.accountID)
			if err != nil {
				t.Fatal(err)
			}
			if err := buffswap.ApplyCurrentGroup(ctx, g, row.character.CharacterID, p.accountID, last, buffswap.Command{Skill: state.SelectedSkills[last], Slot: 33, List: 44, Index: 0xffff}); err != nil {
				t.Fatal(err)
			}
		}
		var settings [2]repo.SettingsRecord
		var inventories [2]repo.InventoryRecord
		for i, row := range rows {
			var err error
			settings[i], _, err = g.Settings.Load(ctx, buffswap.Scope(row.character.CharacterID))
			if err != nil {
				t.Fatal(err)
			}
			inventories[i], _, err = g.Inventory.Load(ctx, row.character.CharacterID)
			if err != nil {
				t.Fatal(err)
			}
		}
		identity := s.boundGameSessionCharacterSnapshotForTagBuffTest(p)
		got, err := s.prepareCurrentTagTournamentSkillState(ctx, p, rows)
		if err != nil {
			t.Fatal(err)
		}
		want := []tagBuffGroupWire{
			{0, 38, 1, 12, 0, 21, 21, 500}, {128, 39, 1, 12, 0, 22, 22, 501},
			{1, 14, 1, 12, 0, 21, 21, 510}, {129, 10, 1, 12, 0, 22, 22, 511},
		}
		if decoded := decodeTagBuffGroups(t, got.buffSwap); !reflect.DeepEqual(decoded, want) {
			t.Fatalf("last=%d decoded=%+v want=%+v", last, decoded, want)
		}
		if baseline != nil && !bytes.Equal(baseline, got.buffSwap) {
			t.Fatal("1393 depends on last edit order")
		}
		if baseline != nil && !reflect.DeepEqual(baselineSources, got.buffSources) {
			t.Fatal("detached group sources depend on last edit order")
		}
		baseline = append([]byte(nil), got.buffSwap...)
		baselineSources = got.buffSources
		for i, row := range rows {
			after, _, err := g.Settings.Load(ctx, buffswap.Scope(row.character.CharacterID))
			if err != nil || !reflect.DeepEqual(after, settings[i]) {
				t.Fatalf("actor%d settings changed: %v", i, err)
			}
			inv, _, err := g.Inventory.Load(ctx, row.character.CharacterID)
			if err != nil || !reflect.DeepEqual(inv, inventories[i]) {
				t.Fatalf("actor%d inventory changed: %v", i, err)
			}
			if len(got.buffSources[i]) != 2 {
				t.Fatal("missing per-group sources")
			}
			for group, source := range got.buffSources[i] {
				if source.Owner.CharacterID != row.character.CharacterID || source.State.Skill != want[2*i+group].skill {
					t.Fatal("source owner/group mismatch")
				}
			}
		}
		if identity != s.boundGameSessionCharacterSnapshotForTagBuffTest(p) {
			t.Fatal("session identity changed")
		}
	}
	// Domain roster order need not equal the native root/secondary order.
	reversed := [2]currentTagTournamentCharacterProjection{rows[1], rows[0]}
	got, err := s.prepareCurrentTagTournamentSkillState(ctx, p, reversed)
	if err != nil {
		t.Fatal(err)
	}
	decoded := decodeTagBuffGroups(t, got.buffSwap)
	for i, role := range []byte{1, 129, 0, 128} {
		if decoded[i].role != role {
			t.Fatalf("reversed role%d=%d want=%d", i, decoded[i].role, role)
		}
	}
}

func (s *Service) boundGameSessionCharacterSnapshotForTagBuffTest(p *gameSession) [2]uint64 {
	return [2]uint64{uint64(p.selectedCharacterID), uint64(p.characterGeneration)}
}

func TestTagTournamentBuffGroupsEmptyEntriesAndLegacyGate(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		t.Run(fmt.Sprintf("paired_%t", enabled), func(t *testing.T) {
			t.Setenv("DNFBRIDGE_BUFF_GROUP_SYNC", fmt.Sprint(enabled))
			s, p, g, rows := tagBuffGroupsFixture(t)
			ctx := context.Background()
			// Opposite empty groups still require explicit entries: native
			// group0 clears the actor, and group1 must not erase group0.
			for i, row := range rows {
				if err := buffswap.ApplyCurrentGroup(ctx, g, row.character.CharacterID, p.accountID, byte(i), buffswap.Command{Slot: 33, List: 44, Index: 0xffff}); err != nil {
					t.Fatal(err)
				}
			}
			got, err := s.prepareCurrentTagTournamentSkillState(ctx, p, rows)
			if err != nil {
				t.Fatal(err)
			}
			if enabled {
				want := []tagBuffGroupWire{{role: 0}, {128, 39, 1, 12, 0, 22, 22, 501}, {1, 14, 1, 12, 0, 21, 21, 510}, {role: 129}}
				if decoded := decodeTagBuffGroups(t, got.buffSwap); !reflect.DeepEqual(decoded, want) {
					t.Fatalf("empty groups=%+v", decoded)
				}
			} else {
				if !bytes.Equal(got.buffSwap, []byte{2, 0, 0, 0, 0, 1, 0, 0, 0}) {
					t.Fatalf("legacy empty wire changed: %x", got.buffSwap)
				}
			}
		})
	}
	for _, version := range []string{"official-24.9", "official-24.8", "", " official-24.9 "} {
		for _, setting := range []string{"on", "off", ""} {
			t.Run(version+"/"+setting, func(t *testing.T) {
				t.Setenv("DNFBRIDGE_BUFF_GROUP_SYNC", setting)
				t.Setenv("DNFBRIDGE_TAG_BUFF_GROUP_SYNC", "on")
				s := &Service{options: options{currentClientVersion: version}}
				if got, want := s.currentTagTournamentBuffGroupsEnabled(), version == "official-24.9" && setting == "on"; got != want {
					t.Fatalf("gate=%t want=%t", got, want)
				}
			})
		}
	}
}

func TestTagTournamentBuffGroupsDisabledRetainsLegacyItems(t *testing.T) {
	t.Setenv("DNFBRIDGE_BUFF_GROUP_SYNC", "off")
	s, p, _, rows := tagBuffGroupsFixture(t)
	got, err := s.prepareCurrentTagTournamentSkillState(context.Background(), p, rows)
	if err != nil {
		t.Fatal(err)
	}
	want := []tagBuffGroupWire{{0, 39, 1, 12, 0, 22, 22, 501}, {1, 10, 1, 12, 0, 22, 22, 511}}
	if decoded := decodeTagBuffGroups(t, got.buffSwap); !reflect.DeepEqual(decoded, want) {
		t.Fatalf("legacy=%+v", decoded)
	}
	var expected packetWriter
	expected.writeByte(2)
	for i, row := range rows {
		body, _, err := s.currentTagTournamentBuffSwapBody(context.Background(), p, row)
		if err != nil {
			t.Fatal(err)
		}
		expected.writeByte(byte(i))
		expected.writeBytes(body)
	}
	if !bytes.Equal(got.buffSwap, expected.bytes()) {
		t.Fatal("legacy full-item bytes changed")
	}
}

func TestTagTournamentBuffGroupsRejectUnlearnedInactiveSelection(t *testing.T) {
	t.Setenv("DNFBRIDGE_BUFF_GROUP_SYNC", "on")
	for _, group := range []byte{0, 1} {
		t.Run(fmt.Sprintf("group%d", group), func(t *testing.T) {
			s, p, g, rows := tagBuffGroupsFixture(t)
			ctx := context.Background()
			if err := buffswap.ApplyCurrentGroup(ctx, g, "60", p.accountID, group, buffswap.Command{Skill: 77, Slot: 12, List: 0, Index: 23}); err != nil {
				t.Fatal(err)
			}
			other := byte(1) - group
			selected := [2]uint16{14, 10}
			if err := buffswap.ApplyCurrentGroup(ctx, g, "60", p.accountID, other, buffswap.Command{Skill: selected[other], Slot: 33, List: 44, Index: 0xffff}); err != nil {
				t.Fatal(err)
			}
			got, err := s.prepareCurrentTagTournamentSkillState(ctx, p, rows)
			if err == nil || !strings.Contains(err.Error(), "选定技能不在该队员当前页") || len(got.buffSwap) != 0 {
				t.Fatalf("inactive unlearned group accepted: err=%v body=%x", err, got.buffSwap)
			}
		})
	}
}

type tagBuffGroupChangingSettings struct {
	repo.SettingsRepository
	trigger string
	fired   bool
	change  func() error
}

func (r *tagBuffGroupChangingSettings) Load(ctx context.Context, scope string) (repo.SettingsRecord, bool, error) {
	if scope == r.trigger && !r.fired {
		r.fired = true
		if err := r.change(); err != nil {
			return repo.SettingsRecord{}, false, err
		}
	}
	return r.SettingsRepository.Load(ctx, scope)
}

func TestTagTournamentBuffGroupsRejectEachChangedSource(t *testing.T) {
	t.Setenv("DNFBRIDGE_BUFF_GROUP_SYNC", "on")
	for _, group := range []byte{0, 1} {
		for _, change := range []string{"binding", "clear", "empty_to_selected"} {
			t.Run(fmt.Sprintf("group%d_%s", group, change), func(t *testing.T) {
				s, p, base, rows := tagBuffGroupsFixture(t)
				ctx := context.Background()
				skill := uint16(38 + group)
				if change == "empty_to_selected" {
					if err := buffswap.ApplyCurrentGroup(ctx, base, "29", p.accountID, group, buffswap.Command{Slot: 33, List: 44, Index: 0xffff}); err != nil {
						t.Fatal(err)
					}
				}
				wrapper := &tagBuffGroupChangingSettings{SettingsRepository: base.Settings, trigger: skillcmd.SkillCommandScope("60")}
				wrapper.change = func() error {
					cmd := buffswap.Command{Skill: skill, Slot: 12, List: 0, Index: 23}
					if change == "clear" {
						cmd = buffswap.Command{Slot: 33, List: 44, Index: 0xffff}
					}
					return buffswap.ApplyCurrentGroup(ctx, base, "29", p.accountID, group, cmd)
				}
				wrapped := base
				wrapped.Settings = wrapper
				s.repositoryProvider = func() (repo.Group, bool) { return wrapped, true }
				got, err := s.prepareCurrentTagTournamentSkillState(ctx, p, rows)
				if !wrapper.fired || err == nil || !strings.Contains(err.Error(), "Buff换装资料变化") || len(got.buffSwap) != 0 {
					t.Fatalf("source drift not rejected: fired=%t err=%v body=%x", wrapper.fired, err, got.buffSwap)
				}
			})
		}
	}
}

// Seed the historical JSON directly: registration helpers would allocate an
// instance before the first projection and conceal this compatibility gap.
// Keep the identity assertions nonfatal so a missing first migration also
// records the actual second projection after the duplicate is introduced.
func TestTagTournamentBuffGroupsHistoricalIdentityLifecycle(t *testing.T) {
	t.Setenv("DNFBRIDGE_BUFF_GROUP_SYNC", "on")
	t.Setenv("DNFBRIDGE_TAG_BUFF_GROUP_SYNC", "on")
	for _, initiallyAmbiguous := range []bool{false, true} {
		name := "unique_A_then_add_same_template_B"
		if initiallyAmbiguous {
			name = "initial_same_template_duplicates_must_not_guess"
		}
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			s, p, g := tagPlayFixture(t)
			ch, found, err := g.Character.Load(ctx, "60")
			if err != nil || !found {
				t.Fatalf("character: found=%t err=%v", found, err)
			}
			ch.Job, ch.Stats["grow_type"] = "14", 0x21
			if err := g.Character.Save(ctx, ch); err != nil {
				t.Fatal(err)
			}
			if err := g.Skill.Save(ctx, repo.SkillRecord{CharacterID: "60", Skills: map[int64]repo.SkillState{14: {Level: 1}}, Layouts: map[int]repo.SkillLayout{0: {198: 14}}}); err != nil {
				t.Fatal(err)
			}
			// Prepare unrelated character data before seeding the old record,
			// so only the actual skillState path can perform its migration.
			rows, err := s.prepareCurrentTagTournamentCharacters(ctx, p, p.tagTournamentSentRosterCharactersForTest(t, g))
			if err != nil {
				t.Fatal(err)
			}
			const rawOldState = `{"wire_profile":"current1426-1368","skill":14,"slots":{"12":{"list":0,"index":21,"item_id":501}}}`
			originalSettings := repo.SettingsRecord{Scope: buffswap.Scope("60"), Values: map[string]string{"state": rawOldState, "source": "current_exe_994D20", "keep": "historical-marker"}}
			if err := g.Settings.Save(ctx, originalSettings); err != nil {
				t.Fatal(err)
			}
			originalInventory := repo.InventoryRecord{CharacterID: "60", Slots: map[string]repo.ItemStack{"0:21": {ItemID: 501, Count: 1}}}
			if initiallyAmbiguous {
				originalInventory.Slots["0:22"] = repo.ItemStack{ItemID: 501, Count: 1}
			}
			if err := g.Inventory.Save(ctx, originalInventory); err != nil {
				t.Fatal(err)
			}
			loadRaw := func() (repo.SettingsRecord, buffswap.State, repo.InventoryRecord) {
				t.Helper()
				record, found, err := g.Settings.Load(ctx, buffswap.Scope("60"))
				if err != nil || !found {
					t.Fatalf("settings: found=%t err=%v", found, err)
				}
				var state buffswap.State
				if err := json.Unmarshal([]byte(record.Values["state"]), &state); err != nil {
					t.Fatal(err)
				}
				inv, found, err := g.Inventory.Load(ctx, "60")
				if err != nil || !found {
					t.Fatalf("inventory: found=%t err=%v", found, err)
				}
				return record, state, inv
			}
			beforeSettings, beforeState, beforeInventory := loadRaw()
			if beforeState.SkillSlots != nil || beforeState.SelectedSkills != nil || beforeState.Slots[12].Instance != "" || beforeState.Slots[12].SourceKind != "" || beforeInventory.Slots["0:21"].Extra["native_buff_instance"] != "" {
				t.Fatal("historical fixture already migrated")
			}
			first, err := s.prepareCurrentTagTournamentSkillState(ctx, p, rows)
			if err != nil {
				t.Fatal(err)
			}
			firstWire := decodeTagBuffGroups(t, first.buffSwap)
			if len(firstWire) != 4 || len(first.buffSources[1]) != 2 {
				t.Fatal("missing paired entries/sources")
			}
			afterSettings, afterState, afterInventory := loadRaw()
			source := first.buffSources[1][0]
			if source.Owner.CharacterID != "60" || source.State.Skill != 14 || firstWire[2].role != 1 || firstWire[2].skill != 14 || firstWire[3] != (tagBuffGroupWire{role: 129}) {
				t.Fatal("historical skill/actor or empty group1 mapping changed")
			}
			if initiallyAmbiguous {
				if firstWire[2].count != 0 || len(source.Items) != 0 || len(source.State.Slots) != 0 || source.Omitted != 1 {
					t.Fatalf("ambiguous old binding guessed: wire=%+v source=%+v", firstWire[2], source)
				}
				if !reflect.DeepEqual(beforeSettings, afterSettings) || !reflect.DeepEqual(beforeInventory, afterInventory) {
					t.Fatal("ambiguous binding or inventory was mutated")
				}
				return
			}
			want := tagBuffGroupWire{1, 14, 1, 12, 0, 21, 21, 501}
			if firstWire[2] != want {
				t.Fatalf("first projection lost unique A: got=%+v want=%+v", firstWire[2], want)
			}
			binding, bindingFound := source.State.Slots[12]
			item, itemFound := source.Items[12]
			if !bindingFound || !itemFound || binding.ItemID != 501 || binding.List != 0 || binding.Index != 21 || !item.HasCurrentStack || item.Binding != binding {
				t.Fatalf("first source is not A: %+v", source)
			}
			persisted := afterState.Slots[12]
			identity := binding.Instance
			t.Logf("first source identity=%q persisted identity=%q item identity=%q", identity, persisted.Instance, afterInventory.Slots["0:21"].Extra["native_buff_instance"])
			if identity == "" || binding.SourceKind != buffswap.SourceCurrentInstance || persisted.Instance != identity || persisted.SourceKind != buffswap.SourceCurrentInstance || persisted.ItemID != 501 || persisted.List != 0 || persisted.Index != 21 || afterInventory.Slots["0:21"].Extra["native_buff_instance"] != identity || item.CurrentStack.Extra["native_buff_instance"] != identity {
				t.Errorf("first paired projection must durably bind unique A: source=%+v persisted=%+v", binding, persisted)
			}
			if afterSettings.Values["keep"] != "historical-marker" {
				t.Error("migration lost unrelated setting")
			}
			// Add a fresh same-template B without copying A's metadata and
			// without registration, buff Load, or a migrating-loader shortcut.
			afterInventory.Slots["0:22"] = repo.ItemStack{ItemID: 501, Count: 1}
			if err := g.Inventory.Save(ctx, afterInventory); err != nil {
				t.Fatal(err)
			}
			second, err := s.prepareCurrentTagTournamentSkillState(ctx, p, rows)
			if err != nil {
				t.Fatal(err)
			}
			secondWire := decodeTagBuffGroups(t, second.buffSwap)
			if len(secondWire) != 4 || len(second.buffSources[1]) != 2 {
				t.Fatal("missing second paired entries/sources")
			}
			secondSource := second.buffSources[1][0]
			t.Logf("after B: wire=%+v source=%+v", secondWire[2], secondSource.State)
			if secondWire[2] != want || len(secondSource.Items) != 1 || identity == "" || secondSource.State.Slots[12].Instance != identity || secondSource.Items[12].Binding.Instance != identity {
				t.Errorf("adding B lost or replaced original A: wire=%+v source=%+v firstIdentity=%q", secondWire[2], secondSource.State, identity)
			}
			finalSettings, _, finalInventory := loadRaw()
			if !reflect.DeepEqual(afterSettings, finalSettings) || !reflect.DeepEqual(afterInventory, finalInventory) {
				t.Error("second projection rewrote registration/assets or assigned B an identity")
			}
		})
	}
}
