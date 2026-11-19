package dnfbridge

import (
	"context"
	"encoding/binary"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"longheng.io/server/internal/modules/dnf/buffswap"
	repo "longheng.io/server/internal/modules/dnf/repository"
)

type peerBuffViewMultiSkillFixture struct {
	repos   repo.Group
	service *Service
	session *gameSession
}

func newPeerBuffViewMultiSkillFixture(t *testing.T) peerBuffViewMultiSkillFixture {
	t.Helper()
	t.Setenv(buffSwapReapplyEnv, "on")
	base := newSkillSwitchInventoryRepos(t)
	service, session, _, _, _ := newSkillSwitchInventoryService(t, skillSwitchInventoryShapeGuardTier, base)
	ctx := context.Background()
	owner, found, err := base.Character.Load(ctx, "1")
	if err != nil || !found {
		t.Fatalf("load Buff target: found=%v err=%v", found, err)
	}
	owner.Name = "buff-target"
	if err := base.Character.Save(ctx, owner); err != nil {
		t.Fatal(err)
	}
	if err := base.Inventory.Save(ctx, repo.InventoryRecord{CharacterID: "1", Slots: map[string]repo.ItemStack{
		"0:54": {ItemID: 101040493, Count: 1},
		"0:55": {ItemID: 101040494, Count: 1},
		"0:56": {ItemID: 101040495, Count: 1},
	}}); err != nil {
		t.Fatal(err)
	}
	if err := base.Equipment.Save(ctx, repo.EquipmentRecord{CharacterID: "1"}); err != nil {
		t.Fatal(err)
	}
	if err := base.Skill.Save(ctx, repo.SkillRecord{CharacterID: "1", Skills: map[int64]repo.SkillState{
		38: {Level: 10},
		39: {Level: 11},
		40: {Level: 12},
	}}); err != nil {
		t.Fatal(err)
	}
	accountID := service.accountIDForSession(session)
	if err := buffswap.ApplyCurrentGroup(ctx, base, "1", accountID, 0, buffswap.Command{Skill: 38, Slot: 12, List: 0, Index: 54}); err != nil {
		t.Fatal(err)
	}
	if err := buffswap.ApplyCurrentGroup(ctx, base, "1", accountID, 1, buffswap.Command{Skill: 39, Slot: 12, List: 0, Index: 55}); err != nil {
		t.Fatal(err)
	}
	if err := buffswap.ApplyCurrent(ctx, base, "1", accountID, buffswap.Command{Skill: 40, Slot: 12, List: 0, Index: 56}); err != nil {
		t.Fatal(err)
	}
	if err := buffswap.ApplyCurrent(ctx, base, "1", accountID, buffswap.Command{Skill: 39, Slot: buffswap.SlotCount, List: 44, Index: 0xffff}); err != nil {
		t.Fatal(err)
	}
	state, found, err := buffswap.Load(ctx, base, "1", accountID)
	if err != nil || !found || state.Skill != 39 || len(state.SkillSlots) != 3 || state.SelectedSkills[0] != 38 || state.SelectedSkills[1] != 39 {
		t.Fatalf("multi-skill fixture state: found=%v err=%v state=%+v", found, err, state)
	}
	return peerBuffViewMultiSkillFixture{repos: base, service: service, session: session}
}

func (f peerBuffViewMultiSkillFixture) carrier(t *testing.T) []byte {
	t.Helper()
	detail := make([]byte, 15+8+currentMode1StatBlobWireSize+1)
	detail[0] = 3
	body := f.service.buildNativePeerBuffCarrier(context.Background(), f.session, 1, detail)
	if len(body) == 0 {
		t.Fatal("peer Buff carrier was not built")
	}
	return body
}

func (f peerBuffViewMultiSkillFixture) selectSkill(t *testing.T, skill uint16) {
	t.Helper()
	if err := buffswap.ApplyCurrent(context.Background(), f.repos, "1", f.service.accountIDForSession(f.session), buffswap.Command{
		Skill: skill,
		Slot:  buffswap.SlotCount,
		List:  44,
		Index: 0xffff,
	}); err != nil {
		t.Fatal(err)
	}
}

func peerBuffViewReadPage(t *testing.T, body []byte, offset *int) []peerSkillPageEntry {
	t.Helper()
	if *offset >= len(body) {
		t.Fatalf("missing Buff skill page at offset %d", *offset)
	}
	count := int(body[*offset])
	*offset = *offset + 1
	if *offset+count*3 > len(body) {
		t.Fatalf("truncated Buff skill page: offset=%d count=%d length=%d", *offset, count, len(body))
	}
	entries := make([]peerSkillPageEntry, 0, count)
	for index := 0; index < count; index++ {
		entries = append(entries, peerSkillPageEntry{
			SkillID: binary.LittleEndian.Uint16(body[*offset : *offset+2]),
			Level:   body[*offset+2],
		})
		*offset = *offset + 3
	}
	return entries
}

