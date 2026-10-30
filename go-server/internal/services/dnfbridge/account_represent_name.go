package dnfbridge

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"

	"golang.org/x/text/encoding/simplifiedchinese"
	"longheng.io/server/internal/modules/dnf/dnfenum"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

const (
	currentRepresentAccountNameStateMsgID  = uint16(0x055B) // Verified 2026-07-19 role-select chain: registered accounts render with this state id.
	officialRepresentAccountNameStateMsgID = uint16(1370)   // Current EXE 201FD60; 1371 is ACCOUNT_FRIEND_INFO.
	officialCmdUpdateRepresentAccountName  = uint16(1435)
	officialCmdRepresentNameDuplicateCheck = uint16(1441)
	officialCmdChangeRepresentAccountName  = uint16(1442)
	currentCmdUpdateRepresentAccountName   = uint16(dnfenum.CmdPacketUpdateRepresentAccountName)
	currentCmdRepresentNameDuplicateCheck  = uint16(dnfenum.CmdPacketRepresentAccountNameDuplicateCheck)
	currentCmdChangeRepresentAccountName   = uint16(dnfenum.CmdPacketChangeRepresentAccountName)
	currentRepresentAccountNameMaxBytes    = 16
	representAccountNameRetryCode          = byte(3)
	representAccountNameDuplicateCode      = byte(20)
	representAccountNameInvalidCode        = byte(159)
	adventureGroupCreatedDateMetadataKey   = "adventure_group_created_date"
	adventureGroupCreatedDateLayout        = "2006-01-02"
)

// accountRepresentNameMetadataWriter updates the unique nickname column and
// one derived metadata row without replacing unrelated account metadata.
// MySQL implements the atomic narrow write; other repositories retain the
// historical snapshot save fallback used by existing fixtures.
type accountRepresentNameMetadataWriter interface {
	SaveRepresentAccountName(context.Context, string, string, string, string, string, time.Time) error
}

func (s *Service) sendRepresentAccountNameState(session *gameSession, name string, forceRegister bool, source string) error {
	encoded, err := encodeRepresentAccountName(name)
	if err != nil {
		return fmt.Errorf("encode represent account name state: %w", err)
	}
	var writer packetWriter
	writer.writeRawDstr(encoded)
	msgID := currentRepresentAccountNameStateMsgID
	if s.options.currentClientVersion == "official-24.9" {
		// sub_201FD60 reads only DSTR(17 WCHAR destination). Sending the
		// legacy 1371 name body reaches sub_201FE50's friend-count parser.
		msgID = officialRepresentAccountNameStateMsgID
	} else if forceRegister {
		writer.writeByte(1)
	} else {
		writer.writeByte(0)
	}
	s.logGameEvent(session, "game-represent-account-name-state-send",
		"msg_id", msgID,
		"classification", 0,
		"source", source,
		"registered", len(encoded) != 0 && !forceRegister,
		"name_byte_len", len(encoded))
	return s.sendGameUpperRawClass(session, msgID, writer.bytes(), 0)
}

// sendRepresentAccountNameRegistrationAfterScene emits the existing current
// registration state at most once per game session, and only after the town
// scene tail has been completely initialized. GET_USERINFO must not open this
// modal over the role selector. A durable registered name suppresses the state
// on later logins.
func (s *Service) sendRepresentAccountNameRegistrationAfterScene(session *gameSession, source string) error {
	if session == nil || session.representAccountNameRegistrationSent {
		return nil
	}
	repositories, available := s.repositoryGroup()
	if !available || repositories.Account == nil {
		s.logGameEvent(session, "game-represent-account-name-scene-registration-skipped",
			"source", source,
			"reason", "account_repository_unavailable")
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), createWriteTimeout)
	defer cancel()
	account, found, err := repositories.Account.Load(ctx, strings.TrimSpace(s.accountIDForSession(session)))
	if err != nil {
		s.logGameEvent(session, "game-represent-account-name-scene-registration-skipped",
			"source", source,
			"account_id", s.accountIDForSession(session),
			"error", err,
			"reason", "account_load_failed")
		return nil
	}
	if found && strings.TrimSpace(account.RepresentAccountName) != "" {
		session.representAccountNameRegistrationSent = true
		s.logGameEvent(session, "game-represent-account-name-scene-registration-skipped",
			"source", source,
			"account_id", s.accountIDForSession(session),
			"reason", "durably_registered")
		return nil
	}
	// Preserve the pre-existing current state body exactly; only its lifecycle
	// position changes. It is not a request ACK and has no added fields.
	if err := s.sendRepresentAccountNameState(session, "", false, source); err != nil {
		return err
	}
	session.representAccountNameRegistrationSent = true
	s.logGameEvent(session, "game-represent-account-name-scene-registration-send",
		"source", source,
		"account_id", s.accountIDForSession(session),
		"account_found", found,
		"reason", "first_unregistered_scene_ready")
	return nil
}

