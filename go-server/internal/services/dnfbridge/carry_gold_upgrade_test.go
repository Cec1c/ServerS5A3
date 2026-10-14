package dnfbridge

import (
	"context"
	"encoding/binary"
	"reflect"
	"testing"

	dnfproto "longheng.io/server/internal/modules/dnf/protocol"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	dnfrepomemory "longheng.io/server/internal/modules/dnf/repository/memory"
)

func newCarryGoldFixture(t *testing.T, gold, stage int64) (*Service, *gameSession, dnfrepo.Group, *bufferConn) {
	t.Helper()
	repos := dnfrepomemory.NewMemoryGroup()
	err := repos.Character.Save(context.Background(), dnfrepo.CharacterRecord{
		CharacterID: "17", AccountID: "dnf:1", Level: 90,
		Stats: map[string]int64{"gold": gold, "carry_gold_stage": stage},
	})
	if err != nil {
		t.Fatal(err)
	}
	conn := &bufferConn{}
	s := &Service{options: options{gameUpperBodyCodec: gameUpperBodyCodecPlain}, repositoryProvider: func() (dnfrepo.Group, bool) { return repos, true }}
	return s, &gameSession{conn: conn, selectedCharacterID: 17, accountID: "dnf:1"}, repos, conn
}

func TestCarryGoldDuplicateRepairsLostReplyWithoutAnotherDebit(t *testing.T) {
	s, session, repos, conn := newCarryGoldFixture(t, 25_000_000, 0)
	if err := s.handleCurrentUpgradeCarryGold(session, carryGoldRequest(1)); err != nil {
		t.Fatal(err)
	}
	conn.write.Reset() // Simulate losing the successful reply after commit.
	if err := s.handleCurrentUpgradeCarryGold(session, carryGoldRequest(1)); err != nil {
		t.Fatal(err)
	}
	ack, rest := splitCurrentGameServerUpperPacketAuto(t, conn.write.Bytes())
	if ack.Header.MsgID != 978 || len(ack.Body) != 6 || ack.Body[0] != 0 || ack.Body[1] != 1 || binary.LittleEndian.Uint32(ack.Body[2:]) != 2 {
		t.Fatalf("duplicate ACK=%x", ack.Body)
	}
	stage, rest := splitCurrentGameServerUpperPacketAuto(t, rest)
	if stage.Header.MsgID != 947 || stage.Header.Classification != 0 || !reflect.DeepEqual(stage.Body, []byte{1}) {
		t.Fatalf("stage recovery=%+v %x", stage.Header, stage.Body)
	}
	wallet, rest := splitCurrentGameServerUpperPacketAuto(t, rest)
	if wallet.Header.MsgID != 14 || len(rest) != 0 {
		t.Fatalf("wallet recovery=%+v trailing=%x", wallet.Header, rest)
	}
	stored, _, _ := repos.Character.Load(context.Background(), "17")
	if stored.Stats["gold"] != 20_000_000 || stored.Stats["carry_gold_stage"] != 1 {
		t.Fatalf("duplicate debit: %v", stored.Stats)
	}
}

func TestCarryGoldRejectedRequestsPreserveStateAndUseNativeFailure(t *testing.T) {
	for _, name := range []string{"funds", "ownership", "no-character", "length", "wrong-class", "unavailable"} {
		t.Run(name, func(t *testing.T) {
			s, session, repos, conn := newCarryGoldFixture(t, 25_000_000, 0)
			body := carryGoldRequest(1)
			code := uint32(2)
			switch name {
			case "funds":
				c, _, _ := repos.Character.Load(context.Background(), "17")
				c.Stats["gold"] = 4_999_999
				if err := repos.Character.Save(context.Background(), c); err != nil {
					t.Fatal(err)
				}
				code = 1
			case "ownership":
				session.accountID = "someone-else"
			case "no-character":
				session.selectedCharacterID = 0
			case "length":
				body = body[:13]
			case "unavailable":
				r := repos
				r.CharacterAssets = nil
				s.repositoryProvider = func() (dnfrepo.Group, bool) { return r, true }
			}
			before, _, _ := repos.Character.Load(context.Background(), "17")
			var err error
			if name == "wrong-class" {
				_, err = currentGameplayModules.DispatchUpper(s, session, 978, 0, body)
			} else {
				err = s.handleCurrentUpgradeCarryGold(session, body)
			}
			if err != nil {
				t.Fatal(err)
			}
			after, _, _ := repos.Character.Load(context.Background(), "17")
			if !reflect.DeepEqual(before, after) {
				t.Fatal("refusal mutated assets")
			}
			if name == "wrong-class" {
				if conn.write.Len() != 0 {
					t.Fatal("wrong class emitted a command reply")
				}
				return
			}
			ack, rest := splitCurrentGameServerUpperPacketAuto(t, conn.write.Bytes())
			if ack.Header.MsgID != 978 || len(ack.Body) != 6 || ack.Body[0] != 0 || binary.LittleEndian.Uint32(ack.Body[2:]) != code || len(rest) != 0 {
				t.Fatalf("refusal=%+v %x rest=%x", ack.Header, ack.Body, rest)
			}
		})
	}
}

