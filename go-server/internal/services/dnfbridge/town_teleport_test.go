package dnfbridge

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/hex"
	"testing"

	"longheng.io/server/internal/modules/dnf/dnfenum"
	dnfproto "longheng.io/server/internal/modules/dnf/protocol"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	dnftown "longheng.io/server/internal/modules/dnf/town"
)

func newTeleportPotionTest(t *testing.T) (*Service, *gameSession, dnfrepo.Group) {
	t.Helper()
	service, session, repositories := newTownMoveTest(t)
	if err := repositories.Inventory.Save(context.Background(), dnfrepo.InventoryRecord{
		CharacterID: "29",
		Slots: map[string]dnfrepo.ItemStack{
			"0:65": {
				ItemID:   2600014,
				Count:    2,
				RawEntry: make([]byte, currentItemListEntryWireSize),
				Extra:    map[string]string{"item_kind": "stackable", "pvf_path": "stackable/professional/potion/ptn_instantmovement.stk"},
			},
		},
	}); err != nil {
		t.Fatal(err)
	}
	loaded, _, _ := repositories.Inventory.Load(context.Background(), "29")
	stack := loaded.Slots["0:65"]
	stack.RawEntry[0x06] = 2
	loaded.Slots["0:65"] = stack
	if err := repositories.Inventory.Save(context.Background(), loaded); err != nil {
		t.Fatal(err)
	}
	source := dungeonDropCatalogTestSource{
		"monster/monster.lst":                                   "",
		"equipment/equipment.lst":                               "",
		"stackable/stackable.lst":                               "2600014 `professional/potion/ptn_instantmovement.stk`\n37 `cash/test.stk`\n",
		"stackable/professional/potion/ptn_instantmovement.stk": "[name] `Potion`\n[stackable type] `[teleport potion]`\n[stack limit] 100\n",
		"stackable/cash/test.stk":                               "[name] `Cash`\n[stackable type] `[material]`\n",
	}
	catalog, err := newPVFDungeonDropCatalog(source)
	if err != nil {
		t.Fatal(err)
	}
	service.pvfItemCatalog = catalog
	return service, session, repositories
}

func buildTeleportPotionRequest(slot int16, itemCode int32, townID byte) []byte {
	body := make([]byte, 8)
	binary.LittleEndian.PutUint16(body[0:2], uint16(slot))
	binary.LittleEndian.PutUint32(body[2:6], uint32(itemCode))
	body[7] = townID
	return body
}

// buildTeleportPotionClickRequest 造**现场同形**的 13 字节 op237 body：尾部带操作者
// 在目标地区地图上点的那个点（body[9:11]=x, body[11:13]=y）。
func buildTeleportPotionClickRequest(slot int16, itemCode int32, townID byte, clickX, clickY int64) []byte {
	body := make([]byte, 13)
	binary.LittleEndian.PutUint16(body[0:2], uint16(slot))
	binary.LittleEndian.PutUint32(body[2:6], uint32(itemCode))
	body[7] = townID
	binary.LittleEndian.PutUint16(body[9:11], uint16(clickX))
	binary.LittleEndian.PutUint16(body[11:13], uint16(clickY))
	return body
}

// newTeleportPotionTownCatalogTest 复用 newTeleportPotionTest 的仓库/道具/物品目录，
// 只把城镇目录换成给定夹具 —— 用来覆盖"目标城镇只声明 [normal]/[dungeon gate] 区、
// 没有 [gate] 区"这种真档形态（真档 town 11/12/43）。
func newTeleportPotionTownCatalogTest(t *testing.T, list string, docs map[string]string) (*Service, *gameSession, dnfrepo.Group) {
	t.Helper()
	service, session, repositories := newTeleportPotionTest(t)
	source := townMoveTestSource{"town/town.lst": list}
	for name, text := range docs {
		source[name] = text
	}
	table, err := dnftown.Load(context.Background(), source, dnftown.Options{})
	if err != nil {
		t.Fatal(err)
	}
	service.townCatalog = table
	return service, session, repositories
}

// assertTeleportPotionFailure 断言客户端收到的是 {00, 23}（操作者截图
// 「对方正忙，无法对其使用该功能。」就是这条失败体触发的），并且后面没有别的包。
func assertTeleportPotionFailure(t *testing.T, session *gameSession) {
	t.Helper()
	failure, rest := splitGameServerUpperPacket(t, session.conn.(*bufferConn).write.Bytes())
	if failure.Header.MsgID != uint16(dnfenum.CmdPacketTeleport) || !bytes.Equal(failure.Body, []byte{0, currentTeleportPotionErrorUnsupported}) || len(rest) != 0 {
		t.Fatalf("failure packet=%+v body=%x rest=%x", failure.Header, failure.Body, rest)
	}
}

