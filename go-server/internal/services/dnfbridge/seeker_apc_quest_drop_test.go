package dnfbridge

import (
	"context"
	"encoding/binary"
	"errors"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/dnfenum"
	"longheng.io/server/internal/modules/dnf/dungeoncmd"
	dnfmonster "longheng.io/server/internal/modules/dnf/monster"
	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
	dnfquest "longheng.io/server/internal/modules/dnf/quest"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	dnfrepomemory "longheng.io/server/internal/modules/dnf/repository/memory"
	"longheng.io/server/internal/modules/dnf/worldmap"
)

const (
	seekerQST022QuestID      int64  = 2949
	seekerQST022APCCode      int64  = 323
	seekerQST022DungeonID    int64  = 42
	seekerQST022ItemID       uint32 = 10164883
	seekerQST022AICAgentPath        = "AICharacter/MissionQuest/Swordman/Orca_MissionQuest/Orca_MissionQuest.aic"
)

type seekerQST022FixtureOptions struct {
	dungeonID  int64
	difficulty int64
	active     bool
	held       int64
	apcCount   int
	maxStack   int64
	dropRate   int64
}

type seekerQST022Fixture struct {
	service      *Service
	session      *gameSession
	runtime      *runtimeDungeonState
	repositories dnfrepo.Group
	connection   *bufferConn
}

func seekerQST022Source(dungeonID int64) bridgePVFSource {
	return seekerQST022SourceOptions(dungeonID, 1, 1, 100)
}

func seekerQST022SourceOptions(dungeonID int64, apcCount int, maxStack int64, dropRate int64) bridgePVFSource {
	mapPath := "map/dungeon/qst022/start.map"
	dungeonPath := "dungeon/qst022.dgn"
	dungeonList := "42 `qst022.dgn`\n"
	if dungeonID != seekerQST022DungeonID {
		dungeonList = "41 `qst022.dgn`\n"
	}
	if apcCount <= 0 {
		apcCount = 1
	}
	if maxStack <= 0 {
		maxStack = 1
	}
	if dropRate <= 0 {
		dropRate = 100
	}
	var aiCharacters strings.Builder
	for i := 0; i < apcCount; i++ {
		aiCharacters.WriteString("323 30 20 0 `[monster]` `[normal]` 0 0\n")
	}
	maxStackString := strconv.FormatInt(maxStack, 10)
	dropRateString := strconv.FormatInt(dropRate, 10)
	return bridgePVFSource{
		worldmap.DefaultMapList: "9242 `" + mapPath + "`\n",
		mapPath: "[map name]\n`利库天井 QST022`\n" +
			"[dungeon]\n" + strconv.FormatInt(dungeonID, 10) + "\n" +
			"[type]\n`[start]`\n" +
			// The ordinary row deliberately shares code 323 with the APC.
			// EnemyRewardItems type 2 must still be selected by actor kind.
			"[monster]\n323 10 0 100 200 0 0 0 `[fixed]` `[normal]`\n" +
			"[ai character]\n" + aiCharacters.String(),
		worldmap.DefaultDungeonList: dungeonList,
		dungeonPath: "[name]\n`QST022 Synthetic`\n" +
			"[minimum required level]\n1\n" +
			"[basis level]\n45\n" +
			"[limit party count]\n1\n" +
			"[maze info]\n" +
			"[size]\n1 1\n" +
			"[greed]\n`A`\n" +
			"[map specification]\n`map` 0 0 9242\n" +
			"[start map]\n0 0\n" +
			"[boss map]\n0 0\n",
		worldmap.DefaultWorldMapList: "1 `qst022.wdm`\n",
		"worldmap/qst022.wdm":        "[name]\n`QST022 Area`\n[dungeon]\n" + strconv.FormatInt(dungeonID, 10) + " -1\n[/dungeon]\n",
		dnfmonster.DefaultList:       "323 `monster/qst022_323.gob`\n",
		"monster/qst022_323.gob": "[name]\n`QST022 ordinary 323`\n" +
			"[level]\n45\n" +
			"[hp]\n500\n" +
			"[exp]\n25\n",
		defaultDungeonAICharacterList: "323 `MissionQuest/Swordman/Orca_MissionQuest/Orca_MissionQuest.aic`\n",
		seekerQST022AICAgentPath: "[minimum info]\n" +
			"`奥尔卡` 0 1 0 0 45 0 0 1 0 0 0 `no creature` 0 0 0\n",
		dnfquest.DefaultList: "2949 `mission_archaeology_03.qst`\n",
		"n_quest/mission_archaeology_03.qst": "[name]\n`山之泪`\n" +
			"[grade]\n`[mission]`\n" +
			"[attribute]\n`not give exp quest`\n[/attribute]\n" +
			"[type]\n`[seeking]`\n" +
			"[int data]\n10164883 1\n" +
			"[level]\n44 99\n" +
			"[enemy reward item]\n" +
			"323 2 42 1 10164883 1 " + dropRateString + " " + maxStackString + "\n" +
			"323 2 42 2 10164883 1 " + dropRateString + " " + maxStackString + "\n" +
			"[/enemy reward item]\n",
	}
}

