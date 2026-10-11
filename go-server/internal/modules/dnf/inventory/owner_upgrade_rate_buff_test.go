package inventory

import (
	"context"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
	"longheng.io/server/internal/modules/dnf/dnfenum"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	dnfrepomemory "longheng.io/server/internal/modules/dnf/repository/memory"
)

// regexpSuccessWeight 从 handler 的 reason= 里反读**实际参与掷点**的成功率权重
// （handler 不透出领域层 UpgradeResult，而 reason= 是运维复测时唯一的现场口径）。
var regexpSuccessWeight = regexp.MustCompile(`success_weight=(\d+)`)

func mustAtoiTest(t *testing.T, raw string) int {
	t.Helper()
	value, err := strconv.Atoi(raw)
	if err != nil {
		t.Fatalf("atoi %q: %v", raw, err)
	}
	return value
}

// 「强化的奥秘 / 增幅秘药」这一族（`[stackable type] [item upgrad buff usual upgrade]`
// / `[item upgrad buff amplify upgrade]`）的服务端成功率加成回归。
//
// 上游 2026-09-29 确认口径：**`newRate = oldRate × 1.1`（乘算）**，封顶 100%，
// 同模式只取一件（最高），只作用于强化与增幅两条路径。见 upgrade_rate_buff.go。

const (
	rateBuffReinforceItemID int64 = 490006368 // 强化的奥秘（usual，30 分钟）
	rateBuffAmplifyItemID   int64 = 490001534 // 永恒的增幅的秘密（amplify，30 分钟）
)

func rateBuffResolution(itemID int64, mode string, bonus int, durationMillis int64) alignedcmd.UpgradeRateBuffResolution {
	return alignedcmd.UpgradeRateBuffResolution{
		Mode:           mode,
		BonusWeight:    bonus,
		DurationMillis: durationMillis,
		PVFPath:        "stackable/fixture/rate_buff.stk",
	}
}

func rateBuffStack(itemID int64, count int64, pvfPath string) dnfrepo.ItemStack {
	stackableType := "[item upgrad buff usual upgrade] 1000"
	if itemID == rateBuffAmplifyItemID {
		stackableType = "[item upgrad buff amplify upgrade] 1000"
	}
	return dnfrepo.ItemStack{
		ItemID: itemID,
		Count:  count,
		Extra: map[string]string{
			"item_kind":      "stackable",
			"pvf_path":       pvfPath,
			"stackable_type": stackableType,
		},
	}
}

func rateBuffResolver(table map[int64]alignedcmd.UpgradeRateBuffResolution) alignedcmd.UpgradeRateBuffResolver {
	return func(itemID int64) (alignedcmd.UpgradeRateBuffResolution, error) {
		if resolution, ok := table[itemID]; ok {
			return resolution, nil
		}
		return alignedcmd.UpgradeRateBuffResolution{}, nil
	}
}

// op44 body（DecodeUseStackableRequest：i16 slot + u8 list + u32 instance + u32 item + u32 reserved）。
func useStackableBody(slot int16, itemID uint32, instance uint32) []byte {
	return []byte{
		byte(slot), byte(slot >> 8),
		byte(listTypeMain),
		byte(instance), byte(instance >> 8), byte(instance >> 16), byte(instance >> 24),
		byte(itemID), byte(itemID >> 8), byte(itemID >> 16), byte(itemID >> 24),
		0, 0, 0, 0,
	}
}

