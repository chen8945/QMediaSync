package backup

import (
	"bytes"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"gorm.io/gorm"
	"gorm.io/gorm/schema"

	"qmediasync/internal/db"
	"qmediasync/internal/helpers"
	"qmediasync/internal/models"
)

const logicalManifestFile = "manifest.json"
const logicalFormatVersion = 2
const logicalTimePrecision = "UTC, rounded to microseconds"

type logicalColumn struct {
	Name    string `json:"name"`
	Type    string `json:"type"`
	Size    int    `json:"size,omitempty"`
	Bits    int    `json:"bits,omitempty"`
	NotNull bool   `json:"not_null,omitempty"`
}

type logicalTable struct {
	Name      string          `json:"name"`
	ModelName string          `json:"model"`
	File      string          `json:"file"`
	Columns   []logicalColumn `json:"columns"`
	RowCount  int64           `json:"row_count"`
	SHA256    string          `json:"sha256"`
	Model     any             `json:"-"`
}

type logicalExclusion struct {
	Name   string `json:"name"`
	Column string `json:"column,omitempty"`
	Reason string `json:"reason"`
}

// retiredLogicalColumns 仅列出已经从模型和业务入口删除、但历史迁移没有 DROP 的列。
// emby_data 在 30→31 迁移中清空；其余字段在旧配置模型替换时停用，不能再次作为业务数据恢复。
func retiredLogicalColumns(table string) []string {
	switch table {
	case "emby_media_items":
		return []string{"emby_data"}
	case "backup_config":
		return []string{"maintenance_mode", "maintenance_mode_time"}
	case "settings":
		return []string{"bai_du_pan_qps", "bai_du_pan_qpm", "bai_du_pan_qph", "bai_du_pan_qpt"}
	case "sync_paths":
		return []string{"baidu_sync_method"}
	default:
		return nil
	}
}

type logicalManifest struct {
	ApplicationVersion string             `json:"application_version,omitempty"`
	FormatVersion      int                `json:"format_version"`
	SchemaVersion      int                `json:"schema_version"`
	SourceEngine       string             `json:"source_engine"`
	CreatedAt          time.Time          `json:"created_at"`
	TimePrecision      string             `json:"time_precision"`
	Tables             []logicalTable     `json:"tables"`
	Excluded           []logicalExclusion `json:"excluded"`
}

// logicalTables 与建表共用注册入口，不维护第二份备份字段清单。
func logicalTables(database *gorm.DB) ([]logicalTable, []logicalExclusion, error) {
	var tables []logicalTable
	var excluded []logicalExclusion
	seen := make(map[string]bool)
	for _, model := range models.AllTables {
		table, err := describeLogicalTable(database, model)
		if err != nil {
			return nil, nil, err
		}
		if seen[table.Name] {
			return nil, nil, fmt.Errorf("备份表重复注册：%s", table.Name)
		}
		seen[table.Name] = true
		reason := ""
		switch model.(type) {
		case models.UserSession, *models.UserSession:
			reason = "browser sessions are cleared on restore"
		case models.EmbyObservedEvidenceIndex, *models.EmbyObservedEvidenceIndex,
			models.EmbyItemMembership, *models.EmbyItemMembership:
			reason = "derived index is rebuilt on restore"
		}
		if reason != "" {
			excluded = append(excluded, logicalExclusion{Name: table.Name, Reason: reason})
			continue
		}
		tables = append(tables, table)
		for _, column := range retiredLogicalColumns(table.Name) {
			if slices.ContainsFunc(table.Columns, func(current logicalColumn) bool { return current.Name == column }) {
				continue
			}
			// 清单声明统一的退休列规则，不依赖源库是否仍残留这些列，便于新旧实例互转。
			excluded = append(excluded, logicalExclusion{
				Name: table.Name, Column: column, Reason: "retired column is no longer used by the application",
			})
		}
	}
	return tables, excluded, nil
}

