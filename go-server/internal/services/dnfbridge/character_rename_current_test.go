package dnfbridge

import (
	"bytes"
	"context"
	"encoding/binary"
	"strings"
	"testing"

	"golang.org/x/text/encoding/simplifiedchinese"

	"longheng.io/server/internal/modules/dnf/characterdata"
	"longheng.io/server/internal/modules/dnf/dnfenum"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	dnfrepomemory "longheng.io/server/internal/modules/dnf/repository/memory"
)

// class1/op135（ENUM_CMDPACKET_CHANGE_CHARAC_NAME）在 official-24.9 下的**实际路由**
// 回归。
//
// 下面的请求体是**按形状构造**的，不是任何一天的现场帧：2026-10-15 的报障窗口里
// 没有任何 135 上行，那一天的日志因此不能当作这条路由的证据。形状取自
// _dist/rename-delete-diagnosis.md §2.2（2026-09-28 packet_log.txt:60954 的 27 字节
// 请求体），与 character_rename_test.go 的同一读法：
//
//	u8 selector | u32 旧名长度 | 旧名 | u32 新名长度 | 新名 | u8 tail
//
// 名字是客户端本地码页（GBK/GB18030）的原始字节，不是 UTF-16/UTF-8。
//
// selector 指的是**下发在册列表里的行号**，不是 SQL 槽位：native 0x36C3950 按行向量
// 取出该行的 name。所以这里把 slot=3 的角色放在 row index 0，并检查 handler 用这一行
// （而不是槽位）去核对 owner 与旧名。
//
// 这些用例走 handleGameCommand（cmd=1 的命令包）而不是直接调 handler：真正决定
// 走新路由还是既有 phase-1 判读的两个开关（currentClientVersion 与
// DNFBRIDGE_CHARACTER_RENAME）都只在分派里生效，直接调 handler 会把它们绕过去。
const (
	bridge135AccountID   = "dnf:1"
	bridge135CharacterID = "26"
	bridge135Slot        = 3
	bridge135OldName     = "旧名字"
	bridge135NewName     = "新角色名"
)

type bridge135Fixture struct {
	service *Service
	session *gameSession
	conn    *bufferConn
	repos   dnfrepo.Group
	clock   *fakeCurrentDungeonDeathTimerQueue
}

// newBridge135Fixture 造一个只含"待改名角色"的独立内存档，以及一条已认证、**没有
// 选角**（selectedCharacterID=0）的会话。改名权 = 1、最低等级 = 1、冷却 = 0、
// 原名保留 = 0，即「改名卡已生效、只等选名字」的那一行。
func newBridge135Fixture(t *testing.T, profile string) *bridge135Fixture {
	t.Helper()
	// 未设/空 = 新的 current 路由档，只有字面量 legacy 才回退；显式清空，免得外部
	// 环境决定这些断言走哪条路。
	t.Setenv(characterRenameLegacyEnv, "")
	ctx := context.Background()
	repos := dnfrepomemory.NewMemoryGroup()
	seeded := dnfrepo.CharacterRecord{
		CharacterID: bridge135CharacterID,
		AccountID:   bridge135AccountID,
		Name:        bridge135OldName,
		Slot:        bridge135Slot,
		Level:       1,
		Stats: map[string]int64{
			characterdata.RenameEntitlementStat:        1,
			characterdata.RenameMinimumLevelStat:       1,
			characterdata.RenameCooldownSecondsStat:    0,
			characterdata.RenameReservationSecondsStat: 0,
			"delete_flag": 0,
		},
	}
	if err := repos.Character.Save(ctx, seeded); err != nil {
		t.Fatalf("seed rename character: %v", err)
	}
	conn := &bufferConn{}
	clock := newFakeCurrentDungeonDeathTimerQueue()
	service := &Service{
		options: options{
			currentClientVersion: profile,
			accountID:            "shared-fallback-account",
		},
		gameplayTimers:     clock,
		repositoryProvider: func() (dnfrepo.Group, bool) { return repos, true },
	}
	session := &gameSession{
		conn:                conn,
		connID:              "bridge135-current-rename",
		accountID:           bridge135AccountID,
		accountFromToken:    true,
		loginPacketSeen:     true,
		selectedCharacterID: 0,
	}
	// 弹窗索引的是下发的紧凑客户端行，不是 SQL 槽位：行 0 就是那个 slot=3 的真实
	// 角色（在册快照由 rememberCurrentRenameRoster 从仓储列表抄来）。
	row, found, err := repos.Character.Load(ctx, bridge135CharacterID)
	if err != nil || !found {
		t.Fatalf("load seeded rename row: found=%t err=%v", found, err)
	}
	session.currentRenameRoster = []dnfrepo.CharacterRecord{row}
	return &bridge135Fixture{service: service, session: session, conn: conn, repos: repos, clock: clock}
}

