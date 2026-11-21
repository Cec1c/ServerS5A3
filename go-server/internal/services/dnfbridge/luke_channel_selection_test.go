package dnfbridge

import (
	"context"
	"encoding/binary"
	"errors"
	"os"
	"reflect"
	"testing"

	"longheng.io/server/internal/modules/dnf/channelcatalog"
	"longheng.io/server/internal/modules/dnf/dnfenum"
	dnfquest "longheng.io/server/internal/modules/dnf/quest"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	dnfrepomemory "longheng.io/server/internal/modules/dnf/repository/memory"
	dnftown "longheng.io/server/internal/modules/dnf/town"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

func lukeSelectionArchive(t *testing.T) *platformpvf.Archive {
	t.Helper()
	path := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if path == "" {
		t.Skip("set DNFBRIDGE_REAL_PVF_SMOKE to verify actual Luke entry quest")
	}
	a, err := platformpvf.LoadArchive(platformpvf.Options{Path: path})
	if err != nil {
		t.Fatal(err)
	}
	rule, err := dnfquest.LoadSpecialChannelRule(a)
	if err != nil || rule.LukeQuestID != 2903 {
		t.Fatalf("actual Luke entry rule: %+v %v", rule, err)
	}
	return a
}

func lukeSelectionService(a *platformpvf.Archive, repos dnfrepo.Group) *Service {
	return &Service{
		options:                 options{currentClientVersion: "official-24.9", gameUpperHeader: gameUpperHeaderServer16, gameUpperBodyCodec: gameUpperBodyCodecPlain},
		initialEquipmentArchive: a,
		repositoryProvider:      func() (dnfrepo.Group, bool) { return repos, true },
		gameSessions:            make(map[uint16]*gameSession),
	}
}

func TestRealPVFLukeChannelSelectionRechecksCurrentCharacter(t *testing.T) {
	ctx := context.Background()
	repos := dnfrepomemory.NewMemoryGroup()
	s := lukeSelectionService(lukeSelectionArchive(t), repos)
	p := &gameSession{accountID: "luke-selection", channel: channelcatalog.Channel{ID: 181, Type: dnfenum.HiddenRaidType}}
	first := dnfrepo.CharacterRecord{CharacterID: "11", AccountID: p.accountID, Name: "qualified", Slot: 0, Job: "0", Level: 90}
	second := dnfrepo.CharacterRecord{CharacterID: "12", AccountID: p.accountID, Name: "unqualified", Slot: 1, Job: "0", Level: 90}
	if err := repos.Quest.Save(ctx, dnfrepo.QuestRecord{CharacterID: first.CharacterID, States: map[int64]dnfrepo.QuestState{2903: {Status: "completed"}}}); err != nil {
		t.Fatal(err)
	}
	if !s.currentLukeChannelSelectionAllowed(ctx, p, first) {
		t.Fatal("completed first character denied")
	}
	if s.currentLukeChannelSelectionAllowed(ctx, p, second) {
		t.Fatal("first character authorized second character")
	}
	for _, tc := range []struct {
		name, status string
		legacy, want bool
	}{
		{"active_zero_remaining", "active", false, false},
		{"completed", "completed", false, true},
		{"done", "done", false, true},
		{"cleared", "cleared", false, true},
		{"legacy_completed", "completed", true, true},
		{"completion_removed_after_allowed", "active", false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			q := dnfrepo.QuestRecord{CharacterID: second.CharacterID, States: map[int64]dnfrepo.QuestState{}, Progress: map[int64]dnfrepo.QuestState{}}
			if tc.legacy {
				q.Progress[2903] = dnfrepo.QuestState{Status: tc.status}
			} else {
				q.States[2903] = dnfrepo.QuestState{Status: tc.status, ProgressValue: 0}
			}
			if err := repos.Quest.Save(ctx, q); err != nil {
				t.Fatal(err)
			}
			before, _, _ := repos.Quest.Load(ctx, second.CharacterID)
			if got := s.currentLukeChannelSelectionAllowed(ctx, p, second); got != tc.want {
				t.Fatalf("permission=%t want=%t", got, tc.want)
			}
			after, _, _ := repos.Quest.Load(ctx, second.CharacterID)
			if !reflect.DeepEqual(before, after) {
				t.Fatal("admission changed quest state")
			}
		})
	}
	foreign := first
	foreign.AccountID = "another-account"
	if s.currentLukeChannelSelectionAllowed(ctx, p, foreign) {
		t.Fatal("foreign account accepted")
	}
}

