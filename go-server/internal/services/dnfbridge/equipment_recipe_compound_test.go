package dnfbridge

// op25（装备设计图制作）运行时 owner 的用例。夹具全部是假 PVF 的**最小同形**文档
// （与真实 Script.pvf 的设计图段名/取值逐字同形；反引号用 \x60 转义书写），真实档
// 另行 opt-in 核对。
//
// 覆盖：
//   - 7 字节 / 9 字节请求布局与全部已证拒绝分支（含 9 字节尾部 00 01）；
//   - 支持范围的 fail-closed 边界（upgrade / maintain / fail output / 比率单位未证 /
//     need material / bead / item category / expertjob only / unsupported / int data）；
//   - 走注册表 + Owner 事务的端到端消耗与发放（槽位型 / 金币型；ID 来源拒绝）；
//   - 有意义的原子失败：材料不足、设计图不足、来源非设计图、来源缺失/锁定、
//     不支持的种类、金币不足、背包容量不足 —— 任一条都必须零消耗、零发放；
//   - ACK 只回最小已证成功体 01 00 00，其后必须有容器刷新；失败只回 00 00。

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"math"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/dnfenum"
	dnfexpertjob "longheng.io/server/internal/modules/dnf/expertjob"
	dnfproto "longheng.io/server/internal/modules/dnf/protocol"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	dnfrepomemory "longheng.io/server/internal/modules/dnf/repository/memory"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

const (
	equipmentRecipeCompoundTestDrawingID  = int64(1000)
	equipmentRecipeCompoundTestMaterialID = int64(2000)
	equipmentRecipeCompoundTestOutputID   = int64(3000)
	equipmentRecipeCompoundTestFillerID   = int64(4000)
)

// 支持形状：显式 [input item] 材料 + [output item] 产出，且没有随机性/失败/维护/升级语义。
const equipmentRecipeCompoundTestSupportedDoc = "" +
	"[stackable type]\n" +
	"\x60[recipe]\x60 0\n" +
	"[string data]\n" +
	"\x60[weaving]\x60\n" +
	"[/string data]\n" +
	"[input item]\n" +
	"2000 2 \n" +
	"[/input item]\n" +
	"[output item]\n" +
	"3000 1 \n" +
	"[/output item]\n"

// [input item] 里 id==0 的一对是金币（与真实 5065 同形）。
const equipmentRecipeCompoundTestGoldDoc = "" +
	"[stackable type]\n" +
	"\x60[recipe]\x60 0\n" +
	"[input item]\n" +
	"0 50 2000 2 \n" +
	"[/input item]\n" +
	"[output item]\n" +
	"3000 1 \n" +
	"[/output item]\n"

func equipmentRecipeCompoundTestSource(drawingDoc string) dungeonDropCatalogTestSource {
	return dungeonDropCatalogTestSource{
		"monster/monster.lst":     "",
		"equipment/equipment.lst": "",
		"stackable/stackable.lst": "" +
			"1000 \x60recipe/drawing.stk\x60\n" +
			"2000 \x60material/solvent.stk\x60\n" +
			"3000 \x60material/product.stk\x60\n" +
			"4000 \x60material/filler.stk\x60\n",
		"stackable/recipe/drawing.stk":   drawingDoc,
		"stackable/material/solvent.stk": "[name] \x60Solvent\x60\n[stackable type] \x60[material]\x60\n[stack limit] 999\n",
		"stackable/material/product.stk": "[name] \x60Product\x60\n[stackable type] \x60[material]\x60\n[stack limit] 999\n",
		"stackable/material/filler.stk":  "[name] \x60Filler\x60\n[stackable type] \x60[material]\x60\n[stack limit] 1\n",
	}
}

func equipmentRecipeCompoundTestSlotRequest(slot uint32, count uint16) []byte {
	body := make([]byte, currentEquipmentRecipeCompoundRequestSize)
	binary.LittleEndian.PutUint32(body[0:4], slot)
	body[4] = currentEquipmentRecipeCompoundSourceIsSlot
	binary.LittleEndian.PutUint16(body[5:7], count)
	return body
}

func equipmentRecipeCompoundTestItemIDRequest(itemID uint32, count uint16) []byte {
	body := make([]byte, currentEquipmentRecipeCompoundExtendedRequestSize)
	binary.LittleEndian.PutUint32(body[0:4], itemID)
	body[4] = currentEquipmentRecipeCompoundSourceIsItemID
	binary.LittleEndian.PutUint16(body[5:7], count)
	body[7] = currentEquipmentRecipeCompoundExtendedTailLow
	body[8] = currentEquipmentRecipeCompoundExtendedTailHigh
	return body
}

func equipmentRecipeCompoundTestService(t *testing.T, source dungeonDropCatalogTestSource, repositories dnfrepo.Group) *Service {
	t.Helper()
	catalog, err := newPVFDungeonDropCatalog(source)
	if err != nil {
		t.Fatalf("build item catalog: %v", err)
	}
	return &Service{
		options: options{
			accountID:          "account-1",
			gameUpperHeader:    gameUpperHeaderChannel13,
			gameUpperBodyCodec: gameUpperBodyCodecPlain,
		},
		pvfItemCatalog:     catalog,
		repositoryProvider: func() (dnfrepo.Group, bool) { return repositories, true },
	}
}

func equipmentRecipeCompoundTestTotal(inventory dnfrepo.InventoryRecord, itemID int64) int64 {
	total := int64(0)
	for _, stack := range inventory.Slots {
		if stack.ItemID == itemID {
			total += stack.Count
		}
	}
	return total
}

func equipmentRecipeCompoundTestDispatch(t *testing.T, service *Service, session *gameSession, classification byte, body []byte) {
	t.Helper()
	registry, err := newGameplayModuleRegistry(equipmentRecipeCompoundGameplayModule())
	if err != nil {
		t.Fatalf("build registry: %v", err)
	}
	handled, err := registry.DispatchUpper(service, session, currentEquipmentRecipeCompoundOpcode, classification, body)
	if !handled || err != nil {
		t.Fatalf("dispatch handled=%t err=%v", handled, err)
	}
}

// Decode every post-ACK packet independently. Main list rows must have the exact
// 119B stride, slot/id/value offsets and committed quantities, with no phantom row.
func equipmentRecipeCompoundTestRefresh(t *testing.T, raw []byte, want map[uint32]uint64, expectedRows ...map[uint16][2]uint32) {
	t.Helper()
	seen := map[uint32]uint64{}
	for _, list := range currentSelectedItemListTypes {
		packet, rest := splitGameServerUpperPacketWithHeader(t, raw, 16)
		raw = rest
		if packet.Header.MsgID != uint16(dnfenum.CmdPacketLeaveParty) || packet.Header.Classification != 0 || len(packet.Body) < 3 || packet.Body[0] != list {
			t.Fatalf("container header=%+v body=%x want list=%d op13/class0", packet.Header, packet.Body, list)
		}
		offset := 1
		if list <= 2 {
			offset += 2
		}
		rows := int(binary.LittleEndian.Uint16(packet.Body[offset : offset+2]))
		offset += 2
		stride := currentItemListEntrySizeForType(list)
		end := offset + rows*stride
		trailer := 0
		if list == 2 {
			trailer = 1
		}
		if len(packet.Body) != end+trailer {
			t.Fatalf("list=%d row count/stride mismatch body=%d rows=%d", list, len(packet.Body), rows)
		}
		if list == 2 && packet.Body[end] != 0 {
			t.Fatal("unexpected cargo trailer")
		}
		if list != 0 {
			continue
		}
		slots := map[uint16]bool{}
		for i := 0; i < rows; i++ {
			row := packet.Body[offset+i*119 : offset+(i+1)*119]
			slot := binary.LittleEndian.Uint16(row[:2])
			id := binary.LittleEndian.Uint32(row[2:6])
			value := binary.LittleEndian.Uint32(row[6:10])
			if len(expectedRows) > 0 {
				if expected, found := expectedRows[0][slot]; !found || expected != [2]uint32{id, value} {
					t.Fatalf("slot=%d row id/value=%d/%d expected=%v found=%t", slot, id, value, expected, found)
				}
			}
			if slots[slot] {
				t.Fatalf("duplicate wire slot=%d", slot)
			}
			slots[slot] = true
			if id != 0 {
				seen[id] += uint64(value)
			}
		}
		if len(expectedRows) > 0 && len(slots) != len(expectedRows[0]) {
			t.Fatalf("main row slots=%v want=%v", slots, expectedRows[0])
		}
	}
	if len(raw) != 0 || !reflect.DeepEqual(seen, want) {
		t.Fatalf("refresh rows=%v want=%v trailing=%d", seen, want, len(raw))
	}
}