func seekerQST022QuestCatalog(t *testing.T, source bridgePVFSource) *dnfquest.Catalog {
	t.Helper()
	index, err := dnfpvf.Build(context.Background(), source, dnfpvf.BuildOptions{
		Lists: []string{dnfquest.DefaultList},
	})
	if err != nil {
		t.Fatalf("build QST022 quest index: %v", err)
	}
	catalog, err := dnfquest.Load(context.Background(), index)
	if err != nil {
		t.Fatalf("load QST022 quest catalog: %v", err)
	}
	return catalog
}

func seekerQST022ItemCatalog() *pvfDungeonDropCatalog {
	return &pvfDungeonDropCatalog{
		source: bridgePVFSource{},
		itemCache: map[uint32]dungeonDropItemDefinition{
			seekerQST022ItemID: {
				ItemID:        seekerQST022ItemID,
				Name:          "山之泪",
				Kind:          dungeonDropItemStackable,
				PVFPath:       "stackable/quest/qst022/mountain_tear.stk",
				StackableType: "[quest]",
				StackLimit:    1,
				SlotStart:     177,
				SlotEnd:       232,
			},
		},
	}
}

func newSeekerQST022Fixture(t *testing.T, opts seekerQST022FixtureOptions) *seekerQST022Fixture {
	t.Helper()
	ctx := context.Background()
	source := seekerQST022SourceOptions(opts.dungeonID, opts.apcCount, opts.maxStack, opts.dropRate)
	table, resolver, monsters := loadBridgeDungeonStaticData(t, source)
	aiCharacters, err := newPVFDungeonAICharacterCatalog(source)
	if err != nil {
		t.Fatalf("load QST022 AICharacter catalog: %v", err)
	}
	questCatalog := seekerQST022QuestCatalog(t, source)

	repositories := dnfrepomemory.NewMemoryGroup()
	if err := repositories.Character.Save(ctx, dnfrepo.CharacterRecord{
		CharacterID: "99",
		AccountID:   "account-1",
		Name:        "QST022Actor",
		Job:         "1",
		Level:       45,
		Stats:       map[string]int64{"fatigue": 100},
	}); err != nil {
		t.Fatalf("save QST022 character: %v", err)
	}
	states := map[int64]dnfrepo.QuestState{}
	if opts.active {
		states[seekerQST022QuestID] = dnfrepo.QuestState{Status: "active"}
	}
	if err := repositories.Quest.Save(ctx, dnfrepo.QuestRecord{
		CharacterID: "99",
		States:      states,
	}); err != nil {
		t.Fatalf("save QST022 quest state: %v", err)
	}
	inventory := dnfrepo.InventoryRecord{
		CharacterID: "99",
		Slots:       map[string]dnfrepo.ItemStack{},
	}
	if opts.held > 0 {
		inventory.Slots["0:177"] = dnfrepo.ItemStack{
			ItemID: int64(seekerQST022ItemID),
			Count:  opts.held,
			Extra:  map[string]string{"source": "qst022_fixture"},
		}
	}
	if err := repositories.Inventory.Save(ctx, inventory); err != nil {
		t.Fatalf("save QST022 inventory: %v", err)
	}

	connection := &bufferConn{}
	session := &gameSession{
		conn:                connection,
		connID:              "seeker-qst022-apc-drop",
		selectedCharacterID: 99,
	}
	service := &Service{
		options: options{
			accountID:            "account-1",
			currentClientVersion: "official-24.9",
			gameUpperBodyCodec:   gameUpperBodyCodecPlain,
		},
		worldMapTable:           table,
		worldMapResolver:        resolver,
		dungeonMonsterTable:     monsters,
		dungeonAICharacterTable: aiCharacters,
		questCatalog:            questCatalog,
		pvfItemCatalog:          seekerQST022ItemCatalog(),
		dungeonChoice:           func(int) (int, error) { return 0, nil },
		dungeonSeed:             func() (uint32, error) { return 1, nil },
		repositoryProvider:      func() (dnfrepo.Group, bool) { return repositories, true },
	}
	runtimeState, _, err := service.prepareDungeonRuntime(
		ctx,
		session,
		dungeoncmd.SelectDungeonRequest{
			DungeonID:  uint32(opts.dungeonID),
			Difficulty: byte(opts.difficulty),
		},
	)
	if err != nil {
		t.Fatalf("prepare QST022 dungeon runtime: %v", err)
	}
	session.dungeon.runtime = runtimeState
	if _, err := runtimeState.Room.AnnounceAllActors(runtimeState.Session); err != nil {
		t.Fatalf("announce QST022 room actors: %v", err)
	}
	return &seekerQST022Fixture{
		service:      service,
		session:      session,
		runtime:      runtimeState,
		repositories: repositories,
		connection:   connection,
	}
}

func seekerQST022DeathBody(objectKey uint32) []byte {
	body := make([]byte, dungeoncmd.Official249DieMonsterBaseSize+dungeoncmd.Official249DieMonsterEntrySize)
	binary.LittleEndian.PutUint32(body[0:4], objectKey)
	binary.LittleEndian.PutUint16(body[4:6], currentSceneActorObjectKey(99))
	body[30] = 1
	return body
}

func seekerQST022InventoryHeld(t *testing.T, repositories dnfrepo.Group) int64 {
	t.Helper()
	inventory, found, err := repositories.Inventory.Load(context.Background(), "99")
	if err != nil {
		t.Fatalf("load QST022 inventory: %v", err)
	}
	if !found {
		return 0
	}
	return currentQuestDropHeldCount(inventory, int64(seekerQST022ItemID))
}

