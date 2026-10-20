package dnfbridge

import (
	"bytes"
	"strings"
	"testing"
)

// 本文件钉住 M1（`DNFBRIDGE_PARTY_PEER_ENDPOINTS_AFTER_ROSTER`，对齐 A21 的 D6）：
//   - off（默认）= formation 里 op11 **恰好 1 次**；
//   - repeat = op11 **恰好 2 次**，第二次在 op9 之后，且与第一次**逐字节相同**。
//
// formation 的整体顺序由 `DNFBRIDGE_PARTY_FORMATION_ORDER` 决定（默认 vendor =
// op11 → op153 → op9；本文件的两条用例都显式写 `legacy`，因为它们是**回退档的逐字节钉子**：
// 只有 legacy 档才必须保持 `op153 → op11 → op9` 与旧的日志串）。
// vendor 两档的顺序钉子在新文件 `party_vendor_formation_alignment_test.go` 里。
//
// 现场对照：A21 `Network\Handlers\PartyHandler.cs:2531-2549`（roster 前）+ `:2571-2592`
// （roster 后）+ `:2571-2574` 的注释；厂商 9.16 的 phase1/phase2 实测见
// `party_formation_order.go` 文件头。

const (
	upperMsgRealtimeInfoFrameID = 0x0099
	upperMsgPeerEndpointsID     = 0x000b
	upperMsgOp9RosterID         = 0x0009
)

// frameMessageIDs 按发序返回帧的 msg_id。
func frameMessageIDs(frames []upperFrame) []uint16 {
	ids := make([]uint16, 0, len(frames))
	for _, frame := range frames {
		ids = append(ids, frame.msgID)
	}
	return ids
}

func peerEndpointFrameBodies(frames []upperFrame) [][]byte {
	bodies := make([][]byte, 0, 2)
	for _, frame := range frames {
		if frame.msgID == upperMsgPeerEndpointsID {
			bodies = append(bodies, frame.body)
		}
	}
	return bodies
}

func TestRuntimePartySnapshotOffSendsSingleEndpointFrameInProvenOrder(t *testing.T) {
	logger, logPath := newPeerEndpointTestLogger(t)
	service, leader, _, _, _ := changeHostFixture(t, 10, 10)
	service.packetLog = logger
	// 回退档：legacy = `op153 → op11 → op9`（逐字节现行为）。
	service.options.partyFormationOrder = partyFormationOrderLegacy
	// 回退档：include_self=on = 我们改动前的 op11 形状（含自己那一行）。
	service.options.partyPeerEndpointIncludeSelf = partyPeerEndpointIncludeSelfOn
	state := runtimePartyStateSnapshot(leader)
	if state.PartyID <= 0 || len(runtimePartyMembers(state)) != 2 {
		t.Fatalf("fixture party state = %+v", state)
	}
	writeFrames(t, leader)

	if err := service.sendRuntimePartySnapshot(leader, state); err != nil {
		t.Fatal(err)
	}
	frames := writeFrames(t, leader)
	ids := frameMessageIDs(frames)
	endpoints := peerEndpointFrameBodies(frames)
	if len(endpoints) != 1 {
		t.Fatalf("off mode sent %d op11 frames, want exactly one (ids=%v)", len(endpoints), ids)
	}
	if len(ids) < 3 ||
		ids[0] != upperMsgRealtimeInfoFrameID ||
		ids[1] != upperMsgPeerEndpointsID ||
		ids[2] != upperMsgOp9RosterID {
		t.Fatalf("legacy mode frame order = %v, want [0x99 0x0b 0x09 ...]", ids)
	}
	if len(endpoints[0]) == 0 {
		t.Fatal("off mode op11 body is empty")
	}
	logged := readPeerEndpointTestLog(t, logger, logPath)
	if !strings.Contains(logged, "ordering=op153_then_op11_before_op9") {
		t.Fatalf("legacy mode lost the proven op11 log string:\n%s", logged)
	}
	if strings.Contains(logged, "party peer endpoints re-applied after roster") {
		t.Fatalf("off mode must not log the M1 re-apply event:\n%s", logged)
	}
}

func TestRuntimePartySnapshotRepeatReappliesEndpointBytesAfterRoster(t *testing.T) {
	logger, logPath := newPeerEndpointTestLogger(t)
	service, leader, _, _, _ := changeHostFixture(t, 10, 10)
	service.packetLog = logger
	service.options.partyPeerEndpointsAfterRoster = partyPeerEndpointsAfterRosterRepeat
	// 回退档：legacy 顺序 + 含自己那一行 = 逐字节现行为。
	service.options.partyFormationOrder = partyFormationOrderLegacy
	service.options.partyPeerEndpointIncludeSelf = partyPeerEndpointIncludeSelfOn
	state := runtimePartyStateSnapshot(leader)
	if state.PartyID <= 0 || len(runtimePartyMembers(state)) != 2 {
		t.Fatalf("fixture party state = %+v", state)
	}
	writeFrames(t, leader)

	if err := service.sendRuntimePartySnapshot(leader, state); err != nil {
		t.Fatal(err)
	}
	frames := writeFrames(t, leader)
	ids := frameMessageIDs(frames)
	endpoints := peerEndpointFrameBodies(frames)
	if len(endpoints) != 2 {
		t.Fatalf("repeat mode sent %d op11 frames, want exactly two (ids=%v)", len(endpoints), ids)
	}
	if !bytes.Equal(endpoints[0], endpoints[1]) {
		t.Fatalf("repeat mode re-applied different bytes:\n first=% X\nsecond=% X", endpoints[0], endpoints[1])
	}
	// 第二次必须在 op9（名册）之后：A21 的原话是 "the final type-0 roster has now
	// created/replaced every member object"，所以要等 final roster 落地后再 reapply。
	firstOp9 := -1
	secondEndpoint := -1
	seenEndpoint := 0
	for index, id := range ids {
		switch id {
		case upperMsgOp9RosterID:
			if firstOp9 < 0 {
				firstOp9 = index
			}
		case upperMsgPeerEndpointsID:
			seenEndpoint++
			if seenEndpoint == 2 {
				secondEndpoint = index
			}
		}
	}
	if firstOp9 < 0 {
		t.Fatalf("repeat mode sent no op9 roster frame (ids=%v)", ids)
	}
	if secondEndpoint < 0 || secondEndpoint < firstOp9 {
		t.Fatalf("second op11 at %d must come after the first op9 at %d (ids=%v)", secondEndpoint, firstOp9, ids)
	}
	// 顺序钉子（legacy 档）：0x99 → op11 → op9 → op11。
	if ids[0] != upperMsgRealtimeInfoFrameID || ids[1] != upperMsgPeerEndpointsID {
		t.Fatalf("repeat mode frame order = %v, want [0x99 0x0b 0x09 0x0b ...]", ids)
	}
	// 判读串钉子：repeat 档必须留下这一行，且 `body_matches_bootstrap=true`
	// （第二次重建的 body 与 roster 前那份逐字节相同）。
	logged := readPeerEndpointTestLog(t, logger, logPath)
	if !strings.Contains(logged, "party peer endpoints re-applied after roster") {
		t.Fatalf("repeat mode did not log the M1 re-apply event:\n%s", logged)
	}
	if !strings.Contains(logged, "body_matches_bootstrap=true") {
		t.Fatalf("repeat mode body is not byte-identical to the bootstrap body:\n%s", logged)
	}
	if !strings.Contains(logged, "ordering=op153_then_op11_before_op9_then_op11_after_op9") {
		t.Fatalf("legacy repeat mode log is missing the ordering marker:\n%s", logged)
	}
}