// 不带点击坐标的 8 字节旧体：落点只能用目标地区自己声明的默认落点。
// 夹具 town 40 的 area 0 (`WestCoast.map`) 没有任何坐标声明，于是逐级回退到该城镇
// 任意一个已声明的落点（area 1 的 `[gate]` 1016 189）—— 关键是**区域仍然是 area 0**
// （目标地区的主城区），而不是上一版那样把 area 1 当落地区域。
func TestHandleTeleportPotionWithoutClickPointUsesTownLandingPointAndConsumesOne(t *testing.T) {
	service, session, repositories := newTeleportPotionTest(t)

	if err := service.handleTeleportPotion(session, buildTeleportPotionRequest(65, 2600014, 40)); err != nil {
		t.Fatal(err)
	}

	conn := session.conn.(*bufferConn)
	transition, rest := splitTownTransitionAndPostState(
		t,
		conn.write.Bytes(),
		session,
		29,
		40,
		0,
		1016,
		189,
		0,
		3,
		townMoveSkillProjectionBody(t, repositories, "29"),
		false,
	)
	wantTransition, err := buildCurrentSceneTransitionBody(40, 0, []currentSceneTransitionRow{{
		ObjectOrResourceKey: 29,
		Value1:              uint16(1016),
		Value2:              uint16(189),
		Value3:              0,
		Value4:              3,
	}})
	if err != nil {
		t.Fatal(err)
	}
	if transition.Header.MsgID != currentSceneTransitionMsgID || transition.Header.Classification != 0 || !bytes.Equal(transition.Body, wantTransition) {
		t.Fatalf("transition header=%+v body=%x want=%x", transition.Header, transition.Body, wantTransition)
	}

	var ack *dnfproto.ChannelPacket
	for len(rest) > 0 {
		packet, next := splitGameServerUpperPacket(t, rest)
		rest = next
		if packet.Header.MsgID == uint16(dnfenum.CmdPacketTeleport) {
			ack = &packet
			break
		}
	}
	if ack == nil {
		t.Fatalf("teleport ack missing in stream")
	}
	wantAck := []byte{1, 65, 0, 0x4E, 0xAC, 0x27, 0x00}
	if ack.Header.Classification != dnfproto.DefaultChannelClassification || !bytes.Equal(ack.Body, wantAck) {
		t.Fatalf("ack header=%+v body=%x want=%x", ack.Header, ack.Body, wantAck)
	}

	stored, found, err := repositories.Character.Load(context.Background(), "29")
	if err != nil || !found {
		t.Fatalf("load stored character found=%t err=%v", found, err)
	}
	if stored.Stats["town_id"] != 40 || stored.Stats["area_id"] != 0 || stored.Stats["pos_x"] != 1016 || stored.Stats["pos_y"] != 189 || stored.Stats["direction"] != 0 {
		t.Fatalf("stored town location=%+v", stored.Stats)
	}
	inventory, _, err := repositories.Inventory.Load(context.Background(), "29")
	if err != nil {
		t.Fatal(err)
	}
	potion := inventory.Slots["0:65"]
	if potion.Count != 1 || potion.RawEntry[0x06] != 1 {
		t.Fatalf("potion after consume=%+v", potion)
	}
}

