package dnfbridge

import (
	"bytes"
	"context"
	"errors"
	"reflect"
	"sync"
	"testing"
	"time"

	darkcombo "longheng.io/server/internal/modules/dnf/darkknightcombo"
	"longheng.io/server/internal/modules/dnf/dnfenum"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	dnfrepomemory "longheng.io/server/internal/modules/dnf/repository/memory"
	dnfskill "longheng.io/server/internal/modules/dnf/skill"
)

type darkKnightComboFailingSkillRepository struct {
	base dnfrepo.SkillRepository
	err  error
}

func (r darkKnightComboFailingSkillRepository) Load(ctx context.Context, key string) (dnfrepo.SkillRecord, bool, error) {
	return r.base.Load(ctx, key)
}

func (r darkKnightComboFailingSkillRepository) Save(context.Context, dnfrepo.SkillRecord) error {
	return r.err
}

type darkKnightComboFailingSettlement struct {
	base dnfrepo.CharacterSettlementUnitOfWork
	err  error
}

func (u darkKnightComboFailingSettlement) WithinCharacterSettlement(
	ctx context.Context,
	characterID string,
	apply func(dnfrepo.Group) error,
) error {
	return u.base.WithinCharacterSettlement(ctx, characterID, func(tx dnfrepo.Group) error {
		tx.Skill = darkKnightComboFailingSkillRepository{base: tx.Skill, err: u.err}
		return apply(tx)
	})
}

func TestSendCurrentDarkKnightComboInitializationUsesNativeClass0Op447(t *testing.T) {
	repositories := dnfrepomemory.NewMemoryGroup()
	character := dnfrepo.CharacterRecord{CharacterID: "77", AccountID: "account-1", Job: "9", Level: 1}
	if err := repositories.Character.Save(context.Background(), character); err != nil {
		t.Fatal(err)
	}
	if err := repositories.Skill.Save(context.Background(), currentDarkKnightComboNativeTestRecord("77")); err != nil {
		t.Fatal(err)
	}
	pages := []darkcombo.Page{{Index: 0, Roots: []darkcombo.Root{{SkillID: 118, Children: []uint16{46, 8}}}}}
	if err := darkcombo.SavePages(context.Background(), repositories.Settings, "77", pages, time.Unix(1, 0)); err != nil {
		t.Fatal(err)
	}
	connection := &bufferConn{}
	service := &Service{
		options:            options{accountID: "account-1", gameUpperBodyCodec: gameUpperBodyCodecPlain},
		repositoryProvider: func() (dnfrepo.Group, bool) { return repositories, true },
		skillCatalog:       &dnfskill.Table{},
		initialSPTable:     map[int]int{1: 0},
		initialTPTable:     map[int]int{1: 0},
	}
	if err := service.sendCurrentDarkKnightComboInitialization(
		&gameSession{conn: connection, accountID: "account-1", selectedCharacterID: 77},
		context.Background(), character, "test",
	); err != nil {
		t.Fatal(err)
	}
	packet, trailing := splitGameServerUpperPacket(t, connection.write.Bytes())
	if len(trailing) != 0 || packet.Header.Classification != 0 || packet.Header.MsgID != currentDarkKnightComboInitializeMsgID {
		t.Fatalf("header=%+v trailing=%x", packet.Header, trailing)
	}
	want, err := darkcombo.EncodeNotificationBody(pages)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(packet.Body, want) {
		t.Fatalf("body=%x want=%x", packet.Body, want)
	}
}

func TestDarkKnightOp19ImmediatelyRestoresNativeComboBeforeHUDConsumers(t *testing.T) {
	repositories := dnfrepomemory.NewMemoryGroup()
	character := dnfrepo.CharacterRecord{CharacterID: "77", AccountID: "account-1", Job: "9", Level: 1}
	if err := repositories.Character.Save(context.Background(), character); err != nil {
		t.Fatal(err)
	}
	if err := repositories.Skill.Save(context.Background(), currentDarkKnightComboNativeTestRecord("77")); err != nil {
		t.Fatal(err)
	}
	pages := []darkcombo.Page{
		{Index: 0, Roots: []darkcombo.Root{{SkillID: 118, Children: []uint16{46}}}},
		{Index: 1, Roots: []darkcombo.Root{{SkillID: 119, Children: []uint16{8}}}},
	}
	if err := darkcombo.SavePages(context.Background(), repositories.Settings, "77", pages, time.Unix(1, 0)); err != nil {
		t.Fatal(err)
	}
	connection := &bufferConn{}
	service := &Service{
		options:            options{accountID: "account-1", gameUpperBodyCodec: gameUpperBodyCodecPlain},
		repositoryProvider: func() (dnfrepo.Group, bool) { return repositories, true },
		skillCatalog:       &dnfskill.Table{},
	}
	session := &gameSession{conn: connection, accountID: "account-1", selectedCharacterID: 77}
	projection := currentSceneSkillInfoProjection{
		body:        []byte{0x11, 0x22},
		characterID: "77",
		job:         darkcombo.Job,
	}
	if err := service.sendCurrentSceneSkillInfoProjection(session, projection, "test_hud_order"); err != nil {
		t.Fatal(err)
	}
	packets := splitAllCurrentGameServerUpperPackets(t, connection.write.Bytes())
	if len(packets) != 2 {
		t.Fatalf("packet count=%d want op19+op447", len(packets))
	}
	if packets[0].Header.MsgID != currentSkillInfoMsgID || packets[0].Header.Classification != 0 {
		t.Fatalf("first packet=%+v want class0/op19", packets[0].Header)
	}
	if packets[1].Header.MsgID != currentDarkKnightComboInitializeMsgID || packets[1].Header.Classification != 0 {
		t.Fatalf("second packet=%+v want class0/op447", packets[1].Header)
	}
}

