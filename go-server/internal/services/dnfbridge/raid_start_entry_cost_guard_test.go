package dnfbridge

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"longheng.io/server/internal/modules/dnf/channelcatalog"
	"longheng.io/server/internal/modules/dnf/raid"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

// 2026-10-02 (V20)：开战入场材料闸门。
// 现场缺口是 handleOnlineStartRaid 【从不校验、也从不扣入场材料】——没票也能开战。
// 这一组用例钉住三档行为：log 只观测不改包、enforce 不足即拒绝（且不标记开始）、
// enforce 齐备才真扣（跨多堆叠合并扣、扣空即删格）。
// 真实规则来自 etc/raid/anton.etc 的 `[ENTRY COST] 10096296 1`。

func TestEvaluateRaidStartCostPartitionsReadyAndShort(t *testing.T) {
	rule := raid.AdmissionRule{ItemID: 10096296, Count: 2}
	statuses, short := evaluateRaidStartCost(rule, []raidStartMemberCost{
		{CharacterID: 11, Owned: 2},
		{CharacterID: 12, Owned: 1},
		{CharacterID: 13, Owned: 7},
	})
	if len(statuses) != 3 || len(short) != 1 || short[0] != 12 {
		t.Fatalf("partition wrong: statuses=%+v short=%v", statuses, short)
	}
	if !statuses[0].Ready || statuses[1].Ready || !statuses[2].Ready {
		t.Fatalf("ready flags wrong: %+v", statuses)
	}
	if statuses[1].OwnedCount != 1 || statuses[2].OwnedCount != 7 {
		t.Fatalf("owned counts wrong: %+v", statuses)
	}
}

func TestEvaluateRaidStartCostCapsOwnedAtUint16AndClampsNegative(t *testing.T) {
	rule := raid.AdmissionRule{ItemID: 1, Count: 1}
	statuses, short := evaluateRaidStartCost(rule, []raidStartMemberCost{
		{CharacterID: 1, Owned: 70000},
		{CharacterID: 2, Owned: -5},
	})
	if len(short) != 1 || short[0] != 2 {
		t.Fatalf("short=%v want [2]", short)
	}
	if statuses[0].OwnedCount != 65535 || statuses[1].OwnedCount != 0 {
		t.Fatalf("clamp/cap wrong: %+v", statuses)
	}
}

func TestRaidEntryCostOwnedCountCountsOnlyMainInventoryStacks(t *testing.T) {
	inventory := dnfrepo.InventoryRecord{CharacterID: "19", Slots: map[string]dnfrepo.ItemStack{
		"0:10":  {ItemID: 10096296, Count: 3},
		"0:40":  {ItemID: 10096296, Count: 4},
		"1:11":  {ItemID: 10096296, Count: 9}, // 换装/装扮栏不算
		"0:12":  {ItemID: 999, Count: 9},      // 别的道具不算
		"0:abc": {ItemID: 10096296, Count: 9}, // 键不合法不算
		"0:13":  {ItemID: 10096296, Count: 0}, // 空堆叠不算
	}}
	if got := raidEntryCostOwnedCount(inventory, 10096296); got != 7 {
		t.Fatalf("owned=%d want 7", got)
	}
}

func TestRaidStartGuardModeParsingHasExplicitRollback(t *testing.T) {
	for raw, want := range map[string]string{
		"":          raidStartEntryCostGuardLog, // 默认档 = 只观测
		"log":       raidStartEntryCostGuardLog,
		"enforce":   raidStartEntryCostGuardEnforce,
		"1":         raidStartEntryCostGuardEnforce,
		"true":      raidStartEntryCostGuardEnforce,
		"off":       raidStartEntryCostGuardOff,
		"0":         raidStartEntryCostGuardOff,
		"legacy":    raidStartEntryCostGuardOff,
		"disabled":  raidStartEntryCostGuardOff,
		"nonsense":  raidStartEntryCostGuardLog,
		"  ENFORCE": raidStartEntryCostGuardEnforce,
	} {
		t.Setenv(raidStartEntryCostGuardEnv, raw)
		if got := raidStartEntryCostGuardMode(); got != want {
			t.Fatalf("env=%q mode=%q want %q", raw, got, want)
		}
	}
}

const raidStartGuardTestMaterial = 10096296 // etc/raid/anton.etc [ENTRY COST]

func TestOfficialRaidStartCannotUseLegacyObservationOrRollback(t *testing.T) {
	for _, raw := range []string{"", "log", "off", "legacy", "nonsense", "enforce"} {
		t.Setenv(raidStartEntryCostGuardEnv, raw)
		service := &Service{options: options{currentClientVersion: "official-24.9"}}
		if service.raidStartEntryCostMode() != raidStartEntryCostGuardEnforce {
			t.Fatalf("official admission bypass for legacy setting %q", raw)
		}
		// No actual PVF/transaction is available: official admission must fail
		// before any inventory write, even when the legacy profile is off.
		allowed, err := service.guardRaidStartEntryCost(&gameSession{}, runtimeRaidState{})
		if allowed || err == nil {
			t.Fatalf("official missing dependencies allowed=%v error=%v setting=%q", allowed, err, raw)
		}
		legacy := &Service{}
		if legacy.raidStartEntryCostMode() != raidStartEntryCostGuardMode() {
			t.Fatal("legacy profile mode changed")
		}
	}
}

