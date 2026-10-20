package dnfbridge

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"math"
	"testing"

	dnfavataroption "longheng.io/server/internal/modules/dnf/avataroptionchange"
	"longheng.io/server/internal/modules/dnf/dnfenum"
	dnfproto "longheng.io/server/internal/modules/dnf/protocol"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	dnfrepomemory "longheng.io/server/internal/modules/dnf/repository/memory"
)

// 装扮属性调整箱（op459 ENUM_CMDPACKET_AVATAR_OPTION_CHANGE）的聚焦回归。
//
// 现场报文（HK `packet_log.20260926-022031.txt:14569`，操作者三次右键）：
//
//	RECV kind=game-legacy cmd=1 type=459 body_len=13
//	     hex=01cb011a000000d0cfb4ff2a0149006498280033001e9f00002f
//	     body = 49 00 | 64 98 28 00 | 33 00 | 1e 9f 00 00 | 2f
//	          = source(73, 2660452) target(51, 40734) ability=47
//
// 夹具把现场形状缩到可用槽位：source = 主背包 0:9 的稀有箱，target = 装扮栏 1:5 的
// 稀有(hair/[grade] 3)装扮；`TestCurrentAvatarOptionChangeLiveBodyShape` 另外用现场
// 的 13 字节体逐字节回放解析。
const (
	currentAvatarOptionChangeTestRareBox   = uint32(2660452)
	currentAvatarOptionChangeTestSeniorBox = uint32(2660834)
	currentAvatarOptionChangeTestRareAva   = uint32(41001)
	currentAvatarOptionChangeTestSeniorAva = uint32(41002)
	currentAvatarOptionChangeTestWeapon    = uint32(41003)
	currentAvatarOptionChangeTestPlainBox  = uint32(21)
)

// currentAvatarOptionChangeTestPVF 是照真档形状造的最小 PVF：
// 稀有箱的 `[avatar select ability change]` 是 “ `[all]` `[all]` 3 0 “，
// 高级箱是 “ `[all]` `[all]` 2 1 “（与真档
// stackable/cash/avatar_optionchange1.stk / avatar_optionchange11.stk 同形）。
func currentAvatarOptionChangeTestPVF() bridgePVFSource {
	return bridgePVFSource{
		"monster/monster.lst":     "",
		"stackable/stackable.lst": "21 `cube_bind.stk`\n2660452 `rare_box.stk`\n2660834 `senior_box.stk`\n",
		"equipment/equipment.lst": "41001 `avatar_hair_rare.equ`\n41002 `avatar_hat_senior.equ`\n41003 `weapon.equ`\n",
		"stackable/rare_box.stk": "" +
			"[name] `装扮属性调整箱 (稀有)`\n" +
			"[stackable type] `[etc]` 0\n" +
			"[avatar select ability change]\n" +
			"`[all]` `[all]` 3 0\n",
		"stackable/senior_box.stk": "" +
			"[name] `装扮属性调整箱 (高级)`\n" +
			"[stackable type] `[etc]` 0\n" +
			"[avatar select ability change]\n" +
			"`[all]` `[all]` 2 1\n",
		// 没有该段的普通堆叠品（例如装扮合成器）：必须被 NotOptionChangeBox 拒掉。
		"stackable/cube_bind.stk":         "[stackable type] `[etc]`\n[stack limit] 999\n",
		"equipment/avatar_hair_rare.equ":  "[equipment type] `[hair avatar]`\n[grade] 3\n[rarity] 4\n",
		"equipment/avatar_hat_senior.equ": "[equipment type] `[hat avatar]`\n[grade] 2\n[rarity] 3\n",
		"equipment/weapon.equ":            "[equipment type] `[weapon]`\n[grade] 3\n[rarity] 4\n",
	}
}

// currentAvatarOptionChangeTestBody 按厂商 AvatarOptionChangeRequestParser 的
// 小端布局拼请求体。
func currentAvatarOptionChangeTestBody(
	sourceSlot int16,
	sourceItemID int32,
	targetSlot int16,
	targetItemID int32,
	abilityNo uint8,
) []byte {
	body := make([]byte, currentAvatarOptionChangeBodySize)
	binary.LittleEndian.PutUint16(body[0:2], uint16(sourceSlot))
	binary.LittleEndian.PutUint32(body[2:6], uint32(sourceItemID))
	binary.LittleEndian.PutUint16(body[6:8], uint16(targetSlot))
	binary.LittleEndian.PutUint32(body[8:12], uint32(targetItemID))
	body[12] = abilityNo
	return body
}

