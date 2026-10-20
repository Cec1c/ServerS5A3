package dnfbridge

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/hex"
	"strings"
	"testing"

	"longheng.io/server/internal/modules/dnf/dnfenum"
	"longheng.io/server/internal/modules/dnf/dungeoncmd"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	dnfrepomemory "longheng.io/server/internal/modules/dnf/repository/memory"
	"longheng.io/server/internal/modules/dnf/worldmap"
)

// ─────────────────────────────────────────────────────────────────────────────
// 1. 开关口径
// ─────────────────────────────────────────────────────────────────────────────

func TestDungeonTournamentSwitchDefaultsOff(t *testing.T) {
	tests := []struct {
		value string
		want  bool
	}{
		{"", false},
		{"off", false},
		{"OFF", false},
		{"  ", false},
		{"0", false},
		{"false", false},
		{"sausages", false},
		{"legacy", false},
		// 口径收紧（本批，操作者要求与其余六个开关统一）：只有 `vendor` 启用。
		// 收紧前 `on` / `1` / `true` 也启用 —— 收紧只删别名，不改任何默认档行为。
		{"vendor", true},
		{"VENDOR", true},
		{"on", false},
		{"1", false},
		{"true", false},
	}
	for _, test := range tests {
		t.Run("value="+test.value, func(t *testing.T) {
			t.Setenv(dungeonTournamentEnvKey, test.value)
			if got := dungeonTournamentEnabled(); got != test.want {
				t.Fatalf("enabled(%q)=%t want=%t", test.value, got, test.want)
			}
		})
	}
}

// 未设置环境变量时也必须是默认档 —— 用 os.Unsetenv 显式覆盖 t.Setenv。
func TestDungeonTournamentSwitchUnsetEnvironmentIsOff(t *testing.T) {
	t.Setenv(dungeonTournamentEnvKey, "")
	if got := currentDungeonTournamentMode(); got != dungeonTournamentModeOff {
		t.Fatalf("mode=%q want=%q", got, dungeonTournamentModeOff)
	}
}

// 选奖应答档的默认口径（2026-09-29 收紧）：**未设置 / 空 / 未知值都是默认档 `select`**
// —— 只有客户端自己发来的 2 字节选奖（solicited）才回 458 选中投影。
// 字面量 `select+timer` = **危险档**（额外允许 3s 兜底路径回 458；实证会崩客户端），
// `state` = 一键完全回退（任何路径都不发 458）。
//
// 现场：青龙大会（dungeon 121）卡在翻牌、奖励只在聊天栏出现、整局 0 条 458 上行；
// 真机 458 handler 的派发表取证见 `dungeon_tournament_config.go`。
// 2026-09-29 06:05Z `conn_id=game-002721`：`select+timer` 语义的兜底路径发出的 458
// 让客户端在 1.265s 后 RST ⇒ 它不再是默认档。
func TestDungeonTournamentRewardAckSwitchDefaultsToSelect(t *testing.T) {
	tests := []struct {
		value string
		want  dungeonTournamentRewardAckMode
	}{
		{"", dungeonTournamentRewardAckSelect},
		{"  ", dungeonTournamentRewardAckSelect},
		{"sausages", dungeonTournamentRewardAckSelect},
		{"state_only", dungeonTournamentRewardAckSelect},
		{"select", dungeonTournamentRewardAckSelect},
		{"SELECT", dungeonTournamentRewardAckSelect},
		{"select+timer", dungeonTournamentRewardAckSelectTimer},
		{"SELECT+TIMER", dungeonTournamentRewardAckSelectTimer},
		{"state", dungeonTournamentRewardAckStateOnly},
		{"STATE", dungeonTournamentRewardAckStateOnly},
		{" state ", dungeonTournamentRewardAckStateOnly},
	}
	for _, test := range tests {
		t.Run("value="+test.value, func(t *testing.T) {
			t.Setenv(dungeonTournamentRewardAckEnvKey, test.value)
			if got := readDungeonTournamentRewardAckMode(test.value); got != test.want {
				t.Fatalf("mode(%q)=%v want=%v", test.value, got, test.want)
			}
		})
	}
	if got := dungeonTournamentRewardAckSelectTimer.String(); got != dungeonTournamentRewardAckValueSelectTimer {
		t.Fatalf("select+timer mode string = %q want %q", got, dungeonTournamentRewardAckValueSelectTimer)
	}
	if got := dungeonTournamentRewardAckSelect.String(); got != dungeonTournamentRewardAckValueSelect {
		t.Fatalf("select mode string = %q want %q", got, dungeonTournamentRewardAckValueSelect)
	}
	if got := dungeonTournamentRewardAckStateOnly.String(); got != dungeonTournamentRewardAckValueState {
		t.Fatalf("rollback mode string = %q want %q", got, dungeonTournamentRewardAckValueState)
	}
	// 零值 Service / 零值档位 = 默认（安全）档；只有显式危险档才碰 3s 兜底路径。
	if (&Service{}).dungeonTournamentRewardAck() != dungeonTournamentRewardAckSelect {
		t.Fatal("a zero-value Service must default to the `select` (safe) ack mode")
	}
	if !dungeonTournamentRewardAckSelectTimer.answersSolicitedSelection() {
		t.Fatal("the select+timer mode must answer a solicited pick with the 458 projection")
	}
	if !dungeonTournamentRewardAckSelect.answersSolicitedSelection() {
		t.Fatal("the select mode must answer a solicited pick with the 458 projection")
	}
	if dungeonTournamentRewardAckStateOnly.answersSolicitedSelection() {
		t.Fatal("the rollback mode must never send the 458 projection")
	}
	// 只有**显式** select+timer 会让 3s 兜底自动选奖路径也回 458。
	if !dungeonTournamentRewardAckSelectTimer.answersTimerSelection() {
		t.Fatal("the select+timer mode must answer the 3s timer path with the 458 projection")
	}
	if dungeonTournamentRewardAckSelect.answersTimerSelection() ||
		dungeonTournamentRewardAckStateOnly.answersTimerSelection() {
		t.Fatal("only the explicit select+timer mode may answer the timer path with the 458 projection")
	}
	// source 类型：只有客户端自己发来的那条算 solicited。
	if !tournamentRewardSelectionSourceClient.solicited() ||
		tournamentRewardSelectionSourceClient.String() != "client" {
		t.Fatal("the client source must be the only solicited one and log as source=client")
	}
	if tournamentRewardSelectionSourceTimer.solicited() ||
		tournamentRewardSelectionSourceTimer.String() != "timer" {
		t.Fatal("the timer source must stay unsolicited and log as source=timer")
	}
}