func describeLogicalTable(database *gorm.DB, model any) (logicalTable, error) {
	statement := &gorm.Statement{DB: database}
	if err := statement.Parse(model); err != nil {
		return logicalTable{}, fmt.Errorf("解析备份模型失败：%w", err)
	}
	table := logicalTable{
		Name: statement.Schema.Table, ModelName: statement.Schema.ModelType.Name(), Model: model,
	}
	table.File = "tables/" + table.ModelName + ".json"
	for _, name := range statement.Schema.DBNames {
		field := statement.Schema.FieldsByDBName[name]
		column, err := describeLogicalColumn(field)
		if err != nil {
			return logicalTable{}, fmt.Errorf("备份表 %s 列 %s：%w", table.Name, name, err)
		}
		table.Columns = append(table.Columns, column)
	}
	if len(table.Columns) == 0 {
		return logicalTable{}, fmt.Errorf("备份表 %s 没有持久化列", table.Name)
	}
	return table, nil
}

func describeLogicalColumn(field *schema.Field) (logicalColumn, error) {
	column := logicalColumn{Name: field.DBName, NotNull: field.NotNull || field.PrimaryKey}
	if _, generated := field.TagSettings["GENERATED"]; generated {
		return column, errors.New("生成列尚未定义备份适配")
	}
	if field.GORMDataType != schema.String && field.DataType != field.GORMDataType {
		return column, fmt.Errorf("数据库类型 %s 尚未定义备份适配", field.DataType)
	}
	switch field.GORMDataType {
	case schema.Int:
		column.Type, column.Bits = "integer", field.Size
	case schema.Uint:
		column.Type, column.Bits = "unsigned_integer", field.Size
	case schema.Float:
		if field.Precision > 0 || field.Scale > 0 {
			return column, errors.New("定点数字尚未定义备份适配")
		}
		column.Type, column.Bits = "float", field.Size
	case schema.Bool:
		column.Type = "boolean"
	case schema.String:
		column.Type = "text"
		dataType := strings.ToLower(string(field.DataType))
		switch {
		case dataType == "string":
			column.Size = field.Size
		case dataType == "text":
		case strings.HasPrefix(dataType, "varchar(") && strings.HasSuffix(dataType, ")"):
			size, err := strconv.Atoi(strings.TrimSuffix(strings.TrimPrefix(dataType, "varchar("), ")"))
			if err != nil || size <= 0 {
				return column, errors.New("字符串长度定义无效")
			}
			column.Size = size
		default:
			return column, fmt.Errorf("字符串数据库类型 %s 尚未定义备份适配", field.DataType)
		}
	case schema.Time:
		column.Type = "timestamp"
	case schema.Bytes:
		column.Type = "bytes"
	default:
		return column, fmt.Errorf("数据库类型 %s 尚未定义备份适配", field.GORMDataType)
	}
	return column, nil
}

func validateLogicalSourceColumns(database *gorm.DB, table logicalTable) error {
	columns, err := database.Migrator().ColumnTypes(table.Name)
	if err != nil {
		return fmt.Errorf("读取备份表 %s 结构失败：%w", table.Name, err)
	}
	expected := make(map[string]bool, len(table.Columns))
	for _, column := range table.Columns {
		expected[column.Name] = true
	}
	for _, column := range columns {
		if !expected[column.Name()] {
			if slices.Contains(retiredLogicalColumns(table.Name), column.Name()) {
				continue
			}
			return fmt.Errorf("备份表 %s 存在未注册的持久化列 %s", table.Name, column.Name())
		}
		delete(expected, column.Name())
	}
	for name := range expected {
		return fmt.Errorf("备份表 %s 缺少持久化列 %s，请先完成数据库迁移", table.Name, name)
	}
	return nil
}

