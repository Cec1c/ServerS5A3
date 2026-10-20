package dnfbridge

import (
	"bytes"
	"context"
	"encoding/binary"
	"testing"
	"time"

	dnfauction "longheng.io/server/internal/modules/dnf/auction"
	"longheng.io/server/internal/modules/dnf/dnfenum"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	dnfrepomemory "longheng.io/server/internal/modules/dnf/repository/memory"
)

// TestCurrentAuctionCategoryTreeMatchesCurrentClient pins the runtime category
// table to the tree the current client actually builds. The fixture is extracted
// from the client's own AddTreeNode calls (see the generated file), so a drift here
// means the server would answer a code the panel never sends, or drop one it does.
func TestCurrentAuctionCategoryTreeMatchesCurrentClient(t *testing.T) {
	client := map[uint16]struct {
		parent uint16
		label  string
	}{}
	for _, node := range currentAuctionClientCategoryTree {
		client[node.Code] = struct {
			parent uint16
			label  string
		}{node.Parent, node.Label}
	}
	if len(client) != 172 {
		t.Fatalf("client fixture nodes=%d want=172", len(client))
	}
	// 40119 is the tree's root (drawn as 全部): the client creates it before the
	// AddTreeNode series, so it is the one node the fixture does not list.
	if got := len(currentAuctionCategory.nodes); got != len(client)+1 {
		t.Fatalf("runtime category nodes=%d want=%d", got, len(client)+1)
	}
	for code, want := range client {
		node, found := currentAuctionCategoryNodeFor(code)
		if !found {
			t.Fatalf("runtime table is missing client node %d (%s)", code, want.label)
		}
		if node.parent != want.parent {
			t.Fatalf("node %d parent=%d want=%d", code, node.parent, want.parent)
		}
		// Labels are documentation only, but a mismatch means the code was
		// misread, so keep them pinned for every node the client names.
		if want.label != "" && node.label != want.label {
			t.Fatalf("node %d label=%q want=%q", code, node.label, want.label)
		}
	}
	for code := range currentAuctionCategory.nodes {
		if code == currentAuctionCategoryAllCode {
			continue
		}
		if _, found := client[code]; !found {
			t.Fatalf("runtime table has extra node %d that the client never sends", code)
		}
	}
}

// TestCurrentAuctionCategoryChildrenMatchCurrentClient checks the parent/child
// lists the evaluation walks, not just the parent pointers.
func TestCurrentAuctionCategoryChildrenMatchCurrentClient(t *testing.T) {
	want := map[uint16][]uint16{}
	for _, node := range currentAuctionClientCategoryTree {
		want[node.Parent] = append(want[node.Parent], node.Code)
	}
	for parent, children := range want {
		if parent == 0 {
			continue
		}
		node, found := currentAuctionCategoryNodeFor(parent)
		if !found {
			t.Fatalf("missing parent %d", parent)
		}
		if len(node.children) != len(children) {
			t.Fatalf("node %d children=%v want=%v", parent, node.children, children)
		}
		seen := map[uint16]bool{}
		for _, child := range node.children {
			seen[child] = true
			if !containsUint16(children, child) {
				t.Fatalf("node %d has unexpected child %d (client children %v)", parent, child, children)
			}
		}
		for _, child := range children {
			if !seen[child] {
				t.Fatalf("node %d is missing client child %d", parent, child)
			}
		}
	}
}

func containsUint16(values []uint16, want uint16) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