// 权威 opcode 固定值。
//
// ⚠️ 期望值在"收尾接线"批被**有意更正**。操作者任务书写的是"厂商 387/388/389、
// 458/459"，但取证核实后发现**那不是真机客户端的号**：线上 DNF.exe
// （`8A7AF614769D0B47D0647B77C33BCE725F2F20258651C01FC6C22AC1DB44AA4D`，
// 与 86JP 生产树 `changlog.md:400` 钉的是同一个二进制）把锦标赛族整体**前移 1**。
// 四条证据见 `dungeon_tournament_config.go` 的 opcode 段注释：
//   - 90CN `NoPack.exe` 派发表反向：对阵表 0182 / 房间 0183 / 终止 0184；
//   - 同族实机：按厂商号发 0183/0184 ⇒ 客户端越界崩溃 `0x0216F100`；
//   - 同族实机：奖励界面卡死时客户端发 **bodyless 457**，回答后才"returns to Shonan"；
//   - 本仓 `dnfenum`（457/458）与上面两条完全一致。
//
// 厂商原值保留在 `a21Vendor*`（只作对拍锚点），本用例同时钉住两者之差 = −1。
func TestDungeonTournamentOpcodesMatchVendorAuthority(t *testing.T) {
	if a21NotiTournamentInfo != 386 || a21NotiTournamentMapInfo != 387 || a21NotiTournamentClearReward != 388 {
		t.Fatalf("NOTI opcodes = %d/%d/%d want 386/387/388 (真机 = 厂商 387/388/389 − 1)",
			a21NotiTournamentInfo, a21NotiTournamentMapInfo, a21NotiTournamentClearReward)
	}
	if a21CmdTournamentRewardSelectState != 457 || a21CmdTournamentRewardSelect != 458 {
		t.Fatalf("CMD opcodes = %d/%d want 457/458 (真机 = 厂商 458/459 − 1)",
			a21CmdTournamentRewardSelectState, a21CmdTournamentRewardSelect)
	}
	if a21CmdDieAICharacter != 144 {
		t.Fatalf("DIE_AI_CHARACTER opcode = %d want 144", a21CmdDieAICharacter)
	}
	// 真机号必须与本仓 dnfenum 的枚举逐值一致（这是"派发表反向"之外的独立印证）。
	if a21CmdTournamentRewardSelectState != uint16(dnfenum.CmdPacketTournamentRewardSelectState) ||
		a21CmdTournamentRewardSelect != uint16(dnfenum.CmdPacketTournamentRewardSelect) {
		t.Fatalf("真机 CMD 号 %d/%d 与 dnfenum 枚举 %d/%d 不一致",
			a21CmdTournamentRewardSelectState, a21CmdTournamentRewardSelect,
			uint16(dnfenum.CmdPacketTournamentRewardSelectState),
			uint16(dnfenum.CmdPacketTournamentRewardSelect))
	}
	// 位移口径：真机 = 厂商 + a21TournamentOpcodeShift。
	if int(a21NotiTournamentInfo)-int(a21VendorNotiTournamentInfo) != a21TournamentOpcodeShift ||
		int(a21NotiTournamentMapInfo)-int(a21VendorNotiTournamentMapInfo) != a21TournamentOpcodeShift ||
		int(a21NotiTournamentClearReward)-int(a21VendorNotiTournamentClearReward) != a21TournamentOpcodeShift ||
		int(a21CmdTournamentRewardSelectState)-int(a21VendorCmdTournamentRewardState) != a21TournamentOpcodeShift ||
		int(a21CmdTournamentRewardSelect)-int(a21VendorCmdTournamentRewardSelect) != a21TournamentOpcodeShift {
		t.Fatal("真机号相对厂商表的位移不一致")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// 2. 默认档下所有新入口都是 no-op
// ─────────────────────────────────────────────────────────────────────────────

func TestTournamentEntryPointsAreInertInDefaultMode(t *testing.T) {
	t.Setenv(dungeonTournamentEnvKey, "")
	// (a) 准入：声明了 `[required item]` 的 DGN 在默认档下与改动前同一条判定 ——
	// 既不因为缺少证物被拒（默认档不校验），也不因为新代码而改变结论。
	dungeon := worldmap.Dungeon{
		ID: 120,
		Metadata: worldmap.DungeonMetadata{
			RequiredItems: []worldmap.DungeonRequiredItem{{ItemID: 4726, Count: 1, ConsumeOnEntry: true}},
			Flags:         map[string]bool{"tournament dungeon": true},
		},
	}
	character := dnfrepo.CharacterRecord{
		CharacterID: "99", Level: 90, Stats: map[string]int64{"fatigue": 100},
	}
	session := &gameSession{connID: "tournament-inert", selectedCharacterID: 99}
	legacy := validateDungeonEntry(character, dungeon, session)
	withTournament := (&Service{}).validateDungeonEntryWithTournament(context.Background(), character, dungeon, session)
	if legacy != nil || withTournament != nil {
		t.Fatalf("default mode changed admission: legacy=%v tournament=%v", legacy, withTournament)
	}

	// (b) 赛制运行时/房间投影：默认档直接返回 (nil, nil)，topology 不被触碰。
	runtime, err := (&Service{}).prepareTournamentDungeonRun(&worldmap.DungeonTopology{}, dungeon, 1)
	if err != nil || runtime != nil {
		t.Fatalf("default mode built a tournament runtime: runtime=%+v err=%v", runtime, err)
	}

	// (c) op144：默认档返回 (false, nil)，不记日志、不改状态。
	handled, err := (&Service{}).handleCurrentTournamentDieAICharacter(session, a21CmdDieAICharacter, []byte{1, 2, 3})
	if handled || err != nil {
		t.Fatalf("default mode handled op144: handled=%t err=%v", handled, err)
	}
}

// 厂商 DecisionHandler `ShouldClearDungeon`（DungeonCombatHandler.cs:260-267）真值表。
func TestShouldClearDungeonMatchesVendorPredicate(t *testing.T) {
	tests := []struct {
		clearCondition bool
		bossEndpoint   bool
		ignoreDefault  bool
		want           bool
	}{
		{false, false, true, false},
		{false, true, true, false}, // 锦标赛：到终点也不通关
		{false, true, false, true}, // 普通副本：到终点即通关
		{true, false, true, true},  // clear condition 优先，ignore 管不到它
		{true, true, true, true},
		{true, true, false, true},
		{false, false, false, false},
	}
	for _, test := range tests {
		got := shouldClearDungeon(test.clearCondition, test.bossEndpoint, test.ignoreDefault)
		if got != test.want {
			t.Fatalf("shouldClearDungeon(%t,%t,%t)=%t want=%t",
				test.clearCondition, test.bossEndpoint, test.ignoreDefault, got, test.want)
		}
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// 3. 定义投影的硬失败矩阵（厂商 TournamentDungeonDefinition.cs:250-510）
// ─────────────────────────────────────────────────────────────────────────────

// tournamentFixtureDungeon 造一份"真档形状"的 120 黄龙定义。
func tournamentFixtureDungeon() worldmap.Dungeon {
	return worldmap.Dungeon{
		ID: 120,
		Metadata: worldmap.DungeonMetadata{
			BasisLevel:      worldmap.OptionalInt{Value: 72, Set: true},
			LimitPartyCount: worldmap.OptionalInt{Value: 1, Set: true},
			Flags:           map[string]bool{"tournament dungeon": true},
			IntegerValues: map[string]int64{
				"coin limit":                        3,
				"tournament round fatigue":          2,
				"tournament clear reward gold rate": 16,
			},
			RequiredItems: []worldmap.DungeonRequiredItem{{ItemID: 4726, Count: 1, ConsumeOnEntry: true}},
			TournamentResultCards: []worldmap.TournamentResultCard{
				{ResultKey: 0, EmptyWeight: 100},
				{ResultKey: 1, GoldWeight: 70, EmptyWeight: 30},
				{ResultKey: 2, GoldWeight: 80, EmptyWeight: 20},
				{ResultKey: 3, GoldWeight: 90, EmptyWeight: 10},
				{ResultKey: 4, GoldWeight: 100},
				{ResultKey: 5, ItemWeight: 100},
			},
			TournamentRewardItemRates: []worldmap.TournamentRewardItemRate{
				{ItemID: 3329, Weight: 60, Count: 1},
				{ItemID: 3329, Weight: 35, Count: 2},
				{ItemID: 3329, Weight: 5, Count: 3},
			},
			TournamentClearExperiences: []worldmap.TournamentRewardExperience{
				{CompletedRounds: 0, Experience: 0},
				{CompletedRounds: 1, Experience: 30000},
				{CompletedRounds: 2, Experience: 50000},
				{CompletedRounds: 3, Experience: 100000},
				{CompletedRounds: 4, Experience: 200000},
			},
		},
	}
}

// tournamentFixtureMap 造一份 15 个 `[monster]` 候选 + 2 个出生点的 120 地图。
func tournamentFixtureMap() worldmap.Map {
	value := worldmap.Map{
		ID:        12000,
		DungeonID: worldmap.OptionalInt{Value: 120, Set: true},
		TournamentStartAreas: []worldmap.TournamentStartAreaInfo{
			{PartyCount: 1, X: 360, Y: 270, Direction: 1},
			{PartyCount: 1, X: 760, Y: 270, Direction: 2},
		},
	}
	strengths := []int64{900, 300, 900, 900, 600, 600, 300, 300, 900, 600, 600, 300, 300, 300, 300}
	for index, strength := range strengths {
		value.TournamentEnemyCandidates = append(value.TournamentEnemyCandidates, worldmap.TournamentEnemyInfo{
			PartyCount: 1,
			Code:       int64(56048 + index),
			Strength:   strength,
			Name:       "对手",
		})
	}
	return value
}

func TestResolveTournamentDefinitionAcceptsRealShapedYellowDragon(t *testing.T) {
	definition, reason, ok := resolveTournamentDungeonDefinition(
		tournamentFixtureDungeon(), tournamentFixtureMap(), nil)
	if !ok {
		t.Fatalf("real-shaped 120 rejected: %s", reason)
	}
	if definition.MapID != 12000 || definition.PartyLimit != 1 || definition.BasisLevel != 72 {
		t.Fatalf("definition = %+v", definition)
	}
	if len(definition.EntryItems) != 1 || definition.EntryItems[0].ItemID != 4726 ||
		!definition.EntryItems[0].ConsumeOnEntry {
		t.Fatalf("entry items = %+v", definition.EntryItems)
	}
	if len(definition.ResultCards) != 6 || len(definition.RewardExperience) != 5 {
		t.Fatalf("reward tables = %+v / %+v", definition.ResultCards, definition.RewardExperience)
	}
	if len(definition.Candidates) != 15 {
		t.Fatalf("candidates = %d want 15", len(definition.Candidates))
	}
}

func TestResolveTournamentDefinitionHardFailures(t *testing.T) {
	tests := []struct {
		name       string
		mutate     func(*worldmap.Dungeon, *worldmap.Map)
		wantReason string
	}{
		{
			name: "not marked tournament",
			mutate: func(d *worldmap.Dungeon, _ *worldmap.Map) {
				d.Metadata.Flags = map[string]bool{}
			},
			wantReason: "dungeon is not marked as tournament",
		},
		{
			name: "basis level missing",
			mutate: func(d *worldmap.Dungeon, _ *worldmap.Map) {
				d.Metadata.BasisLevel = worldmap.OptionalInt{}
			},
			wantReason: "tournament basic level is invalid",
		},
		{
			name: "party limit unsupported",
			mutate: func(d *worldmap.Dungeon, _ *worldmap.Map) {
				d.Metadata.LimitPartyCount = worldmap.OptionalInt{Value: 3, Set: true}
			},
			wantReason: "tournament party limit is unsupported",
		},
		{
			name: "clear reward gold rate missing",
			mutate: func(d *worldmap.Dungeon, _ *worldmap.Map) {
				delete(d.Metadata.IntegerValues, "tournament clear reward gold rate")
			},
			wantReason: "tournament DGN reward or round fields are incomplete",
		},
		{
			name: "reward definition malformed flag",
			mutate: func(d *worldmap.Dungeon, _ *worldmap.Map) {
				d.Metadata.TournamentResultCardMalformed = true
			},
			wantReason: "tournament reward definition is malformed",
		},
		{
			name: "reward experience misses round",
			mutate: func(d *worldmap.Dungeon, _ *worldmap.Map) {
				d.Metadata.TournamentClearExperiences = d.Metadata.TournamentClearExperiences[:4]
			},
			wantReason: "tournament reward experience misses round 4",
		},
		{
			name: "result card misses key",
			mutate: func(d *worldmap.Dungeon, _ *worldmap.Map) {
				d.Metadata.TournamentResultCards = d.Metadata.TournamentResultCards[:5]
			},
			wantReason: "tournament result card misses key 5",
		},
		{
			name: "result card zero total weight",
			mutate: func(d *worldmap.Dungeon, _ *worldmap.Map) {
				d.Metadata.TournamentResultCards[0] = worldmap.TournamentResultCard{ResultKey: 0}
			},
			wantReason: "tournament result card weights are invalid",
		},
		{
			name: "reward item rate empty",
			mutate: func(d *worldmap.Dungeon, _ *worldmap.Map) {
				d.Metadata.TournamentRewardItemRates = nil
			},
			wantReason: "tournament reward item rate is missing",
		},
		{
			name: "map belongs to another dungeon",
			mutate: func(_ *worldmap.Dungeon, m *worldmap.Map) {
				m.DungeonID = worldmap.OptionalInt{Value: 121, Set: true}
			},
			wantReason: "tournament MAP belongs to another dungeon",
		},
		{
			name: "map definition malformed",
			mutate: func(_ *worldmap.Dungeon, m *worldmap.Map) {
				m.TournamentDefinitionMalformed = true
			},
			wantReason: "tournament MAP definition is malformed",
		},
		{
			name: "candidate pool too small for 15",
			mutate: func(_ *worldmap.Dungeon, m *worldmap.Map) {
				m.TournamentEnemyCandidates = m.TournamentEnemyCandidates[:14]
			},
			wantReason: "tournament candidate pool is too small for party count 1",
		},
		{
			name: "start areas incomplete",
			mutate: func(_ *worldmap.Dungeon, m *worldmap.Map) {
				m.TournamentStartAreas = m.TournamentStartAreas[:1]
			},
			wantReason: "tournament start areas are incomplete for party count 1",
		},
		{
			name: "entry item invalid",
			mutate: func(d *worldmap.Dungeon, _ *worldmap.Map) {
				d.Metadata.RequiredItems = []worldmap.DungeonRequiredItem{{ItemID: 0, Count: 1}}
			},
			wantReason: "tournament entry item contains an invalid field",
		},
		{
			name: "apc branch without AICharacter level",
			mutate: func(_ *worldmap.Dungeon, m *worldmap.Map) {
				m.TournamentEnemyCandidates[0].IsApc = true
			},
			wantReason: "tournament APC definition is missing code=56048",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			dungeon := tournamentFixtureDungeon()
			mapValue := tournamentFixtureMap()
			test.mutate(&dungeon, &mapValue)
			_, reason, ok := resolveTournamentDungeonDefinition(dungeon, mapValue, nil)
			if ok {
				t.Fatal("hard failure was accepted")
			}
			if !strings.Contains(reason, test.wantReason) {
				t.Fatalf("failure reason = %q want contains %q", reason, test.wantReason)
			}
		})
	}
}

// `[apc]` 分支只有 code 能解出等级才放行（厂商 :416-428）。
func TestResolveTournamentDefinitionApcBranchNeedsAICharacterLevel(t *testing.T) {
	dungeon := tournamentFixtureDungeon()
	mapValue := tournamentFixtureMap()
	for index := range mapValue.TournamentEnemyCandidates {
		mapValue.TournamentEnemyCandidates[index].IsApc = true
	}
	resolver := func(code int64) (byte, bool) {
		if code == 56048 {
			return 75, true
		}
		return 0, false
	}
	_, reason, ok := resolveTournamentDungeonDefinition(dungeon, mapValue, resolver)
	if ok || !strings.Contains(reason, "missing code=") {
		t.Fatalf("partial AICharacter coverage was accepted: ok=%t reason=%q", ok, reason)
	}
	full := func(int64) (byte, bool) { return 75, true }
	definition, reason, ok := resolveTournamentDungeonDefinition(dungeon, mapValue, full)
	if !ok {
		t.Fatalf("full AICharacter coverage rejected: %s", reason)
	}
	if definition.Candidates[0].ActorType != 5 || definition.Candidates[0].Kind != tournamentActorAICharacter {
		t.Fatalf("apc candidate projection = %+v", definition.Candidates[0])
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// 4. 赛制状态机
// ─────────────────────────────────────────────────────────────────────────────

// deterministicTournamentNext 是一个可复现的随机源（线性同余，绝不越界）。
func deterministicTournamentNext(seed uint32) func(int) int {
	state := seed
	return func(exclusiveMaximum int) int {
		if exclusiveMaximum <= 0 {
			return 0
		}
		state = state*1664525 + 1013904223
		return int(state % uint32(exclusiveMaximum))
	}
}

func mustTournamentRuntime(t *testing.T, partyCount int, seed uint32) (*tournamentDungeonRuntime, tournamentDefinition) {
	t.Helper()
	dungeon := tournamentFixtureDungeon()
	mapValue := tournamentFixtureMap()
	definition, reason, ok := resolveTournamentDungeonDefinition(dungeon, mapValue, nil)
	if !ok {
		t.Fatalf("fixture definition rejected: %s", reason)
	}
	runtime, reason, ok := newTournamentDungeonRuntime(&definition, partyCount, deterministicTournamentNext(seed))
	if !ok {
		t.Fatalf("fixture runtime rejected: %s", reason)
	}
	return runtime, definition
}

func TestTournamentBracketIsSixteenTeamsFourRounds(t *testing.T) {
	runtime, definition := mustTournamentRuntime(t, 1, 20260927)
	snapshot := runtime.snapshot()
	if len(snapshot.Rounds) != tournamentRoundCount {
		t.Fatalf("rounds = %d want 4", len(snapshot.Rounds))
	}
	wantTeams := []int{16, 8, 4, 2}
	for index, round := range snapshot.Rounds {
		if round.Number != byte(index+1) || len(round.Teams) != wantTeams[index] {
			t.Fatalf("round %d teams=%d want=%d", round.Number, len(round.Teams), wantTeams[index])
		}
		playerTeams := 0
		for _, team := range round.Teams {
			if team.IsPlayer {
				playerTeams++
				if len(team.Members) != definition.PartyLimit {
					t.Fatalf("player team members=%d want=%d", len(team.Members), definition.PartyLimit)
				}
			}
		}
		if playerTeams != 1 {
			t.Fatalf("round %d player teams=%d want=1", round.Number, playerTeams)
		}
	}
	// 玩家在第 4 轮必然在场 —— 单败淘汰的玩家路径由 TryCreate 直接决定。
	last := snapshot.Rounds[tournamentRoundCount-1]
	if tournamentFindPlayerIndex(last.Teams) < 0 {
		t.Fatal("final round has no player team")
	}
}

// 厂商 TournamentDungeonRuntime.cs:490-515：PathActors 长度 = 4*partyCount，
// **每条 IsBlocking = true**，第 4 回合 actorType 升格 8/3。
func TestTournamentPathActorsAreBlockingAndFinalRoundUpgrades(t *testing.T) {
	runtime, definition := mustTournamentRuntime(t, 1, 7)
	snapshot := runtime.snapshot()
	if len(snapshot.PathActors) != tournamentRoundCount*definition.PartyLimit {
		t.Fatalf("path actors=%d want=%d", len(snapshot.PathActors), tournamentRoundCount*definition.PartyLimit)
	}
	for index, actor := range snapshot.PathActors {
		if !actor.IsBlocking {
			t.Fatalf("path actor %d is not blocking: %+v", index, actor)
		}
		if actor.Code <= 0 {
			t.Fatalf("path actor %d has no code: %+v", index, actor)
		}
	}
	// 前 3 轮是 `[monster]` 分支 ⇒ actorType 恒为 0；第 4 轮升格为 3（怪物 Boss）。
	for index := 0; index < 3; index++ {
		if snapshot.PathActors[index].Type != 0 {
			t.Fatalf("round %d actor type=%d want 0", index+1, snapshot.PathActors[index].Type)
		}
	}
	if snapshot.PathActors[3].Type != 3 {
		t.Fatalf("final round monster actor type=%d want 3", snapshot.PathActors[3].Type)
	}
}

// 第 4 回合对一个 APC（actorType == 5）应该升格成 8（APC Boss）。
func TestTournamentFinalRoundApcUpgradesToBossTypeEight(t *testing.T) {
	dungeon := tournamentFixtureDungeon()
	mapValue := tournamentFixtureMap()
	for index := range mapValue.TournamentEnemyCandidates {
		mapValue.TournamentEnemyCandidates[index].IsApc = true
	}
	definition, reason, ok := resolveTournamentDungeonDefinition(dungeon, mapValue, func(int64) (byte, bool) { return 60, true })
	if !ok {
		t.Fatalf("apc definition rejected: %s", reason)
	}
	runtime, reason, ok := newTournamentDungeonRuntime(&definition, 1, deterministicTournamentNext(11))
	if !ok {
		t.Fatalf("apc runtime rejected: %s", reason)
	}
	snapshot := runtime.snapshot()
	if snapshot.PathActors[3].Type != 8 {
		t.Fatalf("final round apc actor type=%d want 8", snapshot.PathActors[3].Type)
	}
}

// 厂商 TournamentDungeonRuntime.cs:257-324：按 sequence 归轮，全灭才进轮；
// 第 4 轮全灭 ⇒ TournamentCompleted。
func TestTournamentActorDeathsAdvanceRoundsAndComplete(t *testing.T) {
	runtime, _ := mustTournamentRuntime(t, 1, 99)
	if runtime.tryBindFirstActorSequence(100) == false {
		t.Fatal("first actor sequence bind failed")
	}
	if runtime.tryBindFirstActorSequence(101) {
		t.Fatal("rebinding a different first sequence must fail")
	}
	for round := 1; round <= tournamentRoundCount; round++ {
		sequence := 100 + (round - 1)
		if !runtime.canAcceptActorDeath(sequence) {
			t.Fatalf("round %d sequence %d was rejected before the kill", round, sequence)
		}
		transition := runtime.tryApplyActorDeath(sequence)
		if !transition.accepted() {
			t.Fatalf("round %d death rejected: %+v", round, transition)
		}
		if round < tournamentRoundCount {
			if !transition.roundCompleted() || transition.tournamentCompleted() ||
				transition.CompletedRound != byte(round) || transition.CurrentRound != byte(round+1) {
				t.Fatalf("round %d transition = %+v", round, transition)
			}
		} else {
			if !transition.tournamentCompleted() || transition.CompletedRound != tournamentFinalRound {
				t.Fatalf("final transition = %+v", transition)
			}
		}
		duplicate := runtime.tryApplyActorDeath(sequence)
		if duplicate.Disposition != tournamentDeathDuplicate {
			t.Fatalf("round %d duplicate = %+v", round, duplicate)
		}
	}
	if !runtime.isChampion() || !runtime.isTournamentTerminated() {
		t.Fatalf("runtime did not reach Champion: %+v", runtime.snapshot())
	}
}

// 不在本轮的 sequence 一律拒绝（厂商 :291-292）。
func TestTournamentRejectsOutOfRoundActorDeath(t *testing.T) {
	runtime, _ := mustTournamentRuntime(t, 1, 5)
	if !runtime.tryBindFirstActorSequence(200) {
		t.Fatal("bind failed")
	}
	if transition := runtime.tryApplyActorDeath(202); transition.accepted() {
		t.Fatalf("round-3 sequence accepted while in round 1: %+v", transition)
	}
	if !runtime.tryApplyActorDeath(200).accepted() {
		t.Fatal("round-1 sequence rejected")
	}
	if transition := runtime.tryApplyActorDeath(200); transition.Disposition != tournamentDeathDuplicate {
		t.Fatalf("replayed round-1 sequence = %+v", transition)
	}
}

// 厂商 :326-349：玩家死亡 ⇒ CompletedRounds = currentRound-1，终局 Eliminated。
func TestTournamentEliminationFreezesCompletedRounds(t *testing.T) {
	runtime, _ := mustTournamentRuntime(t, 1, 3)
	if !runtime.tryBindFirstActorSequence(50) {
		t.Fatal("bind failed")
	}
	if transition := runtime.tryApplyActorDeath(50); !transition.roundCompleted() {
		t.Fatalf("round 1 kill = %+v", transition)
	}
	completed, accepted := runtime.tryEliminate()
	if !accepted || completed != 1 {
		t.Fatalf("elimination = (%d,%t) want (1,true)", completed, accepted)
	}
	if _, again := runtime.tryEliminate(); again {
		t.Fatal("second elimination must not be accepted")
	}
	if runtime.isChampion() {
		t.Fatal("eliminated runtime reported Champion")
	}
	if runtime.tryApplyActorDeath(51).accepted() {
		t.Fatal("death accepted after elimination")
	}
}

// 厂商 :376-382 / :402-408：人数必须等于 `[limit party count]`，候选池必须 ≥15*人数。
func TestTournamentRuntimeRejectsPartyCountAndSmallPool(t *testing.T) {
	dungeon := tournamentFixtureDungeon()
	mapValue := tournamentFixtureMap()
	definition, _, ok := resolveTournamentDungeonDefinition(dungeon, mapValue, nil)
	if !ok {
		t.Fatal("fixture definition rejected")
	}
	if _, reason, ok := newTournamentDungeonRuntime(&definition, 2, deterministicTournamentNext(1)); ok ||
		!strings.Contains(reason, "does not match configured limit") {
		t.Fatalf("party count mismatch accepted: ok=%t reason=%q", ok, reason)
	}
	if _, reason, ok := newTournamentDungeonRuntime(&definition, 1, nil); ok ||
		!strings.Contains(reason, "random source is missing") {
		t.Fatalf("missing random source accepted: ok=%t reason=%q", ok, reason)
	}
	// 越界随机源是错误，不是夹取（厂商 :564-573 抛异常）。
	if _, reason, ok := newTournamentDungeonRuntime(&definition, 1, func(int) int { return 999 }); ok ||
		!strings.Contains(reason, "out-of-range") {
		t.Fatalf("out-of-range random source accepted: ok=%t reason=%q", ok, reason)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// 5. 包构造器逐字节
// ─────────────────────────────────────────────────────────────────────────────

func TestBuildTournamentPacketBodies(t *testing.T) {
	cards := []tournamentCardReward{
		{IsGold: true, GoldAmount: 1000},
		{ItemID: 3329, StackCount: 2},
		{IsGold: true, GoldAmount: 500},
		{ItemID: 3329, StackCount: 5},
	}
	state, err := newTournamentRewardState(cards, 1, 0, 200000, 4, true)
	if err != nil {
		t.Fatal(err)
	}

	// NOTI 389 / `BuildTournamentClearReward`（TournamentPacketBuilder.cs:77-105）。
	reward := buildTournamentClearRewardBody(state)
	want := []byte{4}
	want = binary.LittleEndian.AppendUint32(want, 200000)
	want = append(want, 0)
	want = append(want, 2)
	for _, card := range []tournamentCardReward{cards[0], cards[1]} {
		if card.IsGold {
			want = binary.LittleEndian.AppendUint32(want, 0)
			want = binary.LittleEndian.AppendUint32(want, uint32(card.GoldAmount))
		} else {
			want = binary.LittleEndian.AppendUint32(want, uint32(card.ItemID))
			want = binary.LittleEndian.AppendUint32(want, uint32(card.StackCount))
		}
		want = binary.LittleEndian.AppendUint16(want, 0)
	}
	want = append(want, 2)
	for _, card := range []tournamentCardReward{cards[2], cards[3]} {
		if card.IsGold {
			want = binary.LittleEndian.AppendUint32(want, 0)
			want = binary.LittleEndian.AppendUint32(want, uint32(card.GoldAmount))
		} else {
			want = binary.LittleEndian.AppendUint32(want, uint32(card.ItemID))
			want = binary.LittleEndian.AppendUint32(want, uint32(card.StackCount))
		}
		want = binary.LittleEndian.AppendUint16(want, 0)
	}
	if !bytes.Equal(reward, want) {
		t.Fatalf("clear reward body=%x want=%x", reward, want)
	}

	// CMD 458 / `BuildTournamentRewardSelectState`（:107-127）：head 1，
	// 之后 2×4 个字节，本槽位存在且该类型可用 ⇒ 1，否则 0xFF。
	selectState := buildTournamentRewardSelectStateBody(state)
	wantState := []byte{1, 1, 0xFF, 0xFF, 0xFF, 1, 0xFF, 0xFF, 0xFF}
	if !bytes.Equal(selectState, wantState) {
		t.Fatalf("reward select state=%x want=%x", selectState, wantState)
	}

	// CMD 459 / `BuildTournamentRewardSelection`（:129-144）：head 1，
	// 每类型 cardCount 后跟 selection。
	selection := buildTournamentRewardSelectionBody(state)
	if !bytes.Equal(selection, []byte{1, 2, 0xFF, 0xFF, 2, 0xFF, 0xFF}) {
		t.Fatalf("reward selection=%x", selection)
	}
	state.Selected[1] = 1
	selection = buildTournamentRewardSelectionBody(state)
	if !bytes.Equal(selection, []byte{1, 2, 0xFF, 0xFF, 2, 0xFF, 0}) {
		t.Fatalf("reward selection after pick=%x", selection)
	}
}

// 未打满 4 轮时第 2 类卡数量为 0（TournamentParticipantRewardState.cs:62-65）。
func TestTournamentRewardStateSecondCardTypeNeedsFullClear(t *testing.T) {
	cards := []tournamentCardReward{{IsGold: true, GoldAmount: 100}, {ItemID: 1, StackCount: 1}, {}, {}}
	state, err := newTournamentRewardState(cards, 1, 0, 0, 2, false)
	if err != nil {
		t.Fatal(err)
	}
	if state.cardCount(0) != 2 || state.cardCount(1) != 0 {
		t.Fatalf("card counts = %d/%d want 2/0", state.cardCount(0), state.cardCount(1))
	}
	if body := buildTournamentRewardSelectionBody(state); !bytes.Equal(body, []byte{1, 2, 0xFF, 0xFF, 0}) {
		t.Fatalf("partial selection body=%x", body)
	}
	if _, err := newTournamentRewardState(cards[:3], 1, 0, 0, 4, true); err == nil {
		t.Fatal("three cards must be rejected (vendor requires four)")
	}
	if _, err := newTournamentRewardState(cards, 5, 0, 0, 4, true); err == nil {
		t.Fatal("party count above PartySlotCount must be rejected")
	}
	if _, err := newTournamentRewardState(cards, 2, 2, 0, 4, true); err == nil {
		t.Fatal("local party slot outside party count must be rejected")
	}
}

// NOTI 387 / `BuildTournamentInfo`（TournamentPacketBuilder.cs:9-52）：
// 头 6 字节 + 每轮 `byte number, byte teamCount` + 队伍块 + 第二段路径 actor。
func TestBuildTournamentInfoBodyLayout(t *testing.T) {
	runtime, definition := mustTournamentRuntime(t, 1, 4242)
	body, err := buildTournamentInfoBody(runtime, 2, 1000)
	if err != nil {
		t.Fatal(err)
	}
	if binary.LittleEndian.Uint32(body[0:4]) != uint32(definition.DungeonID) {
		t.Fatalf("dungeon id = %d", binary.LittleEndian.Uint32(body[0:4]))
	}
	if body[4] != 2 || body[5] != byte(definition.PartyLimit) {
		t.Fatalf("difficulty/partyLimit = %d/%d", body[4], body[5])
	}
	offset := 6
	snapshot := runtime.snapshot()
	for _, round := range snapshot.Rounds {
		if body[offset] != round.Number || int(body[offset+1]) != len(round.Teams) {
			t.Fatalf("round header at %d = %d/%d want %d/%d",
				offset, body[offset], body[offset+1], round.Number, len(round.Teams))
		}
		offset += 2
		for _, team := range round.Teams {
			if body[offset] != team.Position {
				t.Fatalf("team position at %d = %d want %d", offset, body[offset], team.Position)
			}
			offset++
			for _, member := range team.Members {
				if binary.LittleEndian.Uint32(body[offset:offset+4]) != uint32(int32(member.Code)) {
					t.Fatalf("member code at %d = %d", offset, binary.LittleEndian.Uint32(body[offset:offset+4]))
				}
				if binary.LittleEndian.Uint16(body[offset+4:offset+6]) != clampTournamentStrength(member.Strength) {
					t.Fatalf("member strength at %d = %d", offset, binary.LittleEndian.Uint16(body[offset+4:offset+6]))
				}
				offset += 6
			}
		}
	}
	sequence := uint16(1000)
	for _, round := range snapshot.Rounds {
		if body[offset] != round.Number {
			t.Fatalf("path actor round at %d = %d want %d", offset, body[offset], round.Number)
		}
		offset++
		for member := 0; member < definition.PartyLimit; member++ {
			actor := snapshot.PathActors[(int(round.Number)-1)*definition.PartyLimit+member]
			if binary.LittleEndian.Uint16(body[offset:offset+2]) != sequence {
				t.Fatalf("path sequence at %d = %d want %d", offset, binary.LittleEndian.Uint16(body[offset:offset+2]), sequence)
			}
			if binary.LittleEndian.Uint32(body[offset+2:offset+6]) != uint32(int32(actor.Code)) {
				t.Fatalf("path code at %d", offset)
			}
			if body[offset+6] != actor.Level || body[offset+7] != actor.Type {
				t.Fatalf("path level/type at %d = %d/%d want %d/%d",
					offset, body[offset+6], body[offset+7], actor.Level, actor.Type)
			}
			offset += 8
			sequence++
		}
	}
	if offset != len(body) {
		t.Fatalf("tournament info trailing bytes: offset=%d len=%d body=%x", offset, len(body), body)
	}
}

// NOTI 388 / `BuildTournamentMapInfo`（TournamentPacketBuilder.cs:54-75）：
// 首访 15 字节、revisit 12 字节。
func TestBuildTournamentMapInfoBodyLayout(t *testing.T) {
	first := buildTournamentMapInfoBody(3, 4, 0x11223344, 12000, false)
	want := []byte{3, 4, 0x44, 0x33, 0x22, 0x11, 0, 1, 0xE0, 0x2E, 0, 0, 0, 0, 0}
	if !bytes.Equal(first, want) {
		t.Fatalf("first visit body=%x want=%x", first, want)
	}
	revisit := buildTournamentMapInfoBody(3, 4, 0x11223344, 12000, true)
	wantRevisit := append(append([]byte(nil), want[:7]...), 0)
	if !bytes.Equal(revisit, wantRevisit) {
		t.Fatalf("revisit body=%x want=%x", revisit, wantRevisit)
	}
}

// 厂商 MapFile.cs:28-30 的 clamp：负数收敛到 0、超过 u16 收敛到 65535。
func TestClampTournamentStrength(t *testing.T) {
	if clampTournamentStrength(-1) != 0 || clampTournamentStrength(0) != 0 ||
		clampTournamentStrength(900) != 900 || clampTournamentStrength(70000) != 0xFFFF {
		t.Fatalf("clamp = %d/%d/%d/%d",
			clampTournamentStrength(-1), clampTournamentStrength(0),
			clampTournamentStrength(900), clampTournamentStrength(70000))
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// 6. 默认档零变化：同一条进图路径的 op16/op5/op28/op29 字节完全一致
// ─────────────────────────────────────────────────────────────────────────────

// runDungeonSelectStreamForTournamentControl 跑一次真档形状的成功进图，返回整条下行字节流。
func runDungeonSelectStreamForTournamentControl(t *testing.T) []byte {
	t.Helper()
	table, resolver, monsters := loadBridgeDungeonStaticData(t, bridgeDungeonPVF(false))
	repositories := dnfrepomemory.NewMemoryGroup()
	if err := repositories.Character.Save(context.Background(), dnfrepo.CharacterRecord{
		CharacterID: "99",
		AccountID:   "account-1",
		Level:       20,
		Stats: map[string]int64{
			"fatigue": 100, "town_id": 38, "area_id": 1,
			"pos_x": 450, "pos_y": 234, "direction": 5, "area_state": 3,
		},
	}); err != nil {
		t.Fatal(err)
	}
	if err := repositories.Inventory.Save(context.Background(), dnfrepo.InventoryRecord{
		CharacterID: "99", Slots: map[string]dnfrepo.ItemStack{},
	}); err != nil {
		t.Fatal(err)
	}
	service := &Service{
		options:             options{accountID: "account-1", gameUpperBodyCodec: gameUpperBodyCodecPlain},
		worldMapTable:       table,
		worldMapResolver:    resolver,
		dungeonMonsterTable: monsters,
		dungeonChoice:       func(int) (int, error) { return 0, nil },
		dungeonSeed:         func() (uint32, error) { return 1, nil },
		repositoryProvider:  func() (dnfrepo.Group, bool) { return repositories, true },
	}
	connection := &bufferConn{}
	session := &gameSession{
		conn:                          connection,
		selectedCharacterID:           99,
		enterSelectDungeonContextSent: true,
	}
	bindDungeonSelectorOriginForTestAt(t, service, session, 38, 1, 450, 234)
	body := make([]byte, dungeoncmd.SelectDungeonRequestSize)
	binary.LittleEndian.PutUint32(body[0:4], 700)
	binary.LittleEndian.PutUint16(body[9:11], 0xffff)
	if err := service.handleDungeonSelectUpper(session, body); err != nil {
		t.Fatalf("select dungeon: %v", err)
	}
	return append([]byte(nil), connection.write.Bytes()...)
}

func TestDungeonTournamentSwitchOffKeepsEntryStreamByteIdentical(t *testing.T) {
	streams := map[string][]byte{}
	for _, value := range []string{"", "off", "OFF", "sausages"} {
		t.Setenv(dungeonTournamentEnvKey, value)
		streams[value] = runDungeonSelectStreamForTournamentControl(t)
	}
	baseline := streams[""]
	if len(baseline) == 0 {
		t.Fatal("control entry produced no packets")
	}
	// 同一条路径必须真的走完 op16 ACK → op5 → op28 → op29。
	wantMsgIDs := []uint16{
		uint16(dnfenum.CmdPacketSelectDungeon),
		currentDungeonResourceStateMsgID,
		currentDungeonInfoNotification,
		currentDungeonStartNotification,
	}
	rest := baseline
	seen := make([]uint16, 0, len(wantMsgIDs))
	for _, want := range wantMsgIDs {
		found := false
		for len(rest) > 0 {
			packet, trailing := splitGameServerUpperPacket(t, rest)
			rest = trailing
			seen = append(seen, packet.Header.MsgID)
			if packet.Header.MsgID == want {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("control stream misses msg %d; seen=%v body=%s", want, seen, hex.EncodeToString(baseline))
		}
	}
	for value, stream := range streams {
		if !bytes.Equal(stream, baseline) {
			t.Fatalf("switch=%q changed the entry byte stream:\n got=%s\nwant=%s",
				value, hex.EncodeToString(stream), hex.EncodeToString(baseline))
		}
	}
}