func mustCurrentAvatarOptionChangeTestAssets(
	t *testing.T,
	slots map[string]dnfrepo.ItemStack,
) (dnfrepo.Group, *pvfDungeonDropCatalog) {
	t.Helper()
	catalog, err := newPVFDungeonDropCatalog(currentAvatarOptionChangeTestPVF())
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
		Job:         "0",
	}); err != nil {
		t.Fatal(err)
	}
	if slots == nil {
		slots = map[string]dnfrepo.ItemStack{}
	}
	if err := repositories.Inventory.Save(ctx, dnfrepo.InventoryRecord{
		CharacterID: "19",
		Slots:       dnfrepo.CloneInventory(dnfrepo.InventoryRecord{Slots: slots}).Slots,
	}); err != nil {
		t.Fatal(err)
	}
	return repositories, catalog
}

func currentAvatarOptionChangeTestSlots() map[string]dnfrepo.ItemStack {
	return map[string]dnfrepo.ItemStack{
		"0:9": {ItemID: int64(currentAvatarOptionChangeTestRareBox), Count: 2},
		"1:5": {
			ItemID: int64(currentAvatarOptionChangeTestRareAva),
			Count:  1,
			Extra: map[string]string{
				"item_kind":                       "avatar",
				"equipment_type":                  "[hair avatar]",
				dnfavataroption.AbilityNoExtraKey: "0",
			},
		},
	}
}

func readCurrentAvatarOptionChangeInventory(t *testing.T, repositories dnfrepo.Group) dnfrepo.InventoryRecord {
	t.Helper()
	inventory, found, err := repositories.Inventory.Load(context.Background(), "19")
	if err != nil || !found {
		t.Fatalf("load inventory: found=%t err=%v", found, err)
	}
	return inventory
}

// currentAvatarOptionChangeTestOwner 直接构造模块层 owner，用于断言失败分类。
func currentAvatarOptionChangeTestOwner(
	t *testing.T,
	repositories dnfrepo.Group,
	catalog *pvfDungeonDropCatalog,
) *dnfavataroption.Owner {
	t.Helper()
	owner, err := dnfavataroption.NewOwner(repositories, currentAvatarOptionChangeCatalog{catalog: catalog})
	if err != nil {
		t.Fatal(err)
	}
	return owner
}

// TestCurrentAvatarOptionChangeRequestParsingBoundaries 钉住厂商 TryParse 的边界：
// 体至少 13 字节、小端、逐字段定位；不足 13 字节即解析失败（厂商此时同样回失败体）。
func TestCurrentAvatarOptionChangeRequestParsingBoundaries(t *testing.T) {
	live := currentAvatarOptionChangeTestBody(73, int32(currentAvatarOptionChangeTestRareBox), 51, 40734, 47)
	request, err := decodeCurrentAvatarOptionChangeRequest(live)
	if err != nil {
		t.Fatal(err)
	}
	if request.SourceSlot != 73 || request.SourceItemID != int32(currentAvatarOptionChangeTestRareBox) ||
		request.TargetSlot != 51 || request.TargetItemID != 40734 || request.AbilityNo != 47 {
		t.Fatalf("decoded request = %+v", request)
	}

	// 少于 13 字节：全部拒绝。
	for length := 0; length < currentAvatarOptionChangeBodySize; length++ {
		if _, err := decodeCurrentAvatarOptionChangeRequest(live[:length]); err == nil {
			t.Fatalf("body length %d must be rejected", length)
		}
		// 厂商 handler 对解析失败同样回失败体，不静默。
		repositories, catalog := mustCurrentAvatarOptionChangeTestAssets(t, currentAvatarOptionChangeTestSlots())
		connection := &bufferConn{}
		service := currentAvatarCompoundTestService(repositories, catalog)
		if err := service.serveAvatarOptionChange(
			&gameSession{conn: connection, selectedCharacterID: 19}, live[:length]); err != nil {
			t.Fatal(err)
		}
		ack, _ := splitGameServerUpperPacket(t, connection.write.Bytes())
		if ack.Header.MsgID != currentAvatarOptionChangeOpcode || !bytes.Equal(ack.Body, []byte{0x00}) {
			t.Fatalf("short body %d reply header=%+v body=%x want op459/00", length, ack.Header, ack.Body)
		}
	}
	// 长于 13 字节：取前 13 字节（厂商同样只读固定偏移，不因为尾部字节失败）。
	longer := append(append([]byte(nil), live...), 0xde, 0xad)
	request, err = decodeCurrentAvatarOptionChangeRequest(longer)
	if err != nil || request.AbilityNo != 47 {
		t.Fatalf("longer body decode = %+v err=%v", request, err)
	}
}

