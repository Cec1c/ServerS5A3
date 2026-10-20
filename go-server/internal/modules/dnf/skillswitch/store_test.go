package skillswitch

import (
	"context"
	"encoding/hex"
	"errors"
	"strconv"
	"strings"
	"testing"

	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	dnfrepomemory "longheng.io/server/internal/modules/dnf/repository/memory"
	dnfskill "longheng.io/server/internal/modules/dnf/skill"
)

// 本文件的 body 全部是**现场真实样本**（HK `/opt/dnf90/logs/packet_log*.txt`
// 的 `type=1428` RECV 行尾 6 字节；本仓 RECV 行 `len=13+body_len`）。
//
// ⚠️ 那 6 字节 = **2 字节导线前缀（客户端 u32 序号域高 16 位）+ 4 字节真包体**
// （2026-09-28 实机更正，见 record_body_layout_test.go 与本文件的两侧表）。
// 因此本文件里凡是要"逐字节等于改动前"的断言，都显式选 `LayoutV6`（一键回退档）；
// 默认档 `LayoutV4` 的 4 字节形由 `record_body_layout_test.go` 与
// `TestApplyV4PersistsCanonicalBodyAndUpgradesLegacyRows` 钉住。

// 操作者 2026-09-27 给的**当前客户端**4 条（`conn_id=game-000016 char_id=1`）：
//
//	00000c030c00 / 000013031300 / 000015031500 / 000016031600
var currentClientBodies = []string{
	"00000c030c00",
	"000013031300",
	"000015031500",
	"000016031600",
}

// 2026-09-27 14:28 的 6 条（header_seq 337,338,339,340,348,349）整帧：
//
//	019405130000002fbf999d510100000e030e00
//	01940513000000add3528b520100000f030f00
//	01940513000000720002c65301000010031000
//	019405130000005c45f4dc5401000012031200
//	01940513000000550cf1dc5c0100001a031a00
//	01940513000000770b4c9f5d01000003030300
var liveFourteenThirtyBodies = []string{
	"00000e030e00", "00000f030f00", "000010031000",
	"000012031200", "00001a031a00", "000003030300",
}

// `ModeRegister` 双槽配对形态（2026-09-26 01:21，header_seq=567..577 的 11 连发里抽样）。
var livePairedBodies = []string{
	"00000c003600", // 12 -> 54
	"000017003700", // 23 -> 55
	"000016003800", // 22 -> 56
	"000014004000", // 20 -> 64
}

// `ModeClear` 枚举哨兵（8/8 同形）：整帧
// `0194051300000042b1b5e58f012600212cffff`。
const liveSentinelBody = "2600212cffff"

// `ModeUnread` 唯一 1 条样本：整帧
// `0194051300000002e38ed09101260003013700`。
const liveUnreadBody = "260003013700"

func mustDecode(t *testing.T, value string) []byte {
	t.Helper()
	raw, err := hex.DecodeString(value)
	if err != nil {
		t.Fatalf("hex %q: %v", value, err)
	}
	return raw
}

