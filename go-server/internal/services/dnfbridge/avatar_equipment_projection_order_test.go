package dnfbridge

import (
	"context"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	platformpvf "longheng.io/server/internal/platform/pvf"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// Load captures a real repository result before parking exactly one reader.
// It never fabricates a result or changes the production callback/handler.
type equipmentTailLoadGate struct {
	dnfrepo.EquipmentRepository
	armed       atomic.Bool
	captured    chan dnfrepo.EquipmentRecord
	release     chan struct{}
	skip        atomic.Int32
	releaseOnce sync.Once
}

func (g *equipmentTailLoadGate) Load(ctx context.Context, id string) (dnfrepo.EquipmentRecord, bool, error) {
	r, found, err := g.EquipmentRepository.Load(ctx, id)
	if g.armed.Load() && g.skip.Add(-1) < 0 && g.armed.CompareAndSwap(true, false) {
		g.captured <- r
		select {
		case <-g.release:
		case <-ctx.Done():
			return dnfrepo.EquipmentRecord{}, false, ctx.Err()
		}
	}
	return r, found, err
}

func (g *equipmentTailLoadGate) resume() { g.releaseOnce.Do(func() { close(g.release) }) }

func equipmentTailRealUnequip(f *loginWearBridgeFixture, bag, worn int16) error {
	f.session.avatarAppearanceMu.Lock()
	f.session.avatarAppearanceLastAt = time.Now()
	f.session.avatarAppearanceMu.Unlock()
	body := loginWearBridgeBody(1, bag, 0, 17, worn, 0)
	body[26] = 1
	handled, err := f.service.handleAlignedGameCommand(f.session, 1, 19, body)
	if err != nil || !handled {
		return fmt.Errorf("real unequip handled=%v: %w", handled, err)
	}
	return nil
}

func equipmentTailRunCaptured(t *testing.T, gate *equipmentTailLoadGate, queue *processGameplayTimeQueue) <-chan struct{} {
	t.Helper()
	gate.armed.Store(true)
	done := make(chan struct{})
	go func() { defer close(done); queue.queue.RunDue(time.Now().Add(time.Second)) }()
	select {
	case <-gate.captured:
	case <-time.After(3 * time.Second):
		t.Fatal("real taken callback did not reach repository barrier")
	}
	return done
}

func equipmentTailWait(t *testing.T, done <-chan struct{}) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("production callback did not finish")
	}
}

func equipmentTailAssertBody(t *testing.T, body []byte, wantHair uint32) {
	t.Helper()
	appearance := body[currentSceneObjectTailStartForTest("Actor19")+6:]
	if hair := currentActorMode0AppearanceGoldenItemID(t, appearance, 1); hair != wantHair {
		t.Fatalf("worn hair=%d want=%d", hair, wantHair)
	}
	if coat := currentActorMode0AppearanceGoldenItemID(t, appearance, 3); coat != currentActorMode0AppearanceEmptyItem {
		t.Fatalf("coat unexpectedly worn=%d", coat)
	}
}

func equipmentTailAssertS2(t *testing.T, f *loginWearBridgeFixture) {
	t.Helper()
	equipment, found, err := f.group.Equipment.Load(context.Background(), "19")
	if err != nil || !found || len(equipment.Entries) != 0 {
		t.Fatalf("S2=%+v found=%v err=%v", equipment, found, err)
	}
	inv, _, err := f.group.Inventory.Load(context.Background(), "19")
	if err != nil || inv.Slots["1:0"].ItemID != 40734 || inv.Slots["1:1"].ItemID != 39524 {
		t.Fatalf("durable S2 bags=%+v err=%v", inv, err)
	}
}

