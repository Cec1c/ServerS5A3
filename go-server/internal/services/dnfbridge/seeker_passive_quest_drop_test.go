package dnfbridge

import (
	"context"
	"encoding/binary"
	"errors"
	"strconv"
	"testing"

	"longheng.io/server/internal/modules/dnf/channelcatalog"
	"longheng.io/server/internal/modules/dnf/dnfenum"
	"longheng.io/server/internal/modules/dnf/dungeoncmd"
	dnfmonster "longheng.io/server/internal/modules/dnf/monster"
	dnfproto "longheng.io/server/internal/modules/dnf/protocol"
	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
	dnfquest "longheng.io/server/internal/modules/dnf/quest"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	dnfrepomemory "longheng.io/server/internal/modules/dnf/repository/memory"
	"longheng.io/server/internal/modules/dnf/worldmap"
)

const (
	seekerPassiveDropMapID     int64 = 9100
	seekerPassiveDropMapID2    int64 = 9101
	seekerPassiveDropCharacter int64 = 99
)

type seekerPassiveDropCase struct {
	code       int64
	questID    int64
	itemID     uint32
	dungeonID  int64
	difficulty int64
}

var (
	seekerPassiveDrop16111 = seekerPassiveDropCase{
		code: 16111, questID: 3735, itemID: 10164801, dungeonID: 102, difficulty: 1,
	}
	seekerPassiveDrop16112 = seekerPassiveDropCase{
		code: 16112, questID: 3735, itemID: 10164801, dungeonID: 102, difficulty: 1,
	}
	seekerPassiveDrop85176 = seekerPassiveDropCase{
		code: 85176, questID: 3954, itemID: 10164804, dungeonID: 310, difficulty: 1,
	}
)

type seekerPassiveDropFixtureOptions struct {
	drop             seekerPassiveDropCase
	active           bool
	held             int64
	maxStack         int64
	dropRate         int64
	passive          []int64
	special          []int64
	twoRooms         bool
	difficulty       int64
	rewardDungeonID  int64
	rewardDifficulty int64
}

type seekerPassiveDropFixture struct {
	drop         seekerPassiveDropCase
	maxStack     int64
	service      *Service
	session      *gameSession
	runtime      *runtimeDungeonState
	repositories dnfrepo.Group
	connection   *bufferConn
}

