package dnfbridge

// 567r2 测试（基线纯 4d7 与候选共用同一份字节）。
//
// 两类腿，绝不混称：
//   * generic_contract/* —— 通用库存合约 fixture：不加载 PVF，Extra 取 417 已记录的
//     契约值（stackable/event/chn_event_tau_2009/chn_tau_vita.stk、[waste]、1000）。
//     槽位合法可达：源主背包 65（[waste] 页 65..120）、目标个人库 2:1（< 8 格容量）、
//     库内既有行 2:0；源 3 + 既有 5 = 8 属正例范围。
//   * real_pvf417/* —— 实际固定 PVF 合法 route：从 Script.pvf 解析 417，断言真实契约
//     （kind/path/type/上限/页范围）并核对与 generic fixture 身份一致，再用解析值跑同一条移动。
//
// 只有 real_pvf417 腿声称"实际 PVF 合法路径"；generic 腿不是 PVF 证据。
// 元数据失败腿（settings 读错 / 行非法）用显式合成故障，只走失败分支。

import (
	"context"
	"encoding/binary"
	"errors"
	"os"
	"reflect"
	"strconv"
	"testing"

	"longheng.io/server/internal/modules/dnf/dnfenum"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

type failingSettings567r2 struct{ err error }

func (f failingSettings567r2) Load(context.Context, string) (dnfrepo.SettingsRecord, bool, error) {
	return dnfrepo.SettingsRecord{}, false, f.err
}

func (f failingSettings567r2) Save(context.Context, dnfrepo.SettingsRecord) error { return f.err }

func moveEnsure567r2Body(srcList byte, srcSlot int16, dstList byte, dstSlot int16) []byte {
	body := make([]byte, 28)
	body[0] = srcList
	binary.LittleEndian.PutUint16(body[1:3], uint16(srcSlot))
	binary.LittleEndian.PutUint32(body[7:11], 3)
	body[11] = dstList
	binary.LittleEndian.PutUint16(body[12:14], uint16(dstSlot))
	binary.LittleEndian.PutUint32(body[22:26], ^uint32(0))
	return body
}

func moveEnsure567r2Stack(count, limit int64, pvfPath, stackableType, itemKind string) dnfrepo.ItemStack {
	return dnfrepo.ItemStack{
		ItemID: 417,
		Count:  count,
		Extra: map[string]string{
			"item_kind":      itemKind,
			"pvf_path":       pvfPath,
			"stackable_type": stackableType,
			"stack_limit":    strconv.FormatInt(limit, 10),
		},
	}
}

func moveEnsure567r2Key() string { return strconv.Itoa(int(loginWearBridgeCharacterID)) }

// 已记录的 417 契约（generic 腿用；real_pvf417 腿从 PVF 解析并断言一致）。
const (
	moveEnsure567r2Path  = "stackable/event/chn_event_tau_2009/chn_tau_vita.stk"
	moveEnsure567r2Type  = "[waste]"
	moveEnsure567r2Kind  = "stackable"
	moveEnsure567r2Limit = 1000
)

func TestMoveContainerStateEnsure567r2(t *testing.T) {
	validRow := map[string]string{"source": "seed567r2", "main_list_param16": "24", "avatar_list_param16": "0", "personal_cargo_list_param16": "8"}
	generic := func(count int64) dnfrepo.ItemStack {
		return moveEnsure567r2Stack(count, moveEnsure567r2Limit, moveEnsure567r2Path, moveEnsure567r2Type, moveEnsure567r2Kind)
	}

	cases := []struct {
		name        string
		seedValues  map[string]string
		failRepo    bool
		cmd         byte
		selectedID  uint16
		blankSess   bool
		body        []byte
		wantReject  bool
		wantCreated bool
		wantCargo   string
		wantMove    bool
	}{
		{name: "generic_contract/legal_state_kept_unchanged", seedValues: validRow, wantCargo: "8", wantMove: true},
		{name: "generic_contract/missing_state_initialized_to_valid_capacity", wantCreated: true, wantCargo: strconv.Itoa(int(currentExeInitialPersonalCargoSlotCount)), wantMove: true},
		{name: "generic_contract/settings_error_rejected", failRepo: true, wantReject: true},
		{name: "generic_contract/invalid_state_rejected_without_overwrite", seedValues: map[string]string{"main_list_param16": "24", "avatar_list_param16": "0", "personal_cargo_list_param16": "7"}, wantReject: true, wantCargo: "7"},
		{name: "generic_contract/wrong_class_keeps_old_semantics", cmd: byte(dnfenum.GameCmdCommand) + 1, wantReject: true},
		{name: "generic_contract/selected_zero_keeps_old_semantics", selectedID: 0, wantReject: true},
		{name: "generic_contract/malformed_body_keeps_old_semantics", body: []byte{0, 65, 0}, wantReject: true},
		{name: "generic_contract/body_without_character_container", body: moveEnsure567r2Body(7, 0, 7, 1), wantReject: true},
		{name: "generic_contract/shared_account_fallback_is_not_a_new_gate", blankSess: true, wantCreated: true,
			wantCargo: strconv.Itoa(int(currentExeInitialPersonalCargoSlotCount)), wantMove: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			setDefaultTier(t)
			t.Setenv(op19PlayerFlagEnv, "")
			t.Setenv(op19PlayerFlagQuietEnv, "")
			f := newLoginWearBridgeFixture(t)
			ctx := context.Background()
			if err := f.group.Inventory.Save(ctx, dnfrepo.InventoryRecord{
				CharacterID: moveEnsure567r2Key(),
				Slots:       map[string]dnfrepo.ItemStack{"0:65": generic(3)},
				Warehouse:   map[string]dnfrepo.ItemStack{"2:0": generic(5)},
			}); err != nil {
				t.Fatal(err)
			}
			scope := dnfrepo.CharacterContainerStateScope(moveEnsure567r2Key())
			if tc.seedValues != nil {
				if err := f.group.Settings.Save(ctx, dnfrepo.SettingsRecord{Scope: scope, Values: tc.seedValues}); err != nil {
					t.Fatal(err)
				}
			}
			before, _, _ := f.group.Settings.Load(ctx, scope)
			if tc.failRepo {
				patched := f.group
				patched.Settings = failingSettings567r2{err: errors.New("settings repository unavailable 567r2")}
				f.service.repositoryProvider = func() (dnfrepo.Group, bool) { return patched, true }
			}
			if tc.blankSess {
				f.session.accountID = ""
			}
			cmd := tc.cmd
			if cmd == 0 {
				cmd = byte(dnfenum.GameCmdCommand)
			}
			if tc.selectedID != 0 || tc.name == "generic_contract/selected_zero_keeps_old_semantics" {
				f.session.selectedCharacterID = tc.selectedID
			}
			body := tc.body
			if body == nil {
				// 合法玩家路径形状：主背包 65（[waste] 页）→ 个人库 2:1（< 8 格），既有 2:0。
				body = moveEnsure567r2Body(0, 65, 2, 1)
			}

			handled, err := f.service.handleAlignedGameCommand(f.session, cmd, uint16(dnfenum.CmdPacketMoveItemspace), body)
			if err != nil {
				t.Fatalf("handled=%t err=%v", handled, err)
			}
			inv, _, err := f.group.Inventory.Load(ctx, moveEnsure567r2Key())
			if err != nil {
				t.Fatal(err)
			}
			if tc.wantReject {
				if f.buffer.write.Len() != 0 {
					t.Fatalf("control/reject leg must not answer, got %d bytes", f.buffer.write.Len())
				}
				if got := inv.Slots["0:65"].Count; got != 3 {
					t.Fatalf("control/reject leg must not persist a source change: %+v", inv.Slots)
				}
				if cargo, exists := inv.Warehouse["2:0"]; !exists || cargo.Count != 5 {
					t.Fatalf("control/reject leg must not persist a warehouse change: %+v", inv.Warehouse)
				}
				if _, exists := inv.Warehouse["2:1"]; exists {
					t.Fatalf("control/reject leg must not create the drop slot: %+v", inv.Warehouse)
				}
			} else if tc.wantMove {
				if f.buffer.write.Len() == 0 {
					t.Fatal("accepted move must answer")
				}
				if _, exists := inv.Slots["0:65"]; exists {
					t.Fatalf("source row survived: %+v", inv.Slots)
				}
				if got := inv.Warehouse["2:0"].Count; got != 8 {
					t.Fatalf("merged cargo count=%d want 8 (5+3 into the existing row)", got)
				}
			}
			after, found, err := f.group.Settings.Load(ctx, scope)
			if err != nil {
				t.Fatal(err)
			}
			switch {
			case tc.failRepo:
				if found {
					t.Fatalf("failing settings repository produced a row: %+v", after)
				}
			case tc.wantCreated:
				if !found {
					t.Fatal("missing state must be initialized before the move")
				}
				if got := after.Values["personal_cargo_list_param16"]; got != tc.wantCargo {
					t.Fatalf("initialized cargo=%q want %q", got, tc.wantCargo)
				}
				if after.Values["source"] != "current_exe_86jp_op13_container_state" {
					t.Fatalf("initialized row must come from the unique factory: %+v", after.Values)
				}
			case tc.wantReject:
				if tc.seedValues == nil {
					if found {
						t.Fatalf("control leg must not write Settings: %+v", after.Values)
					}
				} else if !found || !reflect.DeepEqual(before.Values, after.Values) {
					t.Fatalf("existing row must be untouched: before=%+v after=%+v", before.Values, after.Values)
				}
			default:
				if !found || !reflect.DeepEqual(before.Values, after.Values) {
					t.Fatalf("legal row must be unchanged: before=%+v after=%+v", before.Values, after.Values)
				}
			}
		})
	}
}