// 回归（操作者报障「传送地点和实际你点击的地方不一样」）：带点击坐标的 op237 必须
// 落到**点击点**，而且要落在目标地区的主城区(area 0)上。
//
// 夹具角色初始在 town 38 area 1（赛丽亚房间，门坐标 450 234）；现场同一形态是
// packet_log.20260925-212540.txt:1535/1537 —— 角色在 38/1，客户端发
// `08004eac27000026004302cc00`（town=38 x=579 y=204），y=204 在赛丽亚房间的可行走带
// (现场 y∈[233,311]) **之上**，所以点击点属于 38 的 area 0 (`new_Elvengard.map`)。
func TestHandleTeleportPotionLandsOnClickedPointOnDestinationMainMap(t *testing.T) {
	service, session, repositories := newTeleportPotionTest(t)

	if err := service.handleTeleportPotion(session, buildTeleportPotionClickRequest(65, 2600014, 38, 579, 204)); err != nil {
		t.Fatal(err)
	}

	splitTownTransitionAndPostState(
		t,
		session.conn.(*bufferConn).write.Bytes(),
		session,
		29,
		38,
		0,
		579,
		204,
		0,
		3,
		townMoveSkillProjectionBody(t, repositories, "29"),
		false,
	)
	stored, found, err := repositories.Character.Load(context.Background(), "29")
	if err != nil || !found {
		t.Fatalf("load stored character found=%t err=%v", found, err)
	}
	if stored.Stats["town_id"] != 38 || stored.Stats["area_id"] != 0 ||
		stored.Stats["pos_x"] != 579 || stored.Stats["pos_y"] != 204 {
		t.Fatalf("teleport potion did not land on the clicked point: %+v", stored.Stats)
	}
	inventory, _, err := repositories.Inventory.Load(context.Background(), "29")
	if err != nil {
		t.Fatal(err)
	}
	if inventory.Slots["0:65"].Count != 1 {
		t.Fatalf("potion after consume=%+v", inventory.Slots["0:65"])
	}
}

// 回归（操作者报障「某些地图会报图1错误」= 客户端弹「对方正忙，无法对其使用该功能。」）：
// 目标城镇只声明 `[normal]`/`[dungeon gate]` 区、没有 `[gate]` 区时，上一版在
// townGateArea 上硬失败并回 {00, 23}；现在必须仍然落地。
//
// 夹具 town 11 就是真档 town/Shonan.twn 的形态（area 0 `[normal]`、area 2 `[dungeon gate]`
// 16，没有任何坐标声明）。现场同一形态：packet_log.20260925-212540.txt:2032 与 :2034
// （town 11 → game-town-teleport-potion-blocked reason=target_town_gate_area_missing_from_runtime_pvf）。
func TestHandleTeleportPotionLandsInTownWithoutPVFGateArea(t *testing.T) {
	service, session, repositories := newTeleportPotionTownCatalogTest(t,
		"38 `new_Elvengard.twn` 11 `Shonan.twn`",
		map[string]string{
			"town/new_Elvengard.twn": "[area]\n0 `new_Elvengard.map` `[normal]`\n[/area]\n" +
				"[area]\n1 `new_seria_room.map` `[gate]` 450 234\n[/area]\n[name]\n`Elvengard`",
			"town/Shonan.twn": "[area]\n0 `Town/Shonan/Shonan_Gymnasium.map` `[normal]`\n[/area]\n" +
				"[area]\n2 `Town/Shonan/Shonan_Tournament.map` `[dungeon gate]` 16\n[/area]\n[name]\n`Shonan`",
		})
	if _, found := service.townGateArea(11); found {
		t.Fatal("fixture town 11 must have no PVF [gate] area, otherwise this regression proves nothing")
	}

	if err := service.handleTeleportPotion(session, buildTeleportPotionClickRequest(65, 2600014, 11, 1247, 293)); err != nil {
		t.Fatal(err)
	}

	splitTownTransitionAndPostState(
		t,
		session.conn.(*bufferConn).write.Bytes(),
		session,
		29,
		11,
		0,
		1247,
		293,
		0,
		3,
		townMoveSkillProjectionBody(t, repositories, "29"),
		false,
	)
	stored, found, err := repositories.Character.Load(context.Background(), "29")
	if err != nil || !found {
		t.Fatalf("load stored character found=%t err=%v", found, err)
	}
	if stored.Stats["town_id"] != 11 || stored.Stats["area_id"] != 0 ||
		stored.Stats["pos_x"] != 1247 || stored.Stats["pos_y"] != 293 {
		t.Fatalf("teleport potion into a gate-less town did not land: %+v", stored.Stats)
	}
	inventory, _, err := repositories.Inventory.Load(context.Background(), "29")
	if err != nil {
		t.Fatal(err)
	}
	if inventory.Slots["0:65"].Count != 1 {
		t.Fatalf("potion after consume=%+v", inventory.Slots["0:65"])
	}
}

