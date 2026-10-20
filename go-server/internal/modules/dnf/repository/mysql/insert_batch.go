// 本文件实现"整容器重写"里的批量 INSERT（O-A，开关 DNFBRIDGE_MYSQL_INSERT_BATCH）。
//
// 背景（见 _dist/op19-commit-span-decomposition.md §3/§7-O-A）：一次穿脱的提交跨度里
// 99% 是两条"整容器 DELETE + 逐行 INSERT"（主背包 slots + 装备表），每行一条 SQL。
// 跨度实测 = 0.191 ms × 事务内语句数，截距约 0。把 N 行合成一条多行 INSERT 只减少
// **往返次数**，行集合、行内容、行顺序、DELETE 语义一个字节都不改。
//
// 硬约束（本文件的存在理由）：默认档（未配置 / =1）必须与改动前**逐字节相同** ——
// 同一条 SQL 文本、同一组参数、同一语句顺序。因此批量器在 batch<=1 时不做任何聚合，
// 而且多行语句只在 batch>1 时才被拼出来。
package mysql

import (
	"context"
	"errors"
	"os"
	"strconv"
	"strings"
	"sync"
)

const (
	// currentMysqlInsertBatchEnv 是一条多行 INSERT 里最多几行。默认 1 = 逐行一条
	// INSERT（= 改动前的口径，默认部署不改任何语句流）；建议值 200（见 §7-O-A）。
	currentMysqlInsertBatchEnv = "DNFBRIDGE_MYSQL_INSERT_BATCH"

	// mysqlInsertBatchDefault 是未配置时的批量。
	mysqlInsertBatchDefault = 1

	// mysqlInsertBatchMaxRows 是行数硬上限：配置超过它一律钳到这里。
	// 为什么是 1000：按本仓最宽的这两张表算，8 列 × 1000 行 = 8000 个占位符，
	// 距离 MySQL 的 65535 占位符上限还有 8 倍余量；而 1000 行已经把"每次穿脱 ~2000
	// 条语句"压到个位数，再大只会放大单条语句过大（max_allowed_packet）的风险。
	mysqlInsertBatchMaxRows = 1000

	// mysqlInsertPlaceholderLimit 是单条语句的占位符预算。协议上限是 65535，
	// 这里取 30000（约 2.2 倍余量），再按**实际列数**折算行数上限 —— 这样列数变宽
	// 时不需要改代码也不会撞上限。
	mysqlInsertPlaceholderLimit = 30000

	// mysqlKeyDeleteChunkRows 是增量删除里 entry_key IN (...) 一条语句最多几个 key。
	// 取 200：真实的增量 key 集合是 1-2 个（穿/脱各一个端点），200 既保证"永远一条
	// 语句"，又远低于占位符上限。它与 INSERT 批量开关**无关**，否则 batch=1 时
	// 两个 key 会被拆成两条 DELETE。
	mysqlKeyDeleteChunkRows = 200
)

// insertBatchSetting 是某个环境变量取值解析后的结果。
type insertBatchSetting struct {
	rows      int
	eventKind string // 非空 = 该取值需要记一条事件（每个取值只记一次）
	requested int
	detail    string
}

var (
	insertBatchMu    sync.Mutex
	insertBatchCache = map[string]insertBatchSetting{}
)

// mysqlInsertBatchRowsFor 返回本次写入实际使用的批量大小（行/条）。
// 入参是一起参与同一条语句的各个形状的列数；取其中最小的那个折算结果。
func mysqlInsertBatchRowsFor(columnCounts ...int) int {
	rows := configuredMysqlInsertBatch()
	for _, columns := range columnCounts {
		if columns < 1 {
			columns = 1
		}
		if byPlaceholders := mysqlInsertPlaceholderLimit / columns; rows > byPlaceholders {
			rows = byPlaceholders
		}
	}
	if rows < 1 {
		rows = 1
	}
	return rows
}

// configuredMysqlInsertBatch 解析 DNFBRIDGE_MYSQL_INSERT_BATCH。
// 取值规则：未配置 = 1（今天）；非法/<=0 = 1（今天）并记一条事件；>1000 = 钳到 1000 并记一条事件。
// 每个不同的原始取值只记一次事件（结果按原始字符串缓存）。
func configuredMysqlInsertBatch() int {
	raw := strings.TrimSpace(os.Getenv(currentMysqlInsertBatchEnv))

	insertBatchMu.Lock()
	cached, ok := insertBatchCache[raw]
	if !ok {
		cached = parseMysqlInsertBatch(raw)
		insertBatchCache[raw] = cached
	}
	insertBatchMu.Unlock()

	if cached.eventKind != "" {
		notifyWriteEvent(WriteEvent{
			Kind:      cached.eventKind,
			Rows:      cached.rows,
			Requested: cached.requested,
			Detail:    cached.detail,
		})
	}
	return cached.rows
}