// (1) 请求布局：只有 7 字节与 9 字节（尾部 00 01）两种已证形状，其余全部拒绝。
func TestCurrentEquipmentRecipeCompoundRequestLayouts(t *testing.T) {
	slotNineBytes := append(equipmentRecipeCompoundTestSlotRequest(7, 5), 0x00, 0x01)
	modeTwo := equipmentRecipeCompoundTestSlotRequest(10, 1)
	modeTwo[4] = 2
	badTailLow := equipmentRecipeCompoundTestItemIDRequest(1000, 1)
	badTailLow[7] = 0x01
	badTailHigh := equipmentRecipeCompoundTestItemIDRequest(1000, 1)
	badTailHigh[8] = 0x00

	tests := []struct {
		name    string
		body    []byte
		want    currentEquipmentRecipeCompoundRequest
		wantErr bool
	}{
		{
			name: "slot form 7 bytes",
			body: equipmentRecipeCompoundTestSlotRequest(10, 3),
			want: currentEquipmentRecipeCompoundRequest{SourceSlot: 10, SourceItemID: -1, Count: 3},
		},
		{
			name: "slot form 9 bytes",
			body: slotNineBytes,
			want: currentEquipmentRecipeCompoundRequest{SourceSlot: 7, SourceItemID: -1, Count: 5},
		},
		{
			name: "item id form 9 bytes",
			body: equipmentRecipeCompoundTestItemIDRequest(1000, 1),
			want: currentEquipmentRecipeCompoundRequest{SourceIsItemID: true, SourceSlot: -1, SourceItemID: 1000, Count: 1},
		},
		{name: "nil body", body: nil, wantErr: true},
		{name: "6 bytes", body: make([]byte, 6), wantErr: true},
		{name: "8 bytes", body: make([]byte, 8), wantErr: true},
		{name: "10 bytes", body: make([]byte, 10), wantErr: true},
		{name: "11 bytes", body: make([]byte, 11), wantErr: true},
		{name: "count zero", body: equipmentRecipeCompoundTestSlotRequest(10, 0), wantErr: true},
		{name: "slot zero is the gold wallet pseudo slot", body: equipmentRecipeCompoundTestSlotRequest(0, 1), wantErr: true},
		{name: "slot above i16", body: equipmentRecipeCompoundTestSlotRequest(40000, 1), wantErr: true},
		{name: "source mode 2", body: modeTwo, wantErr: true},
		{name: "item id zero", body: equipmentRecipeCompoundTestItemIDRequest(0, 1), wantErr: true},
		{name: "9 byte tail low", body: badTailLow, wantErr: true},
		{name: "9 byte tail high", body: badTailHigh, wantErr: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := parseCurrentEquipmentRecipeCompoundRequest(test.body)
			if test.wantErr {
				if err == nil {
					t.Fatalf("body=%x accepted as %+v", test.body, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("body=%x rejected: %v", test.body, err)
			}
			if got != test.want {
				t.Fatalf("request=%+v want=%+v", got, test.want)
			}
		})
	}
}

// (2) Module owns the proven upper and native legacy COMMAND routes.
func TestCurrentEquipmentRecipeCompoundModuleOwnsProvenOp25Routes(t *testing.T) {
	if currentEquipmentRecipeCompoundOpcode != 25 || uint16(dnfenum.CmdPacketCompoundItem) != 25 {
		t.Fatalf("op25 constant=%d enum=%d want 25", currentEquipmentRecipeCompoundOpcode, uint16(dnfenum.CmdPacketCompoundItem))
	}
	module := equipmentRecipeCompoundGameplayModule()
	if module.Name != "equipment-recipe-compound" {
		t.Fatalf("module name=%q", module.Name)
	}
	if handler, ok := module.UpperHandlers[currentEquipmentRecipeCompoundOpcode]; !ok || handler == nil {
		t.Fatalf("upper handler for op25 missing: %+v", module.UpperHandlers)
	}
	if len(module.LegacyHandlers) != 1 || module.LegacyHandlers[25] == nil || len(module.LegacyNormalizers) != 0 {
		t.Fatalf("native legacy op25 must route without normalization: %+v", module)
	}
	registry, err := newGameplayModuleRegistry(module)
	if err != nil {
		t.Fatalf("registry rejected module: %v", err)
	}
	if _, ok := registry.upperHandlers[currentEquipmentRecipeCompoundOpcode]; !ok {
		t.Fatal("op25 upper route not registered")
	}
	if len(registry.legacyHandlers) != 1 || registry.legacyHandlers[25].handler == nil || len(registry.legacyNormalizers) != 0 {
		t.Fatalf("registry native op25 route incorrect: %+v", registry.legacyHandlers)
	}
}

// (3) 支持范围的 fail-closed 边界。判据直接吃 catalog 解析出来的条目，因此这条用例
// 同时钉住目录给出的 Kind/段与运行时接受的子集之间的一致性。
func TestCurrentEquipmentRecipeCompoundSupportBoundary(t *testing.T) {
	tests := []struct {
		name       string
		doc        string
		wantKind   string
		wantReason string
	}{
		{
			name:     "supported material output",
			doc:      equipmentRecipeCompoundTestSupportedDoc,
			wantKind: "material-output",
		},
		{
			name:       "fail output present",
			doc:        equipmentRecipeCompoundTestSupportedDoc + "[fail output]\n2000 1 \n[/fail output]\n",
			wantKind:   "material-output",
			wantReason: "fail_output_present",
		},
		{
			name:       "success rate 1000 unit unproven",
			doc:        equipmentRecipeCompoundTestSupportedDoc + "[success rate]\n1000 \n",
			wantKind:   "material-output",
			wantReason: "success_rate_unit_unproven",
		},
		{
			name:       "success rate 100 is unproven",
			doc:        equipmentRecipeCompoundTestSupportedDoc + "[success rate]\n100 \n",
			wantKind:   "material-output",
			wantReason: "success_rate_unit_unproven",
		},
		{
			name:       "creation rate 1000 unit unproven",
			doc:        equipmentRecipeCompoundTestSupportedDoc + "[creation rate]\n1000 \n",
			wantKind:   "material-output",
			wantReason: "creation_rate_unit_unproven",
		},
		{
			name:       "maintain ability present",
			doc:        equipmentRecipeCompoundTestSupportedDoc + "[maintain ability]\n\x60normal\x60 \n",
			wantKind:   "material-output",
			wantReason: "maintain_present",
		},
		{
			name:       "maintain socket stat present",
			doc:        equipmentRecipeCompoundTestSupportedDoc + "[maintain socket stat]\n1 \n[/maintain socket stat]\n",
			wantKind:   "material-output",
			wantReason: "maintain_present",
		},
		{
			name:       "action type upgrade present",
			doc:        equipmentRecipeCompoundTestSupportedDoc + "[action type]\n\x60[upgrade]\x60\n[/action type]\n",
			wantKind:   "material-output",
			wantReason: "upgrade_present",
		},
		{
			name:       "custom upgrade type present",
			doc:        equipmentRecipeCompoundTestSupportedDoc + "[custom upgrade type]\n\x60infinite_upgrade_event_20_level\x60\n",
			wantKind:   "material-output",
			wantReason: "upgrade_present",
		},
		{
			name:       "expertjob only present",
			doc:        equipmentRecipeCompoundTestSupportedDoc + "[expertjob only]\n1 \n[/expertjob only]\n",
			wantKind:   "material-output",
			wantReason: "expert_job_only",
		},
		{
			name:       "need material present",
			doc:        equipmentRecipeCompoundTestSupportedDoc + "[need material]\n2000 5 \n[/need material]\n",
			wantKind:   "material-output",
			wantReason: "need_material_semantics_unproven",
		},
		{
			name:       "bead item present",
			doc:        equipmentRecipeCompoundTestSupportedDoc + "[bead item]\n2000 1 \n[/bead item]\n",
			wantKind:   "material-output",
			wantReason: "bead_item_semantics_unproven",
		},
		{
			name:       "item category present",
			doc:        equipmentRecipeCompoundTestSupportedDoc + "[item category]\n\x60use 2nd upgrade table\x60\n[/item category]\n",
			wantKind:   "material-output",
			wantReason: "item_category_upgrade_table_unproven",
		},
		{
			name:       "outputs only is unsupported",
			doc:        "[stackable type]\n\x60[recipe]\x60 0\n[output item]\n3000 1 \n[/output item]\n",
			wantKind:   "unsupported",
			wantReason: "kind_unsupported",
		},
		{
			name:       "int data encoded is not material output",
			doc:        "[stackable type]\n\x60[recipe]\x60 1\n[int data]\n1 2000 1 1 3000 1 0 \n[/int data]\n",
			wantKind:   "int-data-encoded",
			wantReason: "kind_int-data-encoded",
		},
		{
			name:       "in place upgrade kind",
			doc:        "[stackable type]\n\x60[recipe]\x60 0\n[action type]\n\x60[infinite upgrade recipe]\x60\n[/action type]\n[need material]\n2000 5 \n[/need material]\n",
			wantKind:   "in-place-upgrade",
			wantReason: "kind_in-place-upgrade",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			catalog, err := newPVFEquipmentRecipeCatalog(equipmentRecipeCompoundTestSource(test.doc))
			if err != nil {
				t.Fatalf("build recipe catalog: %v", err)
			}
			entry, found, err := catalog.Recipe(equipmentRecipeCompoundTestDrawingID)
			if err != nil || !found {
				t.Fatalf("recipe found=%t err=%v", found, err)
			}
			if string(entry.Kind) != test.wantKind {
				t.Fatalf("kind=%s want=%s entry=%+v", entry.Kind, test.wantKind, entry)
			}
			if got := currentEquipmentRecipeCompoundRejectReason(entry); got != test.wantReason {
				t.Fatalf("reason=%q want=%q entry=%+v", got, test.wantReason, entry)
			}
		})
	}
}