func TestParseRecordLiveBodies(t *testing.T) {
	tests := []struct {
		body string
		want Record
	}{
		// 当前客户端 4 条：ModeConfirm 形态，Slot == Value（v6 档：前缀 + 包体）。
		{"00000c030c00", Record{WirePrefix: 0, Slot: 12, Mode: ModeConfirm, Value: 12, Layout: LayoutV6}},
		{"000013031300", Record{WirePrefix: 0, Slot: 19, Mode: ModeConfirm, Value: 19, Layout: LayoutV6}},
		{"000015031500", Record{WirePrefix: 0, Slot: 21, Mode: ModeConfirm, Value: 21, Layout: LayoutV6}},
		{"000016031600", Record{WirePrefix: 0, Slot: 22, Mode: ModeConfirm, Value: 22, Layout: LayoutV6}},
		// 14:28 那 6 条。
		{"00000e030e00", Record{WirePrefix: 0, Slot: 14, Mode: ModeConfirm, Value: 14, Layout: LayoutV6}},
		{"00000f030f00", Record{WirePrefix: 0, Slot: 15, Mode: ModeConfirm, Value: 15, Layout: LayoutV6}},
		{"000010031000", Record{WirePrefix: 0, Slot: 16, Mode: ModeConfirm, Value: 16, Layout: LayoutV6}},
		{"000012031200", Record{WirePrefix: 0, Slot: 18, Mode: ModeConfirm, Value: 18, Layout: LayoutV6}},
		{"00001a031a00", Record{WirePrefix: 0, Slot: 26, Mode: ModeConfirm, Value: 26, Layout: LayoutV6}},
		{"000003030300", Record{WirePrefix: 0, Slot: 3, Mode: ModeConfirm, Value: 3, Layout: LayoutV6}},
		// ModeRegister 双槽配对。
		{"00000c003600", Record{WirePrefix: 0, Slot: 12, Mode: ModeRegister, Value: 54, Layout: LayoutV6}},
		{"000017003700", Record{WirePrefix: 0, Slot: 23, Mode: ModeRegister, Value: 55, Layout: LayoutV6}},
		{"000016003800", Record{WirePrefix: 0, Slot: 22, Mode: ModeRegister, Value: 56, Layout: LayoutV6}},
		{"000014004000", Record{WirePrefix: 0, Slot: 20, Mode: ModeRegister, Value: 64, Layout: LayoutV6}},
		// ModeClear 哨兵与 ModeUnread 唯一一条（前缀 0x0026 = 客户端 u32 序号域高 16 位，
		// **不是** 6 字节读法里的 "scope"）。
		{liveSentinelBody, Record{WirePrefix: 0x26, Slot: 0x21, Mode: ModeClear, Value: ValueNone, Layout: LayoutV6}},
		{liveUnreadBody, Record{WirePrefix: 0x26, Slot: 3, Mode: ModeUnread, Value: 55, Layout: LayoutV6}},
	}
	for _, test := range tests {
		body := mustDecode(t, test.body)
		got, err := ParseRecordIn(LayoutV6, body)
		if err != nil {
			t.Fatalf("ParseRecordIn(v6, %s) error = %v", test.body, err)
		}
		if got != test.want {
			t.Fatalf("ParseRecordIn(v6, %s) = %+v, want %+v", test.body, got, test.want)
		}
		if got.Hex() != test.body {
			t.Fatalf("Bytes() round trip = %s, want %s", got.Hex(), test.body)
		}
		// 同一条样本按**默认档 v4** 读：字段必须相同，字节形是 4 字节包体。
		fixed, err := ParseRecord(body)
		if err != nil {
			t.Fatalf("ParseRecord(v4, %s) error = %v", test.body, err)
		}
		if !fixed.SameFields(got) || fixed.Layout != LayoutV4 {
			t.Fatalf("ParseRecord(v4, %s) = %+v, want the v6 fields with layout v4", test.body, fixed)
		}
		if fixed.Hex() != test.body[4:] {
			t.Fatalf("ParseRecord(v4, %s).Hex() = %s, want the 4-byte body %s", test.body, fixed.Hex(), test.body[4:])
		}
	}

	// ModeConfirm 的不变量：现场 9/9 + 当前客户端 4/4 都是 Slot == Value。
	for _, body := range append(append([]string(nil), currentClientBodies...), liveFourteenThirtyBodies...) {
		record, err := ParseRecord(mustDecode(t, body))
		if err != nil {
			t.Fatalf("body %s: %v", body, err)
		}
		if record.Mode != ModeConfirm || record.Slot != byte(record.Value) {
			t.Fatalf("body %s = %+v, want ModeConfirm with Slot == Value", body, record)
		}
	}

	// ModeRegister 的不变量：Slot 是 12..23 去掉 13，Value 是 54..64。
	seenA := map[byte]bool{}
	seenB := map[uint16]bool{}
	for _, body := range livePairedBodies {
		record, err := ParseRecord(mustDecode(t, body))
		if err != nil {
			t.Fatalf("body %s: %v", body, err)
		}
		if record.Mode != ModeRegister {
			t.Fatalf("body %s mode = 0x%x, want ModeRegister", body, record.Mode)
		}
		if record.Slot < 12 || record.Slot > 23 || record.Slot == 13 {
			t.Fatalf("body %s Slot = %d, want inside 12..23 without 13", body, record.Slot)
		}
		if record.Value < 54 || record.Value > 64 {
			t.Fatalf("body %s Value = %d, want inside 54..64", body, record.Value)
		}
		seenA[record.Slot] = true
		seenB[record.Value] = true
	}
	if len(seenA) != len(livePairedBodies) || len(seenB) != len(livePairedBodies) {
		t.Fatalf("paired burst is not bijective: A=%v B=%v", seenA, seenB)
	}
}

