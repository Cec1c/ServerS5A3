package mysql

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"

	r "longheng.io/server/internal/modules/dnf/repository"
)

const ActivityArchiveFormat = "activity-permanent-receipts-hot-cold-v1"
const activityArchiveCold = "activity_events_cold"
const activityArchiveReceipts = "activity_event_receipts"
const activityArchiveState = "activity_archive_state"
const activityArchiveHotComment = "activity-archive/v1"

var ErrActivityArchiveSchema = errors.New("activity archive schema is incomplete or incompatible")

type ActivityArchiveTrigger struct {
	Name, Timing, Event, Table, Body string
}

// ActivityArchiveLayout is a reviewable contract, not an installer. No startup
// or ordinary request executes its DDL or moves historical records.
type ActivityArchiveLayout struct {
	Database, Prefix, Hot, Cold, Receipts, State string
	ReceiptDDL, StateDDL, ContractSHA256         string
	Triggers                                     []ActivityArchiveTrigger
}

func activityFingerprintSQL(prefix string) string {
	var values []string
	for _, field := range []string{"account_id", "event_id", "character_id", "metric", "amount", "dungeon_id", "item_id", "rarity", "source"} {
		field = prefix + field
		values = append(values, "UNHEX(LPAD(HEX(OCTET_LENGTH(CAST("+field+" AS BINARY))),8,'0'))", "CAST("+field+" AS BINARY)")
	}
	return "UNHEX(SHA2(CONCAT(" + strings.Join(values, ",") + "),256))"
}

