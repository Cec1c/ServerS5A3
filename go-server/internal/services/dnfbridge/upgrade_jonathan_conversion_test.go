package dnfbridge

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"reflect"
	"testing"

	"longheng.io/server/internal/modules/dnf/channelcatalog"
	"longheng.io/server/internal/modules/dnf/dnfenum"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	memory "longheng.io/server/internal/modules/dnf/repository/memory"
	"longheng.io/server/internal/modules/dnf/town"
	"longheng.io/server/internal/modules/dnf/worldmap"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

// 段106 前半（红尾乔纳森 NPC 强化装备不消耗精炼的时空石）的聚焦用例。
//
// 已核真档事实（固定 v106 PVF SHA256 ad7d05c70421278f7385729bcba3e81e37d99d0f3df85a87a9f0dc19c92504cb）：
// npc/476_Jonathan.npc 声明 `[upgrade gold convert to material] 10158964 100000`，
// 10158964 = stackable/10158001/10158964.stk「精炼的时空石」(`[material]`)。
// 修复前唯一安装点 candidateUpgradeCostResolver 在历史默认档返回 nil ⇒ owner 只按存量
// Extra 金币字段收费，精炼的时空石一件不扣。生产默认接线现在报价完整原生金币费用，
// 并只把已核的 476 reinforce 请求分派给独立的材料转换 helper。
const (
	rest14JonathanNPC     uint16 = 476
	rest14TimeStoneItemID int64  = 10158964
	rest14OrdinaryNPC     uint16 = 1
	rest14NPCStoneSlot    int16  = 135
	rest14TargetSlot      int16  = 54
	rest14MaterialSlot    int16  = 358
	rest14StartGold       int64  = 100000
	rest14StartStones     int64  = 5
	rest14LegacyGoldFee          = "1234"
	rest14Account                = "dnf:rest14-jonathan"
	rest14CharacterID            = "77"
	rest14TargetItemID    int64  = 28309
	// rest14InvalidMaterialACK 是 internal/modules/dnf/inventory owner.go 的
	// upgradeErrorInvalidMaterial（= 22）。domain 包常量在 dnfbridge 不可见，
	// 这里按真实 op50 回包 0x0016 核。
	rest14InvalidMaterialACK byte = 22
)

