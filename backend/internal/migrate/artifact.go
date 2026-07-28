package migrate

import (
	"archive/zip"
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"

	"qmediasync/internal/db"
	"qmediasync/internal/models"

	"gorm.io/gorm"
	"gorm.io/gorm/schema"
)

const (
	migrationArchiveFormat       = "migration"
	migrationArchiveVersion      = 1
	migrationManifestPath        = "manifest.json"
	migrationDataDirectory       = "tables"
	migrationPageSize            = 200
	migrationImportBatchSize     = 200
	migrationMaxManifestSize int = 1 << 20
	migrationMaxJSONLineSize int = 16 << 20
)

// ErrInvalidMigrationArchive 表示迁移包的目录、清单或数据与当前表目录不一致。
var ErrInvalidMigrationArchive = errors.New("迁移包无效")

type migrationManifest struct {
	Format  string                   `json:"format"`
	Version int                      `json:"version"`
	Tables  []migrationManifestTable `json:"tables"`
}

type migrationManifestTable struct {
	ID           string `json:"id"`
	PhysicalName string `json:"physical_name"`
	ImportOrder  int    `json:"import_order"`
	Path         string `json:"path"`
	SHA256       string `json:"sha256"`
	RecordCount  int64  `json:"record_count"`
}

type migrationArchive struct {
	reader   *zip.ReadCloser
	manifest migrationManifest
	files    map[string]*zip.File
}

func (archive *migrationArchive) Close() error {
	return archive.reader.Close()
}

// PreflightPendingMigration 在连接目标数据库前完整检查迁移包。
// 导入时会再次执行同一检查，防止预检和提交之间的包被替换。
func PreflightPendingMigration(path string) error {
	archive, err := openMigrationArchive(path, models.SQLitePostgresMigrationTableCatalog())
	if err != nil {
		return err
	}
	return archive.Close()
}

// ImportPendingMigration 在当前 PostgreSQL 连接中原子导入迁移包。
// 只有目标 schema、全部数据和序列均成功后才删除原包；删除失败同样阻止业务启动，
// 使下次启动从该包重新执行完整导入。
func ImportPendingMigration(path string) (err error) {
	catalog := models.SQLitePostgresMigrationTableCatalog()

	if !startProgress("import", "正在验证迁移包", len(catalog)) {
		return fmt.Errorf("导入迁移包：迁移操作正在运行")
	}
	defer func() {
		if err != nil {
			finishProgress("迁移导入失败", err.Error())
			return
		}
		finishProgress("迁移导入完成", "")
	}()
	if db.Db == nil || db.Db.Dialector == nil || db.Db.Dialector.Name() != "postgres" {
		return fmt.Errorf("导入迁移包：目标数据库不是 PostgreSQL")
	}

	return importMigrationArchive(path, db.Db, catalog, func(completed int, description string) {
		updateProgress(description, completed, "")
	}, migrateImportedDatabase)
}

func importMigrationArchive(path string, database *gorm.DB, catalog []models.TableCatalogEntry, progress func(int, string), postImport func() error) error {
	if database == nil {
		return fmt.Errorf("导入迁移包：目标数据库连接为空")
	}
	if len(catalog) == 0 {
		return fmt.Errorf("导入迁移包：表目录为空")
	}
	archive, err := openMigrationArchive(path, catalog)
	if err != nil {
		return err
	}
	defer archive.Close()

	if err := database.Transaction(func(transaction *gorm.DB) error {
		if err := migrateTargetSchema(transaction, catalog); err != nil {
			return err
		}
		if err := clearMigrationTables(transaction, catalog); err != nil {
			return err
		}
		if err := importMigrationTables(transaction, archive, catalog, progress); err != nil {
			return err
		}
		return repairMigrationSequences(transaction, catalog)
	}); err != nil {
		return fmt.Errorf("导入迁移包: %w", err)
	}
	if postImport != nil {
		if err := postImport(); err != nil {
			return fmt.Errorf("升级导入后的数据库: %w", err)
		}
	}
	if err := os.Remove(path); err != nil {
		return fmt.Errorf("删除已导入的迁移包: %w", err)
	}
	return nil
}

