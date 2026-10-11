package dnfbridge

import (
	"encoding/binary"
	"testing"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
)

// 钉住"给同屏 newcomer 投影哪一份队伍帧"这条口径（见 town_copresence_party_state.go）：
//
//	off（默认）= 会话本地缓存 —— 现场形状下它经常是空的（party_id=0）；
//	on         = 权威队伍状态（中央 RuntimePartyManager），并按既有语义写回会话缓存。
func TestTownCopresenceActorPartyStateUsesAuthoritativeRoster(t *testing.T) {
	harness := newPartyForceHarness(t)
	if ok, partyID, reason, memberCount := harness.service.forceManagedRuntimeParty(1001, 1002); !ok {
		t.Fatalf("force party failed: reason=%s party_id=%d members=%d", reason, partyID, memberCount)
	} else if memberCount != 2 {
		t.Fatalf("authoritative party members=%d, want 2", memberCount)
	}

	// 现场形状：被投影 actor 那条会话的本地缓存是空的
	// （同角色的第二条连接 / 通道重连的替换 socket 读不到权威名单）。
	storeRuntimePartyState(harness.member, alignedcmd.PartyState{})
	if cached := runtimePartyStateSnapshot(harness.member); cached.PartyID != 0 {
		t.Fatalf("fixture session cache = party_id=%d, want the empty-cache field shape", cached.PartyID)
	}

	t.Setenv(townCopresencePartyAuthoritativeEnv, "off")
	if state := harness.service.townCopresenceActorPartyState(harness.member); state.PartyID != 0 {
		t.Fatalf("off must keep the session cache projection, got party_id=%d", state.PartyID)
	}

	t.Setenv(townCopresencePartyAuthoritativeEnv, "on")
	state := harness.service.townCopresenceActorPartyState(harness.member)
	if state.PartyID <= 0 {
		t.Fatalf("authoritative projection party_id=%d, want the live party", state.PartyID)
	}
	if members := len(runtimePartyMembers(state)); members != 2 {
		t.Fatalf("authoritative projection members=%d, want 2", members)
	}
	if cached := runtimePartyStateSnapshot(harness.member); cached.PartyID != state.PartyID {
		t.Fatalf("authoritative state must be written back to the session cache: cached=%d authoritative=%d",
			cached.PartyID, state.PartyID)
	}
}

func TestOfficialTownPeerMenuReadsManagedPartyAfterTargetCacheWasLost(t *testing.T) {
	h := newPartyForceHarness(t)
	h.service.options.currentClientVersion = "official-24.9"
	h.service.onlinePlayers = newOnlinePlayerManager()
	for _, session := range []*gameSession{h.leader, h.member} {
		h.service.onlinePlayers.EnterArea(&onlinePlayerInfo{CharacterID: session.selectedCharacterID, ChannelID: session.channel.ID, TownID: 31, AreaID: 2, Session: session})
	}
	if ok, _, reason, _ := h.service.forceManagedRuntimeParty(1001, 1002); !ok {
		t.Fatal(reason)
	}
	storeRuntimePartyState(h.member, alignedcmd.PartyState{})
	h.leaderConn.write.Reset()
	body := make([]byte, 13)
	binary.LittleEndian.PutUint16(body, 1002)
	body[2] = 15
	if err := h.service.handleOnlineRequestPeer(h.leader, 10, body); err != nil {
		t.Fatal(err)
	}
	packets := splitAllGameServerUpperPackets(t, h.leaderConn.write.Bytes())
	if len(packets) != 3 || packets[0].Header.MsgID != 10 || packets[1].Header.MsgID != 9 || packets[2].Header.MsgID != 7 {
		t.Fatalf("unexpected menu sequence: %+v", packets)
	}
	last := packets[2].Body
	if len(last) != 9 || binary.LittleEndian.Uint16(last[7:]) != 0 {
		t.Fatalf("live managed party was advertised as absent: %x", last)
	}
	if len(packets[1].Body) <= 10 {
		t.Fatal("live roster was replaced by an actor party-clear")
	}
	if runtimePartyStateSnapshot(h.member).PartyID == 0 {
		t.Fatal("target cache was not restored from the manager")
	}
}

// 开关取值口径：只有显式 on/1/true/yes/authoritative 才打开。
func TestTownCopresencePartyAuthoritativeSwitchValues(t *testing.T) {
	for _, value := range []string{"", "off", "0", "false", "no", "legacy", "session"} {
		t.Setenv(townCopresencePartyAuthoritativeEnv, value)
		if townCopresencePartyAuthoritativeEnabled() {
			t.Fatalf("value %q must keep the legacy session-cache behavior", value)
		}
	}
	for _, value := range []string{"on", "ON", "1", "true", "yes", "authoritative"} {
		t.Setenv(townCopresencePartyAuthoritativeEnv, value)
		if !townCopresencePartyAuthoritativeEnabled() {
			t.Fatalf("value %q must enable the authoritative projection", value)
		}
	}
}

