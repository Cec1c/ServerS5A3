package dnfbridge

import (
	"context"
	"encoding/binary"
	"strconv"
	"strings"
	"testing"

	dnfcharstat "longheng.io/server/internal/modules/dnf/charstat"
	"longheng.io/server/internal/modules/dnf/dnfenum"
	"longheng.io/server/internal/modules/dnf/progression"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/worldmap"
)

// ─────────────────────────────────────────────────────────────────────────────
// P10：大会族（黄龙 120 / 青龙 121）通关经验落库的用例
//
// 覆盖：
//   A. 共用抽取的**行为不变性**：怪物击杀经验那条路径的 gain / 成长契约 /
//      升级 / SP 账本 / 落库结果与抽取前逐字段相同（抽取是纯搬运）。
//   B. 通关经验真的改库：冠军 + 完成 4 轮 ⇒ 角色累计经验 + 真档值、
//      升级与 SP 走**同一份**既有实现。
//   C. 幂等：同一次 run 的重复结算（重复 begin / 重复提交）绝不给第二笔。
//   D. 没有可用 run 标识（`run_token == 0`）⇒ 具名拒绝，不提交。
//   E. 经验为 0 ⇒ 具名零值事件，不写库。
//   F. 默认档（开关未开）⇒ 控制流里根本没有这一跳。
//
// 真档门控用例在 `dungeon_tournament_experience_real_pvf_test.go`。
// ─────────────────────────────────────────────────────────────────────────────

// installCurrentTournamentExperienceTestProgression 给合成 PVF 装上**等级 1..90**
// 完整的升级阈值表与 SP 表。
//
// 为什么必须补全而不是只写两行：`progression.AdvanceSkillPoints` 按
// `SyncedLevel+1 .. newLevel` **逐级**取 SP（`skill_points.go:106-125`），
// `TotalSkillPoints` 也逐级累加（`:41-51`）；缺一级就是硬错误，不是静默 0。
// 真档 120 的冠军经验是 200000，会把合成角色从 20 级推到 21 级以上，
// 所以这里给出到 `currentAdventureCharacterLevelCap`（90）的完整表。
func installCurrentTournamentExperienceTestProgression(source bridgePVFSource) {
	// 阈值：`Tables.ThresholdToNext(level)` 读的是 `thresholds[level-1]`
	// （`experience.go:21-26`，语义 = "从 level 升到 level+1 所需的**累计**经验"），
	// 而 `parseExperienceThresholds` 要求整张表**单调不减**
	// （`table.go:136`，任何下降都是 `ErrTableMalformed`）。
	//
	// 本 fixture 的角色是 20 级 0 经验 ⇒ 起作用的是 `thresholds[19]`。
	// 把它压到 1000、前面全部 1、后面全部 1e9 哨兵：
	// 单调不减成立，且 200000 的经验从 20 级出发**只能跨恰好一级**（20 → 21）——
	// "经验真的落库 + 升级走既有实现"两个断言因此都有确定值。
	thresholds := make([]string, 0, currentAdventureCharacterLevelCap)
	for index := 0; index < currentAdventureCharacterLevelCap; index++ {
		switch {
		case index < 19:
			thresholds = append(thresholds, "1")
		case index == 19:
			thresholds = append(thresholds, "1000")
		default:
			thresholds = append(thresholds, "1000000000")
		}
	}
	source[progression.ExperienceTablePath] = strings.TrimSpace(strings.Join(thresholds, " "))

	var sp strings.Builder
	sp.WriteString("[sp table]\n")
	for level := 1; level <= currentAdventureCharacterLevelCap; level++ {
		award := 0
		switch {
		case level == 20:
			award = 20
		case level >= 21:
			award = 10
		}
		sp.WriteString(strconv.Itoa(level))
		sp.WriteByte(' ')
		sp.WriteString(strconv.Itoa(award))
		sp.WriteByte('\n')
	}
	sp.WriteString("[/sp table]\n[tp table]\n50 1\n[/tp table]\n")
	source[progression.SkillPointTablePath] = sp.String()
}