func TestParseRecordRejectsWrongShape(t *testing.T) {
	// v6（回退档）只认 6 字节：多一字节、少一字节、以及 4 字节包体全部具名拒绝。
	for _, body := range [][]byte{nil, {}, {0x00}, mustDecode(t, "00000c030c"), mustDecode(t, "00000c030c0000"),
		mustDecode(t, "0c030c00")} {
		if _, err := ParseRecordIn(LayoutV6, body); !errors.Is(err, ErrRecordShape) {
			t.Fatalf("ParseRecordIn(v6, %x) error = %v, want ErrRecordShape", body, err)
		}
	}
	// v4（默认档）认 4 与 6，其余全部具名拒绝。
	for _, body := range [][]byte{nil, {}, {0x00}, mustDecode(t, "00000c030c"), mustDecode(t, "00000c030c0000")} {
		if _, err := ParseRecord(body); !errors.Is(err, ErrRecordShape) {
			t.Fatalf("ParseRecord(v4, %x) error = %v, want ErrRecordShape", body, err)
		}
	}
}

func TestRecordSkillIDAndBoundSlot(t *testing.T) {
	confirm, _ := ParseRecord(mustDecode(t, "0c030c00"))
	if skillID, ok := confirm.SkillID(); !ok || skillID != 12 {
		t.Fatalf("ModeConfirm SkillID = (%d,%t), want (12,true)", skillID, ok)
	}
	if slot, ok := confirm.BoundSlot(); !ok || slot != 12 {
		t.Fatalf("ModeConfirm BoundSlot = (%d,%t), want (12,true)", slot, ok)
	}
	register, _ := ParseRecord(mustDecode(t, "0c003600"))
	if skillID, ok := register.SkillID(); !ok || skillID != 12 {
		t.Fatalf("ModeRegister SkillID = (%d,%t), want (12,true)", skillID, ok)
	}
	if slot, ok := register.BoundSlot(); !ok || slot != 54 {
		t.Fatalf("ModeRegister BoundSlot = (%d,%t), want (54,true)", slot, ok)
	}
	sentinel, _ := ParseRecord(mustDecode(t, "212cffff"))
	if _, ok := sentinel.SkillID(); ok {
		t.Fatal("ModeClear must not claim a skill id")
	}
	if !sentinel.Sentinel() {
		t.Fatal("ModeClear must report Sentinel()==true")
	}
	// Slot 是重投影的排序键，三种形态都取包体 off0。
	if sentinel.Slot != 0x21 || confirm.Slot != 12 || register.Slot != 12 {
		t.Fatalf("Slot = %d/%d/%d", sentinel.Slot, confirm.Slot, register.Slot)
	}
}

// skillSwitchTestSource 是最小的内存 PVF 文本源。
type skillSwitchTestSource map[string]string

func (s skillSwitchTestSource) ReadText(relativePath string) (string, error) {
	value, ok := s[strings.TrimSpace(relativePath)]
	if !ok {
		return "", errors.New("missing pvf text: " + relativePath)
	}
	return value, nil
}

