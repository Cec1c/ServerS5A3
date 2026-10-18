package dnfbridge

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/imagecommunication"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	dnftown "longheng.io/server/internal/modules/dnf/town"
)

type imageCommunicationTestSource map[string]string

func (s imageCommunicationTestSource) ReadText(p string) (string, error) {
	v, ok := s[strings.ToLower(p)]
	if !ok {
		return "", fmt.Errorf("missing %s", p)
	}
	return v, nil
}

func newImageCommunicationFixture(t *testing.T, gold int64) (*Service, *gameSession, dnfrepo.Group, *bufferConn) {
	t.Helper()
	s, se, r, conn := newCarryGoldFixture(t, gold, 0)
	// Deliberately use a different source NPC and scene from the real PVF.
	source := imageCommunicationTestSource{
		"etc/imagecommunication.etc": "[image communication]\n[charge]\n10000\n[summon time]\n20000\n[summon pos]\n1274 140 0\n[effect pos]\n1274 210 0\n[npc]\n[npc index]\n2000\n[npc pos]\n5 55\n[require quest]\n3734\n[/npc]\n[/image communication]",
		"npc/npc.lst":                "2000 `target.npc`\n808 `caller.npc`",
		"npc/target.npc":             "[name] `target`",
		"npc/caller.npc":             "[role]\n`[image communication]` 0\n[/role]",
		"town/town.lst":              "71 `example.twn`",
		"town/example.twn":           "[area]\n4 `Town/example.map`\n`[normal]`\n[/area]\n[area]\n5 `Town/empty.map`\n`[normal]`\n[/area]",
		"map/town/example.map":       "[npc]\n808 100 200 0\n[/npc]",
		"map/town/empty.map":         "[map name] `empty`",
	}
	catalog, err := imagecommunication.Load(context.Background(), source)
	if err != nil {
		t.Fatal(err)
	}
	towns, err := dnftown.Load(context.Background(), source, dnftown.Options{})
	if err != nil {
		t.Fatal(err)
	}
	s.imageCommunicationResources = &currentImageCommunicationResources{catalog: catalog, towns: towns, source: source}
	se.connID = "image-test"
	se.loginPacketSeen = true
	s.bindGameSessionCharacter(se, 17)
	setCurrentTownPositionSceneLocked(se, 17, 71, 4)
	se.townSceneReadyCharacterID = 17
	c, _, _ := r.Character.Load(context.Background(), "17")
	c.Stats["town_id"], c.Stats["area_id"] = 71, 4
	if err := r.Character.Save(context.Background(), c); err != nil {
		t.Fatal(err)
	}
	if err := r.Quest.Save(context.Background(), dnfrepo.QuestRecord{CharacterID: "17", States: map[int64]dnfrepo.QuestState{3734: {Status: "active", ProgressValue: 1}}}); err != nil {
		t.Fatal(err)
	}
	return s, se, r, conn
}

func assertImageCommunicationSuccess(t *testing.T, stream []byte, gold uint32) []byte {
	t.Helper()
	p, rest := splitCurrentGameServerUpperPacketAuto(t, stream)
	if p.Header.Classification != 1 || p.Header.MsgID != 475 || len(p.Body) != 9 || p.Body[0] != 1 || binary.LittleEndian.Uint32(p.Body[1:]) != 10000 || binary.LittleEndian.Uint32(p.Body[5:]) != gold {
		t.Fatalf("475 reply=%+v %x", p.Header, p.Body)
	}
	return rest
}

func TestImageCommunicationDispatchAndConcurrentReplay(t *testing.T) {
	for _, transport := range []string{"upper", "legacy"} {
		t.Run(transport, func(t *testing.T) {
			s, se, r, conn := newImageCommunicationFixture(t, 20000)
			var wg sync.WaitGroup
			for i := 0; i < 12; i++ {
				wg.Go(func() {
					var handled bool
					var err error
					if transport == "legacy" {
						handled, err = currentGameplayModules.DispatchLegacy(s, se, 475, nil)
					} else {
						handled, err = currentGameplayModules.DispatchUpper(s, se, 475, 1, nil)
					}
					if !handled || err != nil {
						t.Errorf("handled=%v err=%v", handled, err)
					}
				})
			}
			wg.Wait()
			stream := conn.write.Bytes()
			for i := 0; i < 12; i++ {
				stream = assertImageCommunicationSuccess(t, stream, 10000)
			}
			if len(stream) != 0 {
				t.Fatal("extra packets")
			}
			c, _, _ := r.Character.Load(context.Background(), "17")
			if c.Stats["gold"] != 10000 {
				t.Fatal("duplicate charge")
			}
			q, _, _ := r.Quest.Load(context.Background(), "17")
			if q.States[3734].Status != "active" || q.States[3734].ProgressValue != 1 {
				t.Fatalf("quest changed=%+v", q)
			}
		})
	}
}

