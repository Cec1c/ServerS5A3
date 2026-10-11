package dnfbridge

import (
	"bytes"
	"encoding/binary"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
	"longheng.io/server/internal/modules/dnf/dnfenum"
	"longheng.io/server/internal/platform/config"
)

// 本文件钉住 DNFBRIDGE_PARTY_DUNGEON_OWNER_MODE 单变量 A/B：
// 副本内发给**其他**队员的 op2 mode0 / op2 mode1 / op9 三条包的 owner 字节。
//
//   - 默认（未设置，或显式 zero）= 与加开关前**逐字节相同**（owner = 副本场景上下文 0）；
//   - channel = 上述三条包的 owner 字节变成**收包者频道号**（本 fixture = 121），
//     而 mode0/mode1 除这一个字节外逐字节不变；
//   - 本地角色**自己**那条包（op9 自记录、op153、以及本地 actor 的 mode0/mode1）在两个
//     取值下都逐字节不变。
//
// 权威 owner 字节位置（写入点见 partyDungeonPeerOwner 的注释）：
// mode0/mode1 = body[4]，op9 = body[7]。

type dungeonPeerActorWire struct {
	mode0 []byte
	mode1 []byte
	op9   []byte
}

// captureDungeonPeerActorWire 走一次"把队友 actor 投影给 receiver"（mode0 + mode1 + op9），
// 返回三条原始 body。
func captureDungeonPeerActorWire(t *testing.T, service *Service, receiver, actor *gameSession, source string) dungeonPeerActorWire {
	t.Helper()
	conn := receiver.conn.(*bufferConn)
	conn.write.Reset()
	if err := service.sendDungeonPartyPeerActorState(receiver, actor, source); err != nil {
		t.Fatalf("%s: send peer actor state: %v", source, err)
	}
	frames := splitUpperFrames(t, conn.write.Bytes())
	if len(frames) != 3 {
		t.Fatalf("%s: frames=%d want mode0 + mode1 + op9 (%+v)", source, len(frames), frames)
	}
	if frames[0].msgID != uint16(dnfenum.CmdPacketSetUDPIPPort) ||
		frames[1].msgID != uint16(dnfenum.CmdPacketSetUDPIPPort) ||
		frames[2].msgID != uint16(dnfenum.CmdPacketRecoverStamina) {
		t.Fatalf("%s: frame msg ids=%+v want 0x0002 0x0002 0x0009", source, frames)
	}
	return dungeonPeerActorWire{
		mode0: frames[0].body,
		mode1: frames[1].body,
		op9:   frames[2].body,
	}
}

// partyDungeonOwnerSwitchFixture 给出"同一场副本里两个成员"的会话与队伍状态，
// 并把开关设成调用方要求的值。legacy 表示**完全不提** options.partyDungeonOwner 字段
// （加开关之前的构造方式）。
func partyDungeonOwnerSwitchFixture(t *testing.T, mode partyDungeonOwnerMode, legacy bool) (*Service, *gameSession, *gameSession, alignedcmd.PartyState) {
	t.Helper()
	service, leader, follower, _, _ := partyDungeonDeathFixture(t)
	if !legacy {
		service.options.partyDungeonOwner = mode
	}
	return service, leader, follower, runtimePartyStateSnapshot(leader)
}