// (4) 端到端成功：槽位型请求 → Owner 事务消耗设计图与材料、发放产出 → ACK 01 00 00。
func TestCurrentEquipmentRecipeCompoundConsumesSourceAndMaterialsThroughOwnerTransaction(t *testing.T) {
	ctx := context.Background()
	repositories := dnfrepomemory.NewMemoryGroup()
	if err := repositories.Character.Save(ctx, dnfrepo.CharacterRecord{
		CharacterID: "19",
		AccountID:   "account-1",
		Stats:       map[string]int64{"gold": 5000},
	}); err != nil {
		t.Fatalf("save character: %v", err)
	}
	if err := repositories.Inventory.Save(ctx, dnfrepo.InventoryRecord{
		CharacterID: "19",
		Slots: map[string]dnfrepo.ItemStack{
			"0:10": {ItemID: equipmentRecipeCompoundTestDrawingID, Count: 2},
			"0:20": {ItemID: equipmentRecipeCompoundTestMaterialID, Count: 10},
		},
	}); err != nil {
		t.Fatalf("save inventory: %v", err)
	}
	service := equipmentRecipeCompoundTestService(t, equipmentRecipeCompoundTestSource(equipmentRecipeCompoundTestSupportedDoc), repositories)
	connection := &bufferConn{}
	session := &gameSession{conn: connection, connID: "equipment-recipe-compound-test", selectedCharacterID: 19}

	equipmentRecipeCompoundTestDispatch(t, service, session, dnfproto.DefaultChannelClassification, equipmentRecipeCompoundTestSlotRequest(10, 2))

	ack, rest := splitGameServerUpperPacket(t, connection.write.Bytes())
	if ack.Header.MsgID != currentEquipmentRecipeCompoundOpcode || ack.Header.Classification != dnfproto.DefaultChannelClassification {
		t.Fatalf("ack header=%+v", ack.Header)
	}
	if !bytes.Equal(ack.Body, []byte{1, 0, 0}) {
		t.Fatalf("ack body=%x want 01 00 00 (success + changedCount 0 + rewardCount 0)", ack.Body)
	}
	equipmentRecipeCompoundTestRefresh(t, rest, map[uint32]uint64{2000: 6, 3000: 2}, map[uint16][2]uint32{0: {0, 5000}, 20: {2000, 6}, 3: {3000, 2}})
	if len(rest) == 0 {
		t.Fatal("success ack must be followed by the current item list container refresh")
	}

	inventory, found, err := repositories.Inventory.Load(ctx, "19")
	if err != nil || !found {
		t.Fatalf("load inventory found=%t err=%v", found, err)
	}
	if total := equipmentRecipeCompoundTestTotal(inventory, equipmentRecipeCompoundTestDrawingID); total != 0 {
		t.Fatalf("drawing total=%d want 0", total)
	}
	if total := equipmentRecipeCompoundTestTotal(inventory, equipmentRecipeCompoundTestMaterialID); total != 6 {
		t.Fatalf("material total=%d want 6 (10 - 2x2)", total)
	}
	if total := equipmentRecipeCompoundTestTotal(inventory, equipmentRecipeCompoundTestOutputID); total != 2 {
		t.Fatalf("output total=%d want 2", total)
	}
	character, found, err := repositories.Character.Load(ctx, "19")
	if err != nil || !found {
		t.Fatalf("load character found=%t err=%v", found, err)
	}
	if character.Stats["gold"] != 5000 {
		t.Fatalf("gold=%d want 5000 (this recipe declares no gold cost)", character.Stats["gold"])
	}
}

