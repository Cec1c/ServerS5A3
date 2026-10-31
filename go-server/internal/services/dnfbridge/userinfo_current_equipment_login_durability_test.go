package dnfbridge

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"strconv"
	"strings"
	"testing"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	dnfrepomemory "longheng.io/server/internal/modules/dnf/repository/memory"
)

// 本文件是「登录后武器耐久显示 0」这轮的**回归钉子 + 取证钉子**。它不改任何下行字节，
// 只把三件事钉死：
//
//	A. 真实现场夹具（角色 6 的 31 条穿戴行）逐字来自
//	   `packet_log.txt` 13:52:48.847660354Z 那条
//	   `game-upper-selected-userinfo-mode1-equipment-create-rows` 的 `row_summary`
//	   —— 抄写而不是编造；投影出来的 row_summary 必须与之**逐字符相等**。
//	B. 武器行的下行字节：56 字节、耐久 u16 落在行 +10（= 客户端
//	   `sub_1D77560` RVA 0x19776AB 读的位置：48），且**不带**两段可选 raw 块
//	   ——因为客户端自己的判据 `sub_229ACD0`（RVA 0x1E9ACD0）就是
//	   `itemTemplate->[0x444] <= 0x0b`，类型 12 的武器本来就不走那条分支。
//	B2. 回退档（`DNFBRIDGE_MODE1_EQUIPMENT_RAW_BLOCKS=all_types` 未设置）与
//	   今天线上**逐字节相同**：测试里手工重建期望的 56 字节，并再钉一个 sha256。
//	C. 登录行与进图行**同一批字节**：两条链路调用的是同一个
//	   `writeCurrentMode1EquipmentCreateRow`，现场四次 row_summary 也完全相同。
//	   进图能显示正确耐久 ⇒ 投影行不是缺陷所在。
//
// 现场夹具（只读来源：192.0.2.11:/opt/dnf90/logs/packet_log.txt，
// conn game-000021 / character_id=6 / 13:52:48.847660354Z）。
const liveLoginChar6RowSummary = "0:42601:type=0:durability=4:derived=1:raw=1:create=1;" +
	"1:42603:type=1:durability=4:derived=1:raw=1:create=1;" +
	"2:42605:type=2:durability=2:derived=1:raw=1:create=1;" +
	"3:42609:type=3:durability=47:derived=1:raw=1:create=1;" +
	"4:42611:type=4:durability=11:derived=1:raw=1:create=1;" +
	"5:42615:type=5:durability=0:derived=1:raw=1:create=1;" +
	"6:42607:type=6:durability=2:derived=1:raw=1:create=1;" +
	"7:42613:type=7:durability=5:derived=1:raw=1:create=1;" +
	"8:101580204:type=8:durability=0:derived=1:raw=1:create=1;" +
	"9:101590068:type=9:durability=0:derived=1:raw=1:create=1;" +
	"10:101600023:type=10:durability=0:derived=1:raw=1:create=1;" +
	"11:101590023:type=11:durability=0:derived=1:raw=1:create=1;" +
	"12:101040493:type=12:durability=48:derived=1:raw=0:create=1;" +
	"13:400330106:type=13:durability=0:derived=1:raw=0:create=1;" +
	"14:100070551:type=14:durability=38:derived=1:raw=0:create=1;" +
	"15:100170519:type=15:durability=30:derived=1:raw=0:create=1;" +
	"16:100120544:type=16:durability=32:derived=1:raw=0:create=1;" +
	"17:100270515:type=17:durability=25:derived=1:raw=0:create=1;" +
	"18:100220519:type=18:durability=25:derived=1:raw=0:create=1;" +
	"19:400300070:type=19:durability=0:derived=1:raw=0:create=1;" +
	"20:400310070:type=20:durability=0:derived=1:raw=0:create=1;" +
	"21:400320074:type=21:durability=0:derived=1:raw=0:create=1;" +
	"22:100344527:type=22:durability=0:derived=1:raw=0:create=1;" +
	"23:100352839:type=23:durability=0:derived=1:raw=0:create=1;" +
	"25:100390011:type=25:durability=0:derived=1:raw=0:create=1;" +
	"26:400990170:type=26:durability=0:derived=1:raw=0:create=1;" +
	"27:10006783:type=27:durability=0:derived=1:raw=0:create=1;" +
	"28:10006784:type=28:durability=0:derived=1:raw=0:create=1;" +
	"29:10006785:type=29:durability=0:derived=1:raw=0:create=1;" +
	"30:2747069:type=30:durability=65535:derived=1:raw=0:create=1;" +
	"32:100380058:type=32:durability=0:derived=1:raw=0:create=1"