func seekerPassiveDropSource(opts seekerPassiveDropFixtureOptions) bridgePVFSource {
	drop := opts.drop
	if opts.difficulty <= 0 {
		opts.difficulty = 1
	}
	if opts.maxStack <= 0 {
		opts.maxStack = 15
	}
	if opts.dropRate <= 0 {
		opts.dropRate = 100
	}
	rewardDungeonID := opts.rewardDungeonID
	if rewardDungeonID <= 0 {
		rewardDungeonID = drop.dungeonID
	}
	rewardDifficulty := opts.rewardDifficulty
	if rewardDifficulty <= 0 {
		rewardDifficulty = opts.difficulty
	}
	if len(opts.passive) == 0 {
		opts.passive = []int64{drop.code}
	}

	var startObjects string
	for _, code := range opts.passive {
		startObjects += strconv.FormatInt(code, 10) + " 100 200 0\n"
	}
	var startSpecial string
	for _, code := range opts.special {
		startSpecial += strconv.FormatInt(code, 10) + " 300 200 0\n"
	}

	mapList := strconv.FormatInt(seekerPassiveDropMapID, 10) + " `map/seeker_passive/start.map`\n"
	dungeonMapSpec := "`map` 0 0 " + strconv.FormatInt(seekerPassiveDropMapID, 10)
	mazeSize := "1 1"
	mazeGreed := "`A`"
	bossMap := "0 0"
	if opts.twoRooms {
		mapList += strconv.FormatInt(seekerPassiveDropMapID2, 10) + " `map/seeker_passive/next.map`\n"
		dungeonMapSpec += " `normal` 1 0 " + strconv.FormatInt(seekerPassiveDropMapID2, 10)
		mazeSize = "2 1"
		mazeGreed = "`AA`"
		bossMap = "1 0"
	}

	source := bridgePVFSource{
		worldmap.DefaultMapList: mapList,
		"map/seeker_passive/start.map": "[map name]\n`seeker passive start`\n" +
			"[dungeon]\n" + strconv.FormatInt(drop.dungeonID, 10) + "\n" +
			"[type]\n`[start]`\n" +
			"[passive object]\n" + startObjects +
			"[special passive object]\n" + startSpecial,
		"map/seeker_passive/next.map": "[map name]\n`seeker passive next`\n" +
			"[dungeon]\n" + strconv.FormatInt(drop.dungeonID, 10) + "\n" +
			"[type]\n`[normal]`\n" +
			"[passive object]\n" + startObjects +
			"[special passive object]\n" + startSpecial,
		worldmap.DefaultDungeonList: strconv.FormatInt(drop.dungeonID, 10) + " `seeker_passive.dgn`\n",
		"dungeon/seeker_passive.dgn": "[name]\n`Seeker Passive Quest Drop`\n" +
			"[minimum required level]\n1\n" +
			"[basis level]\n20\n" +
			"[limit party count]\n1\n" +
			"[maze info]\n" +
			"[size]\n" + mazeSize + "\n" +
			"[greed]\n" + mazeGreed + "\n" +
			"[map specification]\n" + dungeonMapSpec + "\n" +
			"[start map]\n0 0\n" +
			"[boss map]\n" + bossMap + "\n",
		worldmap.DefaultWorldMapList: "1 `seeker_passive.wdm`\n",
		"worldmap/seeker_passive.wdm": "[name]\n`Seeker Passive Area`\n" +
			"[dungeon]\n" + strconv.FormatInt(drop.dungeonID, 10) + " -1\n[/dungeon]\n",
		dnfmonster.DefaultList: "1 `seeker_passive_dummy.gob`\n",
		"monster/seeker_passive_dummy.gob": "[name]\n`Seeker Passive Dummy`\n" +
			"[level]\n1\n[hp]\n1\n[exp]\n0\n",
		dnfquest.DefaultList: strconv.FormatInt(drop.questID, 10) + " `seeker_passive.qst`\n",
		"n_quest/seeker_passive.qst": "[name]\n`Seeker Passive Drop`\n" +
			"[grade]\n`[mission]`\n" +
			"[type]\n`[seeking]`\n" +
			"[int data]\n" + strconv.FormatUint(uint64(drop.itemID), 10) + " " +
			strconv.FormatInt(opts.maxStack, 10) + "\n" +
			"[enemy reward item]\n" +
			strconv.FormatInt(drop.code, 10) + " 3 " +
			strconv.FormatInt(rewardDungeonID, 10) + " " +
			strconv.FormatInt(rewardDifficulty, 10) + " " +
			strconv.FormatUint(uint64(drop.itemID), 10) + " 1 " +
			strconv.FormatInt(opts.dropRate, 10) + " " +
			strconv.FormatInt(opts.maxStack, 10) + "\n" +
			"[/enemy reward item]\n",
	}
	return source
}

func seekerPassiveDropItemCatalog(drop seekerPassiveDropCase, maxStack int64) *pvfDungeonDropCatalog {
	if maxStack <= 0 {
		maxStack = 15
	}
	return &pvfDungeonDropCatalog{
		source: bridgePVFSource{},
		itemCache: map[uint32]dungeonDropItemDefinition{
			drop.itemID: {
				ItemID:        drop.itemID,
				Name:          "Seeker Passive Quest Item",
				Kind:          dungeonDropItemStackable,
				PVFPath:       "stackable/quest/seeker_passive/item.stk",
				StackableType: "[quest]",
				StackLimit:    maxStack,
				SlotStart:     177,
				SlotEnd:       232,
			},
		},
	}
}