func TestPartyDungeonOwnerModeReadsEnvironmentSwitchAndDefaultsToZero(t *testing.T) {
	for _, test := range []struct {
		value   string
		want    partyDungeonOwnerMode
		wantBad bool
	}{
		{value: "", want: partyDungeonOwnerSceneContext},
		{value: "zero", want: partyDungeonOwnerSceneContext},
		{value: " ZERO ", want: partyDungeonOwnerSceneContext},
		{value: "default", want: partyDungeonOwnerSceneContext},
		{value: "dungeon_context", want: partyDungeonOwnerSceneContext},
		{value: "channel", want: partyDungeonOwnerClientChannel},
		{value: "CHANNEL", want: partyDungeonOwnerClientChannel},
		{value: " channel", want: partyDungeonOwnerClientChannel},
		{value: "client_channel", want: partyDungeonOwnerClientChannel},
		{value: "mixed", want: partyDungeonOwnerMixed},
		{value: " MIXED ", want: partyDungeonOwnerMixed},
		{value: "op2_channel", want: partyDungeonOwnerMixed},
		{value: "121", want: partyDungeonOwnerSceneContext, wantBad: true},
		{value: "owner=121", want: partyDungeonOwnerSceneContext, wantBad: true},
		{value: "dungeon", want: partyDungeonOwnerSceneContext, wantBad: true},
	} {
		t.Run(partyDungeonOwnerModeEnv+"="+test.value, func(t *testing.T) {
			t.Setenv(partyDungeonOwnerModeEnv, test.value)
			opts := optionsFromConfig(config.ServiceConfig{})
			if opts.partyDungeonOwner != test.want {
				t.Fatalf("party dungeon owner mode=%v want=%v", opts.partyDungeonOwner, test.want)
			}
			if opts.partyDungeonOwnerEnvBad != test.wantBad {
				t.Fatalf("invalid flag=%t want=%t (raw=%q)",
					opts.partyDungeonOwnerEnvBad, test.wantBad, opts.partyDungeonOwnerEnvRaw)
			}
			// 非法取值必须能在启动日志里被指名（Service.Start 把这两项写进日志）。
			if test.wantBad && opts.partyDungeonOwnerEnvRaw != strings.TrimSpace(test.value) {
				t.Fatalf("raw env value=%q want=%q", opts.partyDungeonOwnerEnvRaw, strings.TrimSpace(test.value))
			}
		})
	}
	// 零值 options = 加开关之前的构造方式（测试与探针里的 Service 字面量）：必须保持现行为。
	if got := (&Service{}).options.partyDungeonOwner; got != partyDungeonOwnerSceneContext {
		t.Fatalf("zero-value options selected owner mode %v", got)
	}
	if got := (&Service{}).options.partyDungeonOwner.String(); got != partyDungeonOwnerModeValueZero {
		t.Fatalf("zero-value options log label=%q want=%q", got, partyDungeonOwnerModeValueZero)
	}
	if got := partyDungeonOwnerClientChannel.String(); got != partyDungeonOwnerModeValueChannel {
		t.Fatalf("channel log label=%q want=%q", got, partyDungeonOwnerModeValueChannel)
	}
	if got := partyDungeonOwnerMixed.String(); got != partyDungeonOwnerModeValueMixed {
		t.Fatalf("mixed log label=%q want=%q", got, partyDungeonOwnerModeValueMixed)
	}
}