const (
	liveLoginChar6ID          = "6"
	liveLoginWeaponSlot       = 12
	liveLoginWeaponItemID     = 101040493
	liveLoginWeaponDurability = 48
	liveLoginAvatarSlot       = 3
	liveLoginAvatarItemID     = 42609
	liveLoginAvatarDurability = 47
)

// liveLoginWeaponRawEntry 是 `dnf_equipment_entries`（character_id=6, slot_index=12）
// 那一行的 `raw_entry` 原字节（119 字节，只读查询逐字节抄写）：
//
//	slot u16@0x00 = 12 | item = 101040493 | 实例 999999998 (TopSeed) |
//	+0x0A = 0x0F (强化/打包字节 15) | **+0x0B u16 = 48（耐久）** |
//	+0x0D = 0 | +0x0E u32 = 10015138（客户端按 raw+0x0E 读的道具身份）
const liveLoginWeaponRawEntryHex = "0c006dc10506fec99a3b0f300000a2d198000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000"

// liveLoginWeaponExtra 是同一行的 `dnf_equipment_entry_extra`（逐键抄写）。
func liveLoginWeaponExtra() map[string]string {
	return map[string]string{
		"byte_12":                     "0",
		"current_exe_equipment_type":  "12",
		"current_exe_inventory_count": "1",
		"current_exe_runtime_move":    "1",
		"durability":                  "48",
		"enchant_card_id":             "10015138",
		"enchant_upgrade_count":       "0",
		"equipment_slot":              "12",
		"equipment_type":              "[weapon]",
		"equipped_slot":               "12",
		"ext_data0":                   "15",
		"grade_adjust_type":           "gold_kaleido",
		"item_kind":                   "equipment",
		"max_durability":              "48",
		"packed_flag_byte":            "15",
		"pvf_path":                    "equipment/character/swordman/weapon/beamsword/101040493.equ",
		"quality_seed":                "999999998",
		"reinforce":                   "15",
		"upgrade_level":               "15",
		"value_a":                     "10015138",
	}
}

type liveLoginRowExpectation struct {
	slot       int16
	itemID     int64
	typ        int16
	durability uint16
	rawBlocks  bool
}

// parseLiveLoginRowSummary 解析现场 row_summary（只在本文件里用，格式固定）。
func parseLiveLoginRowSummary(t *testing.T, summary string) []liveLoginRowExpectation {
	t.Helper()
	segments := strings.Split(summary, ";")
	rows := make([]liveLoginRowExpectation, 0, len(segments))
	for _, segment := range segments {
		fields := strings.Split(segment, ":")
		if len(fields) != 7 {
			t.Fatalf("live row_summary segment %q has %d fields, want 7", segment, len(fields))
		}
		slot, err := strconv.Atoi(fields[0])
		if err != nil {
			t.Fatalf("slot %q: %v", fields[0], err)
		}
		itemID, err := strconv.ParseInt(fields[1], 10, 64)
		if err != nil {
			t.Fatalf("item %q: %v", fields[1], err)
		}
		typ, err := strconv.Atoi(strings.TrimPrefix(fields[2], "type="))
		if err != nil {
			t.Fatalf("type %q: %v", fields[2], err)
		}
		durability, err := strconv.Atoi(strings.TrimPrefix(fields[3], "durability="))
		if err != nil {
			t.Fatalf("durability %q: %v", fields[3], err)
		}
		rows = append(rows, liveLoginRowExpectation{
			slot:       int16(slot),
			itemID:     itemID,
			typ:        int16(typ),
			durability: uint16(durability),
			rawBlocks:  strings.TrimPrefix(fields[5], "raw=") == "1",
		})
	}
	return rows
}