// TestCurrentAuctionCategoryTopLevelShape pins the nine tabs and the documented
// unpinned set. Both lists are deliberate: the tabs drive 其他's "everything else"
// semantics, and every unpinned node is a known gap rather than a silent one.
func TestCurrentAuctionCategoryTopLevelShape(t *testing.T) {
	wantTabs := []uint16{10000, 11000, 12000, 13000, 14000, 31000, 32000, 33000, 34000}
	if len(currentAuctionCategory.topLevel) != len(wantTabs) {
		t.Fatalf("top-level=%v want=%v", currentAuctionCategory.topLevel, wantTabs)
	}
	for index, code := range wantTabs {
		if currentAuctionCategory.topLevel[index] != code {
			t.Fatalf("top-level[%d]=%d want=%d", index, currentAuctionCategory.topLevel[index], code)
		}
	}
	if len(currentAuctionCategory.unpinned) != 0 {
		t.Fatalf("仍有未映射分类：%v", currentAuctionCategory.unpinned)
	}
	for _, code := range []uint16{12005, 12006, 12007, 12008, 12009, 12010, 12011, 12012, 12013, 12014, 12015, 12016, 12017, 12018, 13004, 31001, 31002, 31003, 31004, 31005, 31006, 31007, 31010, 31011, 31101, 31102, 31103, 31104, 31105, 31106, 31201, 31202, 31203, 31204, 31301, 31302, 31303, 32002, 32003, 32004, 32005, 32006, 32007, 32008, 32009, 32010, 32011, 32012, 32013, 32014, 32015, 32016, 32017, 32101, 32102, 32103, 32104, 32105, 32106, 32107, 32108, 32109, 32110, 32111, 32112, 32113, 32114, 32115, 32116, 33001, 33002, 33003} {
		if currentAuctionCategory.nodes[code].kind != currentAuctionCategoryMetadata {
			t.Fatalf("分类%d缺少元数据规则", code)
		}
	}
	for _, code := range currentAuctionCategory.allCodes {
		node := currentAuctionCategory.nodes[code]
		switch node.kind {
		case currentAuctionCategoryMetadata:
			_, job := currentAuctionCategoryJobs[code]
			_, recipe := currentAuctionRecipeCategories[code]
			if !job && !recipe && code != 13004 && (code < 33001 || code > 33003) {
				t.Fatalf("分类%d缺少元数据规则", code)
			}
			continue
		case currentAuctionCategoryUnpinned, currentAuctionCategoryRemainder,
			currentAuctionCategoryGlobal, currentAuctionCategoryAll:
			continue
		}
		if len(node.children) == 0 && node.kind != currentAuctionCategoryItems {
			t.Fatalf("node %d (%s) is kind=%d with no children and no rule", code, node.label, node.kind)
		}
	}
}

func auctionCategoryTestDefinition(kind dungeonDropItemKind, equipmentType, stackableType, pvfPath string) dungeonDropItemDefinition {
	return dungeonDropItemDefinition{
		Kind: kind, EquipmentType: equipmentType, StackableType: stackableType, PVFPath: pvfPath,
	}
}