// TestCurrentAvatarOptionChangeRejectsEveryFailureBranch 逐条覆盖厂商
// InventoryAvatarOptionChangeError 的分类：每条都必须**回失败体**，并且
// 一个物品都不许被改动。
func TestCurrentAvatarOptionChangeRejectsEveryFailureBranch(t *testing.T) {
	slotsWith := func(slot string, stack dnfrepo.ItemStack) map[string]dnfrepo.ItemStack {
		out := currentAvatarOptionChangeTestSlots()
		out[slot] = stack
		return out
	}
	cases := []struct {
		name     string
		slots    map[string]dnfrepo.ItemStack
		body     []byte
		wantErr  error
		wantName string
	}{
		{
			name:     "负槽位 = InvalidRequest",
			slots:    currentAvatarOptionChangeTestSlots(),
			body:     currentAvatarOptionChangeTestBody(-1, int32(currentAvatarOptionChangeTestRareBox), 5, int32(currentAvatarOptionChangeTestRareAva), 1),
			wantErr:  dnfavataroption.ErrInvalidRequest,
			wantName: "invalid_request",
		},
		{
			name:     "itemId 0 = InvalidRequest",
			slots:    currentAvatarOptionChangeTestSlots(),
			body:     currentAvatarOptionChangeTestBody(9, 0, 5, int32(currentAvatarOptionChangeTestRareAva), 1),
			wantErr:  dnfavataroption.ErrInvalidRequest,
			wantName: "invalid_request",
		},
		{
			name:     "源槽为空 = SourceNotFound",
			slots:    currentAvatarOptionChangeTestSlots(),
			body:     currentAvatarOptionChangeTestBody(8, int32(currentAvatarOptionChangeTestRareBox), 5, int32(currentAvatarOptionChangeTestRareAva), 1),
			wantErr:  dnfavataroption.ErrSourceNotFound,
			wantName: "source_not_found",
		},
		{
			name:     "源 itemId 不一致 = SourceItemMismatch",
			slots:    currentAvatarOptionChangeTestSlots(),
			body:     currentAvatarOptionChangeTestBody(9, int32(currentAvatarOptionChangeTestSeniorBox), 5, int32(currentAvatarOptionChangeTestRareAva), 1),
			wantErr:  dnfavataroption.ErrSourceItemMismatch,
			wantName: "source_item_mismatch",
		},
		{
			name: "源不是调整箱 = NotOptionChangeBox",
			slots: slotsWith("0:9", dnfrepo.ItemStack{
				ItemID: int64(currentAvatarOptionChangeTestPlainBox), Count: 5,
			}),
			body:     currentAvatarOptionChangeTestBody(9, int32(currentAvatarOptionChangeTestPlainBox), 5, int32(currentAvatarOptionChangeTestRareAva), 1),
			wantErr:  dnfavataroption.ErrNotOptionChangeBox,
			wantName: "not_option_change_box",
		},
		{
			name:     "目标槽为空 = TargetNotFound",
			slots:    currentAvatarOptionChangeTestSlots(),
			body:     currentAvatarOptionChangeTestBody(9, int32(currentAvatarOptionChangeTestRareBox), 6, int32(currentAvatarOptionChangeTestRareAva), 1),
			wantErr:  dnfavataroption.ErrTargetNotFound,
			wantName: "target_not_found",
		},
		{
			name:     "目标 itemId 不一致 = TargetItemMismatch",
			slots:    currentAvatarOptionChangeTestSlots(),
			body:     currentAvatarOptionChangeTestBody(9, int32(currentAvatarOptionChangeTestRareBox), 5, int32(currentAvatarOptionChangeTestSeniorAva), 1),
			wantErr:  dnfavataroption.ErrTargetItemMismatch,
			wantName: "target_item_mismatch",
		},
		{
			name: "目标不是装扮 = NotAvatar",
			slots: slotsWith("1:5", dnfrepo.ItemStack{
				ItemID: int64(currentAvatarOptionChangeTestWeapon), Count: 1,
				Extra: map[string]string{"item_kind": "equipment"},
			}),
			body:     currentAvatarOptionChangeTestBody(9, int32(currentAvatarOptionChangeTestRareBox), 5, int32(currentAvatarOptionChangeTestWeapon), 1),
			wantErr:  dnfavataroption.ErrNotAvatar,
			wantName: "target_not_avatar",
		},
		{
			name: "目标不在 PVF 物品表 = AvatarMetadataMissing",
			slots: slotsWith("1:5", dnfrepo.ItemStack{
				ItemID: 49999, Count: 1,
				Extra: map[string]string{"item_kind": "avatar"},
			}),
			body:     currentAvatarOptionChangeTestBody(9, int32(currentAvatarOptionChangeTestRareBox), 5, 49999, 1),
			wantErr:  dnfavataroption.ErrAvatarMetadataMissing,
			wantName: "avatar_metadata_missing",
		},
		{
			name: "稀有箱 × 高级装扮 = AvatarGradeMismatch",
			slots: slotsWith("1:5", dnfrepo.ItemStack{
				ItemID: int64(currentAvatarOptionChangeTestSeniorAva), Count: 1,
				Extra: map[string]string{"item_kind": "avatar", dnfavataroption.AbilityNoExtraKey: "0"},
			}),
			body:     currentAvatarOptionChangeTestBody(9, int32(currentAvatarOptionChangeTestRareBox), 5, int32(currentAvatarOptionChangeTestSeniorAva), 1),
			wantErr:  dnfavataroption.ErrAvatarGradeMismatch,
			wantName: "avatar_grade_mismatch",
		},
		{
			name: "高级箱 × 稀有装扮 = AvatarGradeMismatch",
			slots: slotsWith("0:9", dnfrepo.ItemStack{
				ItemID: int64(currentAvatarOptionChangeTestSeniorBox), Count: 1,
			}),
			body:     currentAvatarOptionChangeTestBody(9, int32(currentAvatarOptionChangeTestSeniorBox), 5, int32(currentAvatarOptionChangeTestRareAva), 1),
			wantErr:  dnfavataroption.ErrAvatarGradeMismatch,
			wantName: "avatar_grade_mismatch",
		},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			repositories, catalog := mustCurrentAvatarOptionChangeTestAssets(t, test.slots)
			request, err := decodeCurrentAvatarOptionChangeRequest(test.body)
			if err != nil {
				t.Fatal(err)
			}

			// ① 模块层：失败分类必须与厂商 InventoryAvatarOptionChangeError 对齐。
			_, ownerErr := currentAvatarOptionChangeTestOwner(t, repositories, catalog).ChangeAvatarOption(
				context.Background(),
				dnfavataroption.Command{
					AccountID:    "account-1",
					CharacterID:  "19",
					SourceSlot:   request.SourceSlot,
					SourceItemID: request.SourceItemID,
					TargetSlot:   request.TargetSlot,
					TargetItemID: request.TargetItemID,
					AbilityNo:    request.AbilityNo,
					ProjectAvatarRow: func(stack *dnfrepo.ItemStack) {
						projectCurrentAvatarOptionChangeRow(request.TargetSlot, stack)
					},
				})
			if !errors.Is(ownerErr, test.wantErr) {
				t.Fatalf("owner error = %v want %v", ownerErr, test.wantErr)
			}
			if got := currentAvatarOptionChangeFailureReason(ownerErr); got != test.wantName {
				t.Fatalf("failure reason = %q want %q", got, test.wantName)
			}

			// ② 分发层：必须回厂商失败体（单字节 00），且不改动任何物品。
			connection := &bufferConn{}
			service := currentAvatarCompoundTestService(repositories, catalog)
			if err := service.serveAvatarOptionChange(
				&gameSession{conn: connection, selectedCharacterID: 19}, test.body); err != nil {
				t.Fatal(err)
			}
			ack, rest := splitGameServerUpperPacket(t, connection.write.Bytes())
			if len(rest) != 0 {
				t.Fatalf("failure reply must be a single packet, rest=%x", rest)
			}
			if ack.Header.MsgID != currentAvatarOptionChangeOpcode ||
				ack.Header.Classification != dnfproto.DefaultChannelClassification {
				t.Fatalf("header = %+v", ack.Header)
			}
			if !bytes.Equal(ack.Body, []byte{0x00}) {
				t.Fatalf("failure body = %x want 00", ack.Body)
			}

			inventory := readCurrentAvatarOptionChangeInventory(t, repositories)
			wantSource := test.slots["0:9"]
			if got := inventory.Slots["0:9"]; got.ItemID != wantSource.ItemID || got.Count != wantSource.Count {
				t.Fatalf("source changed on rejection: %+v want %+v", got, wantSource)
			}
			wantTarget := test.slots["1:5"]
			if got := inventory.Slots["1:5"]; got.ItemID != wantTarget.ItemID ||
				got.Extra[dnfavataroption.AbilityNoExtraKey] != wantTarget.Extra[dnfavataroption.AbilityNoExtraKey] {
				t.Fatalf("target changed on rejection: %+v want %+v", got, wantTarget)
			}
		})
	}
}