// installCurrentTournamentExperienceTestPVF 把完整 progression 表装到该 fixture
// 的 monster catalog source 上（与怪物击杀经验用例同一个 source 身份）。
//
// 先借用既有的 `installCurrentDungeonMonsterExperienceTestPVF` 装齐
// monsterexp.tbl / serverparameter / worldmapexppenaltytable / 角色初始属性表，
// **再覆盖**升级阈值表与 SP 表成本用例要的形状 —— 这样"共用既有资源装载"
// 这件事本身也被用例钉住（缺任何一份表都会让提交失败）。
func installCurrentTournamentExperienceTestPVF(t *testing.T, service *Service) {
	t.Helper()
	catalog, err := service.dungeonMonsterCatalog()
	if err != nil {
		t.Fatal(err)
	}
	source, ok := catalog.source.(bridgePVFSource)
	if !ok {
		t.Fatalf("monster catalog source type=%T", catalog.source)
	}
	installCurrentDungeonMonsterExperienceTestPVF(source)
	installCurrentTournamentExperienceTestProgression(source)
	statTable, err := dnfcharstat.Load(context.Background(), source, dnfcharstat.Options{})
	if err != nil {
		t.Fatalf("load character stat test PVF: %v", err)
	}
	service.characterStats = statTable
	currentDungeonMonsterExperienceResourcesByCatalog.Delete(catalog)
}

// installCurrentTournamentCardRewardTestPVF 装上"通关金币基数"那条链需要的 PVF：
// 金币参考表（`Etc/ItemDropInfo_Common.etc` 的 `[gold drop ref table]`）、
// 掉落规则表（`Etc/ItemDropInfo_Monseter.etc`，其中 `[item drop ref table]`
// 必须含**该副本的 basis level** 行）、可堆叠物清单与一个真实存在的堆叠物模板。
//
// 为什么需要它：`beginCurrentTournamentRewardLocked` 走的是厂商
// `TournamentDungeonApplicationService.cs:415-431 ScaleTournamentGold` —— 金币卡的
// 基数来自既有 clear-reward 生成器（`freezeCurrentDungeonCardRewardStateLocked`），
// 而冠军的上排卡**有 100/130 的金币权重**（真档 `[result card]` key=4 是
// `100 0 0`），所以"金币基数解析不出来"时整次抽卡必须失败（这是既有语义，
// 本批不改）。用例因此必须提供一份可解析的真形状 PVF。
func installCurrentTournamentCardRewardTestPVF(t *testing.T, service *Service) {
	t.Helper()
	catalog, err := service.dungeonMonsterCatalog()
	if err != nil {
		t.Fatal(err)
	}
	source, ok := catalog.source.(bridgePVFSource)
	if !ok {
		t.Fatalf("monster catalog source type=%T", catalog.source)
	}
	// fixture 副本 700 的 `[basis level]` 是 20（`bridgeDungeonPVF`），
	// 所以金币参考表与掉落参考表都要有 level 20 的行。
	source[dungeonCardGoldReferencePath] = "[gold drop ref table]\n20 100 0\n"
	source[dungeonCardDropRulePath] = strings.Replace(
		currentDungeonGoldDropTestRuleText(),
		"[item drop ref table]\n1 0 3\n",
		"[item drop ref table]\n1 0 3\n20 0 3\n", 1)
	source[dungeonDropStackableList] = "3329 `stackable/test_tournament_reward.stk`\n"
	source["stackable/test_tournament_reward.stk"] = "[name]\n`Test Tournament Reward`\n" +
		"[stackable type]\n`[material]`\n[stack limit]\n999\n[attach type]\n`[free]`\n"
	source[dungeonDropEquipmentList] = "9001 `weapon/test_tournament_reward.equ`\n"
	source["equipment/weapon/test_tournament_reward.equ"] = "[name]\n`Test Tournament Reward Equipment`\n" +
		"[durability]\n50\n[attach type]\n`[trade]`\n"
	// 空的物品字典 ⇒ 派生目录装载成功但没有任何通用池（本用例只要金币卡，
	// 不需要通用掉落池；厂商这一层也只是"能装载"）。
	source[dungeonDropEquipmentDictionaryPath] = "[item dictionary]\n[/item dictionary]\n"
	currentDungeonGoldReferencesByCatalog.Delete(catalog)
	currentDungeonMonsterDropRulesByCatalog.Delete(catalog)
}