// seedLiveLoginChar6 建出与现场同形的角色 6：31 条穿戴行，每行的 0x77 raw 行把
// **现场 row_summary 里的耐久**写进 +0x0B；槽 12 用数据库那一行的真字节与真 Extra。
func seedLiveLoginChar6(t *testing.T, repositories dnfrepo.Group) {
	t.Helper()
	weaponRaw, err := hex.DecodeString(liveLoginWeaponRawEntryHex)
	if err != nil {
		t.Fatalf("decode live weapon raw entry: %v", err)
	}
	if len(weaponRaw) != currentItemListEntryWireSize {
		t.Fatalf("live weapon raw entry = %d bytes, want %d", len(weaponRaw), currentItemListEntryWireSize)
	}
	entries := make(map[string]dnfrepo.EquipmentEntry, 31)
	for _, want := range parseLiveLoginRowSummary(t, liveLoginChar6RowSummary) {
		extra := map[string]string{"current_exe_equipment_type": strconv.Itoa(int(want.typ))}
		raw := make([]byte, currentItemListEntryWireSize)
		binary.LittleEndian.PutUint16(raw[0x00:0x02], uint16(want.typ))
		binary.LittleEndian.PutUint32(raw[0x02:0x06], uint32(want.itemID))
		binary.LittleEndian.PutUint16(raw[0x0B:0x0D], want.durability)
		if int16(want.slot) == liveLoginWeaponSlot {
			raw = weaponRaw
			extra = liveLoginWeaponExtra()
		}
		entries[strconv.Itoa(int(want.slot))] = dnfrepo.EquipmentEntry{
			SlotIndex: want.slot,
			ItemID:    want.itemID,
			RawEntry:  raw,
			Extra:     extra,
		}
	}
	if err := repositories.Equipment.Save(context.Background(), dnfrepo.EquipmentRecord{
		CharacterID: liveLoginChar6ID,
		Entries:     entries,
	}); err != nil {
		t.Fatal(err)
	}
}

func newLiveLoginReader(t *testing.T, repositories dnfrepo.Group, service *Service) *csharpLegacyUserInfoReader {
	t.Helper()
	if service == nil {
		service = &Service{repositoryProvider: func() (dnfrepo.Group, bool) { return repositories, true }}
	}
	return &csharpLegacyUserInfoReader{
		ctx:         context.Background(),
		characterID: liveLoginChar6ID,
		service:     service,
		session:     &gameSession{connID: "login-durability-audit"},
	}
}

func newLiveLoginService(t *testing.T, repositories dnfrepo.Group) *Service {
	t.Helper()
	return &Service{
		repositoryProvider: func() (dnfrepo.Group, bool) { return repositories, true },
		characterStats:     testCharacterStatTable(t),
	}
}

// rowBytesBySlot 把整批 create 行按槽位打成真实下行字节。
func rowBytesBySlot(t *testing.T, rows []currentMode1EquipmentObjectRow) map[int16][]byte {
	t.Helper()
	out := make(map[int16][]byte, len(rows))
	for _, row := range rows {
		var writer packetWriter
		writeCurrentMode1EquipmentCreateRow(&writer, row)
		out[int16(row.slot)] = writer.bytes()
	}
	return out
}

// findCreateRowWindow 在整包体里按"槽位字节 + itemID(LE)"定位某一行的 56 字节窗口。
func findCreateRowWindow(t *testing.T, body []byte, slot byte, itemID int64, rowBytes int) []byte {
	t.Helper()
	pattern := make([]byte, 5)
	pattern[0] = slot
	binary.LittleEndian.PutUint32(pattern[1:5], uint32(itemID))
	offset := bytes.Index(body, pattern)
	if offset < 0 {
		t.Fatalf("create row slot=%d item=%d not found in %d-byte body", slot, itemID, len(body))
	}
	if offset+rowBytes > len(body) {
		t.Fatalf("create row slot=%d starts at %d but body is only %d bytes", slot, offset, len(body))
	}
	return body[offset : offset+rowBytes]
}