// (5) Slot request consumes only its selected drawing row; gold uses input item
// id==0; ACK remains the minimum proven success body.
func TestCurrentEquipmentRecipeCompoundSlotFormConsumesOnlySelectedSlotAndGold(t *testing.T) {
	ctx := context.Background()
	repositories := dnfrepomemory.NewMemoryGroup()
	if err := repositories.Character.Save(ctx, dnfrepo.CharacterRecord{
		CharacterID: "19",
		AccountID:   "account-1",
		Stats:       map[string]int64{"gold": 5000},
	}); err != nil {
		t.Fatalf("save character: %v", err)
	}
	if err := repositories.Inventory.Save(ctx, dnfrepo.InventoryRecord{
		CharacterID: "19",
		Slots: map[string]dnfrepo.ItemStack{
			"0:12": {ItemID: equipmentRecipeCompoundTestDrawingID, Count: 3},
			"0:30": {ItemID: equipmentRecipeCompoundTestDrawingID, Count: 1},
			"0:20": {ItemID: equipmentRecipeCompoundTestMaterialID, Count: 10},
		},
	}); err != nil {
		t.Fatalf("save inventory: %v", err)
	}
	service := equipmentRecipeCompoundTestService(t, equipmentRecipeCompoundTestSource(equipmentRecipeCompoundTestGoldDoc), repositories)
	connection := &bufferConn{}
	session := &gameSession{conn: connection, connID: "equipment-recipe-compound-gold-test", selectedCharacterID: 19}

	equipmentRecipeCompoundTestDispatch(t, service, session, dnfproto.DefaultChannelClassification, equipmentRecipeCompoundTestSlotRequest(12, 1))

	ack, rest := splitGameServerUpperPacket(t, connection.write.Bytes())
	if !bytes.Equal(ack.Body, []byte{1, 0, 0}) {
		t.Fatalf("ack body=%x", ack.Body)
	}
	equipmentRecipeCompoundTestRefresh(t, rest, map[uint32]uint64{1000: 3, 2000: 8, 3000: 1}, map[uint16][2]uint32{0: {0, 4950}, 12: {1000, 2}, 30: {1000, 1}, 20: {2000, 8}, 3: {3000, 1}})
	inventory, found, err := repositories.Inventory.Load(ctx, "19")
	if err != nil || !found {
		t.Fatalf("load inventory found=%t err=%v", found, err)
	}
	if stack := inventory.Slots["0:12"]; stack.Count != 2 {
		t.Fatalf("lowest drawing slot 0:12 count=%d want 2", stack.Count)
	}
	if stack := inventory.Slots["0:30"]; stack.Count != 1 {
		t.Fatalf("higher drawing slot 0:30 count=%d want 1 (untouched)", stack.Count)
	}
	if total := equipmentRecipeCompoundTestTotal(inventory, equipmentRecipeCompoundTestMaterialID); total != 8 {
		t.Fatalf("material total=%d want 8 (10 - 2)", total)
	}
	if total := equipmentRecipeCompoundTestTotal(inventory, equipmentRecipeCompoundTestOutputID); total != 1 {
		t.Fatalf("output total=%d want 1", total)
	}
	character, found, err := repositories.Character.Load(ctx, "19")
	if err != nil || !found {
		t.Fatalf("load character found=%t err=%v", found, err)
	}
	if character.Stats["gold"] != 4950 {
		t.Fatalf("gold=%d want 4950 (5000 - 50)", character.Stats["gold"])
	}
}

// (6) 有意义的原子失败：每一条都必须回失败体 00 00，且持久化状态与夹具逐字节一致。
func TestCurrentEquipmentRecipeCompoundAtomicFailuresLeaveAssetsUntouched(t *testing.T) {
	fullSlots := func() map[string]dnfrepo.ItemStack {
		slots := map[string]dnfrepo.ItemStack{
			"0:10": {ItemID: equipmentRecipeCompoundTestDrawingID, Count: 2},
			"0:20": {ItemID: equipmentRecipeCompoundTestMaterialID, Count: 100},
		}
		// 产物是 [material]：先占满快捷格 3..8，再占满材料页 121..176（用不同物品，
		// 无法并堆），于是 PlacePickupItem 找不到可并/可空位 ⇒ 整笔事务失败。
		for slot := int16(3); slot <= 8; slot++ {
			slots[currentCeraShopInventorySlotKey(dnfrepo.MainInventoryListType, slot)] = dnfrepo.ItemStack{ItemID: equipmentRecipeCompoundTestFillerID, Count: 1}
		}
		for slot := int16(121); slot <= 176; slot++ {
			slots[currentCeraShopInventorySlotKey(dnfrepo.MainInventoryListType, slot)] = dnfrepo.ItemStack{ItemID: equipmentRecipeCompoundTestFillerID, Count: 1}
		}
		return slots
	}

	tests := []struct {
		name  string
		doc   string
		slots map[string]dnfrepo.ItemStack
		gold  int64
		body  []byte
	}{
		{
			name: "insufficient materials",
			doc:  equipmentRecipeCompoundTestSupportedDoc,
			slots: map[string]dnfrepo.ItemStack{
				"0:10": {ItemID: equipmentRecipeCompoundTestDrawingID, Count: 1},
				"0:20": {ItemID: equipmentRecipeCompoundTestMaterialID, Count: 1},
			},
			body: equipmentRecipeCompoundTestSlotRequest(10, 1),
		},
		{
			name: "insufficient drawings for requested count",
			doc:  equipmentRecipeCompoundTestSupportedDoc,
			slots: map[string]dnfrepo.ItemStack{
				"0:10": {ItemID: equipmentRecipeCompoundTestDrawingID, Count: 1},
				"0:20": {ItemID: equipmentRecipeCompoundTestMaterialID, Count: 100},
			},
			body: equipmentRecipeCompoundTestSlotRequest(10, 3),
		},
		{
			name: "source item is not a recipe",
			doc:  equipmentRecipeCompoundTestSupportedDoc,
			slots: map[string]dnfrepo.ItemStack{
				"0:10": {ItemID: equipmentRecipeCompoundTestMaterialID, Count: 1},
				"0:20": {ItemID: equipmentRecipeCompoundTestMaterialID, Count: 100},
			},
			body: equipmentRecipeCompoundTestSlotRequest(10, 1),
		},
		{
			name: "source slot is empty",
			doc:  equipmentRecipeCompoundTestSupportedDoc,
			slots: map[string]dnfrepo.ItemStack{
				"0:20": {ItemID: equipmentRecipeCompoundTestMaterialID, Count: 100},
			},
			body: equipmentRecipeCompoundTestSlotRequest(10, 1),
		},
		{
			name: "source is locked by the npc shop",
			doc:  equipmentRecipeCompoundTestSupportedDoc,
			slots: map[string]dnfrepo.ItemStack{
				"0:10": {ItemID: equipmentRecipeCompoundTestDrawingID, Count: 2, Extra: map[string]string{"equipment_lock_state": "1"}},
				"0:20": {ItemID: equipmentRecipeCompoundTestMaterialID, Count: 100},
			},
			body: equipmentRecipeCompoundTestSlotRequest(10, 1),
		},
		{
			name: "unsupported recipe with fail output",
			doc:  equipmentRecipeCompoundTestSupportedDoc + "[fail output]\n2000 1 \n[/fail output]\n",
			slots: map[string]dnfrepo.ItemStack{
				"0:10": {ItemID: equipmentRecipeCompoundTestDrawingID, Count: 2},
				"0:20": {ItemID: equipmentRecipeCompoundTestMaterialID, Count: 100},
			},
			body: equipmentRecipeCompoundTestSlotRequest(10, 1),
		},
		{
			name: "insufficient gold",
			doc:  equipmentRecipeCompoundTestGoldDoc,
			slots: map[string]dnfrepo.ItemStack{
				"0:10": {ItemID: equipmentRecipeCompoundTestDrawingID, Count: 2},
				"0:20": {ItemID: equipmentRecipeCompoundTestMaterialID, Count: 100},
			},
			gold: 5,
			body: equipmentRecipeCompoundTestSlotRequest(10, 2),
		},
		{
			name:  "output cannot be placed because the material page is full",
			doc:   equipmentRecipeCompoundTestSupportedDoc,
			slots: fullSlots(),
			body:  equipmentRecipeCompoundTestSlotRequest(10, 1),
		},
		{
			name:  "output uint32 overflow through owner",
			doc:   strings.ReplaceAll(equipmentRecipeCompoundTestSupportedDoc, "3000 1", "3000 4294967295"),
			slots: map[string]dnfrepo.ItemStack{"0:10": {ItemID: 1000, Count: 2}, "0:20": {ItemID: 2000, Count: 100}},
			body:  equipmentRecipeCompoundTestSlotRequest(10, 2),
		},
		{
			name:  "persisted material balance overflow through owner",
			doc:   equipmentRecipeCompoundTestSupportedDoc,
			slots: map[string]dnfrepo.ItemStack{"0:10": {ItemID: 1000, Count: 2}, "0:20": {ItemID: 2000, Count: math.MaxInt64}, "0:21": {ItemID: 2000, Count: 1}},
			body:  equipmentRecipeCompoundTestSlotRequest(10, 1),
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ctx := context.Background()
			repositories := dnfrepomemory.NewMemoryGroup()
			if err := repositories.Character.Save(ctx, dnfrepo.CharacterRecord{
				CharacterID: "19",
				AccountID:   "account-1",
				Stats:       map[string]int64{"gold": test.gold},
			}); err != nil {
				t.Fatalf("save character: %v", err)
			}
			if err := repositories.Inventory.Save(ctx, dnfrepo.InventoryRecord{
				CharacterID: "19",
				Slots:       test.slots,
			}); err != nil {
				t.Fatalf("save inventory: %v", err)
			}
			service := equipmentRecipeCompoundTestService(t, equipmentRecipeCompoundTestSource(test.doc), repositories)
			connection := &bufferConn{}
			session := &gameSession{conn: connection, connID: "equipment-recipe-compound-failure", selectedCharacterID: 19}

			equipmentRecipeCompoundTestDispatch(t, service, session, dnfproto.DefaultChannelClassification, test.body)

			failure, rest := splitGameServerUpperPacket(t, connection.write.Bytes())
			if failure.Header.MsgID != currentEquipmentRecipeCompoundOpcode || failure.Header.Classification != dnfproto.DefaultChannelClassification {
				t.Fatalf("failure header=%+v", failure.Header)
			}
			if !bytes.Equal(failure.Body, []byte{0, 0}) {
				t.Fatalf("failure body=%x want 00 00", failure.Body)
			}
			if len(rest) != 0 {
				t.Fatalf("a rejected compound must not send extra packets: %d bytes", len(rest))
			}

			inventory, found, err := repositories.Inventory.Load(ctx, "19")
			if err != nil || !found {
				t.Fatalf("load inventory found=%t err=%v", found, err)
			}
			if !reflect.DeepEqual(inventory.Slots, test.slots) {
				t.Fatalf("inventory changed on a rejected compound: got %+v want %+v", inventory.Slots, test.slots)
			}
			character, found, err := repositories.Character.Load(ctx, "19")
			if err != nil || !found {
				t.Fatalf("load character found=%t err=%v", found, err)
			}
			if character.Stats["gold"] != test.gold {
				t.Fatalf("gold changed on a rejected compound: got=%d want=%d", character.Stats["gold"], test.gold)
			}
		})
	}
}