func (f *bridge135Fixture) record(t *testing.T) dnfrepo.CharacterRecord {
	t.Helper()
	record, found, err := f.repos.Character.Load(context.Background(), bridge135CharacterID)
	if err != nil || !found {
		t.Fatalf("load rename character: found=%t err=%v", found, err)
	}
	return record
}

func (f *bridge135Fixture) dispatch(t *testing.T, body []byte) {
	t.Helper()
	if err := f.service.handleGameCommand(f.session, byte(dnfenum.GameCmdCommand), uint16(dnfenum.CmdPacketChangeCharacName), body); err != nil {
		t.Fatalf("handleGameCommand class1/op135: %v", err)
	}
}

// bridge135Body 按现场形状拼 op135 请求体：selector、旧名、新名、tail。名字用
// GB18030 编码后写成长度前缀字节串（packetWriter.writeRawDstr = u32 LE + 原始字节）。
func bridge135Body(t *testing.T, selector byte, oldName, newName string, tail byte) []byte {
	t.Helper()
	encoder := simplifiedchinese.GB18030.NewEncoder()
	oldRaw, err := encoder.Bytes([]byte(oldName))
	if err != nil {
		t.Fatalf("encode old name: %v", err)
	}
	newRaw, err := encoder.Bytes([]byte(newName))
	if err != nil {
		t.Fatalf("encode new name: %v", err)
	}
	var writer packetWriter
	writer.writeByte(selector)
	writer.writeRawDstr(oldRaw)
	writer.writeRawDstr(newRaw)
	writer.writeByte(tail)
	return writer.bytes()
}

// requireBridge135Ack 断言这条路由只回一个 class1/135 包，且包体逐字节等于期望
// （成功 = {1}，拒绝 = {0,2}）。
func requireBridge135Ack(t *testing.T, conn *bufferConn, want []byte) {
	t.Helper()
	packet, rest := splitGameServerUpperPacket(t, conn.write.Bytes())
	if len(rest) != 0 {
		t.Fatalf("op135 route wrote %d trailing bytes after the ACK: %x", len(rest), rest)
	}
	if packet.Header.Classification != 1 || packet.Header.MsgID != uint16(dnfenum.CmdPacketChangeCharacName) {
		t.Fatalf("op135 ACK header = class %d msg %d, want class 1 msg %d",
			packet.Header.Classification, packet.Header.MsgID, uint16(dnfenum.CmdPacketChangeCharacName))
	}
	if !bytes.Equal(packet.Body, want) {
		t.Fatalf("op135 ACK body = %x, want %x", packet.Body, want)
	}
}

// bridge135Reservations 读名字命名空间里的原名保留项（reserve=0 时必须为空）。
func bridge135Reservations(t *testing.T, repos dnfrepo.Group) map[string]string {
	t.Helper()
	state, found, err := repos.Settings.Load(context.Background(), dnfrepo.CharacterNameMutationScope)
	if err != nil {
		t.Fatalf("load name namespace settings: %v", err)
	}
	if !found {
		return nil
	}
	reserved := map[string]string{}
	for key, value := range state.Values {
		if strings.HasPrefix(key, "reserved:") {
			reserved[key] = value
		}
	}
	return reserved
}

// 成功路径：class1/135 ACK{1}，落库新名字，改名权清零，冷却/保留都是 0 时只写时间戳；
// 同一包重放走幂等回执，ACK 相同且不再挪动时间戳。
func TestBridge135Official249CommitsRenameAndRepeatsWithoutNewTimestamp(t *testing.T) {
	fixture := newBridge135Fixture(t, "official-24.9")
	body := bridge135Body(t, 0, bridge135OldName, bridge135NewName, 1)

	fixture.dispatch(t, body)
	requireBridge135Ack(t, fixture.conn, []byte{1})

	now := fixture.clock.Now().UTC().Unix()
	committed := fixture.record(t)
	if committed.Name != bridge135NewName {
		t.Fatalf("stored name = %q, want %q", committed.Name, bridge135NewName)
	}
	if pending := committed.Stats[characterdata.RenameEntitlementStat]; pending != 0 {
		t.Fatalf("stored entitlement = %d, want 0 after the committed rename", pending)
	}
	if last := committed.Stats[characterdata.RenameLastUnixStat]; last != now {
		t.Fatalf("stored rename timestamp = %d, want %d", last, now)
	}
	if next := committed.Stats[characterdata.RenameNextAllowedUnixStat]; next != now {
		t.Fatalf("stored next-allowed timestamp = %d, want %d (cooldown 0)", next, now)
	}
	if reserved := bridge135Reservations(t, fixture.repos); len(reserved) != 0 {
		t.Fatalf("reserve 0 still wrote name reservations: %v", reserved)
	}

	// 同一包重放：在册行仍是提交前那份（旧名），事务里的幂等回执放行这次重放。
	// 它不得再花一次改名权，也不得把冷却/保留期限往后挪。
	fixture.conn.write.Reset()
	fixture.dispatch(t, body)
	requireBridge135Ack(t, fixture.conn, []byte{1})

	replayed := fixture.record(t)
	if replayed.Name != committed.Name || replayed.Stats[characterdata.RenameEntitlementStat] != 0 {
		t.Fatalf("replay changed the stored character: %q pending=%d", replayed.Name, replayed.Stats[characterdata.RenameEntitlementStat])
	}
	if replayed.Stats[characterdata.RenameLastUnixStat] != committed.Stats[characterdata.RenameLastUnixStat] ||
		replayed.Stats[characterdata.RenameNextAllowedUnixStat] != committed.Stats[characterdata.RenameNextAllowedUnixStat] {
		t.Fatalf("replay moved the rename timestamps: last %d->%d next %d->%d",
			committed.Stats[characterdata.RenameLastUnixStat], replayed.Stats[characterdata.RenameLastUnixStat],
			committed.Stats[characterdata.RenameNextAllowedUnixStat], replayed.Stats[characterdata.RenameNextAllowedUnixStat])
	}
}