// TestLoginChar6ProjectionStillMatchesTheLiveRowSummaryVerbatim 是现场夹具的
// 逐字符钉子：投影出来的 row_summary 必须等于 packet_log.txt 里那一行（31 条）。
func TestLoginChar6ProjectionStillMatchesTheLiveRowSummaryVerbatim(t *testing.T) {
	t.Setenv(mode1EquipmentRowAuditEnv, "")
	t.Setenv(mode1EquipmentRawBlocksEnv, "")
	repositories := dnfrepomemory.NewMemoryGroup()
	seedLiveLoginChar6(t, repositories)

	rows := newLiveLoginReader(t, repositories, nil).currentMode1EquipmentObjectRows()
	if len(rows) != 31 {
		t.Fatalf("projected rows = %d, want the 31 live worn rows", len(rows))
	}
	if got := currentMode1EquipmentRowSummary(rows); got != liveLoginChar6RowSummary {
		t.Fatalf("row_summary drifted from the live log:\n got=%s\nwant=%s", got, liveLoginChar6RowSummary)
	}
	if got := currentMode1EquipmentUnknownTypeCount(rows); got != 0 {
		t.Fatalf("unknown_type_count = %d, want 0", got)
	}
	create := currentMode1EquipmentCreateRows(rows)
	if len(create) != 31 {
		t.Fatalf("create rows = %d, want 31 (no row may fall back to the deferred update path)", len(create))
	}
}

// TestLoginWeaponCreateRowCarriesTheDurabilityTheClientReads 是这次缺陷的正面钉子：
// 武器行必须把耐久写进**客户端真正读的那个位置**（行 +10 的 u16 = 48），并且
// **不带**两段可选 raw 块 —— 客户端自己的判据是 itemTemplate->[0x444] <= 0x0b。
func TestLoginWeaponCreateRowCarriesTheDurabilityTheClientReads(t *testing.T) {
	t.Setenv(mode1EquipmentRowAuditEnv, "")
	t.Setenv(mode1EquipmentRawBlocksEnv, "")
	repositories := dnfrepomemory.NewMemoryGroup()
	seedLiveLoginChar6(t, repositories)

	rows := newLiveLoginReader(t, repositories, nil).currentMode1EquipmentObjectRows()
	var weapon currentMode1EquipmentObjectRow
	found := false
	for _, row := range rows {
		if int16(row.slot) == liveLoginWeaponSlot {
			weapon, found = row, true
		}
	}
	if !found {
		t.Fatalf("live weapon row (slot %d) missing from the projection", liveLoginWeaponSlot)
	}
	if weapon.itemID != liveLoginWeaponItemID || weapon.equipmentType != liveLoginWeaponSlot || !weapon.equipmentTypeKnown {
		t.Fatalf("weapon row identity = item %d type %d known=%t, want item %d type %d known=true",
			weapon.itemID, weapon.equipmentType, weapon.equipmentTypeKnown, liveLoginWeaponItemID, liveLoginWeaponSlot)
	}
	if !weapon.durabilityKnown || weapon.durability != liveLoginWeaponDurability {
		t.Fatalf("weapon durability word = (%d, known=%t), want (%d, true)",
			weapon.durability, weapon.durabilityKnown, liveLoginWeaponDurability)
	}
	if weapon.readsRawBlocks {
		t.Fatal("weapon row (type 12) must not read the two optional raw blocks: the current " +
			"client gates them on sub_229ACD0 = itemTemplate->[0x444] <= 0x0b (90CN DNF.exe RVA 0x1E9ACD0)")
	}
	if currentMode1EquipmentReadsRawBlocks(liveLoginWeaponExtra(), liveLoginWeaponSlot, true) {
		t.Fatal("currentMode1EquipmentReadsRawBlocks must follow the client gate for type 12")
	}

	rowBytes := rowBytesBySlot(t, rows)[liveLoginWeaponSlot]
	if len(rowBytes) != 56 {
		t.Fatalf("weapon create row = %d bytes, want 56 (no two 4-byte raw-block headers)", len(rowBytes))
	}
	if rowBytes[0] != liveLoginWeaponSlot {
		t.Fatalf("weapon create row slot byte = %d, want %d", rowBytes[0], liveLoginWeaponSlot)
	}
	if got := binary.LittleEndian.Uint32(rowBytes[1:5]); got != liveLoginWeaponItemID {
		t.Fatalf("weapon create row item id @+1 = %d, want %d", got, liveLoginWeaponItemID)
	}
	if got := binary.LittleEndian.Uint16(rowBytes[10:12]); got != liveLoginWeaponDurability {
		t.Fatalf("weapon create row durability @+10 = %d, want %d (the u16 sub_1D77560 reads at RVA 0x19776AB)",
			got, liveLoginWeaponDurability)
	}
	if got := rowBytes[9]; got != 15 {
		t.Fatalf("weapon create row extAttr @+9 = %d, want the live packed/enhance byte 15", got)
	}
	// 行 +24 起是"两段 raw 块"该在的位置：类型 12 的行这里必须是
	// vector1D6E020 count(0) + state112(0) + vector225CCA0 count(0) + state1E636E0(0)，
	// 一个非零字节都说明有额外块被塞进来。
	if got := rowBytes[24:32]; !bytes.Equal(got, make([]byte, 8)) {
		t.Fatalf("weapon create row @+24..+31 = % X, want 8 zero bytes (no raw-block headers)", got)
	}
	// 三段 0 长度 late raw 块与 state183：确认尾部结构与 56 字节对齐一致。
	for _, offset := range []int{43, 47, 52} {
		if got := binary.LittleEndian.Uint32(rowBytes[offset : offset+4]); got != 0 {
			t.Fatalf("weapon create row @+%d length = %d, want 0", offset, got)
		}
	}
}

