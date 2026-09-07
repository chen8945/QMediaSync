package helpers

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"gopkg.in/yaml.v2"
)

const testSQLiteConfig = "db:\n  engine: sqlite\n  sqliteFile: qmediasync.db\n"

func TestInitConfigReadsConfigYaml(t *testing.T) {
	withTempConfigDir(t, func(configDir string) {
		writeTestConfig(t, filepath.Join(configDir, "config.yaml"), "from-yaml")

		if err := InitConfig(); err != nil {
			t.Fatalf("InitConfig() error = %v", err)
		}

		if GlobalConfig.JwtSecret != "from-yaml" {
			t.Fatalf("GlobalConfig.JwtSecret = %q, want %q", GlobalConfig.JwtSecret, "from-yaml")
		}
	})
}

func TestInitConfigFallsBackToConfigYml(t *testing.T) {
	withTempConfigDir(t, func(configDir string) {
		writeTestConfig(t, filepath.Join(configDir, "config.yml"), "from-yml")

		if err := InitConfig(); err != nil {
			t.Fatalf("InitConfig() error = %v", err)
		}

		if GlobalConfig.JwtSecret != "from-yml" {
			t.Fatalf("GlobalConfig.JwtSecret = %q, want %q", GlobalConfig.JwtSecret, "from-yml")
		}
	})
}

func TestInitConfigPrefersConfigYamlOverConfigYml(t *testing.T) {
	withTempConfigDir(t, func(configDir string) {
		writeTestConfig(t, filepath.Join(configDir, "config.yaml"), "from-yaml")
		writeTestConfig(t, filepath.Join(configDir, "config.yml"), "from-yml")

		if err := InitConfig(); err != nil {
			t.Fatalf("InitConfig() error = %v", err)
		}

		if GlobalConfig.JwtSecret != "from-yaml" {
			t.Fatalf("GlobalConfig.JwtSecret = %q, want %q", GlobalConfig.JwtSecret, "from-yaml")
		}
	})
}

func TestExistingConfigFilePathDefaultsToConfigYaml(t *testing.T) {
	withTempConfigDir(t, func(configDir string) {
		got := ExistingConfigFilePath()
		want := filepath.Join(configDir, "config.yaml")

		if got != want {
			t.Fatalf("ExistingConfigFilePath() = %q, want %q", got, want)
		}
	})
}

func TestSaveConfigWritesConfigYaml(t *testing.T) {
	withTempConfigDir(t, func(configDir string) {
		cfg := MakeDefaultConfig()
		cfg.JwtSecret = "saved-yaml"

		if err := SaveConfig(cfg); err != nil {
			t.Fatalf("SaveConfig() error = %v", err)
		}

		if !PathExists(filepath.Join(configDir, "config.yaml")) {
			t.Fatal("SaveConfig() did not create config.yaml")
		}
		if PathExists(filepath.Join(configDir, "config.yml")) {
			t.Fatal("SaveConfig() created legacy config.yml")
		}
	})
}

func TestMakeDefaultConfigDisablesEmby302InsecureSkipVerify(t *testing.T) {
	cfg := MakeDefaultConfig()
	if cfg.Emby302.InsecureSkipVerify {
		t.Fatal("Emby302.InsecureSkipVerify = true, want false")
	}
}

func TestMakeDefaultConfigLogLevelDefaultsInfo(t *testing.T) {
	cfg := MakeDefaultConfig()
	if cfg.Log.Level != "info" {
		t.Fatalf("Log.Level = %q, want info", cfg.Log.Level)
	}
}

func TestMakeDefaultConfigLogRotationDefaults(t *testing.T) {
	cfg := MakeDefaultConfig()
	if cfg.Log.App != "logs/app.log" {
		t.Fatalf("Log.App = %q, want logs/app.log", cfg.Log.App)
	}
	if cfg.Log.MaxSizeMB != 10 {
		t.Fatalf("Log.MaxSizeMB = %d, want 10", cfg.Log.MaxSizeMB)
	}
	if cfg.Log.MaxBackups != 3 {
		t.Fatalf("Log.MaxBackups = %d, want 3", cfg.Log.MaxBackups)
	}
	if cfg.Log.MaxAgeDays != 7 {
		t.Fatalf("Log.MaxAgeDays = %d, want 7", cfg.Log.MaxAgeDays)
	}
}

