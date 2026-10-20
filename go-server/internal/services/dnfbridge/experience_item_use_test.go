package dnfbridge

import (
	"bytes"
	"context"
	"encoding/binary"
	"os"
	"testing"

	"longheng.io/server/internal/modules/dnf/dnfenum"
	dnfexperienceitem "longheng.io/server/internal/modules/dnf/experienceitem"
	dnfproto "longheng.io/server/internal/modules/dnf/protocol"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	dnfrepomemory "longheng.io/server/internal/modules/dnf/repository/memory"

	platformpvf "longheng.io/server/internal/platform/pvf"
)

// 本文件锁死操作者报障「SP书无法使用」的服务端根因与修复。
//
// 根因：SP/TP 技能书的 `[stackable type]` 是 `[etc]`，而 op44（使用消耗品）
// 的 owner 契约要求 PVF 证明过的 `[waste]` 行（inventory/owner.go 的
// pvfProvenWasteStack），所以右键使用永远在"还没产生任何效果"之前就被拒；
// 同时当前 EXE 的真源命令名表把 ENUM_CMDPACKET_INCREASE_STATUS 注册成 id 30，
// 该命令在服务端**一条路由都没有**。
//
// 参考实现（ServerS4A21 ExperienceItemUseService / InventoryHandler.Experience）
// 正是用 INCREASE_STATUS 承载技能书：请求体只有 `[i16] 主背包槽位`，成功体是
// `01 u16 目标用户ID FF u32 0 u16 0 u16 0`，随后由 NOTI 0x25（本客户端是
// class0/op37 绝对角色状态）下发等级/EXP/剩余 SP/TP。
//
// 真档 PVF 证据（runtime/data/dnf/Script.pvf，5 件带 `[use wav] SP_UP_ITEM`）：
//
//	1031  stackable/book_skill1.stk                `SP+5技能书`
//	1038  stackable/book_skill2.stk                `SP+20技能书`
//	1204  stackable/book_fskill1.stk               `TP+1技能书`
//	1205  stackable/book_fskill2.stk               `TP+5技能书`
//	80003 stackable/extention/test_book_skill1.stk `SP+5技能书`

const currentExperienceItemTestBookPath = "stackable/book_skill1.stk"

func currentExperienceItemTestPVF() bridgePVFSource {
	return bridgePVFSource{
		"monster/monster.lst":     "",
		"equipment/equipment.lst": "",
		"stackable/stackable.lst": "1031 `book_skill1.stk`\n1038 `book_skill2.stk`\n" +
			"1204 `book_fskill1.stk`\n2600000 `potion.stk`\n2600001 `etc_wav.stk`\n2600002 `named_not_book.stk`\n",
		currentExperienceItemTestBookPath: "[name]\n`SP+5技能书`\n[explain]\n`使用后， 可以增加5点SP。`\n" +
			"[stackable type]\n`[etc]` 8\n[use wav]\n`SP_UP_ITEM`\n",
		"stackable/book_skill2.stk": "[name]\n`SP+20技能书`\n[stackable type]\n`[etc]` 1\n[use wav]\n`SP_UP_ITEM`\n",
		"stackable/book_fskill1.stk": "[name]\n`TP+1技能书`\n[explain]\n`使用后, 可以增加1点TP。`\n" +
			"[stackable type]\n`[etc]` 8\n[use wav]\n`SP_UP_ITEM`\n",
		// A `[waste]` consumable: the op44 family that was never the SP book route.
		"stackable/potion.stk": "[name]\n`红色药水`\n[stackable type]\n`[waste]`\n",
		// The SP-up sound alone is not enough: the name must match the book family.
		"stackable/etc_wav.stk":        "[name]\n`奇怪的书`\n[stackable type]\n`[etc]` 8\n[use wav]\n`SP_UP_ITEM`\n",
		"stackable/named_not_book.stk": "[name]\n`SP+5技能书`\n[stackable type]\n`[etc]` 8\n[use wav]\n`BOOK_TOUCH`\n",
	}
}