// TestCurrentAvatarOptionChangeCommitsSourceAndTargetByteForByte 是成功路径：
// 源扣 1、目标 AbilityNo 落库（extra + 0x77 行 +0x0B）、ACK 4 字节逐字节，
// 随后按厂商口径刷新主背包源槽（class0/op14 单行）。
func TestCurrentAvatarOptionChangeCommitsSourceAndTargetByteForByte(t *testing.T) {
	repositories, catalog := mustCurrentAvatarOptionChangeTestAssets(t, currentAvatarOptionChangeTestSlots())
	connection := &bufferConn{}
	service := currentAvatarCompoundTestService(repositories, catalog)
	body := currentAvatarOptionChangeTestBody(9, int32(currentAvatarOptionChangeTestRareBox), 5, int32(currentAvatarOptionChangeTestRareAva), 47)
	if err := service.serveAvatarOptionChange(&gameSession{conn: connection, selectedCharacterID: 19}, body); err != nil {
		t.Fatal(err)
	}

	stream := connection.write.Bytes()
	ack, rest := splitGameServerUpperPacket(t, stream)
	if ack.Header.MsgID != currentAvatarOptionChangeOpcode ||
		ack.Header.Classification != dnfproto.DefaultChannelClassification {
		t.Fatalf("ACK header = %+v", ack.Header)
	}
	// 厂商 BuildSuccess：u8 0x01 | i16 targetSlot | u8 abilityNo = 4 字节。
	if !bytes.Equal(ack.Body, []byte{0x01, 0x05, 0x00, 0x2f}) {
		t.Fatalf("ACK body = %x want 0105002f", ack.Body)
	}
	// 厂商随后 SendUpdateItemList(Main, sourceSlotIndex)：本仓用 class0/op14 单行刷新。
	refresh, tail := splitGameServerUpperPacket(t, rest)
	if len(tail) != 0 {
		t.Fatalf("unexpected extra packets: %x", tail)
	}
	if refresh.Header.MsgID != uint16(dnfenum.CmdPacketWalkoutPartyMember) || refresh.Header.Classification != 0 {
		t.Fatalf("refresh header = %+v", refresh.Header)
	}
	if len(refresh.Body) != 3+currentItemListEntryWireSize {
		t.Fatalf("refresh body len = %d want %d", len(refresh.Body), 3+currentItemListEntryWireSize)
	}
	if refresh.Body[0] != dnfrepo.MainInventoryListType ||
		binary.LittleEndian.Uint16(refresh.Body[1:3]) != 1 {
		t.Fatalf("refresh body head = %x", refresh.Body[:3])
	}
	entry := refresh.Body[3:]
	if got := int16(binary.LittleEndian.Uint16(entry[0:2])); got != 9 {
		t.Fatalf("refresh slot = %d want 9", got)
	}
	if got := binary.LittleEndian.Uint32(entry[2:6]); got != currentAvatarOptionChangeTestRareBox {
		t.Fatalf("refresh item = %d want %d", got, currentAvatarOptionChangeTestRareBox)
	}
	if got := binary.LittleEndian.Uint32(entry[0x06:0x0A]); got != 1 {
		t.Fatalf("refresh remaining count = %d want 1", got)
	}

	inventory := readCurrentAvatarOptionChangeInventory(t, repositories)
	source := inventory.Slots["0:9"]
	if source.ItemID != int64(currentAvatarOptionChangeTestRareBox) || source.Count != 1 {
		t.Fatalf("source after commit = %+v want count 1", source)
	}
	target := inventory.Slots["1:5"]
	if target.ItemID != int64(currentAvatarOptionChangeTestRareAva) {
		t.Fatalf("target row = %+v", target)
	}
	if got := target.Extra[dnfavataroption.AbilityNoExtraKey]; got != "47" {
		t.Fatalf("persisted ext_data0 = %q want 47", got)
	}
	if got := realPVFAvatarAbilityWord(target.RawEntry); got != 47 {
		t.Fatalf("persisted 0x77 ability word = %d want 47 (raw=%x)", got, target.RawEntry)
	}
	if target.RawEntry[currentItemListExtDataByteOffset] != 0 {
		t.Fatalf("avatar row +0x0A = %d want 0", target.RawEntry[currentItemListExtDataByteOffset])
	}
}

