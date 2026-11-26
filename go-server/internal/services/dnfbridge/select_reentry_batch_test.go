package dnfbridge

import (
	"bytes"
	"longheng.io/server/internal/modules/dnf/channelcatalog"
	"testing"
)

func TestReturnSelectOrdinarySceneBatchWithholdsIncompleteTownState(t *testing.T) {
	s, p, _ := newChannelReconnectTest(t)
	s.options.currentClientVersion = "official-24.9"
	p.channel = channelcatalog.Channel{ID: 11, Type: 11, Group: "granfloris"}
	p.channelReconnect = false
	markReturnSelectTownReentry(p, 29)
	c := &channelSceneWriteConn{}
	p.conn = c
	started, err := s.beginCurrentOrdinaryChannelSceneBatch(p)
	if err != nil || !started {
		t.Fatalf("return-select town stream must batch: started=%t err=%v", started, err)
	}
	first, last := []byte{1, 2, 3}, []byte{4, 5, 6}
	if err := s.writeGameRawPacket(p, first); err != nil {
		t.Fatal(err)
	}
	if len(c.writes) != 0 {
		t.Fatal("incomplete town state exposed before final transition")
	}
	if err := s.writeGameRawPacket(p, last); err != nil {
		t.Fatal(err)
	}
	if err := s.finishCurrentOrdinaryChannelSceneBatch(p, nil); err != nil {
		t.Fatal(err)
	}
	if len(c.writes) != 1 || !bytes.Equal(c.writes[0], append(first, last...)) {
		t.Fatalf("writes=%v", c.writes)
	}
}