func writeLogicalBackup(database *gorm.DB, dir string, progress func(logicalTable)) (err error) {
	database, closeReader, err := db.OpenBackupReader(database)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, closeReader()) }()
	if err := helpers.EnsurePrivateDir(dir, "tables"); err != nil {
		return err
	}
	options := &sql.TxOptions{ReadOnly: true}
	if database.Dialector.Name() == "postgres" {
		options.Isolation = sql.LevelRepeatableRead
	}
	return database.Transaction(func(tx *gorm.DB) error {
		var versions []models.Migrator
		if err := tx.Find(&versions).Error; err != nil {
			return fmt.Errorf("读取备份数据库版本失败：%w", err)
		}
		if len(versions) != 1 || versions[0].VersionCode != models.MaxVersionCode {
			return errors.New("数据库结构版本不完整或与当前程序不一致，不能创建备份")
		}
		tables, excluded, err := logicalTables(tx)
		if err != nil {
			return err
		}
		manifest := logicalManifest{
			ApplicationVersion: helpers.Version,
			FormatVersion:      logicalFormatVersion, SchemaVersion: versions[0].VersionCode,
			SourceEngine: tx.Dialector.Name(), CreatedAt: time.Now().UTC(), TimePrecision: logicalTimePrecision,
			Tables: tables, Excluded: excluded,
		}
		if len(tables)+2 > maxBackupEntries {
			return fmt.Errorf("%w：备份表文件过多", ErrArchiveLimit)
		}
		remaining := maxBackupExpandedSize
		for i := range manifest.Tables {
			if err := writeLogicalTable(tx, dir, &manifest.Tables[i], &remaining); err != nil {
				return err
			}
			if progress != nil {
				progress(manifest.Tables[i])
			}
		}
		content, err := json.MarshalIndent(manifest, "", "  ")
		if err != nil {
			return fmt.Errorf("编码备份清单失败：%w", err)
		}
		if int64(len(content)) > maxBackupManifestSize || int64(len(content)) > remaining {
			return fmt.Errorf("%w：备份清单或展开内容过大", ErrArchiveLimit)
		}
		if err := os.WriteFile(filepath.Join(dir, logicalManifestFile), content, 0600); err != nil {
			return fmt.Errorf("写入备份清单失败：%w", err)
		}
		return nil
	}, options)
}

func writeLogicalTable(database *gorm.DB, dir string, table *logicalTable, remaining *int64) (err error) {
	if err := validateLogicalSourceColumns(database, *table); err != nil {
		return err
	}
	file, err := os.OpenFile(filepath.Join(dir, table.File), os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0600)
	if err != nil {
		return fmt.Errorf("创建备份表 %s 文件失败：%w", table.Name, err)
	}
	defer func() { err = errors.Join(err, file.Close()) }()
	hash := sha256.New()
	writer := io.MultiWriter(file, hash)
	statement := &gorm.Statement{DB: database}
	quoted := make([]string, len(table.Columns))
	for i, column := range table.Columns {
		quoted[i] = statement.Quote(column.Name)
	}
	rows, err := database.Raw("SELECT " + strings.Join(quoted, ",") + " FROM " + statement.Quote(table.Name)).Rows()
	if err != nil {
		return fmt.Errorf("读取备份表 %s 失败：%w", table.Name, err)
	}
	defer func() { err = errors.Join(err, rows.Close()) }()
	table.RowCount = 0
	for rows.Next() {
		values := make([]any, len(table.Columns))
		pointers := make([]any, len(values))
		for i := range values {
			pointers[i] = &values[i]
		}
		if err := rows.Scan(pointers...); err != nil {
			return fmt.Errorf("读取备份表 %s 第 %d 行失败：%w", table.Name, table.RowCount+1, err)
		}
		record := make(map[string]any, len(values))
		for i, value := range values {
			column := table.Columns[i]
			encoded, err := encodeLogicalValue(column, value)
			if err != nil {
				return fmt.Errorf("备份表 %s 第 %d 行列 %s：%w", table.Name, table.RowCount+1, column.Name, err)
			}
			record[column.Name] = encoded
		}
		if err := writeBackupRow(writer, record, maxBackupRowSize, remaining); err != nil {
			return fmt.Errorf("写入备份表 %s 第 %d 行失败：%w", table.Name, table.RowCount+1, err)
		}
		table.RowCount++
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("读取备份表 %s 失败：%w", table.Name, err)
	}
	table.SHA256 = hex.EncodeToString(hash.Sum(nil))
	return nil
}