func (s *Service) handleRepresentAccountNameDuplicateCheck(session *gameSession, body []byte) error {
	responseMsgID := currentCmdRepresentNameDuplicateCheck
	if s.options.currentClientVersion == "official-24.9" {
		responseMsgID = officialCmdRepresentNameDuplicateCheck
	}
	name, encoded, code, ok := parseRepresentAccountName(body)
	if !ok {
		s.logGameEvent(session, "game-represent-account-name-duplicate-check-rejected",
			"body_len", len(body),
			"code", code,
			"reason", "invalid_name_dstr")
		return s.sendGameUpperFailure(session, responseMsgID, code)
	}

	repositories, available := s.repositoryGroup()
	if !available || repositories.Account == nil {
		return s.sendGameUpperFailure(session, responseMsgID, representAccountNameRetryCode)
	}
	ctx, cancel := context.WithTimeout(context.Background(), createWriteTimeout)
	defer cancel()
	existingAccountID, found, err := findRepresentAccountName(ctx, repositories.Account, name)
	if err != nil {
		s.logGameEvent(session, "game-represent-account-name-duplicate-check-failed",
			"name_byte_len", len(encoded),
			"error", err)
		return s.sendGameUpperFailure(session, responseMsgID, representAccountNameRetryCode)
	}
	if found && strings.TrimSpace(existingAccountID) != strings.TrimSpace(s.accountIDForSession(session)) {
		s.logGameEvent(session, "game-represent-account-name-duplicate-check-result",
			"name_byte_len", len(encoded),
			"available", false)
		return s.sendGameUpperFailure(session, responseMsgID, representAccountNameDuplicateCode)
	}

	var writer packetWriter
	writer.writeRawDstr(encoded)
	s.logGameEvent(session, "game-represent-account-name-duplicate-check-result",
		"name_byte_len", len(encoded),
		"available", true)
	return s.sendGameUpperSuccess(session, responseMsgID, writer.bytes())
}

// Official command IDs are independently verified against this EXE. Legacy
// 1437 is now friend acceptance and 1444 saves recent emoticons: neither may
// reach the represent-name persistence owner, even with a DSTR-shaped body.
func (s *Service) handleOfficialRepresentAccountCommand(session *gameSession, msgID uint16, classification byte, body []byte) (bool, error) {
	if s.options.currentClientVersion != "official-24.9" {
		return false, nil
	}
	switch msgID {
	case officialCmdUpdateRepresentAccountName, officialCmdRepresentNameDuplicateCheck:
		if classification != 1 {
			return true, nil
		}
		if session == nil {
			return true, nil
		}
		if !session.loginPacketSeen || !session.accountFromToken || strings.TrimSpace(session.accountID) == "" {
			return true, s.sendGameUpperFailure(session, msgID, representAccountNameRetryCode)
		}
		if msgID == officialCmdRepresentNameDuplicateCheck {
			return true, s.handleRepresentAccountNameDuplicateCheck(session, body)
		}
		return true, s.handleUpdateRepresentAccountName(session, body, msgID)
	case officialCmdChangeRepresentAccountName:
		// The existing owner only registers an unset name. Rename requires its
		// own durable entitlement transaction; do not reinterpret it as creation.
		if classification != 1 {
			return true, nil
		}
		return true, s.sendGameUpperFailure(session, msgID, representAccountNameRetryCode)
	case currentCmdUpdateRepresentAccountName, currentCmdChangeRepresentAccountName:
		s.logGameEvent(session, "game-official-legacy-represent-name-id-rejected", "msg_id", msgID, "reason", "current_exe_opcode_owns_another_feature")
		return true, nil
	}
	return false, nil
}