// (7) 非 class1（非 COMMAND）的上行不消耗任何东西、也不回包。
func TestCurrentEquipmentRecipeCompoundRequiresCommandClass(t *testing.T) {
	ctx := context.Background()
	repositories := dnfrepomemory.NewMemoryGroup()
	if err := repositories.Character.Save(ctx, dnfrepo.CharacterRecord{CharacterID: "19", AccountID: "account-1", Stats: map[string]int64{"gold": 0}}); err != nil {
		t.Fatalf("save character: %v", err)
	}
	inventory := map[string]dnfrepo.ItemStack{
		"0:10": {ItemID: equipmentRecipeCompoundTestDrawingID, Count: 2},
		"0:20": {ItemID: equipmentRecipeCompoundTestMaterialID, Count: 100},
	}
	if err := repositories.Inventory.Save(ctx, dnfrepo.InventoryRecord{CharacterID: "19", Slots: inventory}); err != nil {
		t.Fatalf("save inventory: %v", err)
	}
	service := equipmentRecipeCompoundTestService(t, equipmentRecipeCompoundTestSource(equipmentRecipeCompoundTestSupportedDoc), repositories)
	connection := &bufferConn{}
	session := &gameSession{conn: connection, connID: "equipment-recipe-compound-class", selectedCharacterID: 19}

	equipmentRecipeCompoundTestDispatch(t, service, session, 2, equipmentRecipeCompoundTestSlotRequest(10, 1))

	if len(connection.write.Bytes()) != 0 {
		t.Fatalf("non-command class must not be answered: %x", connection.write.Bytes())
	}
	persisted, found, err := repositories.Inventory.Load(ctx, "19")
	if err != nil || !found {
		t.Fatalf("load inventory found=%t err=%v", found, err)
	}
	if !reflect.DeepEqual(persisted.Slots, inventory) {
		t.Fatalf("inventory changed on a mismatched class: got %+v want %+v", persisted.Slots, inventory)
	}
}

func equipmentRecipeCompoundTestSharedSource(doc string) dungeonDropCatalogTestSource {
	source := equipmentRecipeCompoundTestSource(doc)
	source["stackable/stackable.lst"] += "3037 `material/clear.stk`\n3033 `material/black.stk`\n"
	source["stackable/material/clear.stk"] = "[stackable type] `[material]`\n[stack limit] 999\n"
	source["stackable/material/black.stk"] = source["stackable/material/clear.stk"]
	return source
}

func TestCurrentEquipmentRecipeCompoundNativeLegacyHandleGameCommand(t *testing.T) {
	// Exact semantic body from ROOT QA06 packet_log.txt:1320-1321.
	body := []byte{0x45, 0, 0, 0, 0, 1, 0, 0, 1}
	ctx := context.Background()
	repos := dnfrepomemory.NewMemoryGroup()
	if err := repos.Character.Save(ctx, dnfrepo.CharacterRecord{CharacterID: "19", AccountID: "account-1", Stats: map[string]int64{"gold": 100}}); err != nil {
		t.Fatal(err)
	}
	if err := repos.Inventory.Save(ctx, dnfrepo.InventoryRecord{CharacterID: "19", Slots: map[string]dnfrepo.ItemStack{"0:69": {ItemID: 1000, Count: 2}, "0:20": {ItemID: 2000, Count: 10}}}); err != nil {
		t.Fatal(err)
	}
	s := equipmentRecipeCompoundTestService(t, equipmentRecipeCompoundTestSource(equipmentRecipeCompoundTestGoldDoc), repos)
	conn := &bufferConn{}
	session := &gameSession{conn: conn, selectedCharacterID: 19, itemListMigrationsDone: true}
	if err := s.handleGameCommand(session, 0, 25, body); err != nil || conn.write.Len() != 0 {
		t.Fatalf("non-COMMAND err=%v reply=%x", err, conn.write.Bytes())
	}
	if err := s.handleGameCommand(session, 1, 25, body); err != nil {
		t.Fatal(err)
	}
	ack, rest := splitGameServerUpperPacket(t, conn.write.Bytes())
	if ack.Header.MsgID != 25 || ack.Header.Classification != 1 || !bytes.Equal(ack.Body, []byte{1, 0, 0}) {
		t.Fatalf("native ack=%+v body=%x", ack.Header, ack.Body)
	}
	equipmentRecipeCompoundTestRefresh(t, rest, map[uint32]uint64{1000: 1, 2000: 8, 3000: 1})
	inventory, _, err := repos.Inventory.Load(ctx, "19")
	if err != nil {
		t.Fatal(err)
	}
	character, _, err := repos.Character.Load(ctx, "19")
	if err != nil {
		t.Fatal(err)
	}
	if inventory.Slots["0:69"].Count != 1 || character.Stats["gold"] != 50 {
		t.Fatalf("native owner not committed inv=%+v char=%+v", inventory, character)
	}
	conn.write.Reset()
	// Extra bytes are rejected, never stripped by a broad normalizer.
	if err := s.handleGameCommand(session, 1, 25, append(body, 0, 0, 0, 0)); err != nil {
		t.Fatal(err)
	}
	failure, trailing := splitGameServerUpperPacket(t, conn.write.Bytes())
	after, _, _ := repos.Inventory.Load(ctx, "19")
	if !bytes.Equal(failure.Body, []byte{0, 0}) || len(trailing) != 0 || !reflect.DeepEqual(after, inventory) {
		t.Fatal("unproved native trailer accepted")
	}
}