func writeBackupRow(writer io.Writer, record any, limit int64, remaining *int64) error {
	content, err := json.Marshal(record)
	if err != nil {
		return err
	}
	// 上限包含行末换行符，与下一次 JSON Decode 消费的分隔符保持一致。
	size := int64(len(content)) + 1
	if size > limit || size > *remaining {
		return fmt.Errorf("%w：JSON 记录或备份展开内容过大", ErrArchiveLimit)
	}
	n, err := writer.Write(append(content, '\n'))
	*remaining -= int64(n)
	if err == nil && int64(n) != size {
		return io.ErrShortWrite
	}
	return err
}

func encodeLogicalValue(column logicalColumn, value any) (any, error) {
	if value == nil {
		return nil, nil
	}
	if column.Type == "timestamp" {
		var parsed time.Time
		switch v := value.(type) {
		case time.Time:
			parsed = v
		case string:
			var err error
			parsed, err = parseLogicalTime(v)
			if err != nil {
				return nil, err
			}
		default:
			return nil, fmt.Errorf("时间数据库值类型不支持：%T", value)
		}
		return parsed.UTC().Round(time.Microsecond).Format(time.RFC3339Nano), nil
	}
	if raw, ok := value.([]byte); ok && column.Type != "bytes" {
		value = string(raw)
	}
	switch column.Type {
	case "text":
		text, ok := value.(string)
		if !ok {
			return nil, fmt.Errorf("字符串数据库值类型不支持：%T", value)
		}
		if err := validateLogicalText(text); err != nil {
			return nil, err
		}
		return text, nil
	case "boolean":
		switch value {
		case true, int64(1), "true", "t", "1":
			return true, nil
		case false, int64(0), "false", "f", "0":
			return false, nil
		default:
			return nil, errors.New("布尔数据库值必须是 true、false、0 或 1")
		}
	case "integer", "unsigned_integer":
		var raw string
		switch v := value.(type) {
		case int64:
			raw = strconv.FormatInt(v, 10)
		case string:
			raw = v
		default:
			return nil, fmt.Errorf("整数数据库值类型不支持：%T", value)
		}
		return decodeLogicalValue(column, json.RawMessage(raw))
	case "float":
		var number float64
		switch v := value.(type) {
		case float64:
			number = v
		case int64:
			number = float64(v)
		case string:
			parsed, err := strconv.ParseFloat(v, 64)
			if err != nil {
				return nil, errors.New("浮点数据库值格式无效")
			}
			number = parsed
		default:
			return nil, fmt.Errorf("浮点数据库值类型不支持：%T", value)
		}
		if math.IsInf(number, 0) || math.IsNaN(number) {
			return nil, errors.New("非有限浮点值不支持备份")
		}
		return number, nil
	case "bytes":
		if raw, ok := value.([]byte); ok {
			return base64.StdEncoding.EncodeToString(raw), nil
		}
		return nil, fmt.Errorf("二进制数据库值类型不支持：%T", value)
	default:
		return nil, errors.New("未定义的备份列类型")
	}
}

func parseLogicalTime(value string) (time.Time, error) {
	for _, layout := range []string{
		time.RFC3339Nano, "2006-01-02 15:04:05.999999999-07:00", "2006-01-02 15:04:05.999999999-07",
		"2006-01-02 15:04:05.999999999", "2006-01-02T15:04:05.999999999", "2006-01-02",
	} {
		if parsed, err := time.Parse(layout, value); err == nil {
			return parsed, nil
		}
	}
	return time.Time{}, errors.New("时间数据库值格式无效")
}

