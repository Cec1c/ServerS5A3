package dnfbridge

import (
	"bytes"
	"context"
	"errors"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"golang.org/x/text/encoding/simplifiedchinese"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

func requireLukeStartFeedback(t *testing.T, session *gameSession, wantChat bool, words ...string) []byte {
	t.Helper()
	frames := writeFrames(t, session)
	wantFrames := 1
	if wantChat {
		wantFrames = 2
	}
	if len(frames) != wantFrames || frames[0].msgID != 666 || !bytes.Equal(frames[0].body, []byte{0, runtimeRaidFailureCode}) {
		t.Fatalf("rejection must retain the negative ACK first: %+v", frames)
	}
	if !wantChat {
		return nil
	}
	if frames[1].msgID != currentAreaChatNotifyOpcode {
		t.Fatalf("expected player text after ACK, got %+v", frames)
	}
	for _, word := range append([]string{"无法开始攻坚"}, words...) {
		encoded, err := simplifiedchinese.GB18030.NewEncoder().Bytes([]byte(word))
		if err != nil || !bytes.Contains(frames[1].body, encoded) {
			t.Fatalf("missing full player text %q: encode error=%v body=%x", word, err, frames[1].body)
		}
	}
	return frames[1].body
}

func TestLukeStartFeedbackChannelAndSafeReason(t *testing.T) {
	for _, profile := range []struct {
		name, version string
		channelType   uint8
		wantChat      bool
	}{
		{"official_luke", "official-24.9", 32, true},
		{"official_anton", "official-24.9", 23, true},
		{"official_fiendwar", "official-24.9", 42, false},
		{"official_town", "official-24.9", 1, false},
		{"legacy_luke", "", 32, false},
		{"legacy_anton", "", 23, false},
	} {
		t.Run(profile.name, func(t *testing.T) {
			f := raidSafetyFixture(t, nil, 1)
			f.service.options.currentClientVersion = profile.version
			f.state.Members[0].Name = "测试团长"
			leader := f.session()
			leader.channel.Type = profile.channelType
			const privateError = "private-account/sql/transport-error"
			for _, structured := range []bool{false, true} {
				var reason error = errors.New(privateError)
				words := []string{"请确认全员在线、已进入本团等候区，并满足人数、入场材料、前置任务及次数要求"}
				if structured {
					reason = &raidStartRejection{message: "队员组别无效，请重新调整编队", members: []uint16{19, 9999}, cause: reason}
					words = []string{"队员组别无效，请重新调整编队", "测试团长"}
				}
				if err := f.service.sendRaidStartRejection(leader, 666, reason); err != nil {
					t.Fatal(err)
				}
				body := requireLukeStartFeedback(t, leader, profile.wantChat, words...)
				if bytes.Contains(body, []byte(privateError)) || bytes.Contains(body, []byte("9999")) {
					t.Fatalf("player text leaked a cause or a non-member identity: %x", body)
				}
			}
		})
	}
}

func TestLukeStartFeedbackInvalidRosterPreservesStateAndTickets(t *testing.T) {
	for _, mode := range []struct {
		name, version string
		currentLayout bool
	}{
		{"official_normal", "official-24.9", true},
		// This layout bypasses party intent and reaches each formerly silent
		// member-loop branch. Normal-layout early validation must also remain.
		{"official_legacy_layout", "official-24.9", false},
		{"legacy", "", false},
	} {
		t.Run(mode.name, func(t *testing.T) {
			for _, fault := range []string{"invalid_group", "duplicate", "zero_character", "empty_roster", "leader_missing", "too_many_members"} {
				t.Run(fault, func(t *testing.T) {
					f := raidSafetyFixture(t, nil, 2)
					f.service.options.currentClientVersion = mode.version
					f.state.CurrentLayout, f.state.CurrentUIFlags, f.state.TailFlag, f.state.RouteOrRaidType = mode.currentLayout, [3]byte{}, 0, 1
					f.state.Members[0].Name, f.state.Members[1].Name = "测试团长", "测试队员"
					leader := f.session()
					leader.channel.ID, leader.channel.Type = 32, 32
					leader.loginPacketSeen, leader.accountFromToken = true, true
					f.service.bindGameSessionCharacter(leader, leader.selectedCharacterID)
					leader.conn.(*bufferConn).write.Reset()
					// Snapshot the real inventories before corrupting the roster so
					// zero/missing IDs cannot hide an asset mutation from the check.
					bags := raidWaitingInventories(t, f)
					want := "请由团长确认攻坚队人数后开始攻坚"
					var wantMembers []uint16
					switch fault {
					case "invalid_group":
						f.state.Members[1].GroupIndex = runtimeRaidMaxGroups + 1
						want, wantMembers = "队员组别无效，请重新调整编队", []uint16{20}
					case "duplicate":
						f.state.Members = append(f.state.Members, f.state.Members[1])
						want, wantMembers = "攻坚队成员重复，请重新确认成员名单", []uint16{20}
					case "zero_character":
						f.state.Members[1].CharID = 0
						want = "攻坚队存在无效角色，请重新确认成员名单"
					case "empty_roster":
						f.state.Members = nil
					case "leader_missing":
						f.state.Members = f.state.Members[1:]
					case "too_many_members":
						for len(f.state.Members) <= currentRaidMemberMaximum(*f.state) {
							f.state.Members = append(f.state.Members, f.state.Members[1])
						}
					}
					if mode.currentLayout && (fault == "duplicate" || fault == "zero_character") {
						want, wantMembers = "编队状态已变化，请重新确认队伍，每队最多4人", nil
					}
					before := cloneRuntimeRaidState(*f.state)
					state, ok, err := f.service.startRuntimeRaidWithEntryCost(leader)
					var detail *raidStartRejection
					if ok || state.Started || !errors.As(err, &detail) || detail.message != want || !reflect.DeepEqual(detail.members, wantMembers) {
						t.Fatalf("expected structured roster rejection %q/%v: ok=%t state=%+v err=%v detail=%+v", want, wantMembers, ok, state, err, detail)
					}
					if !reflect.DeepEqual(before, *f.state) {
						t.Fatal("roster rejection repaired the invalid roster or changed battle state")
					}
					if err := f.service.handleOnlineStartRaid(leader, 666, nil); err != nil {
						t.Fatal(err)
					}
					requireLukeStartFeedback(t, leader, mode.version == "official-24.9", want)
					if !reflect.DeepEqual(before, *f.state) {
						t.Fatal("handler rejection changed roster or battle state")
					}
					for id, bag := range bags {
						after, found, err := f.repos.Inventory.Load(context.Background(), strconv.Itoa(int(id)))
						if err != nil || !found || !reflect.DeepEqual(bag, after) {
							t.Fatalf("rejected roster changed inventory for %d: found=%t err=%v", id, found, err)
						}
					}
					if text := f.logText(t); !strings.Contains(text, "game-raid-start-rejected") {
						t.Fatal("structured roster rejection did not reach the existing rejection log")
					}
				})
			}
		})
	}
}

func TestLukeStartFeedbackDoesNotBypassOwnerAuthentication(t *testing.T) {
	for _, fault := range []string{"wrong_account", "unbound_session", "no_selected_character"} {
		t.Run(fault, func(t *testing.T) {
			f := raidSafetyFixture(t, nil, 1)
			f.service.options.currentClientVersion = "official-24.9"
			leader := f.session()
			leader.channel.ID, leader.channel.Type = 32, 32
			leader.loginPacketSeen, leader.accountFromToken = true, true
			if fault != "unbound_session" {
				f.service.bindGameSessionCharacter(leader, leader.selectedCharacterID)
			}
			if fault == "wrong_account" {
				leader.accountID = "wrong-account"
			}
			if fault == "no_selected_character" {
				leader.selectedCharacterID = 0
			}
			leader.conn.(*bufferConn).write.Reset()
			before, bags := cloneRuntimeRaidState(*f.state), raidWaitingInventories(t, f)
			if err := f.service.handleOnlineStartRaid(leader, 666, nil); err != nil {
				t.Fatal(err)
			}
			requireLukeStartFeedback(t, leader, false)
			if !reflect.DeepEqual(before, *f.state) || !reflect.DeepEqual(bags, raidWaitingInventories(t, f)) {
				t.Fatal("unauthorized start changed roster, battle or inventory")
			}
		})
	}
}

func TestLukeStartFeedbackRealPVFTicketAndWaitingMember(t *testing.T) {
	for _, fault := range []string{"ticket", "waiting"} {
		t.Run(fault, func(t *testing.T) {
			f, peers := raidStartWaitingFixture(t, 1)
			leader, peer := peers[0], peers[1]
			f.state.Members[1].Name = "测试队员"
			if fault == "ticket" {
				if err := f.repos.Inventory.Save(context.Background(), dnfrepo.InventoryRecord{CharacterID: "20", Slots: map[string]dnfrepo.ItemStack{}}); err != nil {
					t.Fatal(err)
				}
			} else {
				f.service.onlinePlayers.LeaveArea(peer.selectedCharacterID)
			}
			before, bags := cloneRuntimeRaidState(*f.state), raidWaitingInventories(t, f)
			for _, p := range peers {
				p.conn.(*bufferConn).write.Reset()
			}
			if err := f.service.handleOnlineStartRaid(leader, 666, nil); err != nil {
				t.Fatal(err)
			}
			word := "入场材料"
			if fault == "waiting" {
				word = "等候区"
			}
			requireLukeStartFeedback(t, leader, true, word, "测试队员")
			if !reflect.DeepEqual(before, *f.state) || !reflect.DeepEqual(bags, raidWaitingInventories(t, f)) {
				t.Fatal("rejection changed formation, battle, tickets or player assets")
			}
		})
	}
}