func (s *Service) handleUpdateRepresentAccountName(session *gameSession, body []byte, responseMsgID uint16) error {
	name, encoded, code, ok := parseRepresentAccountName(body)
	if !ok {
		s.logGameEvent(session, "game-represent-account-name-update-rejected",
			"body_len", len(body),
			"response_msg_id", responseMsgID,
			"code", code,
			"reason", "invalid_name_dstr")
		return s.sendGameUpperFailure(session, responseMsgID, code)
	}

	repositories, available := s.repositoryGroup()
	if !available || repositories.Account == nil {
		return s.sendGameUpperFailure(session, responseMsgID, representAccountNameRetryCode)
	}
	ctx, cancel := context.WithTimeout(context.Background(), createWriteTimeout)
	defer cancel()
	accountID := strings.TrimSpace(s.accountIDForSession(session))
	account, found, err := repositories.Account.Load(ctx, accountID)
	if err != nil {
		s.logGameEvent(session, "game-represent-account-name-update-load-failed", "account_id", accountID, "error", err)
		return s.sendGameUpperFailure(session, responseMsgID, representAccountNameRetryCode)
	}
	now := time.Now().UTC()
	if found && strings.TrimSpace(account.RepresentAccountName) != "" {
		if strings.EqualFold(strings.TrimSpace(account.RepresentAccountName), name) {
			if ensureAdventureGroupCreatedDate(&account, now) {
				if err := saveRepresentAccountName(ctx, repositories.Account, account, name, now); err != nil {
					s.logGameEvent(session, "game-represent-account-name-date-save-failed", "account_id", accountID, "error", err)
					return s.sendGameUpperFailure(session, responseMsgID, representAccountNameRetryCode)
				}
			}
			return s.sendRepresentAccountNameUpdateSuccess(session, responseMsgID, encoded)
		}
		s.logGameEvent(session, "game-represent-account-name-update-rejected",
			"account_id", accountID,
			"reason", "name_already_registered")
		return s.sendGameUpperFailure(session, responseMsgID, representAccountNameRetryCode)
	}

	existingAccountID, duplicate, err := findRepresentAccountName(ctx, repositories.Account, name)
	if err != nil {
		s.logGameEvent(session, "game-represent-account-name-update-lookup-failed", "account_id", accountID, "error", err)
		return s.sendGameUpperFailure(session, responseMsgID, representAccountNameRetryCode)
	}
	if duplicate && strings.TrimSpace(existingAccountID) != accountID {
		return s.sendGameUpperFailure(session, responseMsgID, representAccountNameDuplicateCode)
	}
	if !found {
		account = dnfrepo.AccountRecord{AccountID: accountID, State: "active"}
	}
	account.RepresentAccountName = name
	ensureAdventureGroupCreatedDate(&account, now)
	if err := saveRepresentAccountName(ctx, repositories.Account, account, name, now); err != nil {
		if errors.Is(err, dnfrepo.ErrRepresentAccountNameExists) {
			return s.sendGameUpperFailure(session, responseMsgID, representAccountNameDuplicateCode)
		}
		s.logGameEvent(session, "game-represent-account-name-update-save-failed", "account_id", accountID, "error", err)
		return s.sendGameUpperFailure(session, responseMsgID, representAccountNameRetryCode)
	}

	if err := s.sendRepresentAccountNameUpdateSuccess(session, responseMsgID, encoded); err != nil {
		return err
	}
	s.logGameEvent(session, "game-represent-account-name-update-saved",
		"account_id", accountID,
		"name_byte_len", len(encoded),
		"response_msg_id", responseMsgID,
		"resume_roster", session.representAccountNamePending)
	if !session.representAccountNamePending {
		return nil
	}
	session.representAccountNamePending = false
	return s.sendUpperGetUserInfoRosterBootstrap(session)
}

