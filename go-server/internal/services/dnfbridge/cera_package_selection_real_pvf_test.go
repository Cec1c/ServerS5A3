package dnfbridge

import (
	"context"
	"encoding/binary"
	"fmt"
	"os"
	"reflect"
	"strings"
	"testing"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	dnfrepomemory "longheng.io/server/internal/modules/dnf/repository/memory"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

// realCeraPackageFixture 打开真实运行时 PVF，并给出一个装了指定礼包的内存角色。
type realCeraPackageFixture struct {
	catalog      *pvfDungeonDropCatalog
	service      *Service
	repositories dnfrepo.Group
	characterID  string
	sourceSlot   int16
}

func newRealCeraPackageFixture(t *testing.T, sourceItemID uint32, sourceCount int, sourceSlot int16) realCeraPackageFixture {
	t.Helper()
	pvfPath := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if pvfPath == "" {
		t.Skip("set DNFBRIDGE_REAL_PVF_SMOKE to verify the real Cera package selection chain")
	}
	archive, err := platformpvf.LoadArchive(platformpvf.Options{Path: pvfPath})
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := newPVFDungeonDropCatalog(archive)
	if err != nil {
		t.Fatal(err)
	}
	repositories := dnfrepomemory.NewMemoryGroup()
	ctx := context.Background()
	const characterID = "19"
	if err := repositories.Character.Save(ctx, dnfrepo.CharacterRecord{CharacterID: characterID, AccountID: "account-1"}); err != nil {
		t.Fatal(err)
	}
	stack := dnfrepo.ItemStack{ItemID: int64(sourceItemID), Count: int64(sourceCount)}
	entry := currentItemListEntryFromStack(dnfrepo.MainInventoryListType, sourceSlot, stack)
	stack.RawEntry = append([]byte(nil), entry.data[:]...)
	if err := repositories.Inventory.Save(ctx, dnfrepo.InventoryRecord{
		CharacterID: characterID,
		Slots:       map[string]dnfrepo.ItemStack{currentCeraShopInventorySlotKey(dnfrepo.MainInventoryListType, sourceSlot): stack},
	}); err != nil {
		t.Fatal(err)
	}
	service := &Service{
		options:            options{accountID: "account-1"},
		pvfItemCatalog:     catalog,
		repositoryProvider: func() (dnfrepo.Group, bool) { return repositories, true },
	}
	return realCeraPackageFixture{
		catalog:      catalog,
		service:      service,
		repositories: repositories,
		characterID:  characterID,
		sourceSlot:   sourceSlot,
	}
}

func (f realCeraPackageFixture) inventory(t *testing.T) dnfrepo.InventoryRecord {
	t.Helper()
	inventory, found, err := f.repositories.Inventory.Load(context.Background(), f.characterID)
	if err != nil || !found {
		t.Fatalf("load inventory found=%t err=%v", found, err)
	}
	return inventory
}

// avatarStacks 返回装扮栏(list 1)里的每一行，按槽位排序。
func (f realCeraPackageFixture) avatarStacks(t *testing.T) map[uint32]dnfrepo.ItemStack {
	t.Helper()
	inventory := f.inventory(t)
	out := make(map[uint32]dnfrepo.ItemStack)
	for key, stack := range inventory.Slots {
		listType, _, ok := parseSceneInventorySlotKey(key)
		if !ok || listType != 1 {
			continue
		}
		out[uint32(stack.ItemID)] = stack
	}
	return out
}

// TestRealPVFFestivalCeraPackageGrantsEverySelectedAvatarWithItsOwnAttribute
// 用真实节日套礼包逐项核对"选择 → 服务端解析 → 下发"：
//  1. 每个被选中的装扮 item id 必须原样落到装扮栏(list 1)——不存在"只发第一项"；
//  2. 每件装扮的"属性"(ext_data0，客户端在 0x77 行的 0x0A 读它)必须是**该项自己**
//     被选中的那个 option，不能被别的项覆盖、也不能退化成第一项的 option；
//  3. 只选择第一项之外的项同样必须正确（用例显式选第 7/8/9 项作为唯一区别）。
func TestRealPVFFestivalCeraPackageGrantsEverySelectedAvatarWithItsOwnAttribute(t *testing.T) {
	const sourceItemID = 490701336 // 2017海军的梦想礼包 (鬼剑士/黑暗武士 1)
	fixture := newRealCeraPackageFixture(t, sourceItemID, 1, 75)
	definition, err := resolveCurrentCeraPackageDefinition(fixture.catalog, sourceItemID)
	if err != nil {
		t.Fatal(err)
	}
	if len(definition.AvatarItemIDs) < 8 {
		t.Fatalf("real package avatars=%v want at least 8", definition.AvatarItemIDs)
	}
	// 每个部位一个**互不相同**的属性字节：任何"只有第一项生效"的实现都会在这里露馅。
	choices := make([]currentCeraPackageChoice, 0, len(definition.AvatarItemIDs))
	wantOptions := make(map[uint32]byte, len(definition.AvatarItemIDs))
	for index, itemID := range definition.AvatarItemIDs {
		option := byte(3 + index*5)
		choices = append(choices, currentCeraPackageChoice{ItemID: itemID, Option: option})
		wantOptions[itemID] = option
	}
	// 最后一项的 option 故意既不是 0 也不是第一项的 option。
	result, err := fixture.service.commitCurrentCeraPackage(
		context.Background(),
		&gameSession{selectedCharacterID: 19},
		fixture.catalog,
		definition,
		currentCeraPackageOpenRequest{SourceSlot: fixture.sourceSlot, Choices: choices},
	)
	if err != nil {
		t.Fatalf("commit real festival package: %v", err)
	}
	if !reflect.DeepEqual(result.ChangedListTypes, []byte{0, 1}) {
		t.Fatalf("changed lists=%v want [0 1]", result.ChangedListTypes)
	}
	granted := fixture.avatarStacks(t)
	for _, itemID := range definition.AvatarItemIDs {
		stack, found := granted[itemID]
		if !found {
			t.Fatalf("selected avatar=%d missing from list 1; granted=%v", itemID, sortedStackItemIDs(granted))
		}
		if stack.Count != 1 {
			t.Fatalf("avatar=%d count=%d want 1", itemID, stack.Count)
		}
		if got := stack.Extra["ext_data0"]; got != fmt.Sprint(wantOptions[itemID]) {
			t.Fatalf("avatar=%d attribute=%q want=%d (all=%v)", itemID, got, wantOptions[itemID], wantOptions)
		}
		if stack.Extra["source"] != "cera_package" || stack.Extra["cera_package_source_item"] != fmt.Sprint(sourceItemID) {
			t.Fatalf("avatar=%d provenance=%v", itemID, stack.Extra)
		}
		// +0x0B 的 u16 就是客户端读"属性"的那一个字（与耐久同字，见
		// avatar_attribute_row.go）：0x77 行字节必须和 Extra 一致，且 +0x0A
		// （强化字节）对装扮必须是 0。
		if got := binary.LittleEndian.Uint16(stack.RawEntry[currentItemListDurabilityOffset : currentItemListDurabilityOffset+2]); got != uint16(wantOptions[itemID]) {
			t.Fatalf("avatar=%d raw ability word=%d want=%d raw=%x", itemID, got, wantOptions[itemID], stack.RawEntry)
		}
		if got := stack.RawEntry[currentItemListExtDataByteOffset]; got != 0 {
			t.Fatalf("avatar=%d raw 0x0A=%d want 0 raw=%x", itemID, got, stack.RawEntry)
		}
	}
	if len(granted) != len(definition.AvatarItemIDs) {
		t.Fatalf("granted avatars=%v want exactly %v", sortedStackItemIDs(granted), definition.AvatarItemIDs)
	}
	if _, stillThere := fixture.inventory(t).Slots[currentCeraShopInventorySlotKey(dnfrepo.MainInventoryListType, fixture.sourceSlot)]; stillThere {
		t.Fatal("source package was not consumed")
	}
}

// TestRealPVFFestivalCeraPackageLastChoiceOnlyDiffersByItsOwnOption pins the
// "只选第一项之外的项也必须正确" half of the contract: identical selections that
// differ only in ONE non-first part's attribute must produce different rows.
func TestRealPVFFestivalCeraPackageLastChoiceOnlyDiffersByItsOwnOption(t *testing.T) {
	const sourceItemID = 490701388 // 2017海军的浪漫礼包 (鬼剑士/黑暗武士 1)
	fixture := newRealCeraPackageFixture(t, sourceItemID, 2, 75)
	definition, err := resolveCurrentCeraPackageDefinition(fixture.catalog, sourceItemID)
	if err != nil {
		t.Fatal(err)
	}
	last := definition.AvatarItemIDs[len(definition.AvatarItemIDs)-1]
	open := func(lastOption byte) dnfrepo.InventoryRecord {
		choices := make([]currentCeraPackageChoice, 0, len(definition.AvatarItemIDs))
		for _, itemID := range definition.AvatarItemIDs {
			option := byte(1)
			if itemID == last {
				option = lastOption
			}
			choices = append(choices, currentCeraPackageChoice{ItemID: itemID, Option: option})
		}
		if _, err := fixture.service.commitCurrentCeraPackage(
			context.Background(),
			&gameSession{selectedCharacterID: 19},
			fixture.catalog,
			definition,
			currentCeraPackageOpenRequest{SourceSlot: fixture.sourceSlot, Choices: choices},
		); err != nil {
			t.Fatalf("commit option=%d: %v", lastOption, err)
		}
		return fixture.inventory(t)
	}
	first := open(7)
	second := open(9)
	_ = first
	// 第二次开包后的装扮栏 = 两次开包的全部行：第 7/8/9 件之外的项两次都是
	// option=1；最后一件一次 7、一次 9——证明"只改一项的属性"确实只影响那一项。
	attributesByItem := map[uint32]map[byte]int{}
	for key, stack := range second.Slots {
		listType, _, ok := parseSceneInventorySlotKey(key)
		if !ok || listType != 1 {
			continue
		}
		var option byte
		if _, err := fmt.Sscanf(stack.Extra["ext_data0"], "%d", &option); err != nil {
			t.Fatalf("avatar=%d attribute=%q", stack.ItemID, stack.Extra["ext_data0"])
		}
		if attributesByItem[uint32(stack.ItemID)] == nil {
			attributesByItem[uint32(stack.ItemID)] = map[byte]int{}
		}
		attributesByItem[uint32(stack.ItemID)][option]++
	}
	lastOptions := attributesByItem[last]
	if lastOptions[7] != 1 || lastOptions[9] != 1 {
		t.Fatalf("last part=%d options=%v want one row each for 7 and 9", last, lastOptions)
	}
	for _, itemID := range definition.AvatarItemIDs {
		if itemID == last {
			continue
		}
		if got := attributesByItem[itemID]; got[1] != 2 {
			t.Fatalf("unchanged part=%d options=%v want two option-1 rows", itemID, got)
		}
	}
}

// TestRealPVFFestivalCeraPackageRejectsPartialAndForgedSelections documents the
// exact contract the client has to satisfy: one choice per avatar item in
// [package data], no duplicates, no unknown item ids.
func TestRealPVFFestivalCeraPackageRejectsPartialAndForgedSelections(t *testing.T) {
	const sourceItemID = 490701876 // 2018兽人指引者礼包 (鬼剑士/黑暗武士 1)
	fixture := newRealCeraPackageFixture(t, sourceItemID, 1, 75)
	definition, err := resolveCurrentCeraPackageDefinition(fixture.catalog, sourceItemID)
	if err != nil {
		t.Fatal(err)
	}
	full := make([]currentCeraPackageChoice, 0, len(definition.AvatarItemIDs))
	for _, itemID := range definition.AvatarItemIDs {
		full = append(full, currentCeraPackageChoice{ItemID: itemID, Option: 1})
	}
	partial := append([]currentCeraPackageChoice(nil), full[:len(full)-1]...)
	duplicated := append(append([]currentCeraPackageChoice(nil), full...), currentCeraPackageChoice{ItemID: definition.AvatarItemIDs[0], Option: 2})
	forged := append(append([]currentCeraPackageChoice(nil), full...), currentCeraPackageChoice{ItemID: 999999, Option: 2})
	cases := []struct {
		name    string
		choices []currentCeraPackageChoice
	}{
		{name: "partial selection (only the first parts)", choices: partial},
		{name: "duplicate item", choices: duplicated},
		{name: "unknown item", choices: forged},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			_, err := fixture.service.commitCurrentCeraPackage(
				context.Background(),
				&gameSession{selectedCharacterID: 19},
				fixture.catalog,
				definition,
				currentCeraPackageOpenRequest{SourceSlot: fixture.sourceSlot, Choices: testCase.choices},
			)
			if err == nil {
				t.Fatal("selection was accepted")
			}
			if !strings.Contains(err.Error(), "selection is invalid") {
				t.Fatalf("selection error=%v want errCurrentCeraPackageChoiceInvalid", err)
			}
		})
	}
}

