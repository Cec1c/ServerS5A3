package dnfbridge

// 567r4 独立验证：候选的 missing-only Ensure 在 **Save 失败** 时是否真的 fail-closed，
// 以及读写覆盖是否会触碰"已存在的合法状态"。private overlay，不碰共享源。
//
// 与 567r2/567r3 的差别只在 stub：那里的失败发生在 Load（根本走不到 Save），
// 这里委托真实仓储 Load（返回 missing/已有合法行），只让 **Save** 失败，
// 从而强制走到生产候选的真实写入点。

import (
	"context"
	"encoding/binary"
	"errors"
	"reflect"
	"strconv"
	"testing"

	"longheng.io/server/internal/modules/dnf/dnfenum"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

// countingSettings567r4 委托真实 Settings 仓储，可选在 Save 上失败，并统计读写次数。
type countingSettings567r4 struct {
	base      dnfrepo.SettingsRepository
	failLoad  bool
	failSave  bool
	loadCalls int
	saveCalls int
}

func (c *countingSettings567r4) Load(ctx context.Context, key string) (dnfrepo.SettingsRecord, bool, error) {
	c.loadCalls++
	if c.failLoad {
		return dnfrepo.SettingsRecord{}, false, errors.New("load failed 567r4")
	}
	return c.base.Load(ctx, key)
}

func (c *countingSettings567r4) Save(ctx context.Context, record dnfrepo.SettingsRecord) error {
	c.saveCalls++
	if c.failSave {
		return errors.New("save failed 567r4")
	}
	return c.base.Save(ctx, record)
}

func saveFail567r4Body() []byte {
	body := make([]byte, 28)
	body[0] = 0
	binary.LittleEndian.PutUint16(body[1:3], 65)
	binary.LittleEndian.PutUint32(body[7:11], 3)
	body[11] = 2
	binary.LittleEndian.PutUint16(body[12:14], 1)
	binary.LittleEndian.PutUint32(body[22:26], ^uint32(0))
	return body
}

func saveFail567r4Stack(count, limit int64) dnfrepo.ItemStack {
	return dnfrepo.ItemStack{
		ItemID: 417,
		Count:  count,
		Extra: map[string]string{
			"item_kind":      "stackable",
			"pvf_path":       "stackable/event/chn_event_tau_2009/chn_tau_vita.stk",
			"stackable_type": "[waste]",
			"stack_limit":    strconv.FormatInt(limit, 10),
		},
	}
}

func TestEnsureSaveFail567r4(t *testing.T) {
	key := strconv.Itoa(int(loginWearBridgeCharacterID))
	scopeOf := func() string { return dnfrepo.CharacterContainerStateScope(key) }
	validRow := map[string]string{"source": "seed567r4", "main_list_param16": "24", "avatar_list_param16": "0", "personal_cargo_list_param16": "8"}

	cases := []struct {
		name       string
		seedValues map[string]string
		failLoad   bool
		failSave   bool
		wantReject bool
		// wantSaveCalls: 期望实际调用 Save 的次数（-1 = 不校验）
		wantSaveCalls int
	}{
		{name: "save_fail_is_failclosed", failSave: true, wantReject: true, wantSaveCalls: -1},
		{name: "load_error_is_failclosed", failLoad: true, wantReject: true, wantSaveCalls: 0},
		{name: "existing_valid_state_is_not_rewritten", seedValues: validRow, failSave: true, wantReject: false, wantSaveCalls: 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			setDefaultTier(t)
			t.Setenv(op19PlayerFlagEnv, "")
			t.Setenv(op19PlayerFlagQuietEnv, "")
			f := newLoginWearBridgeFixture(t)
			ctx := context.Background()
			if err := f.group.Inventory.Save(ctx, dnfrepo.InventoryRecord{
				CharacterID: key,
				Slots:       map[string]dnfrepo.ItemStack{"0:65": saveFail567r4Stack(3, 1000)},
				Warehouse:   map[string]dnfrepo.ItemStack{"2:0": saveFail567r4Stack(5, 1000)},
			}); err != nil {
				t.Fatal(err)
			}
			if tc.seedValues != nil {
				if err := f.group.Settings.Save(ctx, dnfrepo.SettingsRecord{Scope: scopeOf(), Values: tc.seedValues}); err != nil {
					t.Fatal(err)
				}
			}
			before, _, _ := f.group.Settings.Load(ctx, scopeOf())

			stub := &countingSettings567r4{base: f.group.Settings, failLoad: tc.failLoad, failSave: tc.failSave}
			patched := f.group
			patched.Settings = stub
			f.service.repositoryProvider = func() (dnfrepo.Group, bool) { return patched, true }

			handled, err := f.service.handleAlignedGameCommand(f.session, byte(dnfenum.GameCmdCommand), uint16(dnfenum.CmdPacketMoveItemspace), saveFail567r4Body())
			if err != nil {
				t.Fatalf("handled=%t err=%v", handled, err)
			}
			t.Logf("loadCalls=%d saveCalls=%d bufferBytes=%d", stub.loadCalls, stub.saveCalls, f.buffer.write.Len())

			inv, _, err := f.group.Inventory.Load(ctx, key)
			if err != nil {
				t.Fatal(err)
			}
			if tc.wantReject {
				if f.buffer.write.Len() != 0 {
					t.Fatalf("fail-closed leg must not ACK, got %d bytes", f.buffer.write.Len())
				}
				if got := inv.Slots["0:65"].Count; got != 3 {
					t.Fatalf("fail-closed leg must not persist a source change: %+v", inv.Slots)
				}
				if cargo, exists := inv.Warehouse["2:0"]; !exists || cargo.Count != 5 {
					t.Fatalf("fail-closed leg must not persist a warehouse change: %+v", inv.Warehouse)
				}
				if _, exists := inv.Warehouse["2:1"]; exists {
					t.Fatalf("fail-closed leg must not create the drop slot: %+v", inv.Warehouse)
				}
			} else {
				if f.buffer.write.Len() == 0 {
					t.Fatal("accepted move must ACK")
				}
				if _, exists := inv.Slots["0:65"]; exists {
					t.Fatalf("source row survived: %+v", inv.Slots)
				}
				if got := inv.Warehouse["2:0"].Count; got != 8 {
					t.Fatalf("merged cargo=%d want 8", got)
				}
			}

			after, found, err := f.group.Settings.Load(ctx, scopeOf())
			if err != nil {
				t.Fatal(err)
			}
			switch {
			case tc.seedValues != nil:
				if !found || !reflect.DeepEqual(before.Values, after.Values) {
					t.Fatalf("existing valid state must be untouched: before=%+v after=%+v", before.Values, after.Values)
				}
			default:
				if found {
					t.Fatalf("failed Ensure must not leave a row: %+v", after.Values)
				}
			}
			if tc.wantSaveCalls >= 0 && stub.saveCalls != tc.wantSaveCalls {
				t.Fatalf("Save call count=%d want %d", stub.saveCalls, tc.wantSaveCalls)
			}
			if tc.failSave && tc.seedValues == nil && stub.saveCalls == 0 {
				t.Fatal("save-failure leg never reached the production Save path")
			}
		})
	}
}