// TestCurrentAuctionCategoryRulesMatchTheirEvidence walks the rules that the
// client's tree and the PVF census justify, using synthetic definitions so the
// assertions are about the rule text and not about today's catalogue.
func TestCurrentAuctionCategoryRulesMatchTheirEvidence(t *testing.T) {
	katana := auctionCategoryTestDefinition(dungeonDropItemEquipment, "[weapon]", "",
		"equipment/character/swordman/weapon/katana/kat_bloodstream.equ")
	greatsword := auctionCategoryTestDefinition(dungeonDropItemEquipment, "[weapon]", "",
		"equipment/character/swordman/weapon/hsword/chn_401030026.equ")
	wrist := auctionCategoryTestDefinition(dungeonDropItemEquipment, "[wrist]", "",
		"equipment/character/common/wrist/100312431.equ")
	clothCoat := auctionCategoryTestDefinition(dungeonDropItemEquipment, "[coat]", "",
		"equipment/character/common/jacket/cloth/10000.equ")
	plateShoes := auctionCategoryTestDefinition(dungeonDropItemEquipment, "[shoes]", "",
		"equipment/character/common/shoes/plate/19286.equ")
	weaponAvatar := auctionCategoryTestDefinition(dungeonDropItemEquipment, "[weapon avatar]", "",
		"equipment/character/swordman/avatar/weapon/weapon_avatar.equ")
	title := auctionCategoryTestDefinition(dungeonDropItemEquipment, "[title name]", "",
		"equipment/character/common/title/relationship_seria.equ")
	redArtifact := auctionCategoryTestDefinition(dungeonDropItemEquipment, "[artifact red]", "",
		"equipment/creature/artifact_red/zodiac/1.equ")
	pet := auctionCategoryTestDefinition(dungeonDropItemEquipment, "[creature]", "",
		"equipment/creature/2014humanroid/pet.equ")
	material := auctionCategoryTestDefinition(dungeonDropItemStackable, "", "[material]",
		"stackable/material/cubepiece_clear.stk")
	recipe := auctionCategoryTestDefinition(dungeonDropItemStackable, "", "[recipe]",
		"stackable/recipe/rcp_haste.stk")
	expertJob := auctionCategoryTestDefinition(dungeonDropItemStackable, "", "[material expert job]",
		"stackable/monsterCard/mcard_lugaru.stk")
	doll := auctionCategoryTestDefinition(dungeonDropItemStackable, "", "[booster]",
		"stackable/cash/chn_20160126_new_year_package/box.stk")
	misc := auctionCategoryTestDefinition(dungeonDropItemStackable, "", "[etc]",
		"stackable/etc/misc.stk")
	nameTag := auctionCategoryTestDefinition(dungeonDropItemEquipment, "[name tag]", "",
		"equipment/character/common/chn_nametag/2681313.equ")
	avatar := auctionCategoryTestDefinition(dungeonDropItemEquipment, "[coat avatar]", "",
		"equipment/character/swordman/avatar/coat/1.equ")

	for _, test := range []struct {
		name       string
		code       uint16
		definition dungeonDropItemDefinition
		want       bool
	}{
		{"太刀 accepts katana", 10103, katana, true},
		{"太刀 rejects greatsword", 10103, greatsword, false},
		{"巨剑 accepts greatsword", 10105, greatsword, true},
		{"鬼剑士 job accepts katana", 10101, katana, true},
		{"格斗家 job rejects swordman katana", 10201, katana, false},
		{"武器 accepts katana", 10000, katana, true},
		{"武器 rejects weapon avatar", 10000, weaponAvatar, false},
		{"武器 rejects wrist", 10000, wrist, false},
		{"防具 accepts cloth coat", 11000, clothCoat, true},
		{"布甲 accepts cloth coat", 11001, clothCoat, true},
		{"布甲 rejects plate shoes", 11001, plateShoes, false},
		{"布甲>上衣 accepts cloth coat", 11002, clothCoat, true},
		{"布甲>上衣 rejects cloth... shoes are not [coat]", 11002, plateShoes, false},
		{"板甲>鞋 accepts plate shoes", 11404, plateShoes, true},
		{"首饰 accepts wrist", 12000, wrist, true},
		{"手镯 accepts wrist", 12003, wrist, true},
		{"项链 rejects wrist", 12001, wrist, false},
		{"首饰 accepts title", 12000, title, true},
		{"称号 accepts title", 12004, title, true},
		{"宠物 accepts red artifact", 14000, redArtifact, true},
		{"红色宠物装备 accepts red artifact", 14001, redArtifact, true},
		{"蓝色宠物装备 rejects red artifact", 14002, redArtifact, false},
		{"宠物 accepts the pet itself", 14000, pet, true},
		{"材料 accepts clear cube", 13002, material, true},
		{"消耗品>材料 rejects recipe", 13002, recipe, false},
		{"消耗品 rejects recipe: 设计图 owns it", 13000, recipe, false},
		{"消耗品>消耗品 rejects recipe", 13001, recipe, false},
		{"消耗品 rejects 副职业材料", 13000, expertJob, false},
		{"消耗品 accepts booster", 13000, doll, true},
		{"消耗品>消耗品 accepts booster", 13001, doll, true},
		{"消耗品>其他 takes the leftover stackables", 13006, misc, true},
		{"消耗品>其他 rejects 材料", 13006, material, false},
		{"设计图 accepts recipe", 31000, recipe, true},
		{"设计图 rejects material", 31000, material, false},
		{"副职业 accepts expert job material", 33000, expertJob, true},
		{"副职业材料 accepts expert job material", 33004, expertJob, true},
		{"副职业材料 rejects recipe", 33004, recipe, false},
		{"其他 rejects katana", 34000, katana, false},
		{"其他 rejects clear cube", 34000, material, false},
		{"其他 rejects recipe", 34000, recipe, false},
		{"其他 takes name tags (no tab claims them)", 34000, nameTag, true},
		{"其他 takes avatars (no tab claims them)", 34000, avatar, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := currentAuctionCategoryItemMatches(test.code, test.definition); got != test.want {
				t.Fatalf("code=%d got=%t want=%t definition=%+v", test.code, got, test.want, test.definition)
			}
		})
	}
}