func mustCurrentExperienceItemTestAssets(
	t *testing.T,
	slots map[string]dnfrepo.ItemStack,
	points dnfrepo.SkillPointState,
) (dnfrepo.Group, *pvfDungeonDropCatalog) {
	t.Helper()
	catalog, err := newPVFDungeonDropCatalog(currentExperienceItemTestPVF())
	if err != nil {
		t.Fatal(err)
	}
	repositories := dnfrepomemory.NewMemoryGroup()
	ctx := context.Background()
	if err := repositories.Account.Save(ctx, dnfrepo.AccountRecord{AccountID: "account-1"}); err != nil {
		t.Fatal(err)
	}
	if err := repositories.Character.Save(ctx, dnfrepo.CharacterRecord{
		CharacterID: "19",
		AccountID:   "account-1",
		Level:       50,
		Stats:       map[string]int64{"exp": 1234},
	}); err != nil {
		t.Fatal(err)
	}
	if err := repositories.Inventory.Save(ctx, dnfrepo.InventoryRecord{
		CharacterID: "19",
		Slots:       slots,
	}); err != nil {
		t.Fatal(err)
	}
	if err := repositories.Skill.Save(ctx, dnfrepo.SkillRecord{
		CharacterID: "19",
		Points:      points,
	}); err != nil {
		t.Fatal(err)
	}
	return repositories, catalog
}

func currentExperienceItemTestService(repositories dnfrepo.Group, catalog *pvfDungeonDropCatalog) *Service {
	service := &Service{
		options: options{
			accountID:          "account-1",
			gameUpperHeader:    gameUpperHeaderChannel13,
			gameUpperBodyCodec: gameUpperBodyCodecPlain,
		},
		pvfItemCatalog:     catalog,
		repositoryProvider: func() (dnfrepo.Group, bool) { return repositories, true },
	}
	service.initialSPTable = map[int]int{50: 1000}
	service.initialTPTable = map[int]int{50: 10}
	return service
}

func currentExperienceItemTestBody(slot int16) []byte {
	body := make([]byte, currentExperienceItemUseBodySize)
	binary.LittleEndian.PutUint16(body[0:2], uint16(slot))
	return body
}

func currentExperienceItemTestPoints() dnfrepo.SkillPointState {
	return dnfrepo.SkillPointState{
		TotalSP:     1000,
		RemainingSP: 900,
		TotalTP:     10,
		RemainingTP: 8,
		SyncedLevel: 50,
	}
}

// currentExperienceItemWirePackets splits every packet the handler wrote.
func currentExperienceItemWirePackets(t *testing.T, connection *bufferConn) []dnfproto.ChannelPacket {
	t.Helper()
	packets := make([]dnfproto.ChannelPacket, 0, 4)
	rest := append([]byte(nil), connection.write.Bytes()...)
	for len(rest) > 0 {
		packet, trailing := splitGameServerUpperPacket(t, rest)
		packets = append(packets, packet)
		if len(trailing) >= len(rest) {
			t.Fatalf("packet split made no progress: rest=%x", rest)
		}
		rest = trailing
	}
	return packets
}