// jobScopedCatalog 造一个 job 14 的目录，只含**现场真实出现过**的技能号
// （3/12/14/15/16/18/19/21/22/26）。4 与 10 **故意不存在**，
// 用来做「未知技能号 ⇒ 具名拒绝」的证伪点。
func jobScopedCatalog(t *testing.T) *dnfskill.Table {
	t.Helper()
	present := []int{3, 12, 14, 15, 16, 18, 19, 21, 22, 26}
	source := skillSwitchTestSource{}
	list := ""
	for _, id := range present {
		doc := "skill/job14/skill" + strconv.Itoa(id) + ".skl"
		list += strconv.Itoa(id) + " `" + doc + "`\n"
		source[doc] = "[skill type]\n`active`\n"
	}
	source["skill/skilllist.lst"] = "14 `job14.lst`\n"
	source["skill/job14.lst"] = list
	index, err := dnfpvf.Build(context.Background(), source, dnfpvf.BuildOptions{Lists: []string{dnfskill.DefaultList}})
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := dnfskill.Load(context.Background(), index, dnfskill.Options{})
	if err != nil {
		t.Fatal(err)
	}
	return catalog
}

func newSkillSwitchFixture(t *testing.T) (dnfrepo.Group, *Owner) {
	t.Helper()
	repos := dnfrepomemory.NewMemoryGroup()
	if err := repos.Character.Save(context.Background(), dnfrepo.CharacterRecord{
		CharacterID: "1",
		AccountID:   "dnf:10001",
		Job:         "14",
		Level:       90,
	}); err != nil {
		t.Fatal(err)
	}
	owner, err := NewOwner(repos, OwnerOptions{Catalog: jobScopedCatalog(t)})
	if err != nil {
		t.Fatal(err)
	}
	return repos, owner
}

