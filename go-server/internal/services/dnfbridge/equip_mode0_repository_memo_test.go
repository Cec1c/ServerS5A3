package dnfbridge

import (
	"bytes"
	"context"
	"testing"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
	"longheng.io/server/internal/modules/dnf/dnfenum"
	dnfproto "longheng.io/server/internal/modules/dnf/protocol"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	dnfrepomemory "longheng.io/server/internal/modules/dnf/repository/memory"
)

// 穿脱装备 / 换时装「外观重挂」那一次调用的只读仓储记忆
// （equip_mode0_repository_memo.go）的回归钉子。
//
// 现场口径（2026-09-28）：操作者报「穿上脱下的速度不太满意」。mode0 外观包是当前
// EXE 上**唯一**能重挂本地角色外观的下行（见 game_aligned.go:799-828），而它的
// 构造期（ACK 之后、mode0 发出之前）在改动前要把同一份记录反复读：
//
//	equipment 6 次 / character 2 次 / account 1 次  ← 回退档钉的就是这三个"今天"的数
//
// 六个装备读取点（现场逐帧抓）：
//
//	#1 game_select_resolve.go              characterForEnter → dnfcharacterdata.Loader(PartEquipment)
//	#2 character_initialization.go:149     characterInitializationPresenceForRecord
//	#3 initial_equipment_roster_projection attachEquipmentSummary
//	#4 current_actor_mode0_appearance_body loadCurrentSelectedActorMode0AppearanceSummary
//	#5 current_actor_mode0_appearance_body loadCurrentSelectedActorNameTagState
//	#6 current_equipped_creature.go        currentEquippedCreatureForCharacterWithError
//
// 四个用例分别钉：读取次数（默认 1/1/1、回退 6/2/1）、**下行字节逐字节不变**、
// Save 立刻作废记忆、未装记忆的路径逐字节回到原仓储组。
const (
	wearMoveTodayEquipmentLoads = 6
	wearMoveTodayCharacterLoads = 2
	wearMoveTodayAccountLoads   = 1
	wearMoveMemoEquipmentLoads  = 1
	wearMoveMemoCharacterLoads  = 1
	wearMoveMemoAccountLoads    = 1
)

type wearMoveMeasurement struct {
	equipmentLoads  int
	characterLoads  int
	accountLoads    int
	totalLoads      int
	appearanceCount int
	appearanceBody  []byte
}

// measureWearMoveAppearanceCost 跑一次真实的"穿脱装备 → ACK + 外观刷新"后置动作，
// 用既有的 repoCallCounter（item_refresh_cost_test.go）数仓储调用。
// mode == "" 表示默认档；mode == equipMode0RepositoryMemoOff 表示回退档。
func measureWearMoveAppearanceCost(t *testing.T, mode string) wearMoveMeasurement {
	t.Helper()
	fixture := newRefreshCostFixture(t)
	fixture.warmUp(t)
	if mode != "" {
		fixture.service.options.equipMode0RepositoryMemoMode = mode
	}

	result := alignedcmd.Result{
		Operation: "move_itemspace",
		UpperResponses: []alignedcmd.UpperResponse{{
			MsgID:          19,
			Body:           []byte{1, 0, 9, 0, 1, 0, 0, 0, 3, 12, 0},
			Classification: dnfproto.DefaultChannelClassification,
			AllowCodec:     true,
		}},
		PostActions: []alignedcmd.PostAction{
			alignedcmd.PostActionRefreshSelectedWearContainers,
			alignedcmd.PostActionRefreshSelectedActorAppearance,
		},
	}
	if err := fixture.service.sendAlignedUpperResponses(fixture.session, result); err != nil {
		t.Fatalf("wear post action error = %v", err)
	}

	measurement := wearMoveMeasurement{
		equipmentLoads: fixture.counter.total("Equipment.Load"),
		characterLoads: fixture.counter.total("Character.Load"),
		accountLoads:   fixture.counter.total("Account.Load"),
		totalLoads:     fixture.counter.total(""),
	}
	for stream := fixture.buffer.write.Bytes(); len(stream) > 0; {
		packet, rest := splitCurrentGameServerUpperPacketAuto(t, stream)
		if packet.Header.MsgID == uint16(dnfenum.CmdPacketSetUDPIPPort) && packet.Header.Classification == 0 {
			measurement.appearanceCount++
			measurement.appearanceBody = append([]byte(nil), packet.Body...)
		}
		stream = rest
	}
	t.Logf("mode=%q equipment.Load=%d character.Load=%d account.Load=%d total.Load=%d appearance_packets=%d body_len=%d",
		mode, measurement.equipmentLoads, measurement.characterLoads, measurement.accountLoads,
		measurement.totalLoads, measurement.appearanceCount, len(measurement.appearanceBody))
	return measurement
}