func seekerQST022AssertActorIdentity(t *testing.T, fixture *seekerQST022Fixture) (uint32, uint32) {
	t.Helper()
	room := fixture.runtime.Room.Snapshot()
	if len(room.Monsters) != 1 || len(room.ExtendedActors) != 1 {
		t.Fatalf("QST022 room actors: monsters=%+v extended=%+v", room.Monsters, room.ExtendedActors)
	}
	monster := room.Monsters[0]
	apc := room.ExtendedActors[0]
	if monster.Spawn.MonsterID != seekerQST022APCCode ||
		monster.Reference.Kind != worldmap.HostileMonster ||
		apc.Kind != runtimeDungeonActorAICharacter ||
		apc.HostileReference == nil ||
		apc.HostileReference.Kind != worldmap.HostileAICharacter ||
		apc.AICharacter == nil ||
		apc.AICharacter.Code != seekerQST022APCCode ||
		apc.AICharacterMetadata == nil ||
		apc.AICharacterMetadata.Name != "奥尔卡" ||
		apc.AICharacterMetadata.Path != seekerQST022AICAgentPath {
		t.Fatalf("QST022 identities: monster=%+v apc=%+v", monster, apc)
	}
	scene, ok := fixture.runtime.Session.Scene()
	if !ok || scene.RuntimeObjects[apc.ObjectKey] != *apc.HostileReference {
		t.Fatalf("QST022 APC runtime binding: ok=%t object=%d binding=%+v want=%+v",
			ok, apc.ObjectKey, scene.RuntimeObjects[apc.ObjectKey], *apc.HostileReference)
	}
	return monster.ObjectKey, apc.ObjectKey
}

type seekerQST022WirePacket struct {
	msgID uint16
	body  []byte
}

func seekerQST022SendDeath(
	t *testing.T,
	fixture *seekerQST022Fixture,
	objectKey uint32,
) error {
	t.Helper()
	fixture.connection.write.Reset()
	err := fixture.service.handleDungeonMonsterDeath(
		fixture.session,
		seekerQST022DeathBody(objectKey),
	)
	return err
}

func seekerQST022Packets(t *testing.T, data []byte) []seekerQST022WirePacket {
	t.Helper()
	var packets []seekerQST022WirePacket
	for len(data) != 0 {
		packet, rest := splitGameServerUpperPacket(t, data)
		packets = append(packets, seekerQST022WirePacket{
			msgID: packet.Header.MsgID,
			body:  append([]byte(nil), packet.Body...),
		})
		data = rest
	}
	return packets
}

func seekerQST022AssertDeathPackets(
	t *testing.T,
	packets []seekerQST022WirePacket,
	objectKey uint32,
	wantQuestDrop bool,
) {
	t.Helper()
	refreshCount := 0
	deathCount := 0
	for _, packet := range packets {
		switch packet.msgID {
		case uint16(dnfenum.CmdPacketWalkoutPartyMember):
			refresh := packet
			refreshCount++
			if len(refresh.body) != 3+currentItemListEntryWireSize {
				t.Fatalf("QST022 op14 body=%x", refresh.body)
			}
			slot := binary.LittleEndian.Uint16(refresh.body[3:5])
			if slot < 177 || slot > 232 {
				t.Fatalf("QST022 op14 slot=%d body=%x", slot, refresh.body)
			}
			if refresh.body[0] != dnfrepo.MainInventoryListType ||
				binary.LittleEndian.Uint16(refresh.body[1:3]) != 1 ||
				binary.LittleEndian.Uint32(refresh.body[5:9]) != seekerQST022ItemID {
				t.Fatalf("QST022 op14 body=%x", refresh.body)
			}
		case uint16(dnfenum.CmdPacketNotifyDieMonster):
			deathCount++
			if len(packet.body) < 2 ||
				binary.LittleEndian.Uint16(packet.body[:2]) != uint16(objectKey) {
				t.Fatalf("QST022 death ACK object=%d packet=%+v", objectKey, packet)
			}
		}
	}
	wantRefresh := 0
	if wantQuestDrop {
		wantRefresh = 1
	}
	if refreshCount != wantRefresh || deathCount != 1 {
		t.Fatalf("QST022 refresh/death counts=%d/%d want=%d/1 packets=%+v",
			refreshCount, deathCount, wantRefresh, packets)
	}
}

func seekerQST022AssertInventory(t *testing.T, fixture *seekerQST022Fixture, want int64) {
	t.Helper()
	if got := seekerQST022InventoryHeld(t, fixture.repositories); got != want {
		t.Fatalf("QST022 item %d count=%d want=%d", seekerQST022ItemID, got, want)
	}
}