// handBuiltWeaponCreateRow 手工重建期望的 56 字节（不调用被测写入器），
// 用作"回退档逐字节 = 今天"的独立依据。
func handBuiltWeaponCreateRow() []byte {
	out := make([]byte, 56)
	out[0] = liveLoginWeaponSlot
	binary.LittleEndian.PutUint32(out[1:5], liveLoginWeaponItemID)
	binary.LittleEndian.PutUint32(out[5:9], 999999998) // TopSeed：库里的 quality_seed
	out[9] = 15                                        // ext_data0 / packed flag byte
	binary.LittleEndian.PutUint16(out[10:12], liveLoginWeaponDurability)
	binary.LittleEndian.PutUint32(out[12:16], 0)        // linkedItemID（无克隆来源）
	binary.LittleEndian.PutUint32(out[16:20], 10015138) // raw+0x0E 的 value_a
	out[20] = 0                                         // byte_12
	out[21] = 0
	binary.LittleEndian.PutUint16(out[22:24], 0)
	return out
}

// TestLoginWeaponRowRollbackTierIsByteForByteTodaysBytes 钉住"默认档 = 今天"：
// 手工重建的 56 字节必须与写入器输出完全一致，并且 sha256 冻结在常量里。
func TestLoginWeaponRowRollbackTierIsByteForByteTodaysBytes(t *testing.T) {
	t.Setenv(mode1EquipmentRowAuditEnv, "")
	t.Setenv(mode1EquipmentRawBlocksEnv, "")
	repositories := dnfrepomemory.NewMemoryGroup()
	seedLiveLoginChar6(t, repositories)

	rows := newLiveLoginReader(t, repositories, nil).currentMode1EquipmentObjectRows()
	got := rowBytesBySlot(t, rows)[liveLoginWeaponSlot]
	want := handBuiltWeaponCreateRow()
	if !bytes.Equal(got, want) {
		t.Fatalf("weapon create row drifted from the frozen 56-byte shape:\n got=%x\nwant=%x", got, want)
	}
	sum := sha256.Sum256(got)
	if hex.EncodeToString(sum[:]) != "afc50de39ba43bae0bffb32b118ea72745bf84f03afd712ef06a50c0fae74c6c" {
		t.Fatalf("weapon create row hash = %x, want the frozen value", sum)
	}
	if hex.EncodeToString(got) != "0c6dc10506fec99a3b0f300000000000a2d19800000000000000000000000000000000000000000000000000000000000000000000000000" {
		t.Fatalf("weapon create row hex = %x, want the frozen value", got)
	}
}