func TestCurrentEquipmentRecipeCompoundSharedAssetsAtomicity(t *testing.T) {
	// First output 3033 credits the account; second output 4000 can fail
	// character capacity after both inventories and gold have already changed.
	doc := strings.ReplaceAll(equipmentRecipeCompoundTestGoldDoc, "2000 2", "2000 2 3037 9")
	doc = strings.ReplaceAll(doc, "3000 1", "3033 1 4000 1")
	for _, test := range []struct {
		name                string
		shared              int64
		full, locked, wrong bool
		awardFull           bool
		success             bool
	}{
		{name: "shared sufficient", shared: 18, success: true},
		{name: "shared insufficient ignores both historical character copies", shared: 8},
		{name: "shared locked", shared: 18, locked: true},
		{name: "shared wrong fixed item", shared: 18, wrong: true},
		{name: "shared award capacity rolls back consumption", shared: 18, awardFull: true},
		{name: "later character award fails rolls back both inventories and gold", shared: 18, full: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx := context.Background()
			repos := dnfrepomemory.NewMemoryGroup()
			before := dnfrepo.InventoryRecord{CharacterID: "19", Slots: map[string]dnfrepo.ItemStack{
				"0:10": {ItemID: 1000, Count: 2}, "0:20": {ItemID: 2000, Count: 8},
				"0:358": {ItemID: 3037, Count: 999}, "0:130": {ItemID: 3037, Count: 999},
			}}
			if test.success {
				// Ordinary-page historical copies are independently invalid for
				// the existing container builder. Test both copies on rejection;
				// the success case retains the fixed-slot stale copy, hidden by merge.
				delete(before.Slots, "0:130")
			}
			if test.full {
				for slot := int16(3); slot <= 8; slot++ {
					before.Slots[currentCeraShopInventorySlotKey(0, slot)] = dnfrepo.ItemStack{ItemID: 3000, Count: 999}
				}
				for slot := int16(121); slot <= 176; slot++ {
					before.Slots[currentCeraShopInventorySlotKey(0, slot)] = dnfrepo.ItemStack{ItemID: 3000, Count: 999}
				}
			}
			accountBefore := dnfrepo.AccountInventoryRecord{AccountID: "account-1", Slots: map[string]dnfrepo.ItemStack{"0:358": {ItemID: 3037, Count: test.shared}}}
			if test.awardFull {
				accountBefore.Slots["0:354"] = dnfrepo.ItemStack{ItemID: 3033, Count: 999}
			}
			if test.locked {
				stack := accountBefore.Slots["0:358"]
				stack.Extra = map[string]string{"equipment_lock_state": "1"}
				accountBefore.Slots["0:358"] = stack
			}
			if test.wrong {
				stack := accountBefore.Slots["0:358"]
				stack.ItemID = 3033
				accountBefore.Slots["0:358"] = stack
			}
			if err := repos.Character.Save(ctx, dnfrepo.CharacterRecord{CharacterID: "19", AccountID: "account-1", Stats: map[string]int64{"gold": 100}}); err != nil {
				t.Fatal(err)
			}
			if err := repos.Inventory.Save(ctx, before); err != nil {
				t.Fatal(err)
			}
			if err := repos.AccountInventory.Save(ctx, accountBefore); err != nil {
				t.Fatal(err)
			}
			s := equipmentRecipeCompoundTestService(t, equipmentRecipeCompoundTestSharedSource(doc), repos)
			conn := &bufferConn{}
			// Runtime commands follow selected-character bootstrap. Do not invoke
			// the unrelated one-time historical inventory migration in this test.
			session := &gameSession{conn: conn, selectedCharacterID: 19, itemListMigrationsDone: true}
			equipmentRecipeCompoundTestDispatch(t, s, session, 1, equipmentRecipeCompoundTestSlotRequest(10, 1))
			ack, rest := splitGameServerUpperPacket(t, conn.write.Bytes())
			inventory, _, err := repos.Inventory.Load(ctx, "19")
			if err != nil {
				t.Fatal(err)
			}
			account, _, err := repos.AccountInventory.Load(ctx, "account-1")
			if err != nil {
				t.Fatal(err)
			}
			character, _, err := repos.Character.Load(ctx, "19")
			if err != nil {
				t.Fatal(err)
			}
			if !test.success {
				if !bytes.Equal(ack.Body, []byte{0, 0}) || len(rest) != 0 || !reflect.DeepEqual(inventory, before) || !reflect.DeepEqual(account, accountBefore) || character.Stats["gold"] != 100 {
					t.Fatalf("atomic failure changed assets: ack=%x char=%+v inv=%+v account=%+v", ack.Body, character, inventory, account)
				}
				return
			}
			if !bytes.Equal(ack.Body, []byte{1, 0, 0}) || account.Slots["0:358"].Count != 9 || account.Slots["0:354"].ItemID != 3033 || account.Slots["0:354"].Count != 1 || inventory.Slots["0:10"].Count != 1 || inventory.Slots["0:20"].Count != 6 || character.Stats["gold"] != 50 {
				t.Fatalf("wrong committed assets: char=%+v inv=%+v account=%+v", character, inventory, account)
			}
			if !reflect.DeepEqual(inventory.Slots["0:358"], before.Slots["0:358"]) || !reflect.DeepEqual(inventory.Slots["0:130"], before.Slots["0:130"]) {
				t.Fatal("historical copies used as balance")
			}
			if equipmentRecipeCompoundTestTotal(inventory, 3033) != 0 {
				t.Fatal("account award copied into character")
			}
			// Fixed shared slot exposes account 9, never the stale character 999.
			equipmentRecipeCompoundTestRefresh(t, rest, map[uint32]uint64{1000: 1, 2000: 6, 3037: 9, 3033: 1, 4000: 1})
		})
	}
}