func TestSeekerQST022APCQuestDropDifficulty1And2(t *testing.T) {
	for _, difficulty := range []int64{1, 2} {
		t.Run(strconv.FormatInt(difficulty, 10), func(t *testing.T) {
			fixture := newSeekerQST022Fixture(t, seekerQST022FixtureOptions{
				dungeonID:  seekerQST022DungeonID,
				difficulty: difficulty,
				active:     true,
			})
			_, apcObjectKey := seekerQST022AssertActorIdentity(t, fixture)

			definition, found := fixture.service.questCatalog.Find(seekerQST022QuestID)
			if !found || len(definition.EnemyRewardItems) != 2 {
				t.Fatalf("QST022 parsed definition=%+v", definition)
			}
			item, err := fixture.service.pvfItemCatalog.ResolveItem(seekerQST022ItemID)
			if err != nil || item.ItemID != seekerQST022ItemID ||
				item.Kind != dungeonDropItemStackable || item.StackLimit != 1 {
				t.Fatalf("QST022 item fixture: item=%+v err=%v", item, err)
			}
			if candidates := currentQuestDropMatchMonster(
				fixture.service.questCatalog,
				[]int64{seekerQST022QuestID},
				seekerQST022DungeonID,
				difficulty,
				seekerQST022APCCode,
			); len(candidates) != 0 {
				t.Fatalf("current type-1 matcher unexpectedly returned APC candidates: %+v", candidates)
			}

			if err := seekerQST022SendDeath(t, fixture, apcObjectKey); err != nil {
				t.Fatalf("QST022 op39 object=%d: %v", apcObjectKey, err)
			}
			seekerQST022AssertDeathPackets(
				t,
				seekerQST022Packets(t, fixture.connection.write.Bytes()),
				apcObjectKey,
				true,
			)
			room := fixture.runtime.Room.Snapshot()
			if room.ExtendedActors[0].State != runtimeDungeonMonsterDefeated ||
				room.Monsters[0].State != runtimeDungeonMonsterAnnounced {
				t.Fatalf("QST022 APC death state: extended=%+v monsters=%+v",
					room.ExtendedActors, room.Monsters)
			}
			seekerQST022AssertInventory(t, fixture, 1)
		})
	}
}

func TestSeekerQST022APCQuestDropControls(t *testing.T) {
	tests := []struct {
		name string
		opts seekerQST022FixtureOptions
		kill string
	}{
		{
			name: "ordinary monster with same code",
			opts: seekerQST022FixtureOptions{dungeonID: seekerQST022DungeonID, difficulty: 1, active: true},
			kill: "monster",
		},
		{
			name: "unknown object",
			opts: seekerQST022FixtureOptions{dungeonID: seekerQST022DungeonID, difficulty: 1, active: true},
			kill: "unknown",
		},
		{
			name: "wrong dungeon",
			opts: seekerQST022FixtureOptions{dungeonID: 41, difficulty: 1, active: true},
			kill: "apc",
		},
		{
			name: "wrong difficulty",
			opts: seekerQST022FixtureOptions{dungeonID: seekerQST022DungeonID, difficulty: 0, active: true},
			kill: "apc",
		},
		{
			name: "quest not active",
			opts: seekerQST022FixtureOptions{dungeonID: seekerQST022DungeonID, difficulty: 1, active: false},
			kill: "apc",
		},
		{
			name: "already held at max stack",
			opts: seekerQST022FixtureOptions{dungeonID: seekerQST022DungeonID, difficulty: 1, active: true, held: 1},
			kill: "apc",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newSeekerQST022Fixture(t, test.opts)
			monsterObjectKey, apcObjectKey := seekerQST022AssertActorIdentity(t, fixture)
			before := seekerQST022InventoryHeld(t, fixture.repositories)
			switch test.kill {
			case "monster":
				if err := seekerQST022SendDeath(t, fixture, monsterObjectKey); err != nil {
					t.Fatalf("QST022 ordinary op39: %v", err)
				}
				seekerQST022AssertDeathPackets(
					t,
					seekerQST022Packets(t, fixture.connection.write.Bytes()),
					monsterObjectKey,
					false,
				)
			case "apc":
				if err := seekerQST022SendDeath(t, fixture, apcObjectKey); err != nil {
					t.Fatalf("QST022 APC op39: %v", err)
				}
				seekerQST022AssertDeathPackets(
					t,
					seekerQST022Packets(t, fixture.connection.write.Bytes()),
					apcObjectKey,
					false,
				)
			case "unknown":
				beforeDefeated := len(fixture.runtime.Session.Snapshot().Scene.DefeatedObjects)
				fixture.connection.write.Reset()
				if err := fixture.service.handleDungeonMonsterDeath(
					fixture.session,
					seekerQST022DeathBody(0xfffe),
				); err != nil {
					t.Fatalf("QST022 unknown object op39: %v", err)
				}
				if fixture.connection.write.Len() != 0 ||
					len(fixture.runtime.Session.Snapshot().Scene.DefeatedObjects) != beforeDefeated {
					t.Fatalf("QST022 unknown object changed state: writes=%d defeated=%d/%d",
						fixture.connection.write.Len(),
						len(fixture.runtime.Session.Snapshot().Scene.DefeatedObjects),
						beforeDefeated)
				}
			default:
				t.Fatalf("unknown control action %q", test.kill)
			}
			seekerQST022AssertInventory(t, fixture, before)
			if test.kill == "apc" {
				if len(fixture.runtime.questDropReceipts) != 1 {
					t.Fatalf("resolved no-grant event receipts=%d want=1",
						len(fixture.runtime.questDropReceipts))
				}
				if err := seekerQST022SendDeath(t, fixture, apcObjectKey); err != nil {
					t.Fatal(err)
				}
				if packets := seekerQST022Packets(t, fixture.connection.write.Bytes()); len(packets) != 0 {
					t.Fatalf("resolved no-grant duplicate sent packets: %+v", packets)
				}
				seekerQST022AssertInventory(t, fixture, before)
			}
		})
	}
}