func TestMakeDefaultConfigDoesNotContainAdminCredentials(t *testing.T) {
	cfg := MakeDefaultConfig()
	data, err := yaml.Marshal(cfg)
	if err != nil {
		t.Fatalf("Marshal default config: %v", err)
	}
	text := string(data)
	if strings.Contains(text, "adminUsername") || strings.Contains(text, "adminPassword") {
		t.Fatalf("默认配置不应包含管理员字段: %s", text)
	}
}

func TestInitConfigReadsEmby302InsecureSkipVerify(t *testing.T) {
	withTempConfigDir(t, func(configDir string) {
		data := []byte(testSQLiteConfig + "jwtSecret: custom-secret\nemby302:\n  insecure_skip_verify: true\n")
		if err := os.WriteFile(filepath.Join(configDir, "config.yaml"), data, 0644); err != nil {
			t.Fatalf("write test config: %v", err)
		}

		if err := InitConfig(); err != nil {
			t.Fatalf("InitConfig() error = %v", err)
		}
		if !GlobalConfig.Emby302.InsecureSkipVerify {
			t.Fatal("Emby302.InsecureSkipVerify = false, want true")
		}
	})
}

func TestInitConfigDefaultsMissingLogLevelToInfo(t *testing.T) {
	withTempConfigDir(t, func(configDir string) {
		data := []byte(testSQLiteConfig + "jwtSecret: custom-secret\nlog:\n  file: logs/app.log\n")
		if err := os.WriteFile(filepath.Join(configDir, "config.yaml"), data, 0644); err != nil {
			t.Fatalf("write test config: %v", err)
		}

		if err := InitConfig(); err != nil {
			t.Fatalf("InitConfig() error = %v", err)
		}
		if GlobalConfig.Log.Level != "info" {
			t.Fatalf("Log.Level = %q, want info", GlobalConfig.Log.Level)
		}
	})
}

func TestInitConfigNormalizesInvalidLogLevelToInfo(t *testing.T) {
	withTempConfigDir(t, func(configDir string) {
		data := []byte(testSQLiteConfig + "jwtSecret: custom-secret\nlog:\n  level: verbose\n")
		if err := os.WriteFile(filepath.Join(configDir, "config.yaml"), data, 0644); err != nil {
			t.Fatalf("write test config: %v", err)
		}

		if err := InitConfig(); err != nil {
			t.Fatalf("InitConfig() error = %v", err)
		}
		if GlobalConfig.Log.Level != "info" {
			t.Fatalf("Log.Level = %q, want info", GlobalConfig.Log.Level)
		}
	})
}

func TestInitConfigMigratesLegacyLogFileToApp(t *testing.T) {
	withTempConfigDir(t, func(configDir string) {
		data := []byte(testSQLiteConfig + "jwtSecret: custom-secret\nlog:\n  file: logs/custom-app.log\n  level: warn\n")
		if err := os.WriteFile(filepath.Join(configDir, "config.yaml"), data, 0644); err != nil {
			t.Fatalf("write test config: %v", err)
		}

		if err := InitConfig(); err != nil {
			t.Fatalf("InitConfig() error = %v", err)
		}
		if GlobalConfig.Log.App != "logs/custom-app.log" {
			t.Fatalf("Log.App = %q, want logs/custom-app.log", GlobalConfig.Log.App)
		}
		if GlobalConfig.Log.Level != "warn" {
			t.Fatalf("Log.Level = %q, want warn", GlobalConfig.Log.Level)
		}
	})
}

func TestInitConfigNormalizesInvalidLogRotationToDefaults(t *testing.T) {
	withTempConfigDir(t, func(configDir string) {
		data := []byte(testSQLiteConfig + "jwtSecret: custom-secret\nlog:\n  app: logs/app.log\n  maxSizeMB: 0\n  maxBackups: 101\n  maxAgeDays: 366\n")
		if err := os.WriteFile(filepath.Join(configDir, "config.yaml"), data, 0644); err != nil {
			t.Fatalf("write test config: %v", err)
		}

		if err := InitConfig(); err != nil {
			t.Fatalf("InitConfig() error = %v", err)
		}
		if GlobalConfig.Log.MaxSizeMB != 10 || GlobalConfig.Log.MaxBackups != 3 || GlobalConfig.Log.MaxAgeDays != 7 {
			t.Fatalf("日志轮转参数未恢复默认值: %+v", GlobalConfig.Log)
		}
	})
}

