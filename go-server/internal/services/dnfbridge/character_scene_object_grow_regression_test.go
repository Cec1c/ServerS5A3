package dnfbridge

import (
	"bytes"
	"testing"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

// actorGrowByteStart 返回 op2 mode0 actor entry 里 grow 字节的下标
// （entry 尾部 = job、grow、level，名字 DSTR 之后）。
func actorGrowByteStart(name string) int {
	return currentSceneObjectTailStartForTest(name) + 1
}

// TestCurrentSceneObjectEntryCarriesFullPackedGrowType 锁定 2026-09-25 香港实测
// 暴露的显示不一致：
//
//	选角列表（bootstrap roster，msg_id=2）下发持久化 grow_type 0x21；
//	选角后 op2 mode0 actor entry（msg_id=2）只发了低四位 0x01。
//
// 于是同一个角色在选角界面按 [awakening name] 显示二觉名（炽天使）、进场后按
// [growtype name] 显示一转分支名（圣骑士）。两条链路必须取自同一个权威字段
// （dnf_characters.grow_type），并且都不许丢掉高四位（觉醒次数）。
//
// 真档 PVF character/Priest/ATPriest.chr（job 14，character/character.lst 第 14 项）：
//
//	[growtype name] = 圣职者 / 圣骑士 / 异端审判者 / 巫女 / 诱魔者
//	[growtype 2]（= 分支 1 = 圣骑士）的 [awakening name] = 福音传道者 / 炽天使
//
// ⇒ grow_type 0x01→圣骑士、0x11→福音传道者、0x21→炽天使。
func TestCurrentSceneObjectEntryCarriesFullPackedGrowType(t *testing.T) {
	cases := []struct {
		label    string
		name     string
		job      string
		growType int64
		level    int
	}{
		{label: "priest-base", name: "ra0", job: "14", growType: 0x00, level: 10},
		{label: "priest-class-change", name: "ra1", job: "14", growType: 0x01, level: 30},
		{label: "priest-first-awakening", name: "ra2", job: "14", growType: 0x11, level: 60},
		{label: "priest-second-awakening", name: "ra3", job: "14", growType: 0x21, level: 90},
		{label: "other-job-branch2-second-awakening", name: "ra4", job: "11", growType: 0x22, level: 90},
	}
	for _, tc := range cases {
		character := dnfrepo.CharacterRecord{
			CharacterID: "3",
			Name:        tc.name,
			Job:         tc.job,
			Level:       tc.level,
			Stats:       map[string]int64{"grow_type": tc.growType},
		}

		actorBody := buildCurrentSceneObjectListBody(currentSceneBootstrapObjectKey, character, true, "")
		tailStart := currentSceneObjectTailStartForTest(tc.name)
		if len(actorBody) < tailStart+3 {
			t.Fatalf("%s: actor entry is %d bytes, cannot hold the job/grow/level triple at %d", tc.label, len(actorBody), tailStart)
		}
		if got := actorBody[tailStart]; got != byte(numericCharacterStat(tc.job)) {
			t.Fatalf("%s: actor entry job = %d, want %s", tc.label, got, tc.job)
		}
		if got := actorBody[tailStart+1]; got != byte(tc.growType) {
			t.Fatalf("%s: actor entry grow = %#x, want the persisted grow_type %#x (the awakening stage must survive; %#x is the pre-2026-09-25 truncated value)",
				tc.label, got, byte(tc.growType), byte(tc.growType)&0x0f)
		}
		if got := actorBody[tailStart+2]; got != byte(tc.level) {
			t.Fatalf("%s: actor entry level = %d, want %d", tc.label, got, tc.level)
		}

		// 选角列表必须与 actor entry 同值：这正是"两个界面取自同一权威字段"。
		rosterBody := buildNoPackRosterBody([]dnfrepo.CharacterRecord{character})
		rosterGrowStart := noPackRosterGrowStart(15, character)
		if got := rosterBody[rosterGrowStart]; got != byte(tc.growType) {
			t.Fatalf("%s: roster grow = %#x, want %#x", tc.label, got, byte(tc.growType))
		}
		if actorBody[tailStart+1] != rosterBody[rosterGrowStart] {
			t.Fatalf("%s: actor grow %#x != roster grow %#x", tc.label, actorBody[tailStart+1], rosterBody[rosterGrowStart])
		}
	}
}

// TestCurrentSceneObjectEntryMatchesLiveTest2Bytes 逐字节复现香港 2026-09-25 03:52
// conn game-000017 的取证样本：账号 dnf:10001 / 角色 3 `test2`、job=14、
// grow_type=0x21、level=90。
//
// 现场两个包（同一次选角会话）：
//
//	选角列表 msg_id=2 该行 = 0e 21 5a  → 客户端显示"炽天使"（二觉名）
//	actor entry msg_id=2 = 0e 01 5a  → 客户端显示"圣骑士"（一转名，修复前的 bug）
//
// 修复后 actor entry 必须是 0e 21 5a，与选角列表逐字节一致。
func TestCurrentSceneObjectEntryMatchesLiveTest2Bytes(t *testing.T) {
	character := dnfrepo.CharacterRecord{
		CharacterID: "3",
		Name:        "test2",
		Job:         "14",
		Level:       90,
		Stats:       map[string]int64{"grow_type": 0x21},
	}
	want := []byte{0x0e, 0x21, 0x5a}

	actorBody := buildCurrentSceneObjectListBody(currentSceneBootstrapObjectKey, character, true, "")
	tailStart := currentSceneObjectTailStartForTest("test2")
	if got := actorBody[tailStart : tailStart+3]; !bytes.Equal(got, want) {
		t.Fatalf("actor entry job/grow/level = % x, want % x", got, want)
	}

	rosterBody := buildNoPackRosterBody([]dnfrepo.CharacterRecord{character})
	rosterGrowStart := noPackRosterGrowStart(15, character)
	if got := rosterBody[rosterGrowStart-1 : rosterGrowStart+2]; !bytes.Equal(got, want) {
		t.Fatalf("roster entry job/grow/level = % x, want % x", got, want)
	}
	if got := actorBody[actorGrowByteStart("test2")]; got != rosterBody[rosterGrowStart] {
		t.Fatalf("actor grow %#x != roster grow %#x", got, rosterBody[rosterGrowStart])
	}
}