type raidStartGuardFixture struct {
	service *Service
	repos   dnfrepo.Group
	state   *runtimeRaidState
	conn    *bufferConn
	logPath string
	logger  *packetLogger
}

func newRaidStartGuardFixture(t *testing.T, archive *platformpvf.Archive, slots map[string]dnfrepo.ItemStack) *raidStartGuardFixture {
	t.Helper()
	repos := mustCurrentNPCShopRepositories(t, 1000, slots)
	ctx := context.Background()
	if err := repos.Character.Save(ctx, dnfrepo.CharacterRecord{CharacterID: "19", AccountID: "account-1"}); err != nil {
		t.Fatal(err)
	}
	if err := repos.Inventory.Save(ctx, dnfrepo.InventoryRecord{CharacterID: "19", Slots: cloneRaidStartGuardSlots(slots)}); err != nil {
		t.Fatal(err)
	}
	state := &runtimeRaidState{RaidKey: 23<<16 | 19, LeaderID: 19, TailFlag: 1, Members: []runtimeRaidMemberState{{CharID: 19}}}
	logPath := filepath.Join(t.TempDir(), "packet.log")
	logger, err := openPacketLogger(logPath)
	if err != nil {
		t.Fatal(err)
	}
	service := &Service{
		options:                 options{gameUpperHeader: gameUpperHeaderChannel13, gameUpperBodyCodec: gameUpperBodyCodecPlain},
		initialEquipmentArchive: archive,
		repositoryProvider:      func() (dnfrepo.Group, bool) { return repos, true },
		raids:                   map[uint32]*runtimeRaidState{state.RaidKey: state},
		packetLog:               logger,
	}
	return &raidStartGuardFixture{
		service: service, repos: repos, state: state, conn: &bufferConn{},
		logPath: logPath, logger: logger,
	}
}

func cloneRaidStartGuardSlots(slots map[string]dnfrepo.ItemStack) map[string]dnfrepo.ItemStack {
	out := make(map[string]dnfrepo.ItemStack, len(slots))
	for key, stack := range slots {
		out[key] = stack
	}
	return out
}

func (f *raidStartGuardFixture) session() *gameSession {
	return &gameSession{conn: f.conn, selectedCharacterID: 19, accountID: "account-1",
		channel: channelcatalog.Channel{ID: 23, Type: 23}}
}