// 默认（legacy）与显式 zero 必须写出同一份字节，且 owner 字节就是副本场景上下文 0。
func TestDungeonPartyOwnerModeZeroKeepsByteIdenticalWire(t *testing.T) {
	legacyService, legacyLeader, legacyFollower, _ := partyDungeonOwnerSwitchFixture(t, partyDungeonOwnerSceneContext, true)
	legacy := captureDungeonPeerActorWire(t, legacyService, legacyFollower, legacyLeader, "legacy")

	zeroService, leader, follower, _ := partyDungeonOwnerSwitchFixture(t, partyDungeonOwnerSceneContext, false)
	explicitZero := captureDungeonPeerActorWire(t, zeroService, follower, leader, "explicit_zero")

	for name, pair := range map[string][2][]byte{
		"mode0": {legacy.mode0, explicitZero.mode0},
		"mode1": {legacy.mode1, explicitZero.mode1},
		"op9":   {legacy.op9, explicitZero.op9},
	} {
		if !bytes.Equal(pair[0], pair[1]) {
			t.Fatalf("%s: explicit zero changed the wire\nlegacy=% X\nexplicit=% X", name, pair[0], pair[1])
		}
	}
	// mode0/mode1 的 owner 字节在 body[4]，op9 在 body[7]。
	if legacy.mode0[0] != 0 || legacy.mode0[4] != currentSceneObjectContext {
		t.Fatalf("mode0 mode=%d owner=%d want mode 0 owner %d body=% X",
			legacy.mode0[0], legacy.mode0[4], currentSceneObjectContext, legacy.mode0)
	}
	if legacy.mode1[0] != 1 || legacy.mode1[4] != currentSceneObjectContext {
		t.Fatalf("mode1 mode=%d owner=%d want mode 1 owner %d body=% X",
			legacy.mode1[0], legacy.mode1[4], currentSceneObjectContext, legacy.mode1)
	}
	if legacy.op9[7] != currentSceneObjectContext || legacy.op9[9] != currentSceneOp9ActorDisplayKind {
		t.Fatalf("op9 owner=%d kind=%d want owner %d kind %d body=% X",
			legacy.op9[7], legacy.op9[9], currentSceneObjectContext, currentSceneOp9ActorDisplayKind, legacy.op9)
	}
	// 现行为：owner(0) != 客户端频道(121) ⇒ 客户端按跨频道分支读内联身份块。
	if got := currentClientSelectedChannel(legacyFollower); got != 121 {
		t.Fatalf("client selected channel=%d want 121", got)
	}
	if legacy.op9[7] == currentClientSelectedChannel(legacyFollower) {
		t.Fatal("default owner must differ from the client channel so member rows keep the identity block")
	}
	rows, leaderSlot := parseOp9PartyFrame(t, legacy.op9, true)
	if len(rows) != 2 || rows[0].uid != 99 || rows[1].uid != 100 {
		t.Fatalf("default op9 rows=%+v want leader 99 at slot 0", rows)
	}
	for index, row := range rows {
		if !row.hasIdentity || row.name == "" {
			t.Fatalf("default op9 row[%d]=%+v lost the inline identity block", index, row)
		}
	}
	if leaderSlot != 0 || rows[leaderSlot].uid != 99 {
		t.Fatalf("default op9 leader slot=%d (uid=%d) want slot 0 = leader 99", leaderSlot, rows[leaderSlot].uid)
	}
}

// 开关 = channel：mode0/mode1 只有 owner 这一个字节变化；op9 的 owner 变成频道号。
func TestDungeonPartyOwnerModeChannelRewritesOnlyPeerOwnerByte(t *testing.T) {
	const channelID = byte(121)

	zeroService, zeroLeader, zeroFollower, _ := partyDungeonOwnerSwitchFixture(t, partyDungeonOwnerSceneContext, false)
	zero := captureDungeonPeerActorWire(t, zeroService, zeroFollower, zeroLeader, "zero")

	channelService, leader, follower, _ := partyDungeonOwnerSwitchFixture(t, partyDungeonOwnerClientChannel, false)
	wire := captureDungeonPeerActorWire(t, channelService, follower, leader, "channel")

	if got := currentClientSelectedChannel(follower); got != channelID {
		t.Fatalf("client selected channel=%d want=%d", got, channelID)
	}
	// mode0/mode1：owner 字节（body[4]）之外逐字节相同。
	for name, pair := range map[string][2][]byte{
		"mode0": {zero.mode0, wire.mode0},
		"mode1": {zero.mode1, wire.mode1},
	} {
		before, after := pair[0], pair[1]
		if len(before) != len(after) {
			t.Fatalf("%s body len %d -> %d must not change", name, len(before), len(after))
		}
		for index := range before {
			if index == 4 {
				continue
			}
			if before[index] != after[index] {
				t.Fatalf("%s byte[%d]=%02x -> %02x; only the owner byte may change\nbefore=% X\nafter =% X",
					name, index, before[index], after[index], before, after)
			}
		}
		if after[4] != channelID {
			t.Fatalf("%s owner byte=%d want channel %d body=% X", name, after[4], channelID, after)
		}
	}
	// op9：owner 字节 = 频道号；客户端因此走"同频道"分支（成员行不带身份块）。
	if wire.op9[7] != channelID {
		t.Fatalf("op9 owner byte=%d want channel %d body=% X", wire.op9[7], channelID, wire.op9)
	}
	if wire.op9[9] != currentSceneOp9ActorDisplayKind {
		t.Fatalf("op9 kind=%d want %d (record type must not change)", wire.op9[9], currentSceneOp9ActorDisplayKind)
	}
	rows, leaderSlot := parseOp9PartyFrame(t, wire.op9, false)
	zeroRows, zeroLeaderSlot := parseOp9PartyFrame(t, zero.op9, true)
	if len(rows) != len(zeroRows) {
		t.Fatalf("op9 row count %d -> %d", len(zeroRows), len(rows))
	}
	for index := range rows {
		if rows[index].slot != zeroRows[index].slot || rows[index].uid != zeroRows[index].uid ||
			rows[index].state != zeroRows[index].state {
			t.Fatalf("op9 row[%d]=%+v changed slot/uid/state vs %+v", index, rows[index], zeroRows[index])
		}
		if rows[index].hasIdentity {
			t.Fatalf("channel mode must fall on the same-channel branch (no identity block) row[%d]=%+v", index, rows[index])
		}
	}
	if leaderSlot != zeroLeaderSlot || leaderSlot != 0 || rows[leaderSlot].uid != 99 {
		t.Fatalf("op9 leader slot=%d (want %d, uid=99) zero-mode=%d", leaderSlot, zeroLeaderSlot, zeroLeaderSlot)
	}
	// 唯一的线格式差异只能来自身份块（owner 字节落在同频道分支 ⇒ 行变短）。
	if len(wire.op9) >= len(zero.op9) {
		t.Fatalf("op9 body len=%d want shorter than the identity-block form %d", len(wire.op9), len(zero.op9))
	}
}