// TestCurrentAuctionCategoryUnpinnedNodesStayEmpty: an unproven node must answer
// nothing rather than everything.
func TestCurrentAuctionCategoryMetadataRules(t *testing.T) {
	for code, jobs := range currentAuctionCategoryJobs {
		parent := currentAuctionCategory.nodes[code].parent
		typ := "[title name]"
		if parent == 32001 {
			typ = "[support]"
		}
		if parent == 32100 {
			typ = "[magic stone]"
		}
		d := dungeonDropItemDefinition{Kind: dungeonDropItemEquipment, EquipmentType: typ, AuctionUsableJobs: jobs}
		if !currentAuctionCategoryItemMatches(code, d) {
			t.Fatalf("职业分类%d未命中", code)
		}
		d.AuctionUsableJobs = []string{"[missing job]"}
		if currentAuctionCategoryItemMatches(code, d) {
			t.Fatalf("职业分类%d串职业", code)
		}
		d.AuctionUsableJobs = []string{"[all]"}
		if !currentAuctionCategoryItemMatches(code, d) {
			t.Fatalf("职业分类%d排除了通用装备", code)
		}
		d.EquipmentType = "[weapon]"
		if currentAuctionCategoryItemMatches(code, d) {
			t.Fatalf("职业分类%d串装备类型", code)
		}
	}
	sword := auctionCategoryTestDefinition(dungeonDropItemEquipment, "[weapon]", "", "equipment/character/swordman/weapon/ssword/a.equ")
	recipe := dungeonDropItemDefinition{Kind: dungeonDropItemStackable, StackableType: "[recipe]", AuctionRecipeOutput: &sword}
	if !currentAuctionCategoryItemMatches(31001, recipe) || !currentAuctionCategoryItemMatches(31002, recipe) || currentAuctionCategoryItemMatches(31101, recipe) {
		t.Fatal("设计图没有按产物分类")
	}
	recipe.AuctionRecipeOutput = nil
	if currentAuctionCategoryItemMatches(31001, recipe) {
		t.Fatal("未解析产物的设计图混入武器")
	}
	material := dungeonDropItemDefinition{Kind: dungeonDropItemStackable, StackableType: "[material expert job]", AuctionExpertJobs: []byte{2, 4}}
	if !currentAuctionCategoryItemMatches(33001, material) || !currentAuctionCategoryItemMatches(33002, material) || currentAuctionCategoryItemMatches(33003, material) {
		t.Fatal("副职业材料分类串配方")
	}
	material.AuctionExtractor = true
	if !currentAuctionCategoryItemMatches(13004, material) {
		t.Fatal("提取器分类没有读取副职业目录")
	}
}

// TestCurrentAuctionCategoryUnknownAndAllCodesKeepEveryItem documents the two
// codes that must not filter: 全部 (40119) and anything this build has never seen.
func TestCurrentAuctionCategoryUnknownAndAllCodesKeepEveryItem(t *testing.T) {
	service := &Service{}
	if !service.currentAuctionCategoryEligible(currentAuctionCategoryAllCode, 12345) {
		t.Fatal("全部 must not filter anything")
	}
	if !service.currentAuctionCategoryEligible(0, 12345) {
		t.Fatal("an unset code must not filter anything")
	}
	if !service.currentAuctionCategoryEligible(65000, 12345) {
		t.Fatal("an unknown code must not filter anything")
	}
	// A known code with no catalogue behind it answers false, i.e. an empty page.
	if service.currentAuctionCategoryEligible(10103, 12345) {
		t.Fatal("a known category without a catalogue must not report a match")
	}
	seam := &Service{auctionCategoryItem: func(code uint16, itemID uint32) bool { return code == 10103 && itemID == 7 }}
	if !seam.currentAuctionCategoryEligible(10103, 7) || seam.currentAuctionCategoryEligible(10103, 8) {
		t.Fatal("test seam must drive the decision")
	}
	if !seam.currentAuctionCategoryEligible(currentAuctionCategoryAllCode, 8) {
		t.Fatal("全部 must bypass the seam")
	}
}