// prepareCurrentTournamentExperienceTest 是通关经验用例的夹具：
// 合成副本 700 + 完整 progression PVF + 20 级角色（0 经验）。
func prepareCurrentTournamentExperienceTest(t *testing.T) (*Service, *gameSession, *runtimeDungeonState, dnfrepo.Group) {
	t.Helper()
	service, session, runtime := newBackToVillageRuntime(t)
	installCurrentTournamentExperienceTestPVF(t, service)
	installCurrentTournamentCardRewardTestPVF(t, service)
	repositories, ok := service.repositoryGroup()
	if !ok {
		t.Fatal("fixture has no repository group")
	}
	character, found, err := repositories.Character.Load(context.Background(), "99")
	if err != nil || !found {
		t.Fatalf("load character found=%t err=%v", found, err)
	}
	character.Level = 20
	character.Job = "0"
	character.Stats["exp"] = 0
	if err := repositories.Character.Save(context.Background(), character); err != nil {
		t.Fatal(err)
	}
	if err := repositories.Skill.Save(context.Background(), dnfrepo.SkillRecord{
		CharacterID: "99",
		Points: dnfrepo.SkillPointState{
			TotalSP: 20, RemainingSP: 20, SyncedLevel: 20,
		},
	}); err != nil {
		t.Fatal(err)
	}
	runtime.Character = dnfrepo.CloneCharacter(character)
	if runtime.lifecycleToken == 0 {
		t.Fatal("fixture run token must be frozen by commitDungeonRuntime")
	}
	return service, session, runtime, repositories
}

// setChampionTournamentForTest 把当前 runtime 置成"冠军已落定、奖励态还没冻结"。
func setChampionTournamentForTest(t *testing.T, runtime *runtimeDungeonState) *tournamentDungeonRuntime {
	t.Helper()
	tournament := championTournamentRuntimeForTest(t, runtime.Session.Snapshot().Scene.Map.Map.ID)
	runtime.Tournament = tournament
	runtime.IgnoreDefaultDungeonClear = true
	return tournament
}

// ── A. 抽取的行为不变性 ───────────────────────────────────────────────────────

// 本用例钉住"怪物击杀经验那条路径在抽取前后逐字段相同"。
//
// 它同时是 P10 的**回归钉**：`commitCurrentDungeonExperience` 是两条路径共用的
// 唯一实现，这里断言的是"共用之后怪物路径的 gain / 成长契约加成 / 升级 /
// SP·TP 账本 / 落库结果 / 运行时内存态"全部保持原值。
func TestCurrentDungeonExperienceCommitExtractionKeepsMonsterAwardResult(t *testing.T) {
	service, runtime, repositories := prepareCurrentDungeonMonsterExperienceTest(t)
	session := &gameSession{selectedCharacterID: 99, dungeon: dungeonSessionState{runtime: runtime}}
	monster := runtime.Room.Snapshot().Monsters[0]
	result, err := service.awardCurrentDungeonMonsterExperience(context.Background(), session, runtime, monster)
	if err != nil {
		t.Fatal(err)
	}
	if result.Award.Gain != 200 || result.GrowthContractBonus != 0 ||
		result.Character.Level != 20 || result.Character.Stats["exp"] != 200 ||
		!result.Applied || result.Idempotent || result.HonorExpertGain != 0 ||
		result.SPGain != 0 || result.TPGain != 0 {
		t.Fatalf("monster award after extraction = %+v", result)
	}
	character, _, err := repositories.Character.Load(context.Background(), "99")
	if err != nil || character.Level != 20 || character.Stats["exp"] != 200 {
		t.Fatalf("persisted monster award character=%+v err=%v", character, err)
	}
	// 成长契约那一档也必须逐字段不变（gain 200 + 20% = 240）。这里只在
	// 纯函数层对拍，因为 `prepareCurrentDungeonMonsterExperienceTest` 的内存仓储
	// 没有账号行（`currentGrowthContractEffect` 对"账号缺失"的既有语义就是
	// 不生效，不改变 gain）。
	service.premiumCatalog = &currentPremiumCatalog{
		effectsByType: map[int64]currentPremiumEffectInfo{
			84: {BonusExperiencePercent: 20},
		},
	}
	if bonus := service.currentGrowthContractBonusExp(context.Background(), "account-1", 200); bonus != 0 {
		t.Fatalf("a missing account must not produce a growth-contract bonus, got %d", bonus)
	}
}