// migrateImportedDatabase 确保导入的源库已完成当前版本的数据迁移。
// 导入事务已提交但升级失败时迁移包会保留；下次启动会从完整包重新导入再重试升级。
func migrateImportedDatabase() error {
	models.Migrate()
	var migrator models.Migrator
	if err := db.Db.First(&migrator).Error; err != nil {
		return fmt.Errorf("读取导入后的数据库版本: %w", err)
	}
	if migrator.VersionCode != models.MaxVersionCode {
		return fmt.Errorf("导入后的数据库版本为 %d，期望 %d", migrator.VersionCode, models.MaxVersionCode)
	}
	if err := models.ResetStaleEmbySyncRunOnStartup(); err != nil {
		return fmt.Errorf("清理遗留 Emby 同步状态: %w", err)
	}
	return nil
}

// writeMigrationArchive 生成 SQLite→PostgreSQL 专用迁移包。它不调用常规备份导出逻辑，
// 以便迁移的清单、预检与发布边界独立演进。
func writeMigrationArchive(destination string, source *gorm.DB, catalog []models.TableCatalogEntry, progress func(int, string)) error {
	if source == nil {
		return fmt.Errorf("导出迁移包：源数据库连接为空")
	}
	if len(catalog) == 0 {
		return fmt.Errorf("导出迁移包：表目录为空")
	}
	if _, err := os.Lstat(destination); err == nil {
		return fmt.Errorf("导出迁移包：目标文件已存在")
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("检查迁移包目标: %w", err)
	}

	stagingDirectory, err := os.MkdirTemp(filepath.Dir(destination), ".migration-*")
	if err != nil {
		return fmt.Errorf("创建迁移包暂存目录: %w", err)
	}
	defer os.RemoveAll(stagingDirectory)
	if err := os.Chmod(stagingDirectory, 0o700); err != nil {
		return fmt.Errorf("设置迁移包暂存目录权限: %w", err)
	}
	if err := os.Mkdir(filepath.Join(stagingDirectory, migrationDataDirectory), 0o700); err != nil {
		return fmt.Errorf("创建迁移数据目录: %w", err)
	}

	manifest := migrationManifest{
		Format:  migrationArchiveFormat,
		Version: migrationArchiveVersion,
		Tables:  make([]migrationManifestTable, 0, len(catalog)),
	}
	err = source.Transaction(func(transaction *gorm.DB) error {
		for index, entry := range catalog {
			table, err := writeMigrationTable(stagingDirectory, transaction, entry)
			if err != nil {
				return err
			}
			manifest.Tables = append(manifest.Tables, table)
			if progress != nil {
				progress(index+1, fmt.Sprintf("已导出 %s，共 %d 条", entry.PhysicalName, table.RecordCount))
			}
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("读取迁移源数据: %w", err)
	}
	if err := writeMigrationManifest(stagingDirectory, manifest); err != nil {
		return err
	}
	if err := publishMigrationArchive(destination, stagingDirectory, catalog); err != nil {
		return err
	}
	return nil
}

func writeMigrationTable(directory string, database *gorm.DB, entry models.TableCatalogEntry) (migrationManifestTable, error) {
	archivePath := migrationTablePath(entry)
	filePath := filepath.Join(directory, filepath.FromSlash(archivePath))
	file, err := os.OpenFile(filePath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return migrationManifestTable{}, fmt.Errorf("创建表 %s 的迁移数据: %w", entry.ID, err)
	}

	digest := sha256.New()
	buffered := bufio.NewWriter(io.MultiWriter(file, digest))
	encoder := json.NewEncoder(buffered)
	modelType := migrationModelType(entry.Model)
	modelSchema, err := migrationModelSchema(entry.Model)
	if err != nil {
		file.Close()
		return migrationManifestTable{}, fmt.Errorf("读取表 %s 的模型结构: %w", entry.ID, err)
	}
	sliceType := reflect.SliceOf(reflect.PointerTo(modelType))
	var recordCount int64
	for page := 0; ; page++ {
		records := reflect.New(sliceType)
		if err := database.Model(entry.Model).
			Order("id").
			Offset(page * migrationPageSize).
			Limit(migrationPageSize).
			Find(records.Interface()).Error; err != nil {
			file.Close()
			return migrationManifestTable{}, fmt.Errorf("读取表 %s: %w", entry.ID, err)
		}
		values := records.Elem()
		if values.Len() == 0 {
			break
		}
		for index := 0; index < values.Len(); index++ {
			record := migrationRecordMap(values.Index(index), modelSchema)
			if err := encoder.Encode(record); err != nil {
				file.Close()
				return migrationManifestTable{}, fmt.Errorf("编码表 %s: %w", entry.ID, err)
			}
			recordCount++
		}
	}
	if err := buffered.Flush(); err != nil {
		file.Close()
		return migrationManifestTable{}, fmt.Errorf("写入表 %s: %w", entry.ID, err)
	}
	if err := file.Sync(); err != nil {
		file.Close()
		return migrationManifestTable{}, fmt.Errorf("同步表 %s: %w", entry.ID, err)
	}
	if err := file.Close(); err != nil {
		return migrationManifestTable{}, fmt.Errorf("关闭表 %s 的迁移数据: %w", entry.ID, err)
	}
	return migrationManifestTable{
		ID:           entry.ID,
		PhysicalName: entry.PhysicalName,
		ImportOrder:  entry.ImportOrder,
		Path:         archivePath,
		SHA256:       hex.EncodeToString(digest.Sum(nil)),
		RecordCount:  recordCount,
	}, nil
}

func writeMigrationManifest(directory string, manifest migrationManifest) error {
	data, err := json.Marshal(manifest)
	if err != nil {
		return fmt.Errorf("编码迁移包清单: %w", err)
	}
	path := filepath.Join(directory, migrationManifestPath)
	file, err := os.OpenFile(path, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("创建迁移包清单: %w", err)
	}
	if _, err := file.Write(data); err != nil {
		file.Close()
		return fmt.Errorf("写入迁移包清单: %w", err)
	}
	if err := file.Sync(); err != nil {
		file.Close()
		return fmt.Errorf("同步迁移包清单: %w", err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("关闭迁移包清单: %w", err)
	}
	return nil
}

func publishMigrationArchive(destination string, directory string, catalog []models.TableCatalogEntry) error {
	temporary, err := os.CreateTemp(filepath.Dir(destination), ".migration-*.zip")
	if err != nil {
		return fmt.Errorf("创建迁移包临时文件: %w", err)
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0o600); err != nil {
		temporary.Close()
		return fmt.Errorf("设置迁移包临时文件权限: %w", err)
	}

	writer := zip.NewWriter(temporary)
	paths := []string{migrationManifestPath}
	for _, entry := range catalog {
		paths = append(paths, migrationTablePath(entry))
	}
	for _, archivePath := range paths {
		if err := addMigrationArchiveFile(writer, directory, archivePath); err != nil {
			writer.Close()
			temporary.Close()
			return err
		}
	}
	if err := writer.Close(); err != nil {
		temporary.Close()
		return fmt.Errorf("关闭迁移包: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return fmt.Errorf("同步迁移包: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("关闭迁移包: %w", err)
	}
	if err := os.Rename(temporaryPath, destination); err != nil {
		return fmt.Errorf("发布迁移包: %w", err)
	}
	return nil
}

func addMigrationArchiveFile(writer *zip.Writer, directory string, archivePath string) error {
	file, err := os.Open(filepath.Join(directory, filepath.FromSlash(archivePath)))
	if err != nil {
		return fmt.Errorf("读取迁移包暂存文件 %s: %w", archivePath, err)
	}
	defer file.Close()
	entry, err := writer.CreateHeader(&zip.FileHeader{Name: archivePath, Method: zip.Deflate})
	if err != nil {
		return fmt.Errorf("创建迁移包条目 %s: %w", archivePath, err)
	}
	if _, err := io.Copy(entry, file); err != nil {
		return fmt.Errorf("写入迁移包条目 %s: %w", archivePath, err)
	}
	return nil
}

func openMigrationArchive(path string, catalog []models.TableCatalogEntry) (*migrationArchive, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, fmt.Errorf("读取迁移包: %w", err)
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("%w：迁移包不是普通文件", ErrInvalidMigrationArchive)
	}
	reader, err := zip.OpenReader(path)
	if err != nil {
		return nil, fmt.Errorf("%w：无法打开 ZIP", ErrInvalidMigrationArchive)
	}
	archive := &migrationArchive{reader: reader, files: make(map[string]*zip.File, len(reader.File))}
	if err := archive.validate(catalog); err != nil {
		reader.Close()
		return nil, err
	}
	return archive, nil
}

func (archive *migrationArchive) validate(catalog []models.TableCatalogEntry) error {
	for _, file := range archive.reader.File {
		if file.FileInfo().IsDir() || file.Name == "" || strings.Contains(file.Name, "\\") || file.Name != filepath.ToSlash(filepath.Clean(file.Name)) {
			return fmt.Errorf("%w：包含无效路径", ErrInvalidMigrationArchive)
		}
		if _, exists := archive.files[file.Name]; exists {
			return fmt.Errorf("%w：包含重复文件", ErrInvalidMigrationArchive)
		}
		archive.files[file.Name] = file
	}
	manifestFile, exists := archive.files[migrationManifestPath]
	if !exists {
		return fmt.Errorf("%w：缺少清单", ErrInvalidMigrationArchive)
	}
	manifest, err := readMigrationManifest(manifestFile)
	if err != nil {
		return err
	}
	archive.manifest = manifest
	if manifest.Format != migrationArchiveFormat || manifest.Version != migrationArchiveVersion {
		return fmt.Errorf("%w：格式或版本不受支持", ErrInvalidMigrationArchive)
	}
	if len(manifest.Tables) != len(catalog) || len(archive.files) != len(catalog)+1 {
		return fmt.Errorf("%w：表目录不完整", ErrInvalidMigrationArchive)
	}

	byID := make(map[string]migrationManifestTable, len(manifest.Tables))
	for _, table := range manifest.Tables {
		if table.ID == "" || table.PhysicalName == "" || table.Path == "" || table.RecordCount < 0 || !isMigrationSHA256(table.SHA256) {
			return fmt.Errorf("%w：清单表项无效", ErrInvalidMigrationArchive)
		}
		if _, exists := byID[table.ID]; exists {
			return fmt.Errorf("%w：清单包含重复表", ErrInvalidMigrationArchive)
		}
		byID[table.ID] = table
	}
	for _, entry := range catalog {
		table, exists := byID[entry.ID]
		if !exists || table.PhysicalName != entry.PhysicalName || table.ImportOrder != entry.ImportOrder || table.Path != migrationTablePath(entry) {
			return fmt.Errorf("%w：表目录与当前模型不一致", ErrInvalidMigrationArchive)
		}
		file, exists := archive.files[table.Path]
		if !exists {
			return fmt.Errorf("%w：缺少表 %s 的数据", ErrInvalidMigrationArchive, entry.ID)
		}
		if err := verifyMigrationTableFile(file, table, entry); err != nil {
			return err
		}
	}
	return nil
}

func readMigrationManifest(file *zip.File) (migrationManifest, error) {
	reader, err := file.Open()
	if err != nil {
		return migrationManifest{}, fmt.Errorf("%w：读取清单失败", ErrInvalidMigrationArchive)
	}
	defer reader.Close()
	data, err := io.ReadAll(io.LimitReader(reader, int64(migrationMaxManifestSize)+1))
	if err != nil || len(data) > migrationMaxManifestSize {
		return migrationManifest{}, fmt.Errorf("%w：清单无效", ErrInvalidMigrationArchive)
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var manifest migrationManifest
	if err := decoder.Decode(&manifest); err != nil {
		return migrationManifest{}, fmt.Errorf("%w：清单无法解析", ErrInvalidMigrationArchive)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return migrationManifest{}, fmt.Errorf("%w：清单无法解析", ErrInvalidMigrationArchive)
	}
	return manifest, nil
}

func verifyMigrationTableFile(file *zip.File, table migrationManifestTable, entry models.TableCatalogEntry) error {
	reader, err := file.Open()
	if err != nil {
		return fmt.Errorf("%w：读取表 %s 失败", ErrInvalidMigrationArchive, table.ID)
	}
	defer reader.Close()
	modelSchema, err := migrationModelSchema(entry.Model)
	if err != nil {
		return fmt.Errorf("%w：读取表 %s 的模型结构失败", ErrInvalidMigrationArchive, table.ID)
	}
	digest := sha256.New()
	scanner := bufio.NewScanner(io.TeeReader(reader, digest))
	scanner.Buffer(make([]byte, 64*1024), migrationMaxJSONLineSize+1)
	var count int64
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 || len(line) > migrationMaxJSONLineSize || !json.Valid(line) {
			return fmt.Errorf("%w：表 %s 的 JSONL 无效", ErrInvalidMigrationArchive, table.ID)
		}
		if _, err := unmarshalMigrationRecord(line, entry.Model, modelSchema); err != nil {
			return fmt.Errorf("%w：表 %s 的 JSONL 无效", ErrInvalidMigrationArchive, table.ID)
		}
		count++
	}
	if err := scanner.Err(); err != nil || count != table.RecordCount || !strings.EqualFold(hex.EncodeToString(digest.Sum(nil)), table.SHA256) {
		return fmt.Errorf("%w：表 %s 的数据校验失败", ErrInvalidMigrationArchive, table.ID)
	}
	return nil
}

func migrateTargetSchema(database *gorm.DB, catalog []models.TableCatalogEntry) error {
	for _, entry := range catalog {
		if err := database.AutoMigrate(entry.Model); err != nil {
			return fmt.Errorf("迁移目标表 %s 的结构: %w", entry.ID, err)
		}
	}
	return nil
}

func clearMigrationTables(database *gorm.DB, catalog []models.TableCatalogEntry) error {
	for index := len(catalog) - 1; index >= 0; index-- {
		entry := catalog[index]
		if err := database.Session(&gorm.Session{SkipHooks: true, AllowGlobalUpdate: true}).
			Where("1 = 1").Delete(entry.Model).Error; err != nil {
			return fmt.Errorf("清空目标表 %s: %w", entry.ID, err)
		}
	}
	return nil
}

func importMigrationTables(database *gorm.DB, archive *migrationArchive, catalog []models.TableCatalogEntry, progress func(int, string)) error {
	manifestByID := make(map[string]migrationManifestTable, len(archive.manifest.Tables))
	for _, table := range archive.manifest.Tables {
		manifestByID[table.ID] = table
	}
	for index, entry := range catalog {
		table := manifestByID[entry.ID]
		file := archive.files[table.Path]
		reader, err := file.Open()
		if err != nil {
			return fmt.Errorf("打开表 %s 的迁移数据: %w", entry.ID, err)
		}
		count, importErr := importMigrationJSONL(database, entry, reader)
		closeErr := reader.Close()
		if importErr != nil {
			return fmt.Errorf("导入表 %s: %w", entry.ID, importErr)
		}
		if closeErr != nil {
			return fmt.Errorf("关闭表 %s 的迁移数据: %w", entry.ID, closeErr)
		}
		if count != table.RecordCount {
			return fmt.Errorf("%w：表 %s 的记录数不一致", ErrInvalidMigrationArchive, entry.ID)
		}
		if progress != nil {
			progress(index+1, fmt.Sprintf("已导入 %s，共 %d 条", entry.PhysicalName, count))
		}
	}
	return nil
}

func importMigrationJSONL(database *gorm.DB, entry models.TableCatalogEntry, source io.Reader) (int64, error) {
	modelType := migrationModelType(entry.Model)
	modelSchema, err := migrationModelSchema(entry.Model)
	if err != nil {
		return 0, fmt.Errorf("读取模型结构: %w", err)
	}
	sliceType := reflect.SliceOf(reflect.PointerTo(modelType))
	batch := reflect.MakeSlice(sliceType, 0, migrationImportBatchSize)
	scanner := bufio.NewScanner(source)
	scanner.Buffer(make([]byte, 64*1024), migrationMaxJSONLineSize+1)
	var count int64
	flush := func() error {
		if batch.Len() == 0 {
			return nil
		}
		if err := database.Session(&gorm.Session{SkipHooks: true}).CreateInBatches(batch.Interface(), migrationImportBatchSize).Error; err != nil {
			return fmt.Errorf("插入记录: %w", err)
		}
		batch = reflect.MakeSlice(sliceType, 0, migrationImportBatchSize)
		return nil
	}
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 || len(line) > migrationMaxJSONLineSize {
			return 0, fmt.Errorf("%w：JSONL 内容无效", ErrInvalidMigrationArchive)
		}
		record, err := unmarshalMigrationRecord(line, entry.Model, modelSchema)
		if err != nil {
			return 0, fmt.Errorf("%w：记录无法解析", ErrInvalidMigrationArchive)
		}
		batch = reflect.Append(batch, record)
		count++
		if batch.Len() == migrationImportBatchSize {
			if err := flush(); err != nil {
				return 0, err
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return 0, fmt.Errorf("%w：JSONL 行超出限制或损坏", ErrInvalidMigrationArchive)
	}
	if err := flush(); err != nil {
		return 0, err
	}
	return count, nil
}

func repairMigrationSequences(database *gorm.DB, catalog []models.TableCatalogEntry) error {
	if database.Dialector == nil || database.Dialector.Name() != "postgres" {
		return nil
	}
	for _, entry := range catalog {
		var maxID int64
		if err := database.Table(entry.PhysicalName).Select("COALESCE(MAX(id), 0)").Scan(&maxID).Error; err != nil {
			return fmt.Errorf("读取表 %s 的最大主键: %w", entry.ID, err)
		}
		nextID := maxID
		if nextID == 0 {
			nextID = 1
		}
		sequence := entry.PhysicalName + "_id_seq"
		if err := database.Exec("SELECT setval(?::regclass, ?, ?)", sequence, nextID, maxID > 0).Error; err != nil {
			return fmt.Errorf("修复表 %s 的序列: %w", entry.ID, err)
		}
	}
	return nil
}

func migrationTablePath(entry models.TableCatalogEntry) string {
	return migrationDataDirectory + "/" + entry.ID + ".jsonl"
}

func migrationModelType(model any) reflect.Type {
	typeOf := reflect.TypeOf(model)
	for typeOf.Kind() == reflect.Ptr {
		typeOf = typeOf.Elem()
	}
	return typeOf
}

func migrationModelSchema(model any) (*schema.Schema, error) {
	return schema.Parse(model, &sync.Map{}, schema.NamingStrategy{})
}

func migrationRecordMap(record reflect.Value, modelSchema *schema.Schema) map[string]any {
	values := make(map[string]any, len(modelSchema.Fields))
	for _, field := range modelSchema.Fields {
		if field.DBName == "" {
			continue
		}
		value, _ := field.ValueOf(context.Background(), record)
		values[field.DBName] = value
	}
	return values
}

func unmarshalMigrationRecord(line []byte, model any, modelSchema *schema.Schema) (reflect.Value, error) {
	decoder := json.NewDecoder(bytes.NewReader(line))
	var values map[string]json.RawMessage
	if err := decoder.Decode(&values); err != nil {
		return reflect.Value{}, err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return reflect.Value{}, errors.New("JSONL 包含多个值")
	}
	record := reflect.New(migrationModelType(model))
	for _, field := range modelSchema.Fields {
		if field.DBName == "" {
			continue
		}
		raw, exists := values[field.DBName]
		if !exists {
			return reflect.Value{}, fmt.Errorf("缺少列 %s", field.DBName)
		}
		destination := field.ReflectValueOf(context.Background(), record)
		if !destination.CanAddr() {
			return reflect.Value{}, fmt.Errorf("无法写入列 %s", field.DBName)
		}
		if err := json.Unmarshal(raw, destination.Addr().Interface()); err != nil {
			return reflect.Value{}, fmt.Errorf("解析列 %s: %w", field.DBName, err)
		}
		delete(values, field.DBName)
	}
	if len(values) != 0 {
		return reflect.Value{}, errors.New("JSONL 包含未知列")
	}
	return record, nil
}

func isMigrationSHA256(value string) bool {
	if len(value) != sha256.Size*2 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}
