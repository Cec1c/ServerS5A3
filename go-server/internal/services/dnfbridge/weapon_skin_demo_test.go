package dnfbridge

import (
	"bytes"
	"context"
	"encoding/binary"
	"os"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/dnfenum"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	dnfrepomemory "longheng.io/server/internal/modules/dnf/repository/memory"
	"longheng.io/server/internal/modules/dnf/weaponskin"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

func TestWeaponSkinDemoDecode(t *testing.T) {
	frame := make([]byte, 20)
	copy(frame, "WSQ1")
	binary.LittleEndian.PutUint32(frame[4:8], 15)
	binary.LittleEndian.PutUint32(frame[8:12], 16)
	frame[12] = byte(weaponskin.Apply)
	binary.LittleEndian.PutUint32(frame[16:20], 17)
	if q, ok := decodeWeaponSkinDemoRequest(frame); !ok || q.requestID != 15 || q.epoch != 16 || q.revision != 17 || q.action != weaponskin.Apply {
		t.Fatalf("请求解析异常: %+v / %t", q, ok)
	}
	for _, index := range []int{0, 12, 13, 14, 15} {
		wrong := append([]byte(nil), frame...)
		wrong[index] = 99
		if _, ok := decodeWeaponSkinDemoRequest(wrong); ok {
			t.Fatalf("接受错误的第 %d 字节", index)
		}
	}
}

func TestWeaponSkinDemoRealPVFSceneProjection(t *testing.T) {
	filename := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if filename == "" {
		t.Skip("需要现用别云剑样板 PVF")
	}
	archive, err := platformpvf.LoadArchive(platformpvf.Options{Path: filename})
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := newPVFDungeonDropCatalog(archive)
	if err != nil || !weaponSkinDemoAssetReady(catalog) {
		t.Fatalf("样板PVF未载入: %v", err)
	}
	ctx := context.Background()
	repos := dnfrepomemory.NewMemoryGroup()
	character := dnfrepo.CharacterRecord{CharacterID: "19", AccountID: "dnf:1", Name: "Actor19", Job: "0", Level: 90}
	if err := repos.Character.Save(ctx, character); err != nil {
		t.Fatal(err)
	}
	if err := repos.Inventory.Save(ctx, dnfrepo.InventoryRecord{CharacterID: "19", Slots: map[string]dnfrepo.ItemStack{
		"1:7": {ItemID: weaponskin.SampleID, Count: 1},
	}}); err != nil {
		t.Fatal(err)
	}
	const equippedWeapon = 101030517
	equipment := dnfrepo.EquipmentRecord{CharacterID: "19", Entries: map[string]dnfrepo.EquipmentEntry{
		"12": {SlotIndex: 12, ItemID: equippedWeapon},
	}}
	if err := repos.Equipment.Save(ctx, equipment); err != nil {
		t.Fatal(err)
	}
	// 此夹具只验证外观。独立的读条测试负责技能页及技能仓初始化。
	sceneRepos := repos
	sceneRepos.Skill = nil
	service := &Service{
		options:            options{currentClientVersion: "official-24.9"},
		repositoryProvider: func() (dnfrepo.Group, bool) { return sceneRepos, true },
		pvfItemCatalog:     catalog,
	}
	newSession := func() *gameSession { return &gameSession{selectedCharacterID: 19, accountID: character.AccountID} }
	assertBoth := func(avatar, weapon uint32, warehouse bool) {
		t.Helper()
		for _, owner := range []byte{1, 0, 2} {
			body := service.buildCurrentSceneObjectListBodyForSessionInContext(ctx, newSession(), 19, character.Name, character, true, owner)
			rows := peerAppearanceRowsForTest(t, body)
			if body[4] != owner || len(rows) != currentActorMode0AppearanceSlotCount ||
				currentWeaponSkinDemoBodyHasSample(body) != warehouse ||
				rows[weaponSkinDemoAvatarSlot] != (peerAppearanceRowForTest{display: avatar}) ||
				rows[currentActorMode0WeaponSlot] != (peerAppearanceRowForTest{display: weapon}) {
				t.Fatalf("owner=%d 实际外观记录 %+v", owner, rows)
			}
		}
	}
	assertBoth(currentActorMode0AppearanceEmptyItem, equippedWeapon, false)
	cmd := weaponskin.Command{CharacterID: "19", AccountID: "dnf:1", Action: weaponskin.Apply, Now: time.Unix(42, 0), Current: func() bool { return true }}
	if _, err := weaponskin.Update(ctx, repos.CharacterAssets, cmd); err != nil {
		t.Fatal(err)
	}
	assertBoth(weaponskin.SampleID, equippedWeapon, true)
	roster, ok, err := repos.Character.Load(ctx, character.CharacterID)
	if err != nil || !ok {
		t.Fatalf("读取仓库选择: found=%v err=%v", ok, err)
	}
	roster.Roster.Entry.EquipSummary = equipmentRosterSummary(equipment)
	preview := service.buildCurrentRosterBody([]dnfrepo.CharacterRecord{roster})
	if rows := roster.Roster.Entry.EquipSummary; len(rows) != 1 || rows[0].Slot != currentActorMode0WeaponSlot || rows[0].ItemIDOrIcon != equippedWeapon {
		t.Fatalf("选择界面修改了源外观快照: %+v", rows)
	}
	equipment.Entries["10"] = dnfrepo.EquipmentEntry{SlotIndex: 10, ItemID: weaponskin.SampleID}
	wornPreview := roster
	wornPreview.Stats = nil // 单独构造实体穿戴形成的选择界面。
	wornPreview.Roster.Entry.EquipSummary = equipmentRosterSummary(equipment)
	delete(equipment.Entries, "10")
	if !bytes.Equal(preview, service.buildCurrentRosterBody([]dnfrepo.CharacterRecord{wornPreview})) {
		t.Fatal("仓库别云剑的选择界面报文应与实体装扮一致")
	}
	// 完成读条后，本人最后收到恢复外观的 mode0，真实武器行不变。
	local := newSession()
	connection := &bufferConn{}
	local.conn = connection
	local.postStartMapPlayerStateSent = true
	if err := service.sendCurrentPostFinishLoadingPlayerState(local, "样板副本读条完成"); err != nil {
		t.Fatal(err)
	}
	packets := connection.write.Bytes()
	var foundRestore bool
	for len(packets) != 0 {
		packet, rest := splitGameServerUpperPacket(t, packets)
		if packet.Header.Classification == 0 && packet.Header.MsgID == uint16(dnfenum.CmdPacketSetUDPIPPort) {
			if foundRestore || len(rest) != 0 || !currentWeaponSkinDemoBodyHasSample(packet.Body) || packet.Body[4] != currentSceneObjectContext {
				t.Fatalf("读条恢复外观帧或发送顺序异常，剩余字节数 %d", len(rest))
			}
			rows := peerAppearanceRowsForTest(t, packet.Body)
			if rows[currentActorMode0WeaponSlot].display != equippedWeapon {
				t.Fatalf("读条恢复外观更改了真实武器行: %+v", rows)
			}
			foundRestore = true
		}
		packets = rest
	}
	if !foundRestore {
		t.Fatal("读条完成后缺少别云剑外观补发")
	}
	sent := connection.write.Len()
	if err := service.sendCurrentPostFinishLoadingPlayerState(local, "重复读条请求"); err != nil || connection.write.Len() != sent {
		t.Fatalf("重复读条请求不应重发外观: %v", err)
	}
	const wornAvatar = 101600023
	equipment.Entries["10"] = dnfrepo.EquipmentEntry{SlotIndex: 10, ItemID: wornAvatar}
	if err := repos.Equipment.Save(ctx, equipment); err != nil {
		t.Fatal(err)
	}
	assertBoth(weaponskin.SampleID, equippedWeapon, true)
	roster.Roster.Entry.EquipSummary = equipmentRosterSummary(equipment)
	if !bytes.Equal(preview, service.buildCurrentRosterBody([]dnfrepo.CharacterRecord{roster})) {
		t.Fatal("实穿武器装扮与原武器不应盖住选择角色界面的仓库外观")
	}
	delete(equipment.Entries, "10")
	// 装B王实机反馈时穿戴的光剑。
	const lightSword = 101040332
	equipment.Entries["12"] = dnfrepo.EquipmentEntry{SlotIndex: 12, ItemID: lightSword}
	if err := repos.Equipment.Save(ctx, equipment); err != nil {
		t.Fatal(err)
	}
	assertBoth(weaponskin.SampleID, lightSword, true)
	delete(equipment.Entries, "12")
	if err := repos.Equipment.Save(ctx, equipment); err != nil {
		t.Fatal(err)
	}
	assertBoth(weaponskin.SampleID, currentActorMode0AppearanceEmptyItem, true)
	equipment.Entries["10"] = dnfrepo.EquipmentEntry{SlotIndex: 10, ItemID: weaponskin.SampleID}
	if err := repos.Equipment.Save(ctx, equipment); err != nil {
		t.Fatal(err)
	}
	assertBoth(weaponskin.SampleID, currentActorMode0AppearanceEmptyItem, true)
	delete(equipment.Entries, "10")
	equipment.Entries["12"] = dnfrepo.EquipmentEntry{SlotIndex: 12, ItemID: equippedWeapon}
	if err := repos.Equipment.Save(ctx, equipment); err != nil {
		t.Fatal(err)
	}
	assertBoth(weaponskin.SampleID, equippedWeapon, true)
	equipment.Entries["10"] = dnfrepo.EquipmentEntry{SlotIndex: 10, ItemID: wornAvatar}
	if err := repos.Equipment.Save(ctx, equipment); err != nil {
		t.Fatal(err)
	}
	cmd.Action, cmd.Revision = weaponskin.Clear, 1
	if _, err := weaponskin.Update(ctx, repos.CharacterAssets, cmd); err != nil {
		t.Fatal(err)
	}
	assertBoth(wornAvatar, equippedWeapon, false)
	cleared, ok, err := repos.Character.Load(ctx, character.CharacterID)
	if err != nil || !ok {
		t.Fatalf("读取取消外观后的角色: found=%v err=%v", ok, err)
	}
	cleared.Roster.Entry.EquipSummary = equipmentRosterSummary(equipment)
	if got := service.weaponSkinDemoRosterAppearance(cleared); len(got) != 1 || got[0].ItemIDOrIcon != wornAvatar {
		t.Fatalf("取消后应恢复选择界面的实穿武器装扮: %+v", got)
	}
	got, _, _ := repos.Equipment.Load(ctx, "19")
	if len(got.Entries) != 2 || got.Entries["12"].ItemID != equippedWeapon || got.Entries["10"].ItemID != wornAvatar {
		t.Fatalf("穿戴仓发生额外修改: %+v", got.Entries)
	}
	// 同一件别云剑真正穿戴时，外观包仍携带道具编号，但无仓库标记。
	equipment.Entries["10"] = dnfrepo.EquipmentEntry{SlotIndex: 10, ItemID: weaponskin.SampleID}
	if err := repos.Equipment.Save(ctx, equipment); err != nil {
		t.Fatal(err)
	}
	assertBoth(weaponskin.SampleID, equippedWeapon, false)
	delete(equipment.Entries, "10")
	if err := repos.Equipment.Save(ctx, equipment); err != nil {
		t.Fatal(err)
	}
	assertBoth(currentActorMode0AppearanceEmptyItem, equippedWeapon, false)
}