// 单元档（不需要真实 PVF）：空值与 candidate 启用生产默认完整报价，其余历史值保持
// 显式停用。没有运行时档案时，普通强化报价在读取规则处失败关闭；独立 476 helper 仍负
// 责自己的材料转换，普通 NPC、无 NPC 与 0xFFFF 对它保持未解析。
func TestUpgradeCostDefaultQuotesFailClosedWithoutArchive(t *testing.T) {
	// This case deliberately asks a Service without an archive to fail. Do not
	// let its process-global loader error contaminate later real-PVF cases, or
	// let a previous real-PVF cache make the missing-archive case succeed.
	currentUpgradeTableMu.Lock()
	previousUpgrade, previousUpgradeErr := currentUpgradeTableCache, currentUpgradeTableLoadErr
	previousAmplify, previousAmplifyErr := currentAmplifyTableCache, currentAmplifyTableLoadErr
	currentUpgradeTableCache, currentUpgradeTableLoadErr = nil, nil
	currentAmplifyTableCache, currentAmplifyTableLoadErr = nil, nil
	currentUpgradeTableMu.Unlock()
	t.Cleanup(func() {
		currentUpgradeTableMu.Lock()
		currentUpgradeTableCache, currentUpgradeTableLoadErr = previousUpgrade, previousUpgradeErr
		currentAmplifyTableCache, currentAmplifyTableLoadErr = previousAmplify, previousAmplifyErr
		currentUpgradeTableMu.Unlock()
	})
	t.Setenv("DNFBRIDGE_NATIVE_UPGRADE_COST", "")
	service := &Service{}
	resolver := service.candidateUpgradeCostResolver(nil)
	if resolver == nil {
		t.Fatal("empty value must install the full native quote resolver")
	}
	identity, _ := service.boundGameSessionCharacterSnapshot(nil)
	conversion := service.alignedUpgradeNPCConversionCostResolver(identity)
	for _, tc := range []struct {
		name    string
		mode    string
		present bool
		npc     uint16
	}{
		{"no npc context", "reinforce", false, 0},
		{"sentinel npc", "reinforce", true, 0xFFFF},
		{"ordinary npc", "reinforce", true, rest14OrdinaryNPC},
		{"npc outside the list", "reinforce", true, 65534},
	} {
		quote, err := conversion(tc.mode, rest14TargetItemID, 0, tc.present, tc.npc)
		if err != nil || quote.Resolved || quote.GoldCost != 0 || quote.ConversionItemID != 0 || quote.ConversionCount != 0 {
			t.Fatalf("%s: independent 476 helper quote=%+v err=%v, want unresolved", tc.name, quote, err)
		}
		if quote, err := resolver(tc.mode, rest14TargetItemID, 0, tc.present, tc.npc); err == nil {
			t.Fatalf("%s: ordinary quote without a runtime archive must fail closed, got %+v", tc.name, quote)
		}
	}
	// Amplification now has its own default gold quote; the independent
	// reinforcement conversion resolver must still leave it unresolved.
	if quote, err := conversion("amplify", rest14TargetItemID, 0, true, rest14JonathanNPC); err != nil || quote.Resolved {
		t.Fatalf("amplification entered reinforcement conversion: %+v err=%v", quote, err)
	}
	if quote, err := resolver("amplify", rest14TargetItemID, 0, true, rest14JonathanNPC); err == nil {
		t.Fatalf("ordinary amplification without an archive must fail closed: %+v", quote)
	}
	// 已核 476 但没有运行时档案的 Service 必须失败关闭。单元档只证明 resolver 返回 err
	// （这里没有结算可看）；"不回落金币"的结算证据由
	// TestRealPVFJonathanConversionArchiveMissingFailsClosed 的真实 handler 用例给。
	if quote, err := resolver("reinforce", rest14TargetItemID, 0, true, rest14JonathanNPC); err == nil {
		t.Fatalf("verified 476 without a runtime archive must fail closed, got %+v", quote)
	}
	for _, value := range []string{"off", "vendor", "1", "disable", "unknown-value"} {
		t.Setenv("DNFBRIDGE_NATIVE_UPGRADE_COST", value)
		if service.candidateUpgradeCostResolver(nil) != nil {
			t.Fatalf("env %q: non-empty non-candidate values must keep the pre-fix wiring", value)
		}
	}
	t.Setenv("DNFBRIDGE_NATIVE_UPGRADE_COST", "CANDIDATE")
	if service.candidateUpgradeCostResolver(nil) == nil {
		t.Fatal("candidate value must install the full native chain")
	}
}

// rest14Fixture 是本文件真实 handler / 真实 resolver 用例共用的城镇夹具。
type rest14Fixture struct {
	path        string
	archive     *platformpvf.Archive
	towns       *town.Table
	townID      byte
	areaID      byte
	otherTownID byte
	otherAreaID byte
}

// rest14LoadFixture 只挑两个**用真实 placement 图核过**的区域：一个含 NPC 476，
// 一个不含 476（用于场景不符的拒绝档）。
func rest14LoadFixture(t *testing.T) *rest14Fixture {
	t.Helper()
	path := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if path == "" {
		t.Skip("matching real PVF required")
	}
	archive, err := platformpvf.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	towns, err := town.Load(context.Background(), archive, town.Options{})
	if err != nil {
		t.Fatal(err)
	}
	fixture := &rest14Fixture{path: path, archive: archive, towns: towns}
	jonathan, other := false, false
	for _, v := range towns.Towns() {
		if v.ID < 0 || v.ID > 255 {
			continue
		}
		for _, a := range v.Areas {
			if a.ID < 0 || a.ID > 255 {
				continue
			}
			placement, loadErr := worldmap.LoadTownPlacementMap(archive, a.MapPath)
			if loadErr != nil {
				continue
			}
			hasNPC := false
			for _, npc := range placement.NPCs {
				if npc.NPCID == int64(rest14JonathanNPC) {
					hasNPC = true
				}
			}
			if hasNPC && !jonathan {
				fixture.townID, fixture.areaID, jonathan = byte(v.ID), byte(a.ID), true
				continue
			}
			if !hasNPC && !other {
				fixture.otherTownID, fixture.otherAreaID, other = byte(v.ID), byte(a.ID), true
			}
		}
		if jonathan && other {
			break
		}
	}
	if !jonathan || !other {
		t.Fatalf("need one verified area holding NPC %d and one without it (has=%t other=%t)", rest14JonathanNPC, jonathan, other)
	}
	return fixture
}

