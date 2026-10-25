package dnfbridge

import (
	"bytes"
	"testing"
)

// A presence entry can retain an auxiliary transport after the character
// directory selects the resident socket. Rebuilding from that entry must not
// omit the HP initialization that the same canonical actor would receive.
func TestP05AuxiliaryPresenceUsesCanonicalActorProjection(t *testing.T) {
	f := newTownPartyActorHPFixture(t, true)
	if err := f.service.sendTownRemoteActorState(f.target, f.actorInfo, "p05-canonical-control"); err != nil {
		t.Fatal(err)
	}
	canonical := p05Frames(t, f.service, f.target)
	aux := &gameSession{
		conn: &bufferConn{}, connID: "p05-auxiliary", channel: f.actor.channel,
		residentChannel: f.actor.residentChannel, selectedCharacterID: f.actor.selectedCharacterID,
		townActorOwnerChannel:           f.actor.townActorOwnerChannel,
		connectionTownActorOwnerChannel: f.actor.connectionTownActorOwnerChannel,
	}
	f.service.bindGameSessionCharacter(aux, aux.selectedCharacterID)
	if _, ok := f.service.boundGameSessionCharacterSnapshot(aux); ok {
		t.Fatal("fixture did not retain an auxiliary transport")
	}
	row := f.actorInfo
	row.Session = aux
	if err := f.service.sendTownRemoteActorState(f.target, row, "p05-auxiliary-control"); err != nil {
		t.Fatal(err)
	}
	actual := p05Frames(t, f.service, f.target)
	if len(actual) != len(canonical) {
		t.Fatalf("auxiliary projection frames=%v, canonical=%v", frameIDs(actual), frameIDs(canonical))
	}
	for i := range actual {
		if actual[i].msgID != canonical[i].msgID || !bytes.Equal(actual[i].body, canonical[i].body) {
			t.Fatalf("auxiliary projection differs from canonical actor at frame %d (msg %d/%d)", i, actual[i].msgID, canonical[i].msgID)
		}
	}
}
