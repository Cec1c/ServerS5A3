package dnfbridge

import (
	"testing"
)

// 本文件钉住操作者缺陷「**深渊从来不出史诗**」（`_dist/abyss-epic-roll-order.md`）：
// 稀有度闸门与加权取件的**先后次序**。
//
//	改动前（= 线上第 5 次发版，`post_filter`）：先按 hell 表抽稀有度 → 从**那一档**取件 →
//	    再用 `rarity_allowed` 过滤。现场原文（`conn_id=game-000014 … t=2026-09-28T18:49:15Z`）：
//	    `rarity_mode=epic rarity_allowed=4 rarity_rolls=8 rarity_drawn=r0:0,r1:4,r2:4,r3:0,r4:0,r5:0
//	     rarity_selected=r0:0,r1:0,r2:0,r3:0,r4:0,r5:0 rolls_rarity_filtered=8 selected_count=0 granted=0`
//	    —— 史诗档在 A 档阈值里只有 0.7%，所以**深渊一件都不掉**。
//	修复后（默认 `within`）：候选 = 允许档六个桶的**并集**，抽一次加权（与厂商同一次抽签）
//	    ⇒ 抽出来的那一件**一定**属于允许档（本文件钉住的就是这一条）。
//
// ⚠ 本文件只管**次序**（"从哪个桶取件"），不管**落地概率**：
// 操作者 2026-09-29 第 9 条之后，命中之后还有一道史诗窗口
// （`DNFBRIDGE_ABYSS_EPIC_WINDOW`，默认 16000/1e6），所以**默认档不再"每次命中都落地"**。
// 本文件的所有用例因此显式把窗口设成回退字面量 `legacy`（= 每一次命中都落地），
// 这样"次序"这一条不变量仍然被**逐字**钉住；窗口本身的语义由
// `dungeon_hell_party_reward_epic_window_test.go` 负责。
//
// 厂商口径（只读取证）：`HellMonsterDropConfig.cs:63-89` 抽到哪一档就从哪一档取件
// （`:69-79 allowFallback: true`），**整个文件没有任何稀有度过滤** ⇒ 这不是厂商缺陷，
// 是我们在上一轮加闸门时把次序放反了。

// hellPartyRewardRarityOrderRunForTest 用同一份夹具、同一个种子跑一档。
//
// 窗口固定为 `legacy`（= 闸门关掉 = 每一次命中都落地）：本文件测的是**次序**，
// 不是落地概率；把窗口留成默认档会让"每次命中都落地"这条不变量变得不可判。
func hellPartyRewardRarityOrderRunForTest(
	t *testing.T,
	seed uint32,
	raritySwitch string,
	orderSwitch string,
) (hellPartyRewardResult, uint32) {
	t.Helper()
	t.Setenv(hellPartyRewardRarityOrderEnv, orderSwitch)
	t.Setenv(abyssEpicWindowEnv, abyssEpicWindowLegacyValue)
	source := hellPartyRewardTestEquSource(hellPartyRewardRarityBandItems())
	request := hellPartyRewardRarityRequestForTest(t, source, 2048)
	return hellPartyRewardRarityRunForTest(t, request, seed, raritySwitch)
}