func newSeekerPassiveDropFixture(t *testing.T, opts seekerPassiveDropFixtureOptions) *seekerPassiveDropFixture {
	t.Helper()
	ctx := context.Background()
	source := seekerPassiveDropSource(opts)
	table, resolver, monsters := loadBridgeDungeonStaticData(t, source)
	index, err := dnfpvf.Build(ctx, source, dnfpvf.BuildOptions{Lists: []string{dnfquest.DefaultList}})
	if err != nil {
		t.Fatalf("build passive quest PVF: %v", err)
	}
	questCatalog, err := dnfquest.Load(ctx, index)
	if err != nil {
		t.Fatalf("load passive quest PVF: %v", err)
	}

	repositories := dnfrepomemory.NewMemoryGroup()
	if err := repositories.Character.Save(ctx, dnfrepo.CharacterRecord{
		CharacterID: strconv.FormatInt(seekerPassiveDropCharacter, 10),
		AccountID:   "seeker-passive-account",
		Name:        "SeekerPassive",
		Job:         "1",
		Level:       45,
		Stats:       map[string]int64{"fatigue": 100},
	}); err != nil {
		t.Fatalf("save passive quest character: %v", err)
	}
	states := map[int64]dnfrepo.QuestState{}
	if opts.active {
		states[opts.drop.questID] = dnfrepo.QuestState{Status: "active"}
	}
	if err := repositories.Quest.Save(ctx, dnfrepo.QuestRecord{
		CharacterID: strconv.FormatInt(seekerPassiveDropCharacter, 10),
		States:      states,
	}); err != nil {
		t.Fatalf("save passive quest state: %v", err)
	}
	inventory := dnfrepo.InventoryRecord{
		CharacterID: strconv.FormatInt(seekerPassiveDropCharacter, 10),
		Slots:       map[string]dnfrepo.ItemStack{},
	}
	if opts.held > 0 {
		inventory.Slots["0:177"] = dnfrepo.ItemStack{
			ItemID: int64(opts.drop.itemID),
			Count:  opts.held,
			Extra:  map[string]string{"source": "seeker_passive_fixture"},
		}
	}
	if err := repositories.Inventory.Save(ctx, inventory); err != nil {
		t.Fatalf("save passive quest inventory: %v", err)
	}

	connection := &bufferConn{}
	session := &gameSession{
		conn:                connection,
		connID:              "seeker-passive-quest-drop",
		channel:             channelcatalog.Channel{ID: 19},
		selectedCharacterID: uint16(seekerPassiveDropCharacter),
	}
	service := &Service{
		options: options{
			accountID:            "seeker-passive-account",
			currentClientVersion: "official-24.9",
			gameUpperBodyCodec:   gameUpperBodyCodecPlain,
		},
		worldMapTable:       table,
		worldMapResolver:    resolver,
		dungeonMonsterTable: monsters,
		questCatalog:        questCatalog,
		pvfItemCatalog:      seekerPassiveDropItemCatalog(opts.drop, opts.maxStack),
		dungeonChoice:       func(int) (int, error) { return 0, nil },
		dungeonSeed:         func() (uint32, error) { return 1, nil },
		repositoryProvider:  func() (dnfrepo.Group, bool) { return repositories, true },
	}
	difficulty := opts.difficulty
	if difficulty <= 0 {
		difficulty = 1
	}
	runtimeState, _, err := service.prepareDungeonRuntime(
		ctx,
		session,
		dungeoncmd.SelectDungeonRequest{
			DungeonID:  uint32(opts.drop.dungeonID),
			Difficulty: byte(difficulty),
		},
	)
	if err != nil {
		t.Fatalf("prepare passive quest dungeon: %v", err)
	}
	session.dungeon.runtime = runtimeState
	if _, err := runtimeState.Room.AnnounceAllActors(runtimeState.Session); err != nil {
		t.Fatalf("announce passive quest room: %v", err)
	}
	return &seekerPassiveDropFixture{
		drop:         opts.drop,
		maxStack:     opts.maxStack,
		service:      service,
		session:      session,
		runtime:      runtimeState,
		repositories: repositories,
		connection:   connection,
	}
}

func seekerPassiveDropNativeBody(code int64) []byte {
	body := make([]byte, dungeoncmd.Official249DieMonsterBaseSize)
	binary.LittleEndian.PutUint32(body[0:4], uint32(code))
	binary.LittleEndian.PutUint16(body[4:6], ^uint16(0))
	body[30] = 0
	body[37] = 1
	return body
}

func seekerPassiveDropSendFormal(
	t *testing.T,
	fixture *seekerPassiveDropFixture,
	sequence uint32,
	class byte,
	body []byte,
) error {
	t.Helper()
	frame, err := dnfproto.BuildChannelPacket(
		uint16(dnfenum.CmdPacketDieMonster),
		body,
		uint16(sequence),
		class,
	)
	if err != nil {
		t.Fatalf("build formal passive op39 frame: %v", err)
	}
	return fixture.service.handleGameUpper(fixture.session, frame)
}

