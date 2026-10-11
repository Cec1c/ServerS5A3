package gmtool

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

// ---------------------------------------------------------------------------
// A：账号金库三条路由（POST …/cargo/{max,clear,delete}）的测试台
//
// 为什么直接调 cargoApplyXxx 而不是打整条路由：三条路由的前半段都要用 s.db 查启动器
// 账号表（cargoAccountRequest → deps.go:290-332 webAccounts），单测里没有数据库。
// cargoApplyXxx 就是"账号已解析之后"的全部行为（与 api_account_cargo_gold_test.go:17-24
// 对 applyAccountCurrency 的同一取舍）。
// ---------------------------------------------------------------------------

// cargoCountingAccountStore 统计整记录写与键级写的次数，用来钉死 CARGO-D3。
type cargoCountingAccountStore struct {
	dnfrepo.AccountRepository
	saves          int
	metadataWrites int
	keys           []string
}

func (s *cargoCountingAccountStore) Save(ctx context.Context, record dnfrepo.AccountRecord) error {
	s.saves++
	return s.AccountRepository.Save(ctx, record)
}

func (s *cargoCountingAccountStore) SaveMetadataEntry(
	ctx context.Context,
	accountID string,
	key string,
	value string,
	updatedAt time.Time,
) error {
	writer, ok := s.AccountRepository.(dnfrepo.AccountMetadataEntryWriter)
	if !ok {
		return errors.New("underlying account store does not support key-level metadata writes")
	}
	s.metadataWrites++
	s.keys = append(s.keys, key)
	return writer.SaveMetadataEntry(ctx, accountID, key, value, updatedAt)
}

type cargoHarness struct {
	*gmtoolWriteHarness
	account *cargoCountingAccountStore
}

// newCargoHarness 建"账号 + 角色 + 账号共享背包"都在位的测试台。
func newCargoHarness(t *testing.T, metadata map[string]string, slots map[string]dnfrepo.ItemStack) *cargoHarness {
	t.Helper()
	harness := newGmtoolWriteHarness(t, nil)
	ctx := context.Background()
	if metadata == nil {
		metadata = map[string]string{}
	}
	spy := &cargoCountingAccountStore{AccountRepository: harness.group.Account}
	if err := spy.Save(ctx, dnfrepo.AccountRecord{AccountID: "acc-1", Metadata: metadata}); err != nil {
		t.Fatalf("save account: %v", err)
	}
	spy.saves, spy.metadataWrites, spy.keys = 0, 0, nil
	group := harness.group
	group.Account = spy
	if slots != nil {
		if err := group.AccountInventory.Save(ctx, dnfrepo.AccountInventoryRecord{
			AccountID: "acc-1",
			Slots:     slots,
		}); err != nil {
			t.Fatalf("save account inventory: %v", err)
		}
	}
	harness.server.deps.Repos = func() (dnfrepo.Group, bool) { return group, true }
	return &cargoHarness{gmtoolWriteHarness: harness, account: spy}
}

func (h *cargoHarness) accountRecord(t *testing.T) dnfrepo.AccountRecord {
	t.Helper()
	record, found, err := h.account.AccountRepository.Load(context.Background(), "acc-1")
	if err != nil || !found {
		t.Fatalf("load account: found=%t err=%v", found, err)
	}
	return record
}

func (h *cargoHarness) cargoSlots(t *testing.T) (dnfrepo.AccountInventoryRecord, bool) {
	t.Helper()
	group, ok := h.server.deps.Repos()
	if !ok {
		t.Fatal("repositories are not ready")
	}
	record, found, err := group.AccountInventory.Load(context.Background(), "acc-1")
	if err != nil {
		t.Fatalf("load account inventory: %v", err)
	}
	return record, found
}

func (h *cargoHarness) webAccount() webAccount {
	return webAccount{PanelID: 1, Account: "launcher-acc", AccountID: "acc-1"}
}

func (h *cargoHarness) maxCargo(t *testing.T) (int, map[string]any) {
	t.Helper()
	return h.callCargo(t, func(s *Server, recorder *httptest.ResponseRecorder) {
		s.cargoApplyMax(recorder, context.Background(), mustGroup(t, s), h.webAccount(), 1)
	})
}