func TestDarkKnightDungeonLevelRefreshKeepsPerTreeComboChildren(t *testing.T) {
	repositories := dnfrepomemory.NewMemoryGroup()
	character := dnfrepo.CharacterRecord{
		CharacterID: "77",
		AccountID:   "account-1",
		Job:         "9",
		Level:       2,
		Stats:       map[string]int64{"skill_tree_index": 0},
	}
	record := currentDarkKnightComboNativeTestRecord("77")
	if err := repositories.Character.Save(context.Background(), character); err != nil {
		t.Fatal(err)
	}
	if err := repositories.Skill.Save(context.Background(), record); err != nil {
		t.Fatal(err)
	}
	pages := []darkcombo.Page{
		{Index: 0, Roots: []darkcombo.Root{{SkillID: 118, Children: []uint16{46}}}},
		{Index: 1, Roots: []darkcombo.Root{{SkillID: 118, Children: []uint16{8}}}},
	}
	if err := darkcombo.SavePages(context.Background(), repositories.Settings, "77", pages, time.Unix(1, 0)); err != nil {
		t.Fatal(err)
	}
	connection := &bufferConn{}
	service := &Service{
		options:            options{accountID: "account-1", gameUpperBodyCodec: gameUpperBodyCodecPlain},
		repositoryProvider: func() (dnfrepo.Group, bool) { return repositories, true },
		skillCatalog:       currentDarkKnightComboNativeTestCatalog(t),
	}
	session := &gameSession{conn: connection, accountID: "account-1", selectedCharacterID: 77}
	if err := service.sendCommittedDarkKnightSkillRefresh(session, character, record, "test_dark_knight_level_refresh"); err != nil {
		t.Fatal(err)
	}
	packets := splitAllCurrentGameServerUpperPackets(t, connection.write.Bytes())
	if len(packets) != 2 || packets[0].Header.MsgID != currentSkillInfoMsgID ||
		packets[1].Header.MsgID != currentDarkKnightComboInitializeMsgID {
		t.Fatalf("packets=%+v, want op19 then op447", packets)
	}
	_, outerMessages := consumeCurrentSkillInfoFields(t, packets[0].Body[4:])
	want := [][]uint64{{46}, {8}}
	for treeIndex, rawTree := range outerMessages[2] {
		_, treeMessages := consumeCurrentSkillInfoFields(t, rawTree)
		var got []uint64
		for _, rawSkill := range treeMessages[3] {
			fields, _ := consumeCurrentSkillInfoFields(t, rawSkill)
			if uint16(fields[2][0]) == 118 {
				got = fields[4]
				break
			}
		}
		if !reflect.DeepEqual(got, want[treeIndex]) {
			t.Fatalf("tree %d combo children=%v want=%v", treeIndex, got, want[treeIndex])
		}
	}
}

