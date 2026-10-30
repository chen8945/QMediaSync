package config

import (
	"fmt"
	"strings"

	"qmediasync/internal/emby302/util/logs"
)

type Path struct {
	// Emby2Openlist 将 Emby 路径前缀映射到 OpenList 路径前缀, 两个路径使用 : 分隔
	Emby2Openlist []string `yaml:"emby2openlist"`

	// emby2OpenlistArr 根据 Emby2Openlist 转换成路径键值对数组
	emby2OpenlistArr [][2]string
}

func (p *Path) Init() error {
	p.emby2OpenlistArr = make([][2]string, 0, len(p.Emby2Openlist))
	for _, e2a := range p.Emby2Openlist {
		arr := strings.Split(e2a, ":")
		if len(arr) != 2 {
			return fmt.Errorf("path.emby2openlist 配置错误, %s 无法根据 ':' 进行分割", e2a)
		}
		p.emby2OpenlistArr = append(p.emby2OpenlistArr, [2]string{arr[0], arr[1]})
	}
	return nil
}

// MapEmby2Openlist 将 Emby 路径映射成 OpenList 路径
func (p *Path) MapEmby2Openlist(embyPath string) (string, bool) {
	for _, cfg := range p.emby2OpenlistArr {
		ep, ap := cfg[0], cfg[1]
		if strings.HasPrefix(embyPath, ep) {
			logs.Tip("命中 emby2openlist 路径映射: %s => %s (如果命中结果不符合预期, 请将正确的映射配置前移)", ep, ap)
			return strings.Replace(embyPath, ep, ap, 1), true
		}
	}
	return "", false
}