func TestImageCommunicationRefusals(t *testing.T) {
	for _, name := range []string{"class0", "body", "no-login", "no-character", "wrong-owner", "stale-session", "not-ready", "snapshot-owner", "persisted-scene", "no-source", "no-active", "completed", "dungeon", "funds", "uow-missing", "config-missing"} {
		t.Run(name, func(t *testing.T) {
			s, se, r, conn := newImageCommunicationFixture(t, 20000)
			var body []byte
			class := byte(1)
			c, _, _ := r.Character.Load(context.Background(), "17")
			q, _, _ := r.Quest.Load(context.Background(), "17")
			switch name {
			case "class0":
				class = 0
			case "body":
				body = []byte{0xd0, 7, 0, 0}
			case "no-login":
				se.loginPacketSeen = false
			case "no-character":
				se.selectedCharacterID = 0
			case "wrong-owner":
				c.AccountID = "other"
			case "stale-session":
				s.gameSessions[17] = &gameSession{}
			case "not-ready":
				se.townSceneReadyCharacterID = 0
			case "snapshot-owner":
				se.townPositionSnapshot.CharacterID = 18
			case "persisted-scene":
				c.Stats["town_id"] = 14
			case "no-source":
				setCurrentTownPositionSceneLocked(se, 17, 71, 5)
				c.Stats["area_id"] = 5
			case "no-active":
				q.States = nil
			case "completed":
				q.States[3734] = dnfrepo.QuestState{Status: "completed"}
			case "dungeon":
				se.dungeon.runtime = &runtimeDungeonState{}
			case "funds":
				c.Stats["gold"] = 9999
			case "uow-missing":
				rr := r
				rr.CharacterAssets = nil
				s.repositoryProvider = func() (dnfrepo.Group, bool) { return rr, true }
			case "config-missing":
				s.imageCommunicationResources.catalog = nil
			}
			if err := r.Character.Save(context.Background(), c); err != nil {
				t.Fatal(err)
			}
			if err := r.Quest.Save(context.Background(), q); err != nil {
				t.Fatal(err)
			}
			if err := s.handleGameCommand(se, class, 475, body); err != nil {
				t.Fatal(err)
			}
			after, _, _ := r.Character.Load(context.Background(), "17")
			if !reflect.DeepEqual(c, after) {
				t.Fatal("rejected request mutated wallet")
			}
			if name == "class0" {
				if conn.write.Len() != 0 {
					t.Fatal("wrong class reply")
				}
				return
			}
			p, rest := splitCurrentGameServerUpperPacketAuto(t, conn.write.Bytes())
			code := byte(2)
			if name == "funds" {
				code = 10
			}
			if p.Header.Classification != 1 || p.Header.MsgID != 475 || !reflect.DeepEqual(p.Body, []byte{0, code}) {
				t.Fatalf("failure=%+v %x", p.Header, p.Body)
			}
			if name == "funds" {
				wallet, tail := splitCurrentGameServerUpperPacketAuto(t, rest)
				if wallet.Header.MsgID != 14 || wallet.Header.Classification != 0 || len(tail) != 0 {
					t.Fatal("missing wallet repair")
				}
			} else if len(rest) != 0 {
				t.Fatal("extra refusal packets")
			}
		})
	}
}

type imageCommunicationFailConn struct {
	bufferConn
	fail bool
}

func (c *imageCommunicationFailConn) Write(p []byte) (int, error) {
	if c.fail {
		return 0, errors.New("injected socket failure")
	}
	return c.bufferConn.Write(p)
}

