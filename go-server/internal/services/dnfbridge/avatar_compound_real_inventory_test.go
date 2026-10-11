package dnfbridge

import (
	"bytes"
	"context"
	"os"
	"testing"

	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	dnfrepomemory "longheng.io/server/internal/modules/dnf/repository/memory"

	platformpvf "longheng.io/server/internal/platform/pvf"
)

// 本文件锁死"装扮合成器点了之后提示材料不足"的真实根因与修复：
//
// 现场抓包（HK /opt/dnf90/logs/packet_log.txt，conn_id=game-000004 channel_id=121）：
//
//	RECV cmd=1 type=99(ENUM_CMDPACKET_COMPOUND_AVATAR) body_len=22
//	     hex=0163 0023 000000e2 5798ec34 00 5200 0e00 162ec418 1200 162ec418 fe2dc418 0000 0000
//	     → consume=82 slot1=14 item1=415510038 slot2=18 item2=415510038 requested=415510014 ability=0
//	EVENT kind=game-avatar-compound-failed reason=compound_config_unavailable
//	     error=dnf current avatar compound request is invalid:
//	           etc/chn_compound/compoundavatar_atpriest.etc has no [<part> avatar] pool
//
// 该请求本身完全合法（角色 3 test2，job=14 `[at priest]`，主背包 0:82 = 装扮合成器 21 ×5，
// 装扮栏 1:14 / 1:18 = 415510038 `[pants avatar]`，目标外观 415510014 在经典表的
// `[pants avatar]` 池里）。失败是因为运行时 PVF 的
// etc/chn_compound/compoundavatar_atpriest.etc 只有 32 字节 `[character type]`，
// 旧实现把它当整份配置失败，于是任何一次合法合成都回 `00 16 00`。
//
// 现场道具/PVF 证据（只读查库 + 只读读 PVF）：
//
//	dnf_inventory_items: 3|slots|0:82|21|5, 3|slots|1:14|415510038|1, 3|slots|1:18|415510038|1
//	equipment/character/priest/at_avatar/pants/chn_equ_special_avatar_at_priest_pants_415510038.equ
//	     [equipment type] `[pants avatar]`  (兽人英雄金色信仰腿甲)
//	equipment/character/priest/at_avatar/pants/chn_equ_upper_avatar_at_priest_pants_415510014.equ
//	     [equipment type] `[pants avatar]`  (校园新生黑色蛋糕裙)
//	stackable/cash/cube_bind.stk  [name] 装扮合成器  item 21
//	stackable/cash/chn_20140506_memory_bind_cube/memory_cube_bind_2683630.stk [name] 追忆之装扮合成器

// avatarCompoundStubVariantText 是运行时 PVF 里
// etc/chn_compound/compoundavatar_atpriest.etc 的逐字节形状（32 字节：
// 换行 + `[character type]` + 换行 + “ `[at priest]` “ + 空格 + 换行）。
// job=13/15 的同类空壳分别是 37/33 字节，只有 `[character type]`。
const avatarCompoundStubVariantText = "\n[character type]\n`[swordman]` \n"