// TestHellPartyRewardRarityOrderDefaultPicksInsideTheAllowedSet 是新默认档的主用例：
// **每一次命中都落地一件 rarity 4 物品**（窗口设成回退字面量 `legacy` ⇒ 闸门关掉，
// 这样断言的是**次序**本身），且 `rolls_rarity_filtered` 恒为 0。
func TestHellPartyRewardRarityOrderDefaultPicksInsideTheAllowedSet(t *testing.T) {
	result, _ := hellPartyRewardRarityOrderRunForTest(t, 20261101, "", "")
	if result.RarityOrder != hellPartyRewardRarityOrderWithinValue {
		t.Fatalf("rarity_order=%q want %q", result.RarityOrder, hellPartyRewardRarityOrderWithinValue)
	}
	if result.EpicWindowActive {
		t.Fatalf("this case pins the pick order, so the epic window must be rolled back: %+v", result)
	}
	if result.RarityRolls < 1000 {
		t.Fatalf("the fixture must produce enough hits to be meaningful: hits=%d", result.RarityRolls)
	}
	if result.RarityFiltered != 0 {
		t.Fatalf("the default tier must not filter after the draw: filtered=%d", result.RarityFiltered)
	}
	if len(result.Selected) == 0 {
		t.Fatal("the default tier produced no item at all (this is the deployed defect)")
	}
	// 每次命中（除"该档在波段里没有候选"之外）都必须落地，且落地的一定是 r4。
	if len(result.Selected)+result.RarityBucketEmpty != result.RarityRolls {
		t.Fatalf("hits=%d selected=%d bucket_empty=%d", result.RarityRolls, len(result.Selected), result.RarityBucketEmpty)
	}
	if result.RaritySelected[hellPartyRewardRarityIndex(hellPartyRewardRarityEpic)] != len(result.Selected) {
		t.Fatalf("rarity_selected=%s selected=%d", hellPartyRewardRarityHistogram(result.RaritySelected), len(result.Selected))
	}
	for rarity := 0; rarity < hellPartyRewardRarityBuckets; rarity++ {
		if rarity == int(hellPartyRewardRarityEpic) {
			continue
		}
		if result.RaritySelected[rarity] != 0 {
			t.Fatalf("the default tier selected rarity %d: %s", rarity, hellPartyRewardRarityHistogram(result.RaritySelected))
		}
	}
	// 厂商抽到的稀有度**照抽**（台账仍是真的）：r0/r1/r2 分布仍是 A 档的形状。
	if result.RarityDrawn[1] == 0 && result.RarityDrawn[2] == 0 {
		t.Fatalf("the vendor rarity draw did not run: %s", hellPartyRewardRarityHistogram(result.RarityDrawn))
	}
}

// TestHellPartyRewardRarityOrderRollbackLiteralReproducesTheDeployedFilter 钉住回退字面量：
// `post_filter` 下必须**逐字节复现线上那条**（命中全被过滤、`selected_count=0`）。
// 旧用例里那两条断言（"必须过滤"）搬到这里，强度不变。
func TestHellPartyRewardRarityOrderRollbackLiteralReproducesTheDeployedFilter(t *testing.T) {
	result, seed := hellPartyRewardRarityOrderRunForTest(
		t, 20261101, "", hellPartyRewardRarityOrderPostFilterValue,
	)
	if result.RarityOrder != hellPartyRewardRarityOrderPostFilterValue {
		t.Fatalf("rarity_order=%q want %q", result.RarityOrder, hellPartyRewardRarityOrderPostFilterValue)
	}
	if result.RarityFiltered == 0 {
		t.Fatal("the deployed tier must filter non-epic hits; the gate did not run")
	}
	if result.RarityFiltered+result.RarityBucketEmpty != result.NoDrop {
		t.Fatalf("filtered=%d bucket_empty=%d no_drop=%d", result.RarityFiltered, result.RarityBucketEmpty, result.NoDrop)
	}
	// A 档阈值下 r1/r2 占 99.3% ⇒ 绝大多数命中被挡下（线上原文是"全部 8 抽都被挡"）。
	if len(result.Selected)*10 > result.RarityRolls {
		t.Fatalf("the deployed tier should lose almost every hit: selected=%d hits=%d seed=%d",
			len(result.Selected), result.RarityRolls, seed)
	}
	// 落地的那几件仍然只能是 r4（闸门语义没变，变的是次序）。
	if result.RaritySelected[hellPartyRewardRarityIndex(hellPartyRewardRarityEpic)] != len(result.Selected) {
		t.Fatalf("rarity_selected=%s selected=%d", hellPartyRewardRarityHistogram(result.RaritySelected), len(result.Selected))
	}
}