func peerBuffViewSkillPages(t *testing.T, body []byte) ([]peerSkillPageEntry, []peerSkillPageEntry) {
	t.Helper()
	const singleRegisteredItemRowWireSize = 126
	offset := currentMode1CreateRowsOffset + 4 + singleRegisteredItemRowWireSize + 8
	if offset >= len(body) || body[offset] != peerSkillPageSelectedResourceGroup {
		var suffix []byte
		if offset < len(body) {
			suffix = body[offset:]
		}
		t.Fatalf("Buff skill block prefix at %d=%x", offset, suffix)
	}
	offset++
	page0 := peerBuffViewReadPage(t, body, &offset)
	page1 := peerBuffViewReadPage(t, body, &offset)
	return page0, page1
}

func TestPeerBuffViewCarrierListsEveryRegisteredSkillForOtherPlayer(t *testing.T) {
	f := newPeerBuffViewMultiSkillFixture(t)
	body := f.carrier(t)
	page0, page1 := peerBuffViewSkillPages(t, body)
	want := []peerSkillPageEntry{{SkillID: 38, Level: 10}, {SkillID: 39, Level: 11}, {SkillID: 40, Level: 12}}
	if !reflect.DeepEqual(page0, want) || !reflect.DeepEqual(page1, want) {
		t.Fatalf("peer Buff skill pages: page0=%+v page1=%+v want=%+v", page0, page1, want)
	}

	itemOffset := currentMode1CreateRowsOffset + 4
	if itemOffset+13 > len(body) {
		t.Fatalf("Buff item row truncated at %d in body length %d", itemOffset, len(body))
	}
	if !reflect.DeepEqual(body[itemOffset:itemOffset+7], []byte{39, 0, 1, 12, 0, 55, 0}) ||
		binary.LittleEndian.Uint32(body[itemOffset+9:itemOffset+13]) != 101040494 {
		t.Fatalf("active Buff item row disagrees: %x", body[itemOffset:itemOffset+13])
	}
}

func TestPeerBuffViewMultiSkillPageSurvivesCarrierRefresh(t *testing.T) {
	f := newPeerBuffViewMultiSkillFixture(t)
	first := f.carrier(t)
	firstPage0, firstPage1 := peerBuffViewSkillPages(t, first)

	f.selectSkill(t, 40)
	refreshed := f.carrier(t)
	refreshedPage0, refreshedPage1 := peerBuffViewSkillPages(t, refreshed)
	if !reflect.DeepEqual(refreshedPage0, firstPage0) || !reflect.DeepEqual(refreshedPage1, firstPage1) {
		t.Fatalf("refresh dropped registered skills: before=%+v/%+v after=%+v/%+v", firstPage0, firstPage1, refreshedPage0, refreshedPage1)
	}

	itemOffset := currentMode1CreateRowsOffset + 4
	if itemOffset+13 > len(refreshed) {
		t.Fatalf("refreshed Buff item row truncated at %d in body length %d", itemOffset, len(refreshed))
	}
	if !reflect.DeepEqual(refreshed[itemOffset:itemOffset+7], []byte{40, 0, 1, 12, 0, 56, 0}) ||
		binary.LittleEndian.Uint32(refreshed[itemOffset+9:itemOffset+13]) != 101040495 {
		t.Fatalf("refreshed active Buff item row disagrees: %x", refreshed[itemOffset:itemOffset+13])
	}
}

func TestPeerBuffViewTextListsAlternateRegisteredSkills(t *testing.T) {
	f := newPeerBuffViewMultiSkillFixture(t)
	text := reviewOwnedSerializationText(t, f.carrier(t))
	for _, fragment := range []string{
		"buff-target 的 Buff 登记",
		"可查看 Buff 技能 3 个",
		"技能 38 · 基础 Lv10\r\n",
		"技能 39 · 基础 Lv11（当前）\r\n",
		"技能 40 · 基础 Lv12\r\n",
		"已登记 1 件装备",
		"仅供查看",
	} {
		if !strings.Contains(text, fragment) {
			t.Fatalf("peer Buff text missing %q: %q", fragment, text)
		}
	}
}