// ① op44 使用药水：扣 1 张 + 把 buff 记进角色记录（同一个事务），日志带 rate_buff=。
func TestHandlerUseStackableRecordsUpgradeRateBuff(t *testing.T) {
	t.Setenv(UpgradeRateBuffBoostEnv, "")
	ctx := context.Background()
	repos := dnfrepomemory.NewMemoryGroup()
	saveTestCharacter(t, ctx, repos, dnfrepo.CharacterRecord{
		CharacterID: "77", AccountID: "acc", Stats: map[string]int64{"gold": 12345},
	})
	saveTestInventory(t, ctx, repos, dnfrepo.InventoryRecord{
		CharacterID: "77",
		Slots: map[string]dnfrepo.ItemStack{
			slotKey(listTypeMain, 130): rateBuffStack(rateBuffReinforceItemID, 3, "stackable/490006001/chn_490006368.stk"),
		},
	})
	before := time.Now().Unix()
	got, err := NewHandler().Handle(ctx, alignedcmd.Request{
		Opcode:              uint16(dnfenum.CmdPacketUseStackable),
		Body:                useStackableBody(130, uint32(rateBuffReinforceItemID), 4242),
		AccountID:           "acc",
		SelectedCharacterID: 77,
		Repositories:        repos,
		UpgradeRateBuffResolver: rateBuffResolver(map[int64]alignedcmd.UpgradeRateBuffResolution{
			rateBuffReinforceItemID: rateBuffResolution(rateBuffReinforceItemID, "reinforce", 10000, 1800000),
		}),
	})
	if err != nil {
		t.Fatalf("Handle error = %v", err)
	}
	if !got.Handled || len(got.UpperResponses) == 0 {
		t.Fatalf("result = %+v", got)
	}
	if !strings.Contains(got.Reason, "rate_buff=reinforce bonus=10000") {
		t.Fatalf("reason must name the recorded buff: %q", got.Reason)
	}
	if !strings.Contains(got.Reason, "boost_env="+UpgradeRateBuffBoostEnv) {
		t.Fatalf("reason must name the boost switch: %q", got.Reason)
	}
	loaded := loadTestInventory(t, ctx, repos, "77")
	if got := loaded.Slots[slotKey(listTypeMain, 130)].Count; got != 2 {
		t.Fatalf("potion count = %d, want 2", got)
	}
	character, found, err := repos.Character.Load(ctx, "77")
	if err != nil || !found {
		t.Fatalf("load character ok=%t err=%v", found, err)
	}
	until := character.Stats["upgrade_rate_buff_reinforce_until_unix"]
	bonus := character.Stats["upgrade_rate_buff_reinforce_bonus"]
	if bonus != 10000 {
		t.Fatalf("recorded bonus = %d, want 10000", bonus)
	}
	if until < before+1800-5 || until > before+1800+5 {
		t.Fatalf("recorded until = %d, want ~%d (now + 1800s)", until, before+1800)
	}
	if got := character.Stats["upgrade_rate_buff_reinforce_item_id"]; got != rateBuffReinforceItemID {
		t.Fatalf("recorded item id = %d, want %d", got, rateBuffReinforceItemID)
	}
	if character.Stats["upgrade_rate_buff_amplify_bonus"] != 0 {
		t.Fatalf("an amplify buff must not be recorded by a reinforce potion: %+v", character.Stats)
	}
}

// ② op44 对**普通**消耗品不改任何东西（逐字节既有行为）：解析器说"不是 buff 药水"时
// 既没有 rate_buff= 日志，也没有任何 Stats 键。
func TestHandlerUseStackableWithoutRateBuffKeepsLegacyShape(t *testing.T) {
	t.Setenv(UpgradeRateBuffBoostEnv, "")
	ctx := context.Background()
	repos := dnfrepomemory.NewMemoryGroup()
	saveTestCharacter(t, ctx, repos, dnfrepo.CharacterRecord{
		CharacterID: "77", AccountID: "acc", Stats: map[string]int64{"gold": 12345},
	})
	saveTestInventory(t, ctx, repos, dnfrepo.InventoryRecord{
		CharacterID: "77",
		Slots: map[string]dnfrepo.ItemStack{
			slotKey(listTypeMain, 130): {ItemID: 490001533, Count: 3, Extra: map[string]string{"item_kind": "stackable", "pvf_path": "stackable/490001001/chn_490001533.stk", "stackable_type": "[waste] 4"}},
		},
	})
	got, err := NewHandler().Handle(ctx, alignedcmd.Request{
		Opcode:                  uint16(dnfenum.CmdPacketUseStackable),
		Body:                    useStackableBody(130, 490001533, 4242),
		AccountID:               "acc",
		SelectedCharacterID:     77,
		Repositories:            repos,
		UpgradeRateBuffResolver: rateBuffResolver(nil), // 一件都不认
	})
	if err != nil {
		t.Fatalf("Handle error = %v", err)
	}
	if strings.Contains(got.Reason, "rate_buff=") {
		t.Fatalf("a non-buff use must not log rate_buff: %q", got.Reason)
	}
	character, _, err := repos.Character.Load(ctx, "77")
	if err != nil {
		t.Fatal(err)
	}
	for key := range character.Stats {
		if strings.HasPrefix(key, "upgrade_rate_buff_") {
			t.Fatalf("unexpected buff key %q in %+v", key, character.Stats)
		}
	}
}