func parseMysqlInsertBatch(raw string) insertBatchSetting {
	if raw == "" {
		return insertBatchSetting{rows: mysqlInsertBatchDefault}
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value <= 0 {
		return insertBatchSetting{
			rows:      mysqlInsertBatchDefault,
			eventKind: "insert_batch_invalid",
			detail:    currentMysqlInsertBatchEnv + "=" + raw,
		}
	}
	if value > mysqlInsertBatchMaxRows {
		return insertBatchSetting{
			rows:      mysqlInsertBatchMaxRows,
			eventKind: "insert_batch_clamped",
			requested: value,
			detail:    currentMysqlInsertBatchEnv + "=" + raw,
		}
	}
	return insertBatchSetting{rows: value}
}

// insertShape 描述一条"单行 INSERT"语句：前缀（含库表名、列清单与 VALUES）加一组占位符。
// batch=1 时拼出来与改动前的字符串逐字节相同；batch>1 时只把占位符组重复 N 次。
type insertShape struct {
	table        string
	collection   string
	prefix       string
	placeholders string
	columns      int
}

func (s insertShape) single() string {
	return s.prefix + s.placeholders
}

func (s insertShape) rows(count int) string {
	if count <= 1 {
		return s.single()
	}
	var builder strings.Builder
	builder.Grow(len(s.prefix) + count*(len(s.placeholders)+1))
	builder.WriteString(s.prefix)
	for idx := 0; idx < count; idx++ {
		if idx > 0 {
			builder.WriteString(",")
		}
		builder.WriteString(s.placeholders)
	}
	return builder.String()
}

// rowInsertBatcher 把同一条形状的单行写入攒成一条多行 INSERT。
//
// 不变量：
//   - batch<=1 时每条 add 立即 ExecContext，语句文本、参数、**调用顺序**都与改动前
//     完全一致（因此默认部署的语句流逐字节相同，包括 items 行与 extra 行的交错顺序）；
//   - batch>1 时只减少往返：同形状的行按加入顺序攒成一条多行 INSERT，**表内行顺序不变**。
//     跨表交错顺序不参与任何语义（两张表主键不同、互无外键），最终库状态逐行相同 ——
//     这一点由 mysql_write_scope_equivalence_test.go 的行模型比对钉住。
type rowInsertBatcher struct {
	database SQLDB
	ctx      context.Context
	ownerID  string
	batch    int
	shape    insertShape
	args     []any
	rows     int
}

func newRowInsertBatcher(ctx context.Context, database SQLDB, ownerID string, shape insertShape) *rowInsertBatcher {
	return &rowInsertBatcher{
		database: database,
		ctx:      ctx,
		ownerID:  ownerID,
		batch:    mysqlInsertBatchRowsFor(shape.columns),
		shape:    shape,
	}
}

func (b *rowInsertBatcher) add(rowArgs ...any) error {
	if b == nil {
		return nil
	}
	if b.batch <= 1 {
		_, err := b.database.ExecContext(b.ctx, b.shape.single(), rowArgs...)
		return err
	}
	b.args = append(b.args, rowArgs...)
	b.rows++
	if b.rows >= b.batch {
		return b.flush()
	}
	return nil
}

func (b *rowInsertBatcher) flush() error {
	if b == nil || b.rows == 0 {
		return nil
	}
	shape := b.shape
	rows := b.rows
	args := b.args
	b.args = nil
	b.rows = 0
	if rows <= 1 {
		// 单个残留行按单行语句发出（与默认档同一条文本）。
		if _, err := b.database.ExecContext(b.ctx, shape.single(), args...); err != nil {
			return err
		}
		return nil
	}
	if _, err := b.database.ExecContext(b.ctx, shape.rows(rows), args...); err != nil {
		if !isPacketTooLargeError(err) {
			return err
		}
		// 单条语句超过 max_allowed_packet：记一条事件，并把这一批按行重放。
		// 若重放也失败（连接已被服务端关闭），返回错误让事务回滚 —— 绝不静默丢行。
		notifyWriteEvent(WriteEvent{
			Kind:       "insert_batch_packet_too_large",
			Table:      shape.table,
			Collection: shape.collection,
			OwnerID:    b.ownerID,
			Rows:       rows,
			Detail:     currentMysqlInsertBatchEnv,
			Err:        err,
		})
		return b.replayRows(shape, args, rows)
	}
	return nil
}

func (b *rowInsertBatcher) replayRows(shape insertShape, args []any, rows int) error {
	width := shape.columns
	if width < 1 || rows < 1 || len(args) != width*rows {
		return errors.New("mysql: batched insert replay argument width mismatch")
	}
	for idx := 0; idx < rows; idx++ {
		row := args[idx*width : (idx+1)*width]
		if _, err := b.database.ExecContext(b.ctx, shape.single(), row...); err != nil {
			return err
		}
	}
	return nil
}

// isPacketTooLargeError 判定"这条语句超过了 max_allowed_packet"。
// 1153 = ER_NET_PACKET_TOO_LARGE，1301 = ER_SP_TOO_LONG 一族的服务端拒绝；
// 不同驱动/服务端的措辞不一致，因此同时看错误文本。
func isPacketTooLargeError(err error) bool {
	if err == nil {
		return false
	}
	text := strings.ToLower(err.Error())
	for _, needle := range []string{"packet too large", "max_allowed_packet", "1153", "1301"} {
		if strings.Contains(text, needle) {
			return true
		}
	}
	return false
}

// sortedUniqueStrings 排序并去重，保证增量写生成的语句流是确定的（测试可逐字节比对）。
func sortedUniqueStrings(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	set := make(map[string]struct{}, len(values))
	for _, value := range values {
		set[value] = struct{}{}
	}
	return sortedStringKeys(set)
}

// chunkStrings 把 keys 切成每片最多 size 个（size<=0 时按 1 处理）。
func chunkStrings(values []string, size int) [][]string {
	if len(values) == 0 {
		return nil
	}
	if size < 1 {
		size = 1
	}
	chunks := make([][]string, 0, (len(values)+size-1)/size)
	for start := 0; start < len(values); start += size {
		end := start + size
		if end > len(values) {
			end = len(values)
		}
		chunks = append(chunks, values[start:end])
	}
	return chunks
}

func stringsToAny(values []string) []any {
	out := make([]any, len(values))
	for idx, value := range values {
		out[idx] = value
	}
	return out
}