// 回退开关：DNFBRIDGE_TELEPORT_POTION_LANDING=gate 时恢复修好前的行为（PVF [gate] 区 +
// 门坐标），gate-less 城镇重新变成 {00, 23}。默认值见 town_teleport.go 的常量。
func TestHandleTeleportPotionLegacyGateLandingModeKeepsOldFailure(t *testing.T) {
	t.Setenv(currentTeleportPotionLandingEnv, currentTeleportPotionLandingLegacy)
	service, session, _ := newTeleportPotionTownCatalogTest(t,
		"38 `new_Elvengard.twn` 11 `Shonan.twn`",
		map[string]string{
			"town/new_Elvengard.twn": "[area]\n0 `new_Elvengard.map` `[normal]`\n[/area]\n" +
				"[area]\n1 `new_seria_room.map` `[gate]` 450 234\n[/area]\n[name]\n`Elvengard`",
			"town/Shonan.twn": "[area]\n0 `Town/Shonan/Shonan_Gymnasium.map` `[normal]`\n[/area]\n[name]\n`Shonan`",
		})

	if err := service.handleTeleportPotion(session, buildTeleportPotionClickRequest(65, 2600014, 11, 1247, 293)); err != nil {
		t.Fatal(err)
	}
	assertTeleportPotionFailure(t, session)
}

// 回退开关：DNFBRIDGE_TELEPORT_POTION_AREA_GATES=strict 时目标区域的 [need quest] 重新
// 生效（夹具 town 40 的 area 0 声明 `[need quest] 3156`），药水不消耗并回 {00, 23}。
func TestHandleTeleportPotionStrictAreaGatesRestoresPVFRejection(t *testing.T) {
	t.Setenv(currentTeleportPotionAreaGatesEnv, currentTeleportPotionAreaGatesStrict)
	service, session, repositories := newTeleportPotionTest(t)

	if err := service.handleTeleportPotion(session, buildTeleportPotionClickRequest(65, 2600014, 40, 700, 200)); err != nil {
		t.Fatal(err)
	}
	assertTeleportPotionFailure(t, session)
	stored, _, err := repositories.Character.Load(context.Background(), "29")
	if err != nil {
		t.Fatal(err)
	}
	if stored.Stats["town_id"] != 38 || stored.Stats["area_id"] != 1 {
		t.Fatalf("strict area gates mutated the location: %+v", stored.Stats)
	}
	inventory, _, err := repositories.Inventory.Load(context.Background(), "29")
	if err != nil {
		t.Fatal(err)
	}
	if inventory.Slots["0:65"].Count != 2 {
		t.Fatalf("strict area gates consumed the potion: %+v", inventory.Slots["0:65"])
	}
}

func TestHandleTeleportPotionRejectsInvalidPotionAndTownWithoutMutation(t *testing.T) {
	service, session, repositories := newTeleportPotionTest(t)

	if err := service.handleTeleportPotion(session, buildTeleportPotionRequest(65, 37, 40)); err != nil {
		t.Fatal(err)
	}
	failure, rest := splitGameServerUpperPacket(t, session.conn.(*bufferConn).write.Bytes())
	if failure.Header.MsgID != uint16(dnfenum.CmdPacketTeleport) || !bytes.Equal(failure.Body, []byte{0, 23}) || len(rest) != 0 {
		t.Fatalf("failure packet=%+v body=%x rest=%x", failure.Header, failure.Body, rest)
	}

	conn := session.conn.(*bufferConn)
	conn.write.Reset()
	if err := service.handleTeleportPotion(session, buildTeleportPotionRequest(65, 2600014, 99)); err != nil {
		t.Fatal(err)
	}
	failure, rest = splitGameServerUpperPacket(t, conn.write.Bytes())
	if failure.Header.MsgID != uint16(dnfenum.CmdPacketTeleport) || !bytes.Equal(failure.Body, []byte{0, 23}) || len(rest) != 0 {
		t.Fatalf("failure packet=%+v body=%x rest=%x", failure.Header, failure.Body, rest)
	}

	stored, _, _ := repositories.Character.Load(context.Background(), "29")
	if stored.Stats["town_id"] != 38 || stored.Stats["area_id"] != 1 {
		t.Fatalf("location mutated on rejection: %+v", stored.Stats)
	}
	inventory, _, _ := repositories.Inventory.Load(context.Background(), "29")
	if inventory.Slots["0:65"].Count != 2 {
		t.Fatalf("potion consumed on rejection: %+v", inventory.Slots["0:65"])
	}
}

