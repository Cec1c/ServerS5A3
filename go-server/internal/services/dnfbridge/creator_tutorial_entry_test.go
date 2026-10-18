package dnfbridge

import (
	"bytes"
	"encoding/binary"
	"errors"
	"testing"

	"longheng.io/server/internal/modules/dnf/dnfenum"
	dnfproto "longheng.io/server/internal/modules/dnf/protocol"
)

func creatorTutorialEntryFixture(t *testing.T, pvfPath, job string) (*Service, *gameSession) {
	t.Helper()
	repos, service, freshSession := selectAckFixtureFor(t, []int{0}, 0, 38)
	record := repos.Character.(*fakeCharacterStore).records["1"]
	record.Job = job
	record.Level = 1
	record.Name = "tutorial_creator"
	repos.Character.(*fakeCharacterStore).records["1"] = record
	service.options.currentClientVersion = "official-24.9"
	service.options.pvfPath = pvfPath
	return service, freshSession()
}

func creatorHotkeyNoticePackets(packets []dnfproto.ChannelPacket) []dnfproto.ChannelPacket {
	out := make([]dnfproto.ChannelPacket, 0, 1)
	for _, packet := range packets {
		if packet.Header.Classification == 0 && packet.Header.MsgID == 454 {
			out = append(out, packet)
		}
	}
	return out
}

func assertCreatorTutorialHotkeyNotice(t *testing.T, packet dnfproto.ChannelPacket) {
	t.Helper()
	if len(packet.Body) < 6 || packet.Body[0] != 1 {
		t.Fatalf("tutorial creator 454 key type/body = %d/%x, want type 1 text", packet.Body[0], packet.Body)
	}
	if declared := int(binary.LittleEndian.Uint32(packet.Body[1:5])); declared != len(packet.Body)-5 {
		t.Fatalf("tutorial creator 454 text length = %d, body has %d text bytes", declared, len(packet.Body)-5)
	}
	text := packet.Body[5:]
	for _, pair := range []string{"I01\t77\n", "I02\t55\n", "I03\t73\n", "I04\t58\n", "I06\t13\n"} {
		if !bytes.Contains(text, []byte(pair)) {
			t.Fatalf("tutorial creator 454 missing %q in %q", pair, text)
		}
	}
}

func TestPendingTutorialCreatorSelectEntrySendsType1Hotkey454(t *testing.T) {
	t.Setenv(currentGameOptionNoticeEnv, "1")
	path := creatorDefaultGuardRealPVF(t)
	service, session := creatorTutorialEntryFixture(t, path, "10")
	conn := session.conn.(*bufferConn)

	if err := service.sendUpperCSharpSelectInit(session, selectFirstSlotRequest()); err != nil {
		t.Fatalf("first pending creator entry: %v", err)
	}
	packets := splitAllCurrentGameServerUpperPackets(t, conn.write.Bytes())
	if len(packets) == 0 || packets[0].Header.MsgID != uint16(dnfenum.CmdPacketSelectCharacter) {
		t.Fatalf("first pending creator entry did not start with select ACK: %+v", packets)
	}
	notices := creatorHotkeyNoticePackets(packets)
	if len(notices) != 1 {
		t.Fatalf("pending creator entry 454 count = %d, want 1; packets=%+v", len(notices), packets)
	}
	assertCreatorTutorialHotkeyNotice(t, notices[0])
	noticeIndex, op15Index := -1, -1
	for index, packet := range packets {
		if packet.Header.Classification == 0 && packet.Header.MsgID == 454 {
			noticeIndex = index
		}
		if packet.Header.MsgID == uint16(dnfenum.CmdPacketEnterSelectDungeon) {
			op15Index = index
		}
	}
	if noticeIndex < 0 || op15Index < 0 || noticeIndex >= op15Index {
		t.Fatalf("pending creator packet order notice=%d op15=%d, want 454 before op15; packets=%+v", noticeIndex, op15Index, packets)
	}
	if !session.gameOptionNoticeSent {
		t.Fatal("pending creator entry did not latch gameOptionNoticeSent")
	}
	if !session.selectPreviewObjectStateSent {
		t.Fatal("pending creator entry lost the tutorial preview after op15")
	}
	if session.initialTownRouteStage != currentInitialTownRouteIdle {
		t.Fatalf("pending creator entry armed the town route: %d", session.initialTownRouteStage)
	}

	conn.write.Reset()
	if err := service.sendUpperCSharpSelectInit(session, selectFirstSlotRequest()); err != nil {
		t.Fatalf("duplicate pending creator entry: %v", err)
	}
	duplicatePackets := splitAllCurrentGameServerUpperPackets(t, conn.write.Bytes())
	if got := len(creatorHotkeyNoticePackets(duplicatePackets)); got != 0 {
		t.Fatalf("duplicate pending creator entry sent %d additional 454 packets", got)
	}
}