func TestSeekerQST022DuplicateDeathSettlesOnce(t *testing.T) {
	fixture := newSeekerQST022Fixture(t, seekerQST022FixtureOptions{
		dungeonID:  seekerQST022DungeonID,
		difficulty: 1,
		active:     true,
	})
	_, apcObjectKey := seekerQST022AssertActorIdentity(t, fixture)
	if err := seekerQST022SendDeath(t, fixture, apcObjectKey); err != nil {
		t.Fatal(err)
	}
	seekerQST022AssertDeathPackets(
		t,
		seekerQST022Packets(t, fixture.connection.write.Bytes()),
		apcObjectKey,
		true,
	)
	if err := seekerQST022SendDeath(t, fixture, apcObjectKey); err != nil {
		t.Fatal(err)
	}
	if packets := seekerQST022Packets(t, fixture.connection.write.Bytes()); len(packets) != 0 {
		t.Fatalf("successful duplicate sent packets: %+v", packets)
	}
	seekerQST022AssertInventory(t, fixture, 1)
	if len(fixture.runtime.questDropReceipts) != 1 {
		t.Fatalf("QST022 receipts=%d want=1", len(fixture.runtime.questDropReceipts))
	}
}

func TestSeekerQST022ProbabilityMissSettlesOnce(t *testing.T) {
	probe := newSeekerQST022Fixture(t, seekerQST022FixtureOptions{
		dungeonID:  seekerQST022DungeonID,
		difficulty: 1,
		active:     true,
	})
	_, apcObjectKey := seekerQST022AssertActorIdentity(t, probe)
	room := probe.runtime.Room.Snapshot()
	reference := *room.ExtendedActors[0].HostileReference
	enemy, matched := probe.runtime.Room.RewardEnemyByReference(
		reference,
		runtimeDungeonMonsterAnnounced,
	)
	if !matched {
		t.Fatal("QST022 APC reward identity unavailable")
	}
	scene, ok := probe.runtime.Session.Scene()
	if !ok {
		t.Fatal("QST022 scene unavailable")
	}
	var missRate int64
	for n := int64(1); n <= 100 && missRate == 0; n++ {
		probe.runtime.startedAt = time.Unix(0, n)
		key := currentDungeonQuestDropEventKeyFor(probe.session, probe.runtime, scene, apcObjectKey, enemy)
		rollSeed := newCurrentDungeonDropLCG(currentDungeonQuestDropRNGSeed(key)).NextUint32()
		missRate = int64(newCurrentDungeonDropLCG(rollSeed).Next(100))
	}
	if missRate <= 0 {
		t.Fatalf("QST022 deterministic first roll=%d cannot represent a positive-rate miss", missRate)
	}

	fixture := newSeekerQST022Fixture(t, seekerQST022FixtureOptions{
		dungeonID:  seekerQST022DungeonID,
		difficulty: 1,
		active:     true,
		dropRate:   missRate,
	})
	fixture.runtime.startedAt = probe.runtime.startedAt
	_, apcObjectKey = seekerQST022AssertActorIdentity(t, fixture)
	if err := seekerQST022SendDeath(t, fixture, apcObjectKey); err != nil {
		t.Fatal(err)
	}
	seekerQST022AssertDeathPackets(
		t,
		seekerQST022Packets(t, fixture.connection.write.Bytes()),
		apcObjectKey,
		false,
	)
	seekerQST022AssertInventory(t, fixture, 0)
	if len(fixture.runtime.questDropReceipts) != 1 {
		t.Fatalf("probability-miss receipts=%d want=1", len(fixture.runtime.questDropReceipts))
	}
	if err := seekerQST022SendDeath(t, fixture, apcObjectKey); err != nil {
		t.Fatal(err)
	}
	if packets := seekerQST022Packets(t, fixture.connection.write.Bytes()); len(packets) != 0 {
		t.Fatalf("probability-miss duplicate sent packets: %+v", packets)
	}
}

func TestSeekerQST022UsesAuthoritativeAICodeNotPacketMirror(t *testing.T) {
	tests := []struct {
		name          string
		aiCode        int64
		packetCode    uint32
		wantQuestDrop bool
	}{
		{name: "authoritative code", aiCode: seekerQST022APCCode, packetCode: 999, wantQuestDrop: true},
		{name: "packet mirror does not grant", aiCode: 999, packetCode: uint32(seekerQST022APCCode)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newSeekerQST022Fixture(t, seekerQST022FixtureOptions{
				dungeonID:  seekerQST022DungeonID,
				difficulty: 1,
				active:     true,
			})
			_, apcObjectKey := seekerQST022AssertActorIdentity(t, fixture)
			fixture.runtime.Room.mu.Lock()
			index := fixture.runtime.Room.extendedByObjectKey[apcObjectKey]
			fixture.runtime.Room.extendedActors[index].AICharacter.Code = test.aiCode
			fixture.runtime.Room.extendedActors[index].Packet.Code = test.packetCode
			fixture.runtime.Room.mu.Unlock()

			if err := seekerQST022SendDeath(t, fixture, apcObjectKey); err != nil {
				t.Fatal(err)
			}
			seekerQST022AssertDeathPackets(
				t,
				seekerQST022Packets(t, fixture.connection.write.Bytes()),
				apcObjectKey,
				test.wantQuestDrop,
			)
			wantHeld := int64(0)
			if test.wantQuestDrop {
				wantHeld = 1
			}
			seekerQST022AssertInventory(t, fixture, wantHeld)
		})
	}
}