// rest14Service 按真实 Service 接缝装配会话：城镇位置快照、城镇目录与仓储都走真实实现。
func (f *rest14Fixture) rest14Service(repos dnfrepo.Group, townID, areaID byte, positionValid bool) (*Service, *gameSession, *bufferConn) {
	conn := &bufferConn{}
	session := &gameSession{
		conn: conn, connID: "rest14-jonathan", accountID: rest14Account,
		channel: channelcatalog.Channel{ID: 38}, selectedCharacterID: 77,
		townSceneReadyCharacterID: 77,
		townPositionSnapshot:      currentTownPositionSnapshot{CharacterID: 77, TownID: townID, AreaID: areaID, PositionValid: positionValid},
	}
	service := &Service{
		options:                 options{accountID: rest14Account, gameUpperBodyCodec: gameUpperBodyCodecPlain, currentClientVersion: "official-24.9", pvfPath: f.path},
		initialEquipmentArchive: f.archive,
		townCatalog:             f.towns,
		gameSessions:            map[uint16]*gameSession{77: session},
		repositoryProvider:      func() (dnfrepo.Group, bool) { return repos, true },
	}
	return service, session, conn
}

// rest14SeedAssets 铺一份隔离夹具：装备在 0:54、精炼的时空石在 0:135（NPC 材料位）、
// 普通强化材料 3037 在账号共享槽 358。
func rest14SeedAssets(t *testing.T, repos dnfrepo.Group, stones int64, legacyGoldFee string, level string) (dnfrepo.CharacterRecord, dnfrepo.InventoryRecord, dnfrepo.AccountInventoryRecord) {
	t.Helper()
	ctx := context.Background()
	character := dnfrepo.CharacterRecord{CharacterID: rest14CharacterID, AccountID: rest14Account, Level: 90, Stats: map[string]int64{"gold": rest14StartGold}}
	target := realForgingWeaponStack(rest14TargetItemID, 45)
	binary.LittleEndian.PutUint16(target.RawEntry[:2], uint16(rest14TargetSlot))
	target.Extra["rest14_identity"] = "target-28309"
	if legacyGoldFee != "" {
		target.Extra["reinforce_upgrade_gold_0"] = legacyGoldFee
	}
	if level != "" {
		// RawEntry[0x0A] 与 ext_data0 都为空 ⇒ upgradeLevelOf 取 Extra 里的这一档。
		target.Extra["upgrade_level"] = level
	}
	bag := dnfrepo.InventoryRecord{CharacterID: rest14CharacterID, Slots: map[string]dnfrepo.ItemStack{
		"0:54":  target,
		"0:135": {ItemID: rest14TimeStoneItemID, Count: stones},
	}}
	sharedKey := dnfrepo.AccountSharedInventorySlotKey(rest14MaterialSlot)
	shared := dnfrepo.AccountInventoryRecord{AccountID: rest14Account, Slots: map[string]dnfrepo.ItemStack{sharedKey: {ItemID: 3037, Count: 1000}}}
	if err := repos.Character.Save(ctx, character); err != nil {
		t.Fatal(err)
	}
	if err := repos.Inventory.Save(ctx, bag); err != nil {
		t.Fatal(err)
	}
	if err := repos.AccountInventory.Save(ctx, shared); err != nil {
		t.Fatal(err)
	}
	beforeChar, _, _ := repos.Character.Load(ctx, rest14CharacterID)
	beforeBag, _, _ := repos.Inventory.Load(ctx, rest14CharacterID)
	beforeShared, _, _ := repos.AccountInventory.Load(ctx, rest14Account)
	return beforeChar, beforeBag, beforeShared
}