func (h *cargoHarness) clearCargo(t *testing.T) (int, map[string]any) {
	t.Helper()
	return h.callCargo(t, func(s *Server, recorder *httptest.ResponseRecorder) {
		s.cargoApplyClear(recorder, context.Background(), mustGroup(t, s), h.webAccount(), 1)
	})
}

func (h *cargoHarness) deleteCargo(t *testing.T, slot int) (int, map[string]any) {
	t.Helper()
	return h.callCargo(t, func(s *Server, recorder *httptest.ResponseRecorder) {
		s.cargoApplyDelete(recorder, context.Background(), mustGroup(t, s), h.webAccount(), 1, slot)
	})
}

func (h *cargoHarness) callCargo(t *testing.T, call func(*Server, *httptest.ResponseRecorder)) (int, map[string]any) {
	t.Helper()
	recorder := httptest.NewRecorder()
	call(h.server, recorder)
	response := map[string]any{}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response %q: %v", recorder.Body.String(), err)
	}
	return recorder.Code, response
}

func cargoTestSlots() map[string]dnfrepo.ItemStack {
	return map[string]dnfrepo.ItemStack{
		"12:0":   {ItemID: 7001, Count: 1},
		"12:3":   {ItemID: 7002, Count: 4},
		"0:9":    {ItemID: 7001, Count: 1},
		"0:354":  {ItemID: 9001, Count: 12},
		"bad-ke": {ItemID: 1, Count: 1},
	}
}

// 计划书口径：满级必须取成长表的终点（cargo.NextAccountCargoTier 的最后一档）。
func TestCargoMaxUsesTheLastTierOfTheSharedTable(t *testing.T) {
	harness := newCargoHarness(t, map[string]string{"account_cargo_level": "8", "source": "ops"}, nil)
	status, response := harness.maxCargo(t)
	requireOK(t, status, response)

	if response["listParam16"] != float64(120) || response["listType"] != float64(cargoListType) {
		t.Fatalf("响应 = %+v（满级必须是当前PVF成长表最后一档120，且字段名是listParam16）", response)
	}
	if response["previousLevel"] != float64(8) || response["changed"] != true {
		t.Fatalf("响应 = %+v", response)
	}
	record := harness.accountRecord(t)
	if record.Metadata[cargoMetadataLevelKey] != "120" {
		t.Fatalf("%s = %q, want 120", cargoMetadataLevelKey, record.Metadata[cargoMetadataLevelKey])
	}
	if record.Metadata["source"] != "ops" {
		t.Fatalf("别的元数据键被动了: %+v", record.Metadata)
	}
	// CARGO-D3：只走键级写，不整记录写；已经 created（level>0）时只写 level 一个键。
	if harness.account.saves != 0 {
		t.Fatalf("整记录 Account.Save 被调用 %d 次，want 0", harness.account.saves)
	}
	if harness.account.metadataWrites != 1 || len(harness.account.keys) != 1 || harness.account.keys[0] != cargoMetadataLevelKey {
		t.Fatalf("键级写入 = %d %v, want 1 次 [%s]", harness.account.metadataWrites, harness.account.keys, cargoMetadataLevelKey)
	}
	fields := harness.eventFields(t, "gm-account-cargo-max")
	if fields["previous_level"] != int64(8) || fields["max_level"] != int64(120) || fields["result"] != "applied" {
		t.Fatalf("审计字段 = %+v", fields)
	}
}

// 从"从未创建"到满级：先写 created 再写 level（顺序固定）。
func TestCargoMaxCreatesTheStorageFirst(t *testing.T) {
	harness := newCargoHarness(t, map[string]string{}, nil)
	status, response := harness.maxCargo(t)
	requireOK(t, status, response)
	if response["created"] != true || response["changed"] != true {
		t.Fatalf("响应 = %+v", response)
	}
	record := harness.accountRecord(t)
	if record.Metadata[cargoMetadataCreatedKey] != "true" || record.Metadata[cargoMetadataLevelKey] != "120" {
		t.Fatalf("元数据 = %+v", record.Metadata)
	}
	if harness.account.metadataWrites != 2 ||
		len(harness.account.keys) != 2 ||
		harness.account.keys[0] != cargoMetadataCreatedKey ||
		harness.account.keys[1] != cargoMetadataLevelKey {
		t.Fatalf("键级写入顺序 = %v, want [%s %s]",
			harness.account.keys, cargoMetadataCreatedKey, cargoMetadataLevelKey)
	}
}