// TestLoginAvatarRowsKeepTheirRawBlockHeadersUntouched 钉住"不影响 type<=11 行"：
// 现场槽 3（上衣装扮，raw=1）的行仍然是 64 字节、耐久 47 在 +10、
// 且在 +24/+28 各有一段 0 长度的 raw 块头。
func TestLoginAvatarRowsKeepTheirRawBlockHeadersUntouched(t *testing.T) {
	t.Setenv(mode1EquipmentRowAuditEnv, "")
	t.Setenv(mode1EquipmentRawBlocksEnv, "")
	repositories := dnfrepomemory.NewMemoryGroup()
	seedLiveLoginChar6(t, repositories)

	rows := newLiveLoginReader(t, repositories, nil).currentMode1EquipmentObjectRows()
	avatarBytes := rowBytesBySlot(t, rows)[liveLoginAvatarSlot]
	if len(avatarBytes) != 64 {
		t.Fatalf("avatar create row (slot %d) = %d bytes, want 64 (56 + two empty raw headers)",
			liveLoginAvatarSlot, len(avatarBytes))
	}
	if got := binary.LittleEndian.Uint16(avatarBytes[10:12]); got != liveLoginAvatarDurability {
		t.Fatalf("avatar create row durability @+10 = %d, want %d", got, liveLoginAvatarDurability)
	}
	if got := binary.LittleEndian.Uint32(avatarBytes[24:28]); got != 0 {
		t.Fatalf("avatar create row first raw-block length @+24 = %d, want 0", got)
	}
	if got := binary.LittleEndian.Uint32(avatarBytes[28:32]); got != 0 {
		t.Fatalf("avatar create row second raw-block length @+28 = %d, want 0", got)
	}
	// 类型 12 的行则不许多出这 8 字节：现场夹具里槽 12 = raw=0。
	if got := rowBytesBySlot(t, rows)[liveLoginWeaponSlot]; len(got) != 56 {
		t.Fatalf("weapon row = %d bytes while the avatar row is 64: the two branches are still distinct", len(got))
	}
}

// TestLoginAndMapEntryWeaponRowsAreTheSameBytes 是本轮**最关键的比较**：
// 登录（`buildCurrentSelectedUserInfoMode1BodyWithAdventureLevelInContext`，
// initial_town_player_state.go:167）与进图（
// `buildCurrentActorBindingMode1BodyForSelectedWithEquipmentInContext(..., true)`，
// scene_post_start_map.go:381）用的是同一个写入器、同一批行 —— 两边的武器行必须
// 逐字节相同。操作者看到"进图后耐久正确"，而进图那一行也是 raw=0：
// ⇒ 投影行不是缺陷所在。
func TestLoginAndMapEntryWeaponRowsAreTheSameBytes(t *testing.T) {
	t.Setenv(mode1EquipmentRowAuditEnv, "")
	t.Setenv(mode1EquipmentRawBlocksEnv, "")
	repositories := dnfrepomemory.NewMemoryGroup()
	seedLiveLoginChar6(t, repositories)
	service := newLiveLoginService(t, repositories)
	character := dnfrepo.CharacterRecord{CharacterID: liveLoginChar6ID, AccountID: "dnf:10011", Name: "test78", Level: 90}

	loginBody := service.buildCurrentSelectedUserInfoMode1BodyWithAdventureLevel(
		context.Background(), nil, nil, character, true, 6, 3)
	mapEntryBody := service.buildCurrentActorBindingMode1BodyForSelectedWithEquipmentInContext(
		context.Background(), nil, character, true, 6, 3, currentSceneObjectContext, true)

	loginRow := findCreateRowWindow(t, loginBody, liveLoginWeaponSlot, liveLoginWeaponItemID, 56)
	mapRow := findCreateRowWindow(t, mapEntryBody, liveLoginWeaponSlot, liveLoginWeaponItemID, 56)
	if !bytes.Equal(loginRow, mapRow) {
		t.Fatalf("login weapon row != map-entry weapon row:\n login=%x\n  map =%x", loginRow, mapRow)
	}
	if got := binary.LittleEndian.Uint16(loginRow[10:12]); got != liveLoginWeaponDurability {
		t.Fatalf("shared weapon row durability @+10 = %d, want %d", got, liveLoginWeaponDurability)
	}
}

