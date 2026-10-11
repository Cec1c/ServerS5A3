package dnfbridge

import (
	"bytes"
	"context"
	"encoding/binary"
	"testing"

	"longheng.io/server/internal/modules/dnf/raid"
)

func TestRaidRosterUsesStoredIdentityAndNativeNarrowName(t *testing.T) {
	s, _, repos, _ := newPremiumServiceTestRuntime(t)
	character, found, err := repos.Character.Load(context.Background(), "19")
	if err != nil || !found {
		t.Fatalf("character fixture found=%v err=%v", found, err)
	}
	for _, name := range []string{"test1", "测试角色"} {
		character.Name, character.Job, character.Level = name, "2", 90
		character.Stats = map[string]int64{"grow_type": 36}
		if err := repos.Character.Save(context.Background(), character); err != nil {
			t.Fatal(err)
		}
		member := s.newRuntimeRaidMemberLocked(19, 1, 0)
		state := runtimeRaidState{RaidKey: 23<<16 | 19, Members: []runtimeRaidMemberState{member}}
		body, err := raid.BuildMemberRefreshMode3Body(runtimeRaidMemberRefresh(state))
		if err != nil {
			t.Fatal(err)
		}
		length := int(binary.LittleEndian.Uint32(body[12:16]))
		if !bytes.Equal(body[16:16+length], rosterNameBytes(name)) || bytes.IndexByte(body[16:16+length], 0) >= 0 {
			t.Fatalf("native narrow name=%x want full %q", body[16:16+length], name)
		}
		pos := 16 + length
		if body[pos] != 2 || body[pos+1] != 36 || binary.LittleEndian.Uint32(body[pos+4:pos+8]) != 90 {
			t.Fatalf("raid identity lost stored job/grow/level: %x", body[pos:pos+8])
		}
	}
}
