package dnfbridge

import (
	"encoding/binary"
	"os"
	"testing"

	"longheng.io/server/internal/modules/dnf/adventuregroup"
)

// Ordinals and byte positions below are independently verified in the player's
// EXE enum stores 57BE78C/57BE7A4/57C00A4 and E5EC60 initialization.
func TestCurrentAdventureNativeCompatibilityFixture(t *testing.T) {
	if currentAdventureInfoPushMsgID != 1339 || currentAdventureActorRefreshMsgID != 1345 || currentAdventureInfoRequestMsgID != 1401 {
		t.Fatal("adventure route does not match distributed native EXE")
	}
	module := adventureGameplayModule()
	if module.LegacyHandlers[1401] == nil || module.UpperHandlers[1401] == nil || module.LegacyHandlers[1403] != nil || module.UpperHandlers[1403] != nil {
		t.Fatal("native adventure request or unrelated remote-peer command is misrouted")
	}
	projection := adventuregroup.Projection{ConsecutiveLoginDays: 3}
	projection.CollectionCounts[2] = 0x1234
	projection.Runtime.GrowthExperience = 0x102030405
	body := buildCurrentAdventureInfoBodyWithState(0x1234, adventuregroup.Summary{ManageLevel: 4, TotalPoint: 42}, nil, "NativeQA", 20260716, projection)
	if len(body) != 18+7424+4+8 || binary.LittleEndian.Uint32(body[14:18]) != 7424 {
		t.Fatal("native adventure serialized struct extent or name length differs")
	}
	raw := body[18 : 18+7424]
	if raw[7348+2*3] != 2 || binary.LittleEndian.Uint16(raw[7348+2*3+1:7348+2*3+3]) != 0x1234 {
		t.Fatal("native collection triple must be type byte followed by u16 count")
	}
	if binary.LittleEndian.Uint32(raw[7420:7424]) != 0 {
		t.Fatal("independent final native struct field must remain neutral")
	}
	if path := os.Getenv("DNF90_ADVENTURE_NATIVE_FIXTURE"); path != "" {
		if err := os.WriteFile(path+".actor", buildCurrentAdventureActorRefreshBody(0x1234, adventuregroup.Summary{ManageLevel: 4, TotalPoint: 42}), 0600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, body, 0600); err != nil {
			t.Fatal(err)
		}
	}
}