// 已经满级：幂等成功且零写入。
func TestCargoMaxAtMaxIsNoop(t *testing.T) {
	harness := newCargoHarness(t, map[string]string{
		cargoMetadataCreatedKey: "true",
		cargoMetadataLevelKey:   "120",
	}, nil)
	status, response := harness.maxCargo(t)
	requireOK(t, status, response)
	if response["changed"] != false || response["listParam16"] != float64(120) {
		t.Fatalf("响应 = %+v", response)
	}
	if harness.account.metadataWrites != 0 || harness.account.saves != 0 {
		t.Fatalf("没变化时不该写库: metadata=%d saves=%d", harness.account.metadataWrites, harness.account.saves)
	}
	if fields := harness.eventFields(t, "gm-account-cargo-max"); fields["result"] != "noop" {
		t.Fatalf("审计字段 = %+v, want noop", fields)
	}
}

// 账号行不存在：明确失败、零写入。
func TestCargoMaxMissingAccountFails(t *testing.T) {
	harness := newCargoHarness(t, map[string]string{}, nil)
	before := harness.accountRecord(t)
	status, response := harness.callCargo(t, func(s *Server, recorder *httptest.ResponseRecorder) {
		s.cargoApplyMax(recorder, context.Background(), mustGroup(t, s),
			webAccount{PanelID: 9, AccountID: "nobody"}, 9)
	})
	if message := requireFailure(t, status, response); !strings.Contains(message, "账号不存在") {
		t.Fatalf("拒绝文案 = %q", message)
	}
	if harness.account.metadataWrites != 0 || harness.account.saves != 0 {
		t.Fatalf("拒绝路径必须零写入: %d/%d", harness.account.metadataWrites, harness.account.saves)
	}
	if after := harness.accountRecord(t); !reflect.DeepEqual(before, after) {
		t.Fatalf("账号被改动了:\nbefore=%+v\nafter=%+v", before, after)
	}
}

// 清空：只删 list 12 的槽位，坏键保留并如实计入 skipped。
func TestCargoClearRemovesOnlyAccountCargoSlots(t *testing.T) {
	harness := newCargoHarness(t, map[string]string{}, cargoTestSlots())
	status, response := harness.clearCargo(t)
	requireOK(t, status, response)
	if response["deleted"] != float64(2) || response["skipped"] != float64(1) || response["remaining"] != float64(3) {
		t.Fatalf("响应 = %+v, want deleted=2 skipped=1 remaining=3（0:9 / 0:354 / bad-ke 留下）", response)
	}
	record, found := harness.cargoSlots(t)
	if !found {
		t.Fatal("账号共享背包记录消失了")
	}
	if _, ok := record.Slots["12:0"]; ok {
		t.Fatalf("12:0 没被删掉: %+v", record.Slots)
	}
	if _, ok := record.Slots["12:3"]; ok {
		t.Fatalf("12:3 没被删掉: %+v", record.Slots)
	}
	// 角色槽、账号共享槽（晶块）、坏键都必须原样保留。
	for _, key := range []string{"0:9", "0:354", "bad-ke"} {
		if _, ok := record.Slots[key]; !ok {
			t.Fatalf("%s 被误删了: %+v", key, record.Slots)
		}
	}
	removed, _ := response["removed"].([]any)
	if len(removed) != 2 {
		t.Fatalf("removed = %+v, want 2 条", response["removed"])
	}
	fields := harness.eventFields(t, "gm-account-cargo-clear")
	if fields["deleted"] != 2 || fields["skipped"] != int64(1) || fields["remaining"] != 3 {
		t.Fatalf("审计字段 = %+v", fields)
	}
}