func TestDarkKnightComboSessionResetClearsCharacterScopedGates(t *testing.T) {
	now := time.Unix(100, 0).UTC()
	state := &darkKnightComboSessionState{}
	state.arm(77, 1, now.Add(time.Minute))
	state.recordProjection(77, []darkcombo.Page{{
		Index: 0,
		Roots: []darkcombo.Root{{SkillID: 118, Children: []uint16{46}}},
	}}, now)
	state.mu.Lock()
	state.finalTownSceneCharacterID = 77
	state.finalTownSceneGeneration = 9
	state.finalTownSceneInitialization = true
	state.mu.Unlock()

	state.reset()

	if state.consume(77, 1, now.Add(time.Second)) {
		t.Fatal("reset retained op510 token")
	}
	guard := state.inspectSave(77, darkcombo.Page{Index: 0}, now.Add(time.Second), false)
	if guard.generation != 0 || guard.reject || guard.canonicalEcho {
		t.Fatalf("reset retained projection guard: %+v", guard)
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.finalTownSceneCharacterID != 0 || state.finalTownSceneGeneration != 0 || state.finalTownSceneInitialization {
		t.Fatalf("reset retained final-town gate: %+v", state)
	}
}

func TestDarkKnightComboResetTokenSurvivesMismatchAndExpires(t *testing.T) {
	now := time.Unix(100, 0).UTC()
	state := &darkKnightComboSessionState{}
	state.arm(77, 0, now.Add(10*time.Second))
	if state.consume(77, 1, now.Add(time.Second)) {
		t.Fatal("mismatched page consumed the reset token")
	}
	if !state.consume(77, 0, now.Add(2*time.Second)) {
		t.Fatal("matching page did not consume the preserved reset token")
	}
	state.arm(77, 0, now.Add(10*time.Second))
	if state.consume(78, 0, now.Add(11*time.Second)) {
		t.Fatal("expired reset token matched")
	}
	if state.consume(77, 0, now.Add(9*time.Second)) {
		t.Fatal("expired reset token was not cleared")
	}
}

func TestDarkKnightComboProjectionGuardExpiresAfterSceneEchoWindow(t *testing.T) {
	now := time.Unix(100, 0).UTC()
	state := &darkKnightComboSessionState{}
	generation := state.recordProjection(77, []darkcombo.Page{{
		Index: 0,
		Roots: []darkcombo.Root{{SkillID: 118, Children: []uint16{46}}},
	}}, now)
	guard := state.inspectSave(77, darkcombo.Page{Index: 0, Roots: []darkcombo.Root{{SkillID: 118}}}, now.Add(9*time.Second), false)
	if !guard.reject || guard.generation != generation {
		t.Fatalf("inside echo window guard=%+v generation=%d", guard, generation)
	}
	guard = state.inspectSave(77, darkcombo.Page{Index: 0, Roots: []darkcombo.Root{{SkillID: 118}}}, now.Add(currentDarkKnightComboProjectionEchoWindow), false)
	if guard.reject {
		t.Fatalf("expired projection still rejected: %+v", guard)
	}
}

func TestCurrentDarkKnightComboRejectsSceneEchoEmptySaveAndRestoresProjection(t *testing.T) {
	repositories := dnfrepomemory.NewMemoryGroup()
	character := dnfrepo.CharacterRecord{CharacterID: "77", AccountID: "account-1", Job: "9", Level: 1}
	if err := repositories.Character.Save(context.Background(), character); err != nil {
		t.Fatal(err)
	}
	if err := repositories.Skill.Save(context.Background(), currentDarkKnightComboNativeTestRecord("77")); err != nil {
		t.Fatal(err)
	}
	canonical := []darkcombo.Page{{Index: 0, Roots: []darkcombo.Root{{SkillID: 118, Children: []uint16{46}}}}}
	if err := darkcombo.SavePages(context.Background(), repositories.Settings, "77", canonical, time.Unix(1, 0)); err != nil {
		t.Fatal(err)
	}
	now := time.Unix(100, 0).UTC()
	connection := &bufferConn{}
	service := &Service{
		options:            options{accountID: "account-1", gameUpperBodyCodec: gameUpperBodyCodecPlain},
		repositoryProvider: func() (dnfrepo.Group, bool) { return repositories, true },
		skillCatalog:       &dnfskill.Table{},
		initialSPTable:     map[int]int{1: 0},
		initialTPTable:     map[int]int{1: 0},
		gameplayTimers:     &processGameplayTimeQueue{now: func() time.Time { return now }},
	}
	session := &gameSession{conn: connection, accountID: "account-1", selectedCharacterID: 77}
	if err := service.sendCurrentDarkKnightComboInitialization(session, context.Background(), character, "test_projection"); err != nil {
		t.Fatal(err)
	}
	connection.write.Reset()
	now = now.Add(9 * time.Second)
	emptyBody, err := darkcombo.EncodePageBlock(darkcombo.Page{Index: 0, Roots: []darkcombo.Root{{SkillID: 118}}})
	if err != nil {
		t.Fatal(err)
	}
	if err := service.handleCurrentDarkKnightComboSkillInfo(session, emptyBody); err != nil {
		t.Fatal(err)
	}
	packets := splitAllCurrentGameServerUpperPackets(t, connection.write.Bytes())
	if len(packets) != 2 {
		t.Fatalf("packet count=%d want failure+authoritative op447", len(packets))
	}
	if packets[0].Header.Classification != 1 || packets[0].Header.MsgID != 508 || !bytes.Equal(packets[0].Body, []byte{0, 4}) {
		t.Fatalf("failure header=%+v body=%x", packets[0].Header, packets[0].Body)
	}
	wantProjection, err := darkcombo.EncodeNotificationBody(canonical)
	if err != nil {
		t.Fatal(err)
	}
	if packets[1].Header.Classification != 0 || packets[1].Header.MsgID != 447 || !bytes.Equal(packets[1].Body, wantProjection) {
		t.Fatalf("restore header=%+v body=%x want=%x", packets[1].Header, packets[1].Body, wantProjection)
	}
	pages, found, err := darkcombo.LoadPages(context.Background(), repositories.Settings, "77")
	if err != nil || !found || len(pages) != 1 || len(pages[0].Roots) != 1 || len(pages[0].Roots[0].Children) != 1 || pages[0].Roots[0].Children[0] != 46 {
		t.Fatalf("authoritative pages changed: pages=%+v found=%t err=%v", pages, found, err)
	}
}

func TestCurrentDarkKnightComboExplicitResetAllowsImmediateEmptySave(t *testing.T) {
	repositories, service, session, connection, setNow := currentDarkKnightComboProjectionTestHarness(t)
	connection.write.Reset()
	setNow(time.Unix(105, 0).UTC())
	if err := service.handleCurrentDarkKnightComboQuickSlotReset(session, []byte{0, 2}); err != nil {
		t.Fatal(err)
	}
	resetPackets := splitAllCurrentGameServerUpperPackets(t, connection.write.Bytes())
	if len(resetPackets) != 1 || resetPackets[0].Header.MsgID != 510 || !bytes.Equal(resetPackets[0].Body, []byte{1}) {
		t.Fatalf("reset packets=%+v", resetPackets)
	}
	connection.write.Reset()
	emptyBody, err := darkcombo.EncodePageBlock(darkcombo.Page{Index: 0, Roots: []darkcombo.Root{{SkillID: 118}}})
	if err != nil {
		t.Fatal(err)
	}
	if err := service.handleCurrentDarkKnightComboSkillInfo(session, emptyBody); err != nil {
		t.Fatal(err)
	}
	packets := splitAllCurrentGameServerUpperPackets(t, connection.write.Bytes())
	if len(packets) != 1 || packets[0].Header.MsgID != 508 || !bytes.Equal(packets[0].Body, []byte{1}) {
		t.Fatalf("empty save packets=%+v", packets)
	}
	pages, found, err := darkcombo.LoadPages(context.Background(), repositories.Settings, "77")
	if err != nil || !found || len(pages) != 1 || darkKnightComboPageNonEmpty(pages[0]) {
		t.Fatalf("explicit empty save not persisted: pages=%+v found=%t err=%v", pages, found, err)
	}
}

func TestCurrentDarkKnightAutoComboChangedLayoutSendsOp19ThenResetACKWithoutOp447(t *testing.T) {
	repositories, service, session, connection, setNow := currentDarkKnightComboProjectionTestHarness(t)
	record, found, err := repositories.Skill.Load(context.Background(), "77")
	if err != nil || !found {
		t.Fatalf("load skill record found=%t err=%v", found, err)
	}
	// Skill 46 is a saved combo child.  The reset pre-clean must remove it
	// from the ordinary shortcut vector before the client generates a page.
	record.Layouts[0] = dnfrepo.SkillLayout{0: 118, 198: 46}
	if err := repositories.Skill.Save(context.Background(), record); err != nil {
		t.Fatal(err)
	}

	connection.write.Reset()
	setNow(time.Unix(105, 0).UTC())
	if err := service.handleCurrentDarkKnightComboQuickSlotReset(session, []byte{0, 2}); err != nil {
		t.Fatal(err)
	}
	packets := splitAllCurrentGameServerUpperPackets(t, connection.write.Bytes())
	if len(packets) != 2 {
		t.Fatalf("reset packets=%+v, want op19 then op510 ACK", packets)
	}
	if packets[0].Header.Classification != 0 || packets[0].Header.MsgID != currentSkillInfoMsgID {
		t.Fatalf("first reset packet=%+v, want class0/op19", packets[0].Header)
	}
	if packets[1].Header.Classification != 1 || packets[1].Header.MsgID != 510 ||
		!bytes.Equal(packets[1].Body, []byte{1}) {
		t.Fatalf("second reset packet header=%+v body=%x, want class1/op510 success", packets[1].Header, packets[1].Body)
	}
	for _, packet := range packets {
		if packet.Header.MsgID == currentDarkKnightComboInitializeMsgID {
			t.Fatalf("reset transaction was interrupted by op447: packets=%+v", packets)
		}
	}

	// The client-generated page is the next half of the same transaction and
	// must be accepted through the dedicated op508 persistence path.
	connection.write.Reset()
	body, err := darkcombo.EncodePageBlock(darkcombo.Page{
		Index: 0,
		Roots: []darkcombo.Root{{SkillID: 118, Children: []uint16{46}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := service.handleCurrentDarkKnightComboSkillInfo(session, body); err != nil {
		t.Fatal(err)
	}
	packets = splitAllCurrentGameServerUpperPackets(t, connection.write.Bytes())
	if len(packets) != 1 || packets[0].Header.Classification != 1 || packets[0].Header.MsgID != 508 ||
		!bytes.Equal(packets[0].Body, []byte{1}) {
		t.Fatalf("auto-combo save packets=%+v, want class1/op508 success", packets)
	}
}

func TestCurrentDarkKnightComboNonEmptyEditThenEmptySaveIsAccepted(t *testing.T) {
	repositories, service, session, connection, setNow := currentDarkKnightComboProjectionTestHarness(t)
	connection.write.Reset()
	setNow(time.Unix(105, 0).UTC())
	nonEmptyBody, err := darkcombo.EncodePageBlock(darkcombo.Page{Index: 0, Roots: []darkcombo.Root{{SkillID: 118, Children: []uint16{8}}}})
	if err != nil {
		t.Fatal(err)
	}
	if err := service.handleCurrentDarkKnightComboSkillInfo(session, nonEmptyBody); err != nil {
		t.Fatal(err)
	}
	connection.write.Reset()
	setNow(time.Unix(106, 0).UTC())
	emptyBody, err := darkcombo.EncodePageBlock(darkcombo.Page{Index: 0, Roots: []darkcombo.Root{{SkillID: 118}}})
	if err != nil {
		t.Fatal(err)
	}
	if err := service.handleCurrentDarkKnightComboSkillInfo(session, emptyBody); err != nil {
		t.Fatal(err)
	}
	packets := splitAllCurrentGameServerUpperPackets(t, connection.write.Bytes())
	if len(packets) != 1 || packets[0].Header.MsgID != 508 || !bytes.Equal(packets[0].Body, []byte{1}) {
		t.Fatalf("empty save packets=%+v", packets)
	}
	pages, found, err := darkcombo.LoadPages(context.Background(), repositories.Settings, "77")
	if err != nil || !found || len(pages) != 1 || darkKnightComboPageNonEmpty(pages[0]) {
		t.Fatalf("edited empty save not persisted: pages=%+v found=%t err=%v", pages, found, err)
	}
}

func TestCurrentDarkKnightComboCanonicalEchoDoesNotUnlockSceneEmptySave(t *testing.T) {
	repositories, service, session, connection, setNow := currentDarkKnightComboProjectionTestHarness(t)
	connection.write.Reset()
	setNow(time.Unix(105, 0).UTC())
	canonicalBody, err := darkcombo.EncodePageBlock(darkcombo.Page{
		Index: 0,
		Roots: []darkcombo.Root{{SkillID: 118, Children: []uint16{46}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := service.handleCurrentDarkKnightComboSkillInfo(session, canonicalBody); err != nil {
		t.Fatal(err)
	}
	connection.write.Reset()
	setNow(time.Unix(109, 0).UTC())
	emptyBody, err := darkcombo.EncodePageBlock(darkcombo.Page{Index: 0, Roots: []darkcombo.Root{{SkillID: 118}}})
	if err != nil {
		t.Fatal(err)
	}
	if err := service.handleCurrentDarkKnightComboSkillInfo(session, emptyBody); err != nil {
		t.Fatal(err)
	}
	packets := splitAllCurrentGameServerUpperPackets(t, connection.write.Bytes())
	if len(packets) != 2 || packets[0].Header.MsgID != 508 || !bytes.Equal(packets[0].Body, []byte{0, 4}) ||
		packets[1].Header.MsgID != 447 {
		t.Fatalf("canonical echo unlocked empty save: packets=%+v", packets)
	}
	pages, found, err := darkcombo.LoadPages(context.Background(), repositories.Settings, "77")
	if err != nil || !found || len(pages) != 1 || len(pages[0].Roots[0].Children) != 1 || pages[0].Roots[0].Children[0] != 46 {
		t.Fatalf("canonical page changed after empty echo: pages=%+v found=%t err=%v", pages, found, err)
	}
}

func TestCurrentDarkKnightComboAutoCommitRollsBackPageWhenLayoutSaveFails(t *testing.T) {
	repositories := dnfrepomemory.NewMemoryGroup()
	character := dnfrepo.CharacterRecord{CharacterID: "77", AccountID: "account-1", Job: "9", Level: 90}
	if err := repositories.Character.Save(context.Background(), character); err != nil {
		t.Fatal(err)
	}
	record := currentDarkKnightComboNativeTestRecord("77")
	record.Skills[8] = dnfrepo.SkillState{Level: 1, Enabled: true}

	record.Layouts[0] = dnfrepo.SkillLayout{0: 8, 6: 46}
	if err := repositories.Skill.Save(context.Background(), record); err != nil {
		t.Fatal(err)
	}
	canonical := []darkcombo.Page{{Index: 0, Roots: []darkcombo.Root{{SkillID: 118, Children: []uint16{46}}}}}
	if err := darkcombo.SavePages(context.Background(), repositories.Settings, "77", canonical, time.Unix(1, 0)); err != nil {
		t.Fatal(err)
	}
	wantErr := errors.New("injected skill layout persistence failure")
	repositories.CharacterSettlement = darkKnightComboFailingSettlement{
		base: repositories.CharacterSettlement,
		err:  wantErr,
	}
	connection := &bufferConn{}
	service := &Service{
		options:            options{accountID: "account-1", gameUpperBodyCodec: gameUpperBodyCodecPlain},
		repositoryProvider: func() (dnfrepo.Group, bool) { return repositories, true },
		skillCatalog:       &dnfskill.Table{},
	}
	session := &gameSession{conn: connection, accountID: "account-1", selectedCharacterID: 77}
	session.darkKnightCombo.arm(77, 0, service.currentDarkKnightComboNow().Add(time.Minute))
	incoming, err := darkcombo.EncodePageBlock(darkcombo.Page{
		Index: 0,
		Roots: []darkcombo.Root{{SkillID: 118, Children: []uint16{8}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := service.handleCurrentDarkKnightComboSkillInfo(session, incoming); err != nil {
		t.Fatal(err)
	}

	pages, found, err := darkcombo.LoadPages(context.Background(), repositories.Settings, "77")
	if err != nil || !found || len(pages) != 1 || len(pages[0].Roots) != 1 ||
		!bytes.Equal(mustDarkKnightComboPageBlock(t, pages[0]), mustDarkKnightComboPageBlock(t, canonical[0])) {
		t.Fatalf("combo page half-committed after layout failure: pages=%+v found=%t err=%v", pages, found, err)
	}
	persisted, found, err := repositories.Skill.Load(context.Background(), "77")
	if err != nil || !found || persisted.Layouts[0][0] != 8 || persisted.Layouts[0][6] != 46 {
		t.Fatalf("skill layout changed after rollback: record=%+v found=%t err=%v", persisted.Layouts, found, err)
	}
	packets := splitAllCurrentGameServerUpperPackets(t, connection.write.Bytes())
	if len(packets) != 2 || packets[0].Header.MsgID != 508 || !bytes.Equal(packets[0].Body, []byte{0, 4}) ||
		packets[1].Header.Classification != 0 || packets[1].Header.MsgID != 447 {
		t.Fatalf("rollback correction packets=%+v", packets)
	}
}

func TestCurrentDarkKnightComboManualPagesCommitWithoutCrossPageLostUpdate(t *testing.T) {
	repositories := dnfrepomemory.NewMemoryGroup()
	if err := repositories.Character.Save(context.Background(), dnfrepo.CharacterRecord{
		CharacterID: "77",
		AccountID:   "account-1",
		Job:         "9",
	}); err != nil {
		t.Fatal(err)
	}
	if err := repositories.Skill.Save(context.Background(), currentDarkKnightComboNativeTestRecord("77")); err != nil {
		t.Fatal(err)
	}
	template := []darkcombo.Page{
		{Index: 0, Roots: []darkcombo.Root{{SkillID: 118}}},
		{Index: 1, Roots: []darkcombo.Root{{SkillID: 119}}},
	}
	if err := darkcombo.SavePages(context.Background(), repositories.Settings, "77", template, time.Unix(1, 0)); err != nil {
		t.Fatal(err)
	}
	writes := []darkcombo.Page{
		{Index: 0, Roots: []darkcombo.Root{{SkillID: 118, Children: []uint16{46}}}},
		{Index: 1, Roots: []darkcombo.Root{{SkillID: 119, Children: []uint16{8}}}},
	}
	start := make(chan struct{})
	errs := make(chan error, len(writes))
	var workers sync.WaitGroup
	for _, page := range writes {
		page := page
		workers.Add(1)
		go func() {
			defer workers.Done()
			<-start
			_, _, err := persistCurrentDarkKnightComboPage(
				context.Background(),
				repositories,
				"77",
				page,
				false,
				time.Unix(2, 0),
			)
			errs <- err
		}()
	}
	close(start)
	workers.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	pages, found, err := darkcombo.LoadPages(context.Background(), repositories.Settings, "77")
	if err != nil || !found || len(pages) != 2 {
		t.Fatalf("concurrent pages=%+v found=%t err=%v", pages, found, err)
	}
	if got := pages[0].Roots[0].Children; len(got) != 1 || got[0] != 46 {
		t.Fatalf("page0 children=%v want=[46]", got)
	}
	if got := pages[1].Roots[0].Children; len(got) != 1 || got[0] != 8 {
		t.Fatalf("page1 children=%v want=[8]", got)
	}
}

func TestCurrentDarkKnightComboRejectsRootMismatchAndUnlearnedChild(t *testing.T) {
	for _, test := range []struct {
		name string
		page darkcombo.Page
	}{
		{name: "root mismatch", page: darkcombo.Page{Index: 0, Roots: []darkcombo.Root{{SkillID: 119, Children: []uint16{46}}}}},
		{name: "unlearned child", page: darkcombo.Page{Index: 0, Roots: []darkcombo.Root{{SkillID: 118, Children: []uint16{999}}}}},
	} {
		t.Run(test.name, func(t *testing.T) {
			repositories, service, session, connection, _ := currentDarkKnightComboProjectionTestHarness(t)
			connection.write.Reset()
			body, err := darkcombo.EncodePageBlock(test.page)
			if err != nil {
				t.Fatal(err)
			}
			if err := service.handleCurrentDarkKnightComboSkillInfo(session, body); err != nil {
				t.Fatal(err)
			}
			pages, found, err := darkcombo.LoadPages(context.Background(), repositories.Settings, "77")
			if err != nil || !found || len(pages) != 1 || pages[0].Roots[0].SkillID != 118 ||
				len(pages[0].Roots[0].Children) != 1 || pages[0].Roots[0].Children[0] != 46 {
				t.Fatalf("invalid save changed durable page: pages=%+v found=%t err=%v", pages, found, err)
			}
			packets := splitAllCurrentGameServerUpperPackets(t, connection.write.Bytes())
			if len(packets) != 2 || packets[0].Header.MsgID != 508 || !bytes.Equal(packets[0].Body, []byte{0, 4}) ||
				packets[1].Header.MsgID != 447 {
				t.Fatalf("invalid save correction packets=%+v", packets)
			}
		})
	}
}

func mustDarkKnightComboPageBlock(t *testing.T, page darkcombo.Page) []byte {
	t.Helper()
	body, err := darkcombo.EncodePageBlock(page)
	if err != nil {
		t.Fatal(err)
	}
	return body
}

func TestCurrentDarkKnightComboResetRejectsInvalidModeThenRestores(t *testing.T) {
	_, service, session, connection, setNow := currentDarkKnightComboProjectionTestHarness(t)
	connection.write.Reset()
	setNow(time.Unix(105, 0).UTC())
	if err := service.handleCurrentDarkKnightComboQuickSlotReset(session, []byte{0, 1}); err != nil {
		t.Fatal(err)
	}
	packets := splitAllCurrentGameServerUpperPackets(t, connection.write.Bytes())
	if len(packets) != 2 || packets[0].Header.MsgID != 510 || !bytes.Equal(packets[0].Body, []byte{0, 4}) || packets[1].Header.MsgID != 447 || packets[1].Header.Classification != 0 {
		t.Fatalf("invalid reset packets=%+v", packets)
	}
}

func TestNormalizeLegacyDarkKnightComboResetUsesTwoByteSemanticBody(t *testing.T) {
	liveBody := []byte{1, 2, 0xaa, 0xbb, 0xcc, 0xdd}
	got, found := currentGameplayModules.NormalizeLegacy(uint16(dnfenum.CmdPacketComboSkillExtensionQuickSlotReset), liveBody)
	if !found || !bytes.Equal(got, []byte{1, 2}) {
		t.Fatalf("normalized=%x found=%t want=0102", got, found)
	}
}

func TestSendCurrentDarkKnightComboPreloadUsesNativeClass0Op809(t *testing.T) {
	repositories := dnfrepomemory.NewMemoryGroup()
	character := dnfrepo.CharacterRecord{CharacterID: "77", AccountID: "account-1", Job: "9", Slot: 3}
	if err := repositories.Character.Save(context.Background(), character); err != nil {
		t.Fatal(err)
	}
	pages := []darkcombo.Page{{Index: 0, Roots: []darkcombo.Root{{SkillID: 118, Children: []uint16{46}}}}}
	if err := darkcombo.SavePages(context.Background(), repositories.Settings, "77", pages, time.Unix(1, 0)); err != nil {
		t.Fatal(err)
	}
	connection := &bufferConn{}
	service := &Service{
		options:            options{accountID: "account-1", gameUpperBodyCodec: gameUpperBodyCodecPlain},
		repositoryProvider: func() (dnfrepo.Group, bool) { return repositories, true },
	}
	if err := service.sendCurrentDarkKnightComboPreload(
		&gameSession{conn: connection, accountID: "account-1"}, context.Background(), []dnfrepo.CharacterRecord{character}, "test",
	); err != nil {
		t.Fatal(err)
	}
	packet, trailing := splitGameServerUpperPacket(t, connection.write.Bytes())
	if len(trailing) != 0 || packet.Header.Classification != 0 || packet.Header.MsgID != currentDarkKnightComboPreloadMsgID {
		t.Fatalf("header=%+v trailing=%x", packet.Header, trailing)
	}
	want, err := darkcombo.EncodePreloadNotificationBody([]darkcombo.PreloadEntry{{Slot: 3, Pages: pages}})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(packet.Body, want) {
		t.Fatalf("body=%x want=%x", packet.Body, want)
	}
}

func TestGetUserInfoRosterImmediatelyPreloadsDurableDarkKnightCombo(t *testing.T) {
	for _, envelope := range []string{"ordinary_upper", "legacy_hidden_probe_fixed16_zero_prefix"} {
		t.Run(envelope, func(t *testing.T) {
			repositories := dnfrepomemory.NewMemoryGroup()
			character := dnfrepo.CharacterRecord{
				CharacterID: "77",
				AccountID:   "account-1",
				Job:         "9",
				Level:       90,
				Slot:        3,
			}
			if err := repositories.Character.Save(context.Background(), character); err != nil {
				t.Fatal(err)
			}
			pages := []darkcombo.Page{{Index: 0, Roots: []darkcombo.Root{{SkillID: 118, Children: []uint16{46}}}}}
			if err := darkcombo.SavePages(context.Background(), repositories.Settings, "77", pages, time.Unix(1, 0)); err != nil {
				t.Fatal(err)
			}

			connection := &bufferConn{}
			service := &Service{
				options:            options{accountID: "account-1", gameUpperBodyCodec: gameUpperBodyCodecPlain},
				repositoryProvider: func() (dnfrepo.Group, bool) { return repositories, true },
			}
			session := &gameSession{conn: connection, accountID: "account-1"}
			if err := service.sendUpperGetUserInfoRosterBootstrapEnvelope(session, envelope); err != nil {
				t.Fatal(err)
			}

			packets := splitAllCurrentGameServerUpperPackets(t, connection.write.Bytes())
			if len(packets) != 2 {
				t.Fatalf("packet count=%d want roster+op809", len(packets))
			}
			if packets[0].Header.Classification != 0 || packets[0].Header.MsgID != uint16(dnfenum.UpperMsgCharacterRoster) {
				t.Fatalf("roster header=%+v", packets[0].Header)
			}
			want, err := darkcombo.EncodePreloadNotificationBody([]darkcombo.PreloadEntry{{Slot: 3, Pages: pages}})
			if err != nil {
				t.Fatal(err)
			}
			if packets[1].Header.Classification != 0 ||
				packets[1].Header.MsgID != currentDarkKnightComboPreloadMsgID ||
				!bytes.Equal(packets[1].Body, want) {
				t.Fatalf("op809 header=%+v body=%x want=%x", packets[1].Header, packets[1].Body, want)
			}
		})
	}
}

func TestGetUserInfoRosterSkipsDarkKnightPreloadWithoutDurablePages(t *testing.T) {
	repositories := dnfrepomemory.NewMemoryGroup()
	if err := repositories.Character.Save(context.Background(), dnfrepo.CharacterRecord{
		CharacterID: "77",
		AccountID:   "account-1",
		Job:         "9",
		Level:       90,
		Slot:        3,
	}); err != nil {
		t.Fatal(err)
	}
	connection := &bufferConn{}
	service := &Service{
		options:            options{accountID: "account-1", gameUpperBodyCodec: gameUpperBodyCodecPlain},
		repositoryProvider: func() (dnfrepo.Group, bool) { return repositories, true },
	}
	if err := service.sendUpperGetUserInfoRosterBootstrap(&gameSession{conn: connection, accountID: "account-1"}); err != nil {
		t.Fatal(err)
	}
	packets := splitAllCurrentGameServerUpperPackets(t, connection.write.Bytes())
	if len(packets) != 1 || packets[0].Header.MsgID != uint16(dnfenum.UpperMsgCharacterRoster) {
		t.Fatalf("packets=%+v want roster only", packets)
	}
}

func TestCurrentDarkKnightComboSaveKeepsLastChildAndAcknowledges(t *testing.T) {
	repositories := dnfrepomemory.NewMemoryGroup()
	if err := repositories.Character.Save(context.Background(), dnfrepo.CharacterRecord{CharacterID: "77", AccountID: "account-1", Job: "9"}); err != nil {
		t.Fatal(err)
	}
	record := currentDarkKnightComboNativeTestRecord("77")
	record.Skills[119] = dnfrepo.SkillState{Level: 1, Enabled: true}

	if err := repositories.Skill.Save(context.Background(), record); err != nil {
		t.Fatal(err)
	}
	template := []darkcombo.Page{{Index: 0, Roots: []darkcombo.Root{{SkillID: 118}, {SkillID: 119}}}}
	if err := darkcombo.SavePages(context.Background(), repositories.Settings, "77", template, time.Unix(1, 0)); err != nil {
		t.Fatal(err)
	}
	connection := &bufferConn{}
	service := &Service{
		options:            options{accountID: "account-1", gameUpperBodyCodec: gameUpperBodyCodecPlain},
		repositoryProvider: func() (dnfrepo.Group, bool) { return repositories, true },
	}
	page := darkcombo.Page{Index: 0, Roots: []darkcombo.Root{{SkillID: 118, Children: []uint16{46, 8}}, {SkillID: 119, Children: []uint16{8}}}}
	body, err := darkcombo.EncodePageBlock(page)
	if err != nil {
		t.Fatal(err)
	}
	if err := service.handleCurrentDarkKnightComboSkillInfo(&gameSession{conn: connection, accountID: "account-1", selectedCharacterID: 77}, body); err != nil {
		t.Fatal(err)
	}
	pages, found, err := darkcombo.LoadPages(context.Background(), repositories.Settings, "77")
	if err != nil || !found || len(pages) != 1 || len(pages[0].Roots) != 2 {
		t.Fatalf("pages=%+v found=%t err=%v", pages, found, err)
	}
	if got := pages[0].Roots[0].Children; len(got) != 1 || got[0] != 46 {
		t.Fatalf("first root children=%v, want [46]", got)
	}
	if got := pages[0].Roots[1].Children; len(got) != 1 || got[0] != 8 {
		t.Fatalf("second root children=%v, want [8]", got)
	}
	packet, trailing := splitGameServerUpperPacket(t, connection.write.Bytes())
	if len(trailing) != 0 || packet.Header.Classification != 1 || packet.Header.MsgID != 508 || !bytes.Equal(packet.Body, []byte{1}) {
		t.Fatalf("ack header=%+v body=%x trailing=%x", packet.Header, packet.Body, trailing)
	}
}

func TestNormalizeLegacyDarkKnightComboSkillInfoBodyStripsOnlyValidTrailer(t *testing.T) {
	page := darkcombo.Page{Index: 0, Roots: []darkcombo.Root{{SkillID: 118, Children: []uint16{46}}}}
	block, err := darkcombo.EncodePageBlock(page)
	if err != nil {
		t.Fatal(err)
	}
	withTrailer := append(append([]byte(nil), block...), 0)
	if got := normalizeLegacyDarkKnightComboSkillInfoBody(withTrailer); !bytes.Equal(got, block) {
		t.Fatalf("normalized=%x want=%x", got, block)
	}
	invalid := []byte{0, 1, 0}
	if got := normalizeLegacyDarkKnightComboSkillInfoBody(invalid); !bytes.Equal(got, invalid) {
		t.Fatalf("invalid input changed: %x", got)
	}
}

func currentDarkKnightComboNativeTestRecord(characterID string) dnfrepo.SkillRecord {
	states := map[int64]dnfrepo.SkillState{
		8:   {Level: 1, Enabled: true},
		46:  {Level: 1, Enabled: true},
		118: {Level: 1, Enabled: true},
		119: {Level: 1, Enabled: true},
	}
	points := dnfrepo.SkillPointState{}
	return dnfrepo.SkillRecord{
		CharacterID: characterID,
		Skills:      states,
		Points:      points,
		Trees:       dnfrepo.SkillTreeState{Expanded: true, Other: dnfrepo.SkillProfile{Skills: states, Points: points}},
		Layouts:     map[int]dnfrepo.SkillLayout{0: {0: 118}, 1: {0: 118}},
	}
}

func currentDarkKnightComboProjectionTestHarness(t *testing.T) (
	dnfrepo.Group,
	*Service,
	*gameSession,
	*bufferConn,
	func(time.Time),
) {
	t.Helper()
	repositories := dnfrepomemory.NewMemoryGroup()
	character := dnfrepo.CharacterRecord{CharacterID: "77", AccountID: "account-1", Job: "9", Level: 1}
	if err := repositories.Character.Save(context.Background(), character); err != nil {
		t.Fatal(err)
	}
	if err := repositories.Skill.Save(context.Background(), currentDarkKnightComboNativeTestRecord("77")); err != nil {
		t.Fatal(err)
	}
	pages := []darkcombo.Page{{Index: 0, Roots: []darkcombo.Root{{SkillID: 118, Children: []uint16{46}}}}}
	if err := darkcombo.SavePages(context.Background(), repositories.Settings, "77", pages, time.Unix(1, 0)); err != nil {
		t.Fatal(err)
	}
	now := time.Unix(100, 0).UTC()
	connection := &bufferConn{}
	service := &Service{
		options:            options{accountID: "account-1", gameUpperBodyCodec: gameUpperBodyCodecPlain},
		repositoryProvider: func() (dnfrepo.Group, bool) { return repositories, true },
		skillCatalog:       currentDarkKnightComboNativeTestCatalog(t),
		gameplayTimers:     &processGameplayTimeQueue{now: func() time.Time { return now }},
		initialSPTable:     map[int]int{1: 0},
		initialTPTable:     map[int]int{1: 0},
	}
	session := &gameSession{conn: connection, accountID: "account-1", selectedCharacterID: 77}
	if err := service.sendCurrentDarkKnightComboInitialization(session, context.Background(), character, "test_projection"); err != nil {
		t.Fatal(err)
	}
	return repositories, service, session, connection, func(updated time.Time) { now = updated.UTC() }
}

func currentDarkKnightComboNativeTestCatalog(t *testing.T) *dnfskill.Table {
	t.Helper()
	catalog, err := buildSkillCatalogFromSource(context.Background(), initialEquipmentMemSource{
		"skill/skilllist.lst": "9 `demonicswordman/skilllist.lst`\n",
		"skill/demonicswordman/skilllist.lst": "8 `eight.skl`\n" +
			"46 `forty_six.skl`\n" +
			"118 `comboset0.skl`\n" +
			"119 `comboset1.skl`\n",
		"skill/demonicswordman/eight.skl":     "[skill type]\n`active`\n",
		"skill/demonicswordman/forty_six.skl": "[skill type]\n`active`\n",
		"skill/demonicswordman/comboset0.skl": "[combo set]\n46 8\n[/combo set]\n",
		"skill/demonicswordman/comboset1.skl": "[combo set]\n8\n[/combo set]\n",
	})
	if err != nil {
		t.Fatal(err)
	}
	return catalog
}
