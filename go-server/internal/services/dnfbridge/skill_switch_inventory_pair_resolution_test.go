package dnfbridge

import (
	"context"
	"encoding/hex"
	"strings"
	"testing"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	dnfrepomemory "longheng.io/server/internal/modules/dnf/repository/memory"
)

// 「pair 落点探针」（`DNFBRIDGE_SKILL_SWITCH_PAIR_RESOLUTION`）的钉子。
//
// 第五轮的分歧只剩一处：重发帧里的 value 该在**哪个容器**里查。
//   - 主背包（list 0）：char 6 的 11 个 value `54..64` 与 `0:54..0:64` 那 11 件装备
//     的类型 11/11 吻合（第四轮 SQL）；
//   - 换装栏/装扮容器（list 1）：char 1 在 18:38:59Z 亲手绑的 `slot 12 -> 20`
//     只在 `1:20` 查得到，`0:20` 根本不存在（第五轮 SQL）。
//
// 报文分不出来，所以本档把两个容器**都查一遍**写进日志；判据是日志行，不是猜。
// 铁律：**本档不改任何字节**（帧内容与开关无关）。

// skillSwitchResolutionMainRows / ...AvatarRows 是现场 SQL 的两份容器内容
// （`dnf_inventory_items` + `dnf_inventory_item_extra`，2026-09-28）。
var skillSwitchResolutionMainRows = map[string]struct {
	ItemID int64
	Type   string
}{
	"0:54": {28309, "[weapon]"},
	"0:55": {100350891, "[magic stone]"},
	"0:56": {100341845, "[support]"},
	"0:57": {180229, "[pants]"},
	"0:58": {401127, "[amulet]"},
	"0:59": {360229, "[shoes]"},
	"0:60": {421127, "[ring]"},
	"0:61": {240229, "[shoulder]"},
	"0:62": {120229, "[coat]"},
	"0:63": {300229, "[waist]"},
	"0:64": {411127, "[wrist]"},
}

var skillSwitchResolutionAvatarRows = map[string]struct {
	ItemID int64
	Type   string
}{
	"1:54": {101520658, "[breast avatar]"},
	"1:55": {101520658, "[breast avatar]"},
	"1:56": {101520658, "[breast avatar]"},
	"1:57": {101520658, "[breast avatar]"},
	"1:58": {101520671, "[breast avatar]"},
	"1:59": {101530518, "[waist avatar]"},
	"1:60": {101530603, "[waist avatar]"},
	"1:61": {101530603, "[waist avatar]"},
	"1:62": {101530607, "[waist avatar]"},
	"1:63": {101530612, "[waist avatar]"},
	"1:64": {101530612, "[waist avatar]"},
}

// seedSkillSwitchResolutionInventory 把现场那两份容器写进内存背包仓储。
func seedSkillSwitchResolutionInventory(t *testing.T, repos dnfrepo.Group, characterID string) {
	t.Helper()
	slots := make(map[string]dnfrepo.ItemStack, len(skillSwitchResolutionMainRows)+len(skillSwitchResolutionAvatarRows))
	for key, row := range skillSwitchResolutionMainRows {
		slots[key] = dnfrepo.ItemStack{ItemID: row.ItemID, Count: 1, Extra: map[string]string{
			"item_kind":      "equipment",
			"equipment_type": row.Type,
		}}
	}
	for key, row := range skillSwitchResolutionAvatarRows {
		slots[key] = dnfrepo.ItemStack{ItemID: row.ItemID, Count: 1, Extra: map[string]string{
			"item_kind":      "avatar",
			"equipment_type": row.Type,
		}}
	}
	if err := repos.Inventory.Save(context.Background(), dnfrepo.InventoryRecord{
		CharacterID: characterID,
		Slots:       slots,
	}); err != nil {
		t.Fatalf("seed inventory: %v", err)
	}
}

