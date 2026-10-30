package app

import (
	"flag"
	"io/fs"
	"log"
	"os"
	"runtime"

	"qmediasync/internal/helpers"
)

// Options 提供入口注入的构建信息和嵌入资源。
type Options struct {
	Version                 string
	PublishDate             string
	AppName                 string
	FanartAPIKey            string
	TMDBAccessToken         string
	TMDBAPIKey              string
	SCAPIKey                string
	OAuthRelayEncryptionKey string
	Resources               fs.FS
}

var build Options

var Update bool = false

func parseParams() {
	var update string
	flag.String("guid", "", "兼容旧部署参数；进程身份由启动环境决定")
	flag.BoolVar(&helpers.IsFnOS, "fnos", false, "是否是飞牛环境")
	flag.StringVar(&update, "update", "", "更新参数")
	registerAdminRecoveryFlags(flag.CommandLine, &adminRecoveryOptions{})
	// 解析命令行参数
	flag.Parse()
	// 使用参数
	if helpers.IsFnOS {
		log.Printf("当前环境为飞牛环境\n")
	}
	// 检查是否是更新模式
	if update != "" && runtime.GOOS == "windows" {
		Update = true
	}
}

// Run 按现有顺序执行命令分派、初始化和应用生命周期。
func Run(options Options) {
	build = options
	if len(os.Args) > 1 && os.Args[1] == "--restart-parent" {
		if err := runRestartProcess(os.Args[2:]); err != nil {
			log.Printf("重启失败：%v", err)
			os.Exit(1)
		}
		return
	}
	if handled, code := runAdminRecoveryCommand(os.Args[1:]); handled {
		os.Exit(code)
	}
	parseParams()
	getRootDir()
	if Update {
		if err := runUpdateProcess(); err != nil {
			log.Printf("更新失败：%v", err)
			os.Exit(1)
		}
		return
	}
	defer func() {
		if instanceLock != nil {
			if err := instanceLock.Close(); err != nil {
				log.Printf("释放实例锁失败：%v", err)
			}
		}
	}()
	if !initEnv() {
		panic("初始化环境失败")
	}
	if runtime.GOOS == "windows" {
		if helpers.IsRelease {
			go QMSApp.Start()
			helpers.StartApp(func() {
				QMSApp.Stop()
			})
		} else {
			QMSApp.Start()
		}
	} else {
		QMSApp.Start()
	}
}
