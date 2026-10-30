package app

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"html/template"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"qmediasync/internal/db/database"
	"qmediasync/internal/helpers"

	"github.com/gin-gonic/gin"
)

func isInRestrictedDirectory() (bool, string) {
	if runtime.GOOS != "windows" {
		return false, ""
	}

	exePath, err := os.Executable()
	if err != nil {
		return false, ""
	}
	exeDir := filepath.Dir(exePath)

	driveLetter := strings.ToUpper(string(exeDir[0]))
	log.Printf("应用程序路径：%s，盘符：%s", exePath, driveLetter)
	if driveLetter == "C" {
		return true, "应用程序位于 C 盘，建议将应用程序移动到其他盘符（如 D 盘、E 盘等）以避免权限问题"
	}

	restrictedPaths := []string{
		"Program Files",
		"Program Files (x86)",
		"ProgramData",
		"Windows",
	}

	for _, restrictedPath := range restrictedPaths {
		log.Printf("检查目录：%s，是否包含受限路径：%s", exeDir, restrictedPath)
		if strings.Contains(exeDir, restrictedPath) {
			return true, fmt.Sprintf("应用程序位于受限目录 %s 中，建议将应用程序移动到普通用户目录或其他非系统目录", restrictedPath)
		}
	}

	return false, ""
}

type databaseConfigRequest struct {
	Engine       helpers.DbEngine     `json:"engine"`
	PostgresType helpers.PostgresType `json:"postgresType"` // 仅识别旧客户端提交的模式
	Host         string               `json:"host"`
	Port         int                  `json:"port"`
	User         string               `json:"user"`
	Password     string               `json:"password"`
	Database     string               `json:"database"`
	SSL          bool                 `json:"ssl"`
	DropDatabase bool                 `json:"dropDatabase"`
}

func (req databaseConfigRequest) toConfig() (*helpers.Config, error) {
	config := helpers.MakeDefaultConfig()
	config.Db.Engine = req.Engine
	config.Db.PostgresType = req.PostgresType
	if req.Engine == helpers.DbEnginePostgres {
		config.Db.PostgresConfig = helpers.PostgresConfig{
			Host:         req.Host,
			Port:         req.Port,
			User:         req.User,
			Password:     req.Password,
			Database:     req.Database,
			SSL:          req.SSL,
			MaxOpenConns: 25,
			MaxIdleConns: 25,
		}
	}
	if err := config.Db.Validate(); err != nil {
		return nil, err
	}
	return config, nil
}

