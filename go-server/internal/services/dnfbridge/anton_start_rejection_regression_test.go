package dnfbridge

import (
	"context"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	"reflect"
	"strings"
	"testing"
)

func TestAntonStartRejectedExplainsTicketAndWaitingMember(t *testing.T) {
	for _, fault := range []string{"ticket", "waiting"} {
		t.Run(fault, func(t *testing.T) {
			f, peers := raidStartWaitingFixture(t, 0)
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
			frames := writeFrames(t, leader)
			if len(frames) != 2 || frames[0].msgID != 666 || len(frames[0].body) != 2 || frames[0].body[0] != 0 || frames[1].msgID != currentAreaChatNotifyOpcode {
				t.Fatalf("expected rejection plus player-visible reason, got %+v", frames)
			}
			text := string(frames[1].body)
			for _, word := range []string{"无法开始攻坚", "测试队员"} {
				if !strings.Contains(text, string(rosterNameBytes(word))) {
					t.Fatal("missing player-visible detail", word)
				}
			}
			word := "入场材料"
			if fault == "waiting" {
				word = "等候区"
			}
			if !strings.Contains(text, string(rosterNameBytes(word))) {
				t.Fatal("wrong rejection reason", word)
			}
			if !reflect.DeepEqual(before, *f.state) || !reflect.DeepEqual(bags, raidWaitingInventories(t, f)) {
				t.Fatal("rejection changed formation, battle, tickets or player assets")
			}
			if peer.conn.(*bufferConn).write.Len() != 0 {
				t.Fatal("rejection projected a start to the follower")
			}
		})
	}
}
