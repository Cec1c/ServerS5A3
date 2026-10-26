package dnfbridge

import (
	"encoding/hex"
	"os"
	"testing"
)

func TestGuildDungeonSnapshot741Wire(t *testing.T) {
	snapshot := currentGuildDungeonWireSnapshot{Flag: 1, Field1: 0x12345678, EndUnix: 1800000000, Field3: 0x87654321, ClearCounts: map[uint32]uint32{8504: 67, 8500: 34}}
	body, err := buildCurrentGuildDungeonSnapshotBody(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	const want = "017856341200d2496b214365870200000034210000220000003821000043000000"
	if got := hex.EncodeToString(body); got != want {
		t.Fatalf("body=%s want=%s", got, want)
	}
	if path := os.Getenv("GUILD_DUNGEON_NATIVE_PACKET_OUT"); path != "" {
		if err := os.WriteFile(path, body, 0600); err != nil {
			t.Fatal(err)
		}
	}
	empty, err := buildCurrentGuildDungeonSnapshotBody(currentGuildDungeonWireSnapshot{Field1: 0xffffffff})
	if err != nil || len(empty) != 17 {
		t.Fatalf("empty=%x err=%v", empty, err)
	}
	for name, bad := range map[string]currentGuildDungeonWireSnapshot{
		"non boolean flag": {Flag: 2}, "zero ID": {ClearCounts: map[uint32]uint32{0: 1}},
		"signed ID overflow":    {ClearCounts: map[uint32]uint32{0x80000000: 1}},
		"signed count overflow": {ClearCounts: map[uint32]uint32{8500: 0xffffffff}},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := buildCurrentGuildDungeonSnapshotBody(bad); err == nil {
				t.Fatal("accepted invalid snapshot")
			}
		})
	}
}