func validateLogicalText(value string) error {
	if !utf8.ValidString(value) {
		return errors.New("文本包含无效 UTF-8，不能无损备份")
	}
	if strings.ContainsRune(value, 0) {
		return errors.New("文本包含 NUL，不能在 SQLite 与 PostgreSQL 间恢复")
	}
	return nil
}

// validateJSONUnicode 防止 encoding/json 将非法字节或孤立 UTF-16 代理项静默替换为 U+FFFD。
func validateJSONUnicode(raw []byte) error {
	if !utf8.Valid(raw) {
		return errors.New("JSON 包含无效 UTF-8")
	}
	insideString := false
	for i := 0; i < len(raw); i++ {
		if raw[i] == '"' {
			insideString = !insideString
			continue
		}
		if !insideString || raw[i] != '\\' {
			continue
		}
		i++
		if i >= len(raw) || raw[i] != 'u' {
			continue
		}
		if i+4 >= len(raw) {
			return errors.New("JSON Unicode 转义不完整")
		}
		unit, err := strconv.ParseUint(string(raw[i+1:i+5]), 16, 16)
		if err != nil {
			return errors.New("JSON Unicode 转义无效")
		}
		i += 4
		if unit >= 0xDC00 && unit <= 0xDFFF {
			return errors.New("JSON 包含未配对的 UTF-16 代理项")
		}
		if unit < 0xD800 || unit > 0xDBFF {
			continue
		}
		if i+6 >= len(raw) || raw[i+1] != '\\' || raw[i+2] != 'u' {
			return errors.New("JSON 包含未配对的 UTF-16 代理项")
		}
		low, err := strconv.ParseUint(string(raw[i+3:i+7]), 16, 16)
		if err != nil || low < 0xDC00 || low > 0xDFFF {
			return errors.New("JSON 包含未配对的 UTF-16 代理项")
		}
		i += 6
	}
	return nil
}

func decodeLogicalValue(column logicalColumn, raw json.RawMessage) (any, error) {
	raw = bytes.TrimSpace(raw)
	if bytes.Equal(raw, []byte("null")) {
		return nil, nil
	}
	switch column.Type {
	case "integer", "unsigned_integer":
		if len(raw) == 0 || raw[0] == '"' {
			return nil, errors.New("整数 JSON 值必须是数字")
		}
		var number json.Number
		if err := json.Unmarshal(raw, &number); err != nil {
			return nil, errors.New("整数 JSON 值无效")
		}
		bits := column.Bits
		if bits == 0 || bits > 64 {
			bits = 64
		}
		if column.Type == "unsigned_integer" {
			value, err := strconv.ParseUint(string(number), 10, bits)
			if err != nil || value > math.MaxInt64 {
				return nil, errors.New("无符号整数超出双引擎支持范围")
			}
			return int64(value), nil
		}
		value, err := strconv.ParseInt(string(number), 10, bits)
		if err != nil {
			return nil, errors.New("整数超出字段范围")
		}
		return value, nil
	case "float":
		var value float64
		if err := json.Unmarshal(raw, &value); err != nil || math.IsInf(value, 0) || math.IsNaN(value) {
			return nil, errors.New("浮点 JSON 值无效")
		}
		return value, nil
	case "boolean":
		var value bool
		if err := json.Unmarshal(raw, &value); err != nil {
			return nil, errors.New("布尔 JSON 值无效")
		}
		return value, nil
	case "text", "bytes", "timestamp":
		if err := validateJSONUnicode(raw); err != nil {
			return nil, err
		}
		var value string
		if err := json.Unmarshal(raw, &value); err != nil {
			return nil, errors.New("字符串 JSON 值无效")
		}
		if column.Type == "bytes" {
			decoded, err := base64.StdEncoding.DecodeString(value)
			if err != nil {
				return nil, errors.New("二进制 JSON 值无效")
			}
			return decoded, nil
		}
		if column.Type == "timestamp" {
			parsed, err := time.Parse(time.RFC3339Nano, value)
			if err != nil || parsed.Nanosecond()%1000 != 0 || parsed.Location() != time.UTC {
				return nil, errors.New("时间必须使用 UTC 微秒精度")
			}
			return parsed, nil
		}
		if err := validateLogicalText(value); err != nil {
			return nil, err
		}
		return value, nil
	default:
		return nil, errors.New("未定义的备份列类型")
	}
}