// TestCurrentCeraSelectionAuditSummaries pin the diagnostic log contract used by
// the live "选择职业→自动选择属性" investigation: one line must show the client's
// exact selection so a rejection distinguishes a subset from a duplicate or an
// unknown item id without a client packet capture.
func TestCurrentCeraSelectionAuditSummaries(t *testing.T) {
	choices := []currentCeraPackageChoice{{ItemID: 101550691, Option: 3}, {ItemID: 101560839, Option: 8}}
	if got := currentCeraPackageChoiceSummary(choices); got != "2[101550691:3 101560839:8]" {
		t.Fatalf("choice summary=%q", got)
	}
	if got := currentCeraPackageChoiceSummary(nil); got != "<none>" {
		t.Fatalf("empty choice summary=%q", got)
	}
	definition := currentCeraPackageDefinition{AvatarItemIDs: []uint32{700, 701}}
	if got := currentCeraPackageExpectedSummary(definition); got != "2[700 701]" {
		t.Fatalf("expected summary=%q", got)
	}
	cart := []currentCeraShopPurchaseRequestItem{{CommodityID: 108566, AttributeValue: 5}, {CommodityID: 108565, AttributeValue: 0}}
	if got := currentCeraShopCartSummary(cart); got != "2[108566:5 108565:0]" {
		t.Fatalf("cart summary=%q", got)
	}
	if got := currentCeraSelectionBodyHex([]byte{1, 2, 255}); got != "0102ff" {
		t.Fatalf("body hex=%q", got)
	}
}