// 拒绝路径：selector 越界 / 在册行名字对不上 / 会话账号不是该行owner / 角色没有改名权，
// 四种都必须回 class1/135 失败包 {0,2}，并且一个字节的仓位都不动。
func TestBridge135Official249RejectsWithoutDatabaseChange(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(t *testing.T, fixture *bridge135Fixture) []byte
	}{
		{
			name: "selector_out_of_roster",
			mutate: func(t *testing.T, fixture *bridge135Fixture) []byte {
				return bridge135Body(t, 1, bridge135OldName, bridge135NewName, 1)
			},
		},
		{
			name: "old_name_not_on_roster_row",
			mutate: func(t *testing.T, fixture *bridge135Fixture) []byte {
				return bridge135Body(t, 0, "别人的旧名字", bridge135NewName, 1)
			},
		},
		{
			name: "account_is_not_row_owner",
			mutate: func(t *testing.T, fixture *bridge135Fixture) []byte {
				fixture.session.accountID = "dnf:2"
				return bridge135Body(t, 0, bridge135OldName, bridge135NewName, 1)
			},
		},
		{
			name: "no_rename_entitlement",
			mutate: func(t *testing.T, fixture *bridge135Fixture) []byte {
				record := fixture.record(t)
				record.Stats[characterdata.RenameEntitlementStat] = 0
				if err := fixture.repos.Character.Save(context.Background(), record); err != nil {
					t.Fatalf("clear entitlement: %v", err)
				}
				return bridge135Body(t, 0, bridge135OldName, bridge135NewName, 1)
			},
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			fixture := newBridge135Fixture(t, "official-24.9")
			body := testCase.mutate(t, fixture)
			before := fixture.record(t)

			fixture.dispatch(t, body)
			requireBridge135Ack(t, fixture.conn, []byte{0, 2})

			after := fixture.record(t)
			if after.Name != before.Name ||
				after.Stats[characterdata.RenameEntitlementStat] != before.Stats[characterdata.RenameEntitlementStat] ||
				after.Stats[characterdata.RenameLastUnixStat] != before.Stats[characterdata.RenameLastUnixStat] ||
				after.Stats[characterdata.RenameNextAllowedUnixStat] != before.Stats[characterdata.RenameNextAllowedUnixStat] {
				t.Fatalf("rejected op135 changed the stored character: name %q->%q pending %d->%d last %d->%d next %d->%d",
					before.Name, after.Name,
					before.Stats[characterdata.RenameEntitlementStat], after.Stats[characterdata.RenameEntitlementStat],
					before.Stats[characterdata.RenameLastUnixStat], after.Stats[characterdata.RenameLastUnixStat],
					before.Stats[characterdata.RenameNextAllowedUnixStat], after.Stats[characterdata.RenameNextAllowedUnixStat])
			}
			if after.Name != bridge135OldName {
				t.Fatalf("rejected op135 stored name = %q, want the original %q", after.Name, bridge135OldName)
			}
			if reserved := bridge135Reservations(t, fixture.repos); len(reserved) != 0 {
				t.Fatalf("rejected op135 wrote name reservations: %v", reserved)
			}
		})
	}
}

