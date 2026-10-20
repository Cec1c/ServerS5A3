// 本文件定义账号级"史诗图鉴(epic piece book)"碎片数量记录。
//
// 语义（三条互相独立的证据指向同一形状）：
//
//  1. 参考实现 C#（`a21zigai\ServerS4A21`）：`accounts.epic_piece_counts` 是小端
//     `Int32 count[]`，下标 = PVF `etc/epicpieceinfo.etc` 的
//     `[equipment piece drop info]/[piece list]` 二元组顺序
//     （`Server\DfoServer\Sqlite\item_schema.sql:26`、
//     `Game\Inventory\Repositories\EpicPieceBookRepository.cs:8,21-24,47-50`），
//     按 **AccountId** 加载（`Services\InventoryService.cs:627`）；
//     `Docs\新版背包架构业务接入规范.md:337-338` 明写"史诗碎片…是账号级史诗图鉴数量"。
//  2. 厂商 9.16 参考服务端（`D:\DNF90-test\server\runtime\logs\packet_log.txt`，只读）：
//     `EVENT kind=dnf-epic-book-catalog-loaded entries=966`（与我们真档 PVF 解析出的
//     966 组二元组**逐数相等**），并且 7/7 条
//     `EVENT kind=game-epic-book-snapshot-sent … account_id=dnf:<hash> wire_opcode=1149`
//     都以 **账号** 为键 ⇒ 账号级、跨角色共享。
//  3. 同血统的 Go 分支（`90CN\90原神开源0.1.8\dnf90-source-oneclick-main`，
//     `internal\services\dnfbridge\epic_piece_balance.go:20,25-27,29-43`）用
//     `epic_piece:<碎片 itemID>` 键存绝对值。
//
// 本仓库没有 `accounts.epic_piece_counts` 这一列；账号级标量在本仓库统一走
// `dnf_account_metadata` 的 key/value 行（同一取舍见 account_story_digest.go），
// 所以这里按**每个碎片一行**的十进制文本保存，语义与 0.1.8 的键值逐字节一致，
// 且不需要新表、不需要迁移（表已存在，见 `mysql_account.go` 的
// `mysqlAccountMetadataTable`）。
package repository

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"time"
)

const (
	// AccountEpicPieceMetadataPrefix 是账号元数据(dnf_account_metadata)里史诗图鉴
	// 碎片数量的键前缀；完整键 = 前缀 + 十进制碎片 itemID（例 `epic_piece:10300000`）。
	AccountEpicPieceMetadataPrefix = "epic_piece:"

	// AccountEpicPieceBalanceMax 是单个碎片可记账的上限。参考实现的计数是
	// `Int32 count[]`（A21 `EpicPieceBookModel.ToBlob`），0.1.8 的 MySQL upsert 也把
	// 结果钉在 2147483647，因此这里保持同一个上限，避免溢出成负数。
	AccountEpicPieceBalanceMax = uint32(2147483647)
)

var (
	// ErrAccountEpicPieceCounterUnavailable：底层账号仓储不支持原子自增。
	// 刻意**不做**"读-改-写"兜底：史诗碎片是纯增量账本，非原子兜底会在并发/重放时
	// 静默丢碎片。宁可让掉落留在原地（拾取方会记
	// `account_epic_piece_credit_failed_drop_remains_available` 并保留地面掉落）。
	ErrAccountEpicPieceCounterUnavailable = errors.New("dnf account epic piece counter is unavailable")
	// ErrAccountEpicPieceAccountMissing：账号 ID 为空。
	ErrAccountEpicPieceAccountMissing = errors.New("dnf account epic piece account is missing")
	// ErrAccountEpicPieceIDRequired：碎片 itemID 或增量为 0。
	ErrAccountEpicPieceIDRequired = errors.New("dnf account epic piece id and amount are required")
)