// 空金库：deleted=0，且不写库（UpdatedAt 不动）。
func TestCargoClearOnEmptyCargoDoesNotWrite(t *testing.T) {
	harness := newCargoHarness(t, map[string]string{}, map[string]dnfrepo.ItemStack{"0:9": {ItemID: 7001, Count: 1}})
	before, found := harness.cargoSlots(t)
	if !found {
		t.Fatal("fixture 应该有账号共享背包记录")
	}
	status, response := harness.clearCargo(t)
	requireOK(t, status, response)
	if response["deleted"] != float64(0) || response["remaining"] != float64(1) {
		t.Fatalf("响应 = %+v", response)
	}
	after, _ := harness.cargoSlots(t)
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("空金库不该写库:\nbefore=%+v\nafter=%+v", before, after)
	}
	if fields := harness.eventFields(t, "gm-account-cargo-clear"); fields["result"] != "noop" {
		t.Fatalf("审计字段 = %+v, want noop", fields)
	}
}

// CARGO-D1：账号共享背包记录缺失 ⇒ 明确失败（不静默当成"已经清空"）。
func TestCargoClearMissingStorageFails(t *testing.T) {
	harness := newCargoHarness(t, map[string]string{}, nil)
	status, response := harness.clearCargo(t)
	if message := requireFailure(t, status, response); !strings.Contains(message, "没有账号共享背包记录") {
		t.Fatalf("拒绝文案 = %q", message)
	}
	if fields := harness.eventFields(t, "gm-account-cargo-rejected"); fields["reason"] != "account_inventory_missing" {
		t.Fatalf("审计字段 = %+v", fields)
	}
	status, response = harness.deleteCargo(t, 0)
	if message := requireFailure(t, status, response); !strings.Contains(message, "没有账号共享背包记录") {
		t.Fatalf("删除路径拒绝文案 = %q", message)
	}
}

// 删除一格：只删请求的那一格，其余（含别的 list 12 槽）不动。
func TestCargoDeleteRemovesExactlyOneSlot(t *testing.T) {
	harness := newCargoHarness(t, map[string]string{}, cargoTestSlots())
	status, response := harness.deleteCargo(t, 3)
	requireOK(t, status, response)
	if response["slot"] != float64(3) || response["itemId"] != float64(7002) ||
		response["count"] != float64(4) || response["deleted"] != float64(4) {
		t.Fatalf("响应 = %+v", response)
	}
	record, _ := harness.cargoSlots(t)
	if _, ok := record.Slots["12:3"]; ok {
		t.Fatalf("12:3 没被删掉: %+v", record.Slots)
	}
	if _, ok := record.Slots["12:0"]; !ok {
		t.Fatalf("12:0 被误删了: %+v", record.Slots)
	}
	if fields := harness.eventFields(t, "gm-account-cargo-delete"); fields["slot"] != 3 || fields["count"] != int64(4) {
		t.Fatalf("审计字段 = %+v", fields)
	}
}

// 空槽位：明确失败、零写入（上游 GmService.Accounts.cs:367-369 的"该槽位没有物品"）。
func TestCargoDeleteEmptySlotFailsWithoutWriting(t *testing.T) {
	harness := newCargoHarness(t, map[string]string{}, cargoTestSlots())
	before, _ := harness.cargoSlots(t)
	status, response := harness.deleteCargo(t, 7)
	if message := requireFailure(t, status, response); !strings.Contains(message, "没有物品") {
		t.Fatalf("拒绝文案 = %q", message)
	}
	after, _ := harness.cargoSlots(t)
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("拒绝路径必须零写入:\nbefore=%+v\nafter=%+v", before, after)
	}
	// 负槽位/越界槽位由路由层在碰数据库之前拦下（见
	// TestCargoDeleteRouteRejectsBadSlotBeforeTouchingTheDatabase）；
	// 核心函数只对"这个槽位有没有物品"负责，所以这里只断言它照实拒绝。
	status, response = harness.deleteCargo(t, 12)
	if message := requireFailure(t, status, response); !strings.Contains(message, "没有物品") {
		t.Fatalf("空槽位拒绝文案 = %q", message)
	}
}