// TestCurrentExperienceItemUseConsumesSkillBookAndPublishesAbsoluteSP is the
// end-to-end regression for「SP书无法使用」: one op30 request consumes the book,
// persists +5 bonus SP, and publishes the absolute remaining SP.
func TestCurrentExperienceItemUseConsumesSkillBookAndPublishesAbsoluteSP(t *testing.T) {
	repositories, catalog := mustCurrentExperienceItemTestAssets(t, map[string]dnfrepo.ItemStack{
		"0:9": {ItemID: 1031, Count: 2},
	}, currentExperienceItemTestPoints())
	connection := &bufferConn{}
	service := currentExperienceItemTestService(repositories, catalog)
	session := &gameSession{conn: connection, connID: "increase-status-test", selectedCharacterID: 19}

	if err := service.handleCurrentExperienceItemUse(session, currentExperienceItemTestBody(9)); err != nil {
		t.Fatalf("handleCurrentExperienceItemUse error = %v", err)
	}

	packets := currentExperienceItemWirePackets(t, connection)
	if len(packets) == 0 {
		t.Fatal("op30 produced no reply")
	}
	ack := packets[0]
	if ack.Header.Classification != dnfproto.DefaultChannelClassification ||
		ack.Header.MsgID != uint16(dnfenum.CmdPacketIncreaseStatus) {
		t.Fatalf("op30 ack header = class %d msg %d, want class %d/op30",
			ack.Header.Classification, ack.Header.MsgID, dnfproto.DefaultChannelClassification)
	}
	// 12-byte reference success record: 01 | u16 user id | FF | u32 0 | u16 0 | u16 0.
	wantAck := []byte{0x01, 19, 0x00, 0xff, 0, 0, 0, 0, 0, 0, 0, 0}
	if !bytes.Equal(ack.Body, wantAck) {
		t.Fatalf("op30 ack body = %x, want %x", ack.Body, wantAck)
	}

	state := packets[len(packets)-1]
	if state.Header.Classification != 0 || state.Header.MsgID != currentDungeonCharacterStateMsgID {
		t.Fatalf("last packet = class %d msg %d, want class0/op%d",
			state.Header.Classification, state.Header.MsgID, currentDungeonCharacterStateMsgID)
	}
	wantPoints := dnfrepo.SkillPointState{TotalSP: 1005, RemainingSP: 905, TotalTP: 10, RemainingTP: 8, SyncedLevel: 50}
	character, found, err := repositories.Character.Load(context.Background(), "19")
	if err != nil || !found {
		t.Fatalf("load character found=%t err=%v", found, err)
	}
	character.Stats["bonus_sp"] = 5
	character.Stats["bonus_tp"] = 0
	if want := buildCurrentFinishLoadingCharacterStateBody(character, wantPoints); !bytes.Equal(state.Body, want) {
		t.Fatalf("character state body = %x, want %x", state.Body, want)
	}
	// Body layout: level(1) exp(4) expCategory0(4) then two u16 remaining-SP pairs.
	if !bytes.Equal(state.Body[9:11], []byte{0x89, 0x03}) {
		t.Fatalf("remaining SP bytes = %x, want 0x0389 (905)", state.Body[9:11])
	}

	inventory, found, err := repositories.Inventory.Load(context.Background(), "19")
	if err != nil || !found {
		t.Fatalf("load inventory found=%t err=%v", found, err)
	}
	if stack := inventory.Slots["0:9"]; stack.Count != 1 || stack.ItemID != 1031 {
		t.Fatalf("source row = %+v, want item 1031 count 1", stack)
	}
	skill, found, err := repositories.Skill.Load(context.Background(), "19")
	if err != nil || !found {
		t.Fatalf("load skill found=%t err=%v", found, err)
	}
	if skill.Points != wantPoints {
		t.Fatalf("persisted points = %+v, want %+v", skill.Points, wantPoints)
	}
	if loaded, found, err := repositories.Character.Load(context.Background(), "19"); err != nil || !found {
		t.Fatalf("reload character found=%t err=%v", found, err)
	} else if loaded.Stats["bonus_sp"] != 5 {
		t.Fatalf("persisted bonus_sp = %d, want 5", loaded.Stats["bonus_sp"])
	}
}

// TestCurrentExperienceItemUseResolvesTheTPBookFamily pins the TP+1 book.
func TestCurrentExperienceItemUseResolvesTheTPBookFamily(t *testing.T) {
	repositories, catalog := mustCurrentExperienceItemTestAssets(t, map[string]dnfrepo.ItemStack{
		"0:9": {ItemID: 1204, Count: 1},
	}, currentExperienceItemTestPoints())
	connection := &bufferConn{}
	service := currentExperienceItemTestService(repositories, catalog)
	session := &gameSession{conn: connection, connID: "increase-status-tp", selectedCharacterID: 19}

	if err := service.handleCurrentExperienceItemUse(session, currentExperienceItemTestBody(9)); err != nil {
		t.Fatalf("handleCurrentExperienceItemUse error = %v", err)
	}
	skill, found, err := repositories.Skill.Load(context.Background(), "19")
	if err != nil || !found {
		t.Fatalf("load skill found=%t err=%v", found, err)
	}
	if skill.Points.TotalTP != 11 || skill.Points.RemainingTP != 9 || skill.Points.TotalSP != 1000 {
		t.Fatalf("points = %+v, want total_tp=11 remaining_tp=9 total_sp=1000", skill.Points)
	}
	inventory, _, _ := repositories.Inventory.Load(context.Background(), "19")
	if _, found := inventory.Slots["0:9"]; found {
		t.Fatal("exhausted TP book row was not deleted")
	}
}