func seekerPassiveDropHeld(t *testing.T, fixture *seekerPassiveDropFixture) int64 {
	t.Helper()
	inventory, found, err := fixture.repositories.Inventory.Load(
		context.Background(),
		strconv.FormatInt(seekerPassiveDropCharacter, 10),
	)
	if err != nil {
		t.Fatalf("load passive quest inventory: %v", err)
	}
	if !found {
		return 0
	}
	return currentQuestDropHeldCount(inventory, int64(fixture.drop.itemID))
}

func TestSeekerPassiveQuestDropFormalEntryAllRealCodes(t *testing.T) {
	tests := []seekerPassiveDropCase{
		seekerPassiveDrop16111,
		seekerPassiveDrop16112,
		seekerPassiveDrop85176,
	}
	for _, drop := range tests {
		t.Run(strconv.FormatInt(drop.code, 10), func(t *testing.T) {
			fixture := newSeekerPassiveDropFixture(t, seekerPassiveDropFixtureOptions{
				drop: drop, active: true, maxStack: 15,
			})
			if err := seekerPassiveDropSendFormal(
				t,
				fixture,
				100,
				dnfproto.DefaultChannelClassification,
				seekerPassiveDropNativeBody(drop.code),
			); err != nil {
				t.Fatalf("formal passive op39: %v", err)
			}
			if held := seekerPassiveDropHeld(t, fixture); held != 1 {
				t.Fatalf("passive code=%d held=%d want=1", drop.code, held)
			}
		})
	}
}

func seekerPassiveDropRefreshCount(t *testing.T, data []byte) int {
	t.Helper()
	count := 0
	for len(data) != 0 {
		packet, rest := splitGameServerUpperPacket(t, data)
		if packet.Header.MsgID != uint16(dnfenum.CmdPacketWalkoutPartyMember) {
			t.Fatalf(
				"passive quest drop emitted unexpected response opcode=%d body=%x",
				packet.Header.MsgID,
				packet.Body,
			)
		}
		count++
		data = rest
	}
	return count
}

func seekerPassiveDropSendReset(
	t *testing.T,
	fixture *seekerPassiveDropFixture,
	sequence uint32,
	body []byte,
) (int, error) {
	t.Helper()
	fixture.connection.write.Reset()
	err := seekerPassiveDropSendFormal(
		t,
		fixture,
		sequence,
		dnfproto.DefaultChannelClassification,
		body,
	)
	return seekerPassiveDropRefreshCount(t, fixture.connection.write.Bytes()), err
}

func TestSeekerPassiveQuestDropNormalAndSpecialObjectsUseRoomQuota(t *testing.T) {
	drop := seekerPassiveDrop16111
	fixture := newSeekerPassiveDropFixture(t, seekerPassiveDropFixtureOptions{
		drop:     drop,
		active:   true,
		maxStack: 15,
		passive:  []int64{drop.code, drop.code},
		special:  []int64{drop.code},
	})
	body := seekerPassiveDropNativeBody(drop.code)
	for index, sequence := range []uint32{100, 101, 102} {
		refreshes, err := seekerPassiveDropSendReset(t, fixture, sequence, body)
		if err != nil {
			t.Fatalf("sequence=%d: %v", sequence, err)
		}
		if refreshes != 1 {
			t.Fatalf("sequence=%d refreshes=%d want=1", sequence, refreshes)
		}
		if held := seekerPassiveDropHeld(t, fixture); held != int64(index+1) {
			t.Fatalf("sequence=%d held=%d want=%d", sequence, held, index+1)
		}
	}
	refreshes, err := seekerPassiveDropSendReset(t, fixture, 103, body)
	if err != nil {
		t.Fatal(err)
	}
	if refreshes != 0 || seekerPassiveDropHeld(t, fixture) != 3 {
		t.Fatalf("source overage refreshes=%d held=%d", refreshes, seekerPassiveDropHeld(t, fixture))
	}
}