func TestHandleTeleportPotionFallsBackToItemScan(t *testing.T) {
	service, session, repositories := newTeleportPotionTest(t)

	if err := service.handleTeleportPotion(session, buildTeleportPotionRequest(66, 2600014, 40)); err != nil {
		t.Fatal(err)
	}
	conn := session.conn.(*bufferConn)
	splitGameServerUpperPacket(t, conn.write.Bytes())
	inventory, _, _ := repositories.Inventory.Load(context.Background(), "29")
	if inventory.Slots["0:65"].Count != 1 {
		t.Fatalf("scan fallback did not consume the real potion stack: %+v", inventory.Slots)
	}
	stored, _, _ := repositories.Character.Load(context.Background(), "29")
	if stored.Stats["town_id"] != 40 {
		t.Fatalf("stored town location=%+v", stored.Stats)
	}
}

// currentExeTeleportPotionLiveRequestHex 是操作者右键「瞬间移动药水」时客户端发出的
// op237 **body**，逐字节取自 2026-09-25 香港封包日志：
//
//	packet_log.20260925-192600.txt:2762
//	RECV kind=game-legacy conn_id=game-000016 pkt_seq=342 cmd=1 type=237
//	  runtime_cmd_name=ENUM_CMDPACKET_TELEPORT body_len=13 len=26
//	  hex=01ed001a00000095b15be6560006004eac270000270074093001
//
// 去掉 13 字节 transport 头后 body = slot 6 | itemCode 2600014 | 00 | town 0x27=39 |
// 00 | x=0x0974=2420 | y=0x0130=304。真档 PVF 里 stackable.lst 的 2600014 指向
// professional/potion/ptn_instantmovement.stk，其 [stackable type] 声明为 [teleport potion]。
const currentExeTeleportPotionLiveRequestHex = "06004eac270000270074093001"

func decodeCurrentExeTeleportPotionLiveRequest(t *testing.T) []byte {
	t.Helper()
	body, err := hex.DecodeString(currentExeTeleportPotionLiveRequestHex)
	if err != nil {
		t.Fatal(err)
	}
	return body
}

func TestParseCurrentTeleportPotionRequestMatchesLiveOp237Body(t *testing.T) {
	body := decodeCurrentExeTeleportPotionLiveRequest(t)
	request, err := parseCurrentTeleportPotionRequest(body)
	if err != nil {
		t.Fatal(err)
	}
	if request.PotionSlotIndex != 6 || request.ItemCode != 2600014 || request.TownID != 39 {
		t.Fatalf("live op237 request=%+v want slot=6 item=2600014 town=39", request)
	}
	if body[8] != 0x00 || binary.LittleEndian.Uint16(body[9:11]) != 2420 || binary.LittleEndian.Uint16(body[11:13]) != 304 {
		t.Fatalf("live op237 tail=%x want 00 7409 3001", body[8:])
	}
	// 现场 body 的尾部坐标就是操作者点的那个点，必须真的被解析出来（上一版把它当
	// 无意义尾巴丢掉，落点才会和点击位置不一样）。
	if !request.HasClickPoint || request.ClickX != 2420 || request.ClickY != 304 {
		t.Fatalf("live op237 click point lost: %+v", request)
	}
}

// 8 字节旧体没有坐标：不得凭空造一个点击点，落点走区域默认值。
func TestParseCurrentTeleportPotionRequestWithoutClickPoint(t *testing.T) {
	request, err := parseCurrentTeleportPotionRequest(buildTeleportPotionRequest(65, 2600014, 40))
	if err != nil {
		t.Fatal(err)
	}
	if request.HasClickPoint || request.ClickX != 0 || request.ClickY != 0 {
		t.Fatalf("short op237 body invented a click point: %+v", request)
	}
	// 点击点 (0,0) 同样视为"没带坐标"（城镇地图上没有这样的可点位置）。
	zero, err := parseCurrentTeleportPotionRequest(buildTeleportPotionClickRequest(65, 2600014, 40, 0, 0))
	if err != nil {
		t.Fatal(err)
	}
	if zero.HasClickPoint {
		t.Fatalf("op237 (0,0) click point must fall back to the area default: %+v", zero)
	}
}