// TestCurrentExperienceItemUseRejectsNonBookSourcesWithoutMutation keeps every
// non-book, malformed, and empty-slot request fail-closed with an explicit reply.
func TestCurrentExperienceItemUseRejectsNonBookSourcesWithoutMutation(t *testing.T) {
	cases := []struct {
		name     string
		itemID   int64
		slot     int16
		body     []byte
		wantCode byte
	}{
		{name: "malformed body", itemID: 1031, slot: 9, body: []byte{9, 0, 0}, wantCode: currentExperienceItemUseUnknownCode},
		{name: "waste consumable is not a skill book", itemID: 2600000, slot: 9, body: currentExperienceItemTestBody(9), wantCode: currentExperienceItemUseUnknownCode},
		{name: "sp up sound without the book name", itemID: 2600001, slot: 9, body: currentExperienceItemTestBody(9), wantCode: currentExperienceItemUseUnknownCode},
		{name: "book name without the sp up sound", itemID: 2600002, slot: 9, body: currentExperienceItemTestBody(9), wantCode: currentExperienceItemUseUnknownCode},
		{name: "empty source slot", itemID: 0, slot: 12, body: currentExperienceItemTestBody(12), wantCode: currentExperienceItemUseMissingSourceCode},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			slots := map[string]dnfrepo.ItemStack{}
			if testCase.itemID != 0 {
				slots["0:9"] = dnfrepo.ItemStack{ItemID: testCase.itemID, Count: 1}
			}
			repositories, catalog := mustCurrentExperienceItemTestAssets(t, slots, currentExperienceItemTestPoints())
			connection := &bufferConn{}
			service := currentExperienceItemTestService(repositories, catalog)
			session := &gameSession{conn: connection, connID: "increase-status-reject", selectedCharacterID: 19}

			if err := service.handleCurrentExperienceItemUse(session, testCase.body); err != nil {
				t.Fatalf("handleCurrentExperienceItemUse error = %v", err)
			}
			packets := currentExperienceItemWirePackets(t, connection)
			if len(packets) != 1 {
				t.Fatalf("reply count = %d, want exactly one failure ack", len(packets))
			}
			if packets[0].Header.MsgID != uint16(dnfenum.CmdPacketIncreaseStatus) ||
				!bytes.Equal(packets[0].Body, []byte{0x00, testCase.wantCode}) {
				t.Fatalf("failure ack = %+v body=%x, want 00 %02x", packets[0].Header, packets[0].Body, testCase.wantCode)
			}
			skill, found, err := repositories.Skill.Load(context.Background(), "19")
			if err != nil || !found {
				t.Fatalf("load skill found=%t err=%v", found, err)
			}
			if skill.Points != currentExperienceItemTestPoints() {
				t.Fatalf("rejected use mutated the ledger = %+v", skill.Points)
			}
			if testCase.itemID == 1031 || testCase.itemID == 0 {
				return
			}
			inventory, _, _ := repositories.Inventory.Load(context.Background(), "19")
			if stack := inventory.Slots["0:9"]; stack.Count != 1 {
				t.Fatalf("rejected use consumed the source row = %+v", stack)
			}
		})
	}
}

// TestCurrentExperienceItemUseOffSwitchKeepsPreChangeBehaviour pins the rollback.
func TestCurrentExperienceItemUseOffSwitchKeepsPreChangeBehaviour(t *testing.T) {
	t.Setenv(currentExperienceItemUseEnv, "off")
	repositories, catalog := mustCurrentExperienceItemTestAssets(t, map[string]dnfrepo.ItemStack{
		"0:9": {ItemID: 1031, Count: 1},
	}, currentExperienceItemTestPoints())
	connection := &bufferConn{}
	service := currentExperienceItemTestService(repositories, catalog)
	session := &gameSession{conn: connection, connID: "increase-status-off", selectedCharacterID: 19}

	if err := service.handleCurrentExperienceItemUse(session, currentExperienceItemTestBody(9)); err != nil {
		t.Fatalf("handleCurrentExperienceItemUse error = %v", err)
	}
	if len(connection.write.Bytes()) != 0 {
		t.Fatalf("off switch wrote %x, want no reply", connection.write.Bytes())
	}
	inventory, _, _ := repositories.Inventory.Load(context.Background(), "19")
	if stack := inventory.Slots["0:9"]; stack.Count != 1 {
		t.Fatalf("off switch consumed the source row = %+v", stack)
	}
}

// TestExperienceItemGameplayModuleRegistersOp30 covers the route ownership.
func TestExperienceItemGameplayModuleRegistersOp30(t *testing.T) {
	module := experienceItemGameplayModule()
	opcode := uint16(dnfenum.CmdPacketIncreaseStatus)
	if module.Name != "experience-item" {
		t.Fatalf("module name = %q", module.Name)
	}
	if module.LegacyHandlers[opcode] == nil {
		t.Fatalf("experience-item module missing legacy opcode %d", opcode)
	}
	if module.UpperHandlers[opcode] == nil {
		t.Fatalf("experience-item module missing upper opcode %d", opcode)
	}
	owner := ""
	if route, ok := currentGameplayModules.legacyHandlers[opcode]; ok {
		owner = route.module
	}
	if owner != "experience-item" {
		t.Fatalf("op%d legacy owner = %q, want experience-item", opcode, owner)
	}
}