// TestCurrentAvatarOptionChangeRemovesTheLastBox 钉住"最后一个箱子用完即删行"，
// 刷新行必须是空行（itemID = 0xFFFFFFFF），否则客户端会留着已经用掉的箱子。
func TestCurrentAvatarOptionChangeRemovesTheLastBox(t *testing.T) {
	slots := currentAvatarOptionChangeTestSlots()
	last := slots["0:9"]
	last.Count = 1
	slots["0:9"] = last
	repositories, catalog := mustCurrentAvatarOptionChangeTestAssets(t, slots)
	connection := &bufferConn{}
	service := currentAvatarCompoundTestService(repositories, catalog)
	body := currentAvatarOptionChangeTestBody(9, int32(currentAvatarOptionChangeTestRareBox), 5, int32(currentAvatarOptionChangeTestRareAva), 0)
	if err := service.serveAvatarOptionChange(&gameSession{conn: connection, selectedCharacterID: 19}, body); err != nil {
		t.Fatal(err)
	}
	ack, rest := splitGameServerUpperPacket(t, connection.write.Bytes())
	if !bytes.Equal(ack.Body, []byte{0x01, 0x05, 0x00, 0x00}) {
		t.Fatalf("ACK body = %x want 01050000", ack.Body)
	}
	refresh, _ := splitGameServerUpperPacket(t, rest)
	entry := refresh.Body[3:]
	if got := binary.LittleEndian.Uint32(entry[2:6]); got != math.MaxUint32 {
		t.Fatalf("removed-row item id = %d want 0xFFFFFFFF", got)
	}
	inventory := readCurrentAvatarOptionChangeInventory(t, repositories)
	if _, exists := inventory.Slots["0:9"]; exists {
		t.Fatalf("exhausted box row survived: %+v", inventory.Slots["0:9"])
	}
}