// AccountEpicPieceBalanceCounter 是账号仓储的可选能力：**原子**自增一个非负的
// 账号级计数并返回写入后的绝对值。它独立于 AccountRepository，避免事务作用域账号
// 存储被迫实现跨账号/跨事务的读改写。
type AccountEpicPieceBalanceCounter interface {
	AddAccountEpicPieceBalance(context.Context, string, uint32, uint32, time.Time) (uint32, error)
}

// AccountEpicPieceMetadataKey 返回某个碎片在账号元数据里的键。
func AccountEpicPieceMetadataKey(pieceID uint32) string {
	return AccountEpicPieceMetadataPrefix + strconv.FormatUint(uint64(pieceID), 10)
}

// ParseAccountEpicPieceBalance 解析账号元数据里的十进制碎片数量。
// 空串、缺失或非法值都按 0（= 图鉴该条为空）处理。
func ParseAccountEpicPieceBalance(value string) uint32 {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0
	}
	parsed, err := strconv.ParseUint(value, 10, 32)
	if err != nil {
		return 0
	}
	if parsed > uint64(AccountEpicPieceBalanceMax) {
		return AccountEpicPieceBalanceMax
	}
	return uint32(parsed)
}

// FormatAccountEpicPieceBalance 把碎片数量写成账号元数据的十进制文本。
func FormatAccountEpicPieceBalance(balance uint32) string {
	if balance > AccountEpicPieceBalanceMax {
		balance = AccountEpicPieceBalanceMax
	}
	return strconv.FormatUint(uint64(balance), 10)
}

// AccountEpicPieceBalances 读取一个账号记录里**全部**非零碎片数量。
// 返回值是 map（不是按 PVF 下标排列的数组），因此 PVF 换版导致目录条数变化时
// 已有进度不会错位——这也是本仓库选 key/value 而不是位置数组的实际收益。
func AccountEpicPieceBalances(account AccountRecord) map[uint32]uint32 {
	balances := make(map[uint32]uint32, len(account.Metadata))
	for key, raw := range account.Metadata {
		pieceID, ok := parseAccountEpicPieceMetadataKey(key)
		if !ok {
			continue
		}
		balance := ParseAccountEpicPieceBalance(raw)
		if balance == 0 {
			continue
		}
		balances[pieceID] = balance
	}
	return balances
}

// parseAccountEpicPieceMetadataKey 只接受 `epic_piece:<非零 u32>` 形态的键。
func parseAccountEpicPieceMetadataKey(key string) (uint32, bool) {
	if !strings.HasPrefix(key, AccountEpicPieceMetadataPrefix) {
		return 0, false
	}
	raw := strings.TrimSpace(strings.TrimPrefix(key, AccountEpicPieceMetadataPrefix))
	if raw == "" {
		return 0, false
	}
	parsed, err := strconv.ParseUint(raw, 10, 32)
	if err != nil || parsed == 0 {
		return 0, false
	}
	return uint32(parsed), true
}

// AddAccountEpicPieceBalance 在账号级史诗图鉴账本上原子加 amount 并返回新绝对值。
// 仓储不支持原子自增时返回 ErrAccountEpicPieceCounterUnavailable（fail-closed）。
func AddAccountEpicPieceBalance(
	ctx context.Context,
	repo AccountRepository,
	accountID string,
	pieceID uint32,
	amount uint32,
	now time.Time,
) (uint32, error) {
	if repo == nil {
		return 0, ErrAccountEpicPieceCounterUnavailable
	}
	accountID = strings.TrimSpace(accountID)
	if accountID == "" {
		return 0, ErrAccountEpicPieceAccountMissing
	}
	if pieceID == 0 || amount == 0 {
		return 0, ErrAccountEpicPieceIDRequired
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	now = now.UTC()
	if now.IsZero() {
		now = time.Now().UTC()
	}
	counter, ok := repo.(AccountEpicPieceBalanceCounter)
	if !ok {
		return 0, ErrAccountEpicPieceCounterUnavailable
	}
	return counter.AddAccountEpicPieceBalance(ctx, accountID, pieceID, amount, now)
}
