package gmtool

import (
	"bytes"
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
// P3 / J16：账号金库金币（POST /api/accounts/{id}/currency，type=cargo_gold）
//
// 为什么直接调 `applyAccountCurrency` 而不是打整条路由：`handleAccountCurrency`
// 的前半段要用 s.db 查启动器账号表（deps.go:290-332 webAccounts），单测里没有
// 数据库。`applyAccountCurrency` 就是"账号已解析之后"的全部行为（同一段代码，
// 见 api_accounts.go 的注释），打它等价于打路由的业务部分。
// ---------------------------------------------------------------------------

// countingAccountStore 统计"整记录 Save"与"键级 SaveMetadataEntry"各发生几次，
// 用来钉死 J16-1：cargo_gold 只走键级写，不触发 Account.Save 全记录写。
type countingAccountStore struct {
	dnfrepo.AccountRepository
	saves          int
	metadataWrites int
}

func (s *countingAccountStore) Save(ctx context.Context, record dnfrepo.AccountRecord) error {
	s.saves++
	return s.AccountRepository.Save(ctx, record)
}

func (s *countingAccountStore) SaveMetadataEntry(
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
	return writer.SaveMetadataEntry(ctx, accountID, key, value, updatedAt)
}

func (s *countingAccountStore) snapshot(t *testing.T, accountID string) dnfrepo.AccountRecord {
	t.Helper()
	record, found, err := s.AccountRepository.Load(context.Background(), accountID)
	if err != nil || !found {
		t.Fatalf("load account: found=%t err=%v", found, err)
	}
	return record
}

// p3CargoGoldHarness 建一个账号 + 角色都在位的测试台，并返回可计数的账号仓储。
func p3CargoGoldHarness(t *testing.T, metadata map[string]string) (*gmtoolWriteHarness, *countingAccountStore) {
	t.Helper()
	harness := newGmtoolWriteHarness(t, nil)
	account := dnfrepo.AccountRecord{
		AccountID: "acc-1",
		HonorExp:  123,
		Metadata:  metadata,
	}
	if account.Metadata == nil {
		account.Metadata = map[string]string{}
	}
	spy := &countingAccountStore{AccountRepository: harness.group.Account}
	if err := spy.Save(context.Background(), account); err != nil {
		t.Fatalf("save account: %v", err)
	}
	spy.saves = 0 // 只统计请求期间的写
	group := harness.group
	group.Account = spy
	harness.server.deps.Repos = func() (dnfrepo.Group, bool) { return group, true }
	return harness, spy
}

func (h *gmtoolWriteHarness) applyCurrency(t *testing.T, body any) (int, map[string]any) {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal body: %v", err)
	}
	request := httptest.NewRequest("POST", "/api/accounts/1/currency", bytes.NewReader(raw))
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	h.server.applyAccountCurrency(recorder, request.Context(),
		mustGroup(t, h.server), webAccount{PanelID: 1, AccountID: "acc-1"}, decodeCurrencyBody(t, raw))
	response := map[string]any{}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response %q: %v", recorder.Body.String(), err)
	}
	return recorder.Code, response
}

func mustGroup(t *testing.T, server *Server) dnfrepo.Group {
	t.Helper()
	group, ok := server.deps.Repos()
	if !ok {
		t.Fatal("repositories are not ready")
	}
	return group
}

func decodeCurrencyBody(t *testing.T, raw []byte) accountCurrencyRequest {
	t.Helper()
	var body accountCurrencyRequest
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	return body
}