// TestHellPartyRewardRarityOrderKeepsTheRandomStreamIdentical 是本次改动**必须保住**的不变量：
// 两档的随机数消费序列逐位相同（`rolls`/`misses`/`rarity_rolls`/`rarity_drawn`/LCG 终态），
// 只有"掉什么"不同 —— 否则这个房间之后的所有掉落都会被整体挪位。
func TestHellPartyRewardRarityOrderKeepsTheRandomStreamIdentical(t *testing.T) {
	within, withinSeed := hellPartyRewardRarityOrderRunForTest(t, 20261102, "", "")
	deployed, deployedSeed := hellPartyRewardRarityOrderRunForTest(
		t, 20261102, "", hellPartyRewardRarityOrderPostFilterValue,
	)
	if withinSeed != deployedSeed {
		t.Fatalf("the two tiers consumed different random streams: seeds %d vs %d", withinSeed, deployedSeed)
	}
	if within.Rolls != deployed.Rolls || within.Misses != deployed.Misses ||
		within.RarityRolls != deployed.RarityRolls {
		t.Fatalf("rolls/misses/hits differ: %d/%d/%d vs %d/%d/%d",
			within.Rolls, within.Misses, within.RarityRolls,
			deployed.Rolls, deployed.Misses, deployed.RarityRolls)
	}
	if within.RarityDrawn != deployed.RarityDrawn {
		t.Fatalf("the vendor rarity draw differs: %s vs %s",
			hellPartyRewardRarityHistogram(within.RarityDrawn),
			hellPartyRewardRarityHistogram(deployed.RarityDrawn))
	}
	if len(within.Selected) <= len(deployed.Selected) {
		t.Fatalf("the fix must pay more items than the deployed filter: within=%d post_filter=%d",
			len(within.Selected), len(deployed.Selected))
	}
	// 两档的 `candidates=` 台账口径一致（都是"这一抽取件的候选数"）。
	if within.CandidateCount == 0 {
		t.Fatalf("the default tier must report candidates: %+v", within)
	}
}

// TestHellPartyRewardRarityOrderSwitchOnlyKnownLiteralsRollBack 钉住档位解析：
// 只有 `post_filter` / `legacy` / `off` 才回退，其余（含拼错）都是修复档。
func TestHellPartyRewardRarityOrderSwitchOnlyKnownLiteralsRollBack(t *testing.T) {
	for _, test := range []struct {
		value string
		want  hellPartyRewardRarityOrderMode
	}{
		{value: "", want: hellPartyRewardRarityOrderWithin},
		{value: "   ", want: hellPartyRewardRarityOrderWithin},
		{value: "within", want: hellPartyRewardRarityOrderWithin},
		{value: "WITHIN", want: hellPartyRewardRarityOrderWithin},
		{value: "before", want: hellPartyRewardRarityOrderWithin},
		{value: "postfilter", want: hellPartyRewardRarityOrderWithin},
		{value: "post_filter", want: hellPartyRewardRarityOrderPostFilter},
		{value: " POST_FILTER ", want: hellPartyRewardRarityOrderPostFilter},
		{value: "legacy", want: hellPartyRewardRarityOrderPostFilter},
		{value: "off", want: hellPartyRewardRarityOrderPostFilter},
	} {
		if got := readHellPartyRewardRarityOrderMode(test.value); got != test.want {
			t.Fatalf("value=%q ⇒ %s want %s", test.value, got, test.want)
		}
	}
	if env := hellPartyRewardRarityOrderEnv; env != "DNFBRIDGE_HELL_PARTY_REWARD_RARITY_ORDER" {
		t.Fatalf("env name=%q", env)
	}
}