func TestSeekerQST022SameRoomDistinctObjectsAndCrossRoomKeys(t *testing.T) {
	fixture := newSeekerQST022Fixture(t, seekerQST022FixtureOptions{
		dungeonID:  seekerQST022DungeonID,
		difficulty: 1,
		active:     true,
		apcCount:   2,
		maxStack:   2,
	})
	room := fixture.runtime.Room.Snapshot()
	if len(room.ExtendedActors) != 2 {
		t.Fatalf("QST022 extended actors=%+v", room.ExtendedActors)
	}
	first, second := room.ExtendedActors[0].ObjectKey, room.ExtendedActors[1].ObjectKey
	for _, objectKey := range []uint32{first, second} {
		if err := seekerQST022SendDeath(t, fixture, objectKey); err != nil {
			t.Fatalf("QST022 object=%d: %v", objectKey, err)
		}
		seekerQST022AssertDeathPackets(
			t,
			seekerQST022Packets(t, fixture.connection.write.Bytes()),
			objectKey,
			true,
		)
	}
	seekerQST022AssertInventory(t, fixture, 2)
	if len(fixture.runtime.questDropReceipts) != 2 {
		t.Fatalf("same-room receipts=%d want=2", len(fixture.runtime.questDropReceipts))
	}

	scene, ok := fixture.runtime.Session.Scene()
	if !ok {
		t.Fatal("QST022 scene unavailable")
	}
	enemy := runtimeDungeonRewardEnemy{EnemyType: 2, EnemyCode: seekerQST022APCCode}
	firstKey := currentDungeonQuestDropEventKeyFor(fixture.session, fixture.runtime, scene, 403, enemy)
	otherCoordinate := scene
	otherCoordinate.Coordinate.X++
	secondKey := currentDungeonQuestDropEventKeyFor(fixture.session, fixture.runtime, otherCoordinate, 403, enemy)
	if firstKey == secondKey {
		t.Fatalf("cross-room event key collided: %+v", firstKey)
	}
	otherMap := scene
	otherMap.Map.Map.ID++
	thirdKey := currentDungeonQuestDropEventKeyFor(fixture.session, fixture.runtime, otherMap, 403, enemy)
	if firstKey == thirdKey || secondKey == thirdKey {
		t.Fatalf("cross-map event key collided: first=%+v second=%+v third=%+v", firstKey, secondKey, thirdKey)
	}
}

type seekerQST022FailOnceItems struct {
	base   dnfrepo.CharacterItemUnitOfWork
	failed bool
}

func (f *seekerQST022FailOnceItems) WithinCharacterItems(
	ctx context.Context,
	characterID string,
	apply func(dnfrepo.InventoryRepository, dnfrepo.EquipmentRepository) error,
) error {
	if !f.failed {
		f.failed = true
		return f.base.WithinCharacterItems(ctx, characterID, func(
			inventory dnfrepo.InventoryRepository,
			equipment dnfrepo.EquipmentRepository,
		) error {
			if err := apply(inventory, equipment); err != nil {
				return err
			}
			return errors.New("QST022 injected late inventory transaction failure")
		})
	}
	return f.base.WithinCharacterItems(ctx, characterID, apply)
}

func TestSeekerQST022InventoryFailureRetriesWithoutDeathACK(t *testing.T) {
	fixture := newSeekerQST022Fixture(t, seekerQST022FixtureOptions{
		dungeonID:  seekerQST022DungeonID,
		difficulty: 1,
		active:     true,
	})
	_, apcObjectKey := seekerQST022AssertActorIdentity(t, fixture)
	items := &seekerQST022FailOnceItems{base: fixture.repositories.CharacterItems}
	broken := fixture.repositories
	broken.CharacterItems = items
	fixture.service.repositoryProvider = func() (dnfrepo.Group, bool) { return broken, true }

	if err := seekerQST022SendDeath(t, fixture, apcObjectKey); err == nil {
		t.Fatal("inventory transaction failure was accepted")
	}
	if fixture.connection.write.Len() != 0 {
		t.Fatalf("transaction failure sent death success: %x", fixture.connection.write.Bytes())
	}
	seekerQST022AssertInventory(t, fixture, 0)
	if len(fixture.runtime.questDropReceipts) != 0 {
		t.Fatalf("failed transaction recorded receipt: %+v", fixture.runtime.questDropReceipts)
	}

	retryConnection := &bufferConn{}
	fixture.connection = retryConnection
	fixture.session.conn = retryConnection
	if err := seekerQST022SendDeath(t, fixture, apcObjectKey); err != nil {
		t.Fatal(err)
	}
	seekerQST022AssertDeathPackets(
		t,
		seekerQST022Packets(t, retryConnection.write.Bytes()),
		apcObjectKey,
		true,
	)
	seekerQST022AssertInventory(t, fixture, 1)
}