// rest14UpgradeOp50 用观测到的 target/slot/NPC trailer 拼真实 op50 请求体，并跑完真实
// handleAlignedGameCommand，返回 ACK 与主背包投影行。
func rest14UpgradeOp50(t *testing.T, service *Service, session *gameSession, conn *bufferConn, npc uint16) ([]byte, map[uint16][]byte, bool) {
	t.Helper()
	body := make([]byte, 16)
	binary.LittleEndian.PutUint16(body[2:], uint16(rest14TargetSlot))
	binary.LittleEndian.PutUint32(body[4:], uint32(rest14TargetItemID))
	binary.LittleEndian.PutUint16(body[8:], uint16(rest14MaterialSlot))
	binary.LittleEndian.PutUint16(body[10:], 65535)
	name := []byte("rest14-jonathan")
	binary.LittleEndian.PutUint32(body[12:], uint32(len(name)))
	body = append(body, name...)
	body = binary.LittleEndian.AppendUint16(body, npc)
	body = binary.LittleEndian.AppendUint16(body, uint16(rest14NPCStoneSlot))
	handled, err := service.handleAlignedGameCommand(session, byte(dnfenum.GameCmdCommand), uint16(dnfenum.CmdPacketUpgradeItem), body)
	if err != nil || !handled {
		t.Fatalf("handled=%t err=%v", handled, err)
	}
	var ack []byte
	ackIndex := -1
	index := 0
	updates := map[uint16][]byte{}
	walletSeen := false
	raw := conn.write.Bytes()
	for len(raw) > 0 {
		packet, rest := splitGameServerUpperPacket(t, raw)
		raw = rest
		if packet.Header.MsgID == uint16(dnfenum.CmdPacketUpgradeItem) && packet.Header.Classification == 1 {
			if ack != nil {
				t.Fatal("duplicate upgrade ACK")
			}
			ack = append([]byte(nil), packet.Body...)
			ackIndex = index
		}
		if packet.Header.MsgID == 14 && packet.Header.Classification == 0 {
			if ackIndex < 0 {
				t.Fatal("item projection preceded ACK")
			}
			b := packet.Body
			if len(b) < 3 || b[0] != 0 {
				t.Fatalf("bad main update %x", b)
			}
			n := int(binary.LittleEndian.Uint16(b[1:3]))
			if len(b) != 3+n*119 {
				t.Fatalf("bad update width %d rows=%d", len(b), n)
			}
			for i := 0; i < n; i++ {
				row := append([]byte(nil), b[3+i*119:3+(i+1)*119]...)
				slot := binary.LittleEndian.Uint16(row)
				updates[slot] = row
				if slot == 0 && binary.LittleEndian.Uint32(row[2:6]) == 0 {
					walletSeen = true
				}
			}
		}
		index++
	}
	return ack, updates, walletSeen
}