// (计划书测试 9) 只写 dnf_account_metadata["account_cargo_gold"]，不触发全记录写。
func TestCargoGoldWritesMetadataOnly(t *testing.T) {
	harness, spy := p3CargoGoldHarness(t, map[string]string{
		"source":              "ops-audit",
		"account_cera":        "900",
		"account_cargo_level": "8",
	})
	status, response := harness.applyCurrency(t, map[string]any{"type": "cargo_gold", "value": 5000})
	requireOK(t, status, response)

	if response["type"] != "cargo_gold" || response["cargoGold"] != float64(5000) || response["amount"] != float64(5000) {
		t.Fatalf("响应 = %+v", response)
	}
	if spy.saves != 0 {
		t.Fatalf("整记录 Account.Save 被调用 %d 次，want 0（必须走键级 SaveMetadataEntry）", spy.saves)
	}
	if spy.metadataWrites != 1 {
		t.Fatalf("键级写入 = %d 次, want 1", spy.metadataWrites)
	}
	record := spy.snapshot(t, "acc-1")
	if record.Metadata["account_cargo_gold"] != "5000" {
		t.Fatalf("account_cargo_gold = %q, want 5000", record.Metadata["account_cargo_gold"])
	}
	if record.Metadata["source"] != "ops-audit" || record.Metadata["account_cera"] != "900" ||
		record.Metadata["account_cargo_level"] != "8" {
		t.Fatalf("别的元数据键被动了: %+v", record.Metadata)
	}
	if record.HonorExp != 123 {
		t.Fatalf("荣誉经验被动了: %d", record.HonorExp)
	}
	fields := harness.eventFields(t, "gm-account-cargo-gold")
	if fields["old_value"] != int64(0) || fields["value"] != int64(5000) || fields["delta"] != int64(5000) ||
		fields["account_id"] != "acc-1" || fields["result"] != "applied" {
		t.Fatalf("审计事件字段 = %+v", fields)
	}
}

// 边界：负数、超 u32、amount 模式、delta=0 幂等。
func TestCargoGoldBoundsAndAmountMode(t *testing.T) {
	harness, spy := p3CargoGoldHarness(t, map[string]string{"account_cargo_gold": "1000"})
	before := spy.snapshot(t, "acc-1")

	status, response := harness.applyCurrency(t, map[string]any{"type": "cargo_gold", "value": -1})
	if message := requireFailure(t, status, response); !strings.Contains(message, "不能为负") {
		t.Fatalf("负数拒绝文案 = %q", message)
	}
	status, response = harness.applyCurrency(t, map[string]any{"type": "cargo_gold", "value": int64(1) << 33})
	if message := requireFailure(t, status, response); !strings.Contains(message, "0..4294967295") {
		t.Fatalf("超 u32 拒绝文案 = %q", message)
	}
	status, response = harness.applyCurrency(t, map[string]any{"type": "cargo_gold", "amount": -2000})
	if message := requireFailure(t, status, response); !strings.Contains(message, "不能为负") {
		t.Fatalf("扣成负数拒绝文案 = %q", message)
	}
	status, response = harness.applyCurrency(t, map[string]any{"type": "cargo_gold"})
	if message := requireFailure(t, status, response); !strings.Contains(message, "缺少 value 或 amount") {
		t.Fatalf("缺参数拒绝文案 = %q", message)
	}
	if spy.metadataWrites != 0 || spy.saves != 0 {
		t.Fatalf("拒绝路径必须零写入: saves=%d metadata=%d", spy.saves, spy.metadataWrites)
	}
	if after := spy.snapshot(t, "acc-1"); !reflect.DeepEqual(before, after) {
		t.Fatalf("账号被改动了:\nbefore=%+v\nafter=%+v", before, after)
	}

	// amount 模式：1000 + 500 = 1500。
	status, response = harness.applyCurrency(t, map[string]any{"type": "cargo_gold", "amount": 500})
	requireOK(t, status, response)
	if response["cargoGold"] != float64(1500) || response["amount"] != float64(500) {
		t.Fatalf("amount 模式响应 = %+v", response)
	}

	// delta=0：noop，零写入。
	spy.metadataWrites, spy.saves = 0, 0
	status, response = harness.applyCurrency(t, map[string]any{"type": "cargo_gold", "value": 1500})
	requireOK(t, status, response)
	if spy.metadataWrites != 0 || spy.saves != 0 {
		t.Fatalf("没变化时不该写库: saves=%d metadata=%d", spy.saves, spy.metadataWrites)
	}
	if fields := harness.eventFields(t, "gm-account-cargo-gold"); fields["result"] != "noop" {
		t.Fatalf("事件字段 = %+v, want result=noop", fields)
	}
}

