package migrate

import (
	"context"
	"database/sql"
	"embed"
	"fmt"
	"html/template"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"time"

	"qmediasync/internal/db"
	"qmediasync/internal/db/database"
	"qmediasync/internal/helpers"
	"qmediasync/internal/models"

	"github.com/gin-gonic/gin"
	_ "github.com/lib/pq"
)

var migrateFiles embed.FS

func SetMigrateFiles(fs embed.FS) {
	migrateFiles = fs
}

type MigrateServer struct {
	dbManager   *database.EmbeddedManager
	httpServer  *http.Server
	backupPath  string
	isCompleted bool
}

func NewMigrateServer(dbManager *database.EmbeddedManager) *MigrateServer {
	return &MigrateServer{
		dbManager:  dbManager,
		backupPath: filepath.Join(helpers.ConfigDir, "backups", "migrate.zip"),
	}
}

func (s *MigrateServer) Start() error {
	if helpers.IsRelease {
		gin.SetMode(gin.ReleaseMode)
	}
	r := gin.Default()

	data, err := migrateFiles.ReadFile("assets/migrate.html")
	if err != nil {
		return fmt.Errorf("读取迁移模板失败：%v", err)
	}
	tmpl := template.Must(template.New("migrate.html").Parse(string(data)))
	r.SetHTMLTemplate(tmpl)

	r.GET("/", func(c *gin.Context) {
		step := s.getCurrentStep()
		c.HTML(200, "migrate.html", gin.H{
			"title":      "数据库迁移",
			"step":       step,
			"isDocker":   helpers.IsRunningInDocker(),
			"isWindows":  runtime.GOOS == "windows",
			"backupPath": s.backupPath,
		})
	})

	r.GET("/api/step", func(c *gin.Context) {
		step := s.getCurrentStep()
		c.JSON(200, gin.H{
			"step":       step,
			"backupPath": s.backupPath,
		})
	})

	r.POST("/api/backup", s.handleBackup)
	r.POST("/api/test-db", s.handleTestDB)
	r.POST("/api/save-config", s.handleSaveConfig)
	r.GET("/api/backup-status", s.handleBackupStatus)

	s.httpServer = &http.Server{
		Addr:    helpers.GlobalConfig.HttpHost,
		Handler: r,
	}

	fmt.Printf("迁移服务已启动，请在浏览器中访问：http://ip%s\n", helpers.GlobalConfig.HttpHost)
	go func() {
		time.Sleep(1 * time.Second)
		helpers.OpenBrowser("http://127.0.0.1" + helpers.GlobalConfig.HttpHost)
	}()

	if err := s.httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		return fmt.Errorf("启动迁移服务失败：%v", err)
	}

	return nil
}

func (s *MigrateServer) getCurrentStep() int {
	if s.isCompleted {
		return 4
	}
	if helpers.PathExists(s.backupPath) {
		return 3
	}
	return 1
}

func (s *MigrateServer) handleBackup(c *gin.Context) {
	catalog := models.SQLitePostgresMigrationTableCatalog()
	backupDir := filepath.Join(helpers.ConfigDir, "backups")
	if err := os.MkdirAll(backupDir, 0755); err != nil {
		c.JSON(200, gin.H{
			"success": false,
			"error":   "创建备份目录失败：" + err.Error(),
		})
		return
	}
	if !startProgress("backup", "开始迁移备份", len(catalog)) {
		c.JSON(200, gin.H{
			"success": false,
			"error":   "备份任务正在运行",
		})
		return
	}

	go func() {
		if err := s.performMigrateBackup(catalog); err != nil {
			log.Printf("迁移备份失败：%v", err)
		}
	}()

	c.JSON(200, gin.H{
		"success": true,
		"message": "备份任务已启动",
	})
}