// readLogicalRows 按数据库列解码，禁止缺列、额外列和隐式零值填充。
func readLogicalRows(dir string, table logicalTable, consume func(map[string]any) error) (err error) {
	file, err := os.Open(filepath.Join(dir, table.File))
	if err != nil {
		return fmt.Errorf("读取备份表 %s 文件失败：%w", table.Name, err)
	}
	defer func() { err = errors.Join(err, file.Close()) }()
	hash := sha256.New()
	var rowCount int64
	if err := readBackupRows(io.TeeReader(file, hash), maxBackupRowSize, func(raw json.RawMessage) error {
		if err := validateJSONUnicode(raw); err != nil {
			return fmt.Errorf("%w：%v", ErrArchiveInvalid, err)
		}
		record, err := decodeLogicalRecord(table, raw)
		if err != nil {
			return fmt.Errorf("%w：%v", ErrArchiveInvalid, err)
		}
		if consume != nil {
			if err := consume(record); err != nil {
				return err
			}
		}
		rowCount++
		return nil
	}); err != nil {
		return fmt.Errorf("备份表 %s 第 %d 行：%w", table.Name, rowCount+1, err)
	}
	if rowCount != table.RowCount {
		return fmt.Errorf("%w：备份表 %s 行数不符：清单 %d，文件 %d", ErrArchiveInvalid, table.Name, table.RowCount, rowCount)
	}
	if hex.EncodeToString(hash.Sum(nil)) != table.SHA256 {
		return fmt.Errorf("%w：备份表 %s 文件校验失败", ErrArchiveInvalid, table.Name)
	}
	return nil
}

func decodeLogicalRecord(table logicalTable, raw json.RawMessage) (map[string]any, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	token, err := decoder.Token()
	if err != nil || token != json.Delim('{') {
		return nil, errors.New("备份行必须是 JSON 对象")
	}
	columns := make(map[string]logicalColumn, len(table.Columns))
	for _, column := range table.Columns {
		columns[column.Name] = column
	}
	record := make(map[string]any, len(table.Columns))
	for decoder.More() {
		token, err := decoder.Token()
		if err != nil {
			return nil, errors.New("备份列名无效")
		}
		name, ok := token.(string)
		if !ok {
			return nil, errors.New("备份列名必须是字符串")
		}
		column, exists := columns[name]
		if !exists {
			return nil, errors.New("备份行包含未声明的列")
		}
		if _, duplicate := record[name]; duplicate {
			return nil, fmt.Errorf("列 %s 重复", name)
		}
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			return nil, fmt.Errorf("列 %s JSON 值无效", name)
		}
		decoded, err := decodeLogicalValue(column, value)
		if err != nil {
			return nil, fmt.Errorf("列 %s：%w", name, err)
		}
		record[name] = decoded
	}
	if _, err := decoder.Token(); err != nil {
		return nil, errors.New("备份行 JSON 对象未完整结束")
	}
	for _, column := range table.Columns {
		if _, exists := record[column.Name]; !exists {
			return nil, fmt.Errorf("缺少列 %s", column.Name)
		}
	}
	return record, nil
}

