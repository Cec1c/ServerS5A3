package dnfbridge

import (
	"bytes"
	"context"
	"errors"
	"os"
	"testing"

	dnfavataroption "longheng.io/server/internal/modules/dnf/avataroptionchange"
	dnfproto "longheng.io/server/internal/modules/dnf/protocol"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

// 真档 PVF 回归：`DNFBRIDGE_REAL_PVF_SMOKE` 指向线上同源的 Script.pvf 时才跑。
//
// 现场（HK `packet_log.20260926-022031.txt:14569`，操作者三次右键「装扮属性调整箱 (稀有)」）：
//
//	body = 49 00 | 64 98 28 00 | 33 00 | 1e 9f 00 00 | 2f
//	     = source(73, 2660452) target(51, 40734) ability=47
//
// 真档对这两件道具的声明（本文件逐条断言）：
//
//	2660452 = stackable/cash/avatar_optionchange1.stk 「装扮属性调整箱 (稀有)」
//	          [avatar select ability change] = `[all]` `[all]` 3 0   ⇒ AvatarGrade=3
//	40734   = equipment/character/swordman/avatar/coat/afparmor_dkknight.equ
//	          「深渊骑士胸甲」 [coat avatar] [grade] 3             ⇒ 稀有装扮，应成功
//
// 而同一只箱子作用在 `[grade] 2` 的普通高级装扮上必须被拒（厂商口径：
// 箱子只接受 `AvatarGrade == equipment.Grade`）。
const (
	realPVFAvatarOptionChangeLiveSourceItem = uint32(2660452)
	realPVFAvatarOptionChangeLiveTargetItem = uint32(40734)
	realPVFAvatarOptionChangeSeniorAvatar   = uint32(55571)
	realPVFAvatarOptionChangeSeniorBox      = uint32(2660834)
)

// realPVFAvatarOptionChangeLiveBody 是现场 13 字节 legacy body 的逐字节副本。
func realPVFAvatarOptionChangeLiveBody() []byte {
	return []byte{
		0x49, 0x00, 0x64, 0x98, 0x28, 0x00, 0x33, 0x00,
		0x1e, 0x9f, 0x00, 0x00, 0x2f,
	}
}

// TestRealPVFAvatarOptionChangeBoxGrades 钉住真档 47 只箱子的段解析口径：
// `[avatar select ability change]` 每行第一列就是 AvatarGrade，
// 稀有箱 = 3、高级箱 = 2（`[all] [all] <grade> <param>`）。
func TestRealPVFAvatarOptionChangeBoxGrades(t *testing.T) {
	real := realAvatarCompoundPVF(t)
	catalog := real.catalog
	cases := []struct {
		itemID     uint32
		wantPath   string
		wantGrades []int64
	}{
		{itemID: realPVFAvatarOptionChangeLiveSourceItem, wantPath: "stackable/cash/avatar_optionchange1.stk", wantGrades: []int64{3}},
		{itemID: realPVFAvatarOptionChangeSeniorBox, wantPath: "stackable/cash/avatar_optionchange11.stk", wantGrades: []int64{2}},
	}
	for _, test := range cases {
		definition, err := catalog.ResolveItem(test.itemID)
		if err != nil {
			t.Fatalf("resolve box=%d: %v", test.itemID, err)
		}
		if definition.PVFPath != test.wantPath {
			t.Fatalf("box=%d path=%q want %q", test.itemID, definition.PVFPath, test.wantPath)
		}
		if definition.Kind != dungeonDropItemStackable {
			t.Fatalf("box=%d kind=%v want stackable", test.itemID, definition.Kind)
		}
		if len(definition.AvatarOptionChangeGrades) != len(test.wantGrades) {
			t.Fatalf("box=%d grades=%v want %v", test.itemID, definition.AvatarOptionChangeGrades, test.wantGrades)
		}
		for index, want := range test.wantGrades {
			if got := definition.AvatarOptionChangeGrades[index]; got != want {
				t.Fatalf("box=%d grades[%d]=%d want %d (all=%v)",
					test.itemID, index, got, want, definition.AvatarOptionChangeGrades)
			}
		}
		t.Logf("box=%d path=%s avatar_grades=%v", test.itemID, definition.PVFPath, definition.AvatarOptionChangeGrades)
	}
	// 没有该段的普通堆叠品一个都不许被当成箱子。
	plain, err := catalog.ResolveItem(21)
	if err != nil {
		t.Fatal(err)
	}
	if len(plain.AvatarOptionChangeGrades) != 0 {
		t.Fatalf("item 21 must not declare [avatar select ability change]: %v", plain.AvatarOptionChangeGrades)
	}
}

// TestRealPVFAvatarOptionChangeLiveRequestIsAccepted 是现场请求的逐字节回放：
// 稀有箱 2660452 × 稀有装扮 40734（[grade] 3）必须成功，ACK 必须是厂商 4 字节形状
// `01 | i16 targetSlot(51) | u8 abilityNo(47)`，源扣 1，目标属性落库 47。
func TestRealPVFAvatarOptionChangeLiveRequestIsAccepted(t *testing.T) {
	real := realAvatarCompoundPVF(t)
	slots := map[string]dnfrepo.ItemStack{
		"0:73": {ItemID: int64(realPVFAvatarOptionChangeLiveSourceItem), Count: 2},
		"1:51": {
			ItemID: int64(realPVFAvatarOptionChangeLiveTargetItem),
			Count:  1,
			Extra: map[string]string{
				"item_kind":                       "avatar",
				"equipment_type":                  "[coat avatar]",
				dnfavataroption.AbilityNoExtraKey: "0",
			},
		},
	}
	repositories, catalog := mustAvatarCompoundLiveAssets(t, real.source, "0", slots)
	connection := &bufferConn{}
	service := currentAvatarCompoundTestService(repositories, catalog)
	body := realPVFAvatarOptionChangeLiveBody()
	if err := service.serveAvatarOptionChange(&gameSession{conn: connection, selectedCharacterID: 19}, body); err != nil {
		t.Fatal(err)
	}
	ack, rest := splitGameServerUpperPacket(t, connection.write.Bytes())
	if ack.Header.MsgID != currentAvatarOptionChangeOpcode ||
		ack.Header.Classification != dnfproto.DefaultChannelClassification {
		t.Fatalf("ACK header = %+v", ack.Header)
	}
	if !bytes.Equal(ack.Body, []byte{0x01, 0x33, 0x00, 0x2f}) {
		t.Fatalf("ACK body = %x want 0133002f", ack.Body)
	}
	refresh, tail := splitGameServerUpperPacket(t, rest)
	if len(tail) != 0 || len(refresh.Body) != 3+currentItemListEntryWireSize {
		t.Fatalf("refresh tail=%x body_len=%d", tail, len(refresh.Body))
	}
	if refresh.Body[0] != dnfrepo.MainInventoryListType || refresh.Body[3] != 0x49 {
		t.Fatalf("refresh body head = %x", refresh.Body[:6])
	}

	inventory := readCurrentAvatarOptionChangeInventory(t, repositories)
	if got := inventory.Slots["0:73"]; got.ItemID != int64(realPVFAvatarOptionChangeLiveSourceItem) || got.Count != 1 {
		t.Fatalf("live source after commit = %+v want count 1", got)
	}
	target := inventory.Slots["1:51"]
	if target.ItemID != int64(realPVFAvatarOptionChangeLiveTargetItem) {
		t.Fatalf("live target row = %+v", target)
	}
	if got := target.Extra[dnfavataroption.AbilityNoExtraKey]; got != "47" {
		t.Fatalf("persisted ability = %q want 47", got)
	}
	if got := realPVFAvatarAbilityWord(target.RawEntry); got != 47 {
		t.Fatalf("persisted 0x77 ability word = %d want 47", got)
	}
}

// TestRealPVFAvatarOptionChangeRejectsSeniorAvatar 是"稀有箱 × 非稀有装扮"的拒绝：
// 同一只稀有箱（AvatarGrade=3）作用在 [grade] 2 的高级装扮上必须
// AvatarGradeMismatch，并且一个字节的库存都不许变。
func TestRealPVFAvatarOptionChangeRejectsSeniorAvatar(t *testing.T) {
	real := realAvatarCompoundPVF(t)
	senior, err := real.catalog.ResolveItem(realPVFAvatarOptionChangeSeniorAvatar)
	if err != nil {
		t.Fatal(err)
	}
	if senior.Kind != dungeonDropItemEquipment || senior.Grade != 2 {
		t.Fatalf("fixture avatar %d is not a [grade] 2 equipment: %+v",
			realPVFAvatarOptionChangeSeniorAvatar, senior)
	}
	slots := map[string]dnfrepo.ItemStack{
		"0:9": {ItemID: int64(realPVFAvatarOptionChangeLiveSourceItem), Count: 1},
		"1:5": {
			ItemID: int64(realPVFAvatarOptionChangeSeniorAvatar),
			Count:  1,
			Extra: map[string]string{
				"item_kind":                       "avatar",
				"equipment_type":                  senior.EquipmentType,
				dnfavataroption.AbilityNoExtraKey: "0",
			},
		},
	}
	repositories, catalog := mustAvatarCompoundLiveAssets(t, real.source, "0", slots)
	body := currentAvatarOptionChangeTestBody(
		9, int32(realPVFAvatarOptionChangeLiveSourceItem), 5, int32(realPVFAvatarOptionChangeSeniorAvatar), 3)
	request, err := decodeCurrentAvatarOptionChangeRequest(body)
	if err != nil {
		t.Fatal(err)
	}
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
	if !errors.Is(ownerErr, dnfavataroption.ErrAvatarGradeMismatch) {
		t.Fatalf("owner error = %v want ErrAvatarGradeMismatch", ownerErr)
	}

	connection := &bufferConn{}
	service := currentAvatarCompoundTestService(repositories, catalog)
	if err := service.serveAvatarOptionChange(&gameSession{conn: connection, selectedCharacterID: 19}, body); err != nil {
		t.Fatal(err)
	}
	ack, _ := splitGameServerUpperPacket(t, connection.write.Bytes())
	if !bytes.Equal(ack.Body, []byte{0x00}) {
		t.Fatalf("rejection body = %x want 00", ack.Body)
	}
	inventory := readCurrentAvatarOptionChangeInventory(t, repositories)
	if got := inventory.Slots["0:9"]; got.Count != 1 {
		t.Fatalf("rejected request consumed the box: %+v", got)
	}
	if got := inventory.Slots["1:5"].Extra[dnfavataroption.AbilityNoExtraKey]; got != "0" {
		t.Fatalf("rejected request changed the avatar ability: %q", got)
	}
	t.Logf("senior avatar %d grade=%d path=%s rejected by rare box",
		realPVFAvatarOptionChangeSeniorAvatar, senior.Grade, senior.PVFPath)
}

// TestRealPVFAvatarOptionChangeSeniorBoxAcceptsSeniorAvatar 反向对照：
// 高级箱（AvatarGrade=2）× 高级装扮（[grade] 2）必须成功，证明判据是"品级相等"
// 而不是"只认稀有"。
func TestRealPVFAvatarOptionChangeSeniorBoxAcceptsSeniorAvatar(t *testing.T) {
	real := realAvatarCompoundPVF(t)
	slots := map[string]dnfrepo.ItemStack{
		"0:9": {ItemID: int64(realPVFAvatarOptionChangeSeniorBox), Count: 1},
		"1:5": {
			ItemID: int64(realPVFAvatarOptionChangeSeniorAvatar),
			Count:  1,
			Extra: map[string]string{
				"item_kind":                       "avatar",
				dnfavataroption.AbilityNoExtraKey: "0",
			},
		},
	}
	repositories, catalog := mustAvatarCompoundLiveAssets(t, real.source, "0", slots)
	connection := &bufferConn{}
	service := currentAvatarCompoundTestService(repositories, catalog)
	body := currentAvatarOptionChangeTestBody(
		9, int32(realPVFAvatarOptionChangeSeniorBox), 5, int32(realPVFAvatarOptionChangeSeniorAvatar), 3)
	if err := service.serveAvatarOptionChange(&gameSession{conn: connection, selectedCharacterID: 19}, body); err != nil {
		t.Fatal(err)
	}
	ack, _ := splitGameServerUpperPacket(t, connection.write.Bytes())
	if !bytes.Equal(ack.Body, []byte{0x01, 0x05, 0x00, 0x03}) {
		t.Fatalf("ACK body = %x want 01050003", ack.Body)
	}
}

// TestRealPVFAvatarOptionChangeSwitchOffLeavesRealRequestUntouched 在真档上确认
// 回退档：同一条现场请求在 off 档必须 handled=false 且零下行字节。
func TestRealPVFAvatarOptionChangeSwitchOffLeavesRealRequestUntouched(t *testing.T) {
	if os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE") == "" {
		t.Skip("set DNFBRIDGE_REAL_PVF_SMOKE to the runtime Script.pvf")
	}
	t.Setenv(currentAvatarOptionChangeEnv, "legacy")
	connection := &bufferConn{}
	service := &Service{}
	session := &gameSession{conn: connection, selectedCharacterID: 19}
	handled, err := service.handleAvatarOptionChangeCommand(session, currentAvatarOptionChangeOpcode, realPVFAvatarOptionChangeLiveBody())
	if handled || err != nil {
		t.Fatalf("legacy 档 handled=%t err=%v want false/nil", handled, err)
	}
	if connection.write.Len() != 0 {
		t.Fatalf("legacy 档 must not send anything, got %x", connection.write.Bytes())
	}
}