func TestSaveLogSetting保存配置并更新运行时设置(t *testing.T) {
	withTempConfigDir(t, func(configDir string) {
		GlobalConfig = *MakeDefaultConfig()
		SetGlobalLogLevel(LogLevelInfo)
		if err := SaveConfig(&GlobalConfig); err != nil {
			t.Fatalf("保存初始配置失败: %v", err)
		}

		next := LogSetting{
			Level:      LogLevelWarn,
			MaxSizeMB:  20,
			MaxBackups: 5,
			MaxAgeDays: 14,
		}
		if err := SaveLogSetting(next); err != nil {
			t.Fatalf("SaveLogSetting() error = %v", err)
		}

		if ConfiguredLogLevel() != LogLevelWarn {
			t.Fatalf("ConfiguredLogLevel() = %s, want warn", ConfiguredLogLevel().String())
		}
		if GlobalConfig.Log.Level != "warn" || GlobalConfig.Log.MaxSizeMB != 20 || GlobalConfig.Log.MaxBackups != 5 || GlobalConfig.Log.MaxAgeDays != 14 {
			t.Fatalf("GlobalConfig.Log 未更新: %+v", GlobalConfig.Log)
		}
		saved, err := os.ReadFile(filepath.Join(configDir, "config.yaml"))
		if err != nil {
			t.Fatalf("读取保存后的配置失败: %v", err)
		}
		text := string(saved)
		for _, want := range []string{"level: warn", "maxSizeMB: 20", "maxBackups: 5", "maxAgeDays: 14", "app: logs/app.log"} {
			if !strings.Contains(text, want) {
				t.Fatalf("配置文件缺少 %q: %s", want, text)
			}
		}
		if strings.Contains(text, "\n  file:") {
			t.Fatalf("保存后的推荐配置不应继续写入 log.file: %s", text)
		}
	})
}

func TestSaveLogSettingConcurrentReadsRaceFree(t *testing.T) {
	withTempConfigDir(t, func(configDir string) {
		GlobalConfig = *MakeDefaultConfig()
		SetGlobalLogLevel(LogLevelInfo)
		if err := SaveConfig(&GlobalConfig); err != nil {
			t.Fatalf("保存初始配置失败: %v", err)
		}

		var wg sync.WaitGroup
		wg.Add(2)

		go func() {
			defer wg.Done()
			for i := range 100 {
				level := LogLevelInfo
				if i%2 == 0 {
					level = LogLevelWarn
				}
				if err := SaveLogSetting(LogSetting{
					Level:      level,
					MaxSizeMB:  10 + i%3,
					MaxBackups: 3 + i%3,
					MaxAgeDays: 7 + i%3,
				}); err != nil {
					t.Errorf("SaveLogSetting() error = %v", err)
					return
				}
			}
		}()

		go func() {
			defer wg.Done()
			for range 1000 {
				_, _, _ = configuredLogRotation()
				_ = SyncLogDir()
			}
		}()

		wg.Wait()
	})
}

func TestEnsureJWTSecretReplacesEmptyAndDefault(t *testing.T) {
	cases := []struct {
		name      string
		secret    string
		wantRenew bool
	}{
		{name: "空值", secret: "", wantRenew: true},
		{name: "公开默认值", secret: DefaultJWTSecret, wantRenew: true},
		{name: "旧公开默认值", secret: "Q115-STRM-JWT-TOKEN-250706", wantRenew: true},
		{name: "自定义值", secret: "custom-secret-value", wantRenew: false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			withTempConfigDir(t, func(configDir string) {
				GlobalConfig = *MakeDefaultConfig()
				GlobalConfig.JwtSecret = tc.secret

				if err := EnsureJWTSecret(); err != nil {
					t.Fatalf("EnsureJWTSecret() error = %v", err)
				}
				if GlobalConfig.JwtSecret == "" || GlobalConfig.JwtSecret == DefaultJWTSecret {
					t.Fatalf("JwtSecret 未替换为强随机值: %q", GlobalConfig.JwtSecret)
				}
				if !tc.wantRenew && GlobalConfig.JwtSecret != tc.secret {
					t.Fatalf("自定义 JwtSecret 被错误替换: %q", GlobalConfig.JwtSecret)
				}
				if tc.wantRenew && len(GlobalConfig.JwtSecret) < 43 {
					t.Fatalf("JwtSecret 长度过短: %d", len(GlobalConfig.JwtSecret))
				}
			})
		})
	}
}