// mustAvatarCompoundLiveAssets 用给定职业与槽位构造内存资产 + PVF 目录。
func mustAvatarCompoundLiveAssets(
	t *testing.T,
	source dnfpvf.Source,
	job string,
	slots map[string]dnfrepo.ItemStack,
) (dnfrepo.Group, *pvfDungeonDropCatalog) {
	t.Helper()
	catalog, err := newPVFDungeonDropCatalog(source)
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
		Job:         job,
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

// TestCurrentAvatarCompoundSkipsStubVariantInsteadOfFailingWholeConfig 是根因回归：
// 经典表可用时，只有 `[character type]` 的 chn_compound 空壳不能拖垮整份配置。
func TestCurrentAvatarCompoundSkipsStubVariantInsteadOfFailingWholeConfig(t *testing.T) {
	source := currentAvatarCompoundTestPVF()
	source[currentAvatarCompoundCHNRoot+"/compoundavatar_swordman.etc"] = avatarCompoundStubVariantText
	catalog, err := newPVFDungeonDropCatalog(source)
	if err != nil {
		t.Fatal(err)
	}
	config, err := loadCurrentAvatarCompoundConfig(catalog.source, 0)
	if err != nil {
		t.Fatalf("stub CN variant must not fail the whole config: %v", err)
	}
	if len(config.Variants) != 1 || config.Variants[0].Path != currentAvatarCompoundTestConfigPath {
		t.Fatalf("variants = %+v", config.usablePaths())
	}
	if len(config.Skipped) != 1 ||
		config.Skipped[0].Path != currentAvatarCompoundCHNRoot+"/compoundavatar_swordman.etc" {
		t.Fatalf("skipped = %+v", config.Skipped)
	}
	// 空壳被跳过之后，现场形状的请求必须真正合成成功，而不是回 00 16 00。
	withCurrentAvatarCompoundRoll(t, func(limit int) int { return limit - 1 })
	repositories, catalog := mustAvatarCompoundLiveAssets(t, source, "0", currentAvatarCompoundTestSlots())
	connection := &bufferConn{}
	service := currentAvatarCompoundTestService(repositories, catalog)
	body := currentAvatarCompoundTestBody(9, 5, 41001, 6, 41002, 41003, 4)
	if err := service.handleCurrentAvatarCompound(&gameSession{conn: connection, selectedCharacterID: 19}, body); err != nil {
		t.Fatal(err)
	}
	ack, _ := splitGameServerUpperPacket(t, connection.write.Bytes())
	request, err := parseCurrentAvatarCompoundRequest(body)
	if err != nil {
		t.Fatal(err)
	}
	want := buildCurrentAvatarCompoundSuccessBody(request, []currentAvatarCompoundResultRow{
		{Slot: 5, ItemID: 41003, AbilityNo: 4},
	})
	if !bytes.Equal(ack.Body, want) {
		t.Fatalf("op99 ACK=%x want=%x (failure body=%x)", ack.Body, want, buildCurrentAvatarCompoundFailureBody())
	}
	inventory, _, _ := repositories.Inventory.Load(context.Background(), "19")
	if granted, exists := inventory.Slots["1:5"]; !exists || granted.ItemID != 41003 {
		t.Fatalf("granted = %+v exists=%t", granted, exists)
	}
}

// TestCurrentAvatarCompoundFailsWhenEveryVariantIsUnusable 保证"跳过坏变体"不会
// 退化成"什么都接受"：一份可用配置都没有时仍然必须整体失败。
func TestCurrentAvatarCompoundFailsWhenEveryVariantIsUnusable(t *testing.T) {
	source := currentAvatarCompoundTestPVF()
	source[currentAvatarCompoundTestConfigPath] = avatarCompoundStubVariantText
	source[currentAvatarCompoundCHNRoot+"/compoundavatar_swordman.etc"] = avatarCompoundStubVariantText
	catalog, err := newPVFDungeonDropCatalog(source)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := loadCurrentAvatarCompoundConfig(catalog.source, 0); err == nil {
		t.Fatal("a job with no usable variant must fail the config load")
	}
}

// TestCurrentAvatarCompoundRejectsWithCorrectBranch 保证每个拒绝走自己的分支：
// 服务端配置问题不会伪装成"合成材料不足"，材料/合成器/外观各有各的分支名。
func TestCurrentAvatarCompoundRejectsWithCorrectBranch(t *testing.T) {
	type testCase struct {
		slots map[string]dnfrepo.ItemStack
		body  []byte
		want  string
	}
	cases := map[string]testCase{
		"material slot 1 empty": {
			slots: map[string]dnfrepo.ItemStack{
				"1:6": {ItemID: 41002, Count: 1},
				"0:9": {ItemID: 21, Count: 1},
			},
			body: currentAvatarCompoundTestBody(9, 5, 41001, 6, 41002, 41003, 4),
			want: currentAvatarCompoundBranchMaterialSlotInvalid,
		},
		"material item mismatch": {
			slots: currentAvatarCompoundTestSlots(),
			body:  currentAvatarCompoundTestBody(9, 5, 41002, 6, 41002, 41003, 4),
			want:  currentAvatarCompoundBranchMaterialItemMismatch,
		},
		"material is not an avatar": {
			slots: map[string]dnfrepo.ItemStack{
				"1:5": {ItemID: 21, Count: 1},
				"1:6": {ItemID: 41002, Count: 1},
				"0:9": {ItemID: 21, Count: 1},
			},
			body: currentAvatarCompoundTestBody(9, 5, 21, 6, 41002, 41003, 4),
			want: currentAvatarCompoundBranchMaterialNotAvatar,
		},
		"materials are different parts": {
			slots: map[string]dnfrepo.ItemStack{
				"1:5": {ItemID: 41001, Count: 1},
				"1:6": {ItemID: 42001, Count: 1},
				"0:9": {ItemID: 21, Count: 1},
			},
			body: currentAvatarCompoundTestBody(9, 5, 41001, 6, 42001, 41003, 4),
			want: currentAvatarCompoundBranchMaterialPartMismatch,
		},
		"compounder slot empty": {
			slots: map[string]dnfrepo.ItemStack{
				"1:5": {ItemID: 41001, Count: 1},
				"1:6": {ItemID: 41002, Count: 1},
			},
			body: currentAvatarCompoundTestBody(9, 5, 41001, 6, 41002, 41003, 4),
			want: currentAvatarCompoundBranchCompounderMissing,
		},
		"compounder is not a consumable": {
			slots: map[string]dnfrepo.ItemStack{
				"1:5": {ItemID: 41001, Count: 1},
				"1:6": {ItemID: 41002, Count: 1},
				"0:9": {ItemID: 41001, Count: 1},
			},
			body: currentAvatarCompoundTestBody(9, 5, 41001, 6, 41002, 41003, 4),
			want: currentAvatarCompoundBranchCompounderNotUsable,
		},
		"compounder is not declared by the PVF": {
			slots: map[string]dnfrepo.ItemStack{
				"1:5": {ItemID: 41001, Count: 1},
				"1:6": {ItemID: 41002, Count: 1},
				"0:9": {ItemID: 3037, Count: 3},
			},
			body: currentAvatarCompoundTestBody(9, 5, 41001, 6, 41002, 41003, 4),
			want: currentAvatarCompoundBranchCompounderUndeclared,
		},
		"requested item outside the pool": {
			slots: currentAvatarCompoundTestSlots(),
			body:  currentAvatarCompoundTestBody(9, 5, 41001, 6, 41002, 41004, 4),
			want:  currentAvatarCompoundBranchRequestedOutsidePool,
		},
		"requested item is another part": {
			slots: currentAvatarCompoundTestSlots(),
			body:  currentAvatarCompoundTestBody(9, 5, 41001, 6, 41002, 42001, 4),
			want:  currentAvatarCompoundBranchRequestedPartMismatch,
		},
	}
	for name, testCase := range cases {
		t.Run(name, func(t *testing.T) {
			withCurrentAvatarCompoundRoll(t, func(limit int) int { return limit - 1 })
			repositories, catalog := mustAvatarCompoundLiveAssets(t, currentAvatarCompoundTestPVF(), "0", testCase.slots)
			service := currentAvatarCompoundTestService(repositories, catalog)
			session := &gameSession{conn: &bufferConn{}, selectedCharacterID: 19}
			ctx := context.Background()
			request, err := parseCurrentAvatarCompoundRequest(testCase.body)
			if err != nil {
				t.Fatal(err)
			}
			config, err := loadCurrentAvatarCompoundConfig(catalog.source, 0)
			if err != nil {
				t.Fatal(err)
			}
			_, err = service.prepareCurrentAvatarCompound(ctx, session, catalog, config, request)
			if err == nil {
				t.Fatal("prepare accepted an invalid request")
			}
			if branch := currentAvatarCompoundRejectionBranch(err); branch != testCase.want {
				t.Fatalf("branch = %q want %q (err=%v)", branch, testCase.want, err)
			}
			// 同一分支下客户端仍然是"回包 + 零消耗"。
			connection := &bufferConn{}
			session = &gameSession{conn: connection, selectedCharacterID: 19}
			if err := service.handleCurrentAvatarCompound(session, testCase.body); err != nil {
				t.Fatal(err)
			}
			ack, trailing := splitGameServerUpperPacket(t, connection.write.Bytes())
			if len(trailing) != 0 || !bytes.Equal(ack.Body, buildCurrentAvatarCompoundFailureBody()) {
				t.Fatalf("failure ACK=%x trailing=%x", ack.Body, trailing)
			}
			inventory, _, _ := repositories.Inventory.Load(ctx, "19")
			for key, want := range testCase.slots {
				got, exists := inventory.Slots[key]
				if !exists || got.ItemID != want.ItemID || got.Count != want.Count {
					t.Fatalf("slot %s = %+v exists=%t want %+v", key, got, exists, want)
				}
			}
			if len(inventory.Slots) != len(testCase.slots) {
				t.Fatalf("inventory changed on rejection: %+v", inventory.Slots)
			}
		})
	}
}

// realAvatarCompoundPVFAssets 是真实运行时 PVF 的只读句柄。
type realAvatarCompoundPVFAssets struct {
	source  dnfpvf.Source
	catalog *pvfDungeonDropCatalog
}

// realAvatarCompoundPVF 打开真实运行时 Script.pvf；未设置环境变量时跳过。
func realAvatarCompoundPVF(t *testing.T) realAvatarCompoundPVFAssets {
	t.Helper()
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
	return realAvatarCompoundPVFAssets{source: archive, catalog: catalog}
}

// TestRealPVFCurrentAvatarCompoundJob14SkipsStubCNVariant 钉死根因的真实 PVF 形态：
// job=14 `[at priest]` 经典表可用，chn_compound 变体是 32 字节空壳（被跳过）。
func TestRealPVFCurrentAvatarCompoundJob14SkipsStubCNVariant(t *testing.T) {
	real := realAvatarCompoundPVF(t)
	catalog := real.catalog
	config, err := loadCurrentAvatarCompoundConfig(catalog.source, 14)
	if err != nil {
		t.Fatalf("job=14 at priest must load its classic compoundavatar table: %v", err)
	}
	if len(config.Variants) != 1 || config.Variants[0].Path != "etc/compoundavatar_atpriest.etc" {
		t.Fatalf("job=14 variants = %+v", config.usablePaths())
	}
	if len(config.Skipped) != 1 || config.Skipped[0].Path != currentAvatarCompoundCHNRoot+"/compoundavatar_atpriest.etc" {
		t.Fatalf("job=14 skipped = %+v", config.Skipped)
	}
	if len(config.Variants[0].Parts) != 8 {
		t.Fatalf("job=14 parts = %d", len(config.Variants[0].Parts))
	}
	for _, material := range []uint32{21, 899, 490702265} {
		if !config.acceptsMaterial(material) {
			t.Fatalf("job=14 materials %v missing %d", config.declaredMaterialIDs(), material)
		}
	}
	// 空壳是"该职业没有这套池"的标记，不是损坏数据：另外两个职业同样如此。
	for job, size := range map[byte]string{13: "demonic_lancer", 15: "gun_blader"} {
		other, err := loadCurrentAvatarCompoundConfig(catalog.source, job)
		if err != nil {
			t.Fatalf("job=%d %s must still load: %v", job, size, err)
		}
		if len(other.Skipped) != 1 {
			t.Fatalf("job=%d skipped = %+v", job, other.Skipped)
		}
	}
}

// TestRealPVFCurrentAvatarCompoundAtPriestLiveRequestReplay 用现场角色的真实道具重放
// 抓到的 op99 请求：必须是成功回包 + 正确消耗/产出，而不是 `00 16 00`。
func TestRealPVFCurrentAvatarCompoundAtPriestLiveRequestReplay(t *testing.T) {
	real := realAvatarCompoundPVF(t)
	// 只钉不命中额外稀有装扮：11.12% 的命中率不参与本次判定。
	withCurrentAvatarCompoundRoll(t, func(limit int) int { return limit - 1 })
	const (
		consumeSlot = int16(82)
		slot1       = int16(14)
		slot2       = int16(18)
		pantsItem   = uint32(415510038)
		targetItem  = uint32(415510014)
	)
	slots := map[string]dnfrepo.ItemStack{
		"0:82": {ItemID: 21, Count: 5},
		"1:14": {ItemID: int64(pantsItem), Count: 1},
		"1:18": {ItemID: int64(pantsItem), Count: 1},
	}
	repositories, catalog := mustAvatarCompoundLiveAssets(t, real.source, "14", slots)
	connection := &bufferConn{}
	service := currentAvatarCompoundTestService(repositories, catalog)
	body := currentAvatarCompoundTestBody(consumeSlot, slot1, pantsItem, slot2, pantsItem, targetItem, 0)
	if err := service.handleCurrentAvatarCompound(&gameSession{conn: connection, selectedCharacterID: 19}, body); err != nil {
		t.Fatal(err)
	}
	ack, _ := splitGameServerUpperPacket(t, connection.write.Bytes())
	if bytes.Equal(ack.Body, buildCurrentAvatarCompoundFailureBody()) {
		t.Fatalf("live request was rejected with the generic failure body %x", ack.Body)
	}
	request, err := parseCurrentAvatarCompoundRequest(body)
	if err != nil {
		t.Fatal(err)
	}
	want := buildCurrentAvatarCompoundSuccessBody(request, []currentAvatarCompoundResultRow{
		{Slot: slot1, ItemID: targetItem, AbilityNo: 0},
	})
	if !bytes.Equal(ack.Body, want) {
		t.Fatalf("op99 ACK=%x want=%x", ack.Body, want)
	}
	inventory, _, _ := repositories.Inventory.Load(context.Background(), "19")
	if _, exists := inventory.Slots["1:18"]; exists {
		t.Fatalf("second material survived: %+v", inventory.Slots)
	}
	granted, exists := inventory.Slots["1:14"]
	if !exists || granted.ItemID != int64(targetItem) || granted.Count != 1 ||
		granted.Extra["source"] != "avatar_compound" {
		t.Fatalf("granted = %+v exists=%t", granted, exists)
	}
	cube, exists := inventory.Slots["0:82"]
	if !exists || cube.ItemID != 21 || cube.Count != 4 {
		t.Fatalf("compounder stack = %+v exists=%t", cube, exists)
	}
}

// TestRealPVFCurrentAvatarCompoundCNVariantWithRealMaterials 覆盖 CN 变体路径：
// 真实 chn_compound 池（job=0）用真实合成器 2683630 与 21 各成功一次。
//
// 目标外观取 39254「混沌鬼影噬魂头饰」：它只出现在
// etc/chn_compound/compoundavatar_swordman.etc 的 `[hat avatar]` 池里
// （经典池没有的 CN-only 项是 101550551/101550535/101550536/39254/39255），
// 因此变体选择必然落在 chn_compound 那份上。
func TestRealPVFCurrentAvatarCompoundCNVariantWithRealMaterials(t *testing.T) {
	real := realAvatarCompoundPVF(t)
	catalog := real.catalog
	config, err := loadCurrentAvatarCompoundConfig(catalog.source, 0)
	if err != nil {
		t.Fatal(err)
	}
	cnVariant, part, found := config.variantForItem(39254)
	if !found || part != "hat" || cnVariant.Path != currentAvatarCompoundCHNRoot+"/compoundavatar_swordman.etc" {
		t.Fatalf("39254 variant=%s part=%s found=%t", cnVariant.Path, part, found)
	}
	if !config.acceptsMaterial(2683630) {
		t.Fatalf("cn material 2683630 missing: %v", config.declaredMaterialIDs())
	}
	// 角色 2 `菲八番茄`（job=0）装扮栏 1:1 与 1:33 都是真实存在的
	// chn_equ_special_avatar_swordman_cap_101550701（[hat avatar]），
	// 这里就用这两件真实同部位材料。
	const (
		hatItem    = uint32(101550701)
		targetItem = uint32(39254)
	)
	for name, cube := range map[string]uint32{"memory compounder 2683630": 2683630, "classic compounder 21": 21} {
		t.Run(name, func(t *testing.T) {
			withCurrentAvatarCompoundRoll(t, func(limit int) int { return limit - 1 })
			slots := map[string]dnfrepo.ItemStack{
				"0:10": {ItemID: int64(cube), Count: 3},
				"1:1":  {ItemID: int64(hatItem), Count: 1},
				"1:33": {ItemID: int64(hatItem), Count: 1},
			}
			repositories, liveCatalog := mustAvatarCompoundLiveAssets(t, real.source, "0", slots)
			connection := &bufferConn{}
			service := currentAvatarCompoundTestService(repositories, liveCatalog)
			body := currentAvatarCompoundTestBody(10, 1, hatItem, 33, hatItem, targetItem, 3)
			if err := service.handleCurrentAvatarCompound(&gameSession{conn: connection, selectedCharacterID: 19}, body); err != nil {
				t.Fatal(err)
			}
			ack, _ := splitGameServerUpperPacket(t, connection.write.Bytes())
			if bytes.Equal(ack.Body, buildCurrentAvatarCompoundFailureBody()) {
				t.Fatalf("CN variant compound with %d was rejected: %x", cube, ack.Body)
			}
			inventory, _, _ := repositories.Inventory.Load(context.Background(), "19")
			granted, exists := inventory.Slots["1:1"]
			if !exists || granted.ItemID != int64(targetItem) {
				t.Fatalf("granted = %+v exists=%t", granted, exists)
			}
			if granted.Extra["ext_data0"] != "3" {
				t.Fatalf("ability was not persisted: %+v", granted.Extra)
			}
			remaining, exists := inventory.Slots["0:10"]
			if !exists || remaining.Count != 2 {
				t.Fatalf("compounder stack = %+v exists=%t", remaining, exists)
			}
		})
	}
}

// TestRealPVFCurrentAvatarCompoundShortageConsumesNothing 用真实 PVF 覆盖
// "材料真不足"：材料槽为空时按材料分支拒绝，且一件都不消耗。
func TestRealPVFCurrentAvatarCompoundShortageConsumesNothing(t *testing.T) {
	real := realAvatarCompoundPVF(t)
	withCurrentAvatarCompoundRoll(t, func(limit int) int { return limit - 1 })
	const (
		pantsItem  = uint32(415510038)
		targetItem = uint32(415510014)
	)
	// 现场真实持有：主背包 21、装扮栏 1:14 = 415510038；第二件材料不存在。
	slots := map[string]dnfrepo.ItemStack{
		"0:82": {ItemID: 21, Count: 5},
		"1:14": {ItemID: int64(pantsItem), Count: 1},
	}
	repositories, liveCatalog := mustAvatarCompoundLiveAssets(t, real.source, "14", slots)
	service := currentAvatarCompoundTestService(repositories, liveCatalog)
	body := currentAvatarCompoundTestBody(82, 14, pantsItem, 18, pantsItem, targetItem, 0)
	request, err := parseCurrentAvatarCompoundRequest(body)
	if err != nil {
		t.Fatal(err)
	}
	config, err := loadCurrentAvatarCompoundConfig(liveCatalog.source, 14)
	if err != nil {
		t.Fatal(err)
	}
	_, err = service.prepareCurrentAvatarCompound(context.Background(),
		&gameSession{conn: &bufferConn{}, selectedCharacterID: 19}, liveCatalog, config, request)
	if err == nil {
		t.Fatal("prepare accepted a missing second material")
	}
	if branch := currentAvatarCompoundRejectionBranch(err); branch != currentAvatarCompoundBranchMaterialSlotInvalid {
		t.Fatalf("branch = %q err = %v", branch, err)
	}
	connection := &bufferConn{}
	if err := service.handleCurrentAvatarCompound(
		&gameSession{conn: connection, selectedCharacterID: 19}, body); err != nil {
		t.Fatal(err)
	}
	ack, _ := splitGameServerUpperPacket(t, connection.write.Bytes())
	if !bytes.Equal(ack.Body, buildCurrentAvatarCompoundFailureBody()) {
		t.Fatalf("failure ACK=%x", ack.Body)
	}
	inventory, _, _ := repositories.Inventory.Load(context.Background(), "19")
	if len(inventory.Slots) != len(slots) {
		t.Fatalf("failed compound consumed something: %+v", inventory.Slots)
	}
	if stack, exists := inventory.Slots["0:82"]; !exists || stack.Count != 5 {
		t.Fatalf("compounder stack = %+v exists=%t", stack, exists)
	}
}