// TestCurrentAuctionCategoryRequestCode pins the op187 operand offsets against the
// live capture form (mode, page, category, five flags, trailer).
// TestCurrentAuctionCategorySearchFiltersByRequestCode is the end-to-end pin for
// op187: the u16 at body[5:7] decides which listings come back, an unpinned node
// answers an empty page, and 全部 / unknown codes keep the whole market.
func TestCurrentAuctionCategorySearchFiltersByRequestCode(t *testing.T) {
	ctx := context.Background()
	seed := func(t *testing.T) (dnfrepo.Group, *gameSession, *Service, *bufferConn) {
		t.Helper()
		repositories := dnfrepomemory.NewMemoryGroup()
		seedCurrentAuctionBridgeCharacter(t, ctx, repositories, "10", "account-seller", "卖家", 20_000, map[string]dnfrepo.ItemStack{
			"0:9":  {ItemID: 6001, Count: 3},
			"0:10": {ItemID: 6002, Count: 3},
		})
		owner, err := dnfauction.NewOwner(repositories)
		if err != nil {
			t.Fatal(err)
		}
		for _, listing := range []struct {
			id     uint64
			slot   int16
			itemID int64
		}{{51, 9, 6001}, {52, 10, 6002}} {
			if _, err := owner.Register(ctx, dnfauction.RegisterCommand{
				ListingID: listing.id, SellerID: "10", AccountID: "account-seller", SellerName: "卖家",
				ListType: 0, SourceSlot: listing.slot, ExpectedID: listing.itemID, Count: 3,
				UnitPrice: 40, Duration: time.Hour, ListingLimit: 5,
			}); err != nil {
				t.Fatal(err)
			}
		}
		connection := &bufferConn{}
		service := &Service{
			options:                options{gameUpperHeader: gameUpperHeaderChannel13, gameUpperBodyCodec: gameUpperBodyCodecPlain},
			repositoryProvider:     func() (dnfrepo.Group, bool) { return repositories, true },
			auctionItemEligibility: func(itemID uint32) bool { return itemID == 6001 || itemID == 6002 },
			// The test seam stands in for the PVF catalogue: category 10103 (太刀)
			// accepts 6001 only, 13002 (材料) accepts 6002 only.
			auctionCategoryItem: func(code uint16, itemID uint32) bool {
				switch code {
				case 10103:
					return itemID == 6001
				case 13002:
					return itemID == 6002
				case 12005: // 测试目录中没有适用该职业的称号。
					return false
				}
				return false
			},
		}
		session := &gameSession{conn: connection, selectedCharacterID: 11, accountID: "account-buyer"}
		return repositories, session, service, connection
	}
	request := func(code uint16) []byte {
		body := make([]byte, 20)
		body[0] = currentAuctionPayTypeGold
		binary.LittleEndian.PutUint16(body[5:7], code)
		return body
	}
	search := func(t *testing.T, service *Service, session *gameSession, connection *bufferConn, code uint16) (uint32, uint16, []byte) {
		t.Helper()
		connection.write.Reset()
		if err := service.searchCurrentAuctionCategoryLocked(session, request(code)); err != nil {
			t.Fatal(err)
		}
		packet, trailing := splitGameServerUpperPacket(t, connection.write.Bytes())
		if len(trailing) != 0 || packet.Header.MsgID != uint16(dnfenum.CmdPacketAuctionSearchByItemkey) {
			t.Fatalf("code=%d header=%+v trailing=%d", code, packet.Header, len(trailing))
		}
		if packet.Body[0] != 1 {
			t.Fatalf("code=%d body=%x want success", code, packet.Body)
		}
		return binary.LittleEndian.Uint32(packet.Body[2:6]), binary.LittleEndian.Uint16(packet.Body[6:8]), packet.Body
	}

	for _, test := range []struct {
		name      string
		code      uint16
		wantTotal uint32
	}{
		{"太刀 keeps its own item", 10103, 1},
		{"材料 keeps its own item", 13002, 1},
		{"全部 keeps everything", currentAuctionCategoryAllCode, 2},
		{"unknown code keeps everything", 65000, 2},
		{"unpinned node answers nothing", 12005, 0},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, session, service, connection := seed(t)
			total, count, body := search(t, service, session, connection, test.code)
			if total != test.wantTotal {
				t.Fatalf("code=%d total=%d want=%d body=%x", test.code, total, test.wantTotal, body)
			}
			if int(count) != int(test.wantTotal) {
				t.Fatalf("code=%d count=%d want=%d", test.code, count, test.wantTotal)
			}
			if test.wantTotal == 1 {
				row := body[8 : 8+0xA8]
				itemID := binary.LittleEndian.Uint32(row[0x2B+0x01 : 0x2B+0x05])
				wantItem := uint32(6001)
				if test.code == 13002 {
					wantItem = 6002
				}
				if itemID != wantItem {
					t.Fatalf("code=%d returned item=%d want=%d", test.code, itemID, wantItem)
				}
			}
		})
	}
}