func withTempConfigDir(t *testing.T, run func(configDir string)) {
	t.Helper()

	oldConfigDir := ConfigDir
	oldGlobalConfig := GlobalConfig
	oldLogLevel := ConfiguredLogLevel()
	t.Cleanup(func() {
		ConfigDir = oldConfigDir
		GlobalConfig = oldGlobalConfig
		SetGlobalLogLevel(oldLogLevel)
	})

	ConfigDir = t.TempDir()
	GlobalConfig = Config{}
	run(ConfigDir)
}

func writeTestConfig(t *testing.T, path, jwtSecret string) {
	t.Helper()

	data := []byte(testSQLiteConfig + "jwtSecret: " + jwtSecret + "\n")
	if err := os.WriteFile(path, data, 0644); err != nil {
		t.Fatalf("write test config %s: %v", path, err)
	}
}

func TestDatabaseConfigCompatibility(t *testing.T) {
	for _, tc := range []struct {
		name      string
		engine    DbEngine
		mode      PostgresType
		wantError bool
	}{
		{name: "SQLite", engine: DbEngineSqlite},
		{name: "legacy SQLite embedded field", engine: DbEngineSqlite, mode: PostgresTypeEmbedded},
		{name: "PostgreSQL without mode", engine: DbEnginePostgres},
		{name: "legacy external PostgreSQL", engine: DbEnginePostgres, mode: PostgresTypeExternal},
		{name: "embedded PostgreSQL", engine: DbEnginePostgres, mode: PostgresTypeEmbedded, wantError: true},
		{name: "unknown PostgreSQL mode", engine: DbEnginePostgres, mode: "unknown", wantError: true},
		{name: "unknown engine", engine: "mysql", wantError: true},
		{name: "missing engine", wantError: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			withTempConfigDir(t, func(configDir string) {
				cfg := MakeDefaultConfig()
				cfg.Db.Engine = tc.engine
				cfg.Db.PostgresType = tc.mode
				cfg.Db.PostgresConfig.Host = "localhost"
				cfg.Db.PostgresConfig.User = "postgres"
				cfg.Db.PostgresConfig.Database = "qmediasync"
				before, err := yaml.Marshal(cfg)
				if err != nil {
					t.Fatal(err)
				}
				path := filepath.Join(configDir, ConfigFileName)
				if err := os.WriteFile(path, before, 0o600); err != nil {
					t.Fatal(err)
				}
				// 读取新配置不能沿用上一次加载的有效引擎。
				GlobalConfig = *MakeDefaultConfig()
				err = InitConfig()
				if (err != nil) != tc.wantError {
					t.Fatalf("InitConfig() error = %v, wantError %v", err, tc.wantError)
				}
				if tc.wantError {
					if err := SaveConfig(cfg); err == nil {
						t.Fatal("SaveConfig accepted an unsupported database configuration")
					}
					after, err := os.ReadFile(path)
					if err != nil || !bytes.Equal(before, after) {
						t.Fatal("rejected configuration was rewritten or received a new JWT secret")
					}
					return
				}
				if GlobalConfig.Db.Engine != tc.engine {
					t.Fatalf("loaded engine = %q, want %q", GlobalConfig.Db.Engine, tc.engine)
				}
				after, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				if bytes.Contains(after, []byte("postgresType:")) {
					t.Fatal("newly saved configuration contains the legacy PostgreSQL mode")
				}
				if GlobalConfig.Db.PostgresType != tc.mode {
					t.Fatal("saving mutated the in-memory legacy mode")
				}
				if err := LoadExistingConfig(); err != nil {
					t.Fatalf("reload saved configuration: %v", err)
				}
			})
		})
	}
}

func TestDefaultDatabaseConfigUsesPostgres(t *testing.T) {
	cfg := MakeDefaultConfig()
	if cfg.Db.Engine != DbEnginePostgres || cfg.Db.SqliteFile == "" || cfg.Db.PostgresType != "" {
		t.Fatalf("default database config = %+v", cfg.Db)
	}
	want := PostgresConfig{
		Host: "localhost", Port: 5432, User: "qms", Password: "qms123456", Database: "qms",
		MaxOpenConns: 25, MaxIdleConns: 25,
	}
	if cfg.Db.PostgresConfig != want {
		t.Fatal("default PostgreSQL connection settings changed")
	}
	if err := cfg.Db.Validate(); err != nil {
		t.Fatalf("default database config is invalid: %v", err)
	}
}