// 真档默认入口为普通强化、无 NPC 和 0xFFFF 报价完整原生金币，并拒绝未知 NPC 65534；
// 476 仍走独立材料转换 helper，报价与整链候选一致且先过场景授权；增幅独立报价。
func TestRealPVFJonathanConversionDefaultResolverScoping(t *testing.T) {
	fixture := rest14LoadFixture(t)
	t.Setenv("DNFBRIDGE_NATIVE_UPGRADE_COST", "")
	repos := memory.NewMemoryGroup()
	rest14SeedAssets(t, repos, rest14StartStones, "", "")
	service, session, _ := fixture.rest14Service(repos, fixture.townID, fixture.areaID, true)
	resolver := service.candidateUpgradeCostResolver(session)
	if resolver == nil {
		t.Fatal("empty value must install the full native quote resolver")
	}
	quote, err := resolver("reinforce", rest14TargetItemID, 0, true, rest14JonathanNPC)
	if err != nil {
		t.Fatal(err)
	}
	if !quote.Resolved || quote.GoldCost != 0 || quote.ConversionItemID != rest14TimeStoneItemID || quote.ConversionCount != 1 {
		t.Fatalf("npc 476 default quote = %+v", quote)
	}
	candidate, err := service.alignedNativeUpgradeCostResolver()("reinforce", rest14TargetItemID, 0, true, rest14JonathanNPC)
	if err != nil {
		t.Fatal(err)
	}
	if quote != candidate {
		t.Fatalf("default quote %+v differs from the native candidate %+v", quote, candidate)
	}
	for _, npc := range []uint16{rest14OrdinaryNPC, 2, 0xFFFF} {
		ordinary, err := resolver("reinforce", rest14TargetItemID, 0, true, npc)
		if err != nil || !ordinary.Resolved || ordinary.GoldCost != 84975 || ordinary.ConversionItemID != 0 || ordinary.ConversionCount != 0 {
			t.Fatalf("npc %d native quote=%+v err=%v, want resolved gold 84975", npc, ordinary, err)
		}
	}
	if unknown, err := resolver("reinforce", rest14TargetItemID, 0, true, 65534); err == nil {
		t.Fatalf("unknown npc 65534 must fail closed, got %+v", unknown)
	}
	if amplify, err := resolver("amplify", rest14TargetItemID, 0, true, rest14JonathanNPC); err != nil || !amplify.Resolved || amplify.GoldCost != 220500 || amplify.ConversionCount != 0 {
		t.Fatalf("amplify must quote its own gold cost without reinforcement conversion: %+v err=%v", amplify, err)
	}
	if absent, err := resolver("reinforce", rest14TargetItemID, 0, false, 0); err != nil || !absent.Resolved || absent.GoldCost != 84975 || absent.ConversionItemID != 0 || absent.ConversionCount != 0 {
		t.Fatalf("no npc context native quote=%+v err=%v, want resolved gold 84975", absent, err)
	}
	// 场景不符（角色停在另一个不含 476 的真实区域）时 476 也必须拒绝，不能报价。
	mismatch, _, _ := fixture.rest14Service(repos, fixture.otherTownID, fixture.otherAreaID, true)
	if quote, err := mismatch.candidateUpgradeCostResolver(mismatchSessionFor(t, mismatch))("reinforce", rest14TargetItemID, 0, true, rest14JonathanNPC); err == nil {
		t.Fatalf("npc 476 outside its town area must be rejected, got %+v", quote)
	}
}

func mismatchSessionFor(t *testing.T, service *Service) *gameSession {
	t.Helper()
	session, ok := service.gameSessions[77]
	if !ok || session == nil {
		t.Fatal("fixture session missing")
	}
	return session
}

