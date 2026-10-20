package dnfbridge

import (
	"context"
	"encoding/binary"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	dnfdrops "longheng.io/server/internal/modules/dnf/drop"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/worldmap"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

// 本文件钉住「深渊保底必掉史诗碎片」（`dungeon_hell_party_epic_fragment.go`）：
//
//	触发点 = 击杀**最后一波**的组内最后一只（波次表 `Order` 最大的那一波）；
//	那一刻同时发生两件事：**概率那一支**（既有 `RewardRollCount` 投骰，原样保留）
//	与**保底那一支**（无条件 1..N 个史诗碎片，数量按副本难度取）。
//
// fixture 与 `dungeon_hell_party_wave_test.go` / `dungeon_hell_party_reward_test.go`
// 同一份真档形状：map 60083 的 order 1 = 组 125（1 只 1050）、order 2 = 组 211
// （3 只 65745/65746/65747）⇒ **最后一波 = order 2 / 组 211**，
// 保底落在 `last[2]`（65747）死亡那一刻。
//
// 三条独立的不变量（都不依赖概率那一支的结果）：
//  1. 最后一波最后一只死 ⇒ 一定有碎片（默认档）；
//  2. 概率那一支**全 miss**（用例自选的种子）⇒ 碎片照样有；
//  3. 概率那一支**选不出任何装备**（用例把唯一装备挪出等级区间）⇒ 碎片照样有。
//
// 碎片落地的断言**不走 mock**：读的是真实 op38 body 的字节偏移、走的是既有
// `handleCurrentDungeonPickup`（op43 入口）、账本读的是真实 `dnfrepo.Account`。
//
// ⚠ 本文件**不**对真档的"概率是多少"或"装备池有没有候选"下任何结论——
// 那两条旧口径已被推翻（见 `dungeon_hell_party_epic_fragment.go` 文件头）。
// 用例里的"全 miss / 空区间"都是**fixture 条件**，只为证明保底与概率两支互不依赖。

const (
	// abyssEpicFragmentTestAccountID 与波次 fixture 的角色 99 的账号一致。
	abyssEpicFragmentTestAccountID = "account-1"
	// 三个碎片 id 取自真档 `etc/epicpieceinfo.etc [piece list]` 的 id 形态
	// （10300000..10304121 区间），fixture 里只放三行二元组。
	abyssEpicFragmentTestPieceA = uint32(10300783)
	abyssEpicFragmentTestPieceB = uint32(10300784)
	abyssEpicFragmentTestPieceC = uint32(10300785)
	// abyssEpicFragmentTestDefaultAtDifficultyOne 是**内置默认表**在副本难度 1 上的保底数量
	// = 厂商 `ResolveRollCount(dungeonDifficulty=1, hellDifficulty=1)` = dungeonCount 6 + hellCount 1 = 7。
	abyssEpicFragmentTestDefaultAtDifficultyOne = 7
	// abyssEpicFragmentTestOutputLevel 是 fixture 三件成品装备的 `[minimum level]`：
	// 落在生效带 `[85,90]` 内，所以默认档下三件碎片都是合法候选
	// （"把 lv80 挡在门外"那条判据由 `dungeon_hell_party_epic_fragment_level_test.go` 单独钉）。
	abyssEpicFragmentTestOutputLevel = 85
)

var abyssEpicFragmentTestPieces = []uint32{
	abyssEpicFragmentTestPieceA,
	abyssEpicFragmentTestPieceB,
	abyssEpicFragmentTestPieceC,
}

// bridgeAbyssEpicFragmentPVF 在"概率那一支的完整 fixture"之上补**史诗碎片本体**：
// `stackable/stackable.lst` 的三行 + 三个 `.stk` + `etc/epicpieceinfo.etc [piece list]`
// + **三件成品装备**（`equipment/equipment.lst` + `.equ`）。
// 前两者是 `pvfDungeonDropCatalog.ResolveItem` 解析地面掉落所必需的，
// `[piece list]` 是碎片身份的唯一真源（与 `epic_piece_book.go` 同一份目录），
// 而成品装备的 `[minimum level]` 是 `DNFBRIDGE_ABYSS_ITEM_LEVEL_FLOOR` 的判据
// （`dungeon_abyss_item_level_floor.go`：碎片的等级 = 成品装备的使用等级）。
//
// 三件成品装备的 `[creation rate] 0` ⇒ 它们**不进**深渊 `.equ` 奖励池
// （`EquipmentDropPoolProvider.cs:101` 的门槛），概率那一支因此逐字节不变。
func bridgeAbyssEpicFragmentPVF() bridgePVFSource {
	source := bridgeHellPartyRewardPVF()
	stackables := make([]string, 0, len(abyssEpicFragmentTestPieces))
	pairs := make([]string, 0, len(abyssEpicFragmentTestPieces))
	equipmentList := strings.TrimRight(source[dungeonDropEquipmentList], "\n")
	outputs := make([]string, 0, len(abyssEpicFragmentTestPieces))
	for index, piece := range abyssEpicFragmentTestPieces {
		id := strconv.FormatUint(uint64(piece), 10)
		stackables = append(stackables, id+" `epicPiece/"+id+".stk`")
		source["stackable/epicPiece/"+id+".stk"] = "[name]\n`abyss test epic piece " + id + "`\n" +
			"[explain]\n`史诗装备碎片， 用于制作相应装备的材料。`\n" +
			"[rarity]\n4\n[stackable type]\n`[material]`\n" +
			"[stack limit]\n9999\n[attach type]\n`[trade]`\n[price]\n1000\n"
		// 成品装备 id 随便取一个稳定的 45xxxx 形态：本用例只用到"碎片 id 能对上目录、
		// 且成品装备的 `[minimum level]` 落在生效带内"这两件事。
		outputID := strconv.Itoa(450100 + index)
		pairs = append(pairs, outputID+" "+id)
		relative := "piece/" + outputID + ".equ"
		outputs = append(outputs, outputID+" `"+relative+"`")
		source[hellPartyRewardEquipmentListRoot+"/"+relative] =
			"[name]\n`abyss test epic piece output " + outputID + "`\n" +
				"[grade]\n90\n[rarity]\n4\n[creation rate]\n0\n" +
				"[minimum level]\n" + strconv.Itoa(abyssEpicFragmentTestOutputLevel) + "\n" +
				"[attach type]\n`[trade]`\n"
	}
	source[dungeonDropEquipmentList] = equipmentList + "\n" + strings.Join(outputs, "\n") + "\n"
	source[dungeonDropStackableList] = strings.Join(stackables, "\n") + "\n"
	source[currentEpicPieceInfoPath] = "[equipment piece drop info]\n[piece list]\n" +
		strings.Join(pairs, " ") + "\n[/piece list]\n[/equipment piece drop info]\n"
	return source
}

// bridgeAbyssEpicFragmentNoCandidatePVF 把唯一那件装备挪到**等级区间之外**
// （区间 = `[basis - ValueA, basis + ValueA)` = `[89, 91)`），于是概率那一支
// 即使 8 次全过也选不出装备。`.equ` 与 `ItemDictionary.etc` **两处都改**，
// 这样不论池子来自哪一份装载口径，区间里都没有候选。
func bridgeAbyssEpicFragmentNoCandidatePVF() bridgePVFSource {
	source := bridgeAbyssEpicFragmentPVF()
	source["equipment/weapon/test_drop.equ"] = strings.Replace(
		source["equipment/weapon/test_drop.equ"],
		"[grade]\n"+strconv.FormatInt(hellPartyRewardTestBasisLevel, 10)+"\n",
		"[grade]\n1\n", 1,
	)
	source["Etc/ItemDictionary/ItemDictionary.etc"] =
		strconv.FormatUint(uint64(hellPartyRewardTestItem), 10) + " " +
			strconv.FormatUint(uint64(hellPartyRewardTestItemRarity), 10) + " 1 0 10001 100 `abyss test equipment`\n"
	return source
}

// prepareAbyssEpicFragmentRuntime 复用既有进图链路，额外建好"捡东西要能落库"所需的
// 空背包与**账号行**（史诗碎片进的是账号级图鉴账本，账号行缺失即入账失败）。
func prepareAbyssEpicFragmentRuntime(
	t *testing.T,
	source bridgePVFSource,
) (*Service, *gameSession, *bufferConn, *runtimeDungeonState, worldmap.DungeonRoomScene, dnfrepo.Group) {
	t.Helper()
	service, session, conn, runtime, scene := prepareHellPartyDungeonRuntimeForEntryTest(t, source)
	repositories, ok := service.repositoryProvider()
	if !ok {
		t.Fatal("memory repository group is missing")
	}
	if err := repositories.Inventory.Save(context.Background(), dnfrepo.InventoryRecord{
		CharacterID: "99",
		Slots:       map[string]dnfrepo.ItemStack{},
	}); err != nil {
		t.Fatal(err)
	}
	if err := repositories.Account.Save(context.Background(), dnfrepo.AccountRecord{
		AccountID: abyssEpicFragmentTestAccountID,
		Metadata:  map[string]string{"unrelated": "keep"},
		UpdatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	if !runtime.HellRoomSet || len(runtime.HellPartyWaves) != 2 {
		t.Fatalf("hell party runtime not frozen: set=%t waves=%+v", runtime.HellRoomSet, runtime.HellPartyWaves)
	}
	if count, err := runtime.Room.AnnounceAllActors(runtime.Session); err != nil {
		t.Fatalf("announce hell party actors: %v count=%d", err, count)
	}
	return service, session, conn, runtime, scene, repositories
}

// abyssEpicFragmentDropRow 是一条 op38 掉落行（只取本用例要断言的三个字段）。
type abyssEpicFragmentDropRow struct {
	ObjectKey uint32
	ItemID    uint32
	Amount    uint32
}

// abyssEpicFragmentDropRows 按**真实 op38 body 的字节偏移**解出掉落行：
// `u16 target key | u8 count | count × (u32 scene object key | u16 slot | u32 item | u32 amount | …)`。
func abyssEpicFragmentDropRows(t *testing.T, body []byte) []abyssEpicFragmentDropRow {
	t.Helper()
	if len(body) < currentDungeonZeroDropDeathBodySize {
		t.Fatalf("op38 body too short: %x", body)
	}
	count := int(body[2])
	if len(body) != currentDungeonZeroDropDeathBodySize+count*currentDungeonDeathDropWireSize {
		t.Fatalf("op38 body len=%d count=%d body=%x", len(body), count, body)
	}
	rows := make([]abyssEpicFragmentDropRow, 0, count)
	for index := 0; index < count; index++ {
		offset := 3 + index*currentDungeonDeathDropWireSize
		rows = append(rows, abyssEpicFragmentDropRow{
			ObjectKey: binary.LittleEndian.Uint32(body[offset : offset+4]),
			ItemID:    binary.LittleEndian.Uint32(body[offset+6 : offset+10]),
			Amount:    binary.LittleEndian.Uint32(body[offset+10 : offset+14]),
		})
	}
	return rows
}

// abyssEpicFragmentDeath 打死一只波次怪并返回这一条 op38。
func abyssEpicFragmentDeath(
	t *testing.T,
	service *Service,
	session *gameSession,
	conn *bufferConn,
	objectKey uint32,
) []abyssEpicFragmentDropRow {
	t.Helper()
	conn.write.Reset()
	if err := service.handleDungeonMonsterDeath(session, hellPartyRewardDeathBody(objectKey)); err != nil {
		t.Fatalf("death key=%d: %v", objectKey, err)
	}
	packet, rest := splitGameServerUpperPacket(t, conn.write.Bytes())
	if len(rest) != 0 {
		t.Fatalf("death key=%d trailing bytes=%x", objectKey, rest)
	}
	return abyssEpicFragmentDropRows(t, packet.Body)
}

// abyssEpicFragmentSplit 把一次 op38 的行分成"概率那一支的装备"与"保底碎片"。
func abyssEpicFragmentSplit(
	t *testing.T,
	rows []abyssEpicFragmentDropRow,
) ([]abyssEpicFragmentDropRow, []abyssEpicFragmentDropRow) {
	t.Helper()
	equipment := make([]abyssEpicFragmentDropRow, 0, len(rows))
	fragments := make([]abyssEpicFragmentDropRow, 0, 4)
	for _, row := range rows {
		if row.ItemID == hellPartyRewardTestItem {
			equipment = append(equipment, row)
			continue
		}
		known := false
		for _, piece := range abyssEpicFragmentTestPieces {
			if piece == row.ItemID {
				known = true
				break
			}
		}
		if !known {
			t.Fatalf("op38 carried an item outside both fixtures: %+v body rows=%+v", row, rows)
		}
		if row.Amount != 1 {
			t.Fatalf("fragment row amount=%d want 1 (row=%+v)", row.Amount, row)
		}
		fragments = append(fragments, row)
	}
	return equipment, fragments
}

// abyssEpicFragmentLedgerTotal 读账号级史诗图鉴账本的碎片总量。
func abyssEpicFragmentLedgerTotal(t *testing.T, repositories dnfrepo.Group) int {
	t.Helper()
	account, found, err := repositories.Account.Load(context.Background(), abyssEpicFragmentTestAccountID)
	if err != nil || !found {
		t.Fatalf("account found=%t err=%v", found, err)
	}
	total := 0
	for _, balance := range dnfrepo.AccountEpicPieceBalances(account) {
		total += int(balance)
	}
	return total
}

// abyssEpicFragmentSetRuleRate 换掉 fixture 的 `[drop prob]`（保底那一支不读它，
// 这里只是为了让"概率那一支"的输入可控）。
func abyssEpicFragmentSetRuleRate(t *testing.T, service *Service, rate int64) {
	t.Helper()
	source := mustBridgeSource(t, service)
	source[dnfdrops.MonsterRulePath] = hellPartyRewardTestRuleText(rate)
	currentDungeonMonsterDropRulesByCatalog.Delete(service.dungeonMonsterTable)
}

// abyssEpicFragmentAllMissSeed 找一个"概率门 8 次全 miss"的种子。
// 复刻的是**概率门本身**（`chanceRoll = lcg.Next(1001)`，`roll > chance` ⇒ miss，
// 一次 miss 恰好消耗一个随机数），所以这个种子是用例**自己验证过**的，不是猜的。
func abyssEpicFragmentAllMissSeed(t *testing.T, chance int, rolls int) uint32 {
	t.Helper()
	for seed := uint32(1); seed < 100000; seed++ {
		rng := newCurrentDungeonDropLCG(seed)
		allMiss := true
		for attempt := 0; attempt < rolls; attempt++ {
			if rng.Next(hellPartyRewardDropChanceDenominator) <= chance {
				allMiss = false
				break
			}
		}
		if allMiss {
			return seed
		}
	}
	t.Fatalf("no all-miss seed found for chance=%d rolls=%d", chance, rolls)
	return 0
}

// TestAbyssEpicFragmentSwitchDefaultsToGuaranteedAndOnlyRollbackValuesDisable 钉住开关语义：
// 默认（空/未设置/任何其它值）= guaranteed；只有 off/legacy/0/false/disabled 回退。
func TestAbyssEpicFragmentSwitchDefaultsToGuaranteedAndOnlyRollbackValuesDisable(t *testing.T) {
	if abyssEpicFragmentEnv != "DNFBRIDGE_ABYSS_EPIC_FRAGMENT" {
		t.Fatalf("env name drifted: %s", abyssEpicFragmentEnv)
	}
	if abyssEpicFragmentAmountsEnv != "DNFBRIDGE_ABYSS_EPIC_FRAGMENT_AMOUNTS" {
		t.Fatalf("env name drifted: %s", abyssEpicFragmentAmountsEnv)
	}
	for _, value := range []string{"", "guaranteed", "on", "1", "true", "yes", "junk", " legacy2 "} {
		mode := readAbyssEpicFragmentMode(value)
		if !mode.guaranteed() || mode.String() != abyssEpicFragmentValueGuaranteed {
			t.Fatalf("value=%q resolved to %s guaranteed=%t", value, mode, mode.guaranteed())
		}
	}
	for _, value := range []string{"off", "OFF", " legacy ", "Legacy", "0", "false", "disabled"} {
		mode := readAbyssEpicFragmentMode(value)
		if mode.guaranteed() || mode.String() != abyssEpicFragmentValueOff {
			t.Fatalf("value=%q resolved to %s guaranteed=%t", value, mode, mode.guaranteed())
		}
	}
	// 零值（探针/测试路径）= 操作者要求的默认档。
	if !abyssEpicFragmentGuaranteed.guaranteed() {
		t.Fatal("zero-value mode must be the operator's default (guaranteed)")
	}
	t.Setenv(abyssEpicFragmentEnv, "legacy")
	if abyssEpicFragmentModeFromEnv().guaranteed() {
		t.Fatal("env rollback value must disable the guarantee")
	}
	t.Setenv(abyssEpicFragmentEnv, "")
	if !abyssEpicFragmentModeFromEnv().guaranteed() {
		t.Fatal("env default must be the guarantee")
	}
}

// TestDungeonHellPartyFinalWaveIsTheGreatestOrder 钉住"最后一波"的判定口径：
// fixture 两波（order 1 = 组 125、order 2 = 组 211）⇒ 最后一波是 order 2。
// 波次为空 / order 非法 ⇒ 判不出来（调用方据此记 `final_wave_unknown`，绝不猜）。
func TestDungeonHellPartyFinalWaveIsTheGreatestOrder(t *testing.T) {
	_, _, _, runtime, _, _ := prepareAbyssEpicFragmentRuntime(t, bridgeAbyssEpicFragmentPVF())
	finalWave, ok := dungeonHellPartyFinalWave(runtime)
	if !ok {
		t.Fatalf("final wave not resolved: waves=%+v", runtime.HellPartyWaves)
	}
	if finalWave.Order != 2 || finalWave.WaveCount != 2 || finalWave.Index != 1 {
		t.Fatalf("final wave=%+v want order=2 waves=2 index=1", finalWave)
	}
	if len(finalWave.GroupIDs) != 1 || finalWave.GroupIDs[0] != hellPartyRewardTestGroupLast {
		t.Fatalf("final wave groups=%v want [%d]", finalWave.GroupIDs, hellPartyRewardTestGroupLast)
	}
	if !finalWave.containsGroup(hellPartyRewardTestGroupLast) || finalWave.containsGroup(hellPartyRewardTestGroupFirst) {
		t.Fatalf("containsGroup wrong: %v", finalWave.GroupIDs)
	}
	if got := finalWave.groupList(); got != strconv.FormatInt(hellPartyRewardTestGroupLast, 10) {
		t.Fatalf("groupList=%q", got)
	}
	if _, ok := dungeonHellPartyFinalWave(&runtimeDungeonState{}); ok {
		t.Fatal("empty wave table must not resolve a final wave")
	}
	onlyInvalid := &runtimeDungeonState{HellPartyWaves: []dungeonHellPartyWave{
		{Order: 0, GroupID: 125, Monsters: []dungeonHellPartyMonster{{Code: 1050}}},
	}}
	if _, ok := dungeonHellPartyFinalWave(onlyInvalid); ok {
		t.Fatal("non-positive orders must not resolve a final wave")
	}
	// order 并列：两边都算最后一波（不静默漏掉），组号按波次表顺序。
	tied := &runtimeDungeonState{HellPartyWaves: []dungeonHellPartyWave{
		{Order: 3, GroupID: 700, Monsters: []dungeonHellPartyMonster{{Code: 1}}},
		{Order: 3, GroupID: 701, Monsters: []dungeonHellPartyMonster{{Code: 2}}},
		{Order: 1, GroupID: 702, Monsters: []dungeonHellPartyMonster{{Code: 3}}},
	}}
	tiedWave, ok := dungeonHellPartyFinalWave(tied)
	if !ok || tiedWave.Order != 3 || len(tiedWave.GroupIDs) != 2 ||
		tiedWave.GroupIDs[0] != 700 || tiedWave.GroupIDs[1] != 701 {
		t.Fatalf("tied final wave=%+v ok=%t", tiedWave, ok)
	}
	// 难度键：真实档位字母（不折成 A/B 两档）。
	for difficulty, want := range map[byte]string{0: "-", 1: "A", 2: "B", 3: "C", 4: "D", 9: "-"} {
		if got := abyssEpicFragmentTierText(difficulty); got != want {
			t.Fatalf("tier of difficulty=%d is %q want %q", difficulty, got, want)
		}
	}
}

// TestAbyssEpicFragmentDefaultAmountsAreTheVendorDifficultyCounts 把**默认数量表**钉死：
// 它就是厂商 `EpicPieceDropService.cs:120-141 ResolveRollCount` 在档位 A（本档恒 A）下的值
// —— `dungeonCount {0:5,1:6,2:8,3:9,4:10}` + `hellCount(tier A)=1`。
//
// 同时也钉住那条**故意的语义差异**：厂商那个数是"投骰次数（可能全 miss）"，
// 本实现把它当"保底数量"（填 7 就一定发 7）。这两件事一起钉，防止后来者
// 把"保底"悄悄改回"最多"。
func TestAbyssEpicFragmentDefaultAmountsAreTheVendorDifficultyCounts(t *testing.T) {
	table, err := readAbyssEpicFragmentAmounts("")
	if err != nil {
		t.Fatalf("builtin default table must load: %v", err)
	}
	vendorRollCount := func(dungeonDifficulty int) int {
		dungeonCount := map[int]int{0: 5, 1: 6, 2: 8, 3: 9, 4: 10}[dungeonDifficulty]
		hellCount := 1 // hellDifficulty == 1（= A/非常困难）
		return hellCount + dungeonCount
	}
	want := map[int]int{0: 6, 1: 7, 2: 9, 3: 10, 4: 11}
	for difficulty, expected := range want {
		if got := vendorRollCount(difficulty); got != expected {
			t.Fatalf("vendor ResolveRollCount(%d, tier A)=%d want %d", difficulty, got, expected)
		}
		amount, key := table.amountFor(difficulty, 'A')
		if amount != expected || key != "dungeon_difficulty:"+strconv.Itoa(difficulty) {
			t.Fatalf("builtin default difficulty=%d amount=%d key=%s want %d (vendor count)",
				difficulty, amount, key, expected)
		}
	}
	if amount, key := table.amountFor(9, 0); amount != 1 || key != "fallback:*" {
		t.Fatalf("unknown difficulty amount=%d key=%s want 1/fallback:*", amount, key)
	}
	// 默认表里每一档都必须 >= 1（保底不可能为 0）。
	for _, difficulty := range []int{0, 1, 2, 3, 4, 5, 9, -1} {
		if amount, _ := table.amountFor(difficulty, 0); amount < 1 {
			t.Fatalf("difficulty=%d resolved to %d", difficulty, amount)
		}
	}
	if table.source != "builtin_default" || table.specText() != "0:6,1:7,2:9,3:10,4:11,*:1" {
		t.Fatalf("builtin default table drifted: source=%q spec=%q", table.source, table.specText())
	}
}

// TestAbyssEpicFragmentAmountsSpecIsParsedAndNeverSilentlyZero 钉住数量表：
// 副本难度 `0..4` 是首选键、档位字母是次选键、`*` 是兜底键；
// **任何非法 spec 都回退内置默认表并报错，绝不静默变成 0**。
func TestAbyssEpicFragmentAmountsSpecIsParsedAndNeverSilentlyZero(t *testing.T) {
	// 空 = 内置默认表（= 厂商按难度计数，见上一条用例）。
	table, err := readAbyssEpicFragmentAmounts("")
	if err != nil {
		t.Fatalf("empty spec must not error: %v", err)
	}
	if table.source != "builtin_default" {
		t.Fatalf("empty spec source=%q", table.source)
	}
	if amount, key := table.amountFor(1, 'A'); amount != 7 || key != "dungeon_difficulty:1" {
		t.Fatalf("default difficulty=1 amount=%d key=%s want 7 (vendor count at tier A)", amount, key)
	}

	// env spec 覆盖（操作者口径：`A:3,B:2,C:1` 这种字母键也支持）。
	env, err := readAbyssEpicFragmentAmounts("A:3,B:2,C:1,D:1")
	if err != nil {
		t.Fatalf("letter spec: %v", err)
	}
	if env.source != "env" {
		t.Fatalf("env spec source=%q", env.source)
	}
	if amount, key := env.amountFor(1, 'A'); amount != 3 || key != "hell_tier:A" {
		t.Fatalf("A:3 → amount=%d key=%s", amount, key)
	}
	// env spec **整份替换**默认表：没有 `*` 且档位对不上 ⇒ 硬回退 1（不是 0）。
	if amount, key := env.amountFor(1, 'Z'); amount != abyssEpicFragmentHardFallbackAmount || key != "hardcoded_fallback" {
		t.Fatalf("unmatched tier amount=%d key=%s want %d/hardcoded_fallback",
			amount, key, abyssEpicFragmentHardFallbackAmount)
	}
	// 副本难度键优先于档位键。
	both, err := readAbyssEpicFragmentAmounts("4:7,A:2")
	if err != nil {
		t.Fatalf("mixed spec: %v", err)
	}
	if amount, key := both.amountFor(4, 'A'); amount != 7 || key != "dungeon_difficulty:4" {
		t.Fatalf("mixed spec amount=%d key=%s want 7/dungeon_difficulty:4", amount, key)
	}
	if _, err := readAbyssEpicFragmentAmounts("0:6,1:7,2:9,3:10,4:11"); err != nil {
		t.Fatalf("vendor-shaped spec: %v", err)
	}
	if _, err := readAbyssEpicFragmentAmounts("0:1; 3:2  4:3"); err != nil {
		t.Fatalf("separator variants: %v", err)
	}

	// 非法 spec：报错 + **回退内置默认表**（绝不是 0）。
	for _, value := range []string{
		"x", "0", "0:", ":1", "0:0", "0:-1", "0:abc", "0:1.5",
		"0:" + strconv.Itoa(abyssEpicFragmentMaxAmount+1),
		"0:1,0:2", "5:1", "A:1,B:2,*,3",
	} {
		bad, badErr := readAbyssEpicFragmentAmounts(value)
		if badErr == nil {
			t.Fatalf("spec %q must be rejected", value)
		}
		if bad.source != "builtin_default" {
			t.Fatalf("spec %q source=%q want builtin_default", value, bad.source)
		}
		amount, key := bad.amountFor(1, 'A')
		if amount < 1 {
			t.Fatalf("spec %q silently became %d (key=%s)", value, amount, key)
		}
		if amount != 7 || key != "dungeon_difficulty:1" {
			t.Fatalf("spec %q fallback amount=%d key=%s want the builtin default row (7)", value, amount, key)
		}
	}

	// env 接线。
	t.Setenv(abyssEpicFragmentAmountsEnv, "3:7")
	envTable, envErr := abyssEpicFragmentAmountsFromEnv()
	if envErr != nil {
		t.Fatalf("env table: %v", envErr)
	}
	if amount, _ := envTable.amountFor(3, 'A'); amount != 7 {
		t.Fatalf("env table amount=%d want 7", amount)
	}
	t.Setenv(abyssEpicFragmentAmountsEnv, "nonsense")
	if _, envErr := abyssEpicFragmentAmountsFromEnv(); envErr == nil {
		t.Fatal("invalid env spec must report an error")
	}
	// 抽碎片的纯函数：目录里只有一个候选时恒定命中，空目录不命中。
	catalog, catalogErr := loadCurrentEpicPieceCatalog(bridgeAbyssEpicFragmentPVF())
	if catalogErr != nil {
		t.Fatalf("catalog: %v", catalogErr)
	}
	if catalog.count() != len(abyssEpicFragmentTestPieces) {
		t.Fatalf("catalog entries=%d want %d", catalog.count(), len(abyssEpicFragmentTestPieces))
	}
	rng := newCurrentDungeonDropLCG(0x12345678)
	seen := map[uint32]bool{}
	for attempt := 0; attempt < 64; attempt++ {
		pieceID, ok := abyssEpicFragmentDrawForTest(rng, catalog)
		if !ok || !catalog.isEpicPiece(pieceID) {
			t.Fatalf("draw#%d piece=%d ok=%t", attempt, pieceID, ok)
		}
		seen[pieceID] = true
	}
	if len(seen) != len(abyssEpicFragmentTestPieces) {
		t.Fatalf("draw never covered the whole catalog: %v", seen)
	}
	if _, ok := abyssEpicFragmentDrawForTest(rng, &currentEpicPieceCatalog{}); ok {
		t.Fatal("an empty catalog must not produce a piece")
	}
	if _, ok := abyssEpicFragmentDrawForTest(nil, catalog); ok {
		t.Fatal("a nil rng must not produce a piece")
	}
}

// TestAbyssEpicFragmentGuaranteeFiresOnTheFinalWaveLastMonsterAndLandsAsGroundDrop 是主用例。
//
// **默认触发单元 = 操作者选定的 `final_wave`**（只结算 Order 最大那一波；
// map 60083 一次进房只结算 1 次 ⇒ 65747 死亡那一刻）：
//
//	组 125（波 1，**不是**最后一波）唯一那只 ⇒ 只有概率那一支 8 条，**0 条碎片**；
//	组 211（波 2 = 最后一波）前两只 ⇒ 零掉落（不是组最后一只）；
//	组 211 最后一只 ⇒ 概率那一支 8 条 + **保底 7 条**（默认表在副本难度 1 的值）；
//	全部按既有 op43 拾取 ⇒ 装备进背包、碎片进**账号级史诗图鉴账本**（一件都不进背包）。
func TestAbyssEpicFragmentGuaranteeFiresOnTheFinalWaveLastMonsterAndLandsAsGroundDrop(t *testing.T) {
	service, session, conn, runtime, _, repositories := prepareAbyssEpicFragmentRuntime(t, bridgeAbyssEpicFragmentPVF())
	first, last := hellPartyRewardActorKeys(t, runtime)
	const perEvent = abyssEpicFragmentTestDefaultAtDifficultyOne

	allRows := make([]abyssEpicFragmentDropRow, 0, 16+perEvent)
	equipmentTotal, fragmentTotal := 0, 0

	// 组 125：波 1，**不是**最后一波 ⇒ 出厂默认档下**不发**（这正是操作者选定的口径）。
	firstRows := abyssEpicFragmentDeath(t, service, session, conn, first[0])
	equipment, fragments := abyssEpicFragmentSplit(t, firstRows)
	if len(equipment) != 0 || len(fragments) != 0 {
		t.Fatalf("group %d (wave 1) must not receive the guarantee in the shipped final_wave tier: equipment=%d fragments=%d",
			hellPartyRewardTestGroupFirst, len(equipment), len(fragments))
	}
	allRows = append(allRows, firstRows...)
	equipmentTotal += len(equipment)
	fragmentTotal += len(fragments)

	// 组 211 前两只：不是组最后一只 ⇒ 零掉落（两档共同承认的条件）。
	for index := 0; index < len(last)-1; index++ {
		if rows := abyssEpicFragmentDeath(t, service, session, conn, last[index]); len(rows) != 0 {
			t.Fatalf("non-last monster of group %d (index=%d key=%d) carried %d rows: %+v",
				hellPartyRewardTestGroupLast, index, last[index], len(rows), rows)
		}
	}

	// 最后一波的组内最后一只。
	finalRows := abyssEpicFragmentDeath(t, service, session, conn, last[len(last)-1])
	equipment, fragments = abyssEpicFragmentSplit(t, finalRows)
	if len(equipment) != 8 || len(fragments) != perEvent {
		t.Fatalf("final wave last monster: equipment=%d fragments=%d want 8/%d\nrows=%+v",
			len(equipment), len(fragments), perEvent, finalRows)
	}
	allRows = append(allRows, finalRows...)
	equipmentTotal += len(equipment)
	fragmentTotal += len(fragments)

	for _, row := range allRows {
		if row.ItemID == hellPartyRewardTestItem {
			continue
		}
		if row.Amount != 1 {
			t.Fatalf("guaranteed fragment amount=%d want 1 per drop (row=%+v)", row.Amount, row)
		}
	}
	// 两批掉落行在**同一条** op38 上，且落在既有的地面掉落账本里。
	if runtime.DropOwner == nil || len(runtime.DropOwner.byObjectKey) != equipmentTotal+fragmentTotal {
		t.Fatalf("drop ledger=%+v want %d equipment + %d fragments",
			runtime.DropOwner, equipmentTotal, fragmentTotal)
	}
	for _, row := range allRows {
		drop := runtime.DropOwner.byObjectKey[row.ObjectKey]
		if drop == nil || drop.Item.ItemID != row.ItemID || drop.Status != runtimeDungeonDropAvailable {
			t.Fatalf("row %+v is not registered as an available ground drop: %+v", row, drop)
		}
	}

	// 全部捡起来（真实 op43 入口）。
	for _, row := range allRows {
		if err := service.handleCurrentDungeonPickup(session, currentDungeonPickupTestBody(row.ObjectKey)); err != nil {
			t.Fatalf("pickup key=%d item=%d: %v", row.ObjectKey, row.ItemID, err)
		}
	}
	inventory, found, err := repositories.Inventory.Load(context.Background(), "99")
	if err != nil || !found {
		t.Fatalf("inventory found=%t err=%v", found, err)
	}
	granted := 0
	for _, stack := range inventory.Slots {
		if stack.ItemID != int64(hellPartyRewardTestItem) {
			t.Fatalf("a non-equipment item entered the bag: %+v", stack)
		}
		granted += int(stack.Count)
	}
	if granted != equipmentTotal {
		t.Fatalf("inventory equipment=%d want %d (概率那一支每事件 x8)", granted, equipmentTotal)
	}
	// 碎片**不进背包**，只进账号图鉴账本（既有 epic_piece_* 行为，本改动未改它）。
	if total := abyssEpicFragmentLedgerTotal(t, repositories); total != fragmentTotal {
		t.Fatalf("account epic piece ledger total=%d want %d", total, fragmentTotal)
	}
	// 出厂默认档 = **每房间只结算一次**（操作者选定的"只最后一波"口径）：
	// 这也是线上预期产量（难度 1 ⇒ 7 个碎片/房间）。
	if fragmentTotal != perEvent {
		t.Fatalf("per-room yield changed: %d want exactly one payout of %d", fragmentTotal, perEvent)
	}
	// 拾取回包的"目的槽位"是既有碎片哨兵；掉落被标记为已消耗；余额是绝对值。
	fragmentKey := finalRows[len(finalRows)-1].ObjectKey
	drop := runtime.DropOwner.byObjectKey[fragmentKey]
	if drop == nil || drop.Status != runtimeDungeonDropConsumed ||
		drop.DestinationSlot != currentEpicPiecePickupEffectSlot || drop.EpicPieceBalance < 1 {
		t.Fatalf("fragment ground drop after pickup=%+v", drop)
	}
}

// TestAbyssEpicFragmentTriggerDefaultsToTheOperatorsFinalWaveUnit 钉住**触发单元**开关的
// **出厂默认**，并把"操作者选定口径 vs 厂商口径"的差异写成可执行的对照。
// 默认档**不许**静默漂回厂商口径，所以这里正向断言默认 = final_wave。
//
//	默认（空/未设置/任意其它值）= `final_wave`（**操作者 2026-09-28 选定**）：
//	    map 60083 只结算 1 次（最后一波 = 组 211 的 65747）；
//	`group_last`（**非默认**，= 厂商 `DropService.cs:179` 的 `IsLastGroupMonster`）：
//	    每一组都结算 ⇒ map 60083 结算 2 次（组 125 也在内），产量翻倍。
func TestAbyssEpicFragmentTriggerDefaultsToTheOperatorsFinalWaveUnit(t *testing.T) {
	if abyssEpicFragmentTriggerEnv != "DNFBRIDGE_ABYSS_EPIC_FRAGMENT_TRIGGER" {
		t.Fatalf("env name drifted: %s", abyssEpicFragmentTriggerEnv)
	}
	for _, value := range []string{"", "final_wave", "FINAL_WAVE", "junk", " group_last2 "} {
		trigger := readAbyssEpicFragmentTrigger(value)
		if trigger != abyssEpicFragmentTriggerFinalWave || !trigger.requiresFinalWave() {
			t.Fatalf("value=%q resolved to %s (requiresFinalWave=%t) want the shipped operator default final_wave",
				value, trigger, trigger.requiresFinalWave())
		}
		if trigger.String() != abyssEpicFragmentTriggerValueFinalWave {
			t.Fatalf("value=%q String()=%q", value, trigger.String())
		}
	}
	for _, value := range []string{"group_last", " GROUP_LAST ", "Group_Last"} {
		trigger := readAbyssEpicFragmentTrigger(value)
		if trigger != abyssEpicFragmentTriggerGroupLast || trigger.requiresFinalWave() {
			t.Fatalf("value=%q resolved to %s (requiresFinalWave=%t) want the non-default vendor tier",
				value, trigger, trigger.requiresFinalWave())
		}
	}
	// 零值必须是**出厂默认**（操作者口径），不是厂商口径。
	if !abyssEpicFragmentTriggerFinalWave.requiresFinalWave() {
		t.Fatal("zero-value trigger must require the final wave (shipped default)")
	}
	if abyssEpicFragmentTriggerGroupLast.payoutUnit() == abyssEpicFragmentTriggerFinalWave.payoutUnit() {
		t.Fatal("the two trigger tiers must report different payout units")
	}
	t.Setenv(abyssEpicFragmentTriggerEnv, "nonsense")
	if abyssEpicFragmentTriggerFromEnv() != abyssEpicFragmentTriggerFinalWave {
		t.Fatal("env default must be the operator's final_wave unit")
	}

	// 对照：`group_last`（非默认）档下**波 1（组 125）也发** ⇒ 每房间 2 次、碎片翻倍。
	t.Setenv(abyssEpicFragmentTriggerEnv, abyssEpicFragmentTriggerValueGroupLast)
	service, session, conn, runtime, _, repositories := prepareAbyssEpicFragmentRuntime(t, bridgeAbyssEpicFragmentPVF())
	first, last := hellPartyRewardActorKeys(t, runtime)
	firstRows := abyssEpicFragmentDeath(t, service, session, conn, first[0])
	equipment, fragments := abyssEpicFragmentSplit(t, firstRows)
	if len(equipment) != 0 || len(fragments) != abyssEpicFragmentTestDefaultAtDifficultyOne {
		t.Fatalf("fragment rollback tier pays the earlier wave only in fragments: equipment=%d fragments=%d want 0/%d",
			len(equipment), len(fragments), abyssEpicFragmentTestDefaultAtDifficultyOne)
	}
	for index := 0; index < len(last)-1; index++ {
		if rows := abyssEpicFragmentDeath(t, service, session, conn, last[index]); len(rows) != 0 {
			t.Fatalf("non-last monster %d carried rows=%+v", index, rows)
		}
	}
	finalRows := abyssEpicFragmentDeath(t, service, session, conn, last[len(last)-1])
	_, fragments = abyssEpicFragmentSplit(t, finalRows)
	if len(fragments) != abyssEpicFragmentTestDefaultAtDifficultyOne {
		t.Fatalf("vendor tier: fragments=%d want %d", len(fragments), abyssEpicFragmentTestDefaultAtDifficultyOne)
	}
	for _, rows := range [][]abyssEpicFragmentDropRow{firstRows, finalRows} {
		for _, row := range rows {
			if row.ItemID == hellPartyRewardTestItem {
				continue
			}
			if err := service.handleCurrentDungeonPickup(session, currentDungeonPickupTestBody(row.ObjectKey)); err != nil {
				t.Fatalf("pickup fragment key=%d: %v", row.ObjectKey, err)
			}
		}
	}
	// 厂商档 = 每房间 2 次结算 ⇒ 账本恰好是出厂默认档的两倍。
	if total := abyssEpicFragmentLedgerTotal(t, repositories); total != 2*abyssEpicFragmentTestDefaultAtDifficultyOne {
		t.Fatalf("vendor tier ledger total=%d want %d (two payouts per room)",
			total, 2*abyssEpicFragmentTestDefaultAtDifficultyOne)
	}
}

// TestAbyssEpicFragmentGuaranteeHoldsWhenEveryProbabilityRollMisses 是**独立性**用例之一：
// 用例自选一个"概率门 8 次全 miss"的房间种子（并自己验证过），
// 概率那一支因此一条装备都发不出，**保底碎片照样必须出现**。
func TestAbyssEpicFragmentGuaranteeHoldsWhenEveryProbabilityRollMisses(t *testing.T) {
	service, session, conn, runtime, scene, _ := prepareAbyssEpicFragmentRuntime(t, bridgeAbyssEpicFragmentPVF())
	_, last := hellPartyRewardActorKeys(t, runtime)
	const rate = int64(10)
	abyssEpicFragmentSetRuleRate(t, service, rate)
	chance := int(rate) * hellPartyRewardBaseDropRate / hellPartyRewardChancePercentDivisor

	visit, err := runtime.currentDungeonRoomVisit(scene)
	if err != nil {
		t.Fatal(err)
	}
	seed := abyssEpicFragmentAllMissSeed(t, chance, int(hellPartyRewardTestTierA))
	visit.DropRNG = seed
	// 用例自己再证一次：这个种子的 8 次投骰确实全 miss（不是假设）。
	replay := newCurrentDungeonDropLCG(seed)
	for attempt := 0; attempt < int(hellPartyRewardTestTierA); attempt++ {
		if replay.Next(hellPartyRewardDropChanceDenominator) <= chance {
			t.Fatalf("seed=%d roll#%d did not miss (chance=%d)", seed, attempt+1, chance)
		}
	}

	for index := 0; index < len(last)-1; index++ {
		if rows := abyssEpicFragmentDeath(t, service, session, conn, last[index]); len(rows) != 0 {
			t.Fatalf("non-final monster index=%d carried rows=%+v", index, rows)
		}
	}
	finalRows := abyssEpicFragmentDeath(t, service, session, conn, last[len(last)-1])
	equipment, fragments := abyssEpicFragmentSplit(t, finalRows)
	if len(equipment) != 0 {
		t.Fatalf("rate=%d chance=%d seed=%d: probability branch produced %d equipment rows, want 0",
			rate, chance, seed, len(equipment))
	}
	if len(fragments) != abyssEpicFragmentTestDefaultAtDifficultyOne {
		t.Fatalf("all-8-miss must not affect the guarantee: fragments=%d want %d rows=%+v",
			len(fragments), abyssEpicFragmentTestDefaultAtDifficultyOne, finalRows)
	}
}

// TestAbyssEpicFragmentGuaranteeHoldsWhenTheEquipmentBandHasNoCandidates 是**独立性**用例之二：
// 夹具把唯一那件装备挪到等级区间之外，概率那一支 8 次全过也选不出装备
// （`rolls_passed_no_item`），**保底碎片照样必须出现**。
//
// ⚠ 这是**夹具条件**（区间里没有候选），不是对真档"池子是空的"的断言——
// 那条旧口径已被推翻（真档 `.equ` 池在 [84,96) 有 6,951 件，见实现文件头）。
func TestAbyssEpicFragmentGuaranteeHoldsWhenTheEquipmentBandHasNoCandidates(t *testing.T) {
	service, session, conn, runtime, _, repositories := prepareAbyssEpicFragmentRuntime(t, bridgeAbyssEpicFragmentNoCandidatePVF())
	_, last := hellPartyRewardActorKeys(t, runtime)

	var fragmentRows []abyssEpicFragmentDropRow
	for index, objectKey := range last {
		rows := abyssEpicFragmentDeath(t, service, session, conn, objectKey)
		equipment, fragments := abyssEpicFragmentSplit(t, rows)
		if index < len(last)-1 {
			if len(rows) != 0 {
				t.Fatalf("non-final monster index=%d carried rows=%+v", index, rows)
			}
			continue
		}
		if len(equipment) != 0 {
			t.Fatalf("fixture band has no candidate: probability branch produced %d equipment rows", len(equipment))
		}
		fragmentRows = fragments
	}
	if len(fragmentRows) != abyssEpicFragmentTestDefaultAtDifficultyOne {
		t.Fatalf("no-candidate band must not affect the guarantee: fragments=%d want %d",
			len(fragmentRows), abyssEpicFragmentTestDefaultAtDifficultyOne)
	}
	for _, row := range fragmentRows {
		if err := service.handleCurrentDungeonPickup(session, currentDungeonPickupTestBody(row.ObjectKey)); err != nil {
			t.Fatalf("pickup fragment key=%d: %v", row.ObjectKey, err)
		}
	}
	if total := abyssEpicFragmentLedgerTotal(t, repositories); total != abyssEpicFragmentTestDefaultAtDifficultyOne {
		t.Fatalf("account epic piece ledger total=%d want %d",
			total, abyssEpicFragmentTestDefaultAtDifficultyOne)
	}
}

// TestAbyssEpicFragmentRollbackRestoresTodaysBehaviourByteForByte 是回退钉子：
// `DNFBRIDGE_ABYSS_EPIC_FRAGMENT=legacy`（以及 `off`）下
//   - op38 只有概率那一支的装备行（8/0/0/8），一个碎片行都没有；
//   - 房间掉落 LCG 的终态**逐位等于**"只用概率那一支、从同一个种子重放"的结果
//     ⇒ 保底那一支一个随机数都没抽。
func TestAbyssEpicFragmentRollbackRestoresTodaysBehaviourByteForByte(t *testing.T) {
	for _, value := range []string{abyssEpicFragmentRollbackValue, abyssEpicFragmentValueOff} {
		t.Run(value, func(t *testing.T) {
			t.Setenv(abyssEpicFragmentEnv, value)
			service, session, conn, runtime, scene, _ := prepareAbyssEpicFragmentRuntime(t, bridgeAbyssEpicFragmentPVF())
			first, last := hellPartyRewardActorKeys(t, runtime)
			// ⚠ 每次死亡后 `cacheCurrentDungeonRoom` 都会把房间拜访对象**换成新的**
			// （`dungeon_move_runtime.go:143`，DropRNG 会被原值带过去），
			// 所以这里必须**重新取**，不能缓存指针。
			visitSeed := func() uint32 {
				t.Helper()
				current, err := runtime.currentDungeonRoomVisit(scene)
				if err != nil {
					t.Fatal(err)
				}
				return current.DropRNG
			}
			seedBefore := visitSeed()

			// 独立重放：只用既有的概率那一支（同一函数、同一输入）从 `seedBefore` 推期望终态。
			expected, err := service.resolveHellPartyReward(runtime, scene, hellPartyRewardTestGroupFirst, 1050)
			if err != nil {
				t.Fatal(err)
			}
			expected.rng = newCurrentDungeonDropLCG(seedBefore)
			if _, err := hellPartyRewardRolls(expected); err != nil {
				t.Fatal(err)
			}
			expectedFirst := seedBefore // Equipment now waits for the final wave in every fragment tier.

			firstRows := abyssEpicFragmentDeath(t, service, session, conn, first[0])
			equipment, fragments := abyssEpicFragmentSplit(t, firstRows)
			if len(equipment) != 0 || len(fragments) != 0 {
				t.Fatalf("rollback tier changed wave 1: equipment=%d fragments=%d", len(equipment), len(fragments))
			}
			if got := visitSeed(); got != expectedFirst {
				t.Fatalf("rollback tier consumed the room RNG on wave 1: got=%d want=%d", got, expectedFirst)
			}

			for index := 0; index < len(last)-1; index++ {
				if rows := abyssEpicFragmentDeath(t, service, session, conn, last[index]); len(rows) != 0 {
					t.Fatalf("rollback tier changed non-final monster %d: %+v", index, rows)
				}
			}
			expectedFinal, err := service.resolveHellPartyReward(runtime, scene, hellPartyRewardTestGroupLast, 65747)
			if err != nil {
				t.Fatal(err)
			}
			if got := visitSeed(); got != expectedFirst {
				t.Fatalf("non-final monsters changed the room RNG: got=%d want=%d", got, expectedFirst)
			}
			expectedFinal.rng = newCurrentDungeonDropLCG(expectedFirst)
			if _, err := hellPartyRewardRolls(expectedFinal); err != nil {
				t.Fatal(err)
			}
			expectedLast := expectedFinal.rng.Seed()

			finalRows := abyssEpicFragmentDeath(t, service, session, conn, last[len(last)-1])
			equipment, fragments = abyssEpicFragmentSplit(t, finalRows)
			if len(equipment) != 8 || len(fragments) != 0 {
				t.Fatalf("rollback tier changed the final wave: equipment=%d fragments=%d rows=%+v",
					len(equipment), len(fragments), finalRows)
			}
			if got := visitSeed(); got != expectedLast {
				t.Fatalf("rollback tier consumed the room RNG on the final wave: got=%d want=%d",
					got, expectedLast)
			}
			if runtime.DropOwner == nil || len(runtime.DropOwner.byObjectKey) != 8 {
				t.Fatalf("rollback tier drop ledger=%+v want exactly 8 final-wave equipment drops", runtime.DropOwner)
			}
			for _, drop := range runtime.DropOwner.byObjectKey {
				if drop.Item.ItemID != hellPartyRewardTestItem {
					t.Fatalf("rollback tier registered a non-equipment drop: %+v", drop)
				}
			}
		})
	}
}

// TestAbyssEpicFragmentAmountFollowsTheConfiguredDifficultyTable 钉住"免重编译可调"：
// `DNFBRIDGE_ABYSS_EPIC_FRAGMENT_AMOUNTS=1:5` 下，副本难度 1 的最后一波
// ⇒ op38 带 5 条碎片行，捡完账本总量 = 5；
// 非法 spec 下**照旧发保底**（用内置默认表），并把非法取值记进事件。
func TestAbyssEpicFragmentAmountFollowsTheConfiguredDifficultyTable(t *testing.T) {
	t.Setenv(abyssEpicFragmentAmountsEnv, "1:5")
	service, session, conn, runtime, _, repositories := prepareAbyssEpicFragmentRuntime(t, bridgeAbyssEpicFragmentPVF())
	_, last := hellPartyRewardActorKeys(t, runtime)
	if runtime.Request.Difficulty != 1 {
		t.Fatalf("fixture dungeon difficulty=%d want 1", runtime.Request.Difficulty)
	}
	for index := 0; index < len(last)-1; index++ {
		if rows := abyssEpicFragmentDeath(t, service, session, conn, last[index]); len(rows) != 0 {
			t.Fatalf("non-final monster index=%d carried rows=%+v", index, rows)
		}
	}
	finalRows := abyssEpicFragmentDeath(t, service, session, conn, last[len(last)-1])
	_, fragments := abyssEpicFragmentSplit(t, finalRows)
	if len(fragments) != 5 {
		t.Fatalf("env 1:5 ⇒ fragments=%d want 5 (rows=%+v)", len(fragments), finalRows)
	}
	for _, row := range fragments {
		if err := service.handleCurrentDungeonPickup(session, currentDungeonPickupTestBody(row.ObjectKey)); err != nil {
			t.Fatalf("pickup fragment key=%d: %v", row.ObjectKey, err)
		}
	}
	if total := abyssEpicFragmentLedgerTotal(t, repositories); total != 5 {
		t.Fatalf("account epic piece ledger total=%d want 5", total)
	}

	// 非法 spec：不静默变 0 —— 用内置默认表（难度 1 ⇒ 1 个）继续发。
	t.Setenv(abyssEpicFragmentAmountsEnv, "not-a-spec")
	invalidService, invalidSession, invalidConn, invalidRuntime, _, invalidRepositories :=
		prepareAbyssEpicFragmentRuntime(t, bridgeAbyssEpicFragmentPVF())
	_, invalidLast := hellPartyRewardActorKeys(t, invalidRuntime)
	for index := 0; index < len(invalidLast)-1; index++ {
		abyssEpicFragmentDeath(t, invalidService, invalidSession, invalidConn, invalidLast[index])
	}
	rows := abyssEpicFragmentDeath(t, invalidService, invalidSession, invalidConn, invalidLast[len(invalidLast)-1])
	_, invalidFragments := abyssEpicFragmentSplit(t, rows)
	if len(invalidFragments) != abyssEpicFragmentTestDefaultAtDifficultyOne {
		t.Fatalf("invalid amount spec must fall back to the builtin default (%d), got %d fragments",
			abyssEpicFragmentTestDefaultAtDifficultyOne, len(invalidFragments))
	}
	for _, row := range invalidFragments {
		if err := invalidService.handleCurrentDungeonPickup(invalidSession, currentDungeonPickupTestBody(row.ObjectKey)); err != nil {
			t.Fatalf("pickup fragment key=%d: %v", row.ObjectKey, err)
		}
	}
	if total := abyssEpicFragmentLedgerTotal(t, invalidRepositories); total != abyssEpicFragmentTestDefaultAtDifficultyOne {
		t.Fatalf("invalid amount spec ledger total=%d want %d", total, abyssEpicFragmentTestDefaultAtDifficultyOne)
	}
}

// TestAbyssEpicFragmentScopeIsTheHellPartyWaveOnly 钉住作用域：房图自带的普通怪死亡
// 不进保底盘；房间 actor 表里查不到的 key 也不进。
func TestAbyssEpicFragmentScopeIsTheHellPartyWaveOnly(t *testing.T) {
	service, session, conn, runtime, scene, _ := prepareAbyssEpicFragmentRuntime(t, bridgeAbyssEpicFragmentPVF())
	var ordinary uint32
	for _, monster := range runtime.Room.Snapshot().Monsters {
		ordinary = monster.ObjectKey
		break
	}
	if ordinary == 0 {
		t.Fatal("fixture has no ordinary room monster")
	}
	rows := abyssEpicFragmentDeath(t, service, session, conn, ordinary)
	if len(rows) != 0 {
		t.Fatalf("ordinary room monster death carried rows=%+v", rows)
	}
	if granted := service.grantAbyssEpicFragmentGuarantee(
		session, runtime, scene, ordinary, hellPartyRewardRollModeFromEnv(),
	); len(granted) != 0 {
		t.Fatalf("ordinary room monster got a guarantee: %+v", granted)
	}
	if granted := service.grantAbyssEpicFragmentGuarantee(
		session, runtime, scene, 0xFFFE, hellPartyRewardRollModeFromEnv(),
	); len(granted) != 0 {
		t.Fatalf("unknown object key got a guarantee: %+v", granted)
	}
}

// TestRealPVFAbyssEpicFragmentCatalogExists 是真档只读审计
// （`DNFBRIDGE_REAL_PVF_SMOKE` 守卫）：把保底盘**真正依赖的两个事实**落成日志与断言：
//  1. `etc/epicpieceinfo.etc [piece list]` 在真档上非空，且碎片 id 能与成品装备配对；
//  2. 抽出来的碎片 id 能被**既有地面掉落目录** `ResolveItem` 解析
//     （否则保底碎片发不到地面上）。
//
// 不读真档的概率表、不读装备池——那两条是另一条改动的域。
//
// 运行：`DNFBRIDGE_REAL_PVF_SMOKE=<Script.pvf> go test -count=1 -v -run RealPVFAbyssEpicFragment ./internal/services/dnfbridge`
func TestRealPVFAbyssEpicFragmentCatalogExists(t *testing.T) {
	pvfPath := strings.TrimSpace(os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE"))
	if pvfPath == "" {
		t.Skip("set DNFBRIDGE_REAL_PVF_SMOKE to the runtime Script.pvf")
	}
	archive, err := platformpvf.Open(pvfPath)
	if err != nil {
		t.Fatalf("open real pvf: %v", err)
	}
	monsterCatalog, err := newPVFDungeonMonsterCatalog(archive)
	if err != nil {
		t.Fatalf("load real monster catalog: %v", err)
	}
	catalog, err := currentEpicPieceCatalogFor(monsterCatalog)
	if err != nil {
		t.Fatalf("real %s is unusable for the abyss guarantee: %v", currentEpicPieceInfoPath, err)
	}
	if catalog.count() == 0 {
		t.Fatalf("real %s [piece list] is empty", currentEpicPieceInfoPath)
	}
	minPiece, maxPiece := uint32(0), uint32(0)
	for _, piece := range catalog.pieces {
		if minPiece == 0 || piece < minPiece {
			minPiece = piece
		}
		if piece > maxPiece {
			maxPiece = piece
		}
	}
	t.Logf("real %s: entries=%d piece_id_range=[%d,%d]", currentEpicPieceInfoPath, catalog.count(), minPiece, maxPiece)

	dropCatalog, err := monsterCatalog.DropCatalog()
	if err != nil {
		t.Fatalf("real drop catalog is unusable: %v", err)
	}
	rng := newCurrentDungeonDropLCG(0x12345678)
	resolved, sampled := 0, 0
	for sampled = 0; sampled < 64; sampled++ {
		pieceID, ok := abyssEpicFragmentDrawForTest(rng, catalog)
		if !ok {
			t.Fatalf("draw#%d produced no piece", sampled)
		}
		if output, found := catalog.outputFor(pieceID); !found || output == 0 {
			t.Fatalf("drawn piece %d has no output equipment in [piece list]", pieceID)
		}
		if _, err := dropCatalog.ResolveItem(pieceID); err == nil {
			resolved++
		}
	}
	t.Logf("real ground-drop route resolves %d/%d sampled epic fragments via stackable.lst", resolved, sampled)
	if resolved == 0 {
		t.Fatalf("no sampled epic fragment is resolvable by the ground-drop catalog: the guarantee could not land on the ground")
	}
}