func TestSeekerPassiveQuestDropReplayVariantAndSameBodyNewSequence(t *testing.T) {
	drop := seekerPassiveDrop16112
	fixture := newSeekerPassiveDropFixture(t, seekerPassiveDropFixtureOptions{
		drop:     drop,
		active:   true,
		maxStack: 15,
		passive:  []int64{drop.code, drop.code},
	})
	original := seekerPassiveDropNativeBody(drop.code)
	if refreshes, err := seekerPassiveDropSendReset(t, fixture, 100, original); err != nil || refreshes != 1 {
		t.Fatalf("first event refreshes=%d err=%v", refreshes, err)
	}
	if refreshes, err := seekerPassiveDropSendReset(t, fixture, 100, original); err != nil || refreshes != 0 {
		t.Fatalf("same sequence replay refreshes=%d err=%v", refreshes, err)
	}
	variant := append([]byte(nil), original...)
	variant[39] ^= 0x5a
	if refreshes, err := seekerPassiveDropSendReset(t, fixture, 100, variant); err != nil || refreshes != 0 {
		t.Fatalf("same sequence variant refreshes=%d err=%v", refreshes, err)
	}
	if refreshes, err := seekerPassiveDropSendReset(t, fixture, 101, original); err != nil || refreshes != 1 {
		t.Fatalf("same body new sequence refreshes=%d err=%v", refreshes, err)
	}
	if held := seekerPassiveDropHeld(t, fixture); held != 2 {
		t.Fatalf("held=%d want=2", held)
	}
}

func TestSeekerPassiveQuestDropOldSequenceAndCrossRoomRejected(t *testing.T) {
	drop := seekerPassiveDrop85176
	fixture := newSeekerPassiveDropFixture(t, seekerPassiveDropFixtureOptions{
		drop:     drop,
		active:   true,
		maxStack: 10,
		passive:  []int64{drop.code, drop.code},
		twoRooms: true,
	})
	body := seekerPassiveDropNativeBody(drop.code)
	if refreshes, err := seekerPassiveDropSendReset(t, fixture, 100, body); err != nil || refreshes != 1 {
		t.Fatalf("first room event refreshes=%d err=%v", refreshes, err)
	}
	if refreshes, err := seekerPassiveDropSendReset(t, fixture, 99, body); err != nil || refreshes != 0 {
		t.Fatalf("unseen old sequence refreshes=%d err=%v", refreshes, err)
	}
	if _, err := fixture.runtime.Session.MoveByte(1, 0); err != nil {
		t.Fatalf("move to second passive room: %v", err)
	}
	if refreshes, err := seekerPassiveDropSendReset(t, fixture, 100, body); err != nil || refreshes != 0 {
		t.Fatalf("cross-room replay refreshes=%d err=%v", refreshes, err)
	}
	if held := seekerPassiveDropHeld(t, fixture); held != 1 {
		t.Fatalf("held=%d want=1", held)
	}
}

func TestSeekerPassiveQuestDropRoomQuotaSurvivesLeavingAndRevisit(t *testing.T) {
	drop := seekerPassiveDrop16111
	fixture := newSeekerPassiveDropFixture(t, seekerPassiveDropFixtureOptions{
		drop:     drop,
		active:   true,
		maxStack: 15,
		twoRooms: true,
	})
	startScene, ok := fixture.runtime.Session.Scene()
	if !ok {
		t.Fatal("missing passive start scene")
	}
	body := seekerPassiveDropNativeBody(drop.code)
	if refreshes, err := seekerPassiveDropSendReset(t, fixture, 100, body); err != nil || refreshes != 1 {
		t.Fatalf("start room event refreshes=%d err=%v", refreshes, err)
	}
	if _, err := fixture.runtime.Session.MoveByte(1, 0); err != nil {
		t.Fatalf("move to second passive room: %v", err)
	}
	if refreshes, err := seekerPassiveDropSendReset(t, fixture, 101, body); err != nil || refreshes != 1 {
		t.Fatalf("second room event refreshes=%d err=%v", refreshes, err)
	}
	if _, err := fixture.runtime.Session.MoveByteTransition(0, 0, &startScene); err != nil {
		t.Fatalf("revisit passive start room: %v", err)
	}
	if refreshes, err := seekerPassiveDropSendReset(t, fixture, 102, body); err != nil || refreshes != 0 {
		t.Fatalf("revisited room overage refreshes=%d err=%v", refreshes, err)
	}
	if held := seekerPassiveDropHeld(t, fixture); held != 2 {
		t.Fatalf("held=%d want=2", held)
	}
}

