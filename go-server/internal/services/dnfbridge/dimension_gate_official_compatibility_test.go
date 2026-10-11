package dnfbridge

import "testing"

func TestOfficialDimensionGateNeverEmitsLegacyOpcode806(t *testing.T) {
	for _, setting := range []string{"", "on", "off"} {
		t.Run("switch="+setting, func(t *testing.T) {
			t.Setenv(currentDimensionGateEntranceEnv, setting)
			conn := &bufferConn{}
			s := &Service{options: options{currentClientVersion: "official-24.9"}}
			if err := s.sendCurrentDimensionGateEntranceInfo(&gameSession{conn: conn, selectedCharacterID: 19}, "test"); err != nil {
				t.Fatal(err)
			}
			if conn.write.Len() != 0 {
				t.Fatalf("legacy dimension notification was emitted: %x", conn.write.Bytes())
			}
		})
	}
}
