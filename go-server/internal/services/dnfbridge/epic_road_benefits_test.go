package dnfbridge

import (
	"context"
	"encoding/binary"
	"errors"
	onlineevent "longheng.io/server/internal/modules/dnf/onlineevent"
	repo "longheng.io/server/internal/modules/dnf/repository"
	"testing"
)

func seedEpicBridge(t *testing.T) (*Service, *gameSession, *bufferConn, onlineevent.EpicCampaign) {
	t.Helper()
	s, a, _, c := prepareMonthlyBridge(t)
	g, _ := s.repositoryGroup()
	ch, _, _ := g.Character.Load(nil, "1")
	ch.Level = 90
	if e := g.Character.Save(nil, ch); e != nil {
		t.Fatal(e)
	}
	campaign, e := s.currentEpicCampaign(a)
	if e != nil {
		t.Fatal(e)
	}
	if e = onlineevent.AccumulateEpicInvitations(&ch, campaign, s.gameplayNow(), 18000); e != nil {
		t.Fatal(e)
	}
	if e = g.Character.Save(nil, ch); e != nil {
		t.Fatal(e)
	}
	return s, a, c, campaign
}
func TestEpicBenefitsActualBridgeClaimStateReplay(t *testing.T) {
	s, a, c, campaign := seedEpicBridge(t)
	g, _ := s.repositoryGroup()
	for i := uint32(0); i < 3; i++ {
		body := make([]byte, 8)
		binary.LittleEndian.PutUint32(body, 350)
		binary.LittleEndian.PutUint32(body[4:], i)
		c.write.Reset()
		handled, e := s.handleCurrentEpicClaim(a, 688, 1, body)
		if !handled || e != nil {
			t.Fatal(handled, e)
		}
		ack, rest := splitGameServerUpperPacket(t, c.write.Bytes())
		state, _ := splitGameServerUpperPacket(t, rest)
		if ack.Header.MsgID != 688 || len(ack.Body) != 17 || ack.Body[0] != 1 || binary.LittleEndian.Uint32(ack.Body[1:]) != 350 || state.Header.MsgID != 1455 || len(state.Body) != 8 || binary.LittleEndian.Uint32(state.Body) != 18000 || binary.LittleEndian.Uint32(state.Body[4:]) != (1<<(i+1))-1 {
			t.Fatal(ack, state)
		}
		if _, e = s.handleCurrentEpicClaim(a, 688, 1, body); e != nil {
			t.Fatal(e)
		}
	}
	if monthlyMailCount(t, s, "1") != 3 {
		t.Fatal("mail duplicate")
	}
	ch, _, _ := g.Character.Load(nil, "1")
	state, e := onlineevent.EpicCharacterSnapshot(ch, campaign, s.gameplayNow())
	if e != nil || state.Claimed != 7 {
		t.Fatal(state, e)
	}
	m, _, _ := g.Mailbox.Load(nil, "1")
	for _, mail := range m.Mails {
		if len(mail.Attachments) != 1 || mail.Attachments[0].Count != 1 || !mail.Attachments[0].ExpireAt.After(s.gameplayNow()) {
			t.Fatal(mail)
		}
	}
}

type epicBridgeAbortMailbox struct {
	repo.MailboxAssetUnitOfWork
	fail error
}

func (f epicBridgeAbortMailbox) WithinMailboxAssets(ctx context.Context, a, b string, fn func(repo.CharacterRepository, repo.InventoryRepository, repo.MailboxRepository) error) error {
	return f.MailboxAssetUnitOfWork.WithinMailboxAssets(ctx, a, b, func(c repo.CharacterRepository, v repo.InventoryRepository, m repo.MailboxRepository) error {
		if e := fn(c, v, m); e != nil {
			return e
		}
		return f.fail
	})
}
func TestEpicBenefitsActualBridgeAtomicLateFailureAndRetry(t *testing.T) {
	s, a, c, campaign := seedEpicBridge(t)
	g, _ := s.repositoryGroup()
	base := g.MailboxAssets
	g.MailboxAssets = epicBridgeAbortMailbox{base, errors.New("latephysicalabort")}
	s.repositoryProvider = func() (repo.Group, bool) { return g, true }
	body := make([]byte, 8)
	binary.LittleEndian.PutUint32(body, 350)
	if _, e := s.handleCurrentEpicClaim(a, 688, 1, body); e != nil {
		t.Fatal(e)
	}
	ack, _ := splitGameServerUpperPacket(t, c.write.Bytes())
	if ack.Body[0] != 0 || monthlyMailCount(t, s, "1") != 0 {
		t.Fatal(ack)
	}
	ch, _, _ := g.Character.Load(nil, "1")
	state, _ := onlineevent.EpicCharacterSnapshot(ch, campaign, s.gameplayNow())
	if state.Claimed != 0 {
		t.Fatal(state)
	}
	g.MailboxAssets = base
	c.write.Reset()
	if _, e := s.handleCurrentEpicClaim(a, 688, 1, body); e != nil {
		t.Fatal(e)
	}
	if monthlyMailCount(t, s, "1") != 1 {
		t.Fatal("retry no reward")
	}
}
func TestEpicBenefitsActualBridgeOwnerRequestReject(t *testing.T) {
	for _, name := range []string{"index", "short", "long", "class", "unbound", "untrusted", "foreignaccount", "missingcatalog"} {
		t.Run(name, func(t *testing.T) {
			s, a, _, _ := seedEpicBridge(t)
			body := make([]byte, 8)
			binary.LittleEndian.PutUint32(body, 350)
			class := byte(1)
			switch name {
			case "index":
				binary.LittleEndian.PutUint32(body[4:], 3)
			case "short":
				body = body[:7]
			case "long":
				body = append(body, 0)
			case "class":
				class = 0
			case "unbound":
				s.unbindGameSession(a)
			case "untrusted":
				a.accountFromToken = false
			case "foreignaccount":
				a.accountID = "dnf:2"
			case "missingcatalog":
				s.initialEquipmentArchive = nil
				s.initialEquipmentLoadErr = errors.New("source gone")
			}
			if _, e := s.handleCurrentEpicClaim(a, 688, class, body); e != nil {
				t.Fatal(e)
			}
			if monthlyMailCount(t, s, "1") != 0 || monthlyMailCount(t, s, "5") != 0 {
				t.Fatal("foreign reward")
			}
		})
	}
}