func TestCurrentEquipmentRecipeCompoundUnprovenSemanticsLeaveAssetsUntouched(t *testing.T) {
	for _, test := range []struct {
		name, suffix, reason string
		body                 []byte
	}{
		{name: "need skill", suffix: "[need skill]\n1 2\n[/need skill]\n", reason: "need_skill_semantics_unproven"},
		{name: "making weapon 6", suffix: "[minimum itemmaking level]\n`weapon` 6\n", reason: "itemmaking_level_semantics_unproven"},
		{name: "success 100", suffix: "[success rate]\n100\n", reason: "success_rate_unit_unproven"},
		{name: "creation 100", suffix: "[creation rate]\n100\n", reason: "creation_rate_unit_unproven"},
		{name: "ID source", body: equipmentRecipeCompoundTestItemIDRequest(1000, 1)},
		{name: "shared source slot", body: equipmentRecipeCompoundTestSlotRequest(358, 1)},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx := context.Background()
			repos := dnfrepomemory.NewMemoryGroup()
			before := dnfrepo.InventoryRecord{CharacterID: "19", Slots: map[string]dnfrepo.ItemStack{"0:10": {ItemID: 1000, Count: 2}, "0:358": {ItemID: 1000, Count: 2}, "0:20": {ItemID: 2000, Count: 10}}}
			if err := repos.Character.Save(ctx, dnfrepo.CharacterRecord{CharacterID: "19", AccountID: "account-1", Stats: map[string]int64{"gold": 100}}); err != nil {
				t.Fatal(err)
			}
			if err := repos.Inventory.Save(ctx, before); err != nil {
				t.Fatal(err)
			}
			source := equipmentRecipeCompoundTestSource(equipmentRecipeCompoundTestGoldDoc + test.suffix)
			s := equipmentRecipeCompoundTestService(t, source, repos)
			if test.reason != "" {
				catalog, err := newPVFEquipmentRecipeCatalog(source)
				if err != nil {
					t.Fatal(err)
				}
				entry, _, err := catalog.Recipe(1000)
				if err != nil {
					t.Fatal(err)
				}
				if got := currentEquipmentRecipeCompoundRejectReason(entry); got != test.reason {
					t.Fatalf("reason=%s want=%s entry=%+v", got, test.reason, entry)
				}
			}
			body := test.body
			if body == nil {
				body = equipmentRecipeCompoundTestSlotRequest(10, 1)
			}
			conn := &bufferConn{}
			equipmentRecipeCompoundTestDispatch(t, s, &gameSession{conn: conn, selectedCharacterID: 19}, 1, body)
			ack, rest := splitGameServerUpperPacket(t, conn.write.Bytes())
			after, _, _ := repos.Inventory.Load(ctx, "19")
			character, _, _ := repos.Character.Load(ctx, "19")
			if !bytes.Equal(ack.Body, []byte{0, 0}) || len(rest) != 0 || !reflect.DeepEqual(before, after) || character.Stats["gold"] != 100 {
				t.Fatalf("rejected semantic mutated assets ack=%x inv=%+v char=%+v", ack.Body, after, character)
			}
		})
	}
	base := equipmentRecipeEntry{Kind: equipmentRecipeKindMaterialOutput, Materials: []equipmentRecipeItemStack{{ItemID: 2000, Count: 1}}, Outputs: []equipmentRecipeItemStack{{ItemID: 3000, Count: 1}}}
	base.MinimumItemmakingField = "weapon"
	if currentEquipmentRecipeCompoundRejectReason(base) != "itemmaking_level_semantics_unproven" {
		t.Fatal("partial making field bypass")
	}
	base.MinimumItemmakingField = ""
	base.MinimumItemmakingFound = true
	if currentEquipmentRecipeCompoundRejectReason(base) != "itemmaking_level_semantics_unproven" {
		t.Fatal("making found bypass")
	}
	base.MinimumItemmakingFound = false
	if currentEquipmentRecipeCompoundRejectReason(base) != "" {
		t.Fatal("empty need skill must be allowed")
	}
}

func TestCurrentEquipmentRecipeCompoundPlanningOverflowAndCountGuard(t *testing.T) {
	catalog, err := newPVFDungeonDropCatalog(equipmentRecipeCompoundTestSource(equipmentRecipeCompoundTestSupportedDoc))
	if err != nil {
		t.Fatal(err)
	}
	for _, count := range []int64{0, -1} {
		if _, _, err := currentEquipmentRecipeCompoundMaterials(equipmentRecipeEntry{}, count); err == nil {
			t.Fatal("invalid materials count accepted")
		}
		if _, err := currentEquipmentRecipeCompoundOutputPlan(catalog, equipmentRecipeEntry{}, count); err == nil {
			t.Fatal("invalid output count accepted")
		}
	}
	for _, test := range []struct {
		name   string
		entry  equipmentRecipeEntry
		count  int64
		output bool
	}{
		{name: "material multiply", entry: equipmentRecipeEntry{Materials: []equipmentRecipeItemStack{{ItemID: 2000, Count: math.MaxInt64}}}, count: 2},
		{name: "material merge", entry: equipmentRecipeEntry{Materials: []equipmentRecipeItemStack{{ItemID: 2000, Count: math.MaxInt64}, {ItemID: 2000, Count: 1}}}, count: 1},
		{name: "gold multiply", entry: equipmentRecipeEntry{GoldCostFound: true, GoldCost: math.MaxInt64}, count: 2},
		{name: "output multiply", entry: equipmentRecipeEntry{Outputs: []equipmentRecipeItemStack{{ItemID: 3000, Count: math.MaxInt64}}}, count: 2, output: true},
		{name: "output merge", entry: equipmentRecipeEntry{Outputs: []equipmentRecipeItemStack{{ItemID: 3000, Count: math.MaxInt64}, {ItemID: 3000, Count: 1}}}, count: 1, output: true},
		{name: "output wire uint32", entry: equipmentRecipeEntry{Outputs: []equipmentRecipeItemStack{{ItemID: 3000, Count: math.MaxUint32}}}, count: 2, output: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			var err error
			if test.output {
				_, err = currentEquipmentRecipeCompoundOutputPlan(catalog, test.entry, test.count)
			} else {
				_, _, err = currentEquipmentRecipeCompoundMaterials(test.entry, test.count)
			}
			if !errors.Is(err, errCurrentEquipmentRecipeCompoundOverflow) {
				t.Fatalf("err=%v", err)
			}
		})
	}
	entry := equipmentRecipeEntry{Materials: []equipmentRecipeItemStack{{ItemID: 2000, Count: 2}, {ItemID: 2000, Count: 3}}, Outputs: []equipmentRecipeItemStack{{ItemID: 3000, Count: 2}, {ItemID: 3000, Count: 3}}}
	materials, _, err := currentEquipmentRecipeCompoundMaterials(entry, 2)
	if err != nil || len(materials) != 1 || materials[0].Count != 10 {
		t.Fatalf("merged=%v err=%v", materials, err)
	}
	outputs, err := currentEquipmentRecipeCompoundOutputPlan(catalog, entry, 2)
	if err != nil || len(outputs) != 1 || outputs[0].Count != 10 {
		t.Fatalf("outputs=%v err=%v", outputs, err)
	}
	assets := &dnfexpertjob.Assets{Character: &dnfrepo.CharacterRecord{}, Inventory: &dnfrepo.InventoryRecord{Slots: map[string]dnfrepo.ItemStack{"0:20": {ItemID: 2000, Count: math.MaxInt64}, "0:21": {ItemID: 2000, Count: 1}}}, AccountInventory: &dnfrepo.AccountInventoryRecord{Slots: map[string]dnfrepo.ItemStack{}}}
	if _, err := (&Service{}).consumeCurrentEquipmentRecipeCompoundMaterials(assets, []dnfexpertjob.RecipeEntry{{ItemID: 2000, Count: 1}}, 0); !errors.Is(err, errCurrentEquipmentRecipeCompoundOverflow) {
		t.Fatalf("persisted balance overflow err=%v", err)
	}
}