// TestHellPartyRewardRarityOrderUnionPickIsAWeightedDrawOverTheAllowedSet 是"并集抽取"
// 的**分布证明**（针对"宽允许集下分布会不会塌缩到单档"这条怀疑）：
//
//	夹具 `hellPartyRewardRarityBandItems` 覆盖 r1/r2/r3/r4（**没有 r0 物品**，
//	见 `dungeon_hell_party_reward_rarity_test.go:71-75`），权重分别 100/100/100/(100+250)。
//	允许集合 = `1,2,3,4` 时：
//	  ① 候选数必须 = 四档候选之和（并集确实被用上，不是只看第一档）；
//	  ② 长跑下**四档必须全部出现过**（分布不塌缩）；
//	  ③ 各档占比必须贴近**权重占比** 100:100:100:350（±0.05）；
//	  ④ 落地物品必须逐件落在允许档的桶里。
func TestHellPartyRewardRarityOrderUnionPickIsAWeightedDrawOverTheAllowedSet(t *testing.T) {
	t.Setenv(hellPartyRewardRarityOrderEnv, "")
	// 窗口 = `legacy`（闸门关掉）：这一条测的是**并集抽取的分布**，不是落地概率；
	// 开着窗口会把样本量从"每次命中"压到 1.6%，占比断言就没有判别力了。
	t.Setenv(abyssEpicWindowEnv, abyssEpicWindowLegacyValue)
	source := hellPartyRewardTestEquSource(hellPartyRewardRarityBandItems())
	request := hellPartyRewardRarityRequestForTest(t, source, 8192)
	result, _ := hellPartyRewardRarityRunForTest(t, request, 20261105, "1,2,3,4")

	allowed := []int64{1, 2, 3, 4}
	wantCandidates := 0
	allowedIDs := map[uint32]bool{}
	for _, rarity := range allowed {
		ids := hellPartyRewardRarityBucketIDs(t, request, rarity)
		if len(ids) == 0 {
			t.Fatalf("fixture must cover rarity %d", rarity)
		}
		wantCandidates += len(ids)
		for id := range ids {
			allowedIDs[id] = true
		}
	}
	if result.CandidateCount != wantCandidates {
		t.Fatalf("candidates=%d want the union of the allowed buckets=%d (not just one bucket)",
			result.CandidateCount, wantCandidates)
	}
	if len(result.Selected) == 0 {
		t.Fatal("the union pick selected nothing")
	}
	for _, item := range result.Selected {
		if !allowedIDs[item.ItemID] {
			t.Fatalf("item %d is outside the allowed buckets", item.ItemID)
		}
	}
	// ② 不塌缩：四档都出现过。
	distinct := 0
	for _, rarity := range allowed {
		if result.RaritySelected[hellPartyRewardRarityIndex(rarity)] > 0 {
			distinct++
		}
	}
	if distinct != len(allowed) {
		t.Fatalf("the union pick collapsed: %s (want every allowed rarity to appear)",
			hellPartyRewardRarityHistogram(result.RaritySelected))
	}
	// ③ 占比 = 权重占比（r1/r2/r3 各 100，r4 = 100+250 = 350 ⇒ 总 650）。
	wantShares := map[int64]float64{1: 100.0 / 650, 2: 100.0 / 650, 3: 100.0 / 650, 4: 350.0 / 650}
	for _, rarity := range allowed {
		got := float64(result.RaritySelected[hellPartyRewardRarityIndex(rarity)]) / float64(len(result.Selected))
		want := wantShares[rarity]
		if got < want-0.05 || got > want+0.05 {
			t.Fatalf("rarity %d share=%.4f want %.4f±0.05 (distribution=%s)",
				rarity, got, want, hellPartyRewardRarityHistogram(result.RaritySelected))
		}
	}
	// ④ 允许集合里没有候选的那些档（本夹具的 r0/r5）恒为 0。
	if result.RaritySelected[0] != 0 || result.RaritySelected[5] != 0 {
		t.Fatalf("selected a rarity outside the allowed set: %s",
			hellPartyRewardRarityHistogram(result.RaritySelected))
	}
	t.Logf("union pick over [1,2,3,4]: candidates=%d selected=%d distribution=%s",
		result.CandidateCount, len(result.Selected), hellPartyRewardRarityHistogram(result.RaritySelected))
}

