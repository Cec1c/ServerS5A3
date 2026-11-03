package dnfbridge

import (
	"context"
	"errors"
	pvf "longheng.io/server/internal/platform/pvf"
	"os"
	"testing"
)

func TestNativeStartupMissingOptionalForestRule(t *testing.T) {
	path := os.Getenv("DNF_NATIVE_STARTUP_PVF")
	if path == "" {
		t.Skip("actual native PVF required")
	}
	a, e := pvf.LoadArchive(pvf.Options{Path: path})
	if e != nil {
		t.Fatal(e)
	}
	s, session, g := newTownMoveTest(t)
	s.initialEquipmentArchive = a
	s.options.currentClientVersion = "official-24.9"
	s.options.nativeClientProfile = "90cn"
	s.bindGameSessionCharacter(session, 29)
	c, found, e := g.Character.Load(context.Background(), "29")
	if e != nil || !found {
		t.Fatal(e)
	}
	if _, e = s.currentSilentForestEntryLimit(); e == nil {
		t.Fatal("missing entry rule unexpectedly grants admission")
	}
	session.conn.(*bufferConn).write.Reset()
	if e = s.sendCurrentSilentForestCount(session, c); e != nil {
		t.Fatalf("optional forest quota aborts town initialization: %v", e)
	}
	if session.conn.(*bufferConn).write.Len() != 0 {
		t.Fatal("fabricated quota packet emitted")
	}
	_, e = s.currentSilentForestEntryLimit()
	if !errors.Is(e, errSilentForestRuleMissing) {
		t.Fatal("admission did not retain missing-rule rejection", e)
	}
}
