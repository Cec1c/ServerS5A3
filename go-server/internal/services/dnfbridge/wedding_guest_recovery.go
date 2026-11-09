package dnfbridge

import (
	"context"
	"errors"
	r "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/wedding"
	"strconv"
	"time"
)

type currentWeddingGuestSettlement struct {
	first, second wedding.Actor
	at            int64
	key           uint32
	timer         *time.Timer
	lastError     string
}

// Caller holds wedding.mu. A durable ledger remains the authority even if an
// immediate mailbox write or notification fails after marriage/expiration.
func (s *Service) queueCurrentWeddingGuestSettlementLocked(pair wedding.PairResult, at int64) error {
	owner, e := s.currentWeddingOwner()
	if e != nil {
		return e
	}
	ctx, cancel := context.WithTimeout(context.Background(), currentRepositorySnapshotTimeout)
	defer cancel()
	ledger, found, e := owner.ReadGuestLedger(ctx, []string{pair.Actor.CharacterID, pair.Partner.CharacterID}, at)
	if e != nil || !found {
		return e
	}
	if ledger.Settled || ledger.Refunded {
		return nil
	}
	if s.wedding.guestSettlements == nil {
		s.wedding.guestSettlements = map[uint32]*currentWeddingGuestSettlement{}
	}
	job := s.wedding.guestSettlements[ledger.Key]
	if job == nil {
		job = &currentWeddingGuestSettlement{first: wedding.Actor{AccountID: pair.Actor.AccountID, CharacterID: pair.Actor.CharacterID}, second: wedding.Actor{AccountID: pair.Partner.AccountID, CharacterID: pair.Partner.CharacterID}, at: at, key: ledger.Key}
		s.wedding.guestSettlements[job.key] = job
	}
	s.armCurrentWeddingGuestSettlementLocked(job, 0)
	return nil
}

func (s *Service) armCurrentWeddingGuestSettlementLocked(job *currentWeddingGuestSettlement, delay time.Duration) {
	if s.wedding.ceremoniesStopped || s.wedding.guestSettlements[job.key] != job {
		return
	}
	if job.timer != nil {
		job.timer.Stop()
	}
	job.timer = time.AfterFunc(delay, func() { s.runCurrentWeddingGuestSettlement(job) })
}

func (s *Service) runCurrentWeddingGuestSettlement(job *currentWeddingGuestSettlement) {
	s.wedding.mu.Lock()
	defer s.wedding.mu.Unlock()
	if s.wedding.ceremoniesStopped || s.wedding.guestSettlements[job.key] != job {
		return
	}
	job.timer = nil
	ctx, cancel := context.WithTimeout(context.Background(), createWriteTimeout)
	defer cancel()
	owner, e := s.currentWeddingOwner()
	var result wedding.GuestSettlementResult
	if e == nil {
		a, fa, ea := owner.Repositories.Character.Load(ctx, job.first.CharacterID)
		b, fb, eb := owner.Repositories.Character.Load(ctx, job.second.CharacterID)
		if ea != nil {
			e = ea
		} else if eb != nil {
			e = eb
		} else if !fa || !fb || a.AccountID != job.first.AccountID || b.AccountID != job.second.AccountID {
			e = wedding.ErrOwnership
		} else {
			x, ex := wedding.Read(a)
			y, ey := wedding.Read(b)
			req := wedding.PairRequest{Actor: job.first, Partner: job.second, Now: s.gameplayNow()}
			if ex != nil {
				e = ex
			} else if ey != nil {
				e = ey
			} else if x.CeremonyAt == job.at && y.CeremonyAt == job.at && x.Phase == wedding.Married && y.Phase == wedding.Married {
				var rules *wedding.GuestRules
				rules, e = s.currentWeddingGuestRules()
				if e == nil {
					result, e = owner.FinalizeGuestWedding(ctx, req, job.at, rules)
				}
			} else if x.CeremonyAt != job.at && y.CeremonyAt != job.at {
				result, e = owner.RefundGuestWedding(ctx, req, job.at)
			} else {
				e = wedding.ErrState
			}
		}
	}
	if e != nil {
		if job.lastError != e.Error() {
			s.logInfo("婚礼邮件结算待恢复", "event", "game-wedding-guest-settlement-deferred", "run_key", job.key, "ceremony_at", job.at, "error", e)
			job.lastError = e.Error()
		}
		s.armCurrentWeddingGuestSettlementLocked(job, 5*time.Second)
		return
	}
	delete(s.wedding.guestSettlements, job.key)
	for _, id := range result.MailRecipients {
		n, e := wedding.NumericID(id)
		if e != nil {
			continue
		}
		if identity, online := s.onlineGameSessionCharacterSnapshot(uint16(n)); online {
			if e := s.sendMailboxAlarmForSession(identity.session, uint16(n), "wedding_settlement"); e != nil {
				s.logGameEvent(identity.session, "game-wedding-mail-alarm-deferred", "error", e)
			}
		}
	}
}

