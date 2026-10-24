package dnfbridge

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"reflect"
	"testing"

	"longheng.io/server/internal/modules/dnf/guild"
)

// This is an existing-state fixture, not the result of a summon. It deliberately
// includes different levels, skills, progress and a non-first active guardian.
func TestGuildGuardianExistingStateSurvivesIncomeAndReconnect(t *testing.T) {
	s, p, repos := guildMileageFixture(t)
	guildEssenceBridgeCatalog(t, s)
	ctx := context.Background()
	record, _, err := repos.Settings.Load(ctx, guild.RegistryScope)
	if err != nil {
		t.Fatal(err)
	}
	var document map[string]json.RawMessage
	if err = json.Unmarshal([]byte(record.Values["state"]), &document); err != nil {
		t.Fatal(err)
	}
	var guilds map[string]map[string]json.RawMessage
	if err = json.Unmarshal(document["guilds"], &guilds); err != nil {
		t.Fatal(err)
	}
	state := json.RawMessage(`{"ever_acquired":true,"active_id":2,"owned":[{"id":1,"level":10,"skills":[1,1,1,1,1],"ap_progress":18,"gp_progress":0},{"id":2,"level":21,"skills":[5,4,3,2,1],"ap_progress":83,"gp_progress":604}]}`)
	guilds["1"]["guardians"] = state
	document["guilds"], err = json.Marshal(guilds)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	record.Values["state"] = string(raw)
	if err = repos.Settings.Save(ctx, record); err != nil {
		t.Fatal(err)
	}
	conn := p.conn.(*bufferConn)
	conn.write.Reset()
	if err = s.grantCurrentGuildEssenceLogin(p); err != nil {
		t.Fatal(err)
	}
	body := append([]byte(nil), guildReply(t, p, 1297)...)
	if got := binary.LittleEndian.Uint32(body[0x44:]); got != 2 {
		t.Fatalf("income cleared existing active guardian: got %d want 2", got)
	}
	wantRows := []byte{
		1, 0, 0, 0, 10, 1, 1, 1, 1, 1, 18, 0, 0, 0, 0, 0, 0, 0,
		2, 0, 0, 0, 21, 5, 4, 3, 2, 1, 83, 0, 0, 0, 92, 2, 0, 0,
	}
	if !bytes.Equal(body[0x7f8:0x81c], wantRows) || !bytes.Equal(body[0x81c:0x852], make([]byte, 54)) {
		t.Fatal("owned rows or unused slots changed")
	}
	if binary.LittleEndian.Uint32(body[0x17:]) != 10 {
		t.Fatal("login income lost")
	}
	gotRecord, _, err := repos.Settings.Load(ctx, guild.RegistryScope)
	if err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal([]byte(gotRecord.Values["state"]), &document); err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(document["guilds"], &guilds); err != nil {
		t.Fatal(err)
	}
	var gotState, wantState any
	if err = json.Unmarshal(guilds["1"]["guardians"], &gotState); err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(state, &wantState); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(gotState, wantState) {
		t.Fatal("income save changed guardian state")
	}
	exportEssenceBody(t, "owned-income-1297", body)
	restarted := &Service{repositoryProvider: s.repositoryProvider, options: s.options, gameplayTimers: s.gameplayTimers}
	conn.write.Reset()
	if err = restarted.writeCurrentGuildEssenceSnapshot(p); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(guildReply(t, p, 1297), body) {
		t.Fatal("reconnect changed snapshot")
	}
	exportEssenceBody(t, "owned-reconnect-1297", guildReply(t, p, 1297))
	conn.write.Reset()
	if err = s.grantCurrentGuildEssenceLogin(p); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(guildReply(t, p, 1297), body) {
		t.Fatal("repeated income changed state")
	}
	assertNoEssenceNotice(t, conn.write.Bytes())
	// The same numeric character under another account receives no old state.
	p.accountID = "other-owner"
	conn.write.Reset()
	if err = restarted.writeCurrentGuildEssenceSnapshot(p); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(guildReply(t, p, 1297), buildCurrentGuildEssenceSnapshot(guild.EssenceBalance{})) {
		t.Fatal("foreign account received previous guardian state")
	}
	exportEssenceBody(t, "owned-foreign-account-1297", guildReply(t, p, 1297))
}