func TestSeekerPassiveQuestDropRejectsShapeProfileClassTaskAndPlacement(t *testing.T) {
	drop := seekerPassiveDrop16111
	tests := []struct {
		name    string
		options seekerPassiveDropFixtureOptions
		mutate  func(body []byte)
		class   byte
		profile string
	}{
		{
			name:    "wrong profile",
			options: seekerPassiveDropFixtureOptions{drop: drop, active: true, maxStack: 15},
			profile: "legacy",
		},
		{
			name:    "wrong class",
			options: seekerPassiveDropFixtureOptions{drop: drop, active: true, maxStack: 15},
			class:   2,
		},
		{
			name:    "wrong owner",
			options: seekerPassiveDropFixtureOptions{drop: drop, active: true, maxStack: 15},
			mutate: func(body []byte) {
				binary.LittleEndian.PutUint16(body[4:6], 1)
			},
		},
		{
			name:    "nonzero count",
			options: seekerPassiveDropFixtureOptions{drop: drop, active: true, maxStack: 15},
			mutate: func(body []byte) {
				body[30] = 1
			},
		},
		{
			name:    "wrong marker",
			options: seekerPassiveDropFixtureOptions{drop: drop, active: true, maxStack: 15},
			mutate: func(body []byte) {
				body[37] = 0
			},
		},
		{
			name: "not placed",
			options: seekerPassiveDropFixtureOptions{
				drop: drop, active: true, maxStack: 15, passive: []int64{16112},
			},
		},
		{
			name:    "no active task",
			options: seekerPassiveDropFixtureOptions{drop: drop, active: false, maxStack: 15},
		},
		{
			name: "wrong difficulty",
			options: seekerPassiveDropFixtureOptions{
				drop: drop, active: true, maxStack: 15, difficulty: 2, rewardDifficulty: 1,
			},
		},
		{
			name: "wrong dungeon",
			options: seekerPassiveDropFixtureOptions{
				drop: drop, active: true, maxStack: 15, rewardDungeonID: drop.dungeonID + 1,
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newSeekerPassiveDropFixture(t, test.options)
			if test.profile != "" {
				fixture.service.options.currentClientVersion = test.profile
			}
			body := seekerPassiveDropNativeBody(drop.code)
			if test.mutate != nil {
				test.mutate(body)
			}
			class := test.class
			if class == 0 {
				class = dnfproto.DefaultChannelClassification
			}
			fixture.connection.write.Reset()
			if err := seekerPassiveDropSendFormal(t, fixture, 100, class, body); err != nil {
				t.Fatal(err)
			}
			if refreshes := seekerPassiveDropRefreshCount(t, fixture.connection.write.Bytes()); refreshes != 0 {
				t.Fatalf("rejected request refreshes=%d", refreshes)
			}
			if held := seekerPassiveDropHeld(t, fixture); held != 0 {
				t.Fatalf("rejected request held=%d", held)
			}
		})
	}
}

func TestSeekerPassiveQuestDropRespectsHeldCaps(t *testing.T) {
	tests := []struct {
		drop     seekerPassiveDropCase
		maxStack int64
	}{
		{drop: seekerPassiveDrop16111, maxStack: 15},
		{drop: seekerPassiveDrop85176, maxStack: 10},
	}
	for _, test := range tests {
		t.Run(strconv.FormatInt(test.maxStack, 10), func(t *testing.T) {
			passive := make([]int64, 24)
			for index := range passive {
				passive[index] = test.drop.code
			}
			fixture := newSeekerPassiveDropFixture(t, seekerPassiveDropFixtureOptions{
				drop:     test.drop,
				active:   true,
				maxStack: test.maxStack,
				passive:  passive,
			})
			body := seekerPassiveDropNativeBody(test.drop.code)
			for sequence := uint32(100); sequence < 100+20; sequence++ {
				if _, err := seekerPassiveDropSendReset(t, fixture, sequence, body); err != nil {
					t.Fatalf("sequence=%d: %v", sequence, err)
				}
			}
			if held := seekerPassiveDropHeld(t, fixture); held != test.maxStack {
				t.Fatalf("held=%d want cap=%d", held, test.maxStack)
			}
		})
	}
}