// ③ op50 掷点：基础成功率 13000 × 1.1 = 14300（乘算，不是 13000+10000）。
func TestHandlerUpgradeAppliesRateBuffMultiplicatively(t *testing.T) {
	got, _, result := runRateBuffUpgrade(t, rateBuffUpgradeCase{
		mode:          "reinforce",
		baseWeight:    13000,
		stats:         map[string]int64{"upgrade_rate_buff_reinforce_bonus": 10000, "upgrade_rate_buff_reinforce_until_unix": time.Now().Unix() + 600},
		successWeight: 100000, // 必定成功，只为把最终权重打进日志
	})
	if !strings.Contains(got.Reason, "rate_buff_bonus=10000") {
		t.Fatalf("reason must report the applied buff: %q", got.Reason)
	}
	if !strings.Contains(got.Reason, "success_weight=14300") || result.UpgradeSuccessWeight != 14300 {
		t.Fatalf("success weight = %d (reason %q), want 14300 = 13000 × 1.1", result.UpgradeSuccessWeight, got.Reason)
	}
}

// ④ 过期 / 缺键 / 别的模式：一律不加成（逐字节回到既有行为）。
func TestHandlerUpgradeRateBuffExpiryModeIsolationAndAbsence(t *testing.T) {
	now := time.Now().Unix()
	cases := []struct {
		name  string
		mode  string
		stats map[string]int64
	}{
		{name: "expired", mode: "reinforce", stats: map[string]int64{
			"upgrade_rate_buff_reinforce_bonus": 10000, "upgrade_rate_buff_reinforce_until_unix": now - 1}},
		{name: "amplify buff must not boost reinforce", mode: "reinforce", stats: map[string]int64{
			"upgrade_rate_buff_amplify_bonus": 10000, "upgrade_rate_buff_amplify_until_unix": now + 600}},
		{name: "reinforce buff must not boost amplify", mode: "amplify", stats: map[string]int64{
			"upgrade_rate_buff_reinforce_bonus": 10000, "upgrade_rate_buff_reinforce_until_unix": now + 600}},
		{name: "no record at all", mode: "reinforce", stats: nil},
		{name: "until set but bonus missing", mode: "reinforce", stats: map[string]int64{
			"upgrade_rate_buff_reinforce_until_unix": now + 600}},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			got, _, result := runRateBuffUpgrade(t, rateBuffUpgradeCase{
				mode:          test.mode,
				baseWeight:    13000,
				stats:         test.stats,
				successWeight: 100000,
			})
			if result.UpgradeSuccessWeight != 13000 {
				t.Fatalf("success weight = %d, want the untouched 13000", result.UpgradeSuccessWeight)
			}
			if strings.Contains(got.Reason, "rate_buff_bonus=") {
				t.Fatalf("reason must not report a buff: %q", got.Reason)
			}
		})
	}
}

// ⑤ 封顶：基础 95000 × 1.1 = 104500 → 封顶 100000（= 必定成功，永不与失败惩罚档错位）。
func TestHandlerUpgradeRateBuffIsCappedAtFull(t *testing.T) {
	_, _, result := runRateBuffUpgrade(t, rateBuffUpgradeCase{
		mode:          "reinforce",
		baseWeight:    95000,
		stats:         map[string]int64{"upgrade_rate_buff_reinforce_bonus": 10000, "upgrade_rate_buff_reinforce_until_unix": time.Now().Unix() + 600},
		successWeight: 100000,
	})
	if result.UpgradeSuccessWeight != upgradeSuccessWeightScale {
		t.Fatalf("success weight = %d, want the 100000 cap", result.UpgradeSuccessWeight)
	}
}