// 开关 = mixed：**只改 op2 mode0/mode1 的 owner 字节**，op9 与本地自身包一字节不动。
func TestDungeonPartyOwnerModeMixedRewritesOnlyPeerOp2OwnerByte(t *testing.T) {
	const channelID = byte(121)

	zeroService, zeroLeader, zeroFollower, _ := partyDungeonOwnerSwitchFixture(t, partyDungeonOwnerSceneContext, false)
	zero := captureDungeonPeerActorWire(t, zeroService, zeroFollower, zeroLeader, "zero")

	mixedService, leader, follower, _ := partyDungeonOwnerSwitchFixture(t, partyDungeonOwnerMixed, false)
	mixed := captureDungeonPeerActorWire(t, mixedService, follower, leader, "mixed")

	if got := currentClientSelectedChannel(follower); got != channelID {
		t.Fatalf("client selected channel=%d want=%d", got, channelID)
	}
	// op2 mode0/mode1：只有 owner 字节（body[4]）变成频道号，其余逐字节相同。
	for name, pair := range map[string][2][]byte{
		"mode0": {zero.mode0, mixed.mode0},
		"mode1": {zero.mode1, mixed.mode1},
	} {
		before, after := pair[0], pair[1]
		if len(before) != len(after) {
			t.Fatalf("%s body len %d -> %d must not change", name, len(before), len(after))
		}
		for index := range before {
			if index == 4 {
				continue
			}
			if before[index] != after[index] {
				t.Fatalf("%s byte[%d]=%02x -> %02x; mixed may only change the op2 owner byte\nbefore=% X\nafter =% X",
					name, index, before[index], after[index], before, after)
			}
		}
		if after[4] != channelID {
			t.Fatalf("%s owner byte=%d want channel %d body=% X", name, after[4], channelID, after)
		}
	}
	// op9：mixed 档一个字节都不改（owner 仍是副本场景上下文 0 ⇒ 身份块保住）。
	if !bytes.Equal(zero.op9, mixed.op9) {
		t.Fatalf("mixed changed the op9 record\nzero =% X\nmixed=% X", zero.op9, mixed.op9)
	}
	if mixed.op9[7] != currentSceneObjectContext {
		t.Fatalf("mixed op9 owner byte=%d want %d", mixed.op9[7], currentSceneObjectContext)
	}
	rows, leaderSlot := parseOp9PartyFrame(t, mixed.op9, true)
	if len(rows) != 2 || rows[0].uid != 99 || rows[1].uid != 100 {
		t.Fatalf("mixed op9 rows=%+v want leader 99 at slot 0", rows)
	}
	for index, row := range rows {
		if !row.hasIdentity || row.name == "" {
			t.Fatalf("mixed op9 row[%d]=%+v lost the inline identity block (member_identity_block must stay true)", index, row)
		}
	}
	if leaderSlot != 0 || rows[leaderSlot].uid != 99 {
		t.Fatalf("mixed op9 leader slot=%d (uid=%d) want slot 0 = leader 99", leaderSlot, rows[leaderSlot].uid)
	}
}