// ── B. 通关经验真的改库 ───────────────────────────────────────────────────────

// 一次完整的"冠军 ⇒ 冻结奖励态 ⇒ 389"（`beginCurrentTournamentRewardLocked`）里，
// 389 里报给客户端的经验必须真的加到角色累计经验上，并且升级 / SP 账本走既有的
// 那一份实现（与怪物击杀经验同一个 `progression.Commit`）。
func TestBeginTournamentRewardCommitsClearExperienceToCharacter(t *testing.T) {
	t.Setenv(dungeonTournamentEnvKey, dungeonTournamentModeVendor)
	service, session, runtime, repositories := prepareCurrentTournamentExperienceTest(t)

	session.dungeon.mu.Lock()
	defer session.dungeon.mu.Unlock()
	setChampionTournamentForTest(t, runtime)
	clearTournamentRoomForTest(t, runtime)

	const wantExperience = 200000 // 真档 120 的 CompletedRounds=4 值
	service.beginCurrentTournamentRewardLocked(session, runtime)

	if runtime.tournamentReward == nil {
		t.Fatal("the champion reward state was not frozen (389 was never built)")
	}
	if runtime.tournamentReward.RewardExperience != wantExperience {
		t.Fatalf("389 reward experience = %d want %d",
			runtime.tournamentReward.RewardExperience, wantExperience)
	}
	receipt := runtime.tournamentExperience
	if !receipt.Attempted || !receipt.Committed {
		t.Fatalf("tournament experience receipt = %+v want attempted+committed", receipt)
	}
	if want := tournamentExperienceReceiptKey(99, runtime.Dungeon.ID, runtime.lifecycleToken, tournamentFinalRound); receipt.Key != want {
		t.Fatalf("receipt key = %q want %q", receipt.Key, want)
	}
	if receipt.Gain != wantExperience {
		t.Fatalf("receipt gain = %d want %d", receipt.Gain, wantExperience)
	}

	// 库里真的变了：累计经验 +200000，并且跨过了 20 → 21 的阈值。
	character, found, err := repositories.Character.Load(context.Background(), "99")
	if err != nil || !found {
		t.Fatalf("load character found=%t err=%v", found, err)
	}
	if character.Stats["exp"] != wantExperience {
		t.Fatalf("persisted exp = %d want %d", character.Stats["exp"], wantExperience)
	}
	if character.Level != 21 {
		t.Fatalf("persisted level = %d want 21 (200000 crosses the 1000 threshold at 20)",
			character.Level)
	}
	skill, found, err := repositories.Skill.Load(context.Background(), "99")
	if err != nil || !found {
		t.Fatalf("load skill found=%t err=%v", found, err)
	}
	if skill.Points.SyncedLevel != 21 || skill.Points.TotalSP != 30 || skill.Points.RemainingSP != 30 {
		t.Fatalf("persisted skill points = %+v want {30 30 21}", skill.Points)
	}
	// 运行时内存态与库态同步（怪物击杀路径同一处赋值）。
	if runtime.Character.Level != 21 || runtime.Character.Stats["exp"] != wantExperience {
		t.Fatalf("runtime character = %+v", runtime.Character)
	}

	// 客户端拿到的是既有的 class0/op37 绝对角色状态体（不是新造的包）：
	// 它必须是本次结算写出的**第一条**下行包。
	connection, ok := session.conn.(*bufferConn)
	if !ok {
		t.Fatalf("fixture connection type=%T", session.conn)
	}
	state, rest := splitGameServerUpperPacket(t, connection.write.Bytes())
	if state.Header.Classification != 0 || state.Header.MsgID != currentDungeonCharacterStateMsgID ||
		len(state.Body) != currentFinishLoadingCharacterStateBodySize {
		t.Fatalf("tournament experience state packet = %+v body_len=%d",
			state.Header, len(state.Body))
	}
	if state.Body[0] != 21 || binary.LittleEndian.Uint32(state.Body[1:5]) != wantExperience {
		t.Fatalf("op37 body does not carry the committed level/exp: %x", state.Body)
	}
	// ── 顺序（本批解环后的族内口径）：经验态 → op115 → **不发 op31** → 389 ──────
	//
	// 族内 0.1.8 实机（`90原神开源0.1.8\...\changlog.md:438-440`）：客户端必须先看到
	// 通关（op115）才进宝箱界面并发 bodyless 457；而**普通结算入口 op31 绝不能发**——
	// 它是"让客户端去构造 op46、打开普通翻牌界面"的那条包，厂商
	// `DungeonClearPresentationPolicy` 对 Tournament 是 `UsesStandardResultProjection=false`。
	// 线上实证（2026-09-27 23:1x，国内服 game-000034，dungeon 123）：发了 op31 ⇒ 客户端
	// 进普通翻牌界面 ⇒ 魂·黄龙大会翻牌闪退 / 魂·青龙大会卡在翻牌界面。
	sawBossDieAck := false
	sawClearReward := false
	for len(rest) != 0 {
		packet, next := splitGameServerUpperPacket(t, rest)
		rest = next
		switch {
		case packet.Header.Classification == 0 &&
			packet.Header.MsgID == uint16(dnfenum.CmdPacketNotifyBossDieCheck):
			sawBossDieAck = true
			continue
		case packet.Header.Classification == 0 &&
			packet.Header.MsgID == currentDungeonSettlementEntryMsgID:
			t.Fatalf("锦标赛绝不能下发普通结算入口 op%d（客户端会据此打开普通翻牌界面）",
				currentDungeonSettlementEntryMsgID)
		case packet.Header.Classification == 0 &&
			packet.Header.MsgID == a21NotiTournamentClearReward:
			if !sawBossDieAck {
				t.Fatalf("389 must follow the clear ack (op115=%t)", sawBossDieAck)
			}
			if len(rest) != 0 {
				t.Fatalf("unexpected trailing packets after 389: %x", rest)
			}
			if got := binary.LittleEndian.Uint32(packet.Body[1:5]); got != wantExperience {
				t.Fatalf("389 reward experience = %d want %d", got, wantExperience)
			}
			sawClearReward = true
			rest = nil
			continue
		default:
			t.Fatalf("unexpected packet between the experience state and 389: %+v", packet.Header)
		}
	}
	if !sawBossDieAck || !sawClearReward {
		t.Fatalf("champion terminal stream incomplete (op115=%t 389=%t)",
			sawBossDieAck, sawClearReward)
	}
	// run 在 389 之前就已经是 Completed，且结算入口（空 op31）已经发出：
	// 这是"冠军即通关、不等选奖"的硬证据。
	committed := runtime.Session.Snapshot()
	if committed.Run.Status != worldmap.DungeonRunCompleted || !runtime.settlementEntrySent {
		t.Fatalf("champion clear must be committed before 389: run=%s settlement_entry=%t",
			committed.Run.Status, runtime.settlementEntrySent)
	}
}

