package dnfbridge

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	dnfexpertjob "longheng.io/server/internal/modules/dnf/expertjob"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	dnfrepomemory "longheng.io/server/internal/modules/dnf/repository/memory"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

type menuPresenceLoadBarrier struct {
	dnfrepo.CharacterRepository
	loads   atomic.Int32
	entered chan struct{}
	release chan struct{}
}

func (r *menuPresenceLoadBarrier) Load(ctx context.Context, id string) (dnfrepo.CharacterRecord, bool, error) {
	if r.loads.Add(1) == 1 {
		close(r.entered)
		select {
		case <-r.release:
		case <-ctx.Done():
			return dnfrepo.CharacterRecord{}, false, ctx.Err()
		}
	}
	return r.CharacterRepository.Load(ctx, id)
}

type menuPresenceSettlementTripwire struct {
	dnfrepo.CharacterSettlementUnitOfWork
	calls atomic.Int32
}

func (r *menuPresenceSettlementTripwire) WithinCharacterSettlement(context.Context, string, func(dnfrepo.Group) error) error {
	r.calls.Add(1)
	// Observe admission without running any asset mutation or recipe simulation.
	return errors.New("test: menu presence reached asset transaction")
}

func TestRealPVFExpertJobMenuPresenceDuringLoad(t *testing.T) {
	const pvfSHA = "369d072b82c715aea4e02813d89ad9225686983e147bc7be0ee0cae2e296bb06"
	path := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if path == "" {
		t.Skip("set DNFBRIDGE_REAL_PVF_SMOKE to the pinned real PVF")
	}
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	hash := sha256.New()
	_, hashErr := io.Copy(hash, file)
	closeErr := file.Close()
	if hashErr != nil || closeErr != nil {
		t.Fatalf("hash PVF: read=%v close=%v", hashErr, closeErr)
	}
	if got := fmt.Sprintf("%x", hash.Sum(nil)); got != pvfSHA {
		t.Fatalf("PVF SHA=%s want %s", got, pvfSHA)
	}
	archive, err := platformpvf.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	jobs, err := dnfexpertjob.Load(context.Background(), archive)
	if err != nil {
		t.Fatal(err)
	}
	items, err := newPVFDungeonDropCatalog(archive)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("pinned real PVF SHA256=%s", pvfSHA)
	for _, name := range []string{"unchanged_control", "replacement_session", "entry_removed", "town_zero", "town_changed", "area_changed", "channel_changed", "instance_changed"} {
		t.Run(name, func(t *testing.T) {
			repos := dnfrepomemory.NewMemoryGroup()
			character := dnfrepo.CharacterRecord{CharacterID: "72", AccountID: "menu-presence-72", Level: 90,
				Stats: map[string]int64{"gold": 100000, "expert_job_type": int64(dnfexpertjob.EnchanterType), "expert_job_exp": 1195, currentExpertJobMachineEnduranceStat: 173}}
			if err := repos.Character.Save(context.Background(), character); err != nil {
				t.Fatal(err)
			}
			barrier := &menuPresenceLoadBarrier{CharacterRepository: repos.Character, entered: make(chan struct{}), release: make(chan struct{})}
			tripwire := &menuPresenceSettlementTripwire{CharacterSettlementUnitOfWork: repos.CharacterSettlement}
			repos.Character, repos.CharacterSettlement = barrier, tripwire
			conn := &bufferConn{}
			session := &gameSession{conn: conn, selectedCharacterID: 72, accountID: character.AccountID}
			other := &gameSession{conn: &bufferConn{}, selectedCharacterID: 73, accountID: "menu-presence-73"}
			publicStore := &currentExpertJobStore{OwnerCharacterID: 73, OwnerSession: other, Kind: currentExpertJobEnchantStoreKind, TownID: 2, AreaID: 5, Endurance: 99}
			publicStoreBefore := *publicStore
			visitor := &gameSession{selectedCharacterID: 74}
			service := &Service{
				repositoryProvider: func() (dnfrepo.Group, bool) { return repos, true },
				expertJobCatalog:   jobs, pvfItemCatalog: items, initialEquipmentArchive: archive,
				onlinePlayers:     newOnlinePlayerManager(),
				expertJobStores:   map[uint16]*currentExpertJobStore{73: publicStore},
				expertJobVisitors: map[*gameSession]uint16{visitor: 73},
			}
			service.options.gameUpperHeader = gameUpperHeaderChannel13
			service.options.gameUpperBodyCodec = gameUpperBodyCodecPlain
			presence := onlinePlayerInfo{CharacterID: 72, Session: session, TownID: 2, AreaID: 5, ChannelID: 1}
			service.onlinePlayers.EnterArea(&presence)
			body := make([]byte, 13)
			binary.LittleEndian.PutUint16(body[0:2], 72)
			binary.LittleEndian.PutUint32(body[2:6], 10015129)
			body[6] = 2
			binary.LittleEndian.PutUint16(body[8:10], 28)
			binary.LittleEndian.PutUint16(body[11:13], 237)
			done := make(chan error, 1)
			var releaseOnce sync.Once
			release := func() { releaseOnce.Do(func() { close(barrier.release) }) }
			defer release()
			go func() { done <- service.handleCurrentExpertJobStoreEnchant(session, 601, body) }()
			// Timers only bound a broken test; channels define the interleaving.
			select {
			case <-barrier.entered:
			case err := <-done:
				t.Fatalf("handler exited before first Load: %v", err)
			case <-time.After(10 * time.Second):
				t.Fatal("handler did not reach first Load")
			}
			changed := presence
			switch name {
			case "replacement_session":
				changed.Session = &gameSession{selectedCharacterID: 72, accountID: character.AccountID}
			case "town_zero":
				changed.TownID = 0
			case "town_changed":
				changed.TownID++
			case "area_changed":
				changed.AreaID++
			case "channel_changed":
				changed.ChannelID++
			case "instance_changed":
				changed.Instance = 72
			}
			if name == "entry_removed" {
				service.onlinePlayers.LeaveArea(72)
			} else if name != "unchanged_control" {
				service.onlinePlayers.EnterArea(&changed)
			}
			actual, found := service.onlinePlayers.PlayerForCharacter(72)
			if (name == "entry_removed" && found) || (name != "entry_removed" && (!found || !reflect.DeepEqual(actual, changed))) {
				release()
				<-done
				t.Fatalf("presence mutation not installed: found=%t actual=%+v want=%+v", found, actual, changed)
			}
			release()
			select {
			case err := <-done:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(10 * time.Second):
				t.Fatal("handler did not finish after Load release")
			}
			wantCalls := int32(0)
			if name == "unchanged_control" {
				wantCalls = 1
			}
			if got := tripwire.calls.Load(); got != wantCalls {
				t.Errorf("asset transaction calls=%d want=%d after %s during first Load", got, wantCalls, name)
			}
			wire := conn.write.Bytes()
			packet, rest := splitGameServerUpperPacket(t, wire)
			if len(wire) < 3 || binary.LittleEndian.Uint16(wire[1:3]) != 601 || packet.Header.Classification != 1 || !bytes.Equal(packet.Body, []byte{0, 19}) || len(rest) != 0 {
				t.Errorf("want only class1/op601 body0013, got wire=%x", wire)
			}
			service.expertJobStoreMu.Lock()
			storesUnchanged := len(service.expertJobStores) == 1 && service.expertJobStores[73] == publicStore && reflect.DeepEqual(*publicStore, publicStoreBefore)
			visitorsUnchanged := len(service.expertJobVisitors) == 1 && service.expertJobVisitors[visitor] == 73
			service.expertJobStoreMu.Unlock()
			if !storesUnchanged || !visitorsUnchanged {
				t.Errorf("public store/visitor state changed: stores=%t visitors=%t", storesUnchanged, visitorsUnchanged)
			}
			stored, found, err := barrier.CharacterRepository.Load(context.Background(), "72")
			if err != nil || !found || !reflect.DeepEqual(stored, character) {
				t.Errorf("character changed: found=%t err=%v row=%+v", found, err, stored)
			}
			t.Logf("first-Load barrier complete: transaction_calls=%d ACK=%x stores_unchanged=%t visitors_unchanged=%t", tripwire.calls.Load(), packet.Body, storesUnchanged, visitorsUnchanged)
		})
	}
}
