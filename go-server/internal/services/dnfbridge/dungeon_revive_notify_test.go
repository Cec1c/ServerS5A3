package dnfbridge

import (
	"encoding/binary"
	"testing"

	"longheng.io/server/internal/modules/dnf/dnfenum"
	dnfproto "longheng.io/server/internal/modules/dnf/protocol"
)

// 开关归一化：本条的零值是**修复档**，只有显式 `legacy` 才回退
// （与 DNFBRIDGE_HELL_ROOM 同一约定，与 HELL_BLOCKED_SLOTS 相反方向）。
func TestReadReviveStateNotifyModeDefaultsToFixAndOnlyLegacyRollsBack(t *testing.T) {
	tests := []struct {
		raw  string
		want reviveStateNotifyMode
	}{
		{raw: "", want: reviveStateNotifyVendor},
		{raw: "   ", want: reviveStateNotifyVendor},
		{raw: "vendor", want: reviveStateNotifyVendor},
		{raw: "on", want: reviveStateNotifyVendor},
		{raw: "1", want: reviveStateNotifyVendor},
		{raw: "junk", want: reviveStateNotifyVendor},
		{raw: "legacy", want: reviveStateNotifyLegacy},
		{raw: " LEGACY ", want: reviveStateNotifyLegacy},
	}
	for _, test := range tests {
		if got := readReviveStateNotifyMode(test.raw); got != test.want {
			t.Fatalf("readReviveStateNotifyMode(%q)=%v want=%v", test.raw, got, test.want)
		}
	}
	if !readReviveStateNotifyMode("").sends() || readReviveStateNotifyMode("legacy").sends() {
		t.Fatal("sends() must be true only for the fix档")
	}
	if readReviveStateNotifyMode("").String() != "vendor" ||
		readReviveStateNotifyMode("legacy").String() != "legacy" {
		t.Fatal("String() mismatch")
	}
}

// op32 的两种形态同布局，只差中间那个字节：死亡 = 0，复活 = 1
// （厂商 DungeonCombatHandler.cs:267 `dieType=0` vs :597 `state=1 revive`）。
func TestBuildCurrentDungeonReviveStateBodyDiffersFromDeathByOneByte(t *testing.T) {
	death := buildCurrentDungeonDeathStateBody(0x1234, 0, 0)
	revive := buildCurrentDungeonDeathStateBody(0x1234, currentDungeonReviveStateType, 0)
	if len(death) != currentDungeonDeathStateBodySize || len(revive) != currentDungeonDeathStateBodySize {
		t.Fatalf("body sizes death=%d revive=%d want=%d", len(death), len(revive), currentDungeonDeathStateBodySize)
	}
	if binary.LittleEndian.Uint16(revive[0:2]) != 0x1234 || revive[2] != 1 || revive[3] != 0 {
		t.Fatalf("revive body=%x want=34120100", revive)
	}
	if death[2] != 0 {
		t.Fatalf("death body must still carry dieType 0, got %x", death)
	}
}