// ── C. 幂等 ──────────────────────────────────────────────────────────────────

// 同一次 run 里重复走结算（客户端重发选奖态请求 / 重连补发 389 / 同一个 op39
// 尾巴再跑一次）**不许**加第二笔经验。
func TestTournamentClearExperienceIsCommittedOnlyOncePerRun(t *testing.T) {
	t.Setenv(dungeonTournamentEnvKey, dungeonTournamentModeVendor)
	service, session, runtime, repositories := prepareCurrentTournamentExperienceTest(t)

	session.dungeon.mu.Lock()
	defer session.dungeon.mu.Unlock()
	setChampionTournamentForTest(t, runtime)

	const wantExperience = 200000
	service.beginCurrentTournamentRewardLocked(session, runtime)
	firstReceipt := runtime.tournamentExperience

	// 第二次 begin（重复结算）：奖励态已在 ⇒ 厂商 `if (runtime.tournamentReward != nil) return`。
	service.beginCurrentTournamentRewardLocked(session, runtime)
	if runtime.tournamentExperience != firstReceipt {
		t.Fatal("the second begin replaced the receipt value")
	}
	// **这一条是本批施工中真实抓到的缺陷的回归钉**：凭据必须留在 runtime 上
	// （曾经因为传了一份局部拷贝，闸门永远不生效、重放会给第二笔经验）。
	if !firstReceipt.Attempted || !firstReceipt.Committed || firstReceipt.Key == "" {
		t.Fatalf("the receipt must stay on the runtime after a commit: %+v", firstReceipt)
	}
	character, _, err := repositories.Character.Load(context.Background(), "99")
	if err != nil {
		t.Fatal(err)
	}
	if character.Stats["exp"] != wantExperience {
		t.Fatalf("a repeated begin changed exp to %d want %d", character.Stats["exp"], wantExperience)
	}
	// 直接重放一次提交入口（这是"重连补发/重复结算"最坏形态）：
	// 凭据已经 Attempted ⇒ 必须拒绝，且**一条写库都不发生**。
	before := character.Stats["exp"]
	committed, _, err := service.commitCurrentTournamentClearRewardExperienceLocked(
		context.Background(), session, runtime,
		wantExperience, tournamentFinalRound,
	)
	if err != nil || committed {
		t.Fatalf("a replayed commit committed=%t err=%v want (false, nil)", committed, err)
	}
	character, _, err = repositories.Character.Load(context.Background(), "99")
	if err != nil {
		t.Fatal(err)
	}
	if character.Stats["exp"] != before {
		t.Fatalf("a replayed commit changed exp %d -> %d", before, character.Stats["exp"])
	}
	// 奖励态被换掉（模拟"重新冻结奖励态"）也不许再加一次：凭据与奖励态分开保存，
	// 正是为了挡住这一条。
	runtime.tournamentReward = tournamentRewardStateForTest(t, true)
	committed, _, err = service.commitCurrentTournamentClearRewardExperienceLocked(
		context.Background(), session, runtime,
		wantExperience, tournamentFinalRound,
	)
	if err != nil || committed {
		t.Fatalf("a commit after the reward state was rebuilt committed=%t err=%v want (false, nil)",
			committed, err)
	}
	character, _, err = repositories.Character.Load(context.Background(), "99")
	if err != nil || character.Stats["exp"] != before {
		t.Fatalf("exp after a rebuilt reward state = %d want %d err=%v", character.Stats["exp"], before, err)
	}
}