// 开关只覆盖"发给其他队员"的包：本地角色自己那条 op9 记录与 op153 在三档下
// 逐字节不变（本地在副本内 owner=0 本来就显示正常，不能动）。
func TestDungeonPartyOwnerModeChannelKeepsLocalPacketsUnchanged(t *testing.T) {
	const channelID = byte(121)

	zeroService, zeroLeader, zeroFollower, state := partyDungeonOwnerSwitchFixture(t, partyDungeonOwnerSceneContext, false)
	zeroWire := captureDungeonPartyFrameRosterWire(t, zeroService, state, zeroLeader, zeroFollower)

	for _, mode := range []partyDungeonOwnerMode{partyDungeonOwnerClientChannel, partyDungeonOwnerMixed} {
		t.Run(mode.String(), func(t *testing.T) {
			service, leader, follower, state := partyDungeonOwnerSwitchFixture(t, mode, false)
			wire := captureDungeonPartyFrameRosterWire(t, service, state, leader, follower)
			// 该档下队友记录应该写出的 owner（mixed 档 op9 仍是 0）。
			wantPeerOwner := byte(channelID)
			if mode == partyDungeonOwnerMixed {
				wantPeerOwner = currentSceneObjectContext
			}
			for _, characterID := range []uint16{99, 100} {
				// op153（血条/槽位实时表）与开关无关。
				if !bytes.Equal(zeroWire[characterID].realtimeInfo, wire[characterID].realtimeInfo) {
					t.Fatalf("char %d op153 changed under %s mode\nzero=% X\nnow =% X",
						characterID, mode, zeroWire[characterID].realtimeInfo, wire[characterID].realtimeInfo)
				}
				peer := uint16(100)
				if characterID == 100 {
					peer = 99
				}
				// 自己的 op9 记录：逐字节不变，owner 仍是副本场景上下文 0。
				if !bytes.Equal(zeroWire[characterID].ownRecord, wire[characterID].ownRecord) {
					t.Fatalf("char %d own op9 record changed under %s mode\nzero=% X\nnow =% X",
						characterID, mode, zeroWire[characterID].ownRecord, wire[characterID].ownRecord)
				}
				if got := wire[characterID].ownRecord[7]; got != currentSceneObjectContext {
					t.Fatalf("char %d own op9 owner=%d want %d (local packet must not change)",
						characterID, got, currentSceneObjectContext)
				}
				// 队友的 op9 记录：owner = 该档要求的值。
				before := zeroWire[characterID].peerRecord
				after := wire[characterID].peerRecord
				if before[7] != currentSceneObjectContext || after[7] != wantPeerOwner {
					t.Fatalf("char %d peer op9 owner=%d -> %d want %d -> %d",
						characterID, before[7], after[7], currentSceneObjectContext, wantPeerOwner)
				}
				if after[4] != byte(peer) || before[4] != byte(peer) {
					t.Fatalf("char %d peer record object key=%d -> %d want %d",
						characterID, before[4], after[4], peer)
				}
				// mixed 档队友记录与 zero 档逐字节相同（身份块保住）；channel 档走同频道分支。
				if mode == partyDungeonOwnerMixed {
					if !bytes.Equal(before, after) {
						t.Fatalf("char %d peer op9 changed under mixed mode\nzero=% X\nmixed=% X", characterID, before, after)
					}
					continue
				}
				afterRows, _ := parseOp9PartyFrame(t, after, false)
				beforeRows, _ := parseOp9PartyFrame(t, before, true)
				if len(afterRows) != len(beforeRows) {
					t.Fatalf("char %d peer row count %d -> %d", characterID, len(beforeRows), len(afterRows))
				}
				for index := range afterRows {
					if afterRows[index].slot != beforeRows[index].slot || afterRows[index].uid != beforeRows[index].uid {
						t.Fatalf("char %d peer row[%d]=%+v changed vs %+v", characterID, index, afterRows[index], beforeRows[index])
					}
				}
			}
		})
	}
	if got := currentClientSelectedChannel(zeroLeader); got != channelID {
		t.Fatalf("client selected channel=%d want=%d", got, channelID)
	}
}