// 四条边界要的持久化 round-trip：登记 → 从仓储重新读 → 记录还在、顺序稳定、
// 字节逐字形等于**该档位的包体形**。两侧都钉：
//
//	v6（一键回退）= 逐字节等于改动前的 6 字节行；
//	v4（新默认）  = 4 字节真包体（去掉那 2 个非包体字节）。
func TestApplyPersistsPerCharacterAndReloadsVerbatim(t *testing.T) {
	for _, layout := range []Layout{LayoutV6, LayoutV4} {
		repos, owner := newSkillSwitchFixture(t)
		ctx := context.Background()

		bodies := append(append([]string(nil), currentClientBodies...), liveFourteenThirtyBodies...)
		for index, body := range bodies {
			record, err := ParseRecordIn(layout, mustDecode(t, body))
			if err != nil {
				t.Fatalf("layout %s body %s: %v", layout, body, err)
			}
			result, err := owner.Apply(ctx, "1", "dnf:10001", record)
			if err != nil {
				t.Fatalf("layout %s Apply(%s) error = %v", layout, body, err)
			}
			if result.SkillGate != SkillGateJobScopedPVF {
				t.Fatalf("layout %s Apply(%s) skill gate = %q, want %q",
					layout, body, result.SkillGate, SkillGateJobScopedPVF)
			}
			if result.SkillID != uint16(record.Slot) {
				t.Fatalf("layout %s Apply(%s) skill id = %d, want %d",
					layout, body, result.SkillID, record.Slot)
			}
			if result.SlotCount != index+1 {
				t.Fatalf("layout %s Apply(%s) slot count = %d, want %d",
					layout, body, result.SlotCount, index+1)
			}
			if result.Replayed {
				t.Fatalf("layout %s Apply(%s) reported a replay on first write", layout, body)
			}
		}

		// 重新读（模拟重登 / 重新选角后从仓储重建）——这是四条边界共用的那一步。
		state, found, err := Load(ctx, repos, "1", layout)
		if err != nil || !found {
			t.Fatalf("layout %s Load found=%t err=%v", layout, found, err)
		}
		if len(state.Records) != len(bodies) {
			t.Fatalf("layout %s reloaded %d records, want %d", layout, len(state.Records), len(bodies))
		}
		// 按 Slot 升序：3,12,14,15,16,18,19,21,22,26
		wantSlots := []int{3, 12, 14, 15, 16, 18, 19, 21, 22, 26}
		for index, record := range state.Records {
			if int(record.Slot) != wantSlots[index] {
				t.Fatalf("layout %s record %d slot = %d, want %d (slots must be ascending)",
					layout, index, record.Slot, wantSlots[index])
			}
		}
		frames := state.Frames()
		if len(frames) != len(bodies) {
			t.Fatalf("layout %s frames = %d, want %d", layout, len(frames), len(bodies))
		}
		gotHexes := make([]string, 0, len(frames))
		for _, frame := range frames {
			gotHexes = append(gotHexes, hex.EncodeToString(frame))
		}
		wantHexes := []string{
			"000003030300", "00000c030c00", "00000e030e00", "00000f030f00", "000010031000",
			"000012031200", "000013031300", "000015031500", "000016031600", "00001a031a00",
		}
		if layout == LayoutV4 {
			// v4 的字节形 = 去掉每行前 2 个非包体字节。
			for index, want := range wantHexes {
				wantHexes[index] = want[4:]
			}
		}
		if strings.Join(gotHexes, ",") != strings.Join(wantHexes, ",") {
			t.Fatalf("layout %s projection frames = %v, want %v (verbatim in that layout, ascending by slot)",
				layout, gotHexes, wantHexes)
		}

		// 幂等：同一条 body 再来一次只覆盖同一个 slot 键。
		record, _ := ParseRecordIn(layout, mustDecode(t, currentClientBodies[0]))
		result, err := owner.Apply(ctx, "1", "dnf:10001", record)
		if err != nil {
			t.Fatalf("layout %s second Apply error = %v", layout, err)
		}
		if !result.Replayed || result.SlotCount != len(bodies) {
			t.Fatalf("layout %s idempotent apply = %+v, want Replayed with slot_count=%d",
				layout, result, len(bodies))
		}
		if result.LayoutUpgraded {
			t.Fatalf("layout %s reported a layout upgrade on an already-canonical row", layout)
		}

		// 落库位置：既有 settings 家族，scope `character:<id>:skill_switch_inventory`。
		stored, found, err := repos.Settings.Load(ctx, Scope("1"))
		if err != nil || !found {
			t.Fatalf("layout %s settings load found=%t err=%v", layout, found, err)
		}
		if stored.Scope != "character:1:skill_switch_inventory" {
			t.Fatalf("scope = %q", stored.Scope)
		}
		wantSlot12 := "00000c030c00"
		if layout == LayoutV4 {
			wantSlot12 = "0c030c00"
		}
		if stored.Values["slot_12"] != wantSlot12 {
			t.Fatalf("layout %s slot_12 = %q, want %q", layout, stored.Values["slot_12"], wantSlot12)
		}
		if stored.Values[entrySource] != SourceCurrentEXE {
			t.Fatalf("source = %q, want %q", stored.Values[entrySource], SourceCurrentEXE)
		}
		// 别的角色不共享这份状态。
		if _, other, err := Load(ctx, repos, "2", layout); err != nil || other {
			t.Fatalf("layout %s character 2 load found=%t err=%v, want no record", layout, other, err)
		}
	}
}