// 真实 handler 专项（runner 固定名）：默认档扣精炼的时空石、场景不符拒绝、石头不足拒绝、
// 普通 NPC 使用完整原生金币报价并忽略旧 Extra 费用，off/candidate 保持各自语义。
func TestRealPVFJonathanDefaultHandlerRound14(t *testing.T) {
	fixture := rest14LoadFixture(t)
	for _, tc := range []struct {
		name          string
		flag          string
		npc           uint16
		stones        int64
		legacyGoldFee string
		level         string
		scene         string
		wantStones    int64
		wantGold      int64
		converts      bool
		reject        bool
	}{
		{"default 476 town scene deducts one stone", "", rest14JonathanNPC, rest14StartStones, "", "", "town", 4, rest14StartGold, true, false},
		{"default 476 other area rejected", "", rest14JonathanNPC, rest14StartStones, "", "", "other", rest14StartStones, rest14StartGold, false, true},
		{"default 476 scene not ready rejected", "", rest14JonathanNPC, rest14StartStones, "", "", "not-ready", rest14StartStones, rest14StartGold, false, true},
		{"default 476 missing stone stack rejected", "", rest14JonathanNPC, 0, "", "", "town", 0, rest14StartGold, false, true},
		// +10 时原生金币 254925 ⇒ 需要 3 个石头；给 2 个是真·不足档（数量 2 < 3）。
		{"default 476 insufficient stones rejected", "", rest14JonathanNPC, 2, "", "10", "town", 2, rest14StartGold, false, true},
		{"default ordinary npc uses native gold quote", "", rest14OrdinaryNPC, rest14StartStones, rest14LegacyGoldFee, "", "town", rest14StartStones, 15025, false, false},
		{"off keeps the pre-fix wiring", "off", rest14JonathanNPC, rest14StartStones, "", "", "town", rest14StartStones, rest14StartGold, false, false},
		{"candidate 476 town scene deducts one stone", "candidate", rest14JonathanNPC, rest14StartStones, "", "", "town", 4, rest14StartGold, true, false},
		{"candidate 476 other area rejected", "candidate", rest14JonathanNPC, rest14StartStones, "", "", "other", rest14StartStones, rest14StartGold, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("DNFBRIDGE_NATIVE_UPGRADE_COST", tc.flag)
			ctx := context.Background()
			repos := memory.NewMemoryGroup()
			beforeChar, beforeBag, beforeShared := rest14SeedAssets(t, repos, tc.stones, tc.legacyGoldFee, tc.level)
			townID, areaID, positionValid := fixture.townID, fixture.areaID, true
			switch tc.scene {
			case "other":
				townID, areaID = fixture.otherTownID, fixture.otherAreaID
			case "not-ready":
				positionValid = false
			}
			service, session, conn := fixture.rest14Service(repos, townID, areaID, positionValid)
			policy, err := service.alignedUpgradePolicyResolver()("reinforce", rest14TargetItemID, 0)
			if err != nil {
				t.Fatal(err)
			}
			if policy.MaterialItemID != 3037 || policy.MaterialCount <= 0 || policy.SuccessWeight != 100000 {
				t.Fatalf("unexpected pinned +0 policy: %+v", policy)
			}
			ack, updates, walletSeen := rest14UpgradeOp50(t, service, session, conn, tc.npc)
			if ack == nil {
				t.Fatal("no op50 ACK")
			}
			gotChar, _, _ := repos.Character.Load(ctx, rest14CharacterID)
			gotBag, _, _ := repos.Inventory.Load(ctx, rest14CharacterID)
			gotShared, _, _ := repos.AccountInventory.Load(ctx, rest14Account)
			summary := fmt.Sprintf("ACK=%x gold=%d→%d stones=%d→%d ordinary=%d→%d rows=%d target=%+v", ack,
				beforeChar.Stats["gold"], gotChar.Stats["gold"],
				beforeBag.Slots["0:135"].Count, gotBag.Slots["0:135"].Count,
				beforeShared.Slots[dnfrepo.AccountSharedInventorySlotKey(rest14MaterialSlot)].Count, gotShared.Slots[dnfrepo.AccountSharedInventorySlotKey(rest14MaterialSlot)].Count,
				len(updates), gotBag.Slots["0:54"].Extra["upgrade_level"])
			t.Logf("%s: %s", tc.name, summary)
			if tc.reject {
				// 用 Errorf 而不是 Fatalf：旧红/回归时必须把"资产到底扣没扣"一起打出来。
				if !bytes.Equal(ack, []byte{0, rest14InvalidMaterialACK}) {
					t.Errorf("rejection ACK=%x want 00%02x (%s)", ack, rest14InvalidMaterialACK, summary)
				}
				if !reflect.DeepEqual(beforeChar, gotChar) || !reflect.DeepEqual(beforeBag, gotBag) || !reflect.DeepEqual(beforeShared, gotShared) {
					t.Errorf("rejected request changed assets (%s)", summary)
				}
				if len(updates) != 0 {
					t.Errorf("rejected request projected %d success rows (%s)", len(updates), summary)
				}
				return
			}
			if len(ack) < 2 || ack[0] != 1 {
				t.Errorf("success ACK missing (%s)", summary)
				return
			}
			if gotChar.Stats["gold"] != tc.wantGold || gotBag.Slots["0:135"].Count != tc.wantStones {
				t.Errorf("settlement gold=%d stones=%d want %d/%d (%s)", gotChar.Stats["gold"], gotBag.Slots["0:135"].Count, tc.wantGold, tc.wantStones, summary)
			}
			if walletSeen != (tc.wantGold < rest14StartGold) {
				t.Errorf("wallet projected=%t for gold=%d (%s)", walletSeen, tc.wantGold, summary)
			}
			if gotBag.Slots["0:54"].ItemID != rest14TargetItemID || gotBag.Slots["0:54"].Extra["rest14_identity"] != "target-28309" || gotBag.Slots["0:54"].Extra["upgrade_level"] != "1" {
				t.Errorf("target identity/level changed incorrectly: %+v (%s)", gotBag.Slots["0:54"], summary)
			}
			if tc.converts {
				stoneRow := updates[uint16(rest14NPCStoneSlot)]
				if len(stoneRow) != 119 || binary.LittleEndian.Uint32(stoneRow[2:6]) != uint32(rest14TimeStoneItemID) {
					t.Errorf("conversion material projection absent/wrong (%s)", summary)
				} else if int64(binary.LittleEndian.Uint32(stoneRow[6:10])) != tc.wantStones {
					t.Errorf("conversion material projection has stale quantity %d want %d (%s)", int64(binary.LittleEndian.Uint32(stoneRow[6:10])), tc.wantStones, summary)
				}
			}
			normalRow := updates[uint16(rest14MaterialSlot)]
			if len(normalRow) != 119 || binary.LittleEndian.Uint32(normalRow[2:6]) != 3037 || int64(binary.LittleEndian.Uint32(normalRow[6:10])) != 1000-int64(policy.MaterialCount) {
				t.Errorf("ordinary material projection identity/quantity differs from committed assets (%s)", summary)
			}
			if int64(gotShared.Slots[dnfrepo.AccountSharedInventorySlotKey(rest14MaterialSlot)].Count) != 1000-int64(policy.MaterialCount) {
				t.Errorf("ordinary material was not charged exactly once (%s)", summary)
			}
		})
	}
}