// 换一局（新的 run_token）必须换一个新的凭据 —— 否则第二局会被第一局的凭据挡住。
func TestTournamentExperienceReceiptKeyIsPerRun(t *testing.T) {
	base := tournamentExperienceReceiptKey(99, 120, 7, 4)
	if base != tournamentExperienceReceiptKey(99, 120, 7, 4) {
		t.Fatal("the receipt key must be a pure function of its inputs")
	}
	for _, other := range []string{
		tournamentExperienceReceiptKey(99, 120, 8, 4),  // 另一局
		tournamentExperienceReceiptKey(100, 120, 7, 4), // 另一个角色
		tournamentExperienceReceiptKey(99, 121, 7, 4),  // 另一个副本
		tournamentExperienceReceiptKey(99, 120, 7, 3),  // 另一个完成轮数
		tournamentExperienceReceiptKey(99, 120, 0, 4),
	} {
		if other == base {
			t.Fatalf("receipt key %q collided with %q", other, base)
		}
	}
	if !strings.Contains(base, "run_token:7") || !strings.Contains(base, "char:99") ||
		!strings.Contains(base, "dungeon:120") || !strings.Contains(base, "completed_rounds:4") {
		t.Fatalf("receipt key %q does not carry the run/settlement identifiers", base)
	}
}

// ── D. 没有可用 run 标识 ⇒ 明确拒绝 ──────────────────────────────────────────

// `run_token == 0` 时**不许**提交：没有可用的幂等键就是拒绝 + 记错误，
// 而不是"大概齐"再加一笔。
func TestTournamentClearExperienceRejectsWithoutARunIdentity(t *testing.T) {
	t.Setenv(dungeonTournamentEnvKey, dungeonTournamentModeVendor)
	service, session, runtime, repositories := prepareCurrentTournamentExperienceTest(t)

	session.dungeon.mu.Lock()
	defer session.dungeon.mu.Unlock()
	setChampionTournamentForTest(t, runtime)
	runtime.lifecycleToken = 0

	receipt := &runtime.tournamentExperience
	committed, _, err := service.commitCurrentTournamentClearRewardExperienceLocked(
		context.Background(), session, runtime, 200000, tournamentFinalRound,
	)
	if err != nil || committed {
		t.Fatalf("commit without a run token committed=%t err=%v want (false, nil)", committed, err)
	}
	if receipt.Key != "" {
		t.Fatalf("a receipt key was minted without a run token: %q", receipt.Key)
	}
	if receipt.Reason != "tournament_run_token_is_missing" {
		t.Fatalf("receipt reason = %q want tournament_run_token_is_missing", receipt.Reason)
	}
	if receipt.Attempted {
		t.Fatal("a rejected commit without a run token must not burn the one-shot reservation")
	}
	character, _, err := repositories.Character.Load(context.Background(), "99")
	if err != nil {
		t.Fatal(err)
	}
	if character.Stats["exp"] != 0 {
		t.Fatalf("a rejected commit changed exp to %d", character.Stats["exp"])
	}
}