func TestSeekerQST022RefreshFailureReplaysWithoutAnotherGrant(t *testing.T) {
	fixture := newSeekerQST022Fixture(t, seekerQST022FixtureOptions{
		dungeonID:  seekerQST022DungeonID,
		difficulty: 1,
		active:     true,
	})
	_, apcObjectKey := seekerQST022AssertActorIdentity(t, fixture)
	writeErr := errors.New("QST022 injected op14 failure")
	failedConnection := &failNthDungeonWriteConn{failAt: 1, err: writeErr}
	fixture.session.conn = failedConnection
	if err := seekerQST022SendDeath(t, fixture, apcObjectKey); !errors.Is(err, writeErr) {
		t.Fatalf("op14 failure=%v want=%v", err, writeErr)
	}
	seekerQST022AssertInventory(t, fixture, 1)
	if len(fixture.runtime.questDropReceipts) != 1 {
		t.Fatalf("op14 failure receipts=%d want=1", len(fixture.runtime.questDropReceipts))
	}

	retryConnection := &bufferConn{}
	fixture.connection = retryConnection
	fixture.session.conn = retryConnection
	if err := seekerQST022SendDeath(t, fixture, apcObjectKey); err != nil {
		t.Fatal(err)
	}
	seekerQST022AssertDeathPackets(
		t,
		seekerQST022Packets(t, retryConnection.write.Bytes()),
		apcObjectKey,
		true,
	)
	seekerQST022AssertInventory(t, fixture, 1)
}

func TestSeekerQST022DeathACKFailureReplaysWithoutAnotherGrant(t *testing.T) {
	fixture := newSeekerQST022Fixture(t, seekerQST022FixtureOptions{
		dungeonID:  seekerQST022DungeonID,
		difficulty: 1,
		active:     true,
	})
	_, apcObjectKey := seekerQST022AssertActorIdentity(t, fixture)
	writeErr := errors.New("QST022 injected op38 failure")
	failedConnection := &failNthDungeonWriteConn{failAt: 2, err: writeErr}
	fixture.session.conn = failedConnection
	if err := seekerQST022SendDeath(t, fixture, apcObjectKey); !errors.Is(err, writeErr) {
		t.Fatalf("op38 failure=%v want=%v", err, writeErr)
	}
	seekerQST022AssertInventory(t, fixture, 1)
	beforeRetry := seekerQST022Packets(t, failedConnection.bufferConn.write.Bytes())
	if len(beforeRetry) != 1 || beforeRetry[0].msgID != uint16(dnfenum.CmdPacketWalkoutPartyMember) {
		t.Fatalf("op14/op38 failure sequence=%+v", beforeRetry)
	}

	retryConnection := &bufferConn{}
	fixture.connection = retryConnection
	fixture.session.conn = retryConnection
	if err := seekerQST022SendDeath(t, fixture, apcObjectKey); err != nil {
		t.Fatal(err)
	}
	packets := seekerQST022Packets(t, retryConnection.write.Bytes())
	seekerQST022AssertDeathPackets(t, packets, apcObjectKey, false)
	seekerQST022AssertInventory(t, fixture, 1)
}

func TestSeekerQST022ConcurrentDuplicateSettlesOnce(t *testing.T) {
	fixture := newSeekerQST022Fixture(t, seekerQST022FixtureOptions{
		dungeonID:  seekerQST022DungeonID,
		difficulty: 1,
		active:     true,
	})
	_, apcObjectKey := seekerQST022AssertActorIdentity(t, fixture)
	fixture.connection.write.Reset()
	errs := make(chan error, 2)
	var wait sync.WaitGroup
	for i := 0; i < 2; i++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			errs <- fixture.service.handleDungeonMonsterDeath(
				fixture.session,
				seekerQST022DeathBody(apcObjectKey),
			)
		}()
	}
	wait.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	seekerQST022AssertInventory(t, fixture, 1)
	packets := seekerQST022Packets(t, fixture.connection.write.Bytes())
	seekerQST022AssertDeathPackets(t, packets, apcObjectKey, true)
}