// TestRealPVFCeraPackageAvatarRewardsShareOneJobGroup 钉住"选项列表从哪来"的
// 前提：真实 Script.pvf 里 3896 个 [usable cera package] 中，**没有任何一个**礼包
// 的装扮奖励跨职业组——每个礼包的装扮要么全是同一职业（可含"鬼剑士+黑暗武士"这类
// 共用对），要么根本没有装扮。因此客户端开包对话框里的"职业"列表不可能由礼包内容
// 的**多职业**产生，也不可能由服务端投影产生：服务端只收到 (item id, option)。
func TestRealPVFCeraPackageAvatarRewardsShareOneJobGroup(t *testing.T) {
	pvfPath := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if pvfPath == "" {
		t.Skip("set DNFBRIDGE_REAL_PVF_SMOKE to audit every real Cera package job spread")
	}
	archive, err := platformpvf.LoadArchive(platformpvf.Options{Path: pvfPath})
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := newPVFDungeonDropCatalog(archive)
	if err != nil {
		t.Fatal(err)
	}
	packages, withoutAvatars, crossJob, multiJobAvatars := 0, 0, 0, 0
	for itemID, reference := range catalog.itemRefs {
		if reference.kind != dungeonDropItemStackable {
			continue
		}
		definition, resolveErr := catalog.ResolveItem(itemID)
		if resolveErr != nil {
			continue
		}
		document, readErr := parseDungeonCardPVFDocument(catalog.source, definition.PVFPath)
		if readErr != nil {
			continue
		}
		stackableType, _ := document.Text("stackable type")
		if !strings.EqualFold(strings.TrimSpace(stackableType), "[usable cera package]") {
			continue
		}
		resolved, packageErr := resolveCurrentCeraPackageDefinition(catalog, itemID)
		if packageErr != nil {
			withoutAvatars++
			continue
		}
		packages++
		groupKey := ""
		for _, avatarID := range resolved.AvatarItemIDs {
			avatarDefinition, avatarErr := catalog.ResolveItem(avatarID)
			if avatarErr != nil {
				t.Fatalf("package=%d avatar=%d: %v", itemID, avatarID, avatarErr)
			}
			avatarDocument, docErr := parseDungeonCardPVFDocument(catalog.source, avatarDefinition.PVFPath)
			if docErr != nil {
				t.Fatalf("package=%d avatar=%d: %v", itemID, avatarID, docErr)
			}
			jobs := avatarDocument.Texts("usable job")
			if len(jobs) == 0 {
				t.Fatalf("package=%d avatar=%d has no [usable job]", itemID, avatarID)
			}
			if len(jobs) > 1 {
				multiJobAvatars++
			}
			key := strings.ToLower(strings.Join(jobs, "|"))
			if groupKey == "" {
				groupKey = key
				continue
			}
			if key != groupKey {
				crossJob++
				t.Errorf("package=%d mixes job groups %q and %q", itemID, groupKey, key)
			}
		}
	}
	t.Logf("cera packages with avatars=%d without avatar rewards=%d cross_job_packages=%d multi_job_avatars=%d",
		packages, withoutAvatars, crossJob, multiJobAvatars)
	if packages == 0 || multiJobAvatars == 0 {
		t.Fatal("real PVF no longer contains avatar packages with shared multi-job avatars")
	}
}

