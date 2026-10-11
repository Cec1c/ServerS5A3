package dnfbridge

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"longheng.io/server/internal/modules/dnf/dnfenum"
	dnfproto "longheng.io/server/internal/modules/dnf/protocol"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/skillcmd"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

// Controlled native-size frames, NOT player captures. Unknown words are opaque.
func scope44Body(tag uint16) []byte {
	b := testLengthPrefixedBlob(bytes.Repeat([]byte{255}, 512))
	for index, v := range map[int]uint16{0: tag, 1: tag + 1, 2: 0, 22: 0, 43: tag & 1, 126: tag + 3, 127: tag + 4, 128: 0, 192: 0} {
		binary.LittleEndian.PutUint16(b[4+index*2:6+index*2], v)
	}
	return b
}
func scope44Fixture(t *testing.T) (*Service, *gameSession, dnfrepo.Group) {
	t.Helper()
	t.Setenv(currentCharacterOptionNoticeEnv, "on")
	t.Setenv("DNFBRIDGE_CHARACTER_OPTION_PROJECTION", "off")
	t.Setenv(currentGameOptionEntryGuardEnv, "on")
	s, se, r := newChannelReconnectTest(t)
	s.onlinePlayers = newOnlinePlayerManager()
	s.options.currentClientVersion = "official-24.9"
	if dir := os.Getenv("PROTOCOL44_EVIDENCE_DIR"); dir != "" {
		path := filepath.Join(dir, t.Name()+".packet.log")
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, nil, 0600); err != nil {
			t.Fatal(err)
		}
		logger, err := openPacketLogger(path)
		if err != nil {
			t.Fatal(err)
		}
		s.packetLog = logger
		oldLimit := packetLogHexLimit
		packetLogHexLimit = 2048
		t.Cleanup(func() { logger.Close(); packetLogHexLimit = oldLimit })
	}
	se.connID = "controlled44-A"
	a, ok, e := r.Character.Load(context.Background(), "29")
	if e != nil || !ok {
		t.Fatal(e)
	}
	b := dnfrepo.CloneCharacter(a)
	b.CharacterID = "30"
	b.Name = "controlled44-B"
	b.Slot = 1
	if e = r.Character.Save(context.Background(), b); e != nil {
		t.Fatal(e)
	}
	return s, se, r
}
func scope44Upper(t *testing.T, s *Service, se *gameSession, op uint16, body []byte) {
	t.Helper()
	p, e := dnfproto.BuildChannelPacket(op, body, 2, dnfproto.DefaultChannelClassification)
	if e != nil {
		t.Fatal(e)
	}
	if e = s.callGameSession(context.Background(), se, "controlled44-upper", func() error {
		return s.handleGameStreamPacket(se, dnfproto.LatestGameStreamPacket{Kind: dnfproto.LatestGameStreamUpper, Data: p})
	}); e != nil {
		t.Fatal(e)
	}
}
func scope44Select(t *testing.T, s *Service, se *gameSession, id uint16) {
	t.Helper()
	b := make([]byte, 2)
	binary.LittleEndian.PutUint16(b, id)
	scope44Upper(t, s, se, uint16(dnfenum.UpperMsgSelectCharacter), b)
	if se.selectedCharacterID != id {
		t.Fatalf("real op4 selected %d want %d", se.selectedCharacterID, id)
	}
}
func scope44Save(t *testing.T, s *Service, se *gameSession, body []byte) {
	t.Helper()
	p := buildLegacyGamePacketForBridgeTest(byte(dnfenum.GameCmdCommand), 447, 71, body)
	if e := s.callGameSession(context.Background(), se, "controlled44-447", func() error {
		return s.handleGameStreamPacket(se, dnfproto.LatestGameStreamPacket{Kind: dnfproto.LatestGameStreamLegacy, Data: p})
	}); e != nil {
		t.Fatal(e)
	}
}
func scope44Stored(t *testing.T, r dnfrepo.Group, id string) string {
	t.Helper()
	v, ok, e := r.Settings.Load(context.Background(), characterOptionScope(id))
	if e != nil || !ok {
		t.Fatalf("option %s found=%t error=%v", id, ok, e)
	}
	return v.Values["character_option_body_hex"]
}
func scope44Frames(t *testing.T, se *gameSession, label string) []dnfproto.ChannelPacket {
	t.Helper()
	c := se.conn.(*bufferConn)
	if dir := os.Getenv("PROTOCOL44_EVIDENCE_DIR"); dir != "" {
		if e := os.WriteFile(filepath.Join(dir, label+".wire.bin"), c.write.Bytes(), 0600); e != nil {
			t.Fatal(e)
		}
	}
	frames := splitAllCurrentGameServerUpperPackets(t, c.write.Bytes())
	c.write.Reset()
	rows := []map[string]any{}
	for i, p := range frames {
		h := sha256.Sum256(p.Body)
		row := map[string]any{"index": i, "class": p.Header.Classification, "opcode": p.Header.MsgID, "body_len": len(p.Body), "body_sha256": hex.EncodeToString(h[:])}
		if p.Header.MsgID == 390 || p.Header.MsgID == 19 {
			row["body_hex"] = hex.EncodeToString(p.Body)
		}
		rows = append(rows, row)
	}
	value := map[string]any{"controlled_only": true, "conn": se.connID, "selected": se.selectedCharacterID, "generation": se.characterGeneration, "route_character": se.initialTownRouteCharacterID, "route_stage": se.initialTownRouteStage, "scene_ready_character": se.townSceneReadyCharacterID, "option_notice_sent": se.gameOptionNoticeSent, "packets": rows}
	if dir := os.Getenv("PROTOCOL44_EVIDENCE_DIR"); dir != "" {
		b, e := json.MarshalIndent(value, "", "  ")
		if e != nil {
			t.Fatal(e)
		}
		if e = os.WriteFile(filepath.Join(dir, label+".json"), b, 0600); e != nil {
			t.Fatal(e)
		}
	}
	return frames
}
func scope44Fresh(se *gameSession, name string) *gameSession {
	return &gameSession{conn: &bufferConn{}, connID: name, channel: se.channel, residentChannel: se.residentChannel, connectionTownActorOwnerChannel: se.connectionTownActorOwnerChannel, townActorOwnerChannel: se.connectionTownActorOwnerChannel, currentChannelResidentNoticeSent: true, gameEndpointSuccessSent: true, accountID: se.accountID}
}
func scope44NoticeOrder(t *testing.T, p []dnfproto.ChannelPacket, want []byte) {
	t.Helper()
	option, skills := -1, -1
	for i, f := range p {
		if f.Header.Classification != 0 {
			continue
		}
		if f.Header.MsgID == 390 {
			if option >= 0 {
				t.Fatal("duplicate390")
			}
			option = i
			if !bytes.Equal(f.Body, want) {
				t.Fatal("390 differs in complete516B")
			}
		}
		if f.Header.MsgID == 19 && skills < 0 {
			skills = i
		}
	}
	if option < 0 || skills < 0 || option >= skills {
		t.Fatalf("390/19 order: option=%d skills=%d", option, skills)
	}
}
func TestCharacterOptionEntryRegressionActualBindSaveColdTwoCharacters(t *testing.T) {
	s, a, r := scope44Fixture(t)
	for _, tc := range []struct {
		id   uint16
		body []byte
	}{{29, scope44Body(0x11)}, {30, scope44Body(0x22)}} {
		se := scope44Fresh(a, "controlled44-seed")
		scope44Select(t, s, se, tc.id)
		if !gameOptionSaveWindowOpen(se) {
			t.Fatal("actual route did not open save window")
		}
		// Real public Owner Plan reads ownership from the same repository, not a test route marker.
		owner, e := skillcmd.NewOwner(r)
		if e != nil {
			t.Fatal(e)
		}
		plan, e := owner.Plan(context.Background(), skillcmd.Command{SelectedCharacterID: tc.id, AccountID: s.accountIDForSession(se)})
		if e != nil || plan.CharacterID != map[uint16]string{29: "29", 30: "30"}[tc.id] {
			t.Fatalf("Plan=%+v err=%v", plan, e)
		}
		scope44Save(t, s, se, tc.body)
		if scope44Stored(t, r, plan.CharacterID) != hex.EncodeToString(tc.body) {
			t.Fatal("447 changed complete bytes")
		}
	}
	for _, tc := range []struct {
		id   uint16
		body []byte
	}{{29, scope44Body(0x11)}, {30, scope44Body(0x22)}, {29, scope44Body(0x11)}} {
		cold := scope44Fresh(a, "controlled44-cold")
		scope44Select(t, s, cold, tc.id)
		scope44NoticeOrder(t, scope44Frames(t, cold, "cold-"+map[uint16]string{29: "29", 30: "30"}[tc.id]), tc.body)
	}
}
func TestCharacterOptionEntryRegressionReturnSelectReplaysNewCharacter390(t *testing.T) {
	s, se, r := scope44Fixture(t)
	seed := scope44Fresh(se, "controlled44-seed-B")
	scope44Select(t, s, seed, 30)
	scope44Save(t, s, seed, scope44Body(0x22))
	scope44Select(t, s, se, 29)
	scope44Save(t, s, se, scope44Body(0x11))
	scope44Frames(t, se, "switch-A-entry")
	scope44Upper(t, s, se, 7, nil)
	if se.selectedCharacterID != 0 {
		t.Fatal("real op7 did not clear selected")
	}
	scope44Save(t, s, se, scope44Body(0x33))
	if scope44Stored(t, r, "29") != hex.EncodeToString(scope44Body(0x11)) {
		t.Fatal("unselected447 alteredA")
	}
	scope44Upper(t, s, se, 8, nil)
	scope44Select(t, s, se, 30)
	scope44NoticeOrder(t, scope44Frames(t, se, "same-connection-switch-B"), scope44Body(0x22))
}
func TestCharacterOptionEntryRegressionOrdinarySelectEntry447Deferred(t *testing.T) {
	s, se, r := scope44Fixture(t)
	scope44Upper(t, s, se, 8, nil)
	scope44Select(t, s, se, 29)
	if gameOptionSaveWindowOpen(se) {
		t.Fatal("ordinary selection unexpectedly ready")
	}
	scope44Save(t, s, se, scope44Body(0x11))
	if _, found, e := r.Settings.Load(context.Background(), characterOptionScope("29")); e != nil || found {
		t.Fatalf("bootstrap447 stored found=%t err=%v", found, e)
	}
	if e := s.handleCurrentInitialTownProgress(se, 36); e != nil {
		t.Fatal(e)
	}
	if !gameOptionSaveWindowOpen(se) {
		t.Fatal("actual progress36 failed to open window")
	}
	scope44Save(t, s, se, scope44Body(0x11))
	if scope44Stored(t, r, "29") != hex.EncodeToString(scope44Body(0x11)) {
		t.Fatal("postroute447failed")
	}
	scope44Frames(t, se, "ordinary-entry-deferred-then-ready")
}