// ⑥ 环境覆盖：DNFBRIDGE_UPGRADE_SUCCESS_BOOST=1.5 ⇒ ×1.5；
// `1` / `off` / `none` / `legacy` ⇒ 不加成（回退字面量）；无法解析 ⇒ 默认档（PVF 值）。
func TestUpgradeRateBuffBoostEnvOverride(t *testing.T) {
	now := time.Now().Unix()
	stats := map[string]int64{"upgrade_rate_buff_reinforce_bonus": 10000, "upgrade_rate_buff_reinforce_until_unix": now + 600}
	cases := []struct {
		env  string
		want int
	}{
		{env: "", want: 14300},     // 默认档：用 PVF 的 10000
		{env: "1.1", want: 14300},  // 显式覆盖成同一个乘数
		{env: "1.5", want: 19500},  // ×1.5
		{env: "1", want: 13000},    // 回退：不加成
		{env: "1.0", want: 13000},  //
		{env: "off", want: 13000},  //
		{env: "none", want: 13000}, //
		{env: "legacy", want: 13000},
		{env: "0", want: 13000},
		{env: "banana", want: 14300}, // 无法解析 ⇒ 默认档（绝不静默变成"不加成"）
	}
	for _, test := range cases {
		t.Run("env="+test.env, func(t *testing.T) {
			_, _, result := runRateBuffUpgrade(t, rateBuffUpgradeCase{
				mode:          "reinforce",
				baseWeight:    13000,
				stats:         stats,
				successWeight: 100000,
				boostEnv:      test.env,
			})
			if result.UpgradeSuccessWeight != test.want {
				t.Fatalf("env=%q success weight = %d, want %d", test.env, result.UpgradeSuccessWeight, test.want)
			}
		})
	}
}

// ⑦ 叠加规则：同模式重复使用只保留**最高**的一件（不会 ×1.1×1.1）。
func TestRecordUpgradeRateBuffTakesTheHighestNotTheProduct(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	character := dnfrepo.CharacterRecord{CharacterID: "77", Stats: map[string]int64{}}
	if _, bonus, ok := recordUpgradeRateBuff(&character, rateBuffResolution(1, "reinforce", 10000, 1800000), 11, now); !ok || bonus != 10000 {
		t.Fatalf("first record bonus=%d ok=%t", bonus, ok)
	}
	// 第二件加成更低：保留 10000（取最高），只把到期时间往后推。
	if _, bonus, ok := recordUpgradeRateBuff(&character, rateBuffResolution(2, "reinforce", 5000, 600000), 22, now.Add(time.Minute)); !ok || bonus != 10000 {
		t.Fatalf("second (lower) record bonus=%d ok=%t, want the higher 10000 kept", bonus, ok)
	}
	if got := character.Stats["upgrade_rate_buff_reinforce_bonus"]; got != 10000 {
		t.Fatalf("bonus after two uses = %d, want 10000 (never ×1.1×1.1)", got)
	}
	if got := character.Stats["upgrade_rate_buff_reinforce_until_unix"]; got != now.Add(time.Minute).Unix()+600 {
		t.Fatalf("until after the second use = %d, want the later expiry", got)
	}
	// 第三件加成更高：升到 20000。
	if _, bonus, ok := recordUpgradeRateBuff(&character, rateBuffResolution(3, "reinforce", 20000, 600000), 33, now); !ok || bonus != 20000 {
		t.Fatalf("third (higher) record bonus=%d ok=%t, want 20000", bonus, ok)
	}
	// 已过期的旧账不参与"取最高"。
	character.Stats["upgrade_rate_buff_reinforce_until_unix"] = now.Unix() - 1
	if _, bonus, ok := recordUpgradeRateBuff(&character, rateBuffResolution(4, "reinforce", 7000, 600000), 44, now); !ok || bonus != 7000 {
		t.Fatalf("after expiry bonus=%d ok=%t, want the fresh 7000", bonus, ok)
	}
	// 缺持续时间 = 不记账（fail closed：绝不凭空编一个时长）。
	if _, _, ok := recordUpgradeRateBuff(&character, rateBuffResolution(5, "reinforce", 10000, 0), 55, now); ok {
		t.Fatal("a PVF without [stat change duration] must not be recorded")
	}
}