// TestRealPVFCeraShopPackageRowsCarryOnlyTheFirstSuitableJob 钉住商城 [package]
// 行的职业码事实（这也是"只会出现第一个职业的选项"唯一可能的服务端/数据侧来源）：
// 每行只有一个职业码字段，而 cmd/scratch/pvfshopgift 与**原始**夏日行一样只写
// [suitable job] 的第一项——"鬼剑士/黑暗武士"这类双职业礼包因此只带第一个职业码。
func TestRealPVFCeraShopPackageRowsCarryOnlyTheFirstSuitableJob(t *testing.T) {
	pvfPath := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if pvfPath == "" {
		t.Skip("set DNFBRIDGE_REAL_PVF_SMOKE to audit the real [package] job codes")
	}
	archive, err := platformpvf.LoadArchive(platformpvf.Options{Path: pvfPath})
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := newPVFDungeonDropCatalog(archive)
	if err != nil {
		t.Fatal(err)
	}
	document, err := parseDungeonCardPVFDocument(catalog.source, "etc/cerashop.etc")
	if err != nil {
		t.Fatal(err)
	}
	tokens, found := document.Section("package")
	if !found {
		t.Fatal("etc/cerashop.etc has no [package] section")
	}
	values := currentCeraShopPVFValues(tokens)
	const width = 11
	if len(values)%width != 0 {
		t.Fatalf("[package] tokens=%d is not %d-token rows", len(values), width)
	}
	// 先用单职业行推出 job tag → 职业码 的映射（与 pvfshopgift 的 deriveJobCodes 同源）。
	jobCodes := map[string]int64{}
	rows := 0
	multiJobRows := 0
	for offset := 0; offset+width <= len(values); offset += width {
		itemID := values[offset+1].Int
		if itemID <= 0 {
			continue
		}
		definition, resolveErr := catalog.ResolveItem(uint32(itemID))
		if resolveErr != nil {
			continue
		}
		itemDocument, docErr := parseDungeonCardPVFDocument(catalog.source, definition.PVFPath)
		if docErr != nil {
			continue
		}
		jobs := itemDocument.Texts("suitable job")
		if len(jobs) == 0 {
			continue
		}
		rows++
		if len(jobs) > 1 {
			multiJobRows++
		}
		if len(jobs) == 1 {
			jobCodes[strings.ToLower(strings.TrimSpace(jobs[0]))] = values[offset+10].Int
		}
	}
	covered := 0
	for offset := 0; offset+width <= len(values); offset += width {
		itemID := values[offset+1].Int
		if itemID <= 0 {
			continue
		}
		definition, resolveErr := catalog.ResolveItem(uint32(itemID))
		if resolveErr != nil {
			continue
		}
		itemDocument, docErr := parseDungeonCardPVFDocument(catalog.source, definition.PVFPath)
		if docErr != nil {
			continue
		}
		jobs := itemDocument.Texts("suitable job")
		if len(jobs) == 0 {
			continue
		}
		first, ok := jobCodes[strings.ToLower(strings.TrimSpace(jobs[0]))]
		if !ok {
			continue
		}
		if first != values[offset+10].Int {
			t.Errorf("commodity=%d item=%d row job=%d want first suitable job code=%d jobs=%v",
				values[offset].Int, itemID, values[offset+10].Int, first, jobs)
		}
		covered++
	}
	t.Logf("[package] rows=%d multi_suitable_job_rows=%d verified=%d derived_job_codes=%d",
		rows, multiJobRows, covered, len(jobCodes))
	if len(jobCodes) < 12 {
		t.Fatalf("derived job codes=%d want the real catalog job table", len(jobCodes))
	}
}