// TestResolveCurrentSkillBookIsPVFDriven pins the discriminator on the synthetic
// fixture: family = `[etc]` + `[use wav] SP_UP_ITEM` + the `SP+N技能书` name.
func TestResolveCurrentSkillBookIsPVFDriven(t *testing.T) {
	_, catalog := mustCurrentExperienceItemTestAssets(t, nil, currentExperienceItemTestPoints())
	service := currentExperienceItemTestService(dnfrepomemory.NewMemoryGroup(), catalog)
	book, err := service.resolveCurrentSkillBook(1031)
	if err != nil {
		t.Fatalf("resolveCurrentSkillBook(1031) error = %v", err)
	}
	want := dnfexperienceitem.Book{ItemID: 1031, Kind: dnfexperienceitem.KindSP, Amount: 5, PVFPath: currentExperienceItemTestBookPath, Name: "SP+5技能书"}
	if book != want {
		t.Fatalf("book = %+v, want %+v", book, want)
	}
	if book, err := service.resolveCurrentSkillBook(1204); err != nil {
		t.Fatalf("resolveCurrentSkillBook(1204) error = %v", err)
	} else if book.Kind != dnfexperienceitem.KindTP || book.Amount != 1 {
		t.Fatalf("TP book = %+v, want kind=TP amount=1", book)
	}
	for _, itemID := range []int64{2600000, 2600001, 2600002} {
		if _, err := service.resolveCurrentSkillBook(itemID); err == nil {
			t.Fatalf("item %d resolved as a skill book", itemID)
		}
	}
}

// TestRealPVFSkillBookFamily pins the five real runtime-PVF books. It needs the
// runtime archive:
//
//	DNFBRIDGE_REAL_PVF_SMOKE=<repo>\runtime\data\dnf\Script.pvf \
//	  go test -count=1 -run TestRealPVFSkillBookFamily ./internal/services/dnfbridge
func TestRealPVFSkillBookFamily(t *testing.T) {
	pvfPath := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if pvfPath == "" {
		t.Skip("set DNFBRIDGE_REAL_PVF_SMOKE to the runtime Script.pvf")
	}
	archive, err := platformpvf.LoadArchive(platformpvf.Options{Path: pvfPath})
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := newPVFDungeonDropCatalog(archive)
	if err != nil {
		t.Fatal(err)
	}
	service := &Service{pvfItemCatalog: catalog}
	want := map[int64]dnfexperienceitem.Book{
		1031:  {ItemID: 1031, Kind: dnfexperienceitem.KindSP, Amount: 5, PVFPath: "stackable/book_skill1.stk", Name: "SP+5技能书"},
		1038:  {ItemID: 1038, Kind: dnfexperienceitem.KindSP, Amount: 20, PVFPath: "stackable/book_skill2.stk", Name: "SP+20技能书"},
		1204:  {ItemID: 1204, Kind: dnfexperienceitem.KindTP, Amount: 1, PVFPath: "stackable/book_fskill1.stk", Name: "TP+1技能书"},
		1205:  {ItemID: 1205, Kind: dnfexperienceitem.KindTP, Amount: 5, PVFPath: "stackable/book_fskill2.stk", Name: "TP+5技能书"},
		80003: {ItemID: 80003, Kind: dnfexperienceitem.KindSP, Amount: 5, PVFPath: "stackable/extention/test_book_skill1.stk", Name: "SP+5技能书"},
	}
	for itemID, wantBook := range want {
		book, err := service.resolveCurrentSkillBook(itemID)
		if err != nil {
			t.Fatalf("resolveCurrentSkillBook(%d) error = %v", itemID, err)
		}
		if book != wantBook {
			t.Fatalf("book %d = %+v, want %+v", itemID, book, wantBook)
		}
	}
	// A premium contract is `[contract]`, never a book.
	if _, err := service.resolveCurrentSkillBook(10000391); err == nil {
		t.Fatal("item 10000391 (晶之契约) resolved as a skill book")
	}
}