// 默认档把一次外观刷新里的重复读收敛成每个仓一次；回退档 `off` 必须**逐字节回到
// 今天**的读取次数（6/2/1）。这两个数都是现场实测值，不是估计。
func TestEquipMode0RepositoryMemoCollapsesWearMoveAppearanceReads(t *testing.T) {
	reuse := measureWearMoveAppearanceCost(t, "")
	if reuse.equipmentLoads != wearMoveMemoEquipmentLoads ||
		reuse.characterLoads != wearMoveMemoCharacterLoads ||
		reuse.accountLoads != wearMoveMemoAccountLoads {
		t.Fatalf("default tier repository loads = equipment %d / character %d / account %d, want %d/%d/%d",
			reuse.equipmentLoads, reuse.characterLoads, reuse.accountLoads,
			wearMoveMemoEquipmentLoads, wearMoveMemoCharacterLoads, wearMoveMemoAccountLoads)
	}

	rollback := measureWearMoveAppearanceCost(t, equipMode0RepositoryMemoOff)
	if rollback.equipmentLoads != wearMoveTodayEquipmentLoads ||
		rollback.characterLoads != wearMoveTodayCharacterLoads ||
		rollback.accountLoads != wearMoveTodayAccountLoads {
		t.Fatalf("rollback tier repository loads = equipment %d / character %d / account %d, want %d/%d/%d (today)",
			rollback.equipmentLoads, rollback.characterLoads, rollback.accountLoads,
			wearMoveTodayEquipmentLoads, wearMoveTodayCharacterLoads, wearMoveTodayAccountLoads)
	}
	if rollback.equipmentLoads <= reuse.equipmentLoads {
		t.Fatalf("memo must strictly reduce equipment reads: reuse=%d rollback=%d",
			reuse.equipmentLoads, rollback.equipmentLoads)
	}
}

// **下行字节必须逐字节不变**：两档构造出的 mode0 包体、包数、字节长度全等。
func TestEquipMode0RepositoryMemoKeepsWearMoveAppearancePacketIdentical(t *testing.T) {
	reuse := measureWearMoveAppearanceCost(t, "")
	rollback := measureWearMoveAppearanceCost(t, equipMode0RepositoryMemoOff)

	if reuse.appearanceCount != 1 || rollback.appearanceCount != 1 {
		t.Fatalf("appearance packets reuse=%d rollback=%d, want 1/1", reuse.appearanceCount, rollback.appearanceCount)
	}
	if !bytes.Equal(reuse.appearanceBody, rollback.appearanceBody) {
		t.Fatalf("mode0 appearance body differs with the memo installed:\n with    = %x\n without = %x",
			reuse.appearanceBody, rollback.appearanceBody)
	}
	if len(reuse.appearanceBody) == 0 {
		t.Fatal("mode0 appearance body is empty")
	}
}

// Save 立刻作废记忆：读-写-读 不许读到旧值。
func TestEquipMode0RepositoryMemoInvalidatesOnSave(t *testing.T) {
	ctx := context.Background()
	memo := &repositoryMemo{}
	repos := memo.wrap(newEquipMode0MemoTestGroup(t))

	if _, _, err := repos.Equipment.Load(ctx, "19"); err != nil {
		t.Fatalf("first equipment load: %v", err)
	}
	if _, _, err := repos.Equipment.Load(ctx, "19"); err != nil {
		t.Fatalf("second equipment load: %v", err)
	}
	if loads := memo.loadsTotal(); loads != 1 {
		t.Fatalf("memoized loads = %d, want 1 before any write", loads)
	}
	if err := repos.Equipment.Save(ctx, dnfrepo.EquipmentRecord{
		CharacterID: "19",
		Entries:     map[string]dnfrepo.EquipmentEntry{"7": {ItemID: 42}},
	}); err != nil {
		t.Fatalf("equipment save: %v", err)
	}
	record, found, err := repos.Equipment.Load(ctx, "19")
	if err != nil || !found {
		t.Fatalf("equipment reload found=%t err=%v", found, err)
	}
	if entry := record.Entries["7"]; entry.ItemID != 42 {
		t.Fatalf("post-save equipment load returned the stale entry %+v (memo was not invalidated)", entry)
	}
	if loads := memo.loadsTotal(); loads != 2 {
		t.Fatalf("loads after save+load = %d, want 2 (one pre-save, one post-invalidate)", loads)
	}
}

