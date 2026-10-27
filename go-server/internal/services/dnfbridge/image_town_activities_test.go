package dnfbridge

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"longheng.io/server/internal/modules/dnf/adminapi"
	"longheng.io/server/internal/modules/dnf/onlineevent"
	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
	"longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/repository/memory"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

func imageActivityBody(t *testing.T, ids []uint16) []byte {
	t.Helper()
	body, err := buildCurrentActivityEventInfoBodyWithImageTownActivities(currentSpendTimeJoustCatalogGoldenRewards, 10800, false, false, true, true, true, 0, false, false, false, false, false, ids)
	if err != nil {
		t.Fatal(err)
	}
	return body
}

func TestImageTownActivityCatalogEachSwitchAndPreservedRows(t *testing.T) {
	before := parseCurrentSpendTimeEventInfoBody(t, imageActivityBody(t, nil))
	for mask := 0; mask < 32; mask++ {
		var ids []uint16
		for i, id := range []uint16{157, 2099, 2222, 2469, 2475} {
			if mask&(1<<i) != 0 {
				ids = append(ids, id)
			}
		}
		shape := parseCurrentSpendTimeEventInfoBody(t, imageActivityBody(t, ids))
		if int(shape.baseCount) != len(before.baseRows)+len(ids) || int(shape.activeCount) != len(before.activeRows)+len(ids) {
			t.Fatal("catalog counts differ from rows")
		}
		for i, row := range before.baseRows {
			if !bytes.Equal(row.raw, shape.baseRows[i].raw) {
				t.Fatalf("mask%d existing base changed", mask)
			}
		}
		for i, row := range before.activeRows {
			if !bytes.Equal(row.raw, shape.activeRows[i].raw) {
				t.Fatalf("mask%d existing active changed", mask)
			}
		}
		for i, id := range []uint16{157, 2099, 2222, 2469, 2475} {
			if shape.hasEvent(id) != (mask&(1<<i) != 0) {
				t.Fatalf("mask%d event%d", mask, id)
			}
		}
		for _, row := range shape.baseRows[len(before.baseRows):] {
			if row.hasExtended || row.value != 1 {
				t.Fatalf("invented action for NPC flag %+v", row)
			}
		}
		for _, row := range shape.activeRows[len(before.activeRows):] {
			if row.params != [12]uint32{} {
				t.Fatalf("invented NPC params %+v", row)
			}
		}
	}
	legacy, err := buildCurrentActivityEventInfoBodyWithEpicRoad(currentSpendTimeJoustCatalogGoldenRewards, 10800, false, false, true, true, true, 0, false, false, false, false, false)
	if err != nil || !bytes.Equal(legacy, imageActivityBody(t, nil)) {
		t.Fatal("disabled flags changed prior catalog", err)
	}
	for _, ids := range [][]uint16{{0}, {2099, 2099}, {9999}, {350}} {
		if validateCurrentImageTownActivityIDs(ids) == nil {
			t.Fatalf("unknown/duplicate accepted: %v", ids)
		}
	}
}

// Literal rows are independent of the writer and parser's field construction.
// The fixed IDs are little endian; base is 15 bytes, active is 50 bytes.
func TestImageTownActivityLiteralWireRows(t *testing.T) {
	for _, item := range []struct {
		id           uint16
		base, active string
	}{
		{157, "9d0001000000000000000000000000", "9d00" + strings.Repeat("00", 48)},
		{2099, "330801000000000000000000000000", "3308" + strings.Repeat("00", 48)},
		{2222, "ae0801000000000000000000000000", "ae08" + strings.Repeat("00", 48)},
		{2469, "a50901000000000000000000000000", "a509" + strings.Repeat("00", 48)},
		{2475, "ab0901000000000000000000000000", "ab09" + strings.Repeat("00", 48)},
	} {
		shape := parseCurrentSpendTimeEventInfoBody(t, imageActivityBody(t, []uint16{item.id}))
		base := shape.baseRows[len(shape.baseRows)-1].raw
		active := shape.activeRows[len(shape.activeRows)-1].raw
		if hex.EncodeToString(base) != item.base || hex.EncodeToString(active) != item.active {
			t.Fatalf("event%d literal wire mismatch: base=%x active=%x", item.id, base, active)
		}
	}
}