// legacy 档：非 current 版本（含 90cn 默认档）以及显式 DNFBRIDGE_CHARACTER_RENAME=legacy，
// 都保持既有行为——解析 + 具名事件 + **零回包**，同样不写库。
func TestBridge135LegacyProfileAndSwitchKeepExistingSilence(t *testing.T) {
	cases := []struct {
		name     string
		profile  string
		switchTo string
	}{
		{name: "profile_90cn", profile: "90cn"},
		{name: "profile_empty", profile: ""},
		{name: "profile_official249_with_legacy_switch", profile: "official-24.9", switchTo: characterRenameLegacyValue},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			fixture := newBridge135Fixture(t, testCase.profile)
			if testCase.switchTo != "" {
				t.Setenv(characterRenameLegacyEnv, testCase.switchTo)
			}
			before := fixture.record(t)

			fixture.dispatch(t, bridge135Body(t, 0, bridge135OldName, bridge135NewName, 1))

			if written := fixture.conn.write.Bytes(); len(written) != 0 {
				t.Fatalf("op135 wrote %x on the legacy path, want the existing phase-1 silence", written)
			}
			after := fixture.record(t)
			if after.Name != before.Name ||
				after.Stats[characterdata.RenameEntitlementStat] != before.Stats[characterdata.RenameEntitlementStat] ||
				after.Stats[characterdata.RenameLastUnixStat] != before.Stats[characterdata.RenameLastUnixStat] {
				t.Fatalf("legacy op135 changed the stored character: %q pending=%d", after.Name, after.Stats[characterdata.RenameEntitlementStat])
			}
			if after.Name != bridge135OldName {
				t.Fatalf("legacy op135 stored name = %q, want %q", after.Name, bridge135OldName)
			}
		})
	}
}

// 下发的 official-24.9 在册行：带改名权的行在名字后的第一个 flag 必须是 1、第二个保留
// 字节必须是 0（否则客户端卡片进不了改名窗口）；普通行两个都是 0；名字一律用角色记录
// 里存下来的 GB18030 真实名字，不落回合成的 dnf_<角色ID> 占位名。
//
// 名字后面那一个 flag 就是 022c17e0 紧跟 DSTR 读到、036d6500 用来开 1b0 窗口的值；
// 名字本身由 selector 指向的那一行（0x36C3950 的行向量）提供，不是另造的名字。
func TestOfficial249RosterEntryRenameFlagUsesStoredName(t *testing.T) {
	cases := []struct {
		name     string
		record   dnfrepo.CharacterRecord
		index    int
		wantFlag byte
	}{
		{
			name: "pending_rename_row",
			record: dnfrepo.CharacterRecord{
				CharacterID: bridge135CharacterID,
				AccountID:   bridge135AccountID,
				Name:        bridge135OldName,
				Slot:        bridge135Slot,
				Job:         "14",
				Level:       90,
				Stats:       map[string]int64{characterdata.RenameEntitlementStat: 1},
			},
			index:    0,
			wantFlag: 1,
		},
		{
			name: "ordinary_row",
			record: dnfrepo.CharacterRecord{
				CharacterID: "27",
				AccountID:   bridge135AccountID,
				Name:        "普通角色",
				Slot:        0,
				Job:         "0",
				Level:       1,
			},
			index:    1,
			wantFlag: 0,
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			var writer packetWriter
			writeOfficial249RosterEntry(&writer, testCase.index, testCase.record)
			body := writer.bytes()

			if len(body) < 6+2 {
				t.Fatalf("roster entry too short: %d", len(body))
			}
			if slot := binary.LittleEndian.Uint16(body[0:2]); slot != uint16(testCase.record.Slot) {
				t.Fatalf("roster entry slot = %d, want %d", slot, testCase.record.Slot)
			}
			nameLength := int(binary.LittleEndian.Uint32(body[2:6]))
			if nameLength <= 0 || 6+nameLength+2 > len(body) {
				t.Fatalf("roster entry name length = %d in %d bytes", nameLength, len(body))
			}
			name := body[6 : 6+nameLength]
			wantName, err := simplifiedchinese.GB18030.NewEncoder().Bytes([]byte(testCase.record.Name))
			if err != nil {
				t.Fatalf("encode stored name: %v", err)
			}
			if !bytes.Equal(name, wantName) {
				t.Fatalf("roster entry name = %x, want the stored GB18030 name %x", name, wantName)
			}
			if bytes.Contains(name, []byte(fallbackCharacterNameMarker)) {
				t.Fatalf("roster entry name %q is the synthetic dnf_<id> fallback, not the stored name", name)
			}
			if flag := body[6+nameLength]; flag != testCase.wantFlag {
				t.Fatalf("roster entry rename flag = %d, want %d (selector reads it to open the rename window)", flag, testCase.wantFlag)
			}
			if reserved := body[6+nameLength+1]; reserved != 0 {
				t.Fatalf("roster entry byte after the rename flag = %d, want 0", reserved)
			}
		})
	}
}
