package procfs

import (
	"fmt"
	"strconv"
	"strings"
)

// statInfo 是 /proc/<pid>/stat 中本插件需要的字段。
type statInfo struct {
	Name       string
	State      byte
	PPID       int
	UTicks     uint64
	STicks     uint64
	StartTicks uint64
}

// statusInfo 是 /proc/<pid>/status 中本插件需要的字段。
type statusInfo struct {
	UID       uint32
	HasUID    bool
	State     byte
	VmRSSKiB  int64
	VmPeakKiB int64
	Threads   int
}

// parseStat 解析 /proc/<pid>/stat：comm 可能包含空格与括号，必须按首个 '(' 与末个 ')' 切分。
func parseStat(data string) (statInfo, error) {
	open := strings.IndexByte(data, '(')
	closeAt := strings.LastIndexByte(data, ')')
	if open < 0 || closeAt <= open {
		return statInfo{}, fmt.Errorf("stat 格式非法：缺少 comm 字段")
	}
	info := statInfo{Name: data[open+1 : closeAt]}
	fields := strings.Fields(data[closeAt+1:])
	// fields[0] 是第 3 字段 state；utime/stime/starttime 分别是第 14/15/22 字段，即索引 11/12/19。
	if len(fields) < 20 {
		return statInfo{}, fmt.Errorf("stat 字段数 %d 少于 20", len(fields))
	}
	if len(fields[0]) != 1 {
		return statInfo{}, fmt.Errorf("stat state 字段非法: %q", fields[0])
	}
	info.State = fields[0][0]
	ppid, err := strconv.Atoi(fields[1])
	if err != nil {
		return statInfo{}, fmt.Errorf("stat ppid 解析失败: %w", err)
	}
	info.PPID = ppid
	if info.UTicks, err = parseUintField(fields[11], "utime"); err != nil {
		return statInfo{}, err
	}
	if info.STicks, err = parseUintField(fields[12], "stime"); err != nil {
		return statInfo{}, err
	}
	if info.StartTicks, err = parseUintField(fields[19], "starttime"); err != nil {
		return statInfo{}, err
	}
	return info, nil
}

// parseUintField 解析无符号十进制字段，错误信息带字段名便于定位。
func parseUintField(raw, name string) (uint64, error) {
	value, err := strconv.ParseUint(raw, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("stat %s 解析失败: %w", name, err)
	}
	return value, nil
}

// parseStatus 解析 /proc/<pid>/status 的 Uid / State / VmRSS / VmPeak / Threads。
// 字段缺失不报错（内核线程可能没有 Vm*），由调用方按零值处理。
func parseStatus(data string) statusInfo {
	info := statusInfo{}
	for _, line := range strings.Split(data, "\n") {
		key, value, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		value = strings.TrimSpace(value)
		switch key {
		case "State":
			if value != "" {
				info.State = value[0]
			}
		case "Uid":
			if uid, err := strconv.ParseUint(strings.Fields(value)[0], 10, 32); err == nil {
				info.UID, info.HasUID = uint32(uid), true
			}
		case "VmRSS":
			info.VmRSSKiB = firstKiB(value)
		case "VmPeak":
			info.VmPeakKiB = firstKiB(value)
		case "Threads":
			if threads, err := strconv.Atoi(strings.Fields(value)[0]); err == nil {
				info.Threads = threads
			}
		}
	}
	if info.VmRSSKiB == 0 {
		info.VmRSSKiB = 0
	}
	return info
}

// firstKiB 取 "1234 kB" 形式的第一个数值（单位固定为 KiB）。
func firstKiB(value string) int64 {
	fields := strings.Fields(value)
	if len(fields) == 0 {
		return 0
	}
	parsed, err := strconv.ParseInt(fields[0], 10, 64)
	if err != nil {
		return 0
	}
	return parsed
}

// splitCmdline 把 /proc/<pid>/cmdline 的 NUL 分隔字节切成参数列表，丢结尾空段；
// 内核线程的 cmdline 为空，返回 nil。
func splitCmdline(raw []byte) []string {
	if len(raw) == 0 {
		return nil
	}
	parts := strings.Split(string(raw), "\x00")
	for len(parts) > 0 && parts[len(parts)-1] == "" {
		parts = parts[:len(parts)-1]
	}
	return parts
}