// ── E. 经验为 0 ⇒ 具名零值事件，不写库（也不烧掉一次性预定）────────────────

func TestTournamentClearExperienceZeroIsNamedAndWritesNothing(t *testing.T) {
	t.Setenv(dungeonTournamentEnvKey, dungeonTournamentModeVendor)
	service, session, runtime, repositories := prepareCurrentTournamentExperienceTest(t)

	session.dungeon.mu.Lock()
	defer session.dungeon.mu.Unlock()
	setChampionTournamentForTest(t, runtime)

	receipt := &runtime.tournamentExperience
	committed, _, err := service.commitCurrentTournamentClearRewardExperienceLocked(
		context.Background(), session, runtime, 0, 0,
	)
	if err != nil || committed {
		t.Fatalf("zero-experience commit committed=%t err=%v want (false, nil)", committed, err)
	}
	if receipt.Reason != "tournament_clear_reward_experience_is_zero" || !receipt.Attempted || !receipt.Committed {
		t.Fatalf("zero-experience receipt = %+v", receipt)
	}
	if receipt.Key == "" {
		t.Fatal("the zero-experience receipt must still carry the run key")
	}
	character, _, err := repositories.Character.Load(context.Background(), "99")
	if err != nil {
		t.Fatal(err)
	}
	if character.Stats["exp"] != 0 {
		t.Fatalf("a zero-experience reward changed exp to %d", character.Stats["exp"])
	}
}

// ── F. 默认档 ────────────────────────────────────────────────────────────────

// 默认档（开关未开）：
//   - 真机流程里这一跳根本不可达 —— 默认档没有 `runtime.Tournament`
//     （`prepareTournamentDungeonRun` 在开关关闭时返回 nil），
//     `beginCurrentTournamentRewardLocked` 首行就返回；
//   - 即使把 runtime 硬做成"冠军锦标赛"（本用例为了覆盖提交入口而这么做），
//     提交入口也必须在**第一道闸**（`dungeonTournamentEnabled()`）就被挡回：
//     凭据不变、一条写库都不发生。
func TestTournamentClearExperienceIsInertByDefault(t *testing.T) {
	service, session, runtime, repositories := prepareCurrentTournamentExperienceTest(t)

	session.dungeon.mu.Lock()
	defer session.dungeon.mu.Unlock()
	// 默认档的**正常**形态：`Tournament == nil` ⇒ `beginCurrentTournamentRewardLocked`
	// 不做任何事（不发 389、不建奖励态、不提交经验）。
	service.beginCurrentTournamentRewardLocked(session, runtime)
	if runtime.tournamentReward != nil || runtime.tournamentExperience.Attempted {
		t.Fatalf("default mode did anything: reward=%+v receipt=%+v",
			runtime.tournamentReward, runtime.tournamentExperience)
	}

	// 纵深防御：把 runtime 硬做成冠军锦标赛，提交入口仍必须被开关挡回。
	// 被拒时只允许写"原因"这一项诊断字段 —— Attempted / Committed / Key / Gain
	// 必须原样保持零值（否则默认档就被"预约"了一次）。
	setChampionTournamentForTest(t, runtime)
	receipt := &runtime.tournamentExperience
	committed, _, err := service.commitCurrentTournamentClearRewardExperienceLocked(
		context.Background(), session, runtime, 200000, tournamentFinalRound,
	)
	if err != nil || committed || receipt.Reason != "tournament_mode_is_not_vendor" {
		t.Fatalf("default-mode commit committed=%t err=%v receipt=%+v", committed, err, receipt)
	}
	if receipt.Attempted || receipt.Committed || receipt.Key != "" || receipt.Gain != 0 {
		t.Fatalf("default mode burned the one-shot reservation: %+v", *receipt)
	}
	character, _, err := repositories.Character.Load(context.Background(), "99")
	if err != nil || character.Stats["exp"] != 0 {
		t.Fatalf("default mode changed exp=%d err=%v", character.Stats["exp"], err)
	}
}

