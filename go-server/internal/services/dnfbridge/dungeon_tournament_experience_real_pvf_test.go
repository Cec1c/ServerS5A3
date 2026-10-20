package dnfbridge

import (
	"context"
	"strings"
	"testing"

	"longheng.io/server/internal/modules/dnf/progression"
	"longheng.io/server/internal/modules/dnf/worldmap"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

// ─────────────────────────────────────────────────────────────────────────────
// P10 真档门控 smoke：大会族通关经验的**真实数值**与**真的落库**
//
// 运行方式（必须显式给 PVF 路径，否则 skip）：
//
//	$env:DNFBRIDGE_REAL_PVF_SMOKE='H:\86JP启动器\90CN\地下城与勇士\_dnf90_launcher_backup\20260925-013914\Script.pvf'
//	go test -count=1 -timeout 600s -v -run RealScriptPVFTournamentClearExperience ./internal/services/dnfbridge
//
// 本文件钉住两件事：
//  ① 真档 120/121 的 `[tournament clear reward exp]` 是 **5 个键、按 CompletedRounds
//     索引**，且值就是下面这组确定数字（不是"猜键的语义"）；
//  ② 用真档那个数值走**本仓的**通关经验提交入口，角色累计经验真的加上去了
//     （并且第二次提交被幂等拒绝）。
// ─────────────────────────────────────────────────────────────────────────────

// realTournamentClearRewardExperience 是真档 120/121/122 的
// `[tournament clear reward exp]` 全表（2026-09-27 从
// `_dnf90_launcher_backup\20260925-013914\Script.pvf` 读出，逐键核对）。
//
// 语义确认：`CompletedRounds` 是**已经打完的轮数**（0..4），不是"第几轮"，
// 也不是"剩余轮数" —— 0 轮（第一轮就被淘汰）给 0 经验，打满 4 轮（冠军）
// 给全表最大值。这正是 `TournamentDungeonRuntime.TryApplyActorDeath`（第 4 轮全灭
// ⇒ `_completedRounds = 4`）与 `TryEliminate`（`CompletedRounds = max(0, CurrentRound-1)`）
// 两条路径给出的那个数，也是 `beginCurrentTournamentRewardLocked` 用作
// `definition.RewardExperience[int(snapshot.CompletedRounds)]` 索引键的那个数。
var realTournamentClearRewardExperience = map[int64]map[int]int64{
	120: {0: 0, 1: 30000, 2: 50000, 3: 100000, 4: 200000},
	121: {0: 0, 1: 100000, 2: 200000, 3: 300000, 4: 500000},
	122: {0: 0, 1: 50000, 2: 100000, 3: 200000, 4: 400000},
}

func realTournamentClearExperiencePVFPath() string {
	return realTournamentClosingPVFPath()
}

// 真档证据 ①：`[tournament clear reward exp]` 的键与值。
func TestRealScriptPVFTournamentClearExperienceKeysAreIndexedByCompletedRounds(t *testing.T) {
	pvfPath := realTournamentClearExperiencePVFPath()
	if pvfPath == "" {
		t.Skip("set DNFBRIDGE_REAL_PVF_SMOKE (or DNF_WORLDMAP_REAL_PVF_SMOKE) to run the real closing smoke")
	}
	archive, err := platformpvf.Open(pvfPath)
	if err != nil {
		t.Fatal(err)
	}
	table, err := worldmap.LoadSource(context.Background(), archive, worldmap.Options{})
	if err != nil {
		t.Fatal(err)
	}
	resolver, err := worldmap.NewResolver(table)
	if err != nil {
		t.Fatal(err)
	}
	for _, dungeonID := range []int64{120, 121, 122} {
		dungeon, ok := table.FindDungeon(dungeonID)
		if !ok {
			t.Fatalf("real PVF dungeon %d is missing", dungeonID)
		}
		// 原始行（解析层）：必须恰好 5 行、键 0..4 各一次。
		rows := dungeon.Metadata.TournamentClearExperiences
		if len(rows) != tournamentRoundCount+1 {
			t.Fatalf("dungeon %d `[tournament clear reward exp]` rows=%d want=%d",
				dungeonID, len(rows), tournamentRoundCount+1)
		}
		seen := make(map[int64]bool, len(rows))
		for _, row := range rows {
			if row.CompletedRounds < 0 || row.CompletedRounds > tournamentRoundCount {
				t.Fatalf("dungeon %d reward experience key=%d outside 0..%d",
					dungeonID, row.CompletedRounds, tournamentRoundCount)
			}
			if seen[row.CompletedRounds] {
				t.Fatalf("dungeon %d duplicate reward experience key=%d", dungeonID, row.CompletedRounds)
			}
			seen[row.CompletedRounds] = true
			if want := realTournamentClearRewardExperience[dungeonID][int(row.CompletedRounds)]; row.Experience != want {
				t.Fatalf("dungeon %d CompletedRounds=%d experience=%d want=%d",
					dungeonID, row.CompletedRounds, row.Experience, want)
			}
		}

		// 投影层（`resolveTournamentDungeonDefinition`）必须把同一组键值搬到
		// `definition.RewardExperience`，且不参与准入的 map 语义不能吃掉任何一个键。
		topology, err := worldmap.BuildDungeonLayout(resolver, dungeonID, 0, func(choice worldmap.DungeonMapChoice) (int64, error) {
			return choice.Candidates[0].ID, nil
		})
		if err != nil {
			t.Fatal(err)
		}
		room, ok := topology.Room(*topology.Start)
		if !ok || room.Map == nil {
			t.Fatalf("dungeon %d start room unresolved", dungeonID)
		}
		if dungeonID == 122 {
			// 122 是 `[apc]` 分支（厂商 code 20712 的 AICharacter 等级取不到 ⇒
			// 定义层**硬失败**，本批按任务书不实现 `[apc]`）。这里只核对
			// 原始行，不要求投影成功 —— 但失败原因必须具名，不许静默降级。
			_, reason, ok := resolveTournamentDungeonDefinition(dungeon, room.Map.Map, nil)
			if ok || reason == "" {
				t.Fatalf("dungeon 122 must be rejected by the APC branch with a named reason, got ok=%t reason=%q",
					ok, reason)
			}
			continue
		}
		definition, reason, ok := resolveTournamentDungeonDefinition(dungeon, room.Map.Map, nil)
		if !ok {
			t.Fatalf("dungeon %d definition rejected: %s", dungeonID, reason)
		}
		if len(definition.RewardExperience) != tournamentRoundCount+1 {
			t.Fatalf("dungeon %d projected reward experience keys=%d want=%d",
				dungeonID, len(definition.RewardExperience), tournamentRoundCount+1)
		}
		for round := 0; round <= tournamentRoundCount; round++ {
			want := realTournamentClearRewardExperience[dungeonID][round]
			if got := definition.RewardExperience[round]; got != want {
				t.Fatalf("dungeon %d projected CompletedRounds=%d experience=%d want=%d",
					dungeonID, round, got, want)
			}
			if got := definition.RewardExperience[round]; got < 0 || got > 0xFFFFFFFF {
				t.Fatalf("dungeon %d CompletedRounds=%d experience=%d outside the u32 389 field",
					dungeonID, round, got)
			}
		}
	}
}

// 真档证据 ②：把真档数值**真的提交到角色账上**（并且只提交一次）。
//
// 这里用合成会话夹具 + 真档数值，而不是真档拓扑：本用例要证明的是
// "389 报出去的那个经验真的会改角色累计经验、且重复结算被拒"，
// 不需要再跑一遍合成地图的拓扑。
func TestRealScriptPVFTournamentClearExperienceIsPersistedOncePerRun(t *testing.T) {
	pvfPath := realTournamentClearExperiencePVFPath()
	if pvfPath == "" {
		t.Skip("set DNFBRIDGE_REAL_PVF_SMOKE (or DNF_WORLDMAP_REAL_PVF_SMOKE) to run the real closing smoke")
	}
	// 真档只用来取数值：先从真档读一遍，确认这组数字确实是真档里的。
	archive, err := platformpvf.Open(pvfPath)
	if err != nil {
		t.Fatal(err)
	}
	table, err := worldmap.LoadSource(context.Background(), archive, worldmap.Options{})
	if err != nil {
		t.Fatal(err)
	}
	realDungeon, ok := table.FindDungeon(120)
	if !ok {
		t.Fatal("real PVF dungeon 120 is missing")
	}
	realExperience := int64(-1)
	for _, row := range realDungeon.Metadata.TournamentClearExperiences {
		if row.CompletedRounds == tournamentFinalRound {
			realExperience = row.Experience
		}
	}
	if realExperience != realTournamentClearRewardExperience[120][tournamentFinalRound] {
		t.Fatalf("real dungeon 120 CompletedRounds=%d experience=%d want=%d",
			tournamentFinalRound, realExperience, realTournamentClearRewardExperience[120][tournamentFinalRound])
	}

	t.Setenv(dungeonTournamentEnvKey, dungeonTournamentModeVendor)
	service, session, runtime, repositories := prepareCurrentTournamentExperienceTest(t)
	session.dungeon.mu.Lock()
	defer session.dungeon.mu.Unlock()
	setChampionTournamentForTest(t, runtime)

	// 角色 20 级 0 经验。为了把"累计经验 = 真档值"钉成一位数不差的等式，
	// 这里把该 fixture 的阈值表换成"**20 级一级都不跨**"的形状：
	// `thresholds[19] = 1e9`（其余位置给 1 以满足单调不减），
	// 于是升级路径不参与这一条断言（升级本身由
	// `TestBeginTournamentRewardCommitsClearExperienceToCharacter` 覆盖）。
	catalog, err := service.dungeonMonsterCatalog()
	if err != nil {
		t.Fatal(err)
	}
	source, ok := catalog.source.(bridgePVFSource)
	if !ok {
		t.Fatalf("monster catalog source type=%T", catalog.source)
	}
	noLevelUpThresholds := make([]string, 0, currentAdventureCharacterLevelCap)
	for index := 0; index < currentAdventureCharacterLevelCap; index++ {
		if index >= 19 {
			noLevelUpThresholds = append(noLevelUpThresholds, "1000000000")
			continue
		}
		noLevelUpThresholds = append(noLevelUpThresholds, "1")
	}
	source[progression.ExperienceTablePath] = strings.Join(noLevelUpThresholds, " ")
	currentDungeonMonsterExperienceResourcesByCatalog.Delete(catalog)

	committed, result, err := service.commitCurrentTournamentClearRewardExperienceLocked(
		context.Background(), session, runtime, uint32(realExperience), tournamentFinalRound,
	)
	if err != nil || !committed {
		t.Fatalf("real-PVF tournament experience commit committed=%t err=%v receipt=%+v",
			committed, err, runtime.tournamentExperience)
	}
	if result.Character.Stats["exp"] != realExperience {
		t.Fatalf("committed exp=%d want=%d", result.Character.Stats["exp"], realExperience)
	}
	persisted, found, err := repositories.Character.Load(context.Background(), "99")
	if err != nil || !found {
		t.Fatalf("load character found=%t err=%v", found, err)
	}
	if persisted.Stats["exp"] != realExperience {
		t.Fatalf("persisted exp=%d want=%d", persisted.Stats["exp"], realExperience)
	}
	// 幂等凭据必须**留在 runtime 上**（生产路径就是靠它挡住第二次）。
	stored := runtime.tournamentExperience
	if want := tournamentExperienceReceiptKey(99, runtime.Dungeon.ID, runtime.lifecycleToken, tournamentFinalRound); stored.Key != want {
		t.Fatalf("receipt key=%q want=%q", stored.Key, want)
	}
	if !stored.Attempted || !stored.Committed || stored.Gain != uint32(realExperience) {
		t.Fatalf("stored receipt=%+v want attempted+committed+gain=%d", stored, realExperience)
	}

	// 第二次（重连补发 389 的最坏形态）必须被拒，且一行都不写。
	// 重放走的是**同一个 runtime 的同一张凭据**（生产路径就是这样）。
	againCommitted, _, err := service.commitCurrentTournamentClearRewardExperienceLocked(
		context.Background(), session, runtime,
		uint32(realExperience), tournamentFinalRound,
	)
	if err != nil || againCommitted {
		t.Fatalf("replayed real-PVF commit committed=%t err=%v receipt=%+v",
			againCommitted, err, runtime.tournamentExperience)
	}
	if reason := runtime.tournamentExperience.Reason; reason != "tournament_experience_was_already_attempted_for_this_run" {
		t.Fatalf("replay reason=%q want the named duplicate reason", reason)
	}
	persisted, _, err = repositories.Character.Load(context.Background(), "99")
	if err != nil || persisted.Stats["exp"] != realExperience {
		t.Fatalf("replay changed persisted exp=%d want=%d err=%v", persisted.Stats["exp"], realExperience, err)
	}
	if persisted.Level != 20 {
		t.Fatalf("a 1e9 threshold at level 20 must keep the character at 20, got %d", persisted.Level)
	}
}
