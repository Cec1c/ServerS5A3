package dnfbridge

import (
	"bytes"
	"encoding/binary"
	"reflect"
	"testing"

	"longheng.io/server/internal/modules/dnf/channelcatalog"
	"longheng.io/server/internal/modules/dnf/dnfenum"
	"longheng.io/server/internal/modules/dnf/raid"
)

func TestCreatedRaidInfoRequestReturnsMetadataUpdateAndSameRoster(t *testing.T) {
	s := &Service{options: options{gameUpperBodyCodec: gameUpperBodyCodecPlain}}
	conn := &bufferConn{}
	session := &gameSession{conn: conn, channel: channelcatalog.Channel{ID: 23, Type: dnfenum.RaidChannelType}}
	s.bindGameSessionCharacter(session, 6)
	name := []byte{'t', 0, 'e', 0, 's', 0, 't', 0}
	if err := s.handleOnlineCreateRaid(session, 664, buildRaidInfoRequestForBridgeTest(1, name, 0)); err != nil {
		t.Fatal(err)
	}
	key := uint32(23)<<16 | 6
	conn.write.Reset()
	request := make([]byte, 5)
	request[0] = 2
	binary.LittleEndian.PutUint32(request[1:], key)
	if err := s.handleOnlineRequestRaidInfo(session, request); err != nil {
		t.Fatal(err)
	}
	metadata, rest := splitGameServerUpperPacket(t, conn.write.Bytes())
	if metadata.Header.Classification != 0 || metadata.Header.MsgID != raid.RaidMemberRefreshMsgID || binary.LittleEndian.Uint32(metadata.Body[4:8]) != 2 || binary.LittleEndian.Uint32(metadata.Body[8:12]) != key {
		t.Fatalf("bad metadata update: %+v", metadata)
	}
	members, rest := splitRaidRefreshPacket(t, rest, key)
	if len(rest) != 0 || len(members) != 1 || members[0].CharID != 6 || len(s.raids) != 1 {
		t.Fatalf("request recreated or lost raid: members=%+v raids=%d", members, len(s.raids))
	}
	conn.write.Reset()
	request[0] = 1 // actual test1/test78 capture after CREATE_RAID
	if err := s.handleOnlineRequestRaidInfo(session, request); err != nil {
		t.Fatal(err)
	}
	mode1Metadata, mode1Rest := splitGameServerUpperPacket(t, conn.write.Bytes())
	mode1Members, mode1Rest := splitRaidRefreshPacket(t, mode1Rest, key)
	if len(mode1Rest) != 0 || !bytes.Equal(mode1Metadata.Body, metadata.Body) || !reflect.DeepEqual(mode1Members, members) {
		t.Fatal("mode1 query lost authoritative metadata/roster")
	}
	conn.write.Reset()
	binary.LittleEndian.PutUint32(request[1:], key+1)
	if err := s.handleOnlineRequestRaidInfo(session, request); err != nil {
		t.Fatal(err)
	}
	if conn.write.Len() != 0 || len(s.raids) != 1 {
		t.Fatal("unknown raid fabricated")
	}
}

func TestOrdinaryChannelCannotCreateRaidEvenWithPersistedRaidTown(t *testing.T) {
	s := &Service{options: options{gameUpperBodyCodec: gameUpperBodyCodecPlain}}
	conn := &bufferConn{}
	session := &gameSession{conn: conn, channel: channelcatalog.Channel{ID: 11, Type: 11}}
	s.bindGameSessionCharacter(session, 6)
	if err := s.handleOnlineCreateRaid(session, 664, buildRaidInfoRequestForBridgeTest(1, []byte("raid"), 0)); err != nil {
		t.Fatal(err)
	}
	packet, rest := splitGameServerUpperPacket(t, conn.write.Bytes())
	if len(rest) != 0 || len(s.raids) != 0 || len(packet.Body) != 2 || packet.Body[0] != 0 {
		t.Fatal("ordinary channel accepted raid creation")
	}
}