func TestRealPVFLukeChannelReselectionBlocksBothRoutesBeforeBinding(t *testing.T) {
	a := lukeSelectionArchive(t)
	towns, err := dnftown.Load(context.Background(), a, dnftown.Options{})
	if err != nil {
		t.Fatal(err)
	}
	for _, route := range []string{"upper", "legacy"} {
		t.Run(route, func(t *testing.T) {
			ctx := context.Background()
			repos := dnfrepomemory.NewMemoryGroup()
			s := lukeSelectionService(a, repos)
			s.townCatalog = towns
			conn := &bufferConn{}
			p := &gameSession{conn: conn, connID: "luke-reselect-" + route, accountID: "luke-selection", channel: channelcatalog.Channel{ID: 181, Type: dnfenum.HiddenRaidType}, characterGeneration: 1, loginPacketSeen: true, accountFromToken: true, gameEndpointSuccessSent: true, rosterRequested: true}
			characters := []dnfrepo.CharacterRecord{
				{CharacterID: "11", AccountID: p.accountID, Name: "qualified", Slot: 0, Job: "0", Level: 90},
				{CharacterID: "12", AccountID: p.accountID, Name: "unqualified", Slot: 1, Job: "0", Level: 90},
			}
			for _, c := range characters {
				if err := repos.Character.Save(ctx, c); err != nil {
					t.Fatal(err)
				}
			}
			if err := repos.Quest.Save(ctx, dnfrepo.QuestRecord{CharacterID: "11", States: map[int64]dnfrepo.QuestState{2903: {Status: "completed"}}}); err != nil {
				t.Fatal(err)
			}
			if err := repos.Quest.Save(ctx, dnfrepo.QuestRecord{CharacterID: "12", States: map[int64]dnfrepo.QuestState{2903: {Status: "active", ProgressValue: 0}}}); err != nil {
				t.Fatal(err)
			}
			// Reproduce the actual same-connection lifecycle that the cold-login
			// correction intentionally skips after the first character binding.
			if !s.currentLukeChannelSelectionAllowed(ctx, p, characters[0]) {
				t.Fatal("initial character must be qualified")
			}
			s.bindGameSessionCharacter(p, 11)
			if err := s.handleUpperReturnSelectCharacter(p, nil); err != nil {
				t.Fatal(err)
			}
			if p.characterGeneration <= 1 || p.selectedCharacterID != 0 || p.channel.Type != dnfenum.HiddenRaidType {
				t.Fatal("did not retain Luke channel after return-select")
			}
			p.currentRenameRoster = characters
			conn.write.Reset()
			before, _, _ := repos.Quest.Load(ctx, "12")
			var err error
			if route == "upper" {
				err = s.sendUpperCSharpSelectInit(p, []byte{1, 0})
			} else {
				request := make([]byte, 11)
				binary.LittleEndian.PutUint32(request, 12)
				request[4] = 1
				err = s.sendSelectCharacterState(p, request)
			}
			if p.selectedCharacterID != 0 || s.gameSessions[12] != nil {
				t.Fatalf("unqualified role bound after %s selection: selected=%d err=%v", route, p.selectedCharacterID, err)
			}
			if err != nil {
				t.Fatal(err)
			}
			packets := splitServerUpperPacketsForReturnSelectTest(t, conn.write.Bytes())
			if len(packets) != 1 || binary.LittleEndian.Uint16(packets[0][1:3]) != uint16(dnfenum.CmdPacketReturnSelectCharacter) || packets[0][0] != 1 || !reflect.DeepEqual(packets[0][16:], []byte{1}) {
				t.Fatalf("denial did not return selector, packets=%+v", packets)
			}
			if p.townSceneReadyCharacterID != 0 || p.initialTownRouteCharacterID != 0 {
				t.Fatal("denied character acquired town scene")
			}
			after, _, _ := repos.Quest.Load(ctx, "12")
			if !reflect.DeepEqual(before, after) {
				t.Fatal("denied selection changed quest completion")
			}
			// A rejected request must not poison the connection. Once the selected
			// character has actually completed the quest, both entry routes work.
			if err := repos.Quest.Save(ctx, dnfrepo.QuestRecord{CharacterID: "12", States: map[int64]dnfrepo.QuestState{2903: {Status: "completed"}}}); err != nil {
				t.Fatal(err)
			}
			p.currentRenameRoster = characters
			conn.write.Reset()
			if route == "upper" {
				err = s.sendUpperCSharpSelectInit(p, []byte{1, 0})
			} else {
				request := make([]byte, 11)
				binary.LittleEndian.PutUint32(request, 12)
				request[4] = 1
				err = s.sendSelectCharacterState(p, request)
			}
			if err != nil || p.selectedCharacterID != 12 || s.gameSessions[12] != p {
				t.Fatalf("qualified retry denied after %s selection: selected=%d err=%v", route, p.selectedCharacterID, err)
			}
			if conn.write.Len() == 0 {
				t.Fatal("qualified retry did not send selection response")
			}
		})
	}
}