func TestPeerBuffViewTextGroupsEquipmentForEachRegisteredSkill(t *testing.T) {
	f := newPeerBuffViewMultiSkillFixture(t)
	if err := buffswap.ApplyCurrentGroup(context.Background(), f.repos, "1", f.service.accountIDForSession(f.session), 1, buffswap.Command{
		Skill: 39,
		Slot:  12,
		List:  0,
		Index: 54,
	}); err != nil {
		t.Fatal(err)
	}
	f.selectSkill(t, 39)
	ctx := context.Background()
	accountID := f.service.accountIDForSession(f.session)
	before, found, err := buffswap.Load(ctx, f.repos, "1", accountID)
	if err != nil || !found {
		t.Fatalf("load state before peer view: found=%v err=%v", found, err)
	}
	text := reviewOwnedSerializationText(t, f.carrier(t))
	after, found, err := buffswap.Load(ctx, f.repos, "1", accountID)
	if err != nil || !found || !reflect.DeepEqual(before, after) {
		t.Fatalf("peer view mutated target state: before=%+v after=%+v err=%v", before, after, err)
	}

	start38 := strings.Index(text, "技能 38 · 基础 Lv10")
	start39 := strings.Index(text, "技能 39 · 基础 Lv11")
	start40 := strings.Index(text, "技能 40 · 基础 Lv12")
	if start38 < 0 || start39 <= start38 || start40 <= start39 {
		t.Fatalf("peer Buff skill groups are not ordered sections: %q", text)
	}
	section38 := text[start38:start39]
	section39 := text[start39:start40]
	section40 := text[start40:]
	for name, section := range map[string]string{"skill38": section38, "skill39": section39} {
		if !strings.Contains(section, "已登记 1 件装备") || !strings.Contains(section, "装备 101040493") {
			t.Fatalf("%s did not retain the physical item shared across groups: %q", name, section)
		}
	}
	if !strings.Contains(section40, "已登记 1 件装备") || !strings.Contains(section40, "装备 101040495") {
		t.Fatalf("skill40 equipment section disagrees: %q", section40)
	}
}

func TestPeerBuffViewTextCompactsUnder8192WithoutDroppingSkillGroups(t *testing.T) {
	document := peerBuffViewTextDocument{
		CharacterName: strings.Repeat("长角色名称", 32),
		CurrentSkill:  137,
		CurrentName:   "不会进入紧凑格式的长技能名称",
	}
	for skillID := 1; skillID <= 256; skillID++ {
		skill := peerBuffViewTextSkill{
			SkillID: uint16(skillID),
			Name:    strings.Repeat("很长技能名称", 24),
			Level:   byte(skillID),
			Omitted: skillID % 3,
		}
		for slot := 1; slot <= buffswap.SlotCount; slot++ {
			skill.Items = append(skill.Items, peerBuffViewTextItem{
				ItemID: 900000000 + int64(skillID*100+slot),
				Name:   strings.Repeat("很长装备名称", 20),
			})
		}
		document.Skills = append(document.Skills, skill)
	}

	body := renderPeerBuffViewText(document)
	if len(body) > peerBuffViewTextLimit {
		t.Fatalf("compact peer Buff text length=%d exceeds %d", len(body), peerBuffViewTextLimit)
	}
	if !reflect.DeepEqual(body, renderPeerBuffViewText(document)) {
		t.Fatal("compact peer Buff text is not deterministic")
	}
	text := string(body)
	if !strings.Contains(text, "Buff compact 256") || !strings.Contains(text, "仅供查看") {
		t.Fatalf("compact peer Buff text header/footer missing: %q", text)
	}
	if strings.Contains(text, "很长装备名称") {
		t.Fatal("compact peer Buff text retained oversized display names")
	}

	seen := make(map[int]bool, len(document.Skills))
	for _, line := range strings.Split(text, "\r\n") {
		if !strings.HasPrefix(line, "S") {
			continue
		}
		parts := strings.SplitN(strings.TrimPrefix(line, "S"), "/", 2)
		skillID, err := strconv.Atoi(parts[0])
		if err != nil || skillID < 1 || skillID > len(document.Skills) || seen[skillID] {
			t.Fatalf("invalid or duplicate compact skill line %q", line)
		}
		seen[skillID] = true
		firstItemID := strconv.FormatInt(document.Skills[skillID-1].Items[0].ItemID, 10)
		if !strings.Contains(line, firstItemID) {
			t.Fatalf("skill %d lost its first item under budget: %q", skillID, line)
		}
	}
	if len(seen) != len(document.Skills) {
		t.Fatalf("compact text retained %d of %d skill groups", len(seen), len(document.Skills))
	}
	if !strings.Contains(text, "S137/L137*:") {
		t.Fatalf("current compact skill marker missing: %q", text)
	}
}