// 既有 type（cera/celain）逐字不变：响应形状与写库路径都必须与 P3 之前一致。
func TestCurrencyExistingTypesAreUnchanged(t *testing.T) {
	harness, spy := p3CargoGoldHarness(t, map[string]string{"account_cera": "100"})
	status, response := harness.applyCurrency(t, map[string]any{"type": "cera", "value": 250})
	requireOK(t, status, response)
	want := map[string]any{
		"success":     true,
		"accountId":   float64(1),
		"characterId": float64(1),
		"type":        "cera",
		"amount":      float64(150),
		"cera":        float64(250),
		"tokenCera":   float64(0),
		"hint":        "点券存在账号上；客户端会在进入场景或重开点券商店时刷新，请让玩家换个地图或重新登录一次。",
	}
	if !reflect.DeepEqual(want, response) {
		t.Fatalf("cera 响应变了:\nwant=%+v\ngot =%+v", want, response)
	}
	if _, touched := spy.snapshot(t, "acc-1").Metadata["account_cargo_gold"]; touched {
		t.Fatal("cera 分支不该碰 account_cargo_gold")
	}

	// 其它 type 的既有拒绝文案逐字不变。
	for _, testCase := range []struct{ kind, want string }{
		{"token", "90CN 的点券只有一个账号级池子（account_cera），没有独立的代币券数值；请用 cera。"},
		{"serialuck", "90CN 服务端没有赛丽亚幸运数值，无法设置。"},
		{"nonsense", "不支持的类型: nonsense (可用: cera)"},
	} {
		status, response := harness.applyCurrency(t, map[string]any{"type": testCase.kind, "value": 1})
		if message := requireFailure(t, status, response); message != testCase.want {
			t.Fatalf("type=%s 文案 = %q, want %q", testCase.kind, message, testCase.want)
		}
	}
}

// 默认档：开关未设时，账号里有人在线也照写，且一次探针都不打。
func TestCargoGoldDefaultProfileDoesNotProbe(t *testing.T) {
	probeCalls := 0
	harness, spy := p3CargoGoldHarness(t, map[string]string{"account_cargo_gold": "10"})
	harness.server.cfg.RequireOffline = false
	harness.server.deps.Online = func(uint16) bool { probeCalls++; return true }

	status, response := harness.applyCurrency(t, map[string]any{"type": "cargo_gold", "value": 20})
	requireOK(t, status, response)
	if probeCalls != 0 {
		t.Fatalf("默认档连探针都不该碰，实际 %d 次", probeCalls)
	}
	if spy.metadataWrites != 1 {
		t.Fatalf("默认档必须照常写入（键级 1 次），实际 %d", spy.metadataWrites)
	}
	if len(harness.kindsWithPrefix("gm-online-guard")) != 0 {
		t.Fatalf("默认档不该产生守卫日志: %v", harness.kindsWithPrefix("gm-online-guard"))
	}
}

// 开关 on：账号内任一角色在线 -> 拒绝且零写入（上游 gm_extended.go:174-178 的账号级守卫）。
func TestCargoGoldAccountGuardWhenEnabled(t *testing.T) {
	harness, spy := p3CargoGoldHarness(t, map[string]string{"account_cargo_gold": "10"})
	before := spy.snapshot(t, "acc-1")
	harness.server.cfg.RequireOffline = true
	harness.server.deps.Online = func(characterID uint16) bool { return characterID == 1 }

	status, response := harness.applyCurrency(t, map[string]any{"type": "cargo_gold", "value": 20})
	if message := requireFailure(t, status, response); !strings.Contains(message, "仍有角色在线") {
		t.Fatalf("账号级守卫拒绝文案 = %q", message)
	}
	if spy.metadataWrites != 0 || spy.saves != 0 {
		t.Fatalf("拒绝路径必须零写入: saves=%d metadata=%d", spy.saves, spy.metadataWrites)
	}
	if after := spy.snapshot(t, "acc-1"); !reflect.DeepEqual(before, after) {
		t.Fatalf("账号被改动了:\nbefore=%+v\nafter=%+v", before, after)
	}
	fields := harness.eventFields(t, "gm-online-guard-rejected")
	if fields["action"] != "account-cargo-gold" || fields["reason"] != "account_character_online" ||
		fields["account_id"] != "acc-1" {
		t.Fatalf("守卫事件字段 = %+v", fields)
	}

	// 全离线 -> 正常写入。
	harness.server.deps.Online = func(uint16) bool { return false }
	status, response = harness.applyCurrency(t, map[string]any{"type": "cargo_gold", "value": 20})
	requireOK(t, status, response)
	if spy.snapshot(t, "acc-1").Metadata["account_cargo_gold"] != "20" {
		t.Fatal("全离线时应当写入成功")
	}
}
