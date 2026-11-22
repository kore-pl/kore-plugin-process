package procfs

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// procSource 是真实 Linux /proc 执行面：所有路径都由 root + 数字 pid + 固定文件名拼成，
// 不接收调用方给的任意 /proc 路径。
type procSource struct {
	root string
}

// newProcSource 构造真实执行面；root 为空时取 DefaultProcRoot。
func newProcSource(root string) Source {
	if root == "" {
		root = DefaultProcRoot
	}
	return procSource{root: root}
}

// Scan 扫描 root 下全部数字 pid 目录：读取失败或 EPERM 的进程标记 restricted 后继续，
// 进程在扫描期间退出（ENOENT）则直接跳过，不让单个进程失败整个请求。
func (s procSource) Scan() ([]Record, error) {
	entries, err := os.ReadDir(s.root)
	if err != nil {
		return nil, err
	}
	records := make([]Record, 0, len(entries))
	for _, entry := range entries {
		pid, ok := parsePID(entry.Name())
		if !ok {
			continue
		}
		record, err := s.read(pid, false)
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				continue
			}
			return nil, err
		}
		records = append(records, record)
	}
	return records, nil
}

// Stat 读取单个进程的详情字段。
func (s procSource) Stat(pid int) (Record, error) { return s.read(pid, true) }

// Signal 投递已校验的信号。
func (s procSource) Signal(pid int, sig Signal) error { return sendSignal(pid, sig) }

// Probe 读取进程状态字符；进程不存在返回 fs.ErrNotExist。
func (s procSource) Probe(pid int) (byte, error) {
	data, err := s.readLimited(pid, "stat", 16<<10)
	if err != nil {
		return 0, err
	}
	info, err := parseStat(string(data))
	if err != nil {
		return 0, err
	}
	return info.State, nil
}

// read 读取一个进程的 stat / status / cmdline（full 时再读 exe / cwd 链接）。
// stat 不存在 → 返回 fs.ErrNotExist；其余读取失败 → restricted，不中断整次请求。
func (s procSource) read(pid int, full bool) (Record, error) {
	statData, err := s.readLimited(pid, "stat", 64<<10)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return Record{}, err
		}
		return restrictedRecord(pid, "stat 不可读"), nil
	}
	info, err := parseStat(string(statData))
	if err != nil {
		return restrictedRecord(pid, err.Error()), nil
	}
	record := Record{
		PID: pid, Name: info.Name, State: info.State, PPID: info.PPID,
		UTicks: info.UTicks, STicks: info.STicks, StartTicks: info.StartTicks,
	}
	if statusData, statusErr := s.readLimited(pid, "status", 64<<10); statusErr != nil {
		markRestricted(&record, "status 不可读")
	} else if parsed := parseStatus(string(statusData)); parsed.HasUID {
		record.UID = parsed.UID
		record.RSSKiB = parsed.VmRSSKiB
		record.VmPeakKiB = parsed.VmPeakKiB
		record.Threads = parsed.Threads
	}
	if cmdlineData, cmdErr := s.readLimited(pid, "cmdline", cmdlineLimit); cmdErr != nil {
		markRestricted(&record, "cmdline 不可读")
	} else {
		record.Cmdline = strings.Join(splitCmdline(cmdlineData), " ")
	}
	if !full {
		return record, nil
	}
	s.readLinks(pid, &record)
	return record, nil
}

// readLinks 读取 exe / cwd 符号链接；权限不足标记 restricted，进程退出则忽略。
func (s procSource) readLinks(pid int, record *Record) {
	base := filepath.Join(s.root, strconv.Itoa(pid))
	if target, err := os.Readlink(filepath.Join(base, "exe")); err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			markRestricted(record, "exe 不可读")
		}
	} else {
		record.Exe = target
	}
	if target, err := os.Readlink(filepath.Join(base, "cwd")); err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			markRestricted(record, "cwd 不可读")
		}
	} else {
		record.Cwd = target
	}
}

// readLimited 读取 pid 目录下的固定文件，最多 limit 字节。
func (s procSource) readLimited(pid int, name string, limit int64) ([]byte, error) {
	file, err := os.Open(filepath.Join(s.root, strconv.Itoa(pid), name))
	if err != nil {
		return nil, err
	}
	defer func() { _ = file.Close() }()
	return io.ReadAll(io.LimitReader(file, limit))
}

// restrictedRecord 生成只有 pid 与原因的受限记录。
func restrictedRecord(pid int, reason string) Record {
	return Record{PID: pid, Restricted: true, RestrictedReason: reason}
}

// markRestricted 追加受限原因（保留首个原因，避免重复堆叠）。
func markRestricted(record *Record, reason string) {
	record.Restricted = true
	if record.RestrictedReason == "" {
		record.RestrictedReason = reason
	}
}

// parsePID 只接受纯数字目录名（/proc/<pid>），其它条目一律跳过。
func parsePID(name string) (int, bool) {
	if name == "" {
		return 0, false
	}
	for _, r := range name {
		if r < '0' || r > '9' {
			return 0, false
		}
	}
	pid, err := strconv.Atoi(name)
	if err != nil || pid <= 0 {
		return 0, false
	}
	return pid, true
}

// readBootTime 读取 /proc/stat 的 btime（开机时刻 Unix 秒），用于把 starttime 换算为墙钟。
func readBootTime(root string) (int64, error) {
	data, err := os.ReadFile(filepath.Join(root, "stat"))
	if err != nil {
		return 0, err
	}
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 2 && fields[0] == "btime" {
			btime, parseErr := strconv.ParseInt(fields[1], 10, 64)
			if parseErr != nil {
				return 0, fmt.Errorf("btime 解析失败: %w", parseErr)
			}
			return btime, nil
		}
	}
	return 0, fmt.Errorf("未找到 btime")
}