func TestCharacterOptionEntryRegressionActualSkillResetDoesNotRewriteCharacterOptions(t *testing.T) {
	path := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if path == "" {
		t.Skip("fixed QA PVF required")
	}
	s, se, r := scope44Fixture(t)
	scope44Select(t, s, se, 29)
	scope44Save(t, s, se, scope44Body(0x11))
	scope44Frames(t, se, "before-skill-reset")
	// Reset's authoritative profession rules require the actual fixed QA PVF
	// and a controlled, explicit growth record. Missing rules must refuse reset.
	archive, err := platformpvf.LoadArchive(platformpvf.Options{Path: path})
	if err != nil {
		t.Fatal(err)
	}
	s.initialEquipmentArchive = archive
	s.skillCatalog = nil
	s.initialSkillsByJob = nil
	character, _, err := r.Character.Load(context.Background(), "29")
	if err != nil {
		t.Fatal(err)
	}
	character.Stats["grow_type"] = 0x11
	if err = r.Character.Save(context.Background(), character); err != nil {
		t.Fatal(err)
	}
	p := buildLegacyGamePacketForBridgeTest(byte(dnfenum.GameCmdCommand), uint16(dnfenum.CmdPacketSkillInit), 72, []byte{0, 0})
	if e := s.handleGameStreamPacket(se, dnfproto.LatestGameStreamPacket{Kind: dnfproto.LatestGameStreamLegacy, Data: p}); e != nil {
		t.Fatal(e)
	}
	if scope44Stored(t, r, "29") != hex.EncodeToString(scope44Body(0x11)) {
		t.Fatal("actual491rewrote options")
	}
	frames := scope44Frames(t, se, "after-actual491-reset")
	ack, skills := false, false
	for _, f := range frames {
		if f.Header.Classification == 1 && f.Header.MsgID == uint16(dnfenum.CmdPacketSkillInit) && len(f.Body) > 0 && f.Body[0] == 1 {
			ack = true
		}
		if f.Header.Classification == 0 && f.Header.MsgID == 19 {
			skills = true
		}
	}
	if !ack || !skills {
		t.Fatalf("actual491 successful ack=%t skillinfo=%t", ack, skills)
	}
}

