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

const currentAvatarCompoundTestConfigPath = "etc/compoundavatar_swordman.etc"

// currentAvatarCompoundTestPVF 是照着运行时 etc/compoundavatar_<job>.etc 的真实形状造的
// 最小 PVF：段首 N 条是额外稀有装扮池，其余是可选高级外观池，[material] 声明合成器本体。
func currentAvatarCompoundTestPVF() bridgePVFSource {
	return bridgePVFSource{
		"monster/monster.lst":             "",
		"stackable/stackable.lst":         "21 `cube_bind.stk`\n3037 `cube.stk`\n",
		"equipment/equipment.lst":         "41001 `avatar_hat_a.equ`\n41002 `avatar_hat_b.equ`\n41003 `avatar_hat_target.equ`\n41004 `avatar_hat_other.equ`\n41101 `avatar_hat_bonus.equ`\n42001 `avatar_coat_a.equ`\n",
		"stackable/cube_bind.stk":         "[stackable type] `[etc]`\n[stack limit] 999\n",
		"stackable/cube.stk":              "[stackable type] `[material]`\n[stack limit] 999\n",
		"equipment/avatar_hat_a.equ":      "[equipment type] `[hat avatar]`\n[grade] 1\n[rarity] 2\n",
		"equipment/avatar_hat_b.equ":      "[equipment type] `[hat avatar]`\n[grade] 1\n[rarity] 2\n",
		"equipment/avatar_hat_target.equ": "[equipment type] `[hat avatar]`\n[grade] 2\n[rarity] 3\n",
		"equipment/avatar_hat_other.equ":  "[equipment type] `[hat avatar]`\n[grade] 2\n[rarity] 3\n",
		"equipment/avatar_hat_bonus.equ":  "[equipment type] `[hat avatar]`\n[grade] 3\n[rarity] 4\n",
		"equipment/avatar_coat_a.equ":     "[equipment type] `[coat avatar]`\n[grade] 1\n[rarity] 2\n",
		"etc/compoundavatar.etc":          "1 `CompoundAvatar_swordman.etc`\n",
		currentAvatarCompoundTestConfigPath: "" +
			"[character type]\n`[swordman]`\n" +
			"[grade]\n1\n" +
			"[rare rate]\n`hat` 950 `coat` 950\n[/rare rate]\n" +
			"[upper grade]\n2\n" +
			"[upper rare rate]\n`hat` 325 `coat` 325\n[/upper rare rate]\n" +
			"[hat avatar]\n1 41101 300 41001 1000 41002 1000 41003 1000\n[/hat avatar]\n" +
			"[coat avatar]\n1 41101 300 42001 1000\n[/coat avatar]\n" +
			"[material]\n1 21 1\n[/material]\n",
	}
}