// TestWeaponRawBlocksExperimentTierIsOptInAndOffByDefault 钉住实验档的边界：
// 未写开关 = 今天（56 字节）；只有显式 all_types 才会多出两段空 raw 块（64 字节）。
// 这个实验档**不是**修复档：客户端判据是 type<=0x0b，打开会让武器行与其后所有行错位。
func TestWeaponRawBlocksExperimentTierIsOptInAndOffByDefault(t *testing.T) {
	repositories := dnfrepomemory.NewMemoryGroup()
	seedLiveLoginChar6(t, repositories)

	t.Setenv(mode1EquipmentRawBlocksEnv, "")
	if mode1EquipmentRawBlocksForAllTypes() {
		t.Fatal("raw-block experiment tier must be off by default")
	}
	if got := mode1EquipmentRawBlocksSwitch(); got != mode1EquipmentRawBlocksEvidence {
		t.Fatalf("raw-block switch = %q, want %q", got, mode1EquipmentRawBlocksEvidence)
	}
	def := rowBytesBySlot(t, newLiveLoginReader(t, repositories, nil).currentMode1EquipmentObjectRows())
	if len(def[liveLoginWeaponSlot]) != 56 {
		t.Fatalf("default tier weapon row = %d bytes, want 56", len(def[liveLoginWeaponSlot]))
	}

	t.Setenv(mode1EquipmentRawBlocksEnv, mode1EquipmentRawBlocksAllTypes)
	if !mode1EquipmentRawBlocksForAllTypes() {
		t.Fatal("explicit all_types literal must enable the experiment tier")
	}
	experiment := rowBytesBySlot(t, newLiveLoginReader(t, repositories, nil).currentMode1EquipmentObjectRows())
	if len(experiment[liveLoginWeaponSlot]) != 64 {
		t.Fatalf("experiment tier weapon row = %d bytes, want 64 (56 + two empty raw headers)",
			len(experiment[liveLoginWeaponSlot]))
	}
	if !bytes.Equal(experiment[liveLoginWeaponSlot][:24], def[liveLoginWeaponSlot][:24]) {
		t.Fatal("experiment tier must only append the two raw-block headers, not move the durability word")
	}
	// 回退：写回默认档后逐字节回到今天。
	t.Setenv(mode1EquipmentRawBlocksEnv, mode1EquipmentRawBlocksEvidence)
	back := rowBytesBySlot(t, newLiveLoginReader(t, repositories, nil).currentMode1EquipmentObjectRows())
	if !bytes.Equal(back[liveLoginWeaponSlot], def[liveLoginWeaponSlot]) {
		t.Fatal("returning to the evidence literal must restore the frozen bytes")
	}
}

// TestMode1EquipmentRowAuditRecordsTheWeaponFacts 钉住新事件：
// 默认档每次投影都打一条逐行审计，武器行的槽/类型/下发耐久/是否带 raw 块 +
// 客户端读法证据都在同一行里，下一次登录可直接从 packet_log.txt 判定。
func TestMode1EquipmentRowAuditRecordsTheWeaponFacts(t *testing.T) {
	t.Setenv(mode1EquipmentRowAuditEnv, "")
	t.Setenv(mode1EquipmentRawBlocksEnv, "")
	logger, logPath := newPeerEndpointTestLogger(t)
	repositories := dnfrepomemory.NewMemoryGroup()
	seedLiveLoginChar6(t, repositories)
	service := newLiveLoginService(t, repositories)
	service.packetLog = logger

	newLiveLoginReader(t, repositories, service).currentMode1EquipmentObjectRows()

	log := readPeerEndpointTestLog(t, logger, logPath)
	for _, want := range []string{
		"game-upper-selected-userinfo-mode1-equipment-row-audit",
		"weapon_audit=present",
		"weapon_slot=12",
		"weapon_type=12",
		"weapon_durability_sent=48",
		"weapon_raw_blocks=0",
		"weapon_row_bytes=56",
		"weapon_instance=999999998",
		"client_durability_read=row_plus_10_u16_sub_1D77560_0x19776AB",
		"client_raw_block_gate=item_template_plus_0x444_le_0x0b_sub_229ACD0_via_sub_225C960",
		"raw_blocks_switch=evidence",
		"audit_switch=on",
	} {
		if !strings.Contains(log, want) {
			t.Fatalf("row audit is missing %q; log=\n%s", want, log)
		}
	}
	if !strings.Contains(log, "slot=12,type=12,durability_sent=48,raw_blocks=0,row_bytes=56") {
		t.Fatalf("per-row audit line for the weapon is wrong; log=\n%s", log)
	}
}