// rest14AbortAccountAssets 沿用 epicStartAbortAssets 的手法：真实 memory 账号+角色资产事务，
// 回调成功返回后注入失败，检验晚期失败不会留下部分结算。
type rest14AbortAccountAssets struct {
	dnfrepo.AccountCharacterAssetUnitOfWork
	failure error
}

func (u rest14AbortAccountAssets) WithinAccountCharacterAssets(ctx context.Context, accountID string, characterID string, apply func(dnfrepo.AccountInventoryRepository, dnfrepo.CharacterRepository, dnfrepo.InventoryRepository, dnfrepo.EquipmentRepository) error) error {
	return u.AccountCharacterAssetUnitOfWork.WithinAccountCharacterAssets(ctx, accountID, characterID, func(accountInventory dnfrepo.AccountInventoryRepository, characters dnfrepo.CharacterRepository, inventory dnfrepo.InventoryRepository, equipment dnfrepo.EquipmentRepository) error {
		if err := apply(accountInventory, characters, inventory, equipment); err != nil {
			return err
		}
		return u.failure
	})
}

func TestRealPVFJonathanConversionLateFailureRollback(t *testing.T) {
	fixture := rest14LoadFixture(t)
	t.Setenv("DNFBRIDGE_NATIVE_UPGRADE_COST", "")
	ctx := context.Background()
	repos := memory.NewMemoryGroup()
	beforeChar, beforeBag, beforeShared := rest14SeedAssets(t, repos, rest14StartStones, "", "")
	failure := errors.New("injected abort after upgrade apply")
	repos.AccountAssets = rest14AbortAccountAssets{AccountCharacterAssetUnitOfWork: repos.AccountAssets, failure: failure}
	service, session, conn := fixture.rest14Service(repos, fixture.townID, fixture.areaID, true)
	ack, updates, _ := rest14UpgradeOp50(t, service, session, conn, rest14JonathanNPC)
	afterChar, _, _ := repos.Character.Load(ctx, rest14CharacterID)
	afterBag, _, _ := repos.Inventory.Load(ctx, rest14CharacterID)
	afterShared, _, _ := repos.AccountInventory.Load(ctx, rest14Account)
	t.Logf("late failure: ACK=%x gold=%d→%d stones=%d→%d rows=%d", ack, beforeChar.Stats["gold"], afterChar.Stats["gold"], beforeBag.Slots["0:135"].Count, afterBag.Slots["0:135"].Count, len(updates))
	if len(ack) >= 2 && ack[0] == 1 {
		t.Errorf("aborted transaction still returned a success ACK %x (gold=%d stones=%d)", ack, afterChar.Stats["gold"], afterBag.Slots["0:135"].Count)
	}
	if len(updates) != 0 {
		t.Errorf("aborted transaction projected %d settlement rows", len(updates))
	}
	if !reflect.DeepEqual(beforeChar, afterChar) || !reflect.DeepEqual(beforeBag, afterBag) || !reflect.DeepEqual(beforeShared, afterShared) {
		t.Fatal("aborted transaction left a partial settlement")
	}
}