func TestCarryGoldRelogProjectionResetsPreviousCharacterStage(t *testing.T) {
	s, session, repos, conn := newCarryGoldFixture(t, 0, 3)
	character, _, _ := repos.Character.Load(context.Background(), "17")
	if err := s.sendCurrentCarryGoldStage(session, character); err != nil {
		t.Fatal(err)
	}
	if err := s.sendCurrentCarryGoldStage(session, dnfrepo.CharacterRecord{CharacterID: "18"}); err != nil {
		t.Fatal(err)
	}
	stream := conn.write.Bytes()
	for _, want := range []byte{3, 0} {
		packet, rest := splitCurrentGameServerUpperPacketAuto(t, stream)
		if packet.Header.MsgID != 947 || packet.Header.Classification != 0 || !reflect.DeepEqual(packet.Body, []byte{want}) {
			t.Fatalf("projection=%+v %x want=%d", packet.Header, packet.Body, want)
		}
		stream = rest
	}
	if len(stream) != 0 {
		t.Fatal("unexpected extra projection")
	}
}

func carryGoldRequest(stage byte) []byte {
	// Current EXE 0x01D93000 initializes only byte 13 of its stack copy.
	return []byte{0x50, 0x8f, 0xec, 0x3a, 0, 0, 0, 0, 0xd8, 0xf8, 0x19, 0, 0x30, stage}
}

func TestCarryGoldUpgradeDispatchChargesOnceAndRefreshesWallet(t *testing.T) {
	for _, transport := range []string{"legacy", "upper"} {
		t.Run(transport, func(t *testing.T) {
			s, session, repos, conn := newCarryGoldFixture(t, 250_000_000, 0)
			var handled bool
			var err error
			if transport == "legacy" {
				handled, err = currentGameplayModules.DispatchLegacy(s, session, 978, carryGoldRequest(1))
			} else {
				handled, err = currentGameplayModules.DispatchUpper(s, session, 978, dnfproto.DefaultChannelClassification, carryGoldRequest(1))
			}
			if err != nil || !handled {
				t.Fatalf("upgrade dispatch handled=%v err=%v", handled, err)
			}
			ack, rest := splitGameServerUpperPacket(t, conn.write.Bytes())
			if ack.Header.MsgID != 978 || ack.Header.Classification != 1 || len(ack.Body) != 6 || ack.Body[0] != 1 || ack.Body[1] != 1 || binary.LittleEndian.Uint32(ack.Body[2:]) != 0 {
				t.Fatalf("upgrade ACK header=%+v body=%x", ack.Header, ack.Body)
			}
			wallet, rest := splitGameServerUpperPacket(t, rest)
			if wallet.Header.MsgID != 14 || wallet.Header.Classification != 0 || len(rest) != 0 {
				t.Fatalf("wallet header=%+v rest=%x", wallet.Header, rest)
			}
			character, _, _ := repos.Character.Load(context.Background(), "17")
			if character.Stats["gold"] != 245_000_000 || character.Stats["carry_gold_stage"] != 1 {
				t.Fatalf("committed stats=%v", character.Stats)
			}
		})
	}
}
