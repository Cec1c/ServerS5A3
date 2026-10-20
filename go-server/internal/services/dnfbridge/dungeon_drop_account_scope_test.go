package dnfbridge

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/premium"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

// 现场形态（香港 2026-09-25 账号串号期）：角色行建在**共享兜底账号** `dnf:1` 上，
// 而会话账号已由启动器令牌定案成 `dnf:10001`。副本里的**账号级**权益（成长契约）
// 必须按会话账号读。本文件钉住的不变式：
//
//	会话账号 ≠ 兜底账号（也不等于角色行上的账号）时，
//	账号级读取既不跟着角色行走，也不因为两者不同而失败。
const (
	dungeonDropAccountScopeSession = "dnf:10001"
	dungeonDropAccountScopeShared  = "dnf:1"
)

// TestCurrentDungeonDropAccountIDPrefersSessionAccount 钉住口径解析本身：会话账号
// 优先；只有 runtime 没带上会话账号时（历史路径 / 直接构造 runtime 的测试）才退回
// 角色行，与改动前逐字节同行为。
func TestCurrentDungeonDropAccountIDPrefersSessionAccount(t *testing.T) {
	tests := []struct {
		name    string
		runtime *runtimeDungeonState
		want    string
	}{
		{
			name: "session account wins over the character record",
			runtime: &runtimeDungeonState{
				AccountID: dungeonDropAccountScopeSession,
				Character: dnfrepo.CharacterRecord{AccountID: dungeonDropAccountScopeShared},
			},
			want: dungeonDropAccountScopeSession,
		},
		{
			name: "compat: runtime without a session account falls back to the character record",
			runtime: &runtimeDungeonState{
				Character: dnfrepo.CharacterRecord{AccountID: dungeonDropAccountScopeShared},
			},
			want: dungeonDropAccountScopeShared,
		},
		{
			name: "blank session account is not an account",
			runtime: &runtimeDungeonState{
				AccountID: "   ",
				Character: dnfrepo.CharacterRecord{AccountID: dungeonDropAccountScopeShared},
			},
			want: dungeonDropAccountScopeShared,
		},
		{
			name: "nil runtime",
			want: "",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := currentDungeonDropAccountID(test.runtime); got != test.want {
				t.Fatalf("account=%q want=%q", got, test.want)
			}
		})
	}
}

// TestCurrentDungeonDropGrowthContractReadsSessionAccount 是主回归：成长契约是
// **账号级** entitlement（dnf_accounts 上的 premium_expire_84），掉落规划必须按
// runtime 冻结的会话账号读它，而不是按角色行上的账号读。
//
// 两档互为反证，任一档在"退回 runtime.Character.AccountID"的变异下都会变红：
//
//	A 现场形态：契约在会话账号 dnf:10001 上，角色行仍是 dnf:1（无契约）
//	  ⇒ 掉落的成长契约加成必须是 20（旧口径读 dnf:1 ⇒ 0）
//	B 反向控制：契约只在共享兜底账号 dnf:1 上，会话账号没有
//	  ⇒ 加成必须是 0（旧口径读角色行 ⇒ 20，等于把别人的契约算到这名玩家头上）
func TestCurrentDungeonDropGrowthContractReadsSessionAccount(t *testing.T) {
	tests := []struct {
		name            string
		sessionContract bool
		sharedContract  bool
		wantLogged      string
	}{
		{
			name:            "contract on the session account",
			sessionContract: true,
			wantLogged:      "growth_contract_independent_drop_percent=20",
		},
		{
			name:           "contract only on the shared fallback account",
			sharedContract: true,
			wantLogged:     "growth_contract_independent_drop_percent=0",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			service, runtime, repositories := prepareCurrentDungeonDropTest(t, 0, 3227)
			service.premiumCatalog = &currentPremiumCatalog{
				effectsByType: map[int64]currentPremiumEffectInfo{
					// [independent drop rate] 的第一档（1 人队）＝ 20%。
					premium.TypeBonusExp: {IndependentDropRatePercents: []int64{20}},
				},
			}
			future := strconv.FormatInt(time.Now().Add(24*time.Hour).Unix(), 10)
			for _, account := range []struct {
				id     string
				active bool
			}{
				{id: dungeonDropAccountScopeSession, active: test.sessionContract},
				{id: dungeonDropAccountScopeShared, active: test.sharedContract},
			} {
				record := dnfrepo.AccountRecord{AccountID: account.id}
				if account.active {
					record.Metadata = map[string]string{"premium_expire_84": future}
				}
				if err := repositories.Account.Save(context.Background(), record); err != nil {
					t.Fatal(err)
				}
			}
			// 角色行落后于账号定案：runtime 上的会话账号 = dnf:10001，角色行仍是 dnf:1。
			runtime.AccountID = dungeonDropAccountScopeSession
			runtime.Character.AccountID = dungeonDropAccountScopeShared

			monster := runtime.Room.Snapshot().Monsters[0]
			source, ok := service.dungeonMonsterTable.source.(bridgePVFSource)
			if !ok {
				t.Fatalf("test source type=%T", service.dungeonMonsterTable.source)
			}
			source[dungeonCardDropRulePath] = currentDungeonGoldDropTestRuleText()
			source[dungeonCardGoldReferencePath] = "[gold drop ref table]\n20 100 0\n"
			// [independent drop] 记录：0 <怪> <物品> <难度0概率> 0 0 0 0 <尝试次数> 0 0 0 0 <等级下限> <等级上限> <难度> 0
			// 概率满档（1000000）⇒ 该记录是否命中与加成无关，日志里的加成读数才是判别量。
			source[currentDungeonIndependentDropPath] = "[independent drop]\n0 " +
				strconv.FormatInt(monster.Spawn.MonsterID, 10) +
				" 3227 1000000 0 0 0 0 1 0 0 0 0 1 90 -1 0\n[/independent drop]\n"
			currentDungeonMonsterDropRulesByCatalog.Delete(service.dungeonMonsterTable)
			currentDungeonSpecialDropsByMonsterCatalog.Delete(service.dungeonMonsterTable)

			// 包日志是这条读数的唯一出口（planCurrentDungeonMonsterDrops 用
			// logGameEvent(nil, …) 写事件），所以这里落一份临时包日志再读回。
			logDir, err := os.MkdirTemp("", "dnfbridge-account-scope")
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = os.RemoveAll(logDir) }()
			logPath := filepath.Join(logDir, "account-scope-packet-log.txt")
			logger, err := openPacketLogger(logPath)
			if err != nil {
				t.Fatal(err)
			}
			if logger == nil {
				t.Fatal("packet logger was not opened")
			}
			service.packetLog = logger
			if _, err := service.planCurrentDungeonMonsterDrops(runtime, monster, currentSceneActorObjectKey(99)); err != nil {
				t.Fatal(err)
			}
			if err := logger.Close(); err != nil {
				t.Fatal(err)
			}
			data, err := os.ReadFile(logPath)
			if err != nil {
				t.Fatal(err)
			}
			logged := string(data)
			if !strings.Contains(logged, "game-dungeon-monster-special-drop-roll") {
				t.Fatalf("special-drop roll event missing from packet log:\n%s", logged)
			}
			if !strings.Contains(logged, test.wantLogged) {
				t.Fatalf("packet log does not carry %q (session_account=%s character_account=%s):\n%s",
					test.wantLogged, runtime.AccountID, runtime.Character.AccountID, logged)
			}
		})
	}
}