func mustCurrentAvatarCompoundTestAssets(t *testing.T, slots map[string]dnfrepo.ItemStack) (dnfrepo.Group, *pvfDungeonDropCatalog) {
	t.Helper()
	catalog, err := newPVFDungeonDropCatalog(currentAvatarCompoundTestPVF())
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

func currentAvatarCompoundTestService(repositories dnfrepo.Group, catalog *pvfDungeonDropCatalog) *Service {
	return &Service{
		options:            options{accountID: "account-1", gameUpperHeader: gameUpperHeaderChannel13, gameUpperBodyCodec: gameUpperBodyCodecPlain},
		pvfItemCatalog:     catalog,
		repositoryProvider: func() (dnfrepo.Group, bool) { return repositories, true },
	}
}

// withCurrentAvatarCompoundRoll 固定额外稀有装扮的随机源。
func withCurrentAvatarCompoundRoll(t *testing.T, roll func(limit int) int) {
	t.Helper()
	previous := currentAvatarCompoundRoll
	currentAvatarCompoundRoll = roll
	t.Cleanup(func() { currentAvatarCompoundRoll = previous })
}

func currentAvatarCompoundTestSlots() map[string]dnfrepo.ItemStack {
	return map[string]dnfrepo.ItemStack{
		"1:5": {ItemID: 41001, Count: 1},
		"1:6": {ItemID: 41002, Count: 1},
		"0:9": {ItemID: 21, Count: 2},
	}
}

func TestCurrentAvatarCompoundConsumesMaterialsAndGrantsRequestedAvatar(t *testing.T) {
	// 固定不命中稀有池：roll 取上限值，必然 >= 950/10000。
	withCurrentAvatarCompoundRoll(t, func(limit int) int { return limit - 1 })
	repositories, catalog := mustCurrentAvatarCompoundTestAssets(t, currentAvatarCompoundTestSlots())
	connection := &bufferConn{}
	service := currentAvatarCompoundTestService(repositories, catalog)
	body := currentAvatarCompoundTestBody(9, 5, 41001, 6, 41002, 41003, 4)
	if err := service.handleCurrentAvatarCompound(&gameSession{conn: connection, selectedCharacterID: 19}, body); err != nil {
		t.Fatal(err)
	}
	ack, _ := splitGameServerUpperPacket(t, connection.write.Bytes())
	if ack.Header.MsgID != uint16(dnfenum.CmdPacketCompoundAvatar) ||
		ack.Header.Classification != dnfproto.DefaultChannelClassification {
		t.Fatalf("op99 ACK header=%+v", ack.Header)
	}
	request, err := parseCurrentAvatarCompoundRequest(body)
	if err != nil {
		t.Fatal(err)
	}
	want := buildCurrentAvatarCompoundSuccessBody(request, []currentAvatarCompoundResultRow{
		{Slot: 5, ItemID: 41003, AbilityNo: 4},
	})
	if !bytes.Equal(ack.Body, want) {
		t.Fatalf("op99 ACK=%x want=%x", ack.Body, want)
	}

	inventory, _, _ := repositories.Inventory.Load(context.Background(), "19")
	if _, exists := inventory.Slots["1:6"]; exists {
		t.Fatalf("second material remains: %+v", inventory.Slots)
	}
	granted, exists := inventory.Slots["1:5"]
	if !exists || granted.ItemID != 41003 || granted.Count != 1 {
		t.Fatalf("granted avatar = %+v exists=%t", granted, exists)
	}
	if granted.Extra["ext_data0"] != "4" || granted.Extra["source"] != "avatar_compound" {
		t.Fatalf("granted extra = %+v", granted.Extra)
	}
	cube, exists := inventory.Slots["0:9"]
	if !exists || cube.ItemID != 21 || cube.Count != 1 {
		t.Fatalf("cube stack = %+v exists=%t", cube, exists)
	}
}

func TestCurrentAvatarCompoundBonusHitGrantsSecondAvatar(t *testing.T) {
	// roll 恒为 0：命中稀有池，并且权重池取第一件（41101）。
	withCurrentAvatarCompoundRoll(t, func(limit int) int { return 0 })
	repositories, catalog := mustCurrentAvatarCompoundTestAssets(t, currentAvatarCompoundTestSlots())
	connection := &bufferConn{}
	service := currentAvatarCompoundTestService(repositories, catalog)
	body := currentAvatarCompoundTestBody(9, 5, 41001, 6, 41002, 41003, 7)
	if err := service.handleCurrentAvatarCompound(&gameSession{conn: connection, selectedCharacterID: 19}, body); err != nil {
		t.Fatal(err)
	}
	ack, _ := splitGameServerUpperPacket(t, connection.write.Bytes())
	request, err := parseCurrentAvatarCompoundRequest(body)
	if err != nil {
		t.Fatal(err)
	}
	want := buildCurrentAvatarCompoundSuccessBody(request, []currentAvatarCompoundResultRow{
		{Slot: 5, ItemID: 41003, AbilityNo: 7},
		{Slot: 6, ItemID: 41101, AbilityNo: 7},
	})
	if !bytes.Equal(ack.Body, want) {
		t.Fatalf("op99 ACK=%x want=%x", ack.Body, want)
	}
	inventory, _, _ := repositories.Inventory.Load(context.Background(), "19")
	if stack, exists := inventory.Slots["1:5"]; !exists || stack.ItemID != 41003 {
		t.Fatalf("requested avatar = %+v exists=%t", stack, exists)
	}
	if stack, exists := inventory.Slots["1:6"]; !exists || stack.ItemID != 41101 {
		t.Fatalf("bonus avatar = %+v exists=%t", stack, exists)
	}
	if cube, exists := inventory.Slots["0:9"]; !exists || cube.Count != 1 {
		t.Fatalf("cube stack = %+v exists=%t", cube, exists)
	}
}

func TestCurrentAvatarCompoundFailureAnswersClientAndConsumesNothing(t *testing.T) {
	cases := map[string]struct {
		slots map[string]dnfrepo.ItemStack
		body  []byte
	}{
		"cube is not a compounder": {
			slots: map[string]dnfrepo.ItemStack{
				"1:5": {ItemID: 41001, Count: 1},
				"1:6": {ItemID: 41002, Count: 1},
				"0:9": {ItemID: 3037, Count: 3},
			},
			body: currentAvatarCompoundTestBody(9, 5, 41001, 6, 41002, 41003, 4),
		},
		"materials are different parts": {
			slots: map[string]dnfrepo.ItemStack{
				"1:5": {ItemID: 41001, Count: 1},
				"1:6": {ItemID: 42001, Count: 1},
				"0:9": {ItemID: 21, Count: 1},
			},
			body: currentAvatarCompoundTestBody(9, 5, 41001, 6, 42001, 41003, 4),
		},
		"requested item outside the pool": {
			slots: currentAvatarCompoundTestSlots(),
			body:  currentAvatarCompoundTestBody(9, 5, 41001, 6, 41002, 41004, 4),
		},
		"material item mismatch": {
			slots: currentAvatarCompoundTestSlots(),
			body:  currentAvatarCompoundTestBody(9, 5, 41002, 6, 41002, 41003, 4),
		},
	}
	for name, testCase := range cases {
		t.Run(name, func(t *testing.T) {
			withCurrentAvatarCompoundRoll(t, func(limit int) int { return limit - 1 })
			repositories, catalog := mustCurrentAvatarCompoundTestAssets(t, testCase.slots)
			connection := &bufferConn{}
			service := currentAvatarCompoundTestService(repositories, catalog)
			if err := service.handleCurrentAvatarCompound(&gameSession{conn: connection, selectedCharacterID: 19}, testCase.body); err != nil {
				t.Fatal(err)
			}
			ack, trailing := splitGameServerUpperPacket(t, connection.write.Bytes())
			if len(trailing) != 0 {
				t.Fatalf("failure wrote extra packets: %x", trailing)
			}
			if ack.Header.MsgID != uint16(dnfenum.CmdPacketCompoundAvatar) ||
				ack.Header.Classification != dnfproto.DefaultChannelClassification {
				t.Fatalf("failure ACK header=%+v", ack.Header)
			}
			if !bytes.Equal(ack.Body, buildCurrentAvatarCompoundFailureBody()) {
				t.Fatalf("failure ACK=%x", ack.Body)
			}
			inventory, _, _ := repositories.Inventory.Load(context.Background(), "19")
			before := testCase.slots
			if len(inventory.Slots) != len(before) {
				t.Fatalf("inventory changed: %+v want %+v", inventory.Slots, before)
			}
			for key, stack := range before {
				got, exists := inventory.Slots[key]
				if !exists || got.ItemID != stack.ItemID || got.Count != stack.Count {
					t.Fatalf("slot %s = %+v exists=%t want %+v", key, got, exists, stack)
				}
			}
		})
	}
}

// 事务边界：prepare 通过之后、提交之前材料被并发改动时，commit 必须整体回滚，
// 不允许出现"消耗了但没产出"。
func TestCurrentAvatarCompoundCommitRollsBackWhenMaterialsChanged(t *testing.T) {
	withCurrentAvatarCompoundRoll(t, func(limit int) int { return limit - 1 })
	repositories, catalog := mustCurrentAvatarCompoundTestAssets(t, currentAvatarCompoundTestSlots())
	service := currentAvatarCompoundTestService(repositories, catalog)
	session := &gameSession{conn: &bufferConn{}, selectedCharacterID: 19}
	ctx := context.Background()
	request, err := parseCurrentAvatarCompoundRequest(currentAvatarCompoundTestBody(9, 5, 41001, 6, 41002, 41003, 4))
	if err != nil {
		t.Fatal(err)
	}
	config, err := loadCurrentAvatarCompoundConfig(catalog.source, 0)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := service.prepareCurrentAvatarCompound(ctx, session, catalog, config, request)
	if err != nil {
		t.Fatal(err)
	}
	// 模拟并发：合成器在 prepare 之后被别处消耗掉。
	inventory, _, _ := repositories.Inventory.Load(ctx, "19")
	delete(inventory.Slots, "0:9")
	if err := repositories.Inventory.Save(ctx, inventory); err != nil {
		t.Fatal(err)
	}
	if _, err := service.commitCurrentAvatarCompound(ctx, session, catalog, request, plan); err == nil {
		t.Fatal("commit accepted a stale plan")
	}
	stored, _, _ := repositories.Inventory.Load(ctx, "19")
	if _, exists := stored.Slots["1:5"]; !exists {
		t.Fatalf("first material was consumed without a result: %+v", stored.Slots)
	}
	if _, exists := stored.Slots["1:6"]; !exists {
		t.Fatalf("second material was consumed without a result: %+v", stored.Slots)
	}
	for _, stack := range stored.Slots {
		if stack.ItemID == 41003 || stack.ItemID == 41101 {
			t.Fatalf("a result was granted on a failed commit: %+v", stored.Slots)
		}
	}
}

func TestCurrentAvatarCompoundCNVariantUsesItsOwnPoolAndMaterial(t *testing.T) {
	source := currentAvatarCompoundTestPVF()
	source["equipment/equipment.lst"] = "41001 `avatar_hat_a.equ`\n41002 `avatar_hat_b.equ`\n41003 `avatar_hat_target.equ`\n41101 `avatar_hat_bonus.equ`\n42001 `avatar_coat_a.equ`\n43001 `avatar_hat_cn.equ`\n"
	source["equipment/avatar_hat_cn.equ"] = "[equipment type] `[hat avatar]`\n[grade] 2\n[rarity] 3\n"
	source["stackable/stackable.lst"] = "21 `cube_bind.stk`\n2683630 `memory_cube.stk`\n"
	source["stackable/memory_cube.stk"] = "[stackable type] `[etc]`\n[stack limit] 999\n"
	source[currentAvatarCompoundCHNRoot+"/compoundavatar_swordman.etc"] = "" +
		"[character type]\n`[swordman]`\n" +
		"[grade]\n1\n" +
		"[rare rate]\n`hat` 900\n[/rare rate]\n" +
		"[upper grade]\n2\n" +
		"[upper rare rate]\n`hat` 300\n[/upper rare rate]\n" +
		"[hat avatar]\n1 41101 50 43001 1000\n[/hat avatar]\n" +
		"[material]\n1 2683630 1\n[/material]\n"

	catalog, err := newPVFDungeonDropCatalog(source)
	if err != nil {
		t.Fatal(err)
	}
	config, err := loadCurrentAvatarCompoundConfig(catalog.source, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(config.Variants) != 2 {
		t.Fatalf("variants = %d", len(config.Variants))
	}
	variant, part, ok := config.variantForItem(43001)
	if !ok || part != "hat" || variant.Path != currentAvatarCompoundCHNRoot+"/compoundavatar_swordman.etc" {
		t.Fatalf("variant selection = %+v part=%s ok=%t", variant.Path, part, ok)
	}
	if !config.acceptsMaterial(2683630) || !config.acceptsMaterial(21) {
		t.Fatalf("materials = %v %v", config.Variants[0].MaterialIDs, config.Variants[1].MaterialIDs)
	}
	if rate := variant.bonusRateFor("hat", 1, 1); rate != 900 {
		t.Fatalf("both normal grade rate = %d", rate)
	}
	if rate := variant.bonusRateFor("hat", 2, 2); rate != 1200 {
		t.Fatalf("both upper grade rate = %d", rate)
	}
	if rate := variant.bonusRateFor("hat", 1, 2); rate != 1050 {
		t.Fatalf("mixed grade rate = %d", rate)
	}
}

// 运行时 PVF 只读校验：把 DNFBRIDGE_REAL_PVF_SMOKE 指到 runtime 的 Script.pvf 才运行。
func TestRealPVFCurrentAvatarCompoundConfig(t *testing.T) {
	pvfPath := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if pvfPath == "" {
		t.Skip("set DNFBRIDGE_REAL_PVF_SMOKE to the runtime Script.pvf")
	}
	archive, err := platformpvf.Open(pvfPath)
	if err != nil {
		t.Fatal(err)
	}
	config, err := loadCurrentAvatarCompoundConfig(archive, 0)
	if err != nil {
		t.Fatal(err)
	}
	var classic, cn *currentAvatarCompoundVariant
	for index := range config.Variants {
		switch config.Variants[index].Path {
		case "etc/compoundavatar_swordman.etc":
			classic = &config.Variants[index]
		case "etc/chn_compound/compoundavatar_swordman.etc":
			cn = &config.Variants[index]
		}
	}
	if classic == nil || cn == nil {
		t.Fatalf("runtime variants = %+v", config.Variants)
	}
	if len(classic.Parts) != 8 || classic.Grade != 1 || classic.UpperGrade != 2 {
		t.Fatalf("classic parts=%d grade=%d upper=%d", len(classic.Parts), classic.Grade, classic.UpperGrade)
	}
	if classic.RareRate["hat"] != 950 || classic.UpperRareRate["hat"] != 325 {
		t.Fatalf("classic rates hat=%d/%d", classic.RareRate["hat"], classic.UpperRareRate["hat"])
	}
	if len(classic.Parts["hat"].Bonus) != 3 || len(classic.Parts["hat"].Upper) == 0 {
		t.Fatalf("classic hat pool bonus=%d upper=%d", len(classic.Parts["hat"].Bonus), len(classic.Parts["hat"].Upper))
	}
	for _, material := range []uint32{21, 899, 490702265} {
		if !config.acceptsMaterial(material) {
			t.Fatalf("classic materials %v missing %d", classic.MaterialIDs, material)
		}
	}
	if !config.acceptsMaterial(2683630) {
		t.Fatalf("cn materials %v missing 2683630", cn.MaterialIDs)
	}
	if _, part, ok := config.variantForItem(42601); !ok || part != "hat" {
		t.Fatalf("classic 42601 selection part=%s ok=%t", part, ok)
	}
	if variant, part, ok := config.variantForItem(39086); !ok || part != "hat" || variant.Path != classic.Path {
		t.Fatalf("classic 39086 selection = %s part=%s ok=%t", variant.Path, part, ok)
	}
}