// TestCurrentAvatarOptionChangeSwitchRollsBackByteForByte 是 DNFBRIDGE_AVATAR_OPTION_CHANGE
// 的一键回退档：off/0/false/legacy（以及 no/disabled）都必须 handled=false 且零下行字节，
// 即控制流与本改动前完全一致（继续落到 unhandled 兜底）。
func TestCurrentAvatarOptionChangeSwitchRollsBackByteForByte(t *testing.T) {
	body := currentAvatarOptionChangeTestBody(9, int32(currentAvatarOptionChangeTestRareBox), 5, int32(currentAvatarOptionChangeTestRareAva), 47)
	for _, value := range []string{"off", "0", "false", "legacy", "OFF", " legacy "} {
		t.Run(value, func(t *testing.T) {
			t.Setenv(currentAvatarOptionChangeEnv, value)
			if !currentAvatarOptionChangeDisabled() {
				t.Fatalf("value %q must disable the route", value)
			}
			repositories, catalog := mustCurrentAvatarOptionChangeTestAssets(t, currentAvatarOptionChangeTestSlots())
			connection := &bufferConn{}
			service := currentAvatarCompoundTestService(repositories, catalog)
			session := &gameSession{conn: connection, selectedCharacterID: 19}
			handled, err := service.handleAvatarOptionChangeCommand(session, currentAvatarOptionChangeOpcode, body)
			if handled || err != nil {
				t.Fatalf("legacy handled=%t err=%v want false/nil", handled, err)
			}
			handled, err = service.handleAvatarOptionChangeUpperCommand(
				session, currentAvatarOptionChangeOpcode, dnfproto.DefaultChannelClassification, body)
			if handled || err != nil {
				t.Fatalf("upper handled=%t err=%v want false/nil", handled, err)
			}
			if connection.write.Len() != 0 {
				t.Fatalf("rollback 档 must not send anything, got %x", connection.write.Bytes())
			}
			inventory := readCurrentAvatarOptionChangeInventory(t, repositories)
			if got := inventory.Slots["0:9"].Count; got != 2 {
				t.Fatalf("rollback 档 source count = %d want 2", got)
			}
		})
	}
	// 默认档（未设置）必须是修复档。
	t.Setenv(currentAvatarOptionChangeEnv, "")
	if currentAvatarOptionChangeDisabled() {
		t.Fatal("unset DNFBRIDGE_AVATAR_OPTION_CHANGE must default to the fixed route")
	}
}