func ActivityArchivePlan(database, prefix string) (ActivityArchiveLayout, error) {
	if !r.IsSQLIdentifier(database) {
		return ActivityArchiveLayout{}, ErrSchemaConfigInvalid
	}
	prefix, err := normTablePrefix(prefix)
	if err != nil {
		return ActivityArchiveLayout{}, err
	}
	for _, suffix := range []string{activityArchiveCold, activityArchiveReceipts, activityArchiveState, "activity_arc_receipt_bd"} {
		if len(prefix+"_"+suffix) > 64 {
			return ActivityArchiveLayout{}, ErrSchemaConfigInvalid
		}
	}
	s := ActivityArchiveLayout{Database: database, Prefix: prefix,
		Hot: mysqlTable(database, prefix, activityEventsTable), Cold: mysqlTable(database, prefix, activityArchiveCold),
		Receipts: mysqlTable(database, prefix, activityArchiveReceipts), State: mysqlTable(database, prefix, activityArchiveState)}
	s.ReceiptDDL = "CREATE TABLE " + s.Receipts + " (account_id VARCHAR(128) NOT NULL,event_id VARCHAR(128) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,payload BINARY(32) NOT NULL,PRIMARY KEY(account_id,event_id)) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci"
	s.StateDDL = "CREATE TABLE " + s.State + " (slot TINYINT UNSIGNED NOT NULL,format VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,phase VARCHAR(16) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,contract_sha BINARY(32) NOT NULL,PRIMARY KEY(slot)) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci"
	appendTrigger := func(suffix, timing, event, table, body string) {
		s.Triggers = append(s.Triggers, ActivityArchiveTrigger{prefix + "_activity_arc_" + suffix, timing, event, table, body})
	}
	reject := "SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT='immutable activity history or receipt';"
	eventSame := activityFingerprintSQL("OLD.") + "=" + activityFingerprintSQL("NEW.") +
		" AND BINARY OLD.character_name=BINARY NEW.character_name AND OLD.occurred_at=NEW.occurred_at AND BINARY OLD.service_day=BINARY NEW.service_day"
	update := "BEGIN IF NOT (" + eventSame + ") THEN " + reject + " END IF; END"
	appendTrigger("hot_ai", "AFTER", "INSERT", prefix+"_"+activityEventsTable,
		"INSERT INTO "+s.Receipts+" (account_id,event_id,payload) VALUES (NEW.account_id,NEW.event_id,"+activityFingerprintSQL("NEW.")+")")
	appendTrigger("hot_bu", "BEFORE", "UPDATE", prefix+"_"+activityEventsTable, update)
	appendTrigger("hot_bd", "BEFORE", "DELETE", prefix+"_"+activityEventsTable,
		"BEGIN IF NOT EXISTS (SELECT 1 FROM "+s.Cold+" c WHERE c.account_id=OLD.account_id AND c.event_id=OLD.event_id AND "+activityFingerprintSQL("c.")+"="+activityFingerprintSQL("OLD.")+" AND BINARY c.character_name=BINARY OLD.character_name AND c.occurred_at=OLD.occurred_at AND BINARY c.service_day=BINARY OLD.service_day) THEN "+reject+" END IF; END")
	appendTrigger("cold_bi", "BEFORE", "INSERT", prefix+"_"+activityArchiveCold,
		"BEGIN IF NOT EXISTS (SELECT 1 FROM "+s.Receipts+" r WHERE r.account_id=NEW.account_id AND r.event_id=NEW.event_id AND r.payload="+activityFingerprintSQL("NEW.")+") OR NOT EXISTS (SELECT 1 FROM "+s.Hot+" h WHERE h.account_id=NEW.account_id AND h.event_id=NEW.event_id AND "+activityFingerprintSQL("h.")+"="+activityFingerprintSQL("NEW.")+" AND BINARY h.character_name=BINARY NEW.character_name AND h.occurred_at=NEW.occurred_at AND BINARY h.service_day=BINARY NEW.service_day) THEN "+reject+" END IF; END")
	appendTrigger("cold_bu", "BEFORE", "UPDATE", prefix+"_"+activityArchiveCold, update)
	appendTrigger("cold_bd", "BEFORE", "DELETE", prefix+"_"+activityArchiveCold, "BEGIN "+reject+" END")
	appendTrigger("receipt_bu", "BEFORE", "UPDATE", prefix+"_"+activityArchiveReceipts,
		"BEGIN IF NOT (BINARY OLD.account_id=BINARY NEW.account_id AND BINARY OLD.event_id=BINARY NEW.event_id AND OLD.payload=NEW.payload) THEN "+reject+" END IF; END")
	appendTrigger("receipt_bd", "BEFORE", "DELETE", prefix+"_"+activityArchiveReceipts, "BEGIN "+reject+" END")
	material := ActivityArchiveFormat + "\n" + s.HotDDL() + "\n" + s.ReceiptDDL + "\n" + s.StateDDL + "\n" + s.BackfillSQL()
	for _, trigger := range s.Triggers {
		if len(trigger.Name) > 64 {
			return ActivityArchiveLayout{}, ErrSchemaConfigInvalid
		}
		material += "\n" + s.TriggerDDL(trigger)
	}
	sum := sha256.Sum256([]byte(material))
	s.ContractSHA256 = hex.EncodeToString(sum[:])
	return s, nil
}

func (s ActivityArchiveLayout) TriggerDDL(t ActivityArchiveTrigger) string {
	return "CREATE TRIGGER " + quoteSQLIdentifier(s.Database) + "." + quoteSQLIdentifier(t.Name) + " " + t.Timing + " " + t.Event + " ON " + quoteSQLIdentifier(s.Database) + "." + quoteSQLIdentifier(t.Table) + " FOR EACH ROW " + t.Body
}

// BackfillSQL is for a stopped-writer maintenance connection. The caller must
// bound its primary-key ranges, retain the original table and establish the
// complete contract before publishing phase=active. It is never auto-executed.
func (s ActivityArchiveLayout) BackfillSQL() string {
	return "INSERT INTO " + s.Receipts + " (account_id,event_id,payload) SELECT account_id,event_id," + activityFingerprintSQL("") + " FROM " + s.Hot
}

// HotDDL creates the canonical new hot table only after maintenance has
// retained the complete old table under Cold. The comment lets a restricted
// reader detect an active layout even when it cannot see every companion table.
func (s ActivityArchiveLayout) HotDDL() string {
	ddl := strings.Replace(activitySchema(s.Database, s.Prefix)[0], "CREATE TABLE IF NOT EXISTS", "CREATE TABLE", 1)
	return ddl + " COMMENT='" + activityArchiveHotComment + "'"
}