// 已核 476 的资源失败专项（默认档）：城镇场景有效，但运行时档案缺失 ⇒ 规则读不出来时必须
// 失败关闭。判据是结算层面"没扣金币、也没扣石头、没有成功回包"，而不是只看 resolver 返回 err。
func TestRealPVFJonathanConversionArchiveMissingFailsClosed(t *testing.T) {
	fixture := rest14LoadFixture(t)
	t.Setenv("DNFBRIDGE_NATIVE_UPGRADE_COST", "")
	ctx := context.Background()
	repos := memory.NewMemoryGroup()
	beforeChar, beforeBag, beforeShared := rest14SeedAssets(t, repos, rest14StartStones, "", "")
	service, session, conn := fixture.rest14Service(repos, fixture.townID, fixture.areaID, true)
	// 场景本身有效（与 default 476 成功档同一夹具），只让运行档案拿不到：既清掉已装配的
	// 档案字段，也把懒加载路径指到不存在的文件，确保 476 的规则真的读不出来。
	service.initialEquipmentArchive = nil
	service.options.pvfPath = t.TempDir() + "/missing-Script.pvf"
	ack, updates, _ := rest14UpgradeOp50(t, service, session, conn, rest14JonathanNPC)
	gotChar, _, _ := repos.Character.Load(ctx, rest14CharacterID)
	gotBag, _, _ := repos.Inventory.Load(ctx, rest14CharacterID)
	gotShared, _, _ := repos.AccountInventory.Load(ctx, rest14Account)
	t.Logf("archive missing: ACK=%x gold=%d→%d stones=%d→%d rows=%d", ack, beforeChar.Stats["gold"], gotChar.Stats["gold"], beforeBag.Slots["0:135"].Count, gotBag.Slots["0:135"].Count, len(updates))
	if len(ack) >= 2 && ack[0] == 1 {
		t.Errorf("missing runtime archive returned a success ACK %x", ack)
	}
	if gotChar.Stats["gold"] != beforeChar.Stats["gold"] {
		t.Errorf("missing runtime archive fell back to gold: %d → %d", beforeChar.Stats["gold"], gotChar.Stats["gold"])
	}
	if gotBag.Slots["0:135"].Count != beforeBag.Slots["0:135"].Count {
		t.Errorf("stones changed without a rule: %d → %d", beforeBag.Slots["0:135"].Count, gotBag.Slots["0:135"].Count)
	}
	if len(updates) != 0 {
		t.Errorf("missing runtime archive projected %d rows", len(updates))
	}
	if !reflect.DeepEqual(beforeChar, gotChar) || !reflect.DeepEqual(beforeBag, gotBag) || !reflect.DeepEqual(beforeShared, gotShared) {
		t.Error("missing runtime archive changed assets")
	}
}