type lukeSelectionFaultQuest struct {
	dnfrepo.QuestRepository
	wrongOwner bool
}

func (r lukeSelectionFaultQuest) Load(context.Context, string) (dnfrepo.QuestRecord, bool, error) {
	if r.wrongOwner {
		return dnfrepo.QuestRecord{CharacterID: "other-role", States: map[int64]dnfrepo.QuestState{2903: {Status: "completed"}}}, true, nil
	}
	return dnfrepo.QuestRecord{}, false, errors.New("quest storage unavailable")
}

func TestRealPVFLukeChannelSelectionFailsClosed(t *testing.T) {
	a := lukeSelectionArchive(t)
	for _, tc := range []string{"missing_repository", "load_error", "wrong_record_owner", "missing_pvf"} {
		t.Run(tc, func(t *testing.T) {
			repos := dnfrepomemory.NewMemoryGroup()
			switch tc {
			case "missing_repository":
				repos.Quest = nil
			case "load_error":
				repos.Quest = lukeSelectionFaultQuest{QuestRepository: repos.Quest}
			case "wrong_record_owner":
				repos.Quest = lukeSelectionFaultQuest{QuestRepository: repos.Quest, wrongOwner: true}
			}
			s := lukeSelectionService(a, repos)
			if tc == "missing_pvf" {
				s.initialEquipmentArchive = nil
			}
			p := &gameSession{accountID: "owner", channel: channelcatalog.Channel{Type: dnfenum.HiddenRaidType}}
			if s.currentLukeChannelSelectionAllowed(context.Background(), p, dnfrepo.CharacterRecord{CharacterID: "1", AccountID: "owner"}) {
				t.Fatal("unknown quest state granted entry")
			}
		})
	}
}

func TestLukeChannelSelectionLeavesOtherChannelsUnchanged(t *testing.T) {
	for _, channelType := range []uint8{0, 1, 23, 39, 42} {
		s := &Service{options: options{currentClientVersion: "official-24.9"}, repositoryProvider: func() (dnfrepo.Group, bool) {
			t.Fatal("unrelated channel queried Luke permissions")
			return dnfrepo.Group{}, false
		}}
		if !s.currentLukeChannelSelectionAllowed(context.Background(), &gameSession{channel: channelcatalog.Channel{Type: channelType}}, dnfrepo.CharacterRecord{}) {
			t.Fatalf("unrelated channel %d rejected", channelType)
		}
	}
	s := &Service{}
	if !s.currentLukeChannelSelectionAllowed(context.Background(), &gameSession{channel: channelcatalog.Channel{Type: dnfenum.HiddenRaidType}}, dnfrepo.CharacterRecord{}) {
		t.Fatal("legacy profile changed")
	}
}
