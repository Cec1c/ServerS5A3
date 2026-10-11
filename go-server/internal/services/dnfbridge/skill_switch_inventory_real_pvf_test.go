package dnfbridge

import (
	"context"
	"fmt"
	"os"
	"testing"

	"longheng.io/server/internal/modules/dnf/skillswitch"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

// `skillswitch.Record.SkillID` 把 op1428 的包体 off0（`Slot`，旧名 `slot_a`）
// 读成 job-scoped PVF 技能号，本用例把这条**推断**钉在真档 `Script.pvf` 上
// （与 `skill_catalog_real_smoke_test.go` 同一条可选门禁：不设
// `DNFBRIDGE_REAL_PVF_SMOKE` 就跳过，普通测试套件不依赖 100 MB 归档）。
//
// 现场 `Slot` 集合（82 条 RECV 的全部取值）= {3,9,12,13,14,15,16,17,18,19,20,21,22,23,26,33}。
// 判据是"它完整落在真实技能号空间里，而 `Value` 不是"（`Value ∈ 54..64` 落在
// `dnf_skill_layouts.slot_index` 段、`0xFFFF` 是哨兵）。
//
// 证伪点：job 14 的技能目录里 **4 与 10 不存在**，所以
// `skillswitch.ErrSkillUnknown` 的拒绝路径有真实反例，而不是自造的假目录。
func TestRealScriptPVFOp1428SkillIDReading(t *testing.T) {
	pvfPath := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if pvfPath == "" {
		t.Skip("set DNFBRIDGE_REAL_PVF_SMOKE to run the real Script.pvf op1428 skill-id smoke")
	}
	archive, err := platformpvf.LoadArchive(platformpvf.Options{Path: pvfPath})
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := buildSkillCatalogFromSource(context.Background(), archive)
	if err != nil {
		t.Fatal(err)
	}

	// 现场 slot_a 的全部取值，逐条来自 packet_log 的 6 字节包体。
	liveSlotA := []uint16{3, 9, 12, 13, 14, 15, 16, 17, 18, 19, 20, 21, 22, 23, 26, 33}

	for _, job := range []byte{11, 14} {
		missing := make([]uint16, 0)
		for _, skillID := range liveSlotA {
			if _, present := catalog.Find(job, skillID); !present {
				missing = append(missing, skillID)
			}
		}
		if len(missing) != 0 {
			t.Fatalf("job %d: live op1428 slot_a values absent from the job-scoped PVF list: %v", job, missing)
		}
		t.Logf("job %d: every live slot_a value resolves in %s", job, "skill/skilllist.lst")
	}

	// 证伪点：同一张表里确实存在"不在目录里"的技能号，拒绝路径有真实反例。
	absent := []uint16{4, 10}
	for _, skillID := range absent {
		if _, present := catalog.Find(14, skillID); present {
			t.Fatalf("job 14 unexpectedly contains skill %d; pick another absent id for the rejection fixture", skillID)
		}
	}
	t.Logf("job 14: skills %v are absent and therefore usable as ErrSkillUnknown fixtures", absent)

	// 端到端：真档 + op1428 的**当前客户端 4 条**必须全部落库，一条都不被拒绝。
	repos := newSkillSwitchInventoryRepos(t)
	service, session, connection, _, _ := newSkillSwitchInventoryService(t, "", repos)
	service.skillCatalog = catalog
	for _, body := range skillSwitchInventoryCurrentClientBodies {
		if _, err := service.handleSkillSwitchInventoryCommand(
			session,
			currentSkillSwitchInventoryOpcode,
			skillSwitchInventoryBody(t, body),
		); err != nil {
			t.Fatalf("body %s: %v", body, err)
		}
	}
	if got := connection.write.Bytes(); len(got) != 0 {
		t.Fatalf("op1428 must not reply inline, wrote %x", got)
	}
	// 默认档 v4：落库/投影的字节形是 4 字节真包体（旧 6 字节行的前 2 字节不是包体）。
	state, found, err := skillswitch.Load(context.Background(), repos, "1", skillswitch.LayoutV4)
	if err != nil || !found {
		t.Fatalf("state found=%t err=%v", found, err)
	}
	if len(state.Records) != len(skillSwitchInventoryCurrentClientBodies) {
		t.Fatalf("persisted %d records against the real PVF, want %d (a rejection would lose the registration)",
			len(state.Records), len(skillSwitchInventoryCurrentClientBodies))
	}
	frames := state.Frames()
	for index, body := range skillSwitchInventoryCurrentClientBodies {
		if got := fmt.Sprintf("%x", frames[index]); got != body[4:] {
			t.Fatalf("projected frame %d = %s, want the v4 body %s", index, got, body[4:])
		}
	}
	// 回退档 v6 从同一份仓储读出的字节必须逐字节等于改动前的现场 6 字节样本。
	legacy, _, err := skillswitch.Load(context.Background(), repos, "1", skillswitch.LayoutV6)
	if err != nil {
		t.Fatal(err)
	}
	legacyFrames := legacy.Frames()
	for index, body := range skillSwitchInventoryCurrentClientBodies {
		if got := fmt.Sprintf("%x", legacyFrames[index]); got != body {
			t.Fatalf("v6 rollback frame %d = %s, want byte-for-byte %s", index, got, body)
		}
	}
}