// 钉住 p14/p15 结论对应的跳过规则：**双方同队时**那条"再声明一次队伍"的 op9 可以跳过；
// 单方不在队伍里（或不同队）时照旧发送。
func TestTownCopresenceSkipsRedundantPartyFrameOnlyForSharedParty(t *testing.T) {
	harness := newPartyForceHarness(t)
	if ok, _, reason, _ := harness.service.forceManagedRuntimeParty(1001, 1002); !ok {
		t.Fatalf("force party failed: %s", reason)
	}

	t.Setenv(townCopresenceSkipSamePartyFrameEnv, "off")
	if harness.service.shouldSkipTownCopresencePartyFrame(harness.leader, harness.member) {
		t.Fatal("off must keep sending the frame (byte-for-byte legacy behavior)")
	}

	t.Setenv(townCopresenceSkipSamePartyFrameEnv, "on")
	if !harness.service.shouldSkipTownCopresencePartyFrame(harness.leader, harness.member) {
		t.Fatal("a shared authoritative party must skip the redundant re-declaration frame")
	}
	if !harness.service.shouldSkipTownCopresencePartyFrame(harness.member, harness.leader) {
		t.Fatal("the rule must be symmetric between observer and actor")
	}

	// 单方不在任何队伍里 ⇒ 不能跳过（那条"空状态清掉过期入队菜单"的原意必须保留）。
	storeRuntimePartyState(harness.member, alignedcmd.PartyState{})
	hollow := &gameSession{conn: &bufferConn{}, selectedCharacterID: 4242}
	if harness.service.shouldSkipTownCopresencePartyFrame(hollow, harness.member) {
		t.Fatal("an actor with no authoritative party must keep the legacy frame")
	}
}

func TestTownCopresenceSkipSamePartyFrameSwitchValues(t *testing.T) {
	// 默认 off：副本侧的同类投影是已被用例固化的行为，不能一刀切；本规则只能显式打开。
	for _, value := range []string{"", "off", "0", "false", "no", "legacy"} {
		t.Setenv(townCopresenceSkipSamePartyFrameEnv, value)
		if townCopresenceSkipSamePartyFrameEnabled() {
			t.Fatalf("value %q must keep sending the frame (default)", value)
		}
	}
	for _, value := range []string{"on", "ON", "1", "true", "yes", "skip"} {
		t.Setenv(townCopresenceSkipSamePartyFrameEnv, value)
		if !townCopresenceSkipSamePartyFrameEnabled() {
			t.Fatalf("value %q must enable the skip rule", value)
		}
	}
}

func TestOfficialTownCopresenceUsesLivePartyAndSuppressesOnlyRedundantPeerFrame(t *testing.T) {
	h := newPartyForceHarness(t)
	h.service.options.currentClientVersion = "official-24.9"
	t.Setenv(townCopresencePartyAuthoritativeEnv, "")
	t.Setenv(townCopresenceSkipSamePartyFrameEnv, "")
	if ok, _, reason, _ := h.service.forceManagedRuntimeParty(1001, 1002); !ok {
		t.Fatal(reason)
	}
	storeRuntimePartyState(h.member, alignedcmd.PartyState{})
	if state := h.service.townCopresenceActorPartyState(h.member); state.PartyID == 0 || len(runtimePartyMembers(state)) != 2 {
		t.Fatal("official peer projection lost the live roster after reconnect")
	}
	if !h.service.shouldSkipTownCopresencePartyFrame(h.leader, h.member) || !h.service.shouldSkipTownCopresencePartyFrame(h.member, h.leader) {
		t.Fatal("same-party peer frame must not redeclare the existing roster")
	}
	if h.service.shouldSkipTownCopresencePartyFrame(h.leader, h.leader) {
		t.Fatal("local actor state must still be sent")
	}
	outsider := &gameSession{selectedCharacterID: 4242, conn: &bufferConn{}}
	if h.service.shouldSkipTownCopresencePartyFrame(h.leader, outsider) || h.service.shouldSkipTownCopresencePartyFrame(outsider, h.leader) {
		t.Fatal("different-party and partyless peers must retain their state")
	}
	t.Setenv(townCopresenceSkipSamePartyFrameEnv, "off")
	if h.service.shouldSkipTownCopresencePartyFrame(h.leader, h.member) {
		t.Fatal("explicit rollback must preserve the old frame")
	}
	t.Setenv(townCopresencePartyAuthoritativeEnv, "off")
	storeRuntimePartyState(h.member, alignedcmd.PartyState{})
	if h.service.townCopresenceActorPartyState(h.member).PartyID != 0 {
		t.Fatal("explicit rollback must preserve session cache")
	}
}
