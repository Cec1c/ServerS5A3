package dnfbridge

import (
	"context"
	"encoding/binary"
	"errors"
	"runtime"
	"strings"
	"testing"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

type remoteActorSnapshotFaultEquipment struct {
	dnfrepo.EquipmentRepository
	loads       []string
	failFirst   bool
	failOnMode1 bool
	onLoad      func()
	err         error
}

func (r *remoteActorSnapshotFaultEquipment) Load(ctx context.Context, id string) (dnfrepo.EquipmentRecord, bool, error) {
	r.loads = append(r.loads, id)
	if r.onLoad != nil {
		r.onLoad()
	}
	pc, _, _, _ := runtime.Caller(1)
	if r.failOnMode1 && strings.HasSuffix(runtime.FuncForPC(pc).Name(), ".(*csharpLegacyUserInfoReader).currentMode1EquipmentObjectRows") {
		return dnfrepo.EquipmentRecord{}, false, r.err
	}
	if r.failFirst && len(r.loads) == 1 {
		return dnfrepo.EquipmentRecord{}, false, r.err
	}
	return r.EquipmentRepository.Load(ctx, id)
}

func TestTownRemoteActorEquipmentSnapshotRejectsSelectionChangedDuringConstruction(t *testing.T) {
	fixture := newTownResendFixture(t)
	repos, _ := fixture.service.repositoryGroup()
	fault := &remoteActorSnapshotFaultEquipment{EquipmentRepository: repos.Equipment, onLoad: func() {
		fixture.peer.selectedCharacterID = 29
	}}
	repos.Equipment = fault
	fixture.service.repositoryProvider = func() (dnfrepo.Group, bool) { return repos, true }
	peer, _ := fixture.service.onlinePlayers.PlayerForCharacter(17)
	if err := fixture.service.sendTownRemoteActorState(fixture.local, peer, "selection_changed"); err == nil || !strings.Contains(err.Error(), "selection changed during construction") {
		t.Fatalf("changed actor selection = %v", err)
	}
	if len(fault.loads) != 1 || fixture.localBuf.write.Len() != 0 || fixture.peerBuf.write.Len() != 0 {
		t.Fatal("selection change published an obsolete actor")
	}
}

func TestTownRemoteActorEquipmentSnapshotRejectsFailureAndRetriesRealPeerWeapon(t *testing.T) {
	fixture := newTownResendFixture(t)
	repos, _ := fixture.service.repositoryGroup()
	seedLiveCharacter631WornSlots(t, repos)
	record, _, err := repos.Equipment.Load(context.Background(), "6")
	if err != nil {
		t.Fatal(err)
	}
	weapon := record.Entries["12"] // actual worn row fixture: item101040493, durability48.
	if err := repos.Equipment.Save(context.Background(), dnfrepo.EquipmentRecord{
		CharacterID: "17", Entries: map[string]dnfrepo.EquipmentEntry{"12": weapon},
	}); err != nil {
		t.Fatal(err)
	}
	fault := &remoteActorSnapshotFaultEquipment{EquipmentRepository: repos.Equipment, failFirst: true, err: errors.New("remote equipment read failed")}
	repos.Equipment = fault
	fixture.service.repositoryProvider = func() (dnfrepo.Group, bool) { return repos, true }
	peer, _ := fixture.service.onlinePlayers.PlayerForCharacter(17)
	if err := fixture.service.sendTownRemoteActorState(fixture.local, peer, "snapshot_failure"); !errors.Is(err, fault.err) {
		t.Fatalf("repository error lost: %v", err)
	}
	if fixture.localBuf.write.Len() != 0 || fixture.peerBuf.write.Len() != 0 {
		t.Fatal("failed snapshot published partial/empty peer state")
	}
	// Inject failure at the later mode1 Equipment.Load, after all mode0 reads
	// are healthy. The snapshot makes that extra storage read unnecessary.
	fault.loads, fault.failFirst, fault.failOnMode1 = nil, false, true
	if err := fixture.service.sendTownRemoteActorState(fixture.local, peer, "snapshot_retry"); err != nil {
		t.Fatal(err)
	}
	frames := fixture.frames()
	if len(frames) < 2 || frames[0].msgID != 2 || frames[1].msgID != 2 || frames[0].body[0] != 0 || frames[1].body[0] != 1 {
		t.Fatalf("peer projection ordering: %v", frameIDs(frames))
	}
	body := frames[1].body
	if binary.LittleEndian.Uint16(body[21:23]) != 17 || body[currentMode1CreateCountOffset] != 1 {
		t.Fatalf("mode1 key/count targets wrong actor or empty set: key=%d count=%d", binary.LittleEndian.Uint16(body[21:23]), body[currentMode1CreateCountOffset])
	}
	if len(fault.loads) != 1 || fault.loads[0] != "17" {
		t.Fatalf("equipment reads=%v, want actual peer17 once", fault.loads)
	}
	row := body[currentMode1CreateRowsOffset:]
	if row[0] != 12 || binary.LittleEndian.Uint32(row[1:5]) != 101040493 || binary.LittleEndian.Uint16(row[10:12]) != 48 {
		t.Fatalf("peer weapon identity/durability lost: %x", row[:12])
	}
	if fixture.peerBuf.write.Len() != 0 {
		t.Fatal("remote projection wrote to actor's own connection")
	}
}

func TestTownRemoteActorEquipmentSnapshotRejectsStalePresenceBeforeReadOrSend(t *testing.T) {
	fixture := newTownResendFixture(t)
	repos, _ := fixture.service.repositoryGroup()
	fault := &remoteActorSnapshotFaultEquipment{EquipmentRepository: repos.Equipment}
	repos.Equipment = fault
	fixture.service.repositoryProvider = func() (dnfrepo.Group, bool) { return repos, true }
	peer, _ := fixture.service.onlinePlayers.PlayerForCharacter(17)
	fixture.peer.selectedCharacterID = 29
	if err := fixture.service.sendTownRemoteActorState(fixture.local, peer, "stale_presence"); err == nil {
		t.Fatal("stale actor presence accepted")
	}
	if len(fault.loads) != 0 || fixture.localBuf.write.Len() != 0 || fixture.peerBuf.write.Len() != 0 {
		t.Fatal("stale selected character read or published another actor's state")
	}
}

func TestTownRemoteActorEquipmentSnapshotKeepsHealthyAbsentEquipmentEmpty(t *testing.T) {
	fixture := newTownResendFixture(t)
	repos, _ := fixture.service.repositoryGroup()
	fault := &remoteActorSnapshotFaultEquipment{EquipmentRepository: repos.Equipment, failOnMode1: true, err: errors.New("later mode1 load must not occur")}
	repos.Equipment = fault
	fixture.service.repositoryProvider = func() (dnfrepo.Group, bool) { return repos, true }
	peer, _ := fixture.service.onlinePlayers.PlayerForCharacter(17)
	if err := fixture.service.sendTownRemoteActorState(fixture.local, peer, "healthy_absent_equipment"); err != nil {
		t.Fatal(err)
	}
	frames := fixture.frames()
	if len(frames) < 2 || frames[1].body[currentMode1CreateCountOffset] != 0 || len(fault.loads) != 1 || fault.loads[0] != "17" {
		t.Fatalf("healthy absent peer equipment changed: frames=%v reads=%v", frameIDs(frames), fault.loads)
	}
}
