package dnfbridge

import (
	"bytes"
	"context"
	"os"
	"testing"

	"longheng.io/server/internal/modules/dnf/dnfenum"
	dnfproto "longheng.io/server/internal/modules/dnf/protocol"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	dnfrepomemory "longheng.io/server/internal/modules/dnf/repository/memory"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

// 现场报障（操作者原话）："商城 快乐暑假行程单 无效"。
//
// 运行时 PVF 的真身是 `stackable/cash/chn_490702507.stk`（item 490702507，
// `[name] 欢乐暑假行程单`，`[stackable type] [usable cera package]`，
// `[package data] 490702504 1`），奖励 490702504 是 `[booster]` 礼盒——**没有装扮**。
// `[usable cera package]` 在运行时 PVF 里是 5454 件道具的大族，绝大多数都没有装扮
// 奖励，而旧代码要求 `avatar_choices > 0`、并把 count==0 的 op518 请求体当畸形，
// 于是这些礼包一律拿到失败体 4（客户端表现就是"道具无效"）。

// ceraPackageNoAvatarTestCatalog 造一件"奖励里没有装扮"的 cera 礼包：
// 源道具 505（[usable cera package]）-> 奖励 600（[material] 堆叠品）× 3。
func ceraPackageNoAvatarTestCatalog(t *testing.T) *pvfDungeonDropCatalog {
	t.Helper()
	source := dungeonDropCatalogTestSource{
		"monster/monster.lst":     "",
		"stackable/stackable.lst": "505 `cash/test_noavatar_package.stk`\n600 `material/test_reward.stk`\n",
		"equipment/equipment.lst": "",
		"stackable/cash/test_noavatar_package.stk": "[stackable type]\n`[usable cera package]`\n" +
			"[stack limit]\n100\n[expiration date]\n`2028-08-16 06:00:00`\n[package data]\n600 3\n[/package data]\n",
		"stackable/material/test_reward.stk": "[stackable type]\n`[material]`\n[stack limit]\n999\n[usable period]\n7\n",
	}
	catalog, err := newPVFDungeonDropCatalog(source)
	if err != nil {
		t.Fatal(err)
	}
	return catalog
}

func TestCurrentCeraPackageWithoutAvatarRewardsResolvesAndOpensWithZeroChoices(t *testing.T) {
	// 回退档（containers）：没有装扮奖励时照旧只回放一个 list 0 整容器 op13。
	// 默认档（delta）对 (0,75)/(0,3) 发逐槽 op14 且一个整容器都不发。见 item_grant_notice.go。
	pinItemGrantNoticeContainers(t)
	catalog := ceraPackageNoAvatarTestCatalog(t)
	definition, err := resolveCurrentCeraPackageDefinition(catalog, 505)
	if err != nil {
		t.Fatalf("a [usable cera package] without avatar rewards must resolve: %v", err)
	}
	if definition.Source.ItemID != 505 || len(definition.Rewards) != 1 || definition.Rewards[0].Definition.ItemID != 600 ||
		definition.Rewards[0].Count != 3 || len(definition.AvatarItemIDs) != 0 {
		t.Fatalf("definition=%+v", definition)
	}

	ctx := context.Background()
	repositories := dnfrepomemory.NewMemoryGroup()
	if err := repositories.Character.Save(ctx, dnfrepo.CharacterRecord{CharacterID: "19", AccountID: "account-1"}); err != nil {
		t.Fatal(err)
	}
	sourceStack := dnfrepo.ItemStack{ItemID: 505, Count: 2}
	sourceEntry := currentItemListEntryFromStack(dnfrepo.MainInventoryListType, 75, sourceStack)
	sourceStack.RawEntry = append([]byte(nil), sourceEntry.data[:]...)
	if err := repositories.Inventory.Save(ctx, dnfrepo.InventoryRecord{
		CharacterID: "19",
		Slots:       map[string]dnfrepo.ItemStack{"0:75": sourceStack},
	}); err != nil {
		t.Fatal(err)
	}
	connection := &bufferConn{}
	service := &Service{
		options: options{
			accountID:          "account-1",
			gameUpperHeader:    gameUpperHeaderChannel13,
			gameUpperBodyCodec: gameUpperBodyCodecPlain,
		},
		pvfItemCatalog:     catalog,
		repositoryProvider: func() (dnfrepo.Group, bool) { return repositories, true },
	}
	session := &gameSession{conn: connection, connID: "cera-package-noavatar", selectedCharacterID: 19}

	// 当前客户端的 op518 请求体：`[u16 源槽位][u8 选装扮数]`，没有装扮时 count==0。
	requestBody := currentCeraPackageTestRequestBody(75)
	if len(requestBody) != currentCeraPackageRequestHeaderSize {
		t.Fatalf("fixture request body=%x", requestBody)
	}
	if err := service.handleGameCommand(
		session,
		byte(dnfenum.GameCmdCommand),
		uint16(dnfenum.CmdPacketOpenCerapackage),
		requestBody,
	); err != nil {
		t.Fatal(err)
	}
	ack, rest := splitGameServerUpperPacket(t, connection.write.Bytes())
	if ack.Header.Classification != dnfproto.DefaultChannelClassification ||
		ack.Header.MsgID != uint16(dnfenum.CmdPacketOpenCerapackage) ||
		!bytes.Equal(ack.Body, []byte{1, 75, 0}) {
		t.Fatalf("op518 ACK header=%+v body=%x", ack.Header, ack.Body)
	}
	list0, trailing := splitCurrentSceneItemListPacket(t, rest)
	if len(trailing) != 0 || len(list0.Body) == 0 || list0.Body[0] != dnfrepo.MainInventoryListType {
		t.Fatalf("list0 body=%x trailing=%d", list0.Body, len(trailing))
	}

	inventory, found, err := repositories.Inventory.Load(ctx, "19")
	if err != nil || !found {
		t.Fatalf("load inventory found=%t err=%v", found, err)
	}
	if source := inventory.Slots["0:75"]; source.ItemID != 505 || source.Count != 1 {
		t.Fatalf("source after open=%+v, want item 505 count 1", source)
	}
	// [material] 家族直接进入材料页 121..176；此处核对恰好一行 600×3。
	rewardSlots := make([]string, 0, 1)
	rewardUnits := int64(0)
	for key, stack := range inventory.Slots {
		if stack.ItemID != 600 {
			continue
		}
		rewardSlots = append(rewardSlots, key)
		rewardUnits += stack.Count
		if stack.Extra["source"] != "cera_package" {
			t.Fatalf("reward provenance=%+v", stack.Extra)
		}
	}
	if len(rewardSlots) != 1 || rewardUnits != 3 {
		t.Fatalf("reward rows=%v units=%d, want exactly one row of item 600 x3", rewardSlots, rewardUnits)
	}
}

func TestCurrentCeraPackageZeroChoiceRequestStaysFailClosedForAvatarPackages(t *testing.T) {
	// 3 字节 = [u16 源槽位][u8 0] 是合法形状。
	request, err := parseCurrentCeraPackageOpenRequest([]byte{75, 0, 0})
	if err != nil {
		t.Fatalf("zero-choice op518 body must parse: %v", err)
	}
	if request.SourceSlot != 75 || len(request.Choices) != 0 {
		t.Fatalf("request=%+v", request)
	}
	// 仍然拒绝：尾随字节、声明了选项却没有选项体、源槽位后的畸形长度。
	for _, malformed := range [][]byte{
		nil,
		{75, 0},
		{75, 0, 0, 0},
		{75, 0, 1, 0, 0, 0, 0, 9},
		{75, 0, 2, 188, 2, 0, 0, 9, 188, 2, 0, 0, 3},
	} {
		if _, err := parseCurrentCeraPackageOpenRequest(malformed); err == nil {
			t.Fatalf("malformed body accepted: %x", malformed)
		}
	}

	// 有装扮奖励的礼包：客户端没带任何选择时必须整单失败，绝不发一半奖励。
	catalog := mustCurrentCeraPackageTestCatalog(t)
	ctx := context.Background()
	repositories := dnfrepomemory.NewMemoryGroup()
	if err := repositories.Character.Save(ctx, dnfrepo.CharacterRecord{CharacterID: "19", AccountID: "account-1"}); err != nil {
		t.Fatal(err)
	}
	sourceStack := dnfrepo.ItemStack{ItemID: 500, Count: 1}
	sourceEntry := currentItemListEntryFromStack(dnfrepo.MainInventoryListType, 75, sourceStack)
	sourceStack.RawEntry = append([]byte(nil), sourceEntry.data[:]...)
	if err := repositories.Inventory.Save(ctx, dnfrepo.InventoryRecord{
		CharacterID: "19",
		Slots:       map[string]dnfrepo.ItemStack{"0:75": sourceStack},
	}); err != nil {
		t.Fatal(err)
	}
	connection := &bufferConn{}
	service := &Service{
		options: options{
			accountID:          "account-1",
			gameUpperHeader:    gameUpperHeaderChannel13,
			gameUpperBodyCodec: gameUpperBodyCodecPlain,
		},
		pvfItemCatalog:     catalog,
		repositoryProvider: func() (dnfrepo.Group, bool) { return repositories, true },
	}
	session := &gameSession{conn: connection, connID: "cera-package-zerochoices", selectedCharacterID: 19}
	if err := service.handleGameCommand(
		session,
		byte(dnfenum.GameCmdCommand),
		uint16(dnfenum.CmdPacketOpenCerapackage),
		currentCeraPackageTestRequestBody(75),
	); err != nil {
		t.Fatal(err)
	}
	ack, trailing := splitGameServerUpperPacket(t, connection.write.Bytes())
	if len(trailing) != 0 || len(ack.Body) == 0 || ack.Body[0] != 0 {
		t.Fatalf("an avatar package without selections must fail: ack=%x trailing=%d", ack.Body, len(trailing))
	}
	inventory, _, _ := repositories.Inventory.Load(ctx, "19")
	if source := inventory.Slots["0:75"]; source.ItemID != 500 || source.Count != 1 {
		t.Fatalf("rejected open consumed the source: %+v", source)
	}
	if len(inventory.Slots) != 1 {
		t.Fatalf("rejected open granted rewards: %+v", inventory.Slots)
	}
}

// TestRealPVFCeraPackageSummerItineraryIsANonAvatarPackage 是现场取证：
// `欢乐暑假行程单`（490702507）在运行时 PVF 里是一件 [usable cera package]，
// 奖励只有 490702504（[booster] 首日度假礼盒），没有任何装扮选项。
func TestRealPVFCeraPackageSummerItineraryIsANonAvatarPackage(t *testing.T) {
	pvfPath := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if pvfPath == "" {
		t.Skip("set DNFBRIDGE_REAL_PVF_SMOKE to verify the live summer package")
	}
	archive, err := platformpvf.LoadArchive(platformpvf.Options{Path: pvfPath})
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := newPVFDungeonDropCatalog(archive)
	if err != nil {
		t.Fatal(err)
	}
	definition, err := resolveCurrentCeraPackageDefinition(catalog, 490702507)
	if err != nil {
		t.Fatalf("the live 欢乐暑假行程单 (490702507) must resolve as a cera package: %v", err)
	}
	if definition.Source.ItemID != 490702507 {
		t.Fatalf("source=%+v", definition.Source)
	}
	if len(definition.Rewards) != 1 || definition.Rewards[0].Definition.ItemID != 490702504 || definition.Rewards[0].Count != 1 {
		t.Fatalf("rewards=%+v", definition.Rewards)
	}
	if len(definition.AvatarItemIDs) != 0 {
		t.Fatalf("the summer itinerary is expected to have no avatar choices, got %v", definition.AvatarItemIDs)
	}
	t.Logf("source path=%s reward path=%s kind=%s",
		definition.Source.PVFPath, definition.Rewards[0].Definition.PVFPath, definition.Rewards[0].Definition.Kind)
}