// TestHellPartyRewardRarityOrderRejectsNonEpicFromTheAllowedSet 钉住"闸门仍然生效"：
// 允许集合收窄到 `4` 时，抽出来的**每一件都必须来自 r4 桶**（不是"抽到别的档也放行"）；
// 允许集合换成 `0,4` 时并集也必须只由这两档组成（本夹具没有 r0 物品 ⇒ r0 恒 0 是
// **夹具事实**，不是分布塌缩：见上面那条分布证明）。
func TestHellPartyRewardRarityOrderRejectsNonEpicFromTheAllowedSet(t *testing.T) {
	t.Setenv(hellPartyRewardRarityOrderEnv, "")
	// 窗口 = `legacy`（闸门关掉）：这一条测"允许集合真的收窄了吗"，
	// 落地样本必须足够多才判得动（默认窗口下 4096 抽只剩约 60 件）。
	t.Setenv(abyssEpicWindowEnv, abyssEpicWindowLegacyValue)
	source := hellPartyRewardTestEquSource(hellPartyRewardRarityBandItems())
	request := hellPartyRewardRarityRequestForTest(t, source, 4096)
	result, _ := hellPartyRewardRarityRunForTest(t, request, 20261103, "")

	allowed := hellPartyRewardRarityBucketIDs(t, request, 4)
	forbidden := make(map[uint32]bool)
	for _, rarity := range []int64{0, 1, 2, 3} {
		for id := range hellPartyRewardRarityBucketIDs(t, request, rarity) {
			forbidden[id] = true
		}
	}
	if len(allowed) == 0 || len(forbidden) == 0 {
		t.Fatalf("fixture must cover r4 (%d) and lower rarities (%d)", len(allowed), len(forbidden))
	}
	if len(result.Selected) == 0 {
		t.Fatal("no item selected at all")
	}
	for _, item := range result.Selected {
		if forbidden[item.ItemID] {
			t.Fatalf("the allowed set admitted a non-epic item %d", item.ItemID)
		}
		if !allowed[item.ItemID] {
			t.Fatalf("item %d is not in the rarity-4 bucket", item.ItemID)
		}
	}
	// 允许集合扩到 `0,4`：并集的候选数必须 = r0 桶 + r4 桶（夹具里 r0 桶为空 ⇒ 只剩 r4）。
	if empty := hellPartyRewardRarityBucketIDs(t, request, 0); len(empty) != 0 {
		t.Fatalf("this fixture is assumed to have no rarity-0 item, got %d", len(empty))
	}
	wide, _ := hellPartyRewardRarityRunForTest(t, request, 20261103, "0,4")
	if wide.CandidateCount != len(allowed) {
		t.Fatalf("wide candidates=%d want r0(%d)+r4(%d)", wide.CandidateCount,
			len(hellPartyRewardRarityBucketIDs(t, request, 0)), len(allowed))
	}
	if len(wide.Selected) == 0 {
		t.Fatal("the wide allowed set produced no item")
	}
	for _, item := range wide.Selected {
		if !allowed[item.ItemID] {
			t.Fatalf("the wide allowed set produced item %d outside r0/r4", item.ItemID)
		}
	}
}

// 真档上的次序判据写在既有的真档用例里
// （`TestRealPVFHellPartyRewardEpicTierDropsOnlyTheRealRarity4Pool` 末尾：默认档
// `filtered=0` + 每次命中都落地 + `post_filter` 几乎全被过滤 + 两档随机流逐位相同），
// 这样不必重复 50 行真档夹具装配。