// v4（新默认）的**迁移与双读**故事，逐条钉死：
//
//  1. 线上已有的**旧 6 字节行**在 v4 档下直接读得出（去掉 2 个非包体字节），**不需要迁移**；
//  2. 同一行在 v6 档下逐字节等于改动前；
//  3. v4 档**再次收到同一条记录**时才改写成 4 字节规范形，并且只改字节形
//     （`layout_upgraded=true`，字段不变）；
//  4. 反向：v4 写过的规范行在 v6 档下也能读回（补回 `0x0000` 前缀）⇒ 回退不丢状态。
func TestApplyV4PersistsCanonicalBodyAndUpgradesLegacyRows(t *testing.T) {
	repos, owner := newSkillSwitchFixture(t)
	ctx := context.Background()
	// 手工写入改动前的行（= 线上库内 `character:6:skill_switch_inventory` 的真实形状）。
	if err := repos.Settings.Save(ctx, dnfrepo.SettingsRecord{
		Scope: Scope("1"),
		Values: map[string]string{
			"slot_12":             "00000c030c00",
			"record_00000c030c00": "scope=0 slot_a=12 sub_mode=0x3 slot_b=12",
			"slot_count":          "1",
			"record_count":        "1",
			"source":              SourceCurrentEXE,
		},
	}); err != nil {
		t.Fatal(err)
	}

	// 1) v4 档读旧行：固定映射，字段完全不变；旧 12-hex 取证键仍计入 forensic_count。
	state, found, err := Load(ctx, repos, "1", LayoutV4)
	if err != nil || !found || len(state.Records) != 1 {
		t.Fatalf("v4 Load(found=%t err=%v records=%d), want one record", found, err, len(state.Records))
	}
	if got := hex.EncodeToString(state.Frames()[0]); got != "0c030c00" {
		t.Fatalf("v4 frame = %s, want 0c030c00 (strip the 2 non-body bytes)", got)
	}
	if state.ForensicCount != 1 {
		t.Fatalf("v4 forensic count = %d, want 1 (legacy 12-hex forensic keys must still count)",
			state.ForensicCount)
	}
	if state.Records[0].Slot != 12 || state.Records[0].Mode != ModeConfirm || state.Records[0].Value != 12 {
		t.Fatalf("v4 record = %+v, want slot 12 / mode 0x3 / value 12", state.Records[0])
	}

	// 2) v6 档读同一行：逐字节等于改动前。
	legacy, _, err := Load(ctx, repos, "1", LayoutV6)
	if err != nil {
		t.Fatal(err)
	}
	if got := hex.EncodeToString(legacy.Frames()[0]); got != "00000c030c00" {
		t.Fatalf("v6 frame = %s, want byte-for-byte the stored row 00000c030c00", got)
	}

	// 3) v4 档再次收到同一条记录：改写成规范形，且**只改字节形**。
	record, err := ParseRecord(mustDecode(t, "0c030c00"))
	if err != nil {
		t.Fatal(err)
	}
	result, err := owner.Apply(ctx, "1", "dnf:10001", record)
	if err != nil {
		t.Fatalf("Apply error = %v", err)
	}
	if !result.LayoutUpgraded {
		t.Fatalf("Apply = %+v, want layout_upgraded on the first v4 rewrite of a legacy row", result)
	}
	if result.Replayed {
		t.Fatalf("Apply = %+v, want replayed=false (the row was rewritten)", result)
	}
	stored, _, err := repos.Settings.Load(ctx, Scope("1"))
	if err != nil {
		t.Fatal(err)
	}
	if stored.Values["slot_12"] != "0c030c00" {
		t.Fatalf("slot_12 = %q, want the canonical 4-byte body 0c030c00", stored.Values["slot_12"])
	}
	// 旧取证键**不删**，新取证键另加一条（可追溯）。
	if _, present := stored.Values["record_00000c030c00"]; !present {
		t.Fatalf("legacy forensic key must be kept; values=%v", stored.Values)
	}
	if _, present := stored.Values["record_0c030c00"]; !present {
		t.Fatalf("canonical forensic key must be added; values=%v", stored.Values)
	}

	// 再收一次：幂等，不再迁移。
	result, err = owner.Apply(ctx, "1", "dnf:10001", record)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Replayed || result.LayoutUpgraded {
		t.Fatalf("second Apply = %+v, want Replayed with no further upgrade", result)
	}

	// 4) 反向 dual-read：v4 写过的规范行在 v6 档下也能读回（`0x0000` 前缀），回退不丢状态。
	rolledBack, _, err := Load(ctx, repos, "1", LayoutV6)
	if err != nil {
		t.Fatal(err)
	}
	if got := hex.EncodeToString(rolledBack.Frames()[0]); got != "00000c030c00" {
		t.Fatalf("v6 read of a canonical row = %s, want 00000c030c00 (prefix widened, fields kept)", got)
	}
	if !rolledBack.Records[0].SameFields(record) {
		t.Fatalf("rollback view = %+v, want the same fields as %+v", rolledBack.Records[0], record)
	}
}