func (s *MigrateServer) performMigrateBackup(catalog []models.TableCatalogEntry) (err error) {
	count := 0
	defer func() {
		if err != nil {
			finishProgress("迁移备份失败", err.Error())
			return
		}
		finishProgress("备份完成", "")
	}()
	backupDir := filepath.Join(helpers.ConfigDir, "backups")
	if err := os.MkdirAll(backupDir, 0755); err != nil {
		helpers.AppLogger.Errorf("创建备份目录失败：%v", err)
		return err
	}

	if err := writeMigrationArchive(s.backupPath, db.Db, catalog, func(completed int, description string) {
		count = completed
		updateProgress(description, count, "")
	}); err != nil {
		updateProgress("打包备份失败", count, err.Error())
		return err
	}

	updateProgress("备份完成，正在停止内嵌数据库…", count, "")
	helpers.AppLogger.Infof("迁移备份完成，文件保存到：%s", s.backupPath)

	if s.dbManager != nil {
		if err := s.dbManager.Stop(); err != nil {
			helpers.AppLogger.Warnf("停止内嵌数据库失败：%v", err)
		} else {
			helpers.AppLogger.Info("内嵌数据库已停止")
		}
	}

	return nil
}

func (s *MigrateServer) handleBackupStatus(c *gin.Context) {
	c.JSON(200, CurrentProgress())
}

func (s *MigrateServer) handleTestDB(c *gin.Context) {
	var req testDBRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(400, gin.H{"success": false, "error": err.Error()})
		return
	}
	if err := req.Validate(); err != nil {
		c.JSON(400, gin.H{"success": false, "error": err.Error()})
		return
	}

	connStr := fmt.Sprintf("host=%s port=%d user=%s password=%s dbname=postgres sslmode=disable connect_timeout=5",
		req.Host, req.Port, req.User, req.Password)
	sqlDB, err := sql.Open("postgres", connStr)
	if err != nil {
		c.JSON(200, gin.H{"success": false, "error": "连接失败：" + err.Error()})
		return
	}
	defer sqlDB.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := sqlDB.PingContext(ctx); err != nil {
		c.JSON(200, gin.H{"success": false, "error": "连接失败：" + err.Error()})
		return
	}

	c.JSON(200, gin.H{"success": true, "message": "数据库连接成功"})
}

func (s *MigrateServer) handleSaveConfig(c *gin.Context) {
	var req saveConfigRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(400, gin.H{"error": err.Error()})
		return
	}
	if err := req.Validate(); err != nil {
		c.JSON(400, gin.H{"error": err.Error()})
		return
	}

	helpers.GlobalConfig.Db.PostgresType = helpers.PostgresTypeExternal
	helpers.GlobalConfig.Db.PostgresConfig = helpers.PostgresConfig{
		Host:         req.Host,
		Port:         req.Port,
		User:         req.User,
		Password:     req.Password,
		Database:     req.Database,
		MaxOpenConns: 25,
		MaxIdleConns: 25,
	}

	if err := helpers.SaveConfig(&helpers.GlobalConfig); err != nil {
		c.JSON(500, gin.H{"error": "保存配置失败：" + err.Error()})
		return
	}

	postgresDir := filepath.Join(helpers.ConfigDir, "postgres")
	postgresBackupDir := filepath.Join(helpers.ConfigDir, "postgres-backup")
	if helpers.PathExists(postgresDir) {
		if err := os.Rename(postgresDir, postgresBackupDir); err != nil {
			helpers.AppLogger.Warnf("重命名 PostgreSQL 目录失败：%v", err)
		} else {
			helpers.AppLogger.Info("已将 PostgreSQL 目录重命名为 postgres-backup")
		}
	}

	s.isCompleted = true
	c.JSON(200, gin.H{
		"success": true,
		"message": "配置已保存，程序即将退出，请重新启动",
	})

	go func() {
		time.Sleep(1 * time.Second)
		os.Exit(0)
	}()
}

func (s *MigrateServer) Stop() {
	if s.httpServer != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		s.httpServer.Shutdown(ctx)
	}
}

func ShouldMigrate() bool {
	return helpers.GlobalConfig.Db.Engine == helpers.DbEnginePostgres &&
		helpers.GlobalConfig.Db.PostgresType == helpers.PostgresTypeEmbedded
}

func ShouldRestore() bool {
	backupPath := filepath.Join(helpers.ConfigDir, "backups", "migrate.zip")
	return helpers.PathExists(backupPath) &&
		helpers.GlobalConfig.Db.Engine == helpers.DbEnginePostgres &&
		helpers.GlobalConfig.Db.PostgresType == helpers.PostgresTypeExternal
}

func GetMigrateBackupPath() string {
	return filepath.Join(helpers.ConfigDir, "backups", "migrate.zip")
}