func TestCharacterOptionEntryRegressionAccountReplayGateUnchangedAndSameCharacterReentry(t *testing.T) {
	s, se, r := scope44Fixture(t)
	seed := scope44Fresh(se, "controlled44-account-seed")
	scope44Select(t, s, seed, 29)
	scope44Save(t, s, seed, scope44Body(0x11))
	account := testLengthPrefixedBlob(bytes.Repeat([]byte{0x44}, 178))
	p := buildLegacyGamePacketForBridgeTest(byte(dnfenum.GameCmdCommand), 197, 73, account)
	if e := s.handleGameStreamPacket(seed, dnfproto.LatestGameStreamPacket{Kind: dnfproto.LatestGameStreamLegacy, Data: p}); e != nil {
		t.Fatal(e)
	}
	scope44Select(t, s, se, 29)
	first := scope44Frames(t, se, "account-gate-first-entry")
	accountCount := 0
	for _, f := range first {
		if f.Header.Classification == 0 && f.Header.MsgID == 173 {
			accountCount++
		}
	}
	if accountCount != 1 {
		t.Fatalf("first account replay=%d", accountCount)
	}
	scope44Upper(t, s, se, 7, nil)
	scope44Upper(t, s, se, 8, nil)
	scope44Select(t, s, se, 29)
	second := scope44Frames(t, se, "same-character-reentry")
	for _, f := range second {
		if f.Header.Classification == 0 && f.Header.MsgID == 173 {
			t.Fatal("candidate repeated accepted account replay")
		}
	}
	scope44NoticeOrder(t, second, scope44Body(0x11))
	if scope44Stored(t, r, "29") != hex.EncodeToString(scope44Body(0x11)) {
		t.Fatal("samecharacterreentry changedblob")
	}
}