func sortedStackItemIDs(stacks map[uint32]dnfrepo.ItemStack) []uint32 {
	out := make([]uint32, 0, len(stacks))
	for itemID := range stacks {
		out = append(out, itemID)
	}
	sortUint32s(out)
	return out
}

// TestRealPVFFestivalCeraPackageAvatarsSpanTwoJobsButAreGrantedExactlyAsChosen
// 记录并钉住"选择职业"的现实：2017/2018 节日套的装扮本体是**两个职业共用**的
// （例：鬼剑士 + 黑暗武士，魔法师 + 缔造者），所以客户端开包对话框里的"职业"
// 一项必然来自装扮自己的 [usable job]（服务端不下发任何职业清单），而服务端在
// 下发时**只认客户端选中的 item id 与属性字节**，不做任何按职业的二次映射。
func TestRealPVFFestivalCeraPackageAvatarsSpanTwoJobsButAreGrantedExactlyAsChosen(t *testing.T) {
	pvfPath := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if pvfPath == "" {
		t.Skip("set DNFBRIDGE_REAL_PVF_SMOKE to inspect the real festival package job spread")
	}
	archive, err := platformpvf.LoadArchive(platformpvf.Options{Path: pvfPath})
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := newPVFDungeonDropCatalog(archive)
	if err != nil {
		t.Fatal(err)
	}
	definition, err := resolveCurrentCeraPackageDefinition(catalog, 490701336)
	if err != nil {
		t.Fatal(err)
	}
	sharedByTwoJobs := 0
	for _, avatarID := range definition.AvatarItemIDs {
		avatarDefinition, resolveErr := catalog.ResolveItem(avatarID)
		if resolveErr != nil {
			t.Fatalf("resolve avatar=%d: %v", avatarID, resolveErr)
		}
		document, docErr := parseDungeonCardPVFDocument(catalog.source, avatarDefinition.PVFPath)
		if docErr != nil {
			t.Fatalf("read avatar=%d: %v", avatarID, docErr)
		}
		jobs := document.Texts("usable job")
		if len(jobs) == 0 {
			t.Fatalf("avatar=%d has no [usable job]", avatarID)
		}
		if len(jobs) > 1 {
			sharedByTwoJobs++
		}
		if strings.ToLower(strings.TrimSpace(jobs[0])) != "[swordman]" {
			t.Fatalf("avatar=%d first usable job=%v want [swordman]", avatarID, jobs)
		}
	}
	if sharedByTwoJobs == 0 {
		t.Fatal("real festival package has no multi-job avatar; the 选择职业 premise changed")
	}
	t.Logf("festival package=%d avatars=%d shared_by_two_jobs=%d", definition.Source.ItemID, len(definition.AvatarItemIDs), sharedByTwoJobs)

	// [package] 商城行只有**一个**职业码字段，而 pvfshopgift 取的是 [suitable job] 的
	// 第一项：两个职业共用的礼包因此只带第一个职业码。这里把该事实钉住，避免有人
	// 把它当成"服务端按职业过滤了选项"的证据。
	document, err := parseDungeonCardPVFDocument(catalog.source, definition.Source.PVFPath)
	if err != nil {
		t.Fatal(err)
	}
	suitable := document.Texts("suitable job")
	if len(suitable) < 2 {
		t.Fatalf("package suitable jobs=%v want the 鬼剑士/黑暗武士 pair", suitable)
	}
	t.Logf("package suitable_jobs=%v (shop row carries only the first one)", suitable)
}

func sortUint32s(values []uint32) {
	for i := 1; i < len(values); i++ {
		for j := i; j > 0 && values[j] < values[j-1]; j-- {
			values[j], values[j-1] = values[j-1], values[j]
		}
	}
}