func newConfigRouter(resources fs.FS, prepareRestart func() (bool, error), exit func()) *gin.Engine {
	if helpers.IsRelease {
		gin.SetMode(gin.ReleaseMode)
	}
	r := gin.Default()
	// 使用入口提供的资源加载模板
	data, err := fs.ReadFile(resources, "assets/db_config.html")
	if err != nil {
		log.Fatal(err)
	}
	// 创建内存中的 HTML 文件
	tmpl := template.Must(template.New("db_config.html").Parse(string(data)))
	r.SetHTMLTemplate(tmpl)

	r.GET("/", func(c *gin.Context) {
		isRestricted, warningMsg := isInRestrictedDirectory()
		c.HTML(200, "db_config.html", gin.H{
			"title":        "数据库配置",
			"isRestricted": isRestricted,
			"warningMsg":   warningMsg,
			"isWindows":    runtime.GOOS == "windows",
		})
	})

	r.POST("/api/config/test-db", func(c *gin.Context) {
		var req struct {
			Host     string `json:"host"`
			Port     int    `json:"port"`
			User     string `json:"user"`
			Password string `json:"password"`
			Database string `json:"database"`
			SSL      bool   `json:"ssl"`
		}
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(400, gin.H{"success": false, "error": err.Error()})
			return
		}

		sslMode := "disable"
		if req.SSL {
			sslMode = "require"
		}
		connStr := fmt.Sprintf("host=%s port=%d user=%s password=%s dbname=postgres sslmode=%s",
			req.Host, req.Port, req.User, req.Password, sslMode)
		sqlDB, err := sql.Open("pgx", connStr)
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

		var dbExists bool
		err = sqlDB.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM pg_database WHERE datname = $1)", req.Database).Scan(&dbExists)
		if err != nil {
			c.JSON(200, gin.H{"success": false, "error": "检查数据库失败：" + err.Error()})
			return
		}

		if !dbExists {
			c.JSON(200, gin.H{"success": true, "message": "数据库连接成功", "dbExists": false})
			return
		}

		connStrDb := fmt.Sprintf("host=%s port=%d user=%s password=%s dbname=%s sslmode=%s",
			req.Host, req.Port, req.User, req.Password, req.Database, sslMode)
		sqlDBDb, err := sql.Open("pgx", connStrDb)
		if err != nil {
			c.JSON(200, gin.H{"success": true, "message": "数据库连接成功", "dbExists": true, "hasOtherTables": false})
			return
		}
		defer sqlDBDb.Close()

		var tableCount int
		err = sqlDBDb.QueryRowContext(ctx, "SELECT COUNT(*) FROM information_schema.tables WHERE table_schema = 'public' AND table_name NOT LIKE 'gorr_%'").Scan(&tableCount)
		if err != nil {
			c.JSON(200, gin.H{"success": true, "message": "数据库连接成功", "dbExists": true, "hasOtherTables": false})
			return
		}

		c.JSON(200, gin.H{"success": true, "message": "数据库连接成功", "dbExists": true, "hasOtherTables": tableCount > 0})
	})

	var saveMu sync.Mutex
	var configSaved, restartPrepared bool
	var clearedDatabaseConfig *helpers.Config
	var savedMessage string
	r.POST("/api/config/save", func(c *gin.Context) {
		saveMu.Lock()
		defer saveMu.Unlock()
		if restartPrepared {
			c.JSON(200, gin.H{"success": true, "message": savedMessage})
			return
		}
		if !configSaved {
			yamlConfig := clearedDatabaseConfig
			var req databaseConfigRequest
			if yamlConfig == nil {
				if err := c.ShouldBindJSON(&req); err != nil {
					c.JSON(400, gin.H{"error": err.Error()})
					return
				}
				var err error
				yamlConfig, err = req.toConfig()
				if err != nil {
					c.JSON(400, gin.H{"error": err.Error()})
					return
				}
			}
			if err := checkLegacyDatabaseState(); err != nil {
				c.JSON(409, gin.H{"error": err.Error()})
				return
			}
			if clearedDatabaseConfig == nil && req.Engine == helpers.DbEnginePostgres {
				quotedDatabase, err := database.QuotePostgresIdentifier(req.Database)
				if err != nil {
					c.JSON(200, gin.H{"error": "数据库名不合法：" + err.Error()})
					return
				}
				if req.DropDatabase {
					sslMode := "disable"
					if req.SSL {
						sslMode = "require"
					}
					connStr := fmt.Sprintf("host=%s port=%d user=%s password=%s dbname=postgres sslmode=%s",
						req.Host, req.Port, req.User, req.Password, sslMode)
					sqlDB, err := sql.Open("pgx", connStr)
					if err != nil {
						c.JSON(200, gin.H{"error": "连接数据库失败：" + err.Error()})
						return
					}
					ctx, cancel := context.WithTimeout(c.Request.Context(), 10*time.Second)
					_, dropErr := sqlDB.ExecContext(ctx, "DROP DATABASE IF EXISTS "+quotedDatabase)
					cancel()
					if dropErr == nil {
						// 清库后固定本次设置，落盘失败的重试不能再次删除数据库。
						clearedDatabaseConfig = yamlConfig
					}
					if err := errors.Join(dropErr, sqlDB.Close()); err != nil {
						c.JSON(200, gin.H{"error": "清空数据库失败：" + err.Error()})
						return
					}
				}
			}

			if err := helpers.SaveConfig(yamlConfig); err != nil {
				message := "保存配置失败：" + err.Error()
				if clearedDatabaseConfig != nil {
					message = "数据库已清空，但" + message + "。修复后再次保存将沿用本次数据库设置。"
				}
				c.JSON(500, gin.H{"error": message})
				return
			}

			configSaved = true
		}

		autoRestart, err := prepareRestart()
		if err != nil {
			log.Printf("配置已保存，但准备自动启动失败：%v", err)
			c.JSON(200, gin.H{
				"success":        true,
				"restartPending": true,
				"message":        "配置已保存，但未能自动启动。可点击保存重试；仍失败时，请按当前部署方式停止后重新启动 QMediaSync。启动完成后，请查看日志中的初始化码，刷新此页面创建管理员账号。",
			})
			return
		}
		message := "配置已保存，配置服务即将退出。请按当前部署方式重启服务；启动完成后，请查看启动日志中的初始化码，手动刷新当前页面，使用初始化码创建首个管理员。"
		if autoRestart {
			message = "配置已保存，正在启动 QMediaSync。启动完成后，请查看日志中的初始化码，刷新此页面创建管理员账号。"
		}
		restartPrepared = true
		savedMessage = message
		c.JSON(200, gin.H{"success": true, "message": message})
		go exit()
	})

	return r
}

func StartConfigWebServer() {
	r := newConfigRouter(build.Resources, helpers.PrepareInitialConfigRestart, func() {
		time.Sleep(time.Second)
		os.Exit(0)
	})

	fmt.Printf("配置服务已启动，请在浏览器中访问：http://ip:12333\n")
	go func() {
		// 第一次启动建议多等一会儿，因为数据库初始化需要时间
		time.Sleep(2 * time.Second)
		helpers.OpenBrowser("http://127.0.0.1:12333")
	}()
	if err := r.Run(":12333"); err != nil {
		log.Fatalf("启动配置服务失败：%v", err)
	}
}
