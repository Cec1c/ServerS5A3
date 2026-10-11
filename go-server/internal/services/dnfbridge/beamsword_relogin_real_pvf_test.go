package dnfbridge

import (
	"encoding/binary"
	"os"
	"strconv"
	"testing"

	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

// 现场值（HK dnf_local，character_id=6 / slot_index=12）——与
// dnf_equipment_entries.raw_entry / dnf_equipment_entry_extra 逐字节一致。
const (
	reloginBeamswordItemID     int64  = 101040493
	reloginBeamswordSeed       uint32 = 203004656
	reloginBeamswordDurability uint16 = 48
	reloginBeamswordPath              = "equipment/character/swordman/weapon/beamsword/101040493.equ"
)

// 真实运行时 PVF 回归：操作者报障的那把**光剑**（角色 6 test78 身上那把）在真档里
// 到底是什么形状。它决定了"武器重登被卸下"能不能用"PVF 缺键"解释 —— 结论是不能。
//
// 现场（HK dnf_local，character_id=6 / slot_index=12）：
//
//	item_id    101040493
//	pvf_path   equipment/character/swordman/weapon/beamsword/101040493.equ
//	durability 48 / max_durability 48 / quality_seed 203004656
//	raw[0x0A]=13 raw[0x0B:0x0D]=48 raw[0x0E:0x12]=101040493（119 字节）
//
// 运行：DNFBRIDGE_REAL_PVF_SMOKE=<repo>\runtime\data\dnf\Script.pvf go test -count=1 \
//
//	-run RealPVFBeamsword -v ./internal/services/dnfbridge
//
// 逐条否掉的候选（对照任务给出的四个候选根因）：
//   - ③「该武器的 PVF 段落缺失某个键」：真档 `[durability] 48` **存在**，
//     且与库里的 durability/max_durability 完全一致；没有 `[maximum durability]`
//     不是缺陷 —— `[maximum durability]` 在同档所有武器/护甲上都不存在
//     （对照 `equipment/character/swordman/weapon/katana/101010912.equ`：同样只有
//     `[durability] 45`）。`[equipment type] \`[weapon]\` 25` 里的 25 是**光剑**
//     的子类型编号（对照太刀是 17），不是异常值。
//   - ②「身份证据在重建时丢失」：库行身份键齐全且自洽（quality_seed == raw[6:10]、
//     durability == raw[0x0B:0x0D] == PVF `[durability]`、item_kind/pvf_path/槽位一致）。
//   - ①「耐久 0/为负」：耐久是 48/48，见上。
//   - ④「登录期 reconcile 当成过期清掉」：本行没有任何到期键，登录期 reconcile 只作用于
//     邮件附件（joust_settlement_mail_expiration.go），不碰装备表。
//
// 真正的机制是 op19 端点方向判定（见 equip.equipmentEndpointIdentityEcho 与
// equip/equipment_endpoint_echo_test.go）。
func TestRealPVFBeamswordOnTheReportedCharacterIsAWellFormedWeapon(t *testing.T) {
	pvfPath := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if pvfPath == "" {
		t.Skip("set DNFBRIDGE_REAL_PVF_SMOKE to the runtime Script.pvf")
	}
	archive, err := platformpvf.LoadArchive(platformpvf.Options{Path: pvfPath})
	if err != nil {
		t.Fatalf("load real Script.pvf: %v", err)
	}
	const weaponPath = "equipment/character/swordman/weapon/beamsword/101040493.equ"
	text, err := archive.ReadText(weaponPath)
	if err != nil {
		t.Fatalf("read %s: %v", weaponPath, err)
	}
	document, err := dnfpvf.Parse(weaponPath, text)
	if err != nil {
		t.Fatalf("parse %s: %v", weaponPath, err)
	}

	if name, _ := document.Text("name"); name != "圣耀救赎光剑" {
		t.Fatalf("[name] = %q, want 圣耀救赎光剑", name)
	}
	durability, ok := document.Int("durability")
	if !ok || durability != int64(reloginBeamswordDurability) {
		t.Fatalf("[durability] = %d (present=%t), want %d == the live DB durability", durability, ok, reloginBeamswordDurability)
	}
	for _, name := range []string{"maximum durability", "max durability"} {
		if _, present := document.Int(name); present {
			t.Fatalf("[%s] unexpectedly present: the live weapon declares durability through [durability] only", name)
		}
	}
	if grade, _ := document.Int("grade"); grade != 96 {
		t.Fatalf("[grade] = %d, want 96", grade)
	}
	if rarity, _ := document.Int("rarity"); rarity != 4 {
		t.Fatalf("[rarity] = %d, want 4 (epic)", rarity)
	}
	if level, _ := document.Int("minimum level"); level != 90 {
		t.Fatalf("[minimum level] = %d, want 90", level)
	}
	equipmentType, _ := document.Text("equipment type")
	if equipmentType != "[weapon]" {
		t.Fatalf("[equipment type] = %q, want [weapon]", equipmentType)
	}
	// `[equipment type] \`[weapon]\` 25`：第三个 token 是子类型编号（光剑 = 25，
	// 太刀 = 17），不是解析异常。
	types := document.Ints("equipment type")
	if len(types) != 1 || types[0] != 25 {
		t.Fatalf("[equipment type] ints = %v, want [25] (beamsword sub type)", types)
	}
	jobText, _ := document.Text("usable job")
	if jobText != "[swordman]" {
		t.Fatalf("[usable job] first entry = %q, want [swordman] (character 6 is job=0 grow_type=1)", jobText)
	}
}

// 同一把武器的 0x77 行投影必须与库里那一行完全一致：耐久在 +0x0B、强化/打包字节在
// +0x0A、实例 = quality_seed。任何一项错位，客户端看到的武器行就会与权威状态不一致
// （那正是客户端在场景就绪后复述穿戴态、服务端又把它当"脱下"执行的前提）。
func TestRealPVFBeamswordWornRowProjectsDurabilityAndInstance(t *testing.T) {
	raw := make([]byte, 0x77)
	binary.LittleEndian.PutUint16(raw[0:2], 12)
	binary.LittleEndian.PutUint32(raw[2:6], uint32(reloginBeamswordItemID))
	binary.LittleEndian.PutUint32(raw[6:10], reloginBeamswordSeed)
	raw[0x0A] = 13
	binary.LittleEndian.PutUint16(raw[0x0B:0x0D], reloginBeamswordDurability)
	binary.LittleEndian.PutUint32(raw[0x0E:0x12], uint32(reloginBeamswordItemID))

	entry := dnfrepo.EquipmentEntry{
		SlotIndex: 12,
		ItemID:    reloginBeamswordItemID,
		RawEntry:  raw,
		Extra: map[string]string{
			"current_exe_equipment_type": "12",
			"durability":                 strconv.FormatUint(uint64(reloginBeamswordDurability), 10),
			"equipment_type":             "[weapon]",
			"ext_data0":                  "13",
			"item_kind":                  "equipment",
			"max_durability":             strconv.FormatUint(uint64(reloginBeamswordDurability), 10),
			"packed_flag_byte":           "13",
			"pvf_path":                   reloginBeamswordPath,
			"quality_seed":               strconv.FormatUint(uint64(reloginBeamswordSeed), 10),
			"raw_entry_hex":              hexString(raw),
		},
	}
	projected, ok := currentItemListEntryFromEquipment(entry)
	if !ok {
		t.Fatal("worn beamsword row did not project")
	}
	if got := binary.LittleEndian.Uint16(projected.data[0:2]); got != 12 {
		t.Fatalf("projected slot = %d, want 12", got)
	}
	if got := binary.LittleEndian.Uint32(projected.data[2:6]); got != uint32(reloginBeamswordItemID) {
		t.Fatalf("projected item = %d, want %d", got, reloginBeamswordItemID)
	}
	if got := binary.LittleEndian.Uint32(projected.data[6:10]); got != reloginBeamswordSeed {
		t.Fatalf("projected instance = %d, want the quality seed %d", got, reloginBeamswordSeed)
	}
	if got := projected.data[0x0A]; got != 13 {
		t.Fatalf("projected +0x0A = %d, want the packed/enhance byte 13", got)
	}
	if got := binary.LittleEndian.Uint16(projected.data[0x0B:0x0D]); got != reloginBeamswordDurability {
		t.Fatalf("projected durability = %d, want %d", got, reloginBeamswordDurability)
	}
}

func hexString(raw []byte) string {
	const digits = "0123456789abcdef"
	out := make([]byte, 0, len(raw)*2)
	for _, value := range raw {
		out = append(out, digits[value>>4], digits[value&0x0f])
	}
	return string(out)
}