// 三档各自的 owner 计划：zero = 两个 0；channel = 两个频道号；mixed = op2 频道号 + op9 0。
// 收包者频道还没提交（0）时三档都必须回退 0 = 现行为，绝不写出"半个覆盖"。
func TestPartyDungeonPeerOwnerPlanPerModeAndFallback(t *testing.T) {
	const channelID = byte(121)
	for _, test := range []struct {
		mode      partyDungeonOwnerMode
		wantLabel string
		wantOp2   byte
		wantOp9   byte
	}{
		{mode: partyDungeonOwnerSceneContext, wantLabel: partyDungeonOwnerModeValueZero,
			wantOp2: currentSceneObjectContext, wantOp9: currentSceneObjectContext},
		{mode: partyDungeonOwnerClientChannel, wantLabel: partyDungeonOwnerModeValueChannel,
			wantOp2: channelID, wantOp9: channelID},
		{mode: partyDungeonOwnerMixed, wantLabel: partyDungeonOwnerModeValueMixed,
			wantOp2: channelID, wantOp9: currentSceneObjectContext},
	} {
		t.Run(test.wantLabel, func(t *testing.T) {
			service, _, follower, _ := partyDungeonOwnerSwitchFixture(t, test.mode, false)
			plan := service.partyDungeonPeerOwnerPlan(follower)
			if plan.Mode != test.wantLabel || plan.ActorOwner != test.wantOp2 || plan.PartyFrameOwner != test.wantOp9 {
				t.Fatalf("plan=%+v want mode=%s op2=%d op9=%d", plan, test.wantLabel, test.wantOp2, test.wantOp9)
			}
			wantOverride := test.mode != partyDungeonOwnerSceneContext
			if plan.overridesPeerOwner() != wantOverride {
				t.Fatalf("overrides=%t want=%t", plan.overridesPeerOwner(), wantOverride)
			}
			// 客户端频道未提交：任何档位都回退 0。
			follower.channel.ID = 0
			follower.connectionTownActorOwnerChannel = 0
			fallback := service.partyDungeonPeerOwnerPlan(follower)
			if fallback.ActorOwner != currentSceneObjectContext || fallback.PartyFrameOwner != currentSceneObjectContext ||
				fallback.overridesPeerOwner() {
				t.Fatalf("uncommitted channel plan=%+v want both owners 0", fallback)
			}
		})
	}
	// nil 安全：探针与测试里的裸 Service 不会 panic，且语义 = zero 档。
	_, _, follower, _ := partyDungeonOwnerSwitchFixture(t, partyDungeonOwnerMixed, false)
	plan := (*Service)(nil).partyDungeonPeerOwnerPlan(follower)
	if plan.Mode != partyDungeonOwnerModeValueZero || plan.overridesPeerOwner() {
		t.Fatalf("nil service plan=%+v want zero mode without override", plan)
	}
}

// countOwnerOverrideEvents 数"同时带全部给定字段"的单次事件行数。
func countOwnerOverrideEvents(text string, fields ...string) int {
	count := 0
	for _, line := range strings.Split(text, "\n") {
		if !strings.Contains(line, "kind=game-party-dungeon-owner-override") {
			continue
		}
		matched := true
		for _, field := range fields {
			if !strings.Contains(line, field) {
				matched = false
				break
			}
		}
		if matched {
			count++
		}
	}
	return count
}