// 端到端：op40 死亡 → op41 复活。修复档必须**先**发 class-0/32 的 state=1 通知，
// **再**发 class-1/op41 的 ACK（厂商 DungeonCombatHandler.cs:593-608 的顺序）。
func TestCurrentDungeonUseCoinSendsReviveStateBeforeAck(t *testing.T) {
	runOnce := func(t *testing.T, mode reviveStateNotifyMode) (msgs []uint16, classes []byte, bodies [][]byte, requestKey uint16) {
		t.Helper()
		service, session, runtime := newBackToVillageRuntime(t)
		service.gameplayTimers = newFakeCurrentDungeonDeathTimerQueue()
		service.options.reviveStateNotify = mode
		if err := service.handleDungeonDieCharacter(session, make([]byte, currentDungeonDieCharacterBodySize)); err != nil {
			t.Fatal(err)
		}
		if !runtime.deathReturnWaiting {
			t.Fatal("op40 did not arm the death return")
		}
		conn := session.conn.(*bufferConn)
		conn.write.Reset()

		body := make([]byte, currentDungeonUseCoinBodySize)
		requestKey = currentSceneActorObjectKey(session.selectedCharacterID)
		binary.LittleEndian.PutUint16(body, requestKey)
		packet, err := dnfproto.BuildChannelPacket(
			uint16(dnfenum.CmdPacketUseCoin), body, 9, dnfproto.DefaultChannelClassification,
		)
		if err != nil {
			t.Fatal(err)
		}
		if err := service.handleGameUpper(session, packet); err != nil {
			t.Fatal(err)
		}
		if runtime.deathReturnWaiting {
			t.Fatal("op41 did not cancel the death return")
		}

		raw := conn.write.Bytes()
		for len(raw) > 0 {
			parsed, rest := splitGameServerUpperPacket(t, raw)
			msgs = append(msgs, parsed.Header.MsgID)
			classes = append(classes, parsed.Header.Classification)
			bodies = append(bodies, parsed.Body)
			raw = rest
		}
		return msgs, classes, bodies, requestKey
	}

	t.Run("fix档 sends the revive notification first", func(t *testing.T) {
		msgs, classes, bodies, requestKey := runOnce(t, reviveStateNotifyVendor)
		if len(msgs) < 2 {
			t.Fatalf("op41 wrote %d packets (%v), want at least the revive NOTI + the ACK", len(msgs), msgs)
		}
		if msgs[0] != currentDungeonDeathStateMsgID || classes[0] != 0 {
			t.Fatalf("first packet msg=%d class=%d want msg=%d class=0", msgs[0], classes[0], currentDungeonDeathStateMsgID)
		}
		if len(bodies[0]) != currentDungeonDeathStateBodySize ||
			binary.LittleEndian.Uint16(bodies[0][0:2]) != requestKey ||
			bodies[0][2] != 1 || bodies[0][3] != 0 {
			t.Fatalf("revive NOTI body=%x want key=%d state=1 flag=0", bodies[0], requestKey)
		}
		if msgs[1] != uint16(dnfenum.CmdPacketUseCoin) || classes[1] != 1 {
			t.Fatalf("second packet msg=%d class=%d want msg=%d class=1 (the ACK)",
				msgs[1], classes[1], uint16(dnfenum.CmdPacketUseCoin))
		}
	})

	t.Run("legacy rollback sends no revive notification", func(t *testing.T) {
		msgs, _, _, _ := runOnce(t, reviveStateNotifyLegacy)
		for index, msg := range msgs {
			if msg == currentDungeonDeathStateMsgID {
				t.Fatalf("legacy档 must not send the revive NOTI, but packet %d is msg=%d (%v)", index, msg, msgs)
			}
		}
		if len(msgs) == 0 {
			t.Fatal("legacy档 still must send the op41 ACK")
		}
		if msgs[0] != uint16(dnfenum.CmdPacketUseCoin) {
			t.Fatalf("legacy档 first packet msg=%d want the op41 ACK", msgs[0])
		}
	})
}

// 失败路径不许误发复活通知：目标 actor 对不上时必须两包都不发。
func TestCurrentDungeonUseCoinRejectsWrongTargetWithoutReviveNotification(t *testing.T) {
	service, session, runtime := newBackToVillageRuntime(t)
	service.gameplayTimers = newFakeCurrentDungeonDeathTimerQueue()
	service.options.reviveStateNotify = reviveStateNotifyVendor
	if err := service.handleDungeonDieCharacter(session, make([]byte, currentDungeonDieCharacterBodySize)); err != nil {
		t.Fatal(err)
	}
	conn := session.conn.(*bufferConn)
	conn.write.Reset()
	body := make([]byte, currentDungeonUseCoinBodySize)
	binary.LittleEndian.PutUint16(body, currentSceneActorObjectKey(session.selectedCharacterID)+7)
	if err := service.handleDungeonUseCoin(session, body); err != nil {
		t.Fatal(err)
	}
	if got := conn.write.Bytes(); len(got) != 0 {
		t.Fatalf("mismatched target wrote %x, want nothing", got)
	}
	if !runtime.deathReturnWaiting {
		t.Fatal("mismatched target must not cancel the death return")
	}
}