// 数据规则：引用了 job-scoped PVF 里不存在的技能号 ⇒ 具名拒绝，整条不落库。
func TestApplyRejectsSkillAbsentFromJobScopedPVF(t *testing.T) {
	repos, owner := newSkillSwitchFixture(t)
	ctx := context.Background()
	// job 14 的目录里只有 12/19/21/22；4 与 10 故意不存在。
	absent, err := ParseRecord([]byte{0x00, 0x00, 0x04, 0x03, 0x04, 0x00})
	if err != nil {
		t.Fatal(err)
	}
	_, err = owner.Apply(ctx, "1", "dnf:10001", absent)
	if !errors.Is(err, ErrSkillUnknown) {
		t.Fatalf("Apply(absent skill 4) error = %v, want ErrSkillUnknown", err)
	}
	if !strings.Contains(err.Error(), "job=14") || !strings.Contains(err.Error(), "skill=4") {
		t.Fatalf("rejection must name the job and the skill: %v", err)
	}
	if _, found, err := repos.Settings.Load(ctx, Scope("1")); err != nil || found {
		t.Fatalf("rejected record must not write a settings row (found=%t err=%v)", found, err)
	}

	// 同一批里合法的记录仍然可以落库 —— 拒绝是按条的，不是按角色的。
	ok, _ := ParseRecord(mustDecode(t, currentClientBodies[0]))
	if _, err := owner.Apply(ctx, "1", "dnf:10001", ok); err != nil {
		t.Fatalf("Apply(skill 12) error = %v", err)
	}
	stored, found, err := repos.Settings.Load(ctx, Scope("1"))
	if err != nil || !found {
		t.Fatalf("settings found=%t err=%v", found, err)
	}
	// 默认档 v4：落库的是 4 字节真包体（旧 6 字节行的前 2 字节不是包体）。
	if stored.Values["slot_12"] != "0c030c00" {
		t.Fatalf("slot_12 = %q, want the canonical 4-byte body 0c030c00", stored.Values["slot_12"])
	}
	if _, present := stored.Values["record_0c030c00"]; !present {
		t.Fatalf("forensic key must use the v4 body; values=%v", stored.Values)
	}
}

// 目录没加载不是「未知技能」：必须放行落库，并把原因写进 SkillGate（不许静默）。
func TestApplyWithoutCatalogFailsOpen(t *testing.T) {
	repos := dnfrepomemory.NewMemoryGroup()
	ctx := context.Background()
	if err := repos.Character.Save(ctx, dnfrepo.CharacterRecord{CharacterID: "1", AccountID: "dnf:10001", Job: "14"}); err != nil {
		t.Fatal(err)
	}
	owner, err := NewOwner(repos)
	if err != nil {
		t.Fatal(err)
	}
	record, _ := ParseRecord(mustDecode(t, currentClientBodies[0]))
	result, err := owner.Apply(ctx, "1", "dnf:10001", record)
	if err != nil {
		t.Fatalf("Apply without catalog error = %v", err)
	}
	if result.SkillGate != SkillGateCatalogAbsent {
		t.Fatalf("skill gate = %q, want %q", result.SkillGate, SkillGateCatalogAbsent)
	}
	if result.SlotCount != 1 {
		t.Fatalf("slot count = %d, want 1", result.SlotCount)
	}

	// job 列读不出 u8 时同样放行，但 gate 要能区分出来。
	if err := repos.Character.Save(ctx, dnfrepo.CharacterRecord{CharacterID: "2", AccountID: "dnf:10001", Job: "not-a-number"}); err != nil {
		t.Fatal(err)
	}
	ownerWithCatalog, err := NewOwner(repos, OwnerOptions{Catalog: jobScopedCatalog(t)})
	if err != nil {
		t.Fatal(err)
	}
	result, err = ownerWithCatalog.Apply(ctx, "2", "dnf:10001", record)
	if err != nil {
		t.Fatalf("Apply with unreadable job error = %v", err)
	}
	if result.SkillGate != SkillGateJobUnresolved {
		t.Fatalf("skill gate = %q, want %q", result.SkillGate, SkillGateJobUnresolved)
	}
}

