package dnfbridge

import (
	"bytes"
	"context"
	"os"
	"reflect"
	"strconv"
	"testing"

	repo "longheng.io/server/internal/modules/dnf/repository"
)

// Exercise the transport entry and committed projections, beyond the domain
// matrix. All records are synthetic; the resource archive is supplied explicitly.
func TestSecondAwakeningReportedClassesDispatchRealPVF(t *testing.T) {
	if os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE") == "" {
		t.Skip("current real PVF required")
	}
	for _, tc := range []struct{ job, branch int }{{0, 1}, {0, 3}, {12, 4}} {
		t.Run("job_"+strconv.Itoa(tc.job)+"_branch_"+strconv.Itoa(tc.branch), func(t *testing.T) {
			s, p, g, _ := levelUpTicketBridgeFixture(t, 10162929, 90)
			ctx := context.Background()
			p.loginPacketSeen = true
			c, _, err := g.Character.Load(ctx, "29")
			if err != nil {
				t.Fatal(err)
			}
			c.Job = strconv.Itoa(tc.job)
			c.Stats["grow_type"] = int64(0x10 | tc.branch)
			if err = g.Character.Save(ctx, c); err != nil {
				t.Fatal(err)
			}
			// Discard the fixture's unrelated job-2 skills and use the normal
			// resource-backed initialization for this class.
			if err = g.Skill.Save(ctx, repo.SkillRecord{CharacterID: "29"}); err != nil {
				t.Fatal(err)
			}
			if _, _, err = s.loadOrBackfillCurrentSceneSkillRecord(ctx, g, c, byte(tc.job)); err != nil {
				t.Fatal(err)
			}
			bag, _, err := g.Inventory.Load(ctx, "29")
			if err != nil {
				t.Fatal(err)
			}
			stack := bag.Slots["0:4"]
			stack.Count = 3
			bag.Slots["0:4"] = stack
			if err = g.Inventory.Save(ctx, bag); err != nil {
				t.Fatal(err)
			}
			request := testQuestClearCouponRequestBody(4, 121)
			handled, err := s.handleAlignedGameCommand(p, 1, 515, request)
			if !handled || err != nil {
				t.Fatalf("dispatch: handled=%v err=%v", handled, err)
			}
			after := levelUpTicketBridgeSnapshot(t, g)
			if after[0].(repo.CharacterRecord).Stats["grow_type"] != int64(0x20|tc.branch) || after[1].(repo.InventoryRecord).Slots["0:4"].Count != 2 {
				t.Fatal("awakening or single-ticket consumption mismatch")
			}
			packets := splitAllFinishBridgePackets(t, p.conn.(*bufferConn).write.Bytes())
			if len(packets) == 0 || !bytes.Equal(packets[0].Body, []byte{1, 4, 0, 0, 121, 0, 0, 0}) {
				t.Fatal("missing success acknowledgement")
			}
			seen := map[uint16]bool{}
			for _, packet := range packets {
				seen[packet.Header.MsgID] = true
			}
			for _, op := range []uint16{14, 2, currentDungeonCharacterStateMsgID, currentSkillInfoMsgID} {
				if !seen[op] {
					t.Fatalf("missing committed projection %d", op)
				}
			}
			p.conn.(*bufferConn).write.Reset()
			handled, err = s.handleAlignedGameCommand(p, 1, 515, request)
			if !handled || err != nil || !reflect.DeepEqual(after, levelUpTicketBridgeSnapshot(t, g)) {
				t.Fatal("duplicate request changed persisted state", err)
			}
			packets = splitAllFinishBridgePackets(t, p.conn.(*bufferConn).write.Bytes())
			if len(packets) != 1 || !bytes.Equal(packets[0].Body, []byte{0, 19, 4, 0, 0, 121}) {
				t.Fatal("duplicate request not rejected")
			}
		})
	}
}
