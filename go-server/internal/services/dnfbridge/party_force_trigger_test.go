package dnfbridge

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/channelcatalog"
)

// 本文件钉住"外部可触发的强制组队"（party_force_trigger.go）：
//   - 两个**在线且权威**的会话 → 双方都拿到完整名册（这是"看不到对方动"排查的前置条件）；
//   - 任一端不在线/不是权威会话 → 具名拒绝，且**一个字节都不发**；
//   - 开关 off（默认）→ 不启动循环、不消费命令文件、不写结果文件、不发包。
//
// 脚手架与 party_member_leave_test.go 同构（bufferConn + bindGameSessionCharacter）。

type partyForceHarness struct {
	service     *Service
	leader      *gameSession
	member      *gameSession
	leaderConn  *bufferConn
	memberConn  *bufferConn
	commandPath string
	resultPath  string
}

func newPartyForceHarness(t *testing.T) *partyForceHarness {
	t.Helper()
	dir := t.TempDir()
	commandPath := filepath.Join(dir, "force_party.cmd")
	resultPath := filepath.Join(dir, "force_party.result")
	service := &Service{options: options{
		gameUpperBodyCodec:    gameUpperBodyCodecPlain,
		partyForce:            partyForceOn,
		partyForceCommandPath: commandPath,
		partyForceResultPath:  resultPath,
		partyForceInterval:    50 * time.Millisecond,
	}}
	channel := channelcatalog.Channel{ID: 38, Type: 1, Name: "ch.38", Port: 10038}
	leaderConn, memberConn := &bufferConn{}, &bufferConn{}
	leader := &gameSession{conn: leaderConn, channel: channel, selectedCharacterID: 1001}
	member := &gameSession{conn: memberConn, channel: channel, selectedCharacterID: 1002}
	service.bindGameSessionCharacter(leader, 1001)
	service.bindGameSessionCharacter(member, 1002)
	return &partyForceHarness{
		service:     service,
		leader:      leader,
		member:      member,
		leaderConn:  leaderConn,
		memberConn:  memberConn,
		commandPath: commandPath,
		resultPath:  resultPath,
	}
}

func (h *partyForceHarness) writeCommand(t *testing.T, text string) {
	t.Helper()
	if err := os.WriteFile(h.commandPath, []byte(text), 0o644); err != nil {
		t.Fatalf("write command file: %v", err)
	}
}

func (h *partyForceHarness) readResult(t *testing.T) string {
	t.Helper()
	raw, err := os.ReadFile(h.resultPath)
	if err != nil {
		t.Fatalf("read result file: %v", err)
	}
	return strings.TrimSpace(string(raw))
}