func TestImageTownActivityControlsRequireRestartAndRetainPriorPolicy(t *testing.T) {
	ctx := context.Background()
	g := memory.NewMemoryGroup()
	old, err := onlineevent.NewControls(ctx, g.Settings, []onlineevent.ControlDefinition{{ID: 2347, Name: "online", DefaultEnabled: true}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = old.Save(ctx, 0, []onlineevent.ControlChange{{ID: 2347, Enabled: false}}); err != nil {
		t.Fatal(err)
	}
	s := &Service{options: options{currentClientVersion: "official-24.9"}, repositoryProvider: func() (repository.Group, bool) { return g, true }}
	if err = s.initializeActivityControls(ctx); err != nil {
		t.Fatal(err)
	}
	for _, id := range []uint16{157, 2099, 2222, 2469, 2475} {
		if s.activityControls.Enabled(id) {
			t.Fatalf("new activity enabled without policy %d", id)
		}
	}
	if s.activityControls.Enabled(2347) {
		t.Fatal("old configured false overwritten")
	}
	var changes []onlineevent.ControlChange
	for _, id := range []uint16{157, 2099, 2222, 2469, 2475} {
		changes = append(changes, onlineevent.ControlChange{ID: id, Enabled: true})
	}
	snapshot, err := s.activityControls.Save(ctx, 1, changes)
	if err != nil || !snapshot.RestartRequired {
		t.Fatal(snapshot, err)
	}
	for _, id := range []uint16{157, 2099, 2222, 2469, 2475} {
		if s.activityControls.Enabled(id) {
			t.Fatal("save activated process policy")
		}
	}
	if err = s.initializeActivityControls(ctx); err != nil {
		t.Fatal(err)
	}
	for _, id := range []uint16{157, 2099, 2222, 2469, 2475} {
		if !s.activityControls.Enabled(id) {
			t.Fatal("restart lost policy")
		}
	}
	if s.activityControls.Enabled(2347) {
		t.Fatal("new switch overwrote existing activity")
	}
}

func TestImageTownActivityPrivateAPIListsAndSavesActualSwitches(t *testing.T) {
	s := adminTestService(t)
	g := memory.NewMemoryGroup()
	s.repositoryProvider = func() (repository.Group, bool) { return g, true }
	if err := s.initializeActivityControls(context.Background()); err != nil {
		t.Fatal(err)
	}
	w := adminTestCall(s, `{"version":1,"action":"activities"}`, "127.0.0.1:1234", strings.Repeat("x", 32))
	var reply adminapi.Response
	if err := json.Unmarshal(w.Body.Bytes(), &reply); err != nil || !reply.OK || reply.Activities == nil {
		t.Fatal(w.Body.String(), err)
	}
	for _, id := range []uint16{157, 2099, 2222, 2469, 2475} {
		found := false
		for _, row := range reply.Activities.Activities {
			if row.ID == id {
				found = true
				if !row.AvailabilityOnly || row.Group != "图片活动NPC" || row.Enabled {
					t.Fatalf("wrong UI metadata %+v", row)
				}
			}
		}
		if !found {
			t.Fatalf("GM API missing %d", id)
		}
	}
	w = adminTestCall(s, `{"version":1,"action":"save_activities","revision":0,"changes":[{"id":157,"enabled":true},{"id":2099,"enabled":true},{"id":2222,"enabled":true},{"id":2469,"enabled":true},{"id":2475,"enabled":true}]}`, "127.0.0.1:1234", strings.Repeat("x", 32))
	if err := json.Unmarshal(w.Body.Bytes(), &reply); err != nil || !reply.OK || reply.Activities == nil || !reply.Activities.RestartRequired {
		t.Fatal(w.Body.String(), err)
	}
	for _, id := range []uint16{157, 2099, 2222, 2469, 2475} {
		if s.activityControls.Enabled(id) {
			t.Fatal("API save applied before restart")
		}
	}
	if err := s.initializeActivityControls(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, id := range []uint16{157, 2099, 2222, 2469, 2475} {
		if !s.activityControls.Enabled(id) {
			t.Fatal("API save not persistent")
		}
	}
}

// imageTownActivityFakeSource injects individual missing or mismatched NPC gates.
type imageTownActivityFakeSource struct {
	documents map[string]string
}

func (s imageTownActivityFakeSource) ReadText(relativePath string) (string, error) {
	text, ok := s.documents[relativePath]
	if !ok {
		return "", fmt.Errorf("fake source has no %s", relativePath)
	}
	return text, nil
}

// imageTownActivityGateDocument builds a synthetic gate document for fault tests.
func imageTownActivityGateDocument(id uint16) string {
	return fmt.Sprintf("[open visible event]\r\n%d\r\n", id)
}

func imageTownActivityIDs(t *testing.T) []uint16 {
	t.Helper()
	ids := make([]uint16, 0, len(currentImageTownActivities()))
	for _, activity := range currentImageTownActivities() {
		ids = append(ids, activity.id)
	}
	return ids
}

// TestImageTownActivitySelectionKeepsHealthyEntriesWhenOneResourceIsMissing is
// the missing-one-NPC counterexample: exactly one optional entry has no
// document, every other entry keeps its valid id in request order, and only the
// dropped id gets a diagnostic.
func TestImageTownActivitySelectionKeepsHealthyEntriesWhenOneResourceIsMissing(t *testing.T) {
	ids := imageTownActivityIDs(t)
	if len(ids) < 3 {
		t.Fatalf("optional image activities=%d", len(ids))
	}
	documents := map[string]string{}
	for _, activity := range currentImageTownActivities() {
		documents[activity.path] = imageTownActivityGateDocument(activity.id)
	}
	broken, ok := currentImageTownActivity(ids[1])
	if !ok {
		t.Fatal("missing broken activity")
	}
	delete(documents, broken.path)
	valid, diagnostics := selectValidImageTownActivities(imageTownActivityFakeSource{documents: documents}, ids)
	if len(valid) != len(ids)-1 {
		t.Fatalf("valid=%v want %d ids", valid, len(ids)-1)
	}
	seen := make(map[uint16]bool, len(valid))
	for _, id := range valid {
		seen[id] = true
	}
	for _, id := range ids {
		if id == broken.id {
			continue
		}
		if !seen[id] {
			t.Fatalf("healthy id %d was dropped although only %d failed", id, broken.id)
		}
	}
	if seen[broken.id] {
		t.Fatalf("broken id %d was reported valid", broken.id)
	}
	if len(diagnostics) != 1 || diagnostics[0].id != broken.id || diagnostics[0].reason == "" {
		t.Fatalf("diagnostics=%+v want only id=%d with a reason", diagnostics, broken.id)
	}
}

// TestImageTownActivitySelectionDropsOnlyTheFailingIds pins that each failure
// stands alone: a gate mismatch on the first id and an unreadable document for
// the last id drop exactly those two, while the middle ids stay valid, and the
// remaining set is still an accepted catalog selection.
func TestImageTownActivitySelectionDropsOnlyTheFailingIds(t *testing.T) {
	ids := imageTownActivityIDs(t)
	if len(ids) < 3 {
		t.Fatalf("optional image activities=%d", len(ids))
	}
	documents := map[string]string{}
	for _, activity := range currentImageTownActivities() {
		documents[activity.path] = imageTownActivityGateDocument(activity.id)
	}
	first, _ := currentImageTownActivity(ids[0])
	last, _ := currentImageTownActivity(ids[len(ids)-1])
	documents[first.path] = imageTownActivityGateDocument(first.id + 1)
	delete(documents, last.path)
	valid, diagnostics := selectValidImageTownActivities(imageTownActivityFakeSource{documents: documents}, ids)
	if len(valid) != len(ids)-2 {
		t.Fatalf("valid=%v want %d ids", valid, len(ids)-2)
	}
	if len(diagnostics) != 2 {
		t.Fatalf("diagnostics=%+v want exactly the two failing ids", diagnostics)
	}
	reasons := map[uint16]string{}
	for _, diagnostic := range diagnostics {
		reasons[diagnostic.id] = diagnostic.reason
	}
	if reason, ok := reasons[first.id]; !ok || !strings.Contains(reason, "NPC gate mismatch") {
		t.Fatalf("mismatched id %d reason=%q", first.id, reason)
	}
	if reason, ok := reasons[last.id]; !ok || reason == "" {
		t.Fatalf("unreadable id %d reason=%q", last.id, reason)
	}
	if err := validateCurrentImageTownActivityIDs(valid); err != nil {
		t.Fatalf("remaining valid ids are not an accepted selection: %v", err)
	}
}

// TestImageTownActivitySelectionNilSourceDropsEveryRequestedID covers the
// whole-archive-unavailable case at the helper level: nothing is valid, each
// requested id is reported once, and no id is invented.
func TestImageTownActivitySelectionNilSourceDropsEveryRequestedID(t *testing.T) {
	ids := imageTownActivityIDs(t)
	valid, diagnostics := selectValidImageTownActivities(nil, ids)
	if len(valid) != 0 {
		t.Fatalf("nil source valid=%v", valid)
	}
	if len(diagnostics) != len(ids) {
		t.Fatalf("diagnostics=%d want %d", len(diagnostics), len(ids))
	}
	for index, diagnostic := range diagnostics {
		if diagnostic.id != ids[index] || diagnostic.reason != currentImageTownActivitySourceUnavailable {
			t.Fatalf("diagnostics[%d]=%+v", index, diagnostic)
		}
	}
}

// imageTownActivityArchiveFixture prepares the real send path with a packet log
// and a resource archive that is deliberately unavailable.
func imageTownActivityArchiveFixture(t *testing.T) (*Service, *gameSession, *bufferConn) {
	t.Helper()
	s, a, _, conn := prepareMonthlyBridge(t)
	s.options.joustEventDisabled = true
	s.initialEquipmentArchive = nil
	s.initialEquipmentLoadErr = fmt.Errorf("test resource archive unavailable")
	logPath := filepath.Join(t.TempDir(), "image-town-activities.log")
	logger, err := openPacketLogger(logPath)
	if err != nil {
		t.Fatal(err)
	}
	s.packetLog = logger
	t.Cleanup(func() { _ = logger.Close() })
	return s, a, conn
}

func imageTownActivityEnableAll(t *testing.T, s *Service) {
	t.Helper()
	changes := make([]onlineevent.ControlChange, 0, len(currentImageTownActivities()))
	for _, activity := range currentImageTownActivities() {
		changes = append(changes, onlineevent.ControlChange{ID: activity.id, Enabled: true})
	}
	if _, err := s.activityControls.Save(context.Background(), s.activityControls.Snapshot().Revision, changes); err != nil {
		t.Fatal(err)
	}
	if err := s.initializeActivityControls(context.Background()); err != nil {
		t.Fatal(err)
	}
}

// TestImageTownActivityDefaultAllOffDoesNotReadTheResourceArchive proves the
// all-off default: the caller reads the operator policy first, finds no enabled
// optional entry and returns before any archive lookup. The sentinel load error
// is armed so any archive read would surface; the catalog must still be emitted.
func TestImageTownActivityDefaultAllOffDoesNotReadTheResourceArchive(t *testing.T) {
	s, a, _, conn := prepareMonthlyBridge(t)
	s.options.joustEventDisabled = true
	s.initialEquipmentArchive = nil
	s.options.pvfPath = filepath.Join(t.TempDir(), "missing-resource.pvf")
	if err := s.initializeActivityControls(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, activity := range currentImageTownActivities() {
		if s.activityControls.Enabled(activity.id) {
			t.Fatalf("activity %d enabled by default", activity.id)
		}
	}
	armed := fmt.Errorf("all-off send must not consult the resource archive")
	s.initialEquipmentLoadErr = armed
	catalog := &currentSpendTimeRuntimeCatalog{
		rewardItemIDs:     currentSpendTimeJoustCatalogGoldenRewards,
		totalStageSeconds: 10800,
	}
	sent, err := s.sendCurrentSpendTimeProtocolState(a, catalog, 0, 0, true)
	if err != nil || !sent {
		t.Fatalf("sent=%t err=%v", sent, err)
	}
	packet, _ := splitGameServerUpperPacketWithHeader(t, conn.write.Bytes(), 16)
	if packet.Header.MsgID != currentSpendTimeEventInfoMsgID {
		t.Fatalf("first packet msg_id=%d want %d", packet.Header.MsgID, currentSpendTimeEventInfoMsgID)
	}
	if s.initialEquipmentLoadErr != armed {
		t.Fatalf("all-off send read the resource archive: %v", s.initialEquipmentLoadErr)
	}
}

// TestSendImageTownActivitiesAllOptionalResourcesFailKeepsLegacyCatalogAndOp1206
// is the end-to-end counterexample for the reported field defect. With the
// resource archive unusable, the real send path must still publish op108
// (legacy rows byte-intact) directly followed by op1206, omit every optional
// entry, and record one structured diagnostic per dropped entry.
func TestSendImageTownActivitiesAllOptionalResourcesFailKeepsLegacyCatalogAndOp1206(t *testing.T) {
	s, a, conn := imageTownActivityArchiveFixture(t)
	catalog := &currentSpendTimeRuntimeCatalog{
		rewardItemIDs:     currentSpendTimeJoustCatalogGoldenRewards,
		totalStageSeconds: 10800,
	}
	if sent, err := s.sendCurrentSpendTimeProtocolState(a, catalog, 0, 0, true); err != nil || !sent {
		t.Fatalf("baseline sent=%t err=%v", sent, err)
	}
	baselinePacket, _ := splitGameServerUpperPacketWithHeader(t, conn.write.Bytes(), 16)
	baseline, err := zlibDecompress(baselinePacket.Body)
	if err != nil {
		t.Fatal(err)
	}
	conn.write.Reset()
	imageTownActivityEnableAll(t, s)
	sent, err := s.sendCurrentSpendTimeProtocolState(a, catalog, 0, 0, true)
	if err != nil || !sent {
		t.Fatalf("sent=%t err=%v", sent, err)
	}
	packet, rest := splitGameServerUpperPacketWithHeader(t, conn.write.Bytes(), 16)
	if packet.Header.MsgID != currentSpendTimeEventInfoMsgID {
		t.Fatalf("first packet msg_id=%d want %d", packet.Header.MsgID, currentSpendTimeEventInfoMsgID)
	}
	body, err := zlibDecompress(packet.Body)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(body, baseline) {
		t.Fatal("failed optional resources changed the legacy op108 body")
	}
	shape := parseCurrentSpendTimeEventInfoBody(t, body)
	if !shape.hasEvent(uint16(currentSpendTimeEventID)) {
		t.Fatal("op108 lost the 2347 spend-time/签到 row")
	}
	for _, activity := range currentImageTownActivities() {
		if shape.hasEvent(activity.id) {
			t.Fatalf("op108 advertised optional event %d without a validated resource", activity.id)
		}
	}
	progress, trailing := splitGameServerUpperPacket(t, rest)
	if progress.Header.MsgID != currentSpendTimeProgressMsgID {
		t.Fatalf("second packet msg_id=%d want %d (108→1206 order)", progress.Header.MsgID, currentSpendTimeProgressMsgID)
	}
	if len(trailing) != 0 {
		t.Fatalf("unexpected %d trailing bytes after op108→op1206", len(trailing))
	}
	log := testPacketLogText(t, s)
	for _, activity := range currentImageTownActivities() {
		if !strings.Contains(log, fmt.Sprintf("activity_id=%d", activity.id)) {
			t.Fatalf("missing diagnostic for dropped optional event %d:\n%s", activity.id, log)
		}
	}
	if !strings.Contains(log, "policy=skip_unreadable_optional_entry_and_keep_legacy_catalog") {
		t.Fatalf("diagnostic is not structured:\n%s", log)
	}
}

func TestRealPVFImageTownActivitiesBootstrap(t *testing.T) {
	path := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if path == "" {
		t.Skip("set DNFBRIDGE_REAL_PVF_SMOKE for image activity NPC gate proof")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(data)
	if hex.EncodeToString(digest[:]) != "ad7d05c70421278f7385729bcba3e81e37d99d0f3df85a87a9f0dc19c92504cb" {
		t.Fatal("unreviewed PVF")
	}
	archive, err := platformpvf.OpenBytes(data)
	if err != nil {
		t.Fatal(err)
	}
	mapText, err := archive.ReadText("map/common/(r)gate_seria.map")
	if err != nil {
		t.Fatal(err)
	}
	mapDocument, err := dnfpvf.Parse("map/common/(r)gate_seria.map", mapText)
	if err != nil {
		t.Fatal(err)
	}
	npcs, ok := mapDocument.Section("NPC")
	if !ok || len(npcs)%5 != 0 {
		t.Fatal("unreviewed town NPC placement grammar")
	}
	for _, id := range []int64{20475, 20387, 400000046, 400000028, 3135, 20490} {
		found := false
		for i := 0; i < len(npcs); i += 5 {
			if npcs[i].Kind == dnfpvf.TokenInt && npcs[i].Int == id {
				found = true
			}
		}
		if !found {
			t.Fatalf("image town missing NPC %d", id)
		}
	}
	for _, activity := range currentImageTownActivities() {
		if err = validateCurrentImageTownActivity(archive, activity); err != nil {
			t.Fatal(err)
		}
		text, readErr := archive.ReadText(activity.path)
		if readErr != nil {
			t.Fatal(readErr)
		}
		document, parseErr := dnfpvf.Parse(activity.path, text)
		if parseErr != nil {
			t.Fatal(parseErr)
		}
		t.Logf("NPC gate=%d path=%s field=%v", activity.id, activity.path, document.Ints("open visible event"))
		if err = validateCurrentImageTownActivity(epicRoadOverrideSource{archive, activity.path, "[open visible event] 9999"}, activity); err == nil {
			t.Fatal("foreign NPC gate accepted")
		}
		if err = validateCurrentImageTownActivity(epicRoadOverrideSource{archive, activity.path, ""}, activity); err == nil {
			t.Fatal("missing NPC accepted")
		}
	}
	s, a, _, conn := prepareMonthlyBridge(t)
	s.initialEquipmentArchive = archive
	s.options.joustEventDisabled = true
	changes := []onlineevent.ControlChange{{ID: 157, Enabled: true}, {ID: 2099, Enabled: true}, {ID: 2222, Enabled: true}, {ID: 2469, Enabled: true}, {ID: 2475, Enabled: true}}
	if _, err = s.activityControls.Save(context.Background(), s.activityControls.Snapshot().Revision, changes); err != nil {
		t.Fatal(err)
	}
	if err = s.initializeActivityControls(context.Background()); err != nil {
		t.Fatal(err)
	}
	catalog := &currentSpendTimeRuntimeCatalog{rewardItemIDs: currentSpendTimeJoustCatalogGoldenRewards, totalStageSeconds: 10800}
	sent, err := s.sendCurrentSpendTimeProtocolState(a, catalog, 0, 0, true)
	if err != nil || !sent {
		t.Fatal(sent, err)
	}
	packet, rest := splitGameServerUpperPacketWithHeader(t, conn.write.Bytes(), 16)
	if packet.Header.MsgID != 108 {
		t.Fatal("catalog missing")
	}
	body, err := zlibDecompress(packet.Body)
	if err != nil {
		t.Fatal(err)
	}
	// This case exceeds the historical test parser's old ten-row bound.
	shape := parseCurrentSpendTimeEventInfoBody(t, body)
	for _, id := range []uint16{157, 2099, 2222, 2469, 2475, 350, 2347, 181} {
		if !shape.hasEvent(id) {
			t.Fatalf("actual send omitted%d", id)
		}
	}
	progress, _ := splitGameServerUpperPacket(t, rest)
	if progress.Header.MsgID != 1206 {
		t.Fatal("catalog/progress order changed")
	}
}