func TestSeekerPassiveQuestDropInventoryFailureRetriesSameSequence(t *testing.T) {
	drop := seekerPassiveDrop16111
	fixture := newSeekerPassiveDropFixture(t, seekerPassiveDropFixtureOptions{
		drop: drop, active: true, maxStack: 15,
	})
	items := &seekerQST022FailOnceItems{base: fixture.repositories.CharacterItems}
	broken := fixture.repositories
	broken.CharacterItems = items
	fixture.service.repositoryProvider = func() (dnfrepo.Group, bool) { return broken, true }
	body := seekerPassiveDropNativeBody(drop.code)
	if _, err := seekerPassiveDropSendReset(t, fixture, 100, body); err == nil {
		t.Fatal("inventory transaction failure was accepted")
	}
	if held := seekerPassiveDropHeld(t, fixture); held != 0 {
		t.Fatalf("transaction failure held=%d", held)
	}
	if refreshes, err := seekerPassiveDropSendReset(t, fixture, 100, body); err != nil || refreshes != 1 {
		t.Fatalf("same-sequence retry refreshes=%d err=%v", refreshes, err)
	}
	if held := seekerPassiveDropHeld(t, fixture); held != 1 {
		t.Fatalf("retry held=%d want=1", held)
	}
}

func TestSeekerPassiveQuestDropPendingEventRetriesAfterHighWatermarkAdvances(t *testing.T) {
	drop := seekerPassiveDrop16112
	fixture := newSeekerPassiveDropFixture(t, seekerPassiveDropFixtureOptions{
		drop: drop, active: true, maxStack: 15,
	})
	items := &seekerQST022FailOnceItems{base: fixture.repositories.CharacterItems}
	broken := fixture.repositories
	broken.CharacterItems = items
	fixture.service.repositoryProvider = func() (dnfrepo.Group, bool) { return broken, true }
	body := seekerPassiveDropNativeBody(drop.code)

	if _, err := seekerPassiveDropSendReset(t, fixture, 100, body); err == nil {
		t.Fatal("first pending event did not report its transaction failure")
	}
	if refreshes, err := seekerPassiveDropSendReset(t, fixture, 101, body); err != nil || refreshes != 0 {
		t.Fatalf("new event while pending refreshes=%d err=%v", refreshes, err)
	}
	if refreshes, err := seekerPassiveDropSendReset(t, fixture, 100, body); err != nil || refreshes != 1 {
		t.Fatalf("pending old-sequence retry refreshes=%d err=%v", refreshes, err)
	}
	if held := seekerPassiveDropHeld(t, fixture); held != 1 {
		t.Fatalf("held=%d want=1", held)
	}
}

func TestSeekerPassiveQuestDropRefreshFailureRetriesWithoutRegrant(t *testing.T) {
	drop := seekerPassiveDrop85176
	fixture := newSeekerPassiveDropFixture(t, seekerPassiveDropFixtureOptions{
		drop: drop, active: true, maxStack: 10,
	})
	writeErr := errors.New("passive quest drop injected op14 failure")
	failedConnection := &failNthDungeonWriteConn{failAt: 1, err: writeErr}
	fixture.session.conn = failedConnection
	body := seekerPassiveDropNativeBody(drop.code)
	if err := seekerPassiveDropSendFormal(
		t,
		fixture,
		100,
		dnfproto.DefaultChannelClassification,
		body,
	); !errors.Is(err, writeErr) {
		t.Fatalf("refresh failure=%v want=%v", err, writeErr)
	}
	if held := seekerPassiveDropHeld(t, fixture); held != 1 {
		t.Fatalf("refresh failure held=%d want=1", held)
	}

	retryConnection := &bufferConn{}
	fixture.connection = retryConnection
	fixture.session.conn = retryConnection
	if refreshes, err := seekerPassiveDropSendReset(t, fixture, 100, body); err != nil || refreshes != 1 {
		t.Fatalf("refresh retry refreshes=%d err=%v", refreshes, err)
	}
	if held := seekerPassiveDropHeld(t, fixture); held != 1 {
		t.Fatalf("refresh retry held=%d want=1", held)
	}
}

