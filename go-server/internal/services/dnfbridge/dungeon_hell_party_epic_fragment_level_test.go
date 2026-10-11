package dnfbridge

import (
	"context"
	"os"
	"sort"
	"strconv"
	"strings"
	"testing"

	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
	"longheng.io/server/internal/modules/dnf/worldmap"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

// 本文件钉住操作者缺陷「**深渊派对掉落了 lv80 的史诗碎片，这边应该没有 lv80 的才对，
// 应该是 85-90**」以及操作者的追问「**会不会掉落 lv80 的史诗（装备）呢**」。
//
// 两条路径的等级约束（真档实测见 `_dist/abyss-round3.md` §4）：
//
//	碎片（`dungeon_hell_party_epic_fragment.go`）：候选 = `etc/epicpieceinfo.etc [piece list]`
//	    全部 966 件，均匀抽、**完全不看等级**；碎片的等级 = 它的成品装备在 `.equ` 里的
//	    `[minimum level]`。真档直方图 = 65:67 70:200 75:195 **80:256** 85:138 90:110。
//	装备（`dungeon_hell_party_reward_pool.go`）：`.equ` 池 + 厂商 grade 闭区间
//	    `[min(minLv,basis), basis+7]`（副本 310 = `[87,97]`）。真档该区间六个 rarity 桶
//	    的候选 `[minimum level]` 实测：r0 68 件全 90、r1 68 件全 90、r2 135 件 85/90、
//	    r3 429 件 85/90、r4（史诗）248 件 = 85:138 90:110 ⇒ **一个都不低于 85**。
//
// 本轮给两条路径都加上同一条按物品等级的显式地板（`dungeon_abyss_item_level_floor.go`，
// 默认 `[85,90]`，`legacy` 逐字节回退）。真档意义上：碎片路径 966 → 248 件候选；
// 装备路径 948 → 948 件（**零行为差**），但"换个低等级副本就会放进 lv80 史诗"
// 那一条被堵死（用例最后一节用真档数据证伪）。

// TestAbyssItemLevelBandSwitchOnlyLegacyRollsBack 钉住档位解析：
// 只有字面量 `legacy`（任一个即可）才回退；未设置/空/非法取值都是默认 `[85,90]`，
// 且原始取值照进事件字段（拼错不会被静默吞掉）。
func TestAbyssItemLevelBandSwitchOnlyLegacyRollsBack(t *testing.T) {
	for _, test := range []struct {
		floor    string
		ceiling  string
		active   bool
		wantLow  int64
		wantHigh int64
	}{
		{floor: "", ceiling: "", active: true, wantLow: 85, wantHigh: 90},
		{floor: "  ", ceiling: "\t", active: true, wantLow: 85, wantHigh: 90},
		{floor: "85", ceiling: "90", active: true, wantLow: 85, wantHigh: 90},
		{floor: "80", ceiling: "95", active: true, wantLow: 80, wantHigh: 95},
		{floor: "90", ceiling: "85", active: true, wantLow: 90, wantHigh: 90},
		{floor: "vendro", ceiling: "", active: true, wantLow: 85, wantHigh: 90},
		{floor: "-1", ceiling: "0", active: true, wantLow: 85, wantHigh: 90},
		{floor: "legacy", ceiling: "", active: false},
		{floor: "", ceiling: "LEGACY", active: false},
		{floor: " legacy ", ceiling: "90", active: false},
	} {
		band := readAbyssItemLevelBand(test.floor, test.ceiling)
		if band.Active != test.active || band.Floor != test.wantLow || band.Ceiling != test.wantHigh {
			t.Fatalf("floor=%q ceiling=%q ⇒ active=%t band=[%d,%d] want active=%t [%d,%d]",
				test.floor, test.ceiling, band.Active, band.Floor, band.Ceiling,
				test.active, test.wantLow, test.wantHigh)
		}
		if test.floor == "" && band.FloorSwitch != "-" {
			t.Fatalf("unset floor must render as '-' got %q", band.FloorSwitch)
		}
		if test.floor == "vendro" && band.FloorSwitch != "vendro" {
			t.Fatalf("a misspelled switch must survive into the event: %q", band.FloorSwitch)
		}
	}
	// 两个消费点语义：碎片 fail-closed（等级未知 ⇒ 拒绝）、装备 fail-open（未知 ⇒ 放行）。
	band := readAbyssItemLevelBand("", "")
	if band.permitsFragmentLevel(0) || !band.permitsFragmentLevel(85) || band.permitsFragmentLevel(80) ||
		band.permitsFragmentLevel(91) {
		t.Fatal("fragment floor must accept only a known level inside [85,90]")
	}
	if !band.permitsEquipmentLevel(0) || !band.permitsEquipmentLevel(85) ||
		band.permitsEquipmentLevel(80) || band.permitsEquipmentLevel(91) {
		t.Fatal("equipment floor must drop only a known level below the floor / above the ceiling")
	}
	legacy := readAbyssItemLevelBand("legacy", "legacy")
	if !legacy.permitsFragmentLevel(80) || !legacy.permitsFragmentLevel(0) || !legacy.permitsEquipmentLevel(80) {
		t.Fatal("the legacy band must admit everything (byte-for-byte today)")
	}
}

// bridgeAbyssEpicFragmentLv80PVF 把 fixture 第三件碎片的成品装备改成 `[minimum level] 80`
// （= 操作者看到的那一类），其余两件仍在带内。
func bridgeAbyssEpicFragmentLv80PVF() bridgePVFSource {
	source := bridgeAbyssEpicFragmentPVF()
	relative := "piece/" + strconv.Itoa(450100+len(abyssEpicFragmentTestPieces)-1) + ".equ"
	body := source[hellPartyRewardEquipmentListRoot+"/"+relative]
	source[hellPartyRewardEquipmentListRoot+"/"+relative] = strings.Replace(
		body,
		"[minimum level]\n"+strconv.Itoa(abyssEpicFragmentTestOutputLevel)+"\n",
		"[minimum level]\n80\n", 1,
	)
	return source
}

// TestAbyssEpicFragmentLevelFloorBlocksTheLv80PieceOnAFixture 是缺陷 (4) 的判别力用例：
// 同一份 fixture、同一个种子下，默认档**一次都抽不到** lv80 那一件，而回退档抽得到
// —— 证明"lv80 消失"是这条地板造成的，不是随机或巧合。
func TestAbyssEpicFragmentLevelFloorBlocksTheLv80PieceOnAFixture(t *testing.T) {
	lv80Piece := abyssEpicFragmentTestPieces[len(abyssEpicFragmentTestPieces)-1]

	run := func(t *testing.T, band abyssItemLevelBand) (map[uint32]int, int) {
		t.Helper()
		service, _, _, _, _, _ := prepareAbyssEpicFragmentRuntime(t, bridgeAbyssEpicFragmentLv80PVF())
		monsterCatalog, err := service.dungeonMonsterCatalog()
		if err != nil {
			t.Fatal(err)
		}
		catalog, err := service.epicPieceCatalog()
		if err != nil {
			t.Fatal(err)
		}
		candidates, index, err := currentAbyssEpicPieceLevelCandidates(monsterCatalog, catalog, band)
		if err != nil {
			t.Fatal(err)
		}
		rng := newCurrentDungeonDropLCG(0x5eed1234)
		seen := map[uint32]int{}
		for attempt := 0; attempt < 512; attempt++ {
			pieceID := candidates[rng.Next(len(candidates))]
			seen[pieceID]++
			if !band.Active {
				continue
			}
			level, found := index.levelOf(pieceID)
			if !found {
				t.Fatalf("an in-band candidate %d has no resolved level", pieceID)
			}
			if !band.admits(level) {
				t.Fatalf("the floor let piece %d (level %d) through", pieceID, level)
			}
		}
		return seen, len(candidates)
	}

	fixedSeen, fixedCandidates := run(t, readAbyssItemLevelBand("", ""))
	if fixedCandidates != len(abyssEpicFragmentTestPieces)-1 {
		t.Fatalf("the shipped band must drop exactly the lv80 piece: candidates=%d want %d",
			fixedCandidates, len(abyssEpicFragmentTestPieces)-1)
	}
	if fixedSeen[lv80Piece] != 0 {
		t.Fatalf("the lv80 piece %d was drawn %d times under the shipped [85,90] floor",
			lv80Piece, fixedSeen[lv80Piece])
	}
	if len(fixedSeen) == 0 {
		t.Fatal("the shipped band produced no draw at all")
	}

	legacySeen, legacyCandidates := run(t, readAbyssItemLevelBand("legacy", "legacy"))
	if legacyCandidates != len(abyssEpicFragmentTestPieces) {
		t.Fatalf("the legacy band must keep every piece: candidates=%d want %d",
			legacyCandidates, len(abyssEpicFragmentTestPieces))
	}
	if legacySeen[lv80Piece] == 0 {
		t.Fatalf("the legacy band must still be able to draw the lv80 piece %d: seen=%v",
			lv80Piece, legacySeen)
	}
}

// ── 真档（`DNFBRIDGE_REAL_PVF_SMOKE` 守卫）────────────────────────────────────

// TestRealPVFAbyssItemLevelFloorsAreRespectedOnBothPaths 是操作者追问的**永久答案**：
// 在**线上那份 Script.pvf** 上，表驱动地证明
//
//	① 碎片路径：默认档的候选**恰好**是 lv85 + lv90 那 248 件，一件 65/70/75/80 都不在；
//	② 装备路径：副本 310 的波段 `[87,97]` 上**六个 rarity 桶的每一件候选**的
//	   `[minimum level]` 都在 `[85,90]` 内（⇒ **lv80 史诗不可能掉落**），
//	   而且史诗（rarity 4）那 248 件**就是** ① 那 248 件的成品装备；
//	③ 反证"光靠波段不够"：把副本的 `[minimum required level]` 换成 86（波段 `[86,97]`），
//	   波段里就**真的出现** lv80 史诗 —— 那一件被本轮的显式地板挡住。
//
// 运行：`DNFBRIDGE_REAL_PVF_SMOKE=<Script.pvf> go test -count=1 -v -run RealPVFAbyssItemLevel ./internal/services/dnfbridge`
func TestRealPVFAbyssItemLevelFloorsAreRespectedOnBothPaths(t *testing.T) {
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
		t.Fatalf("load real piece catalog: %v", err)
	}
	band := readAbyssItemLevelBand("", "")

	// ① 碎片路径。
	candidates, index, err := currentAbyssEpicPieceLevelCandidates(monsterCatalog, catalog, band)
	if err != nil {
		t.Fatalf("build the real piece level index: %v", err)
	}
	if index == nil {
		t.Fatal("the shipped tier must build the piece level index (a neutralised floor returns the whole catalog)")
	}
	if index.Resolved != catalog.count() || index.Unresolved != 0 {
		t.Fatalf("real level index resolved=%d unresolved=%d catalog=%d",
			index.Resolved, index.Unresolved, catalog.count())
	}
	levelHistogram := map[int64]int{}
	for _, piece := range catalog.pieces {
		level, found := index.levelOf(piece)
		if !found {
			t.Fatalf("piece %d has no resolved output level", piece)
		}
		levelHistogram[level]++
	}
	t.Logf("real %s: pieces=%d level_histogram=%s", currentEpicPieceInfoPath, catalog.count(),
		zzLevelHistogramText(levelHistogram))
	if catalog.count() != 966 {
		t.Fatalf("real piece catalog entries=%d want 966", catalog.count())
	}
	if levelHistogram[80] != 256 || levelHistogram[85] != 138 || levelHistogram[90] != 110 {
		t.Fatalf("real level histogram changed: %s", zzLevelHistogramText(levelHistogram))
	}
	if len(candidates) != 248 {
		t.Fatalf("the shipped [85,90] band must keep 248 real pieces, got %d", len(candidates))
	}
	inBand := map[int64]int{}
	for _, piece := range candidates {
		level, _ := index.levelOf(piece)
		if level != 85 && level != 90 {
			t.Fatalf("the shipped band admitted piece %d at level %d", piece, level)
		}
		inBand[level]++
	}
	t.Logf("real fragment candidates in [85,90]: total=%d levels=%s", len(candidates),
		zzLevelHistogramText(inBand))
	rng := newCurrentDungeonDropLCG(0xabcdef01)
	for attempt := 0; attempt < 4096; attempt++ {
		piece := candidates[rng.Next(len(candidates))]
		level, _ := index.levelOf(piece)
		if !band.admits(level) {
			t.Fatalf("draw#%d produced piece %d at level %d outside the band", attempt, piece, level)
		}
	}
	legacyCandidates, _, err := currentAbyssEpicPieceLevelCandidates(
		monsterCatalog, catalog, readAbyssItemLevelBand("legacy", "legacy"),
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(legacyCandidates) != catalog.count() {
		t.Fatalf("the legacy band must keep every piece: %d want %d", len(legacyCandidates), catalog.count())
	}

	// ② 装备路径：真档副本 310 的波段。
	table, err := worldmap.LoadSource(context.Background(), archive, worldmap.Options{})
	if err != nil {
		t.Fatalf("load real worldmap: %v", err)
	}
	dungeon, ok := table.FindDungeon(310)
	if !ok {
		t.Fatal("real dungeon 310 not indexed")
	}
	minimumLevel := dungeon.Metadata.MinimumRequiredLevel.Value
	basisLevel := dungeon.Metadata.BasisLevel.Value
	gradeMin, gradeMax, rangeOK := hellPartyRewardGradeRangeForLevels(minimumLevel, basisLevel)
	if !rangeOK {
		t.Fatal("real dungeon 310 has no usable grade band")
	}
	pool, err := loadHellPartyRewardEquipmentPool(archive)
	if err != nil {
		t.Fatalf("load the real hell party equipment pool: %v", err)
	}
	paths := zzRealEquipmentPaths(t, archive)
	t.Logf("real dungeon 310 [minimum required level]=%d [basis level]=%d ⇒ grade band [%d,%d]",
		minimumLevel, basisLevel, gradeMin, gradeMax)
	fragmentOutputs := map[uint32]struct{}{}
	for _, piece := range candidates {
		if output, found := catalog.outputFor(piece); found {
			fragmentOutputs[output] = struct{}{}
		}
	}
	for _, rarity := range []int64{0, 1, 2, 3, 4, 5} {
		levelHistogram := map[int64]int{}
		belowFloor := 0
		aboveCeiling := 0
		matches := 0
		for _, candidate := range pool.candidates(gradeMin, gradeMax, rarity) {
			definition := zzRealEquipmentDefinition(t, archive, paths, int64(candidate.ItemID))
			levelHistogram[definition.minimumLevel]++
			if definition.minimumLevel < abyssItemLevelFloorDefault {
				belowFloor++
			}
			if definition.minimumLevel > abyssItemLevelCeilingDefault {
				aboveCeiling++
			}
			if _, isFragmentOutput := fragmentOutputs[uint32(candidate.ItemID)]; isFragmentOutput {
				matches++
			}
		}
		t.Logf("real band [%d,%d] rarity=%d candidates=%d levels=%s below_floor=%d above_ceiling=%d fragment_output_matches=%d",
			gradeMin, gradeMax, rarity, zzLevelHistogramTotal(levelHistogram),
			zzLevelHistogramText(levelHistogram), belowFloor, aboveCeiling, matches)
		// **操作者追问的直接答案**：真档副本 310 上，六个 rarity 桶里没有一件候选
		// 的 `[minimum level]` 低于 85 ⇒ lv80 史诗**不可能**掉出来。
		if belowFloor != 0 {
			t.Fatalf("real band [%d,%d] rarity=%d has %d candidate(s) below lv%d: %s",
				gradeMin, gradeMax, rarity, belowFloor, abyssItemLevelFloorDefault,
				zzLevelHistogramText(levelHistogram))
		}
		if rarity == 4 {
			if zzLevelHistogramTotal(levelHistogram) != 248 || matches != 248 {
				t.Fatalf("real epic candidates=%d fragment-output matches=%d want 248/248 (levels=%s)",
					zzLevelHistogramTotal(levelHistogram), matches, zzLevelHistogramText(levelHistogram))
			}
		}
	}

	// ③ 完整池子的等级全貌：**先回答"池子里到底有没有 lv80 史诗"**。
	// 用回退档读（否则地板会把低等级候选挡掉），区间取整个 grade 域。
	t.Setenv(abyssItemLevelFloorEnv, abyssItemLevelLegacyValue)
	t.Setenv(abyssItemLevelCeilingEnv, abyssItemLevelLegacyValue)
	wholeEpicLevels := map[int64]int{}
	for _, rarity := range []int64{0, 1, 2, 3, 4, 5} {
		levelHistogram := map[int64]int{}
		gradeHistogram := map[int64]int{}
		for _, candidate := range pool.candidates(1, hellPartyRewardPoolGradeCeiling, rarity) {
			definition := zzRealEquipmentDefinition(t, archive, paths, int64(candidate.ItemID))
			levelHistogram[definition.minimumLevel]++
			gradeHistogram[definition.grade]++
			if rarity == 4 {
				wholeEpicLevels[definition.minimumLevel]++
			}
		}
		t.Logf("real whole pool rarity=%d candidates=%d levels=%s grades=%s",
			rarity, zzLevelHistogramTotal(levelHistogram),
			zzLevelHistogramText(levelHistogram), zzLevelHistogramText(gradeHistogram))
	}
	// 真档事实（可复核）：**史诗装备池就是碎片那一族**——966 件、等级直方图与
	// `etc/epicpieceinfo.etc [piece list]` 的 966 件逐个对上（65/70/75/80/85/90）。
	// 也就是说"地上掉的史诗"与"保底发的碎片"是同一批物品的两种形态。
	if zzLevelHistogramTotal(wholeEpicLevels) != catalog.count() {
		t.Fatalf("real epic pool=%d want the %d piece-family entries: %s",
			zzLevelHistogramTotal(wholeEpicLevels), catalog.count(), zzLevelHistogramText(wholeEpicLevels))
	}
	if wholeEpicLevels[80] != 256 {
		t.Fatalf("real epic pool must contain the 256 lv80 epics: %s", zzLevelHistogramText(wholeEpicLevels))
	}

	// ④ 反证（真档、真物品）：**光靠波段不够**。把副本的 `[minimum required level]` 换成
	// 86（同一个 basis 90 ⇒ 波段 `[86,97]`）在回退档下确实把 256 件 lv80 史诗放进候选；
	// 同一个波段在默认档下被显式地板清掉。
	looseMin, looseMax, looseOK := hellPartyRewardGradeRangeForLevels(86, basisLevel)
	if !looseOK {
		t.Fatal("the loose band is unusable")
	}
	looseLevels := map[int64]int{}
	for _, candidate := range pool.candidates(looseMin, looseMax, 4) {
		definition := zzRealEquipmentDefinition(t, archive, paths, int64(candidate.ItemID))
		looseLevels[definition.minimumLevel]++
	}
	t.Logf("real provenance: band [%d,%d] rarity=4 WITHOUT the floor = %s",
		looseMin, looseMax, zzLevelHistogramText(looseLevels))
	if looseLevels[80] != 256 {
		t.Fatalf("the band alone must admit all 256 lv80 epics: %s", zzLevelHistogramText(looseLevels))
	}
	t.Setenv(abyssItemLevelFloorEnv, "")
	t.Setenv(abyssItemLevelCeilingEnv, "")
	guardedLevels := map[int64]int{}
	for _, candidate := range pool.candidates(looseMin, looseMax, 4) {
		definition := zzRealEquipmentDefinition(t, archive, paths, int64(candidate.ItemID))
		guardedLevels[definition.minimumLevel]++
	}
	t.Logf("real provenance: the same band WITH the floor = %s", zzLevelHistogramText(guardedLevels))
	if guardedLevels[80] != 0 {
		t.Fatalf("the explicit floor did not remove the real lv80 epics: %s", zzLevelHistogramText(guardedLevels))
	}
	// 真档副本 310 的实际波段 `[87,97]` 上候选数不变（地板在这里是空操作）。
	if got := len(pool.candidates(gradeMin, gradeMax, 4)); got != 248 {
		t.Fatalf("the shipped floor changed the real epic candidate count: %d want 248", got)
	}

	// ⑤ 同一判据的合成钉子（把"已知等级低于地板"的那一件直接塞进桶里）：
	// 默认档选不到它，回退档选得到 —— 这条地板是承重的，与 PVF 里恰好有什么无关。
	synthetic := &hellPartyRewardEquipmentPool{
		Buckets: map[int64][]dungeonDropWeightedItem{
			hellPartyRewardPoolKey(86, 4): {{ItemID: 450100, Weight: 100, MinimumLevel: 80}},
			hellPartyRewardPoolKey(91, 4): {{ItemID: 450101, Weight: 100, MinimumLevel: 85}},
		},
	}
	if selected := synthetic.candidates(86, 97, 4); len(selected) != 1 || selected[0].MinimumLevel != 85 {
		t.Fatalf("the shipped floor must drop the lv80 synthetic candidate: %+v", selected)
	}
	t.Setenv(abyssItemLevelFloorEnv, abyssItemLevelLegacyValue)
	t.Setenv(abyssItemLevelCeilingEnv, abyssItemLevelLegacyValue)
	if selected := synthetic.candidates(86, 97, 4); len(selected) != 2 {
		t.Fatalf("the legacy tier must see both synthetic candidates: %+v", selected)
	}
}

// zzPVFSource / zzEquipmentDefinition 是本文件读真档 `.equ` 用的最小接口与结果
// （`platformpvf.Archive` 满足前者）。
type zzPVFSource interface {
	ReadText(string) (string, error)
}

type zzEquipmentDefinition struct {
	minimumLevel int64
	grade        int64
	rarity       int64
}

func zzRealEquipmentPaths(t *testing.T, archive zzPVFSource) map[int64]string {
	t.Helper()
	text, err := archive.ReadText(dungeonDropEquipmentList)
	if err != nil {
		t.Fatalf("read %s: %v", dungeonDropEquipmentList, err)
	}
	document, err := dnfpvf.Parse(dungeonDropEquipmentList, text)
	if err != nil {
		t.Fatalf("parse %s: %v", dungeonDropEquipmentList, err)
	}
	paths := make(map[int64]string, 1024)
	for _, entry := range dnfpvf.ParseList(document) {
		clean := cleanDungeonDropPath(entry.Path)
		if entry.ID <= 0 || clean == "" {
			continue
		}
		if _, exists := paths[entry.ID]; exists {
			continue
		}
		paths[entry.ID] = clean
	}
	return paths
}

func zzRealEquipmentDefinition(
	t *testing.T,
	archive zzPVFSource,
	paths map[int64]string,
	itemID int64,
) zzEquipmentDefinition {
	t.Helper()
	relative, found := paths[itemID]
	if !found {
		t.Fatalf("real equipment %d is not in %s", itemID, dungeonDropEquipmentList)
	}
	_, body, err := readDungeonDropText(archive, hellPartyRewardEquipmentListRoot, relative)
	if err != nil {
		t.Fatalf("read %s: %v", relative, err)
	}
	parsed, err := dnfpvf.Parse(relative, body)
	if err != nil {
		t.Fatalf("parse %s: %v", relative, err)
	}
	minimumLevel, _ := parsed.Int("minimum level")
	grade, _ := parsed.Int("grade")
	rarity, _ := parsed.Int("rarity")
	return zzEquipmentDefinition{minimumLevel: minimumLevel, grade: grade, rarity: rarity}
}

func zzLevelHistogramText(histogram map[int64]int) string {
	keys := make([]int64, 0, len(histogram))
	for key := range histogram {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i] < keys[j] })
	parts := make([]string, 0, len(keys))
	for _, key := range keys {
		parts = append(parts, strconv.FormatInt(key, 10)+":"+strconv.Itoa(histogram[key]))
	}
	return strings.Join(parts, " ")
}

func zzLevelHistogramTotal(histogram map[int64]int) int {
	total := 0
	for _, count := range histogram {
		total += count
	}
	return total
}