func TestPendingTutorialNonCreatorSelectEntryRestoresNormal454(t *testing.T) {
	t.Setenv(currentGameOptionNoticeEnv, "1")
	path := creatorDefaultGuardRealPVF(t)
	service, session := creatorTutorialEntryFixture(t, path, "0")
	conn := session.conn.(*bufferConn)

	if err := service.sendUpperCSharpSelectInit(session, selectFirstSlotRequest()); err != nil {
		t.Fatalf("pending non-creator entry: %v", err)
	}
	packets := splitAllCurrentGameServerUpperPackets(t, conn.write.Bytes())
	notices := creatorHotkeyNoticePackets(packets)
	if len(notices) != 1 || notices[0].Body[0] != 0 || !bytes.Contains(notices[0].Body[5:], []byte("I01\t2\n")) {
		t.Fatalf("pending non-creator entry did not restore NormalType: %+v", notices)
	}
	if !session.gameOptionNoticeSent {
		t.Fatal("pending non-creator entry did not latch account notice gate")
	}
	if !session.selectPreviewObjectStateSent {
		t.Fatal("pending non-creator entry lost the tutorial preview")
	}
}

type creatorTutorialWriteConn struct {
	bufferConn
	writes [][]byte
}

func (c *creatorTutorialWriteConn) Write(p []byte) (int, error) {
	n, err := c.bufferConn.Write(p)
	c.writes = append(c.writes, append([]byte(nil), p...))
	return n, err
}

type creatorTutorialFailConn struct {
	bufferConn
	failAt int
	writes int
	err    error
}

func (c *creatorTutorialFailConn) Write(p []byte) (int, error) {
	c.writes++
	if c.writes == c.failAt {
		return 0, c.err
	}
	return c.bufferConn.Write(p)
}

func creatorTutorialNoticeWriteIndex(t *testing.T, writes [][]byte) int {
	t.Helper()
	for index, chunk := range writes {
		if len(creatorHotkeyNoticePackets(splitAllCurrentGameServerUpperPackets(t, chunk))) != 0 {
			return index + 1
		}
	}
	return 0
}

func TestPendingTutorialCreatorRetriesAfterHotkeySendFailure(t *testing.T) {
	t.Setenv(currentGameOptionNoticeEnv, "1")
	path := creatorDefaultGuardRealPVF(t)
	service, session := creatorTutorialEntryFixture(t, path, "10")
	counting := &creatorTutorialWriteConn{}
	session.conn = counting
	if err := service.sendUpperCSharpSelectInit(session, selectFirstSlotRequest()); err != nil {
		t.Fatalf("locate pending creator 454 write: %v", err)
	}
	noticeWrite := creatorTutorialNoticeWriteIndex(t, counting.writes)
	if noticeWrite == 0 {
		t.Fatal("pending creator entry emitted no 454 write to target")
	}

	service, session = creatorTutorialEntryFixture(t, path, "10")
	failing := &creatorTutorialFailConn{failAt: noticeWrite, err: errors.New("injected 454 write failure")}
	session.conn = failing
	if err := service.sendUpperCSharpSelectInit(session, selectFirstSlotRequest()); err != nil {
		t.Fatalf("pending creator entry with failed 454 write: %v", err)
	}
	if session.gameOptionNoticeSent {
		t.Fatal("failed 454 write permanently consumed gameOptionNoticeSent")
	}
	if got := len(creatorHotkeyNoticePackets(splitAllCurrentGameServerUpperPackets(t, failing.bufferConn.write.Bytes()))); got != 0 {
		t.Fatalf("failed 454 write still produced %d 454 packets", got)
	}

	failing.failAt = 0
	failing.bufferConn.write.Reset()
	if err := service.sendUpperCSharpSelectInit(session, selectFirstSlotRequest()); err != nil {
		t.Fatalf("pending creator retry after 454 write recovery: %v", err)
	}
	notices := creatorHotkeyNoticePackets(splitAllCurrentGameServerUpperPackets(t, failing.bufferConn.write.Bytes()))
	if len(notices) != 1 {
		t.Fatalf("pending creator retry 454 count = %d, want 1", len(notices))
	}
	assertCreatorTutorialHotkeyNotice(t, notices[0])
	if !session.gameOptionNoticeSent {
		t.Fatal("successful pending creator retry did not latch gameOptionNoticeSent")
	}
}