func TestImageCommunicationSendFailureAndChangedWalletRecovery(t *testing.T) {
	s, se, r, _ := newImageCommunicationFixture(t, 20000)
	conn := &imageCommunicationFailConn{fail: true}
	se.conn = conn
	if err := s.handleCurrentImageCommunication(se, nil); err == nil {
		t.Fatal("send failure missing")
	}
	c, _, _ := r.Character.Load(context.Background(), "17")
	if c.Stats["gold"] != 10000 {
		t.Fatal("debit not committed")
	}
	c.Stats["gold"] += 23
	if err := r.Character.Save(context.Background(), c); err != nil {
		t.Fatal(err)
	}
	conn.fail = false
	if err := s.handleCurrentImageCommunication(se, nil); err != nil {
		t.Fatal(err)
	}
	rest := assertImageCommunicationSuccess(t, conn.write.Bytes(), 10000)
	wallet, tail := splitCurrentGameServerUpperPacketAuto(t, rest)
	if wallet.Header.MsgID != 14 || wallet.Header.Classification != 0 || len(tail) != 0 {
		t.Fatal("current wallet not reprojected")
	}
	after, _, _ := r.Character.Load(context.Background(), "17")
	if after.Stats["gold"] != 10023 {
		t.Fatal("recovery charged again")
	}
}

func TestImageCommunicationSceneExpiryAndReconnect(t *testing.T) {
	s, se, r, conn := newImageCommunicationFixture(t, 100000)
	now := time.Date(2026, 10, 18, 1, 0, 0, 0, time.UTC)
	s.gameplayTimers = &processGameplayTimeQueue{now: func() time.Time { return now }}
	use := func(se *gameSession, conn *bufferConn, want uint32) {
		t.Helper()
		conn.write.Reset()
		if err := s.handleCurrentImageCommunication(se, nil); err != nil {
			t.Fatal(err)
		}
		if len(assertImageCommunicationSuccess(t, conn.write.Bytes(), want)) != 0 {
			t.Fatal("extra reply")
		}
	}
	use(se, conn, 90000)
	use(se, conn, 90000)
	// Motion stays in the same scene; reopening before expiry reuses permit.
	se.townPositionSnapshot.PositionX++
	now = now.Add(imagecommunication.PermitLifetime - time.Millisecond)
	use(se, conn, 90000)
	now = now.Add(time.Millisecond)
	use(se, conn, 80000)
	setCurrentTownPositionSceneLocked(se, 17, 71, 5)
	setCurrentTownPositionSceneLocked(se, 17, 71, 4)
	use(se, conn, 70000)
	resetDungeonEntrySceneGates(se)
	se.townSceneReadyCharacterID = 17
	use(se, conn, 60000)
	newConn := &bufferConn{}
	reconnect := &gameSession{conn: newConn, connID: "reconnect", accountID: se.accountID, loginPacketSeen: true}
	s.bindGameSessionCharacter(reconnect, 17)
	setCurrentTownPositionSceneLocked(reconnect, 17, 71, 4)
	reconnect.townSceneReadyCharacterID = 17
	use(reconnect, newConn, 50000)
	conn.write.Reset()
	if err := s.handleCurrentImageCommunication(se, nil); err != nil {
		t.Fatal(err)
	}
	p, _ := splitCurrentGameServerUpperPacketAuto(t, conn.write.Bytes())
	if !reflect.DeepEqual(p.Body, []byte{0, 2}) {
		t.Fatal("retired connection reused permit")
	}
	c, _, _ := r.Character.Load(context.Background(), "17")
	if c.Stats["gold"] != 50000 {
		t.Fatalf("gold=%d", c.Stats["gold"])
	}
	// Switching characters must never carry the former character's receipt.
	if err := r.Character.Save(context.Background(), dnfrepo.CharacterRecord{CharacterID: "18", AccountID: reconnect.accountID, Level: 90, Stats: map[string]int64{"gold": 30000, "town_id": 71, "area_id": 4}}); err != nil {
		t.Fatal(err)
	}
	if err := r.Quest.Save(context.Background(), dnfrepo.QuestRecord{CharacterID: "18", States: map[int64]dnfrepo.QuestState{3734: {Status: "active"}}}); err != nil {
		t.Fatal(err)
	}
	s.bindGameSessionCharacter(reconnect, 18)
	setCurrentTownPositionSceneLocked(reconnect, 18, 71, 4)
	reconnect.townSceneReadyCharacterID = 18
	use(reconnect, newConn, 20000)
	s.bindGameSessionCharacter(reconnect, 17)
	setCurrentTownPositionSceneLocked(reconnect, 17, 71, 4)
	reconnect.townSceneReadyCharacterID = 17
	use(reconnect, newConn, 40000)
}