// waitResult 等结果文件出现（轮询循环用例用）。
func (h *partyForceHarness) waitResult(t *testing.T) string {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(h.resultPath); err == nil {
			return h.readResult(t)
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("result file never appeared")
	return ""
}

// TestPartyForceTriggerJoinsTwoOnlineSessions 是核心用例：两个在线权威会话必须被组成同一队，
// 且**两端都收到名册**（op11/op9/op153），客户端投影的 party id 也要跟着变。
func TestPartyForceTriggerJoinsTwoOnlineSessions(t *testing.T) {
	h := newPartyForceHarness(t)
	h.writeCommand(t, "1001 1002\n")

	h.service.pollPartyForceCommand(h.commandPath, h.resultPath)

	leaderIdentity, ok := h.service.boundGameSessionCharacterSnapshot(h.leader)
	if !ok {
		t.Fatal("leader session is not authoritative")
	}
	memberIdentity, ok := h.service.boundGameSessionCharacterSnapshot(h.member)
	if !ok {
		t.Fatal("member session is not authoritative")
	}
	manager := h.service.runtimePartyManagerForService()
	leaderSnapshot, leaderFound := manager.SnapshotByUser(1001, leaderIdentity.generation)
	memberSnapshot, memberFound := manager.SnapshotByUser(1002, memberIdentity.generation)
	if !leaderFound || !memberFound {
		t.Fatalf("force party did not put both characters in one party: leader=%t member=%t", leaderFound, memberFound)
	}
	if leaderSnapshot.ID != memberSnapshot.ID || len(leaderSnapshot.Members) != 2 {
		t.Fatalf("party id/members mismatch: leader=%d member=%d members=%d",
			leaderSnapshot.ID, memberSnapshot.ID, len(leaderSnapshot.Members))
	}
	if leaderSnapshot.Leader != 1001 {
		t.Fatalf("party leader = %d, want 1001", leaderSnapshot.Leader)
	}

	// 双方都必须收到包：邀请路径的等价物是"两端各自拿到完整名册"。
	if h.leaderConn.write.Len() == 0 {
		t.Fatal("leader client received nothing after the forced party")
	}
	if h.memberConn.write.Len() == 0 {
		t.Fatal("member client received nothing after the forced party")
	}
	if state := runtimePartyStateSnapshot(h.leader); state.PartyID != int(leaderSnapshot.ID) {
		t.Fatalf("leader projection party id = %d, want %d", state.PartyID, leaderSnapshot.ID)
	}
	if state := runtimePartyStateSnapshot(h.member); state.PartyID != int(memberSnapshot.ID) {
		t.Fatalf("member projection party id = %d, want %d", state.PartyID, memberSnapshot.ID)
	}

	result := h.readResult(t)
	if !strings.HasPrefix(result, "ok party_id=") {
		t.Fatalf("result line = %q, want `ok party_id=...`", result)
	}
	if !strings.Contains(result, "leader=1001") || !strings.Contains(result, "member=1002") ||
		!strings.Contains(result, "member_count=2") {
		t.Fatalf("result line = %q, want leader/member/member_count fields", result)
	}
	if _, err := os.Stat(h.commandPath); !os.IsNotExist(err) {
		t.Fatalf("command file must be consumed, stat err = %v", err)
	}
	// 名册形态要与邀请路径一致：两端各自都要拿到 op11 端点行（msg 11）与 op9 名册（msg 9），
	// 否则"服务端组了队但客户端不知道队友在哪"——那正是现场"看不到对方动"的形态。
	for name, conn := range map[string]*bufferConn{"leader": h.leaderConn, "member": h.memberConn} {
		ids := upperMsgIDs(t, conn.write.Bytes())
		if !containsMsgID(ids, 11) {
			t.Fatalf("%s packet stream %v is missing the op11 peer-endpoint rows", name, ids)
		}
		if !containsMsgID(ids, 9) {
			t.Fatalf("%s packet stream %v is missing the op9 roster frame", name, ids)
		}
	}
}

// upperMsgIDs 把一条 conn 上收到的服务端包流拆成 msg id 列表（测试断言用）。
func upperMsgIDs(t *testing.T, raw []byte) []uint16 {
	t.Helper()
	var ids []uint16
	rest := raw
	for len(rest) > 0 {
		packet, remain := splitGameServerUpperPacket(t, rest)
		ids = append(ids, packet.Header.MsgID)
		if len(remain) >= len(rest) {
			break
		}
		rest = remain
	}
	return ids
}

func containsMsgID(ids []uint16, want uint16) bool {
	for _, id := range ids {
		if id == want {
			return true
		}
	}
	return false
}

// TestPartyForceTriggerLoopConsumesCommandFile 覆盖真正上线的那条路：开关 on 时轮询循环
// 自己发现命令文件并执行（不需要外部调用 pollPartyForceCommand）。
func TestPartyForceTriggerLoopConsumesCommandFile(t *testing.T) {
	h := newPartyForceHarness(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	h.service.startPartyForceTrigger(ctx)
	h.writeCommand(t, "1001 1002\n")

	result := h.waitResult(t)
	if !strings.HasPrefix(result, "ok ") {
		t.Fatalf("loop result = %q, want ok", result)
	}
	if snapshot, found := h.service.runtimePartyManagerForService().SnapshotByUser(1001, 0); !found ||
		len(snapshot.Members) != 2 {
		t.Fatalf("loop did not create the party: found=%t", found)
	}
}

// TestPartyForceTriggerRejectsOfflineMember 角色不在线时必须具名拒绝，并且不发包。
func TestPartyForceTriggerRejectsOfflineMember(t *testing.T) {
	h := newPartyForceHarness(t)
	// 让 member 掉线：直接从会话表里摘掉（同包测试可直接触碰私有状态）。
	h.service.mu.Lock()
	delete(h.service.gameSessions, 1002)
	h.service.mu.Unlock()
	h.writeCommand(t, "1001 1002\n")

	h.service.pollPartyForceCommand(h.commandPath, h.resultPath)

	result := h.readResult(t)
	if !strings.Contains(result, "rejected reason="+partyForceReasonMemberNotOnline) {
		t.Fatalf("result line = %q, want rejection %s", result, partyForceReasonMemberNotOnline)
	}
	if h.leaderConn.write.Len() != 0 {
		t.Fatal("a rejected force party must not send anything to the leader")
	}
	if manager := h.service.runtimePartyManagerForService(); len(manager.Snapshots()) != 0 {
		t.Fatalf("no runtime party may exist after a rejection: %d", len(manager.Snapshots()))
	}
}

// TestPartyForceTriggerRejectsStaleMemberSession 会话不是该角色的权威会话时必须拒绝：
// 绝不"猜"一个退役 socket 去改队伍状态。
func TestPartyForceTriggerRejectsStaleMemberSession(t *testing.T) {
	h := newPartyForceHarness(t)
	// 让 member 的代际前进，但会话表里保留的是"旧代际"的那条记录：
	// 直接把表项换成一个未绑定的会话即可模拟"当前权威会话不是它"。
	h.service.mu.Lock()
	h.service.gameSessions[1002] = &gameSession{
		conn:                h.memberConn,
		channel:             h.member.channel,
		selectedCharacterID: 0,
	}
	h.service.mu.Unlock()
	h.writeCommand(t, "1001 1002\n")

	h.service.pollPartyForceCommand(h.commandPath, h.resultPath)

	result := h.readResult(t)
	if !strings.Contains(result, "rejected reason="+partyForceReasonMemberNotAuthoritative) {
		t.Fatalf("result line = %q, want rejection %s", result, partyForceReasonMemberNotAuthoritative)
	}
	if h.leaderConn.write.Len() != 0 || h.memberConn.write.Len() != 0 {
		t.Fatal("a rejected force party must not send anything")
	}
}

// TestPartyForceTriggerRejectsSameCharacter 同一角色不能和自己组队。
func TestPartyForceTriggerRejectsSameCharacter(t *testing.T) {
	h := newPartyForceHarness(t)
	h.writeCommand(t, "1001 1001\n")

	h.service.pollPartyForceCommand(h.commandPath, h.resultPath)

	result := h.readResult(t)
	if !strings.Contains(result, "rejected reason="+partyForceReasonSameCharacter) {
		t.Fatalf("result line = %q, want rejection %s", result, partyForceReasonSameCharacter)
	}
}

// TestPartyForceTriggerRejectsBadCommand 命令格式错误必须具名拒绝（不是静默吞掉）。
func TestPartyForceTriggerRejectsBadCommand(t *testing.T) {
	h := newPartyForceHarness(t)
	h.writeCommand(t, "hello world\n")

	h.service.pollPartyForceCommand(h.commandPath, h.resultPath)

	result := h.readResult(t)
	if !strings.Contains(result, "rejected reason="+partyForceReasonBadCommand) {
		t.Fatalf("result line = %q, want rejection %s", result, partyForceReasonBadCommand)
	}
	if h.leaderConn.write.Len() != 0 {
		t.Fatal("a malformed command must not send anything")
	}
}

// TestPartyForceTriggerDisabledDoesNothing 是本改动最重要的一条边界：
// **默认 off 档下不启动循环、不消费命令文件、不写结果文件、不发一个字节**。
func TestPartyForceTriggerDisabledDoesNothing(t *testing.T) {
	h := newPartyForceHarness(t)
	h.service.options.partyForce = partyForceOff
	h.writeCommand(t, "1001 1002\n")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	h.service.startPartyForceTrigger(ctx)
	time.Sleep(250 * time.Millisecond)

	if _, err := os.Stat(h.commandPath); err != nil {
		t.Fatalf("command file was consumed while the trigger is disabled: %v", err)
	}
	if _, err := os.Stat(h.resultPath); !os.IsNotExist(err) {
		t.Fatalf("result file was written while the trigger is disabled (stat err=%v)", err)
	}
	if h.leaderConn.write.Len() != 0 || h.memberConn.write.Len() != 0 {
		t.Fatal("packets were sent while the trigger is disabled")
	}
	if manager := h.service.runtimePartyManagerForService(); len(manager.Snapshots()) != 0 {
		t.Fatalf("a runtime party was created while the trigger is disabled: %d", len(manager.Snapshots()))
	}
}

// TestPartyForceModeAndConfigParsing 钉住开关与配置解析（默认值 + 非法值回退）。
func TestPartyForceModeAndConfigParsing(t *testing.T) {
	if readPartyForceMode("") != partyForceOff {
		t.Fatal("empty value must default to off")
	}
	if readPartyForceMode("off") != partyForceOff {
		t.Fatal("`off` must stay off")
	}
	for _, value := range []string{"on", "ON", " on ", "true", "1", "yes"} {
		if readPartyForceMode(value) != partyForceOn {
			t.Fatalf("value %q must enable the trigger", value)
		}
	}
	if interval := partyForceIntervalFromMS(0); interval != defaultPartyForceIntervalMS*time.Millisecond {
		t.Fatalf("interval for 0 = %s, want default", interval)
	}
	if interval := partyForceIntervalFromMS(50); interval != defaultPartyForceIntervalMS*time.Millisecond {
		t.Fatalf("interval below the floor = %s, want default", interval)
	}
	if interval := partyForceIntervalFromMS(250); interval != 250*time.Millisecond {
		t.Fatalf("interval = %s, want 250ms", interval)
	}
}