type rateBuffUpgradeCase struct {
	mode          string
	baseWeight    int
	stats         map[string]int64
	successWeight int
	boostEnv      string
}

// runRateBuffUpgrade 走 handler 的 op50 口径，返回（结果, 背包, 领域层 UpgradeResult 的等价事实）。
// 领域层结果通过 reason 与 ACK 反读（handler 不把它透出来），这里用一个小包装把
// reason 里的 success_weight 抽出来给用例断言。
func runRateBuffUpgrade(t *testing.T, fixture rateBuffUpgradeCase) (alignedcmd.Result, dnfrepo.InventoryRecord, rateBuffUpgradeOutcome) {
	t.Helper()
	t.Setenv(UpgradeRateBuffBoostEnv, fixture.boostEnv)
	ctx := context.Background()
	for attempt := 0; attempt < 64; attempt++ {
		repos := dnfrepomemory.NewMemoryGroup()
		stats := map[string]int64{"gold": 12345}
		for key, value := range fixture.stats {
			stats[key] = value
		}
		saveTestCharacter(t, ctx, repos, dnfrepo.CharacterRecord{CharacterID: "77", AccountID: "acc", Stats: stats})
		extra := map[string]string{
			"item_kind":      "equipment",
			"durability":     "45",
			"max_durability": "45",
		}
		if fixture.mode == "amplify" {
			extra["amplify_type"] = "3"
			extra["amplify_value"] = "5"
		}
		raw := make([]byte, currentItemListEntrySize)
		raw[0x0A] = 10
		saveTestInventory(t, ctx, repos, dnfrepo.InventoryRecord{
			CharacterID: "77",
			Slots: map[string]dnfrepo.ItemStack{
				slotKey(listTypeMain, 9): {ItemID: 700, Count: 1, RawEntry: raw, Extra: extra},
			},
		})
		body := []byte{
			0x00, 0x00,
			0x09, 0x00,
			0xBC, 0x02, 0x00, 0x00,
			0xFF, 0xFF,
			0xFF, 0xFF,
			0x00, 0x00, 0x00, 0x00,
		}
		if fixture.mode == "amplify" {
			body[0] = 0x01
		}
		got, err := NewHandler().Handle(ctx, alignedcmd.Request{
			Opcode:              uint16(dnfenum.CmdPacketUpgradeItem),
			Body:                body,
			AccountID:           "acc",
			SelectedCharacterID: 77,
			Repositories:        repos,
			UpgradePolicyResolver: func(mode string, currentLevel int) (alignedcmd.UpgradePolicyResolution, error) {
				return alignedcmd.UpgradePolicyResolution{
					SuccessWeight: fixture.baseWeight, PenaltyType: 3,
				}, nil
			},
		})
		if err != nil {
			t.Fatalf("Handle error = %v", err)
		}
		ack := got.UpperResponses[0]
		wantSuccess := fixture.successWeight >= upgradeSuccessWeightScale
		if len(ack.Body) > 12 && (ack.Body[12] == upgradeResultCodeSuccess) == wantSuccess {
			outcome := rateBuffUpgradeOutcome{reason: got.Reason}
			if m := regexpSuccessWeight.FindStringSubmatch(got.Reason); len(m) == 2 {
				outcome.UpgradeSuccessWeight = mustAtoiTest(t, m[1])
			}
			return got, loadTestInventory(t, ctx, repos, "77"), outcome
		}
	}
	t.Fatalf("64 次都没落到期望档位")
	return alignedcmd.Result{}, dnfrepo.InventoryRecord{}, rateBuffUpgradeOutcome{}
}

type rateBuffUpgradeOutcome struct {
	reason               string
	UpgradeSuccessWeight int
}