func equipmentTailSaveWire(t *testing.T, name string, stream []byte) {
	t.Helper()
	var timeline []map[string]any
	for len(stream) > 0 {
		p, rest := splitLongHengGameServerUpperPacket(t, stream)
		stream = rest
		timeline = append(timeline, map[string]any{"ordinal": len(timeline) + 1, "msg_id": p.Header.MsgID, "classification": p.Header.Classification, "body_hex": hex.EncodeToString(p.Body)})
	}
	if dir := os.Getenv("DNF90_STALE_TAIL_ARTIFACT"); dir != "" {
		data, err := json.MarshalIndent(timeline, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(filepath.Join(dir, name+".json"), data, 0644); err != nil {
			t.Fatal(err)
		}
	}
}

func TestAvatarEquipmentProjectionTakenTailPrecedesNextRealMutation(t *testing.T) {
	f, gate, queue := newEquipmentTailOrderFixture(t)
	if err := equipmentTailRealUnequip(f, 0, 3); err != nil {
		t.Fatal(err)
	}
	f.buffer.write.Reset()
	done := equipmentTailRunCaptured(t, gate, queue)
	// This is a positive assertion on the real production lock, not a timeout
	// used as evidence that the second command is blocked.
	if f.session.equipmentProjectionMu.TryLock() {
		f.session.equipmentProjectionMu.Unlock()
		t.Fatal("taken S1 callback did not own mutation/projection lock")
	}
	second := make(chan error, 1)
	go func() { second <- equipmentTailRealUnequip(f, 1, 1) }()
	gate.resume()
	equipmentTailWait(t, done)
	select {
	case err := <-second:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("second real handler failed to resume after S1 projection")
	}
	equipmentTailAssertS2(t, f)
	p, stream := splitLongHengGameServerUpperPacket(t, f.buffer.write.Bytes())
	if p.Header.MsgID != 2 || p.Header.Classification != 0 {
		t.Fatalf("S1 not before S2 ACK: %+v", p.Header)
	}
	equipmentTailAssertBody(t, p.Body, 39524)
	ack, stream := splitLongHengGameServerUpperPacket(t, stream)
	row, stream := splitLongHengGameServerUpperPacket(t, stream)
	if ack.Header.MsgID != 19 || ack.Body[0] != 1 || row.Header.MsgID != 14 || row.Body[0] != 1 || binary.LittleEndian.Uint32(row.Body[5:]) != 39524 {
		t.Fatalf("S2 ACK/op14 did not follow S1: ack=%+v/%x row=%+v/%x", ack.Header, ack.Body, row.Header, row.Body)
	}
	// Lock waiting can legitimately make this command exceed the 200ms burst
	// window. Its immediate mode0 must then be fresh S2, just as a deferred
	// mode0 must be fresh S2 when the burst still fits the window.
	if len(stream) > 0 {
		latest, rest := splitLongHengGameServerUpperPacket(t, stream)
		if latest.Header.MsgID != 2 || len(rest) != 0 {
			t.Fatalf("unexpected S2 immediate projection: %+v rest=%x", latest.Header, rest)
		}
		equipmentTailAssertBody(t, latest.Body, currentActorMode0AppearanceEmptyItem)
	}
	// A newer coalesced tail must still run and converge to S2, rather than
	// making the regression pass by dropping every appearance callback.
	queue.queue.RunDue(time.Now().Add(2 * time.Second))
	stream = f.buffer.write.Bytes()
	for i := 0; i < 3; i++ {
		_, stream = splitLongHengGameServerUpperPacket(t, stream)
	}
	latest, stream := splitLongHengGameServerUpperPacket(t, stream)
	if latest.Header.MsgID != 2 || len(stream) != 0 {
		t.Fatalf("latest S2 tail missing or duplicated: %+v rest=%x", latest.Header, stream)
	}
	equipmentTailAssertBody(t, latest.Body, currentActorMode0AppearanceEmptyItem)
	equipmentTailSaveWire(t, "green-tail-first-timeline", f.buffer.write.Bytes())
}

func TestAvatarEquipmentProjectionMutationBeforeTailReadsLatest(t *testing.T) {
	f, gate, queue := newEquipmentTailOrderFixture(t)
	if err := equipmentTailRealUnequip(f, 0, 3); err != nil {
		t.Fatal(err)
	}
	f.buffer.write.Reset()
	if err := equipmentTailRealUnequip(f, 1, 1); err != nil {
		t.Fatal(err)
	}
	equipmentTailAssertS2(t, f)
	done := equipmentTailRunCaptured(t, gate, queue)
	gate.resume()
	equipmentTailWait(t, done)
	stream := f.buffer.write.Bytes()
	ack, stream := splitLongHengGameServerUpperPacket(t, stream)
	row, stream := splitLongHengGameServerUpperPacket(t, stream)
	latest, stream := splitLongHengGameServerUpperPacket(t, stream)
	if ack.Header.MsgID != 19 || row.Header.MsgID != 14 || latest.Header.MsgID != 2 || len(stream) != 0 {
		t.Fatalf("S2 first order ack=%+v row=%+v latest=%+v rest=%x", ack.Header, row.Header, latest.Header, stream)
	}
	equipmentTailAssertBody(t, latest.Body, currentActorMode0AppearanceEmptyItem)
	equipmentTailSaveWire(t, "green-mutation-first-timeline", f.buffer.write.Bytes())
}

func TestAvatarEquipmentProjectionTakenBuildRejectsStoppedOrReboundSession(t *testing.T) {
	for _, boundary := range []string{"stop", "rebind"} {
		t.Run(boundary, func(t *testing.T) {
			f, gate, queue := newEquipmentTailOrderFixture(t)
			if err := equipmentTailRealUnequip(f, 0, 3); err != nil {
				t.Fatal(err)
			}
			f.buffer.write.Reset()
			// The fourth real equipment read is inside appearance body construction,
			// after selectedCharacterForEnter completed its binding check. Turning
			// memo off is an existing supported path, not a replacement constructor.
			f.service.options.equipMode0RepositoryMemoMode = equipMode0RepositoryMemoOff
			gate.skip.Store(3)
			done := equipmentTailRunCaptured(t, gate, queue)
			if boundary == "stop" {
				f.service.stopAvatarAppearanceCoalesce(f.session, "test_end_during_build")
			} else {
				f.service.bindGameSessionCharacter(f.session, 20)
			}
			gate.resume()
			equipmentTailWait(t, done)
			if f.buffer.write.Len() != 0 {
				t.Fatalf("expired %s binding emitted %x", boundary, f.buffer.write.Bytes())
			}
		})
	}
}

func newEquipmentTailOrderFixture(t *testing.T) (*loginWearBridgeFixture, *equipmentTailLoadGate, *processGameplayTimeQueue) {
	t.Helper()
	path := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if path == "" {
		t.Skip("set DNFBRIDGE_REAL_PVF_SMOKE for the real avatar owner/projection ordering regression")
	}
	archive, err := platformpvf.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := newPVFDungeonDropCatalog(archive)
	if err != nil {
		t.Fatal(err)
	}
	f := newLoginWearBridgeFixture(t)
	f.service.initialEquipmentArchive = archive
	f.service.pvfItemCatalog = catalog
	f.service.options.currentClientVersion = "official-24.9"
	// Cold real-PVF validation can outlast the production 200ms burst window.
	// These tests require a queued callback; separate the burst window from
	// the deadline so RunDue still takes the real tail without wall-clock waits.
	f.service.options.avatarAppearanceCoalesceWindowMillis = 30000
	f.service.options.avatarAppearanceTrailingMillis = 20
	f.session.op19QuietResolving = true
	ctx := context.Background()
	equipment := dnfrepo.EquipmentRecord{CharacterID: "19", Entries: map[string]dnfrepo.EquipmentEntry{}}
	for _, a := range []struct {
		slot int16
		id   uint32
	}{{3, 40734}, {1, 39524}} {
		definition, e := catalog.ResolveItem(a.id)
		if e != nil {
			t.Fatal(e)
		}
		if !currentItemListStackExtraIsAvatar(map[string]string{"equipment_type": definition.EquipmentType}) {
			t.Fatalf("not a real avatar: %+v", definition)
		}
		raw := make([]byte, currentItemListEntryWireSize)
		binary.LittleEndian.PutUint16(raw, uint16(a.slot))
		binary.LittleEndian.PutUint32(raw[2:], a.id)
		binary.LittleEndian.PutUint32(raw[0x0e:], a.id)
		slot := fmt.Sprint(a.slot)
		equipment.Entries[slot] = dnfrepo.EquipmentEntry{SlotIndex: a.slot, ItemID: int64(a.id), RawEntry: raw, Extra: map[string]string{"item_kind": "equipment", "equipment_type": definition.EquipmentType, "pvf_path": definition.PVFPath, "equipped_slot": slot, "equipment_slot": slot, "current_exe_equipment_type": slot, "current_exe_runtime_move": "1", "current_exe_inventory_count": "1", "raw_entry_hex": hex.EncodeToString(raw)}}
	}
	if err = f.group.Equipment.Save(ctx, equipment); err != nil {
		t.Fatal(err)
	}
	gate := &equipmentTailLoadGate{EquipmentRepository: f.group.Equipment, captured: make(chan dnfrepo.EquipmentRecord, 1), release: make(chan struct{})}
	t.Cleanup(gate.resume)
	group := f.group
	group.Equipment = gate
	f.service.repositoryProvider = func() (dnfrepo.Group, bool) { return group, true }
	f.service.bindGameSessionCharacter(f.session, 19)
	queue := f.service.ensureGameplayTimeQueue().(*processGameplayTimeQueue)
	return f, gate, queue
}