// TestCurrentAvatarOptionChangeDefaultIsOnAndOnlyHandlesOp459 钉住 opcode 归属：
// 只有 459 归本路径，别的包必须原样放行。
func TestCurrentAvatarOptionChangeDefaultIsOnAndOnlyHandlesOp459(t *testing.T) {
	t.Setenv(currentAvatarOptionChangeEnv, "")
	repositories, catalog := mustCurrentAvatarOptionChangeTestAssets(t, currentAvatarOptionChangeTestSlots())
	service := currentAvatarCompoundTestService(repositories, catalog)
	session := &gameSession{conn: &bufferConn{}, selectedCharacterID: 19}
	for _, other := range []uint16{458, 460, 518, 99} {
		handled, err := service.handleAvatarOptionChangeCommand(session, other, nil)
		if handled || err != nil {
			t.Fatalf("type=%d handled=%t err=%v want false/nil", other, handled, err)
		}
		handled, err = service.handleAvatarOptionChangeUpperCommand(
			session, other, dnfproto.DefaultChannelClassification, nil)
		if handled || err != nil {
			t.Fatalf("upper msg=%d handled=%t err=%v want false/nil", other, handled, err)
		}
	}
	// 非默认 channel 分类的 upper 459 必须被挡下且不回包（与 defaultClassGameplayHandler 同口径）。
	connection := &bufferConn{}
	blocked := &gameSession{conn: connection, selectedCharacterID: 19}
	handled, err := service.handleAvatarOptionChangeUpperCommand(blocked, currentAvatarOptionChangeOpcode, 0, nil)
	if !handled || err != nil {
		t.Fatalf("mismatched classification handled=%t err=%v want true/nil", handled, err)
	}
	if connection.write.Len() != 0 {
		t.Fatalf("mismatched classification must not reply, got %x", connection.write.Bytes())
	}
}

// TestCurrentAvatarOptionChangeLiveBodyShape 用现场抓包的 13 字节体逐字节回放解析：
// 体来自 packet_log.20260926-022031.txt:14569（三次右键包体相同）。
func TestCurrentAvatarOptionChangeLiveBodyShape(t *testing.T) {
	// 13 字节 legacy body（去掉 13 字节头 01cb011a000000d0cfb4ff2a01 之后的部分）。
	live := []byte{
		0x49, 0x00, 0x64, 0x98, 0x28, 0x00, 0x33, 0x00,
		0x1e, 0x9f, 0x00, 0x00, 0x2f,
	}
	request, err := decodeCurrentAvatarOptionChangeRequest(live)
	if err != nil {
		t.Fatal(err)
	}
	if request.SourceSlot != 73 || request.SourceItemID != 2660452 ||
		request.TargetSlot != 51 || request.TargetItemID != 40734 || request.AbilityNo != 47 {
		t.Fatalf("live request = %+v", request)
	}
}