func saveRepresentAccountName(
	ctx context.Context,
	repository dnfrepo.AccountRepository,
	account dnfrepo.AccountRecord,
	name string,
	now time.Time,
) error {
	createdDate := account.Metadata[adventureGroupCreatedDateMetadataKey]
	if writer, ok := repository.(accountRepresentNameMetadataWriter); ok {
		return writer.SaveRepresentAccountName(
			ctx,
			account.AccountID,
			name,
			adventureGroupCreatedDateMetadataKey,
			createdDate,
			account.State,
			now.UTC(),
		)
	}
	account.RepresentAccountName = name
	if account.Metadata == nil {
		account.Metadata = make(map[string]string)
	}
	account.Metadata[adventureGroupCreatedDateMetadataKey] = createdDate
	account.UpdatedAt = now.UTC()
	return repository.Save(ctx, account)
}

// ensureAdventureGroupCreatedDate persists the calendar day on which the
// account-wide nickname is first registered.  This is a separate owner from
// AccountRecord.CreatedAt: accounts can exist before the registration modal is
// completed.  The current EXE displays only YYYY.M.D, so a date-only value is
// both sufficient and avoids inventing a time-of-day that the client never
// reads.
func ensureAdventureGroupCreatedDate(account *dnfrepo.AccountRecord, now time.Time) bool {
	if account == nil {
		return false
	}
	if account.Metadata == nil {
		account.Metadata = make(map[string]string)
	}
	if strings.TrimSpace(account.Metadata[adventureGroupCreatedDateMetadataKey]) != "" {
		return false
	}
	account.Metadata[adventureGroupCreatedDateMetadataKey] = now.UTC().Format(adventureGroupCreatedDateLayout)
	account.UpdatedAt = now.UTC()
	return true
}

func (s *Service) sendRepresentAccountNameUpdateSuccess(session *gameSession, responseMsgID uint16, encoded []byte) error {
	var writer packetWriter
	writer.writeRawDstr(encoded)
	return s.sendGameUpperSuccess(session, responseMsgID, writer.bytes())
}

func findRepresentAccountName(ctx context.Context, repository dnfrepo.AccountRepository, name string) (string, bool, error) {
	finder, ok := repository.(dnfrepo.RepresentAccountNameFinder)
	if !ok {
		return "", false, errors.New("represent account name finder is unavailable")
	}
	return finder.FindAccountIDByRepresentName(ctx, name)
}

func parseRepresentAccountName(body []byte) (string, []byte, byte, bool) {
	if len(body) < 5 {
		return "", nil, representAccountNameInvalidCode, false
	}
	length := int(binary.LittleEndian.Uint32(body[:4]))
	// The current op1443 duplicate-check and op1437/op1444 registration
	// writers emit exactly one DSTR, and every reply handler consumes exactly
	// that value. Do not silently accept a second field or protected-transport
	// residue as part of the same request.
	if length <= 0 || length > currentRepresentAccountNameMaxBytes || 4+length != len(body) {
		return "", nil, representAccountNameInvalidCode, false
	}
	raw := append([]byte(nil), body[4:4+length]...)
	if bytes.IndexByte(raw, 0) >= 0 {
		return "", nil, representAccountNameInvalidCode, false
	}
	name, err := decodeCharacterName(raw)
	if err != nil || strings.TrimSpace(name) != name || name == "" {
		return "", nil, representAccountNameInvalidCode, false
	}
	for _, value := range name {
		if unicode.IsControl(value) || unicode.IsSpace(value) {
			return "", nil, representAccountNameInvalidCode, false
		}
	}
	encoded, err := encodeRepresentAccountName(name)
	if err != nil {
		return "", nil, representAccountNameInvalidCode, false
	}
	return name, encoded, 0, true
}

func encodeRepresentAccountName(name string) ([]byte, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, nil
	}
	encoded, err := simplifiedchinese.GB18030.NewEncoder().Bytes([]byte(name))
	if err != nil {
		return nil, err
	}
	if len(encoded) == 0 || len(encoded) > currentRepresentAccountNameMaxBytes {
		return nil, fmt.Errorf("encoded represent account name length %d is outside 1..%d", len(encoded), currentRepresentAccountNameMaxBytes)
	}
	return encoded, nil
}