func TestSeekerPassiveQuestDropSequenceWrapsFFFFToZero(t *testing.T) {
	drop := seekerPassiveDrop16111
	fixture := newSeekerPassiveDropFixture(t, seekerPassiveDropFixtureOptions{
		drop: drop, active: true, maxStack: 15,
		passive: []int64{drop.code, drop.code},
	})
	body := seekerPassiveDropNativeBody(drop.code)
	if refreshes, err := seekerPassiveDropSendReset(t, fixture, 65535, body); err != nil || refreshes != 1 {
		t.Fatalf("sequence 65535 refreshes=%d err=%v", refreshes, err)
	}
	if refreshes, err := seekerPassiveDropSendReset(t, fixture, 0, body); err != nil || refreshes != 1 {
		t.Fatalf("sequence 0 refreshes=%d err=%v", refreshes, err)
	}
	if held := seekerPassiveDropHeld(t, fixture); held != 2 {
		t.Fatalf("rollover held=%d want=2", held)
	}
}

func TestLeadPassiveRegisteredSpecialStillDrops(t *testing.T) {
	t.Setenv(mapOwnedPassiveObjectModeEnvName, "register")
	drop := seekerPassiveDrop16111
	fixture := newSeekerPassiveDropFixture(t, seekerPassiveDropFixtureOptions{
		drop:     drop,
		active:   true,
		maxStack: 15,
		passive:  []int64{9999},
		special:  []int64{drop.code, drop.code},
	})
	scene, ok := fixture.runtime.Session.Scene()
	if !ok {
		t.Fatal("scene")
	}
	fixture.runtime.registerCurrentDungeonMapOwnedPassiveObjects(scene)
	for _, sequence := range []uint32{100, 101} {
		if _, err := seekerPassiveDropSendReset(
			t,
			fixture,
			sequence,
			seekerPassiveDropNativeBody(drop.code),
		); err != nil {
			t.Fatal(err)
		}
	}
	if held := seekerPassiveDropHeld(t, fixture); held != 2 {
		t.Fatalf("registered two special sources held=%d want2", held)
	}
}

func TestLeadPassiveExhaustedQuotaDoesNotGrowReceipts(t *testing.T) {
	drop := seekerPassiveDrop16111
	fixture := newSeekerPassiveDropFixture(t, seekerPassiveDropFixtureOptions{
		drop:     drop,
		active:   true,
		maxStack: 15,
		passive:  []int64{drop.code},
	})
	for sequence := uint32(100); sequence < 200; sequence++ {
		if _, err := seekerPassiveDropSendReset(
			t,
			fixture,
			sequence,
			seekerPassiveDropNativeBody(drop.code),
		); err != nil {
			t.Fatal(err)
		}
	}
	if receipts := len(fixture.runtime.passiveQuestDropReceipts); receipts != 1 {
		t.Fatalf("one source retained %d receipts after quota exhausted", receipts)
	}
}

func TestLeadPassiveNonQuestRegisteredSpecialReleasesProjectedDrop(t *testing.T) {
	t.Setenv(mapOwnedPassiveObjectModeEnvName, "register")
	t.Setenv(passiveObjectDropRowEnv, "probe")
	service, session, conn, runtime, scene := prepareHellPartyDungeonRuntimeForEntryTest(
		t,
		bridgeHellPartyProbeDropPVF(),
	)
	service.options.currentClientVersion = "official-24.9"
	if err := service.prepareCurrentDungeonPassiveObjectDrops(
		session,
		runtime,
		scene,
		"lead_non_task_special",
	); err != nil {
		t.Fatal(err)
	}
	registration := runtime.currentDungeonMapOwnedPassiveObjectRegistration(scene)
	if registration == nil || len(registration.Rows) != 1 {
		t.Fatalf("map-owned registration=%+v", registration)
	}
	row := registration.Rows[0]
	body := seekerPassiveDropNativeBody(30528)
	frame, err := dnfproto.BuildChannelPacket(
		uint16(dnfenum.CmdPacketDieMonster),
		body,
		100,
		dnfproto.DefaultChannelClassification,
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := service.handleGameUpper(session, frame); err != nil {
		t.Fatal(err)
	}
	if !row.Released {
		t.Fatal("non-task special object must still release its projected ground drop row")
	}
	if conn.write.Len() != 0 {
		t.Fatalf("non-task special object unexpectedly answered op39: %x", conn.write.Bytes())
	}
}