// buildCurrentAvatarOptionChangeLiveLegacyPacket 逐字节重建现场那一帧
// （packet_log.20260926-022031.txt:14569 的 hex），用于验证**真实分发路径**
// （handleLegacyGamePacket → handleGameCommand → 本钩子）确实接上了。
func buildCurrentAvatarOptionChangeLiveLegacyPacket() []byte {
	body := realPVFAvatarOptionChangeLiveBody()
	packet := make([]byte, dnfproto.LegacyGameHeaderSize+len(body))
	packet[0] = byte(dnfenum.GameCmdCommand)
	binary.LittleEndian.PutUint16(packet[1:3], currentAvatarOptionChangeOpcode)
	binary.LittleEndian.PutUint32(packet[3:7], uint32(len(packet)))
	copy(packet[7:11], []byte{0xd0, 0xcf, 0xb4, 0xff})
	binary.LittleEndian.PutUint16(packet[11:13], 298)
	copy(packet[dnfproto.LegacyGameHeaderSize:], body)
	return packet
}

// TestCurrentAvatarOptionChangeLegacyDispatchIsWired 是分发接线回归：现场那一帧原样
// 走 handleLegacyGamePacket，默认档必须产出 op459 ACK + 主背包源槽 op14 刷新；
// 回退档必须一个字节都不发（控制流回到改动前的 unhandled 兜底）。
func TestCurrentAvatarOptionChangeLegacyDispatchIsWired(t *testing.T) {
	slots := map[string]dnfrepo.ItemStack{
		"0:73": {ItemID: int64(realPVFAvatarOptionChangeLiveSourceItem), Count: 2},
	}
	raw := buildCurrentAvatarOptionChangeLiveLegacyPacket()
	if len(raw) != 26 || raw[0] != 1 || binary.LittleEndian.Uint16(raw[1:3]) != 459 {
		t.Fatalf("rebuilt live frame = %x", raw)
	}

	t.Setenv(currentAvatarOptionChangeEnv, "")
	repositories, catalog := mustCurrentAvatarOptionChangeTestAssets(t, slots)
	connection := &bufferConn{}
	service := currentAvatarCompoundTestService(repositories, catalog)
	session := &gameSession{conn: connection, connID: "avatar-option-change-test", selectedCharacterID: 19}
	if err := service.handleLegacyGamePacket(session, raw); err != nil {
		t.Fatal(err)
	}
	ack, rest := splitGameServerUpperPacket(t, connection.write.Bytes())
	if ack.Header.MsgID != currentAvatarOptionChangeOpcode ||
		ack.Header.Classification != dnfproto.DefaultChannelClassification {
		t.Fatalf("dispatch ACK header = %+v", ack.Header)
	}
	// 夹具里没有 1:51 这件装扮 ⇒ 这是"分发真的走到了 owner"的失败回包（单字节 00）。
	if !bytes.Equal(ack.Body, []byte{0x00}) {
		t.Fatalf("dispatch ACK body = %x want 00", ack.Body)
	}
	if len(rest) != 0 {
		t.Fatalf("failure dispatch must not refresh, rest=%x", rest)
	}

	// 回退档：同一帧、同一会话形状，必须零字节。
	t.Setenv(currentAvatarOptionChangeEnv, "legacy")
	rollbackConnection := &bufferConn{}
	rollbackRepositories, rollbackCatalog := mustCurrentAvatarOptionChangeTestAssets(t, slots)
	rollbackService := currentAvatarCompoundTestService(rollbackRepositories, rollbackCatalog)
	rollbackSession := &gameSession{
		conn: rollbackConnection, connID: "avatar-option-change-test", selectedCharacterID: 19,
	}
	if err := rollbackService.handleLegacyGamePacket(rollbackSession, raw); err != nil {
		t.Fatal(err)
	}
	if rollbackConnection.write.Len() != 0 {
		t.Fatalf("rollback 档 dispatch sent %x", rollbackConnection.write.Bytes())
	}
}