// (8) 真实档 opt-in：目录覆盖率、支持子集非空、被支持条目的材料/产出真的能解析，
// 并记录目录构造成本（运行时不做缓存，成本必须可接受）。
func TestRealPVFEquipmentRecipeCompoundDeterministicSupport(t *testing.T) {
	path := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if path == "" {
		t.Skip("real PVF required")
	}
	archive, err := platformpvf.LoadArchive(platformpvf.Options{Path: path})
	if err != nil {
		t.Fatal(err)
	}
	items, err := newPVFDungeonDropCatalog(archive)
	if err != nil {
		t.Fatal(err)
	}
	constructionStart := time.Now()
	recipes, err := newPVFEquipmentRecipeCatalog(archive)
	if err != nil {
		t.Fatal(err)
	}
	constructionElapsed := time.Since(constructionStart)
	scanStart := time.Now()
	entries, coverage, err := recipes.Scan()
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	t.Logf("catalog construction=%s scan=%s coverage=%+v", constructionElapsed, time.Since(scanStart), coverage)

	supported := 0
	plannable := 0
	notPlannable := make([]int64, 0, 8)
	reasons := map[string]int{}
	for _, entry := range entries {
		reason := currentEquipmentRecipeCompoundRejectReason(entry)
		if reason != "" {
			reasons[reason]++
			continue
		}
		supported++
		materials, gold, materialErr := currentEquipmentRecipeCompoundMaterials(entry, 1)
		if materialErr != nil || len(materials) == 0 || gold < 0 {
			t.Fatalf("supported entry %d has an unusable material plan: materials=%+v gold=%d err=%v", entry.ItemID, materials, gold, materialErr)
		}
		if _, planErr := currentEquipmentRecipeCompoundOutputPlan(items, entry, 1); planErr != nil {
			notPlannable = append(notPlannable, entry.ItemID)
			continue
		}
		plannable++
	}
	t.Logf("supported=%d plannable=%d not_plannable=%v reasons=%v", supported, plannable, notPlannable, reasons)
	// Re-measured after C1/C2/C3. These are filter/plan counts, not native QA.
	if supported != 803 || plannable != 799 || !reflect.DeepEqual(notPlannable, []int64{10012063, 10012064, 10012115, 10012116}) {
		t.Fatalf("real PVF scope changed: supported=%d plannable=%d unresolved=%v", supported, plannable, notPlannable)
	}
	for _, id := range []int64{5003, 5001, 1242, 5539, 5781, 10001499} {
		entry, found, err := recipes.Recipe(id)
		if err != nil || !found {
			t.Fatalf("GM sample %d found=%t err=%v", id, found, err)
		}
		t.Logf("GM sample id=%d name=%s reason=%q materials=%v gold=%d outputs=%v", id, entry.Name, currentEquipmentRecipeCompoundRejectReason(entry), entry.Materials, entry.GoldCost, entry.Outputs)
	}
	t.Run("real 5003 shared materials committed and cold read", func(t *testing.T) {
		ctx := context.Background()
		repos := dnfrepomemory.NewMemoryGroup()
		if err := repos.Character.Save(ctx, dnfrepo.CharacterRecord{CharacterID: "19", AccountID: "account-1", Stats: map[string]int64{"gold": 1000}}); err != nil {
			t.Fatal(err)
		}
		if err := repos.Inventory.Save(ctx, dnfrepo.InventoryRecord{CharacterID: "19", Slots: map[string]dnfrepo.ItemStack{"0:70": {ItemID: 5003, Count: 2}, "0:71": {ItemID: 5003, Count: 1}, "0:121": {ItemID: 3055, Count: 90}}}); err != nil {
			t.Fatal(err)
		}
		if err := repos.AccountInventory.Save(ctx, dnfrepo.AccountInventoryRecord{AccountID: "account-1", Slots: map[string]dnfrepo.ItemStack{"0:358": {ItemID: 3037, Count: 27}}}); err != nil {
			t.Fatal(err)
		}
		s := equipmentRecipeCompoundTestService(t, equipmentRecipeCompoundTestSource(equipmentRecipeCompoundTestSupportedDoc), repos)
		s.pvfItemCatalog = items
		conn := &bufferConn{}
		session := &gameSession{conn: conn, selectedCharacterID: 19, itemListMigrationsDone: true}
		body := append(equipmentRecipeCompoundTestSlotRequest(70, 2), 0, 1)
		equipmentRecipeCompoundTestDispatch(t, s, session, 1, body)
		ack, rest := splitGameServerUpperPacket(t, conn.write.Bytes())
		if !bytes.Equal(ack.Body, []byte{1, 0, 0}) {
			t.Fatalf("real5003 ack=%x", ack.Body)
		}
		want := map[uint32]uint64{5003: 1, 3055: 30, 3037: 9, 1005: 60}
		rows := map[uint16][2]uint32{0: {0, 1000}, 71: {5003, 1}, 121: {3055, 30}, 358: {3037, 9}, 3: {1005, 60}}
		equipmentRecipeCompoundTestRefresh(t, rest, want, rows)
		inventory, _, err := repos.Inventory.Load(ctx, "19")
		if err != nil {
			t.Fatal(err)
		}
		account, _, err := repos.AccountInventory.Load(ctx, "account-1")
		if err != nil {
			t.Fatal(err)
		}
		if _, exists := inventory.Slots["0:70"]; exists || inventory.Slots["0:71"].Count != 1 || inventory.Slots["0:121"].Count != 30 || account.Slots["0:358"].Count != 9 || equipmentRecipeCompoundTestTotal(inventory, 1005) != 60 || equipmentRecipeCompoundTestTotal(inventory, 3037) != 0 {
			t.Fatalf("real5003 assets inv=%+v account=%+v", inventory, account)
		}
		// A new Service/session reprojects repository state; it never reruns compound.
		cold := equipmentRecipeCompoundTestService(t, equipmentRecipeCompoundTestSource(equipmentRecipeCompoundTestSupportedDoc), repos)
		cold.pvfItemCatalog = items
		coldConn := &bufferConn{}
		if err := cold.sendSelectedCurrentContainerListsRefresh(&gameSession{conn: coldConn, selectedCharacterID: 19, itemListMigrationsDone: true}, "compound_cold_read"); err != nil {
			t.Fatal(err)
		}
		equipmentRecipeCompoundTestRefresh(t, coldConn.write.Bytes(), want, rows)
	})
	pinned := []struct {
		itemID     int64
		wantReason string
	}{
		{itemID: 10095704, wantReason: "fail_output_present"},
		{itemID: 10090321, wantReason: "kind_in-place-upgrade"},
		{itemID: 10005001, wantReason: "kind_unsupported"},
		{itemID: 2683031, wantReason: "kind_int-data-encoded"},
	}
	for _, sample := range pinned {
		entry, found, err := recipes.Recipe(sample.itemID)
		if err != nil {
			t.Fatalf("pinned entry %d: %v", sample.itemID, err)
		}
		if !found {
			t.Fatalf("pinned entry %d not found in the real catalog", sample.itemID)
		}
		if got := currentEquipmentRecipeCompoundRejectReason(entry); got != sample.wantReason {
			t.Fatalf("pinned entry %d reason=%q want=%q kind=%s", sample.itemID, got, sample.wantReason, entry.Kind)
		}
	}

	// 10097920 是 [item category] "use 2nd upgrade table" 的装备设计图：kind 仍是
	// material-output，但升级表选择器未证，必须落在拒绝侧（具体原因由该文档其余段决定）。
	if entry, found, err := recipes.Recipe(10097920); err != nil || !found {
		t.Fatalf("pinned 10097920 found=%t err=%v", found, err)
	} else if entry.Kind != equipmentRecipeKindMaterialOutput || currentEquipmentRecipeCompoundRejectReason(entry) == "" {
		t.Fatalf("pinned 10097920 kind=%s reason=%q: an upgrade-table design drawing must be rejected",
			entry.Kind, currentEquipmentRecipeCompoundRejectReason(entry))
	}
}
