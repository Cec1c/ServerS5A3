package dnfbridge

import (
	"bytes"
	"context"
	"os"
	"reflect"
	"testing"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
	"longheng.io/server/internal/modules/dnf/dnfenum"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/repository/memory"
	"longheng.io/server/internal/modules/dnf/skillcmd"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

// 当前EXE的295cc10扫描6..11，295ccd0扫描0..11，295e560从12起枚举普通技能。
// 旧持久化的6号位是被动树区，198号位才是第七个主动，不能直接下发。
func TestCurrentSkillQuickbarProjectionUsesContiguousTwelveSlots(t *testing.T) {
	record := dnfrepo.SkillRecord{
		Skills:  map[int64]dnfrepo.SkillState{1: {Level: 1}, 12: {Level: 30}, 65: {Level: 2}, 88: {Level: 3}},
		Layouts: map[int]dnfrepo.SkillLayout{0: {0: 1, 6: 12, 198: 65, 203: 88}, 1: {7: 12, 199: 65}},
		Trees: dnfrepo.SkillTreeState{Expanded: true, Other: dnfrepo.SkillProfile{
			Skills: map[int64]dnfrepo.SkillState{12: {Level: 1}, 65: {Level: 1}},
		}},
	}
	before := dnfrepo.CloneSkill(record)
	body, quick, err := buildCurrentSceneSkillInfoBody(record, record.Layouts[0], nil)
	if err != nil {
		t.Fatal(err)
	}
	want := map[int]uint16{0: 1, 12: 12, 6: 65, 11: 88}
	if got := currentSkillInfoFirstTreeSlots(t, body); !reflect.DeepEqual(got, want) {
		t.Fatalf("被动或后六格的90版位置错误: got=%v want=%v", got, want)
	}
	if !reflect.DeepEqual(quick, []int{0, 6, 11}) {
		t.Fatalf("快捷栏没有包括后六格: %v", quick)
	}
	_, outer := consumeCurrentSkillInfoFields(t, body[4:])
	_, other := consumeCurrentSkillInfoFields(t, outer[2][1])
	otherSlots := map[int]uint16{}
	for _, row := range other[3] {
		fields, _ := consumeCurrentSkillInfoFields(t, row)
		otherSlots[int(fields[1][0])] = uint16(fields[2][0])
	}
	if !reflect.DeepEqual(otherSlots, map[int]uint16{13: 12, 7: 65}) {
		t.Fatalf("非活动页位置未按同一契约转换: %v", otherSlots)
	}
	if !reflect.DeepEqual(record, before) {
		t.Fatal("协议投影修改了持久化布局或加点")
	}
}

func TestCurrentSkillQuickbarBuyAndDragResponsesUseWireSlots(t *testing.T) {
	body, err := skillcmd.BuildBuySkillSuccess(skillcmd.MutationResult{
		Entries: []skillcmd.MutationEntry{{Slot: 6, SkillID: 12, Level: 30}, {Slot: 198, SkillID: 65, Level: 2}},
	})
	if err != nil {
		t.Fatal(err)
	}
	conn := &bufferConn{}
	service := &Service{options: options{gameUpperBodyCodec: gameUpperBodyCodecPlain}}
	session := &gameSession{conn: conn}
	result := alignedcmd.Result{Operation: "buy_skill", UpperResponses: []alignedcmd.UpperResponse{{
		MsgID: uint16(dnfenum.CmdPacketBuySkill), Classification: 1, Body: body,
	}}}
	if err := service.sendAlignedUpperResponses(session, result); err != nil {
		t.Fatal(err)
	}
	packet, rest := splitGameServerUpperPacket(t, conn.write.Bytes())
	if len(rest) != 0 || packet.Body[7] != 12 || packet.Body[12] != 6 {
		t.Fatalf("学习回包仍使用旧槽位: %x", packet.Body)
	}
	if body[7] != 6 || body[12] != 198 {
		t.Fatal("投影意外原地修改了领域应答")
	}
	conn.write.Reset()
	result = alignedcmd.Result{Operation: "change_skill_slot", UpperResponses: []alignedcmd.UpperResponse{{
		MsgID: uint16(dnfenum.CmdPacketChangeSkillslot), Classification: 1, Body: []byte{1, 0, 198, 6},
	}}}
	if err := service.sendAlignedUpperResponses(session, result); err != nil {
		t.Fatal(err)
	}
	packet, _ = splitGameServerUpperPacket(t, conn.write.Bytes())
	if !reflect.DeepEqual(packet.Body, []byte{1, 0, 6, 12}) {
		t.Fatalf("拖动ACK没有回显客户端槽位: %x", packet.Body)
	}
}

func TestCurrentSkillQuickbarDragConvertsBeforeTransactionOnEitherPage(t *testing.T) {
	ctx := context.Background()
	catalog, err := buildSkillCatalogFromSource(ctx, initialEquipmentMemSource{
		"skill/skilllist.lst":   "0 `job0.lst`\n",
		"skill/job0.lst":        "46 `job0/active.skl`\n",
		"skill/job0/active.skl": "[type]\n`[active]`\n",
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, tree := range []byte{0, 1} {
		repos := memory.NewMemoryGroup()
		character := dnfrepo.CharacterRecord{CharacterID: "31", AccountID: "audit", Job: "0", Level: 1}
		if err := repos.Character.Save(ctx, character); err != nil {
			t.Fatal(err)
		}
		record := dnfrepo.SkillRecord{
			CharacterID: "31", Skills: map[int64]dnfrepo.SkillState{46: {Level: 1, Enabled: true}},
			Points:  dnfrepo.SkillPointState{TotalSP: 100, RemainingSP: 100, SyncedLevel: 1},
			Layouts: map[int]dnfrepo.SkillLayout{0: {198: 46}, 1: {198: 46}},
			Trees: dnfrepo.SkillTreeState{Expanded: true, Active: tree, Other: dnfrepo.SkillProfile{
				Skills: map[int64]dnfrepo.SkillState{46: {Level: 1, Enabled: true}},
				Points: dnfrepo.SkillPointState{TotalSP: 100, RemainingSP: 100, SyncedLevel: 1},
			}},
		}
		if err := repos.Skill.Save(ctx, record); err != nil {
			t.Fatal(err)
		}
		conn := &bufferConn{}
		service := &Service{
			options:            options{accountID: "audit", gameUpperBodyCodec: gameUpperBodyCodecPlain},
			repositoryProvider: func() (dnfrepo.Group, bool) { return repos, true },
			skillCatalog:       catalog, initialSkillsByJob: map[byte][]initialSkillEntry{0: {{SkillID: 46, Level: 1}}},
			initialSPTable: map[int]int{1: 100},
		}
		request := []byte{tree, 6, 12, 255, 255, 255, 255, 0}
		before := append([]byte(nil), request...)
		handled, err := service.handleAlignedGameCommand(&gameSession{conn: conn, selectedCharacterID: 31},
			byte(dnfenum.GameCmdCommand), uint16(dnfenum.CmdPacketChangeSkillslot), request)
		if err != nil || !handled {
			t.Fatalf("第%d页拖动失败: handled=%t err=%v", tree, handled, err)
		}
		packet, rest := splitGameServerUpperPacket(t, conn.write.Bytes())
		if len(rest) != 0 || !bytes.Equal(packet.Body, []byte{1, tree, 6, 12}) {
			t.Fatalf("拖动未回显线上位置: %x", packet.Body)
		}
		cold, _, err := repos.Skill.Load(ctx, "31")
		if err != nil || !reflect.DeepEqual(cold.Layouts[int(tree)], dnfrepo.SkillLayout{6: 46}) {
			t.Fatalf("第%d页没有按领域位置落库: %+v err=%v", tree, cold.Layouts, err)
		}
		if !reflect.DeepEqual(cold.Layouts[int(tree^1)], record.Layouts[int(tree^1)]) ||
			!reflect.DeepEqual(cold.Skills, record.Skills) || cold.Points != record.Points || !reflect.DeepEqual(cold.Trees, record.Trees) {
			t.Fatal("位置互换修改了另一页、加点或SP/TP")
		}
		if !bytes.Equal(request, before) {
			t.Fatal("请求原始字节被原地修改")
		}
	}
}

func TestCurrentSkillQuickbarMappingIsBijectiveAndBounded(t *testing.T) {
	seen := map[int]bool{}
	for stored := 0; stored < 204; stored++ {
		wire, err := currentSkillSlotToWire(stored)
		if err != nil || seen[wire] {
			t.Fatalf("槽位映射重复或失败: %d -> %d err=%v", stored, wire, err)
		}
		seen[wire] = true
		back, err := currentSkillSlotFromWire(wire)
		if err != nil || back != stored {
			t.Fatalf("拖动无法逆转: %d -> %d -> %d err=%v", stored, wire, back, err)
		}
	}
	for _, slot := range []int{-1, 204, 255, 512} {
		if _, err := currentSkillSlotToWire(slot); err == nil {
			t.Fatalf("放行非法领域槽位%d", slot)
		}
		if _, err := currentSkillSlotFromWire(slot); err == nil {
			t.Fatalf("放行非法客户端槽位%d", slot)
		}
	}
}

func TestCurrentSkillQuickbarResponsePreservesCommandsFailuresAndSentinel(t *testing.T) {
	body, err := skillcmd.BuildBuySkillSuccess(skillcmd.MutationResult{SkillTree: 1, FinalMode: 3,
		Entries: []skillcmd.MutationEntry{{Slot: 198, SkillID: 65, Level: 1, CommandData: []byte{0, 198, 255}}, {Slot: 6, SkillID: 12, Level: 30}, {Slot: 255, SkillID: 200, Level: 1}}})
	if err != nil {
		t.Fatal(err)
	}
	want := append([]byte(nil), body...)
	want[7], want[16] = 6, 12
	got, err := currentSkillSlotResponseBody(alignedcmd.UpperResponse{MsgID: 29, Classification: 1, Body: body})
	if err != nil || !bytes.Equal(got, want) {
		t.Fatalf("指令/哨兵/尾标被破坏: got=%x want=%x err=%v", got, want, err)
	}
	for _, r := range []alignedcmd.UpperResponse{
		{MsgID: 29, Classification: 1, Body: []byte{0, 19}},
		{MsgID: 28, Classification: 1, Body: []byte{0, 19}},
		{MsgID: 29, Classification: 0, Body: []byte{1, 198}},
		{MsgID: 331, Classification: 1, Body: []byte{1, 198}},
	} {
		got, err := currentSkillSlotResponseBody(r)
		if err != nil || !bytes.Equal(got, r.Body) {
			t.Fatalf("转换影响了其他包或失败包: %+v", r)
		}
	}
	for _, bad := range [][]byte{{1}, {1, 0, 0, 0, 0, 0, 1, 198, 1}, {1, 0, 0, 0, 0, 0, 1, 198, 1, 0, 1, 1, 9, 0}} {
		if _, err := currentSkillSlotResponseBody(alignedcmd.UpperResponse{MsgID: 29, Classification: 1, Body: bad}); err == nil {
			t.Fatalf("未拒绝残缺学习回包: %x", bad)
		}
	}
}

func TestRealPVFCurrentSkillQuickbarShortSwordPassiveAndTwelveActives(t *testing.T) {
	pvfPath := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if pvfPath == "" {
		t.Skip("设置真实PVF路径后验证短剑精通和十二个主动技能")
	}
	archive, err := platformpvf.LoadArchive(platformpvf.Options{Path: pvfPath})
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := buildSkillCatalogFromSource(context.Background(), archive)
	if err != nil {
		t.Fatal(err)
	}
	passive, ok := catalog.Find(0, 12)
	if !ok || passive.Active || passive.Kind != "[passive]" || len(passive.GrowTypes) <= 2 {
		t.Fatalf("实际短剑精通分类不符: %+v", passive)
	}
	states := map[int64]dnfrepo.SkillState{12: {Level: 1, Enabled: true}}
	activeCount := 0
	for _, definition := range catalog.SkillsForJob(0) {
		if definition.Active {
			states[int64(definition.ID)] = dnfrepo.SkillState{Level: 1, Enabled: true}
			activeCount++
			if activeCount == 12 {
				break
			}
		}
	}
	if activeCount != 12 {
		t.Fatal("真实PVF主动技能不足十二个")
	}
	layout, err := skillcmd.BuildCurrentSkillLayout(catalog, 0, 0, states, nil)
	if err != nil {
		t.Fatal(err)
	}
	record := dnfrepo.SkillRecord{Skills: states, Layouts: map[int]dnfrepo.SkillLayout{0: layout}}
	before := dnfrepo.CloneSkill(record)
	body, quick, err := buildCurrentSceneSkillInfoBody(record, layout, nil)
	if err != nil {
		t.Fatal(err)
	}
	wire := currentSkillInfoFirstTreeSlots(t, body)
	if wire[12] != 12 || len(quick) != 12 {
		t.Fatalf("实际短剑精通进入快捷栏或十二格不完整: wire=%v quick=%v", wire, quick)
	}
	for slot := 0; slot < 12; slot++ {
		definition, ok := catalog.Find(0, wire[slot])
		if !ok || !definition.Active {
			t.Fatalf("快捷栏%d实际为非主动技能: %+v", slot, definition)
		}
	}
	if !reflect.DeepEqual(before, record) {
		t.Fatal("真实PVF投影修改了源布局或技能")
	}
	t.Logf("真实短剑精通id=12，原存储位置6映射为普通区12；十二个主动覆盖线上0..11，SP/TP与原布局未改")
}