func TestCharacterOptionEntryRegressionNormalGameSwitchABARestoresWholeBlob(t *testing.T) {
	s, se, r := scope44Fixture(t)
	// Seed B and A through the very same ordinary game connection, real op4
	// selection, actor route, opcode447 dispatch and op7/op8 return lifecycle.
	scope44Select(t, s, se, 30)
	scope44Save(t, s, se, scope44Body(0x22))
	scope44Frames(t, se, "normal-seed-B")
	scope44Upper(t, s, se, 7, nil)
	scope44Upper(t, s, se, 8, nil)
	scope44Select(t, s, se, 29)
	scope44Save(t, s, se, scope44Body(0x11))
	scope44Frames(t, se, "normal-seed-A")
	beforeA, _, err := r.Skill.Load(context.Background(), "29")
	if err != nil {
		t.Fatal(err)
	}
	beforeB, _, err := r.Skill.Load(context.Background(), "30")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		id    uint16
		body  []byte
		label string
	}{{30, scope44Body(0x22), "normal-A-to-B"}, {29, scope44Body(0x11), "normal-B-to-A"}} {
		scope44Upper(t, s, se, 7, nil)
		scope44Upper(t, s, se, 8, nil)
		scope44Select(t, s, se, tc.id)
		p := scope44Frames(t, se, tc.label)
		oi, si := -1, -1
		for i, f := range p {
			if f.Header.Classification != 0 {
				continue
			}
			if f.Header.MsgID == 390 {
				oi = i
				if !bytes.Equal(f.Body, tc.body) {
					t.Errorf("%s complete516B differ", tc.label)
				}
			}
			if f.Header.MsgID == 19 && si < 0 {
				si = i
			}
		}
		if oi < 0 || si < 0 || oi >= si {
			t.Errorf("%s actual390=%d actual19=%d", tc.label, oi, si)
		}
	}
	if scope44Stored(t, r, "29") != hex.EncodeToString(scope44Body(0x11)) || scope44Stored(t, r, "30") != hex.EncodeToString(scope44Body(0x22)) {
		t.Fatal("ordinary game447 saved to wrong character")
	}
	afterA, _, err := r.Skill.Load(context.Background(), "29")
	if err != nil {
		t.Fatal(err)
	}
	afterB, _, err := r.Skill.Load(context.Background(), "30")
	if err != nil {
		t.Fatal(err)
	}
	if beforeA.Points != afterA.Points || beforeB.Points != afterB.Points {
		t.Fatal("ordinary options switching changed SP/TP ledger")
	}
}

func TestCharacterOptionEntryRegressionSameEntryRouteRetryDoesNotDuplicate390(t *testing.T) {
	s, se, _ := scope44Fixture(t)
	scope44Select(t, s, se, 29)
	scope44Save(t, s, se, scope44Body(0x11))
	scope44Frames(t, se, "retry-seed-entry")
	scope44Upper(t, s, se, 7, nil)
	scope44Upper(t, s, se, 8, nil)
	scope44Select(t, s, se, 29)
	first := scope44Frames(t, se, "retry-entry-first-replay")
	count := 0
	for _, f := range first {
		if f.Header.Classification == 0 && f.Header.MsgID == 390 {
			count++
		}
	}
	for i := 0; i < 2; i++ {
		if err := s.handleCurrentInitialTownProgress(se, 36); err != nil {
			t.Fatal(err)
		}
	}
	retry := scope44Frames(t, se, "retry-entry-repeated-progress36")
	for _, f := range retry {
		if f.Header.Classification == 0 && f.Header.MsgID == 390 {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("same actual entry plus two route retries emitted390=%d want1", count)
	}
}