// 回归：当前 EXE 把 op237 当 **legacy** 命令发（现场 `RECV kind=game-legacy … type=237`）。
// 如果它只注册在 UpperHandlers，legacy 注册表查不到就直接返回 false，服务端
// 一个字节都不回——玩家看到的就是"点了没反应"（2026-09-25 香港现场：操作者两次
// op237，两次都没有 ACK，也没有任何 game-town-teleport-potion-* 事件）。
func TestTownLegacyDispatchReachesTeleportPotionHandler(t *testing.T) {
	service, session, repositories := newTeleportPotionTest(t)
	// 现场 body 带点击点 (2420,304)，目标城镇是 39（HendonMyre）⇒ 按"目标地区主城区"
	// 落到 39 的 area 0，并提交现场那个点击点。夹具 39/0 不声明任何门槛，所以这条链路
	// 不再需要预置任务完成状态。
	registry := mustGameplayModuleRegistry(townGameplayModule())
	body := decodeCurrentExeTeleportPotionLiveRequest(t)

	handled, err := registry.DispatchLegacy(service, session, uint16(dnfenum.CmdPacketTeleport), body)
	if err != nil {
		t.Fatal(err)
	}
	if !handled {
		t.Fatal("town module dropped legacy op237: the current EXE sends ENUM_CMDPACKET_TELEPORT as a legacy command, so the teleport potion becomes a silent no-op")
	}

	rest := session.conn.(*bufferConn).write.Bytes()
	var ack *dnfproto.ChannelPacket
	for len(rest) > 0 {
		packet, next := splitGameServerUpperPacket(t, rest)
		rest = next
		if packet.Header.MsgID == uint16(dnfenum.CmdPacketTeleport) {
			captured := packet
			ack = &captured
			break
		}
	}
	if ack == nil {
		t.Fatal("legacy op237 produced no op237 ack")
	}
	// 现场 body 声明 slot=6，而药水实际在 0:65 ⇒ 走的是同一条按 itemCode 回退扫描；
	// ack 形状 = {01, i16 slot, i32 itemCode}（与现场同包已验证的成功体一致）。
	if want := []byte{1, 6, 0, 0x4E, 0xAC, 0x27, 0x00}; !bytes.Equal(ack.Body, want) {
		t.Fatalf("op237 ack body=%x want=%x", ack.Body, want)
	}

	// 现场声明的目标城镇是 39（HendonMyre）：区域 = area 0（目标地区主城区），
	// 落点 = 现场点击点 (2420,304)。
	stored, _, err := repositories.Character.Load(context.Background(), "29")
	if err != nil {
		t.Fatal(err)
	}
	if stored.Stats["town_id"] != 39 || stored.Stats["area_id"] != 0 ||
		stored.Stats["pos_x"] != 2420 || stored.Stats["pos_y"] != 304 {
		t.Fatalf("teleport potion did not commit the clicked point on the destination main map: %+v", stored.Stats)
	}
	inventory, _, err := repositories.Inventory.Load(context.Background(), "29")
	if err != nil {
		t.Fatal(err)
	}
	if inventory.Slots["0:65"].Count != 1 {
		t.Fatalf("teleport potion was not consumed exactly once: %+v", inventory.Slots["0:65"])
	}
}

func TestParseCurrentTeleportPotionRequestCarriesRequestedArea(t *testing.T) {
	cases := []struct {
		name     string
		bodyHex  string
		wantArea int64
		wantX    int64
		wantY    int64
	}{
		{name: "death_valley_area_5", bodyHex: "06004eac2700002605e5017e01", wantArea: 5, wantX: 485, wantY: 382},
		{name: "main_area_0", bodyHex: "06004eac27000026007805f900", wantArea: 0, wantX: 1400, wantY: 249},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body, err := hex.DecodeString(tc.bodyHex)
			if err != nil {
				t.Fatal(err)
			}
			request, err := parseCurrentTeleportPotionRequest(body)
			if err != nil {
				t.Fatal(err)
			}
			if request.TownID != 38 || !request.HasTargetArea || request.TargetAreaID != tc.wantArea {
				t.Fatalf("request=%+v want town=38 has_area=true area=%d", request, tc.wantArea)
			}
			if !request.HasClickPoint || request.ClickX != tc.wantX || request.ClickY != tc.wantY {
				t.Fatalf("request=%+v want click=(%d,%d)", request, tc.wantX, tc.wantY)
			}
		})
	}
	// 旧 8 字节体没有区域字段：HasTargetArea 必须为 false，区域交给改动前的选择链。
	legacy, err := parseCurrentTeleportPotionRequest(buildTeleportPotionRequest(65, 2600014, 40))
	if err != nil {
		t.Fatal(err)
	}
	if legacy.HasTargetArea {
		t.Fatalf("legacy 8-byte body must not claim a requested area: %+v", legacy)
	}
}

// Explicit destination in the 13-byte current-client body.
func buildTeleportPotionAreaClickRequest(slot int16, itemCode int32, townID, areaID byte, x, y int64) []byte {
	body := buildTeleportPotionClickRequest(slot, itemCode, townID, x, y)
	body[8] = areaID
	return body
}
