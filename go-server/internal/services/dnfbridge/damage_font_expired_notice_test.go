package dnfbridge

import "testing"

func TestDamageFontExpiredNoticeUsesNativeSystemText(t *testing.T) {
	s := &Service{options: options{currentClientVersion: "official-24.9", gameUpperBodyCodec: gameUpperBodyCodecPlain}}
	conn := &bufferConn{}
	session := &gameSession{selectedCharacterID: 47, conn: conn}
	if err := s.sendDamageFontExpiredNotice(session); err != nil {
		t.Fatal(err)
	}
	assertRiftUnlockSystemText(t, conn.write.Bytes(), "该伤害字体已超过配置的使用期限，无法使用。道具未消耗。")
}