// 路由层：缺 slot 字段 / 越界 slot 必须在**碰数据库之前**就拒绝
// （槽位 0 是合法槽位，不能被当成缺省值吞掉）。
func TestCargoDeleteRouteRejectsBadSlotBeforeTouchingTheDatabase(t *testing.T) {
	harness := newGmtoolWriteHarness(t, nil) // s.db == nil：任何一步走到账号表都会 panic
	for _, testCase := range []struct {
		name    string
		body    map[string]any
		wantSub string
	}{
		{name: "缺少 slot", body: map[string]any{}, wantSub: "缺少 slot"},
		{name: "负 slot", body: map[string]any{"slot": -1}, wantSub: "slot 必须在 0.."},
		{name: "slot 越界", body: map[string]any{"slot": 1 << 20}, wantSub: "slot 必须在 0.."},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			status, response := harness.post(t, "/api/accounts/1/cargo/delete",
				map[string]string{"id": "1"}, testCase.body, harness.server.handleAccountCargoDelete)
			if message := requireFailure(t, status, response); !strings.Contains(message, testCase.wantSub) {
				t.Fatalf("拒绝文案 = %q, want 含 %q", message, testCase.wantSub)
			}
			if fields := harness.eventFields(t, "gm-account-cargo-rejected"); fields["result"] != "rejected" {
				t.Fatalf("审计字段 = %+v", fields)
			}
		})
	}
}

// 在线守卫（CARGO-D4）：开关打开 + 账号内有人在线 ⇒ 拒绝且零写入。
func TestCargoGuardsOnlineAccountWhenEnabled(t *testing.T) {
	harness := newCargoHarness(t, map[string]string{cargoMetadataLevelKey: "8"}, cargoTestSlots())
	before, _ := harness.cargoSlots(t)
	harness.server.cfg.RequireOffline = true
	harness.server.deps.Online = func(characterID uint16) bool { return characterID == 1 }

	status, response := harness.callCargo(t, func(s *Server, recorder *httptest.ResponseRecorder) {
		if !s.requireAccountOffline(context.Background(), recorder, "account-cargo-clear",
			mustGroup(t, s), "acc-1", 1) {
			return
		}
		s.cargoApplyClear(recorder, context.Background(), mustGroup(t, s), harness.webAccount(), 1)
	})
	if message := requireFailure(t, status, response); !strings.Contains(message, "仍有角色在线") {
		t.Fatalf("守卫拒绝文案 = %q", message)
	}
	after, _ := harness.cargoSlots(t)
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("守卫拒绝必须零写入:\nbefore=%+v\nafter=%+v", before, after)
	}

	// 全离线 ⇒ 正常写入，且清空真的发生。
	harness.server.deps.Online = func(uint16) bool { return false }
	status, response = harness.callCargo(t, func(s *Server, recorder *httptest.ResponseRecorder) {
		if !s.requireAccountOffline(context.Background(), recorder, "account-cargo-clear",
			mustGroup(t, s), "acc-1", 1) {
			return
		}
		s.cargoApplyClear(recorder, context.Background(), mustGroup(t, s), harness.webAccount(), 1)
	})
	requireOK(t, status, response)
	if response["deleted"] != float64(2) {
		t.Fatalf("全离线时应当真的清空: %+v", response)
	}
}

// 默认档：开关未开时连探针都不打（逐字节今天的默认行为）。
func TestCargoDefaultProfileDoesNotProbe(t *testing.T) {
	probes := 0
	harness := newCargoHarness(t, map[string]string{cargoMetadataLevelKey: "8"}, nil)
	harness.server.cfg.RequireOffline = false
	harness.server.deps.Online = func(uint16) bool { probes++; return true }
	status, response := harness.maxCargo(t)
	requireOK(t, status, response)
	if probes != 0 {
		t.Fatalf("默认档不该探针，实际 %d 次", probes)
	}
	if len(harness.kindsWithPrefix("gm-online-guard")) != 0 {
		t.Fatalf("默认档不该产生守卫日志: %v", harness.kindsWithPrefix("gm-online-guard"))
	}
}

// 满级档位必须来自 shared 表：walk 一遍 cargo.NextAccountCargoTier 的链，结果必须一致。
func TestCargoMaxLevelWalksTheExportedTierChain(t *testing.T) {
	level, err := cargoMaxLevel()
	if err != nil {
		t.Fatalf("cargoMaxLevel: %v", err)
	}
	if level != 120 {
		t.Fatalf("满级 = %d, want 120（当前PVF账号金库的最后一档）", level)
	}
}