// real_pvf417 腿：仅在实际固定 PVF 下运行。先解析并断言 417 的真实契约，核对与 generic
// fixture 身份一致，再用解析值跑同一条"主背包 65 → 个人库 2:1、既有 2:0"的合法路线。
func TestMoveContainerStateEnsure567r2RealPVF417(t *testing.T) {
	path := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if path == "" {
		t.Skip("set DNFBRIDGE_REAL_PVF_SMOKE for the real 417 legal-route leg")
	}
	archive, err := platformpvf.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := newPVFDungeonDropCatalog(archive)
	if err != nil {
		t.Fatal(err)
	}
	def, err := catalog.ResolveItem(417)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("REAL_PVF417 path=%s kind=%s type=%s slots=%d..%d cap=%d", def.PVFPath, def.Kind, def.StackableType, def.SlotStart, def.SlotEnd, def.StackLimit)
	if def.Kind != dungeonDropItemStackable || def.StackLimit != 1000 || def.SlotStart != 65 || def.SlotEnd != 120 {
		t.Fatalf("real 417 contract changed: %+v", def)
	}
	if def.PVFPath != moveEnsure567r2Path || def.StackableType != moveEnsure567r2Type {
		t.Fatalf("generic fixture identity diverges from the real PVF: path=%q type=%q", def.PVFPath, def.StackableType)
	}

	ctx := context.Background()
	setDefaultTier(t)
	t.Setenv(op19PlayerFlagEnv, "")
	t.Setenv(op19PlayerFlagQuietEnv, "")
	f := newLoginWearBridgeFixture(t)
	source := moveEnsure567r2Stack(3, def.StackLimit, def.PVFPath, def.StackableType, string(def.Kind))
	existing := moveEnsure567r2Stack(5, def.StackLimit, def.PVFPath, def.StackableType, string(def.Kind))
	if err := f.group.Inventory.Save(ctx, dnfrepo.InventoryRecord{
		CharacterID: moveEnsure567r2Key(),
		Slots:       map[string]dnfrepo.ItemStack{"0:65": source},
		Warehouse:   map[string]dnfrepo.ItemStack{"2:0": existing},
	}); err != nil {
		t.Fatal(err)
	}
	if err := f.group.Settings.Save(ctx, dnfrepo.SettingsRecord{
		Scope: dnfrepo.CharacterContainerStateScope(moveEnsure567r2Key()),
		Values: map[string]string{
			"source": "seed567r2-real", "main_list_param16": "24", "avatar_list_param16": "0", "personal_cargo_list_param16": "8",
		},
	}); err != nil {
		t.Fatal(err)
	}
	handled, err := f.service.handleAlignedGameCommand(f.session, byte(dnfenum.GameCmdCommand), uint16(dnfenum.CmdPacketMoveItemspace), moveEnsure567r2Body(0, 65, 2, 1))
	if err != nil || !handled {
		t.Fatalf("handled=%t err=%v", handled, err)
	}
	if f.buffer.write.Len() == 0 {
		t.Fatal("real-PVF legal route must answer")
	}
	inv, _, err := f.group.Inventory.Load(ctx, moveEnsure567r2Key())
	if err != nil {
		t.Fatal(err)
	}
	if _, exists := inv.Slots["0:65"]; exists {
		t.Fatalf("real-PVF route left the source row: %+v", inv.Slots)
	}
	if got := inv.Warehouse["2:0"].Count; got != 8 {
		t.Fatalf("real-PVF merged cargo=%d want 8", got)
	}
	if _, exists := inv.Warehouse["2:1"]; exists {
		t.Fatalf("real-PVF route created the drop slot instead of merging: %+v", inv.Warehouse)
	}
}