// 开关生效时发送处必须记**单次**事件（按 conn + 队友 key 去重，不每包刷屏）；
// 默认 zero（以及 legacy options）时这条事件一次都不出现。
func TestDungeonPartyOwnerOverrideLogsOncePerPeer(t *testing.T) {
	overrideEvents := func(t *testing.T, mode partyDungeonOwnerMode, legacy bool) string {
		t.Helper()
		logPath := filepath.Join(t.TempDir(), "packet.log")
		logger, err := openPacketLogger(logPath)
		if err != nil {
			t.Fatal(err)
		}
		service, leader, follower, _ := partyDungeonOwnerSwitchFixture(t, mode, legacy)
		service.packetLog = logger
		// 三次发送：进图落位那一次 + 队伍结构变化补发那一次（两次投影同一个队友），
		// 再加一次副本内队伍帧补发（两个方向各一条记录）。
		for index, source := range []string{"test_owner_override_first", "test_owner_override_second"} {
			if err := service.sendDungeonPartyPeerActorState(follower, leader, source); err != nil {
				t.Fatalf("peer actor state #%d: %v", index, err)
			}
		}
		if delivered := service.sendDungeonPartyFrameRoster(runtimePartyStateSnapshot(leader), "test_owner_override_roster"); delivered != 4 {
			t.Fatalf("roster resend delivered=%d want 4", delivered)
		}
		if err := logger.Close(); err != nil {
			t.Fatal(err)
		}
		logged, err := os.ReadFile(logPath)
		if err != nil {
			t.Fatal(err)
		}
		return string(logged)
	}

	// channel 档：两个 owner 都是频道号。
	channelText := overrideEvents(t, partyDungeonOwnerClientChannel, false)
	// 队友 -> 本人（follower 连接上的 peer 99）：两次投影 + 队伍帧补发里那一条，
	// 共 3 次发送，必须只有一条事件。
	if got := countOwnerOverrideEvents(channelText, "receiver_char_id=100", "peer_key=99"); got != 1 {
		t.Fatalf("follower-side owner override events=%d want 1 (dedup per conn+peer)\n%s", got, channelText)
	}
	// 反向（leader 连接上的 peer 100）：只有队伍帧补发会走到，同样只有一条。
	if got := countOwnerOverrideEvents(channelText, "receiver_char_id=99", "peer_key=100"); got != 1 {
		t.Fatalf("leader-side owner override events=%d want 1\n%s", got, channelText)
	}
	line := ""
	for _, candidate := range strings.Split(channelText, "\n") {
		if strings.Contains(candidate, "kind=game-party-dungeon-owner-override") &&
			strings.Contains(candidate, "receiver_char_id=100") {
			line = candidate
			break
		}
	}
	for _, field := range []string{
		"mode=channel",
		"owner=121",
		"peer_key=99",
		"peer_char_id=99",
		"receiver_char_id=100",
		"local_channel=121",
		"op2_owner_channel=121",
		"op9_owner_channel=121",
		"env=" + partyDungeonOwnerModeEnv,
	} {
		if !strings.Contains(line, field) {
			t.Fatalf("channel owner override log is missing %q\nline=%s", field, line)
		}
	}
	// 发送处日志同样要能一眼判档位：op2 那条带档位 + 两个实际 owner 值。
	for _, field := range []string{
		"kind=game-dungeon-party-peer-actor-send",
		"owner_mode=channel",
		"owner_channel=121",
		"mode=channel",
		"op2_owner_channel=121",
		"op9_owner_channel=121",
	} {
		if !strings.Contains(channelText, field) {
			t.Fatalf("channel op2 send log is missing %q", field)
		}
	}
	// op9 发送处：档位 + 两个 owner 值 + 身份块状态。
	for _, field := range []string{
		"kind=game-current-party-frame-op9-send",
		"owner_switch=channel",
		"owner_source=explicit_owner",
		"owner_channel=121",
		"op2_owner_channel=121",
		"member_identity_block=false",
	} {
		if !strings.Contains(channelText, field) {
			t.Fatalf("channel op9 send log is missing %q", field)
		}
	}

	// mixed 档：op2 = 频道号、op9 = 0；身份块必须保住（member_identity_block=true）。
	mixedText := overrideEvents(t, partyDungeonOwnerMixed, false)
	if got := countOwnerOverrideEvents(mixedText, "receiver_char_id=100", "peer_key=99"); got != 1 {
		t.Fatalf("mixed follower-side owner override events=%d want 1\n%s", got, mixedText)
	}
	for _, field := range []string{
		"mode=mixed",
		"owner=121", // owner= 取"这一档实际改掉的第一个非零值" = op2 的频道号
		"op2_owner_channel=121",
		"op9_owner_channel=0",
	} {
		if !strings.Contains(mixedText, field) {
			t.Fatalf("mixed owner override log is missing %q", field)
		}
	}
	for _, field := range []string{
		"kind=game-dungeon-party-peer-actor-send",
		"owner_mode=mixed",
		"owner_channel=121",
		"op2_owner_channel=121",
		"op9_owner_channel=0",
		"kind=game-current-party-frame-op9-send",
		"owner_switch=mixed",
		"owner_source=explicit_owner",
		"owner_channel=0",
		"op2_owner_channel=121",
		"member_identity_block=true",
	} {
		if !strings.Contains(mixedText, field) {
			t.Fatalf("mixed send log is missing %q", field)
		}
	}
	// 默认（显式 zero 与 legacy options）时这条事件一次都不出现，且发送处日志不带开关字段。
	for name, legacy := range map[string]bool{"explicit_zero": false, "legacy_options": true} {
		zeroText := overrideEvents(t, partyDungeonOwnerSceneContext, legacy)
		if got := countOwnerOverrideEvents(zeroText); got != 0 {
			t.Fatalf("%s logged %d owner override events\n%s", name, got, zeroText)
		}
		for _, field := range []string{
			"owner_mode=dungeon_context",
			"member_identity_block=true",
			"owner_channel=0",
		} {
			if !strings.Contains(zeroText, field) {
				t.Fatalf("%s send log lost %q (default path must stay unchanged)", name, field)
			}
		}
		if strings.Contains(zeroText, "owner_switch=") {
			t.Fatalf("%s emitted the switch log field on the default path\n%s", name, zeroText)
		}
	}
}

