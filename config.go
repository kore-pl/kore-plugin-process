package main

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/pengtang1024/kore/pkg/pluginapi"
)

// embeddedInfo 是随二进制发布的内嵌清单：只含握手字段，字节稳定。
// 刻意不内嵌 info.json——其 size_bytes 会被打包脚本回填，若内嵌会使二进制
// 随清单自指震荡，破坏 make dist 的 sha 确定性（manifest.json 提交态固定）。
//
//go:embed manifest.json
var embeddedInfo []byte

// manifest 是 info.json 中插件运行期真正需要的字段，其余字段归内核校验。
type manifest struct {
	ID           string   `json:"id"`
	Version      string   `json:"version"`
	Protocol     string   `json:"protocol"`
	RequiresKore string   `json:"requires_kore"`
	Capabilities []string `json:"capabilities"`
}

// config 是插件运行期配置：环境变量 + 内嵌清单，不读任何配置文件。
type config struct {
	socket        string
	id            string
	token         string
	version       string
	requiresKore  string
	capabilities  []string
	maskArgs      bool
	protectedPIDs []int
	startedAt     time.Time
}

// loadConfig 读取协议环境变量并解析内嵌 info.json，校验协议主版本。
func loadConfig() (config, error) {
	var m manifest
	if err := json.Unmarshal(embeddedInfo, &m); err != nil {
		return config{}, fmt.Errorf("解析内嵌 info.json: %w", err)
	}
	if m.Protocol != pluginapi.Protocol {
		return config{}, fmt.Errorf("info.json.protocol=%q 与 SDK 协议 %q 不一致", m.Protocol, pluginapi.Protocol)
	}
	maskArgs, err := parseMaskArgs(os.Getenv("KORE_PROCESS_MASK_ARGS"))
	if err != nil {
		return config{}, err
	}
	protectedPIDs, err := parseProtectedPIDs(os.Getenv("KORE_PROTECTED_PIDS"))
	if err != nil {
		return config{}, err
	}
	cfg := config{
		socket:        os.Getenv("KORE_PLUGIN_SOCK"),
		id:            os.Getenv("KORE_PLUGIN_ID"),
		token:         os.Getenv("KORE_PLUGIN_TOKEN"),
		version:       m.Version,
		requiresKore:  m.RequiresKore,
		capabilities:  m.Capabilities,
		maskArgs:      maskArgs,
		protectedPIDs: protectedPIDs,
		startedAt:     time.Now(),
	}
	if cfg.id == "" {
		cfg.id = m.ID
	}
	if cfg.socket == "" {
		return config{}, fmt.Errorf("缺少环境变量 KORE_PLUGIN_SOCK")
	}
	return cfg, nil
}

// parseMaskArgs 解析 KORE_PROCESS_MASK_ARGS：缺省 true；只接受 true/false 族取值。
func parseMaskArgs(raw string) (bool, error) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "", "1", "true", "yes", "on":
		return true, nil
	case "0", "false", "no", "off":
		return false, nil
	default:
		return false, fmt.Errorf("KORE_PROCESS_MASK_ARGS 只接受 true/false（实际 %q）", raw)
	}
}

// parseProtectedPIDs 解析 KORE_PROTECTED_PIDS（逗号分隔的正整数）；空值返回 nil。
func parseProtectedPIDs(raw string) ([]int, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, nil
	}
	seen := make(map[int]bool)
	pids := make([]int, 0, 4)
	for _, token := range strings.Split(raw, ",") {
		token = strings.TrimSpace(token)
		if token == "" {
			continue
		}
		pid, err := strconv.Atoi(token)
		if err != nil || pid <= 0 {
			return nil, fmt.Errorf("KORE_PROTECTED_PIDS 含非法 pid %q", token)
		}
		if !seen[pid] {
			seen[pid] = true
			pids = append(pids, pid)
		}
	}
	return pids, nil
}