func TestApplyRejectsWrongOwnerAndMissingCharacter(t *testing.T) {
	_, owner := newSkillSwitchFixture(t)
	ctx := context.Background()
	record, _ := ParseRecord(mustDecode(t, currentClientBodies[0]))
	if _, err := owner.Apply(ctx, "1", "dnf:99999", record); !errors.Is(err, ErrCharacterOwner) {
		t.Fatalf("wrong account error = %v, want ErrCharacterOwner", err)
	}
	if _, err := owner.Apply(ctx, "404", "dnf:10001", record); !errors.Is(err, ErrCharacterNotFound) {
		t.Fatalf("missing character error = %v, want ErrCharacterNotFound", err)
	}
	if _, err := owner.Apply(ctx, "", "dnf:10001", record); !errors.Is(err, ErrCharacterRequired) {
		t.Fatalf("empty character error = %v, want ErrCharacterRequired", err)
	}
}

// ModeClear 哨兵（8/8 出现在真实记录**后面**）不得抹掉已登记的状态键。两侧都钉。
func TestApplySentinelKeepsStateKeys(t *testing.T) {
	for _, layout := range []Layout{LayoutV6, LayoutV4} {
		repos, owner := newSkillSwitchFixture(t)
		ctx := context.Background()
		for _, body := range currentClientBodies {
			record, _ := ParseRecordIn(layout, mustDecode(t, body))
			if _, err := owner.Apply(ctx, "1", "dnf:10001", record); err != nil {
				t.Fatalf("layout %s Apply(%s) error = %v", layout, body, err)
			}
		}
		sentinel, _ := ParseRecordIn(layout, mustDecode(t, liveSentinelBody))
		result, err := owner.Apply(ctx, "1", "dnf:10001", sentinel)
		if err != nil {
			t.Fatalf("layout %s Apply(sentinel) error = %v", layout, err)
		}
		if !result.Sentinel {
			t.Fatalf("layout %s sentinel record must be reported as Sentinel", layout)
		}
		if result.SlotCount != len(currentClientBodies) {
			t.Fatalf("layout %s sentinel changed slot count to %d, want %d",
				layout, result.SlotCount, len(currentClientBodies))
		}
		stored, _, err := repos.Settings.Load(ctx, Scope("1"))
		if err != nil {
			t.Fatal(err)
		}
		for _, body := range currentClientBodies {
			record, _ := ParseRecordIn(layout, mustDecode(t, body))
			want := body
			if layout == LayoutV4 {
				want = body[4:]
			}
			if got := stored.Values[slotEntryKey(record.Slot)]; got != want {
				t.Fatalf("layout %s slot_%d = %q after sentinel, want %s",
					layout, record.Slot, got, want)
			}
		}
		wantSentinel := liveSentinelBody
		if layout == LayoutV4 {
			wantSentinel = liveSentinelBody[4:]
		}
		if _, present := stored.Values[entrySentinelPrefix+wantSentinel]; !present {
			t.Fatalf("layout %s sentinel must still leave a forensic key; values=%v", layout, stored.Values)
		}
	}
}

func TestLoadWithoutSettingsRepositoryIsNotFound(t *testing.T) {
	state, found, err := Load(context.Background(), dnfrepo.Group{}, "1", LayoutV4)
	if err != nil || found || len(state.Records) != 0 {
		t.Fatalf("Load without settings repo found=%t err=%v state=%+v", found, err, state)
	}
}