// 没装记忆的路径必须与改动前逐字节相同：repositoryGroupFor 直接回落原仓储组。
func TestEquipMode0RepositoryMemoIsAbsentWithoutContext(t *testing.T) {
	group := newEquipMode0MemoTestGroup(t)
	service := &Service{repositoryProvider: func() (dnfrepo.Group, bool) { return group, true }}
	plain, ok := service.repositoryGroup()
	if !ok {
		t.Fatal("repositories unavailable")
	}
	viaContext, ok := service.repositoryGroupFor(context.Background())
	if !ok {
		t.Fatal("repositoryGroupFor reported unavailable")
	}
	if _, memoized := viaContext.Equipment.(memoEquipmentRepository); memoized {
		t.Fatal("repositoryGroupFor must not memoize without an installed memo")
	}
	if viaContext.Equipment != plain.Equipment || viaContext.Character != plain.Character || viaContext.Account != plain.Account {
		t.Fatal("repositoryGroupFor must return the unchanged group when no memo is installed")
	}

	withMemo, _ := withEquipMode0RepositoryMemo(context.Background())
	memoized, _ := service.repositoryGroupFor(withMemo)
	if _, wrapped := memoized.Equipment.(memoEquipmentRepository); !wrapped {
		t.Fatal("repositoryGroupFor must memoize once a memo is installed")
	}
	if memoized.Settings != plain.Settings || memoized.Inventory != plain.Inventory {
		t.Fatal("the memo must pass every other repository through unchanged")
	}
}

func TestEquipMode0RepositoryMemoModeNormalization(t *testing.T) {
	for _, test := range []struct {
		value string
		want  string
	}{
		{value: "", want: equipMode0RepositoryMemoReuse},
		{value: "reuse", want: equipMode0RepositoryMemoReuse},
		{value: "on", want: equipMode0RepositoryMemoReuse},
		{value: "typo", want: equipMode0RepositoryMemoReuse},
		{value: "off", want: equipMode0RepositoryMemoOff},
		{value: "disable", want: equipMode0RepositoryMemoOff},
		{value: "0", want: equipMode0RepositoryMemoOff},
		{value: "legacy", want: equipMode0RepositoryMemoOff},
		{value: "none", want: equipMode0RepositoryMemoOff},
		{value: "  OFF  ", want: equipMode0RepositoryMemoOff},
	} {
		if got := normalizeEquipMode0RepositoryMemoMode(test.value); got != test.want {
			t.Fatalf("normalizeEquipMode0RepositoryMemoMode(%q) = %q, want %q", test.value, got, test.want)
		}
	}
	if !(&Service{}).equipMode0RepositoryMemoEnabled() {
		t.Fatal("zero-value options must install the memo (default = fixed behaviour)")
	}
	if got := (&Service{}).equipMode0RepositoryMemoMode(); got != equipMode0RepositoryMemoReuse {
		t.Fatalf("zero-value mode = %q, want %q", got, equipMode0RepositoryMemoReuse)
	}
	if (&Service{options: options{equipMode0RepositoryMemoMode: equipMode0RepositoryMemoOff}}).
		equipMode0RepositoryMemoEnabled() {
		t.Fatal("off must not install the memo")
	}
}

func newEquipMode0MemoTestGroup(t *testing.T) dnfrepo.Group {
	t.Helper()
	ctx := context.Background()
	group := dnfrepomemory.NewMemoryGroup()
	if err := group.Equipment.Save(ctx, dnfrepo.EquipmentRecord{
		CharacterID: "19",
		Entries:     map[string]dnfrepo.EquipmentEntry{"7": {ItemID: 1}},
	}); err != nil {
		t.Fatal(err)
	}
	return group
}
