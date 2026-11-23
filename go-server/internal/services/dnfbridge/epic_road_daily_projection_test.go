package dnfbridge

import (
	"context"
	"encoding/binary"
	"testing"

	oe "longheng.io/server/internal/modules/dnf/onlineevent"
)

// Break caught: the current client's visible Epic Road panel must receive the
// durably recorded daily progress and each native reward threshold.
func TestEpicRoadDailyPanelEnabledForCurrentClient(t *testing.T) {
	s, session, conn, campaign := seedSemesterAdapter(t)
	s.options.currentClientVersion = "official-24.9"
	if err := s.initializeActivityControls(context.Background()); err != nil {
		t.Fatal(err)
	}
	identity, ok := s.boundGameSessionCharacterSnapshot(session)
	if !ok {
		t.Fatal("missing owner")
	}
	group, _ := s.repositoryGroup()
	for _, tc := range []struct{ days, available uint32 }{
		{0, 0}, {1, 0}, {7, 0}, {8, 1}, {15, 1}, {16, 3}, {23, 3}, {24, 7},
	} {
		character, _, err := group.Character.Load(context.Background(), "1")
		if err != nil {
			t.Fatal(err)
		}
		key := oe.SemesterJournalKey(campaign)
		character.Stats[key+":day_bitmap"] = int64((uint32(1) << tc.days) - 1)
		character.Stats[key+":qualified_days"] = int64(tc.days)
		if err := group.Character.Save(context.Background(), character); err != nil {
			t.Fatal(err)
		}
		conn.write.Reset()
		if err := s.sendCurrentSemesterCumulative(session, identity); err != nil {
			t.Fatalf("days=%d: visible Epic Road daily panel not projected: %v", tc.days, err)
		}
		packet, rest := splitGameServerUpperPacket(t, conn.write.Bytes())
		if packet.Header.MsgID != 654 || len(packet.Body) != 11 || len(rest) != 0 {
			t.Fatalf("bad projection: %+v", packet)
		}
		if uint32(packet.Body[0]) != tc.days || binary.LittleEndian.Uint32(packet.Body[3:7]) != tc.available || binary.LittleEndian.Uint32(packet.Body[7:11]) != 0 {
			t.Fatalf("days=%d threshold=%d: got %x", tc.days, tc.available, packet.Body)
		}
	}
}

// Break caught: enabling the compatible profile by default must not bypass a
// deliberate activity shutdown or enable unsupported profiles.
func TestEpicRoadDailyExplicitDisablePreserved(t *testing.T) {
	s, session, conn, _ := seedSemesterAdapter(t)
	s.options.currentClientVersion = "official-24.9"
	enableSemesterRoute(t, s, false)
	identity, _ := s.boundGameSessionCharacterSnapshot(session)
	if err := s.sendCurrentSemesterCumulative(session, identity); err == nil {
		t.Fatal("explicit disabled daily activity projected")
	}
	if conn.write.Len() != 0 {
		t.Fatal("disabled daily activity sent state")
	}
}

func TestEpicRoadDailyLegacyProfileRemainsDisabled(t *testing.T) {
	s, session, conn, _ := seedSemesterAdapter(t)
	s.options.currentClientVersion = "90cn"
	if err := s.initializeActivityControls(context.Background()); err != nil {
		t.Fatal(err)
	}
	identity, _ := s.boundGameSessionCharacterSnapshot(session)
	if err := s.sendCurrentSemesterCumulative(session, identity); err == nil {
		t.Fatal("legacy profile received current daily panel")
	}
	if conn.write.Len() != 0 {
		t.Fatal("legacy profile emitted current daily state")
	}
}

func TestEpicRoadDailyPersistedActivityShutdownPreserved(t *testing.T) {
	for _, tc := range []struct{ name, daily, epic string }{
		{"daily-off", "false", "true"},
		{"epic-off", "true", "false"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, session, conn, _ := seedSemesterAdapter(t)
			s.options.currentClientVersion = "official-24.9"
			group, _ := s.repositoryGroup()
			record, _, err := group.Settings.Load(context.Background(), oe.ControlsScope)
			if err != nil {
				t.Fatal(err)
			}
			record.Scope = oe.ControlsScope
			record.Values = map[string]string{"revision": "1", "216": tc.daily, "350": tc.epic}
			if err := group.Settings.Save(context.Background(), record); err != nil {
				t.Fatal(err)
			}
			if err := s.initializeActivityControls(context.Background()); err != nil {
				t.Fatal(err)
			}
			identity, _ := s.boundGameSessionCharacterSnapshot(session)
			if err := s.sendCurrentSemesterCumulative(session, identity); err == nil {
				t.Fatal("persisted shutdown ignored")
			}
			if conn.write.Len() != 0 {
				t.Fatal("shutdown emitted daily state")
			}
			after, _, err := group.Settings.Load(context.Background(), oe.ControlsScope)
			if err != nil || after.Values["216"] != tc.daily || after.Values["350"] != tc.epic {
				t.Fatal("initialization overwrote operator choices", err)
			}
		})
	}
}