func (s *Service) recoverCurrentWeddingRuns() {
	ctx, cancel := context.WithTimeout(context.Background(), createWriteTimeout)
	owner, e := s.currentWeddingOwner()
	var runs []wedding.GuestRunSnapshot
	if e == nil {
		runs, e = owner.GuestRuns(ctx)
	}
	cancel()
	if e == nil {
		for _, run := range runs {
			// An unrelated damaged reference must not starve every later run.
			// Each run gets its own bounded snapshot rather than sharing one
			// deadline across an indefinitely growing index.
			e = errors.Join(e, s.recoverCurrentWeddingRun(owner, run))
		}
	}
	if e != nil {
		s.wedding.mu.Lock()
		if !s.wedding.ceremoniesStopped {
			if s.wedding.recoveryTimer != nil {
				s.wedding.recoveryTimer.Stop()
			}
			s.wedding.recoveryTimer = time.AfterFunc(5*time.Second, s.recoverCurrentWeddingRuns)
		}
		s.wedding.mu.Unlock()
		s.logInfo("婚礼启动恢复待重试", "event", "game-wedding-startup-recovery-deferred", "error", e)
	}
}

func (s *Service) recoverCurrentWeddingRun(owner wedding.Owner, run wedding.GuestRunSnapshot) error {
	ctx, cancel := context.WithTimeout(context.Background(), createWriteTimeout)
	defer cancel()
	var chars [2]r.CharacterRecord
	for i, id := range run.Reference.Hosts {
		c, found, e := owner.Repositories.Character.Load(ctx, id)
		if e != nil {
			return e
		}
		if !found {
			return wedding.ErrOwnership
		}
		chars[i] = c
	}
	x, xe := wedding.Read(chars[0])
	y, ye := wedding.Read(chars[1])
	if xe != nil || ye != nil {
		return wedding.ErrState
	}
	pair := wedding.PairResult{Actor: chars[0], Partner: chars[1]}
	s.wedding.mu.Lock()
	defer s.wedding.mu.Unlock()
	if s.wedding.ceremoniesStopped {
		return nil
	}
	if x.CeremonyAt == run.Reference.At && y.CeremonyAt == run.Reference.At && (x.Phase == wedding.Reserved || x.Phase == wedding.InCeremony) {
		return s.scheduleCurrentWeddingCeremonyLocked(pair)
	}
	return s.queueCurrentWeddingGuestSettlementLocked(pair, run.Reference.At)
}

// Selection schedules a retry from durable run references, never from a
// client-supplied owner/key. It does not wait while selection holds asset locks.
func (s *Service) queueCurrentWeddingGuestRecovery(session *gameSession) error {
	identity, bound := s.boundGameSessionCharacterSnapshot(session)
	if !bound || session.events == nil || session.events.loop == nil {
		return nil
	}
	return s.tryPostGameSessionCharacterEvent(session, "game-wedding-debt-recovery", identity.character, identity.generation, func() error {
		ctx, cancel := context.WithTimeout(context.Background(), currentRepositorySnapshotTimeout)
		defer cancel()
		c, e := s.currentWeddingCharacter(ctx, identity)
		if e != nil {
			return e
		}
		state, e := wedding.Read(c)
		if e != nil || state.Phase != wedding.Married {
			return e
		}
		owner, e := s.currentWeddingOwner()
		if e != nil {
			return e
		}
		partner, found, e := owner.Repositories.Character.Load(ctx, strconv.FormatInt(state.PartnerID, 10))
		if e != nil {
			return e
		}
		if !found {
			return wedding.ErrState
		}
		s.wedding.mu.Lock()
		defer s.wedding.mu.Unlock()
		return s.queueCurrentWeddingGuestSettlementLocked(wedding.PairResult{Actor: c, Partner: partner}, state.CeremonyAt)
	})
}