// TestSkillSwitchPairResolutionLogsBothContainers 是主用例：帧**逐字节不变**，
// 只多一条把两份容器都查过的 EVENT。
func TestSkillSwitchPairResolutionLogsBothContainers(t *testing.T) {
	repos := dnfrepomemory.NewMemoryGroup()
	fixture := newSkillSwitchBindFixtureWithRepos(t, repos)
	seedSkillSwitchRestoreScope(t, repos)
	seedSkillSwitchResolutionInventory(t, repos, "6")

	if err := fixture.service.sendSkillSwitchInventoryProjection(fixture.session, skillSwitchBurstBoundary); err != nil {
		t.Fatalf("projection error = %v", err)
	}
	packets := drainUpperPackets(t, fixture.connection)
	if len(packets) != 1 {
		t.Fatalf("boundary wrote %d packets, want exactly the one loadout frame", len(packets))
	}
	// 帧字节与第四轮那份手写常量逐字节相同 —— 本档对线上字节**零影响**。
	if got := hex.EncodeToString(packets[0].body); got != skillSwitchRestoreFrameHex {
		t.Fatalf("body = %s\nwant  %s", got, skillSwitchRestoreFrameHex)
	}
	log := readPeerEndpointTestLog(t, fixture.logger, fixture.logPath)
	for _, want := range []string{
		"EVENT kind=" + skillSwitchPairResolutionEvent,
		"char_id=6",
		"scope=character:6:skill_switch_inventory",
		"inventory=ok",
		"pair_rows=13",
		"probe_lists=0=main_inventory,1=switch_or_avatar_container",
		"resolved_counts=list_0=11 list_1=11",
		"unresolved_pairs=13->13,25->25",
		"byte_effect=none_log_only",
		"switch=" + skillSwitchPairResolutionResolve,
		"switch_env=" + skillSwitchPairResolutionEnv,
		// 两份容器的实物：主背包 54 是武器、换装/装扮容器 54 是胸部装扮。
		"list_0_items=0:54=28309/equipment[weapon]",
		"list_1_items=1:54=101520658/avatar[breast avatar]",
	} {
		if !strings.Contains(log, want) {
			t.Fatalf("resolution log missing %q; log=\n%s", want, log)
		}
	}
}

// TestSkillSwitchPairResolutionOffKeepsTodaysLog 钉回退字面量：一个字段都不多写，
// 帧仍然逐字节相同。
func TestSkillSwitchPairResolutionOffKeepsTodaysLog(t *testing.T) {
	t.Setenv(skillSwitchPairResolutionEnv, skillSwitchPairResolutionOff)
	repos := dnfrepomemory.NewMemoryGroup()
	fixture := newSkillSwitchBindFixtureWithRepos(t, repos)
	seedSkillSwitchRestoreScope(t, repos)
	seedSkillSwitchResolutionInventory(t, repos, "6")

	if err := fixture.service.sendSkillSwitchInventoryProjection(fixture.session, skillSwitchBurstBoundary); err != nil {
		t.Fatalf("projection error = %v", err)
	}
	packets := drainUpperPackets(t, fixture.connection)
	if len(packets) != 1 {
		t.Fatalf("boundary wrote %d packets, want 1", len(packets))
	}
	if got := hex.EncodeToString(packets[0].body); got != skillSwitchRestoreFrameHex {
		t.Fatalf("body = %s\nwant  %s", got, skillSwitchRestoreFrameHex)
	}
	log := readPeerEndpointTestLog(t, fixture.logger, fixture.logPath)
	if strings.Contains(log, skillSwitchPairResolutionEvent) {
		t.Fatalf("the rollback literal must not write the probe event; log=\n%s", log)
	}
}

// TestSkillSwitchPairResolutionWithoutInventoryStillSends 钉失败契约：
// 角色没有背包记录时只记 `inventory=missing`，**边界照常发包**、绝不打断。
func TestSkillSwitchPairResolutionWithoutInventoryStillSends(t *testing.T) {
	repos := dnfrepomemory.NewMemoryGroup()
	fixture := newSkillSwitchBindFixtureWithRepos(t, repos)
	seedSkillSwitchRestoreScope(t, repos)

	if err := fixture.service.sendSkillSwitchInventoryProjection(fixture.session, skillSwitchBurstBoundary); err != nil {
		t.Fatalf("projection error = %v", err)
	}
	packets := drainUpperPackets(t, fixture.connection)
	if len(packets) != 1 {
		t.Fatalf("boundary wrote %d packets, want 1", len(packets))
	}
	if got := hex.EncodeToString(packets[0].body); got != skillSwitchRestoreFrameHex {
		t.Fatalf("body = %s\nwant  %s", got, skillSwitchRestoreFrameHex)
	}
	log := readPeerEndpointTestLog(t, fixture.logger, fixture.logPath)
	for _, want := range []string{
		"EVENT kind=" + skillSwitchPairResolutionEvent,
		"inventory=missing",
		"resolved_counts=list_0=0 list_1=0",
		"unresolved_pairs=12->54,13->13,14->62",
	} {
		if !strings.Contains(log, want) {
			t.Fatalf("resolution log missing %q; log=\n%s", want, log)
		}
	}
}