// ── G. 提交失败 ⇒ 具名失败事件 + 本局不再重试 ────────────────────────────────

// 数据库层失败（这里用"库内 SP 账本与等级不同步"造出来）必须：
// ① 留下具名失败原因；② 不改变累计经验；③ **烧掉**本局的一次性预定
// （再调一次不会重试 —— 提交失败时我们无法知道事务是否落了一半）。
func TestTournamentClearExperienceFailureIsNamedAndNotRetried(t *testing.T) {
	t.Setenv(dungeonTournamentEnvKey, dungeonTournamentModeVendor)
	service, session, runtime, repositories := prepareCurrentTournamentExperienceTest(t)

	session.dungeon.mu.Lock()
	defer session.dungeon.mu.Unlock()
	setChampionTournamentForTest(t, runtime)
	// 让共享提交实现在 version precondition 之前就失败：SP 账本等级与角色等级不同步。
	if err := repositories.Skill.Save(context.Background(), dnfrepo.SkillRecord{
		CharacterID: "99",
		Points:      dnfrepo.SkillPointState{TotalSP: 20, RemainingSP: 20, SyncedLevel: 19},
	}); err != nil {
		t.Fatal(err)
	}

	receipt := &runtime.tournamentExperience
	committed, _, err := service.commitCurrentTournamentClearRewardExperienceLocked(
		context.Background(), session, runtime, 200000, tournamentFinalRound,
	)
	if committed || err == nil {
		t.Fatalf("a failing commit committed=%t err=%v want a named failure", committed, err)
	}
	if !receipt.Attempted || receipt.Committed || receipt.Reason != "tournament_experience_commit_failed" {
		t.Fatalf("failure receipt = %+v", receipt)
	}
	character, _, loadErr := repositories.Character.Load(context.Background(), "99")
	if loadErr != nil || character.Stats["exp"] != 0 {
		t.Fatalf("a failing commit changed exp=%d err=%v", character.Stats["exp"], loadErr)
	}
	// 本局不再重试：第二次调用被一次性预定挡住，且不产生第二次尝试。
	receiptKey := receipt.Key
	committed, _, err = service.commitCurrentTournamentClearRewardExperienceLocked(
		context.Background(), session, runtime, 200000, tournamentFinalRound,
	)
	if err != nil || committed {
		t.Fatalf("retry after a failure committed=%t err=%v want (false, nil)", committed, err)
	}
	if receipt.Reason != "tournament_experience_was_already_attempted_for_this_run" || receipt.Key != receiptKey {
		t.Fatalf("retry receipt = %+v", receipt)
	}
}

// ── H. 荣誉 Expert 表不参与通关经验（未到等级上限）──────────────────────────

// 角色未到 `currentAdventureCharacterLevelCap` 时，通关经验**不应该**去装载
// 名誉 Expert 表（`currentHonorExpertExperienceGain` 的第一条 early-return），
// 因此 fixture 里没有 honor 表也必须提交成功 —— 这也是"只复用既有实现、
// 不给通关经验新加任何规则"的一个可观察推论。
func TestTournamentClearExperienceDoesNotRequireTheHonorTableBelowCap(t *testing.T) {
	t.Setenv(dungeonTournamentEnvKey, dungeonTournamentModeVendor)
	service, session, runtime, _ := prepareCurrentTournamentExperienceTest(t)
	session.dungeon.mu.Lock()
	defer session.dungeon.mu.Unlock()
	setChampionTournamentForTest(t, runtime)
	clearTournamentRoomForTest(t, runtime)
	service.beginCurrentTournamentRewardLocked(session, runtime)
	if !runtime.tournamentExperience.Committed || runtime.tournamentExperience.Gain != 200000 {
		t.Fatalf("commit below the level cap = %+v", runtime.tournamentExperience)
	}
	if runtime.Character.Level != 21 {
		t.Fatalf("level = %d want 21", runtime.Character.Level)
	}
	if _, ok := runtime.Character.Stats[currentHonorExpertLevelStatKey]; ok {
		t.Fatal("a below-cap tournament award must not write HonorExpert state")
	}
}
