package dnfbridge

import (
	"bytes"
	"context"
	"reflect"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/adventuregroup"
	"longheng.io/server/internal/modules/dnf/dnfenum"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	dnfrepomemory "longheng.io/server/internal/modules/dnf/repository/memory"
)

func TestOfficialAdventureExpeditionInfoBeforeCharacterSelectionUsesBoundAccountReadOnly(t *testing.T) {
	repos := dnfrepomemory.NewMemoryGroup()
	tables := loadAdventureGroupTestTables(t)
	service := &Service{options: options{currentClientVersion: "official-24.9", accountID: "fallback"}, adventureGroupTable: tables,
		repositoryProvider: func() (dnfrepo.Group, bool) { return repos, true }}
	now := service.gameplayNow()
	accounts := []dnfrepo.AccountRecord{}
	for index, owner := range []string{"player-a", "player-b", "fallback"} {
		state := adventuregroup.RuntimeState{Version: 1, Expeditions: map[string]adventuregroup.Expedition{"1": {
			Area: 1, State: 1, StartedAt: now.Add(-time.Minute).Unix(), EndsAt: now.Add(time.Hour).Unix(),
			Members: []adventuregroup.ExpeditionMember{{CharacterID: uint16(20 + index), Name: owner, Level: 90}},
		}}}
		value, err := state.Marshal()
		if err != nil {
			t.Fatal(err)
		}
		account := dnfrepo.AccountRecord{AccountID: owner, Metadata: map[string]string{adventuregroup.RuntimeStateMetadataKey: value}}
		if err := repos.Account.Save(context.Background(), account); err != nil {
			t.Fatal(err)
		}
		accounts = append(accounts, account)
	}
	for _, account := range accounts[:2] {
		session := &gameSession{accountID: account.AccountID, accountFromToken: true, loginPacketSeen: true, conn: &bufferConn{}}
		if err := service.handleCurrentAdventureExpeditionInfo(session, []byte{0}); err != nil {
			t.Fatal(err)
		}
		frames := writeFrames(t, session)
		state, err := adventuregroup.ParseRuntimeState(account, tables.Runtime(), service.gameplayNow())
		if err != nil {
			t.Fatal(err)
		}
		// No owned character exists in this legacy fixture. Its saved base
		// level90 cannot serve as a real HonorExpert level in the wire view.
		active := state.Expeditions["1"]
		active.Members[0].Level = 0
		state.Expeditions["1"] = active
		want := append([]byte{1}, buildCurrentAdventureExpeditionStateBody(state, service.gameplayNow())...)
		if len(frames) != 1 || frames[0].msgID != uint16(dnfenum.CmdPacketMercenaryInfo) || !bytes.Equal(frames[0].body, want) {
			t.Fatalf("preselection account %s received %+v want %x", account.AccountID, frames, want)
		}
		if session.selectedCharacterID != 0 {
			t.Fatal("read selected a character")
		}
	}
	for _, account := range accounts {
		after, found, err := repos.Account.Load(context.Background(), account.AccountID)
		if err != nil || !found || !reflect.DeepEqual(account, after) {
			t.Fatalf("read modified %s: %+v %v", account.AccountID, after, err)
		}
	}
}

func TestAdventurePreselectionInfoRejectsUnboundMissingAndLegacyAccounts(t *testing.T) {
	repos := dnfrepomemory.NewMemoryGroup()
	if err := repos.Account.Save(context.Background(), dnfrepo.AccountRecord{AccountID: "fallback"}); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name, profile, account string
		token, login           bool
	}{
		{"no-token", "official-24.9", "fallback", false, true},
		{"no-login", "official-24.9", "fallback", true, false},
		{"no-account", "official-24.9", "", true, true},
		{"missing-account", "official-24.9", "missing", true, true},
		{"legacy", "90cn", "fallback", true, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			service := &Service{options: options{currentClientVersion: test.profile, accountID: "fallback"}, adventureGroupTable: loadAdventureGroupTestTables(t), repositoryProvider: func() (dnfrepo.Group, bool) { return repos, true }}
			session := &gameSession{accountID: test.account, accountFromToken: test.token, loginPacketSeen: test.login, conn: &bufferConn{}}
			if err := service.handleCurrentAdventureExpeditionInfo(session, []byte{0}); err != nil {
				t.Fatal(err)
			}
			frames := writeFrames(t, session)
			if len(frames) != 1 || !bytes.Equal(frames[0].body, []byte{0, currentAdventureGenericFailureCode}) {
				t.Fatalf("unexpected preselection success: %+v", frames)
			}
		})
	}
}