// logText 关掉 logger 后读回判读行。
func (f *raidStartGuardFixture) logText(t *testing.T) string {
	t.Helper()
	if err := f.logger.Close(); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(f.logPath)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func (f *raidStartGuardFixture) slotCount(t *testing.T, key string) (int64, bool) {
	t.Helper()
	inventory, found, err := f.repos.Inventory.Load(context.Background(), "19")
	if err != nil || !found {
		t.Fatalf("inventory load found=%v err=%v", found, err)
	}
	stack, exists := inventory.Slots[key]
	return stack.Count, exists
}

func TestRaidStartEntryCostGuardEndToEnd(t *testing.T) {
	path := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if path == "" {
		t.Skip("requires current PVF")
	}
	archive, err := platformpvf.Open(path)
	if err != nil {
		t.Fatal(err)
	}

	t.Run("log_mode_observes_but_never_blocks_or_consumes", func(t *testing.T) {
		t.Setenv(raidStartEntryCostGuardEnv, raidStartEntryCostGuardLog)
		fixture := newRaidStartGuardFixture(t, archive, map[string]dnfrepo.ItemStack{})
		allowed, err := fixture.service.guardRaidStartEntryCost(fixture.session(), *fixture.state)
		if err != nil || !allowed {
			t.Fatalf("log 档必须放行: allowed=%v err=%v", allowed, err)
		}
		text := fixture.logText(t)
		if !strings.Contains(text, "kind=game-raid-start-entry-cost-guard") ||
			!strings.Contains(text, "decision=observe") ||
			!strings.Contains(text, "reason=entry_cost_short") {
			t.Fatalf("log 档必须记 observe 判读行:\n%s", text)
		}
		if fixture.state.Started {
			t.Fatal("闸门自身不得改动 Started 状态")
		}
	})

	t.Run("off_mode_returns_before_reading_the_rule", func(t *testing.T) {
		t.Setenv(raidStartEntryCostGuardEnv, raidStartEntryCostGuardOff)
		// 故意不给 PVF：off 档必须在读规则之前返回，才谈得上"逐字节等于改动前"。
		service := &Service{raids: map[uint32]*runtimeRaidState{}}
		conn := &bufferConn{}
		session := &gameSession{conn: conn, selectedCharacterID: 19, accountID: "account-1"}
		allowed, err := service.guardRaidStartEntryCost(session, runtimeRaidState{RaidKey: 1})
		if err != nil || !allowed {
			t.Fatalf("off 档必须直接放行: allowed=%v err=%v", allowed, err)
		}
		if conn.write.Len() != 0 {
			t.Fatalf("off 档不得写任何日志:\n%s", conn.write.String())
		}
	})

	t.Run("enforce_mode_rejects_short_member_without_starting", func(t *testing.T) {
		t.Setenv(raidStartEntryCostGuardEnv, raidStartEntryCostGuardEnforce)
		// 无票：`[ENTRY COST] 10096296 1` 需要 1 个，这里 0 个。
		fixture := newRaidStartGuardFixture(t, archive, map[string]dnfrepo.ItemStack{})
		// 走完整开战入口：拒绝必须发生在 markRuntimeRaidStarted 之前。
		if err := fixture.service.handleOnlineStartRaid(fixture.session(), 666, nil); err != nil {
			t.Fatal(err)
		}
		if fixture.state.Started {
			t.Fatal("材料不足时不得标记已开始")
		}
		text := fixture.logText(t)
		if !strings.Contains(text, "decision=reject") || !strings.Contains(text, "reason=entry_cost_short") {
			t.Fatalf("enforce 档必须记 reject:\n%s", text)
		}
	})

	t.Run("enforce_mode_consumes_then_allows_when_ready", func(t *testing.T) {
		t.Setenv(raidStartEntryCostGuardEnv, raidStartEntryCostGuardEnforce)
		fixture := newRaidStartGuardFixture(t, archive, map[string]dnfrepo.ItemStack{
			"0:50": {ItemID: raidStartGuardTestMaterial, Count: 1},
			"0:51": {ItemID: raidStartGuardTestMaterial, Count: 3},
			"0:52": {ItemID: 4242, Count: 5},
		})
		allowed, err := fixture.service.guardRaidStartEntryCost(fixture.session(), *fixture.state)
		if err != nil || !allowed {
			t.Fatalf("材料齐备时必须放行: allowed=%v err=%v", allowed, err)
		}
		text := fixture.logText(t)
		if !strings.Contains(text, "kind=game-raid-start-entry-cost-consumed") ||
			!strings.Contains(text, "consumed_total=1") {
			t.Fatalf("必须记扣除汇总行:\n%s", text)
		}
		// 规则 Count=1 ⇒ 从 0:50 扣 1 ⇒ 该格（唯一一件）删除；0:51 与无关道具不动。
		if _, exists := fixture.slotCount(t, "0:50"); exists {
			t.Fatal("扣空的堆叠必须删格")
		}
		if count, _ := fixture.slotCount(t, "0:51"); count != 3 {
			t.Fatalf("第二堆叠不该被动: %d", count)
		}
		if count, _ := fixture.slotCount(t, "0:52"); count != 5 {
			t.Fatalf("无关道具不该被动: %d", count)
		}
	})
}

// 规则里的 Count 若大于 1，跨堆叠合并扣要正确（用合成规则直接打 consume 路径，
// 不依赖真实 PVF 的具体数字），并且不足时 fail-closed（一个都不扣）。
func TestConsumeRaidEntryCostMergesStacksAndFailsClosedWhenShort(t *testing.T) {
	repos := mustCurrentNPCShopRepositories(t, 1000, map[string]dnfrepo.ItemStack{})
	ctx := context.Background()
	if err := repos.Character.Save(ctx, dnfrepo.CharacterRecord{CharacterID: "19", AccountID: "account-1"}); err != nil {
		t.Fatal(err)
	}
	if err := repos.Inventory.Save(ctx, dnfrepo.InventoryRecord{CharacterID: "19", Slots: map[string]dnfrepo.ItemStack{
		"0:50": {ItemID: 777, Count: 2},
		"0:51": {ItemID: 777, Count: 2},
	}}); err != nil {
		t.Fatal(err)
	}
	service := &Service{repositoryProvider: func() (dnfrepo.Group, bool) { return repos, true }}
	rule := raid.AdmissionRule{ItemID: 777, Count: 3, WaitingTown: 1, WaitingArea: 1, MinimumMembers: 1, SoloMinimumMembers: 1}

	consumed, err := service.consumeRaidEntryCostForMember(ctx, "account-1", "19", rule)
	if err != nil || consumed != 3 {
		t.Fatalf("consumed=%d err=%v want 3/nil", consumed, err)
	}
	inventory, _, _ := repos.Inventory.Load(ctx, "19")
	if _, exists := inventory.Slots["0:50"]; exists {
		t.Fatalf("0:50 应被扣空删除: %+v", inventory.Slots)
	}
	if got := inventory.Slots["0:51"].Count; got != 1 {
		t.Fatalf("0:51 应剩 1: %+v", inventory.Slots)
	}

	// 再要 3 个时只剩 1 个 ⇒ fail-closed：一个都不扣。
	if _, err := service.consumeRaidEntryCostForMember(ctx, "account-1", "19", rule); err == nil {
		t.Fatal("材料不足时必须报错")
	}
	inventory, _, _ = repos.Inventory.Load(ctx, "19")
	if got := inventory.Slots["0:51"].Count; got != 1 {
		t.Fatalf("失败的扣除不得改动背包: %+v", inventory.Slots)
	}
}