func validateLogicalTargetRow(database *gorm.DB, table logicalTable, record map[string]any) error {
	for _, column := range table.Columns {
		value := record[column.Name]
		if value == nil && column.NotNull {
			return fmt.Errorf("列 %s 不允许 NULL", column.Name)
		}
		if text, ok := value.(string); ok && column.Type == "text" && database.Dialector.Name() == "postgres" &&
			column.Size > 0 && utf8.RuneCountInString(text) > column.Size {
			return fmt.Errorf("列 %s 超出目标数据库的 %d 字符限制", column.Name, column.Size)
		}
	}
	return nil
}

// prepareLogicalBackup 必须在目标数据库发生任何修改前完成；无清单时由调用者走旧格式适配。
func prepareLogicalBackup(dir string, database *gorm.DB) (*logicalManifest, error) {
	content, err := readLimitedBackupFile(filepath.Join(dir, logicalManifestFile), maxBackupManifestSize)
	if err != nil {
		return nil, err
	}
	if err := validateJSONUnicode(content); err != nil {
		return nil, fmt.Errorf("%w：备份清单：%v", ErrArchiveInvalid, err)
	}
	var manifest logicalManifest
	decoder := json.NewDecoder(bytes.NewReader(content))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&manifest); err != nil {
		return nil, fmt.Errorf("%w：备份清单 JSON 无效", ErrArchiveInvalid)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("%w：备份清单包含额外内容", ErrArchiveInvalid)
	}
	if manifest.FormatVersion != logicalFormatVersion || manifest.SchemaVersion != models.MaxVersionCode {
		return nil, fmt.Errorf("%w：不支持备份格式或数据库版本：格式 %d，数据库 %d，当前数据库 %d", ErrArchiveUnsupported, manifest.FormatVersion, manifest.SchemaVersion, models.MaxVersionCode)
	}
	if manifest.TimePrecision != logicalTimePrecision || (manifest.SourceEngine != "sqlite" && manifest.SourceEngine != "postgres") {
		return nil, fmt.Errorf("%w：备份清单的数据库引擎或时间精度无效", ErrArchiveInvalid)
	}
	expected, exclusions, err := logicalTables(database)
	if err != nil {
		return nil, err
	}
	if len(manifest.Tables) != len(expected) || !reflect.DeepEqual(manifest.Excluded, exclusions) {
		return nil, fmt.Errorf("%w：备份清单的表集合或明确排除项与当前程序不一致", ErrArchiveInvalid)
	}
	if err := validateLogicalFiles(dir, expected); err != nil {
		return nil, err
	}
	for i := range expected {
		actual, want := &manifest.Tables[i], expected[i]
		if actual.Name != want.Name || actual.ModelName != want.ModelName || actual.File != want.File || !reflect.DeepEqual(actual.Columns, want.Columns) {
			return nil, fmt.Errorf("%w：备份表 %s 的表或列定义与当前程序不一致", ErrArchiveInvalid, want.Name)
		}
		if actual.RowCount < 0 || len(actual.SHA256) != sha256.Size*2 {
			return nil, fmt.Errorf("%w：备份表 %s 的行数或校验值无效", ErrArchiveInvalid, want.Name)
		}
		actual.Model = want.Model
		isMigrator := false
		switch want.Model.(type) {
		case models.Migrator, *models.Migrator:
			isMigrator = true
		}
		if isMigrator && actual.RowCount != 1 {
			return nil, fmt.Errorf("%w：备份数据库版本表必须且只能包含一条记录", ErrArchiveInvalid)
		}
		if err := readLogicalRows(dir, *actual, func(row map[string]any) error {
			if isMigrator && row["version_code"] != int64(manifest.SchemaVersion) {
				return fmt.Errorf("%w：数据库版本记录与备份清单不一致", ErrArchiveInvalid)
			}
			if err := validateLogicalTargetRow(database, *actual, row); err != nil {
				return fmt.Errorf("%w：%v", ErrArchiveInvalid, err)
			}
			return nil
		}); err != nil {
			return nil, err
		}
	}
	return &manifest, nil
}