// TestCurrentAuctionCategorySearchKeepsOp186AndConsignmentUntouched guards the
// blast radius of the op187 change: the item-key search (op186) still answers by
// item id alone, and the gold-consignment mode still takes its own branch.
func TestCurrentAuctionCategorySearchKeepsOp186AndConsignmentUntouched(t *testing.T) {
	ctx := context.Background()
	repositories := dnfrepomemory.NewMemoryGroup()
	seedCurrentAuctionBridgeCharacter(t, ctx, repositories, "10", "account-seller", "卖家", 20_000, map[string]dnfrepo.ItemStack{
		"0:9": {ItemID: 6001, Count: 3},
	})
	owner, err := dnfauction.NewOwner(repositories)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := owner.Register(ctx, dnfauction.RegisterCommand{
		ListingID: 51, SellerID: "10", AccountID: "account-seller", SellerName: "卖家",
		ListType: 0, SourceSlot: 9, ExpectedID: 6001, Count: 3,
		UnitPrice: 40, Duration: time.Hour, ListingLimit: 5,
	}); err != nil {
		t.Fatal(err)
	}
	connection := &bufferConn{}
	service := &Service{
		options:                options{gameUpperHeader: gameUpperHeaderChannel13, gameUpperBodyCodec: gameUpperBodyCodecPlain},
		repositoryProvider:     func() (dnfrepo.Group, bool) { return repositories, true },
		auctionItemEligibility: func(itemID uint32) bool { return itemID == 6001 },
		auctionCategoryItem:    func(code uint16, itemID uint32) bool { return false },
	}
	session := &gameSession{conn: connection, selectedCharacterID: 11, accountID: "account-buyer"}

	// op186: mode 0, page 0, count 1, category 40119, item 6001. The category is
	// ignored on this path; the item id list is what filters.
	body := []byte{0x00, 0, 0, 0, 0, 0x00, 0x1f, 0x01, 0xb7, 0x9c, 0x71, 0x17, 0x00, 0x00, 0, 0, 0, 0, 0, 0, 0, 0x08}
	if err := service.searchCurrentAuctionItemsLocked(session, body); err != nil {
		t.Fatal(err)
	}
	if total, count, _ := decodeCurrentAuctionSearchPage(t, connection); total != 1 || count != 1 {
		t.Fatalf("op186 total=%d count=%d want 1/1: the item-key search must stay untouched", total, count)
	}

	// op187 mode 1 is the 金币寄售 branch and must not reach the item categories.
	gold := make([]byte, 20)
	gold[0] = currentAuctionPayTypeConsignment
	gold[5], gold[6] = 0x10, 0x27
	if err := service.searchCurrentAuctionCategoryLocked(session, gold); err != nil {
		t.Fatal(err)
	}
	packet, rest := splitGameServerUpperPacket(t, connection.write.Bytes())
	if len(rest) != 0 || !bytes.Equal(packet.Body, []byte{0, currentAuctionFailureCode, 1}) {
		t.Fatalf("金币寄售拒绝必须保留自身路由：%x", packet.Body)
	}
}

func decodeCurrentAuctionSearchPage(t *testing.T, connection *bufferConn) (uint32, uint16, []byte) {
	t.Helper()
	packet, trailing := splitGameServerUpperPacket(t, connection.write.Bytes())
	if len(trailing) != 0 {
		t.Fatalf("trailing=%x", trailing)
	}
	connection.write.Reset()
	if packet.Body[0] != 1 {
		t.Fatalf("body=%x want success", packet.Body)
	}
	return binary.LittleEndian.Uint32(packet.Body[2:6]), binary.LittleEndian.Uint16(packet.Body[6:8]), packet.Body
}

func decodeCurrentAuctionSearchEnvelope(t *testing.T, connection *bufferConn) (uint32, uint16, byte, bool) {
	t.Helper()
	packet, trailing := splitGameServerUpperPacket(t, connection.write.Bytes())
	if len(trailing) != 0 || len(packet.Body) < 8 || packet.Body[0] != 1 {
		return 0, 0, 0, false
	}
	return binary.LittleEndian.Uint32(packet.Body[2:6]), binary.LittleEndian.Uint16(packet.Body[6:8]), packet.Body[1], true
}

func TestCurrentAuctionCategoryRequestCode(t *testing.T) {
	for _, test := range []struct {
		body []byte
		want uint16
	}{
		{[]byte{0x00, 0, 0, 0, 0, 0x10, 0x27, 0x07, 0x00, 0x1f, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0x08}, 10000},
		{[]byte{0x00, 0, 0, 0, 0, 0x77, 0x27, 0x07, 0x00, 0x1f, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0x08}, 10103},
		{[]byte{0x00, 0, 0, 0, 0, 0x00, 0x7d, 0x07, 0x00, 0x1f, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0x08}, 32000},
		{[]byte{0x00, 0, 0, 0}, 0},
		{nil, 0},
	} {
		if got := currentAuctionCategoryRequestCode(test.body); got != test.want {
			t.Fatalf("category=%d want=%d body=%x", got, test.want, test.body)
		}
	}
}
