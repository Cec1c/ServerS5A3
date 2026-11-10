package dnfbridge

import (
	"context"
	"encoding/binary"
	"errors"
	"testing"

	"longheng.io/server/internal/modules/dnf/buffswap"
	repo "longheng.io/server/internal/modules/dnf/repository"
)

var errPairedRebuildTitleSuffix = errors.New("paired rebuild title suffix failure")

// Install the pending auxiliary row only after the real mode1 has been
// written. This isolates suffix failure from mode1 construction and Buff I/O.
type buffSuffixPairedRebuildConn struct {
	*bufferConn
	session       *gameSession
	failTitle     bool
	titleAttempts int
}

func (c *buffSuffixPairedRebuildConn) Write(data []byte) (int, error) {
	if len(data) >= 16 && data[0] == 0 {
		msgID := binary.LittleEndian.Uint16(data[1:3])
		body := data[16:]
		if msgID == 14 && len(body) >= 5 && body[0] == 3 && binary.LittleEndian.Uint16(body[3:5]) == 13 {
			c.titleAttempts++
			if c.failTitle {
				return 0, errPairedRebuildTitleSuffix
			}
		}
		if msgID == 2 && len(body) > 0 && body[0] == 1 {
			n, err := c.bufferConn.Write(data)
			if err == nil {
				row := currentItemListEntry{}
				row.patchCore(13, 400330094, 1)
				binary.LittleEndian.PutUint16(row.data[0x61:0x63], 10001)
				binary.LittleEndian.PutUint16(row.data[0x63:0x65], 37)
				c.session.pendingTitleEnchantCharacterID = c.session.selectedCharacterID
				c.session.pendingTitleEnchantRow = &row
			}
			return n, err
		}
	}
	return c.bufferConn.Write(data)
}

func buffSuffixPairedRebuildFixture(t *testing.T, failTitle bool) (*Service, *gameSession, *buffSuffixPairedRebuildConn) {
	t.Helper()
	t.Setenv("DNFBRIDGE_BUFF_GROUP_SYNC", "on")
	s, session, conn, _ := newSceneReadyReplayFixture(t)
	s.options.currentClientVersion = "official-24.9"
	session.accountFromToken = true
	repos, ok := s.repositoryGroup()
	if !ok {
		t.Fatal("fixture repositories unavailable")
	}
	ctx := context.Background()
	worn, found, err := repos.Equipment.Load(ctx, "6")
	if err != nil || !found {
		t.Fatalf("fixture equipment found=%v err=%v", found, err)
	}
	// The shared fixture's slot26 has an intentionally invalid pet handle.
	// Remove it so this test controls the sole auxiliary failure itself.
	delete(worn.Entries, "26")
	if err := repos.Equipment.Save(ctx, worn); err != nil {
		t.Fatal(err)
	}
	if err := repos.Inventory.Save(ctx, repo.InventoryRecord{
		CharacterID: "6",
		Slots: map[string]repo.ItemStack{
			"0:54": {ItemID: 101040493, Count: 1},
			"0:55": {ItemID: 101040493, Count: 1},
		},
	}); err != nil {
		t.Fatal(err)
	}
	for group := byte(0); group < 2; group++ {
		if err := buffswap.ApplyCurrentGroup(ctx, repos, "6", s.accountIDForSession(session), group,
			buffswap.Command{Skill: uint16(38 + group), Slot: 12, List: 0, Index: uint16(54 + group)}); err != nil {
			t.Fatal(err)
		}
	}
	observed := &buffSuffixPairedRebuildConn{bufferConn: conn, session: session, failTitle: failTitle}
	session.conn = observed
	return s, session, observed
}

func buffSuffixPairedRebuildRequireGroups(t *testing.T, conn *buffSuffixPairedRebuildConn, failed bool) {
	t.Helper()
	mode1At, titleAt := -1, -1
	counts := [2]int{}
	for i, frame := range loginActorGenerationSplit(t, conn.write.Bytes()) {
		if frame.msgID == 2 && frame.mode == 1 {
			mode1At = i
		}
		if frame.msgID == 14 {
			titleAt = i
		}
		if frame.msgID == 1368 || frame.msgID == 1369 {
			t.Fatalf("paired rebuild emitted a single-skill reset: msg=%d", frame.msgID)
		}
		if frame.msgID != 1367 {
			continue
		}
		body := frame.body
		if len(body) != 170 || body[0] != 0xfe || body[1] > 1 || body[4] != 33 {
			t.Fatalf("invalid group envelope=%x", body)
		}
		group := body[1]
		counts[group]++
		if mode1At < 0 || i != mode1At+1+int(group) {
			t.Fatalf("group%d was not immediately after mode1: mode1=%d frame=%d", group, mode1At, i)
		}
		if binary.LittleEndian.Uint16(body[2:4]) != uint16(38+group) {
			t.Fatalf("group%d skill=%d", group, binary.LittleEndian.Uint16(body[2:4]))
		}
		row := body[5+12*5 : 5+13*5]
		if row[0] != 1 || row[1] != 12 || row[2] != 0 || binary.LittleEndian.Uint16(row[3:5]) != uint16(54+group) {
			t.Fatalf("group%d rebound the wrong instance location: %x", group, row)
		}
	}
	if mode1At < 0 || counts != [2]int{1, 1} || conn.titleAttempts != 1 {
		t.Fatalf("mode1=%d group counts=%v title attempts=%d", mode1At, counts, conn.titleAttempts)
	}
	if !failed && titleAt <= mode1At+2 {
		t.Fatalf("successful title suffix preceded group restore: mode1=%d title=%d", mode1At, titleAt)
	}
}

func TestBuffPairedMode1RebuildRestoresBothGroupsBeforeTitleSuffix(t *testing.T) {
	for _, failTitle := range []bool{false, true} {
		name := "success"
		if failTitle {
			name = "title_suffix_failure"
		}
		t.Run(name, func(t *testing.T) {
			s, session, conn := buffSuffixPairedRebuildFixture(t, failTitle)
			err := s.sendSelectedActorMode1AppearanceWithPostState(session, "paired_test", "paired_test", true, false)
			if failTitle && !errors.Is(err, errPairedRebuildTitleSuffix) || !failTitle && err != nil {
				t.Fatalf("suffix fail=%v err=%v", failTitle, err)
			}
			buffSuffixPairedRebuildRequireGroups(t, conn, failTitle)
		})
	}
}

func TestBuffPairedLoginRebuildRestoresBothGroupsBeforeTitleSuffix(t *testing.T) {
	for _, failTitle := range []bool{false, true} {
		name := "success"
		if failTitle {
			name = "title_suffix_failure"
		}
		t.Run(name, func(t *testing.T) {
			s, session, conn := buffSuffixPairedRebuildFixture(t, failTitle)
			err := s.rebuildLoginActorGenerationAfterSceneCommit(session, "paired_test", 6)
			if failTitle && !errors.Is(err, errPairedRebuildTitleSuffix) || !failTitle && err != nil {
				t.Fatalf("suffix fail=%v err=%v", failTitle, err)
			}
			buffSuffixPairedRebuildRequireGroups(t, conn, failTitle)
		})
	}
}