type dungeonRosterWire struct {
	realtimeInfo []byte
	ownRecord    []byte
	peerRecord   []byte
}

// captureDungeonPartyFrameRosterWire 走一次"队伍结构变化后的副本内补发"（op153 + 每个成员
// 一条 op9），按"记录描述的 object key"把 op9 分成自己的与队友的。
func captureDungeonPartyFrameRosterWire(t *testing.T, service *Service, state alignedcmd.PartyState, leader, follower *gameSession) map[uint16]dungeonRosterWire {
	t.Helper()
	leaderConn := leader.conn.(*bufferConn)
	followerConn := follower.conn.(*bufferConn)
	leaderConn.write.Reset()
	followerConn.write.Reset()

	if delivered := service.sendDungeonPartyFrameRoster(state, "test_owner_switch_roster"); delivered != 4 {
		t.Fatalf("delivered=%d want 2 sessions x 2 op9 records", delivered)
	}
	wire := make(map[uint16]dungeonRosterWire, 2)
	for characterID, conn := range map[uint16]*bufferConn{99: leaderConn, 100: followerConn} {
		entry := dungeonRosterWire{}
		for _, frame := range splitUpperFrames(t, conn.write.Bytes()) {
			switch frame.msgID {
			case 0x0099:
				if entry.realtimeInfo != nil {
					t.Fatalf("char %d received two op153 frames", characterID)
				}
				entry.realtimeInfo = frame.body
			case uint16(dnfenum.CmdPacketRecoverStamina):
				if len(frame.body) < 10 || frame.body[9] != currentSceneOp9ActorDisplayKind {
					continue
				}
				described := binary.LittleEndian.Uint16(frame.body[4:6])
				switch described {
				case characterID:
					entry.ownRecord = frame.body
				case 99, 100:
					entry.peerRecord = frame.body
				default:
					t.Fatalf("char %d op9 record describes unknown object key=%d", characterID, described)
				}
			}
		}
		if entry.realtimeInfo == nil || entry.ownRecord == nil || entry.peerRecord == nil {
			t.Fatalf("char %d roster wire incomplete: op153=%t own=%t peer=%t",
				characterID, entry.realtimeInfo != nil, entry.ownRecord != nil, entry.peerRecord != nil)
		}
		wire[characterID] = entry
	}
	return wire
}