// TestMode1EquipmentRowAuditRollbackLiteralIsSilent 钉住回退字面量：
// 写 off 之后不再打审计事件，而且**下行字节逐字节不变**（本开关不碰 wire）。
func TestMode1EquipmentRowAuditRollbackLiteralIsSilent(t *testing.T) {
	t.Setenv(mode1EquipmentRawBlocksEnv, "")
	repositories := dnfrepomemory.NewMemoryGroup()
	seedLiveLoginChar6(t, repositories)

	t.Setenv(mode1EquipmentRowAuditEnv, "")
	loggerOn, pathOn := newPeerEndpointTestLogger(t)
	serviceOn := newLiveLoginService(t, repositories)
	serviceOn.packetLog = loggerOn
	rowsOn := newLiveLoginReader(t, repositories, serviceOn).currentMode1EquipmentObjectRows()
	logOn := readPeerEndpointTestLog(t, loggerOn, pathOn)

	t.Setenv(mode1EquipmentRowAuditEnv, mode1EquipmentRowAuditOff)
	loggerOff, pathOff := newPeerEndpointTestLogger(t)
	serviceOff := newLiveLoginService(t, repositories)
	serviceOff.packetLog = loggerOff
	rowsOff := newLiveLoginReader(t, repositories, serviceOff).currentMode1EquipmentObjectRows()
	logOff := readPeerEndpointTestLog(t, loggerOff, pathOff)

	if !strings.Contains(logOn, "game-upper-selected-userinfo-mode1-equipment-row-audit") {
		t.Fatalf("default tier must emit the row audit; log=\n%s", logOn)
	}
	if strings.Contains(logOff, "game-upper-selected-userinfo-mode1-equipment-row-audit") {
		t.Fatalf("rollback literal must not emit the row audit; log=\n%s", logOff)
	}
	if !strings.Contains(logOff, "game-upper-selected-userinfo-mode1-equipment-create-rows") {
		t.Fatalf("rollback literal must keep the pre-existing create-rows event; log=\n%s", logOff)
	}
	if got, want := rowBytesBySlot(t, rowsOff), rowBytesBySlot(t, rowsOn); len(got) != len(want) {
		t.Fatalf("row count changed between tiers: %d vs %d", len(got), len(want))
	} else {
		for slot, wantBytes := range want {
			if !bytes.Equal(got[slot], wantBytes) {
				t.Fatalf("audit switch changed the downlink bytes for slot %d:\n on =%x\n off=%x", slot, wantBytes, got[slot])
			}
		}
	}
}

// TestCurrentMode1EquipmentReadsRawBlocksFollowsTheClientGate 复核客户端判据，
// 并把"给 type>11 补 raw 块"固定为**必须显式打开的实验档**。
func TestCurrentMode1EquipmentReadsRawBlocksFollowsTheClientGate(t *testing.T) {
	t.Setenv(mode1EquipmentRawBlocksEnv, "")
	if !currentMode1EquipmentReadsRawBlocks(nil, 11, true) {
		t.Fatal("type 11 must read the raw blocks (client: itemTemplate->[0x444] <= 0x0b)")
	}
	if currentMode1EquipmentReadsRawBlocks(nil, 12, true) {
		t.Fatal("type 12 (weapon) must not read the raw blocks")
	}
	if !currentMode1EquipmentReadsRawBlocks(map[string]string{"mode1_reads_raw_blocks": "1"}, 12, true) {
		t.Fatal("the explicit per-row extra stays authoritative")
	}
	t.Setenv(mode1EquipmentRawBlocksEnv, mode1EquipmentRawBlocksAllTypes)
	if !currentMode1EquipmentReadsRawBlocks(nil, 12, true) {
		t.Fatal("the experiment literal must be the only way to force raw blocks for type>11")
	}
}