func TestSeekerQST022SharedRoomConcurrentCharactersGrantOnceEach(t *testing.T) {
	fixture := newSeekerQST022Fixture(t, seekerQST022FixtureOptions{
		dungeonID:  seekerQST022DungeonID,
		difficulty: 1,
		active:     true,
	})
	_, apcObjectKey := seekerQST022AssertActorIdentity(t, fixture)
	ctx := context.Background()
	if err := fixture.repositories.Character.Save(ctx, dnfrepo.CharacterRecord{
		CharacterID: "100",
		AccountID:   "account-1",
		Name:        "QST022Peer",
		Job:         "1",
		Level:       45,
	}); err != nil {
		t.Fatal(err)
	}
	if err := fixture.repositories.Quest.Save(ctx, dnfrepo.QuestRecord{
		CharacterID: "100",
		States: map[int64]dnfrepo.QuestState{
			seekerQST022QuestID: {Status: "active"},
		},
	}); err != nil {
		t.Fatal(err)
	}
	if err := fixture.repositories.Inventory.Save(ctx, dnfrepo.InventoryRecord{
		CharacterID: "100",
		Slots:       map[string]dnfrepo.ItemStack{},
	}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := fixture.runtime.Room.CommitActorDeathReport(
		apcObjectKey,
		fixture.runtime.Session,
	); err != nil {
		t.Fatal(err)
	}
	peerConnection := &bufferConn{}
	peerSession := &gameSession{
		conn:                peerConnection,
		connID:              "seeker-qst022-apc-drop-peer",
		selectedCharacterID: 100,
	}
	peerRuntime := *fixture.runtime
	peerRuntime.Character = dnfrepo.CharacterRecord{CharacterID: "100"}
	peerRuntime.questDropReceipts = nil

	settleAll := func() error {
		errs := make(chan error, 2)
		var wait sync.WaitGroup
		wait.Add(2)
		go func() {
			defer wait.Done()
			errs <- fixture.service.settleCurrentDungeonRewardEnemyQuestDropLocked(
				fixture.session,
				fixture.runtime,
				apcObjectKey,
			)
		}()
		go func() {
			defer wait.Done()
			errs <- fixture.service.settleCurrentDungeonRewardEnemyQuestDropLocked(
				peerSession,
				&peerRuntime,
				apcObjectKey,
			)
		}()
		wait.Wait()
		close(errs)
		for err := range errs {
			if err != nil {
				return err
			}
		}
		return nil
	}
	if err := settleAll(); err != nil {
		t.Fatal(err)
	}
	if err := settleAll(); err != nil {
		t.Fatal(err)
	}
	if held := seekerQST022InventoryHeld(t, fixture.repositories); held != 1 {
		t.Fatalf("selected character held=%d want=1", held)
	}
	peerInventory, found, err := fixture.repositories.Inventory.Load(ctx, "100")
	if err != nil || !found ||
		currentQuestDropHeldCount(peerInventory, int64(seekerQST022ItemID)) != 1 {
		t.Fatalf("peer inventory=%+v found=%t err=%v", peerInventory, found, err)
	}
	if len(fixture.runtime.questDropReceipts) != 1 || len(peerRuntime.questDropReceipts) != 1 {
		t.Fatalf("receipts selected=%d peer=%d want=1/1",
			len(fixture.runtime.questDropReceipts),
			len(peerRuntime.questDropReceipts))
	}
}

func TestSeekerQST022GrantedItemFinishesQuestByConsumingOne(t *testing.T) {
	fixture := newSeekerQST022Fixture(t, seekerQST022FixtureOptions{
		dungeonID:  seekerQST022DungeonID,
		difficulty: 1,
		active:     true,
	})
	_, apcObjectKey := seekerQST022AssertActorIdentity(t, fixture)
	if err := seekerQST022SendDeath(t, fixture, apcObjectKey); err != nil {
		t.Fatal(err)
	}
	seekerQST022AssertInventory(t, fixture, 1)

	ctx := context.Background()
	acceptedAt := fixture.runtime.startedAt
	if acceptedAt.IsZero() {
		acceptedAt = time.Now().UTC()
	}
	if err := fixture.repositories.Quest.Save(ctx, dnfrepo.QuestRecord{
		CharacterID: "99",
		States: map[int64]dnfrepo.QuestState{
			seekerQST022QuestID: {
				Status:        "active",
				ProgressValue: 0,
				UpdatedAt:     acceptedAt,
				Extra: map[string]string{
					"completion_key":  "seeker-qst022-finish",
					"completion_kind": "clear_map",
					"reward_state":    "pending",
				},
			},
		},
	}); err != nil {
		t.Fatal(err)
	}
	skill := dnfrepo.SkillRecord{CharacterID: "99"}
	skill.Points.SyncedLevel = 45
	if err := fixture.repositories.Skill.Save(ctx, skill); err != nil {
		t.Fatal(err)
	}
	character, found, err := fixture.repositories.Character.Load(ctx, "99")
	if err != nil || !found {
		t.Fatalf("load QST022 character found=%t err=%v", found, err)
	}
	if character.Stats == nil {
		character.Stats = make(map[string]int64)
	}
	character.Stats["exp"] = 0
	if err := fixture.repositories.Character.Save(ctx, character); err != nil {
		t.Fatal(err)
	}
	owner, err := dnfquest.NewOwner(fixture.repositories)
	if err != nil {
		t.Fatal(err)
	}
	result, err := owner.ApplyFinishSettlement(ctx, fixture.service.questCatalog, dnfquest.FinishCommitInput{
		AccountID:    "account-1",
		CharacterID:  "99",
		QuestID:      seekerQST022QuestID,
		Multiplier:   1,
		CommittedAt:  time.Now().UTC(),
		Progression:  finishBridgeCapProgression(t),
		AllocateItem: finishBridgeAllocator(nil),
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.ConsumedItems) != 1 ||
		result.ConsumedItems[0].ItemID != int64(seekerQST022ItemID) ||
		result.ConsumedItems[0].Delta != 1 ||
		result.ConsumedItems[0].PostCount != 0 {
		t.Fatalf("QST022 finish consumed=%+v", result.ConsumedItems)
	}
	seekerQST022AssertInventory(t, fixture, 0)
}

func TestSeekerQST022FixtureKeepsOriginalAICMinimumInfo(t *testing.T) {
	source := seekerQST022Source(seekerQST022DungeonID)
	text := source[seekerQST022AICAgentPath]
	for _, want := range []string{"[minimum info]", "`奥尔卡`", "45"} {
		if !strings.Contains(text, want) {
			t.Fatalf("QST022 AIC fixture missing %q: %q", want, text)
		}
	}
}
