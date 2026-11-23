package procfs

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/pengtang1024/kore/pkg/pluginapi"
)

// 端口 / fd 归属的硬上限：防止全系统扫描被异常进程数或海量 fd 拖垮。
const (
	maxPortFDs     = 512
	maxPortProcs   = 2000
	maxPortSockets = 4096
)

// portsParams 是 process.ports 入参：pid 缺省/0 表示扫描全部进程。
type portsParams struct {
	PID int `json:"pid"`
}

// PortListener 是一条监听端口归属：端口 / 协议 / 地址 / 进程名 / pid。
type PortListener struct {
	PID      int    `json:"pid"`
	Name     string `json:"name"`
	Protocol string `json:"protocol"`
	Address  string `json:"address"`
	Port     int    `json:"port"`
	Inode    uint64 `json:"inode"`
}

// PortFD 是一个文件描述符的归类：socket:[inode] 单独归类，敏感路径标记。
type PortFD struct {
	PID       int    `json:"pid"`
	Name      string `json:"name"`
	FD        string `json:"fd"`
	Target    string `json:"target"`
	Kind      string `json:"kind"`
	Sensitive bool   `json:"sensitive"`
	Inode     uint64 `json:"inode,omitempty"`
	Port      int    `json:"port,omitempty"`
	Protocol  string `json:"protocol,omitempty"`
}

// portsResult 是 process.ports 的结构；listeners 按端口升序、fds 按 pid+fd 升序。
type portsResult struct {
	Schema           string         `json:"schema"`
	PID              int            `json:"pid"`
	ScannedProcesses int            `json:"scanned_processes"`
	ScannedFDs       int            `json:"scanned_fds"`
	Listeners        []PortListener `json:"listeners"`
	FDs              []PortFD       `json:"fds"`
	Truncated        bool           `json:"truncated"`
}

// listeningSocket 是 /proc/net/tcp{,6} 中一条 LISTEN 套接字（inode 为反查键）。
type listeningSocket struct {
	Protocol string
	Address  string
	Port     int
	Inode    uint64
}

// callPorts 读 /proc/net/tcp{,6} 的 LISTEN 套接字，再用 /proc/<pid>/fd 符号链接反查归属；
// 只读 procfs，不联网、不起 ticker。
func (r *Runtime) callPorts(raw json.RawMessage) (json.RawMessage, *pluginapi.Error) {
	var p portsParams
	if perr := decodeParams(raw, &p); perr != nil {
		return nil, perr
	}
	if p.PID < 0 {
		return nil, invalid("pid 必须为非负整数（0 或缺省表示扫描全部进程）")
	}
	sockets, err := r.readListeningSockets()
	if err != nil {
		return nil, classify(err)
	}
	var targets []int
	if p.PID > 0 {
		targets = []int{p.PID}
	} else {
		targets = r.listProcPIDs()
	}
	result := portsResult{Schema: "process.ports.v1", PID: p.PID}
	seenListener := make(map[string]bool, 64)
	for _, pid := range targets {
		name := r.readComm(pid)
		fds, perr := r.readProcessFDs(pid, name, sockets, &result, seenListener)
		if perr != nil {
			if p.PID > 0 {
				return nil, perr
			}
			continue // 全系统扫描时跳过不可读 / 已退出进程
		}
		result.FDs = append(result.FDs, fds...)
		result.ScannedProcesses++
	}
	sort.Slice(result.Listeners, func(i, j int) bool {
		if result.Listeners[i].Port != result.Listeners[j].Port {
			return result.Listeners[i].Port < result.Listeners[j].Port
		}
		return result.Listeners[i].PID < result.Listeners[j].PID
	})
	sort.Slice(result.FDs, func(i, j int) bool {
		if result.FDs[i].PID != result.FDs[j].PID {
			return result.FDs[i].PID < result.FDs[j].PID
		}
		return result.FDs[i].FD < result.FDs[j].FD
	})
	return encode(result)
}

// readListeningSockets 解析 /proc/net/tcp 与 /proc/net/tcp6 的 LISTEN（st=0A）条目。
func (r *Runtime) readListeningSockets() (map[uint64]listeningSocket, error) {
	sockets := make(map[uint64]listeningSocket, 64)
	for _, item := range []struct {
		file     string
		protocol string
		ipv6     bool
	}{
		{"tcp", "tcp", false},
		{"tcp6", "tcp6", true},
	} {
		path := filepath.Join(r.opts.ProcRoot, "net", item.file)
		data, err := os.ReadFile(path)
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				continue
			}
			return nil, err
		}
		parseTCPTable(data, item.protocol, item.ipv6, sockets)
		if len(sockets) > maxPortSockets {
			return nil, fmt.Errorf("LISTEN 套接字超过上限 %d", maxPortSockets)
		}
	}
	return sockets, nil
}

// parseTCPTable 解析一张 /proc/net/tcp{,6} 表；格式异常行跳过，不让单行脏数据失败整个请求。
func parseTCPTable(data []byte, protocol string, ipv6 bool, sockets map[uint64]listeningSocket) {
	for index, line := range strings.Split(string(data), "\n") {
		if index == 0 || strings.TrimSpace(line) == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 10 || fields[3] != "0A" {
			continue
		}
		address, port, err := parseHexEndpoint(fields[1], ipv6)
		if err != nil {
			continue
		}
		inode, err := strconv.ParseUint(fields[9], 10, 64)
		if err != nil || inode == 0 {
			continue
		}
		sockets[inode] = listeningSocket{Protocol: protocol, Address: address, Port: port, Inode: inode}
	}
}

// parseHexEndpoint 解析 /proc/net/tcp{,6} 的 local_address：HEX_IP:HEX_PORT。
// IPv4 是 4 字节小端；IPv6 是 4 个 32 位小端字，用标准库 net.IP 做文本化。
func parseHexEndpoint(raw string, ipv6 bool) (string, int, error) {
	host, portHex, ok := strings.Cut(raw, ":")
	if !ok {
		return "", 0, fmt.Errorf("endpoint 缺少端口: %q", raw)
	}
	port, err := strconv.ParseUint(portHex, 16, 16)
	if err != nil {
		return "", 0, fmt.Errorf("端口解析失败: %w", err)
	}
	ip, err := parseHexIP(host, ipv6)
	if err != nil {
		return "", 0, err
	}
	return ip, int(port), nil
}

// parseHexIP 把 procfs 的十六进制地址转成可读 IP；格式不合法即报错（由调用方跳过该行）。
func parseHexIP(hex string, ipv6 bool) (string, error) {
	if ipv6 {
		if len(hex) != 32 {
			return "", fmt.Errorf("IPv6 地址长度 %d != 32", len(hex))
		}
		bytes := make([]byte, 16)
		for word := 0; word < 4; word++ {
			value, err := strconv.ParseUint(hex[word*8:(word+1)*8], 16, 32)
			if err != nil {
				return "", fmt.Errorf("IPv6 地址解析失败: %w", err)
			}
			bytes[word*4] = byte(value)
			bytes[word*4+1] = byte(value >> 8)
			bytes[word*4+2] = byte(value >> 16)
			bytes[word*4+3] = byte(value >> 24)
		}
		return net.IP(bytes).String(), nil
	}
	if len(hex) != 8 {
		return "", fmt.Errorf("IPv4 地址长度 %d != 8", len(hex))
	}
	value, err := strconv.ParseUint(hex, 16, 32)
	if err != nil {
		return "", fmt.Errorf("IPv4 地址解析失败: %w", err)
	}
	return net.IPv4(byte(value), byte(value>>8), byte(value>>16), byte(value>>24)).String(), nil
}

// listProcPIDs 列出 /proc 下的数字 pid（最多 maxPortProcs 个，超出由调用方标记 truncated）。
func (r *Runtime) listProcPIDs() []int {
	entries, err := os.ReadDir(r.opts.ProcRoot)
	if err != nil {
		return nil
	}
	pids := make([]int, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		pid, err := strconv.Atoi(entry.Name())
		if err != nil || pid <= 0 {
			continue
		}
		pids = append(pids, pid)
		if len(pids) >= maxPortProcs {
			break
		}
	}
	sort.Ints(pids)
	return pids
}

// readComm 读取进程名；不可读时返回空串（只影响展示，不影响归属判定）。
func (r *Runtime) readComm(pid int) string {
	data, err := os.ReadFile(filepath.Join(r.opts.ProcRoot, strconv.Itoa(pid), "comm"))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

// readProcessFDs 读 /proc/<pid>/fd 符号链接并归类；命中 LISTEN inode 的 fd 同时产出端口归属。
func (r *Runtime) readProcessFDs(pid int, name string, sockets map[uint64]listeningSocket,
	result *portsResult, seenListener map[string]bool) ([]PortFD, *pluginapi.Error) {
	dir := filepath.Join(r.opts.ProcRoot, strconv.Itoa(pid), "fd")
	entries, err := os.ReadDir(dir)
	if err != nil {
		switch {
		case errors.Is(err, fs.ErrNotExist):
			return nil, notFound(fmt.Sprintf("pid %d 不存在或已退出", pid))
		case errors.Is(err, fs.ErrPermission):
			return nil, needRoot("读取 /proc/" + strconv.Itoa(pid) + "/fd 需要 root 权限：请用 sudo 启动 kored")
		}
		return nil, classify(err)
	}
	fds := make([]PortFD, 0, len(entries))
	for _, entry := range entries {
		if len(fds) >= maxPortFDs {
			result.Truncated = true
			break
		}
		target, err := os.Readlink(filepath.Join(dir, entry.Name()))
		if err != nil {
			continue
		}
		result.ScannedFDs++
		fd := PortFD{PID: pid, Name: name, FD: entry.Name(), Target: target, Kind: "file"}
		if inode, ok := socketInode(target); ok {
			fd.Kind = "socket"
			fd.Inode = inode
			if sock, ok := sockets[inode]; ok {
				fd.Protocol, fd.Port = sock.Protocol, sock.Port
				key := fmt.Sprintf("%d/%d", pid, inode)
				if !seenListener[key] {
					seenListener[key] = true
					result.Listeners = append(result.Listeners, PortListener{
						PID: pid, Name: name, Protocol: sock.Protocol,
						Address: sock.Address, Port: sock.Port, Inode: inode,
					})
				}
			}
		} else if strings.HasPrefix(target, "anon_inode:") {
			fd.Kind = "other"
		} else {
			fd.Sensitive = sensitivePath(target)
		}
		fds = append(fds, fd)
	}
	return fds, nil
}

// socketInode 从 socket:[123] 里取出 inode。
func socketInode(target string) (uint64, bool) {
	if !strings.HasPrefix(target, "socket:[") || !strings.HasSuffix(target, "]") {
		return 0, false
	}
	inode, err := strconv.ParseUint(target[len("socket:["):len(target)-1], 10, 64)
	return inode, err == nil
}

// sensitivePath 标记凭据 / 密钥 / 关键系统文件的 fd；只做只读归类，不打开目标。
func sensitivePath(path string) bool {
	for _, prefix := range []string{"/etc/shadow", "/etc/gshadow", "/etc/sudoers", "/proc/kcore"} {
		if path == prefix || strings.HasPrefix(path, prefix+"/") {
			return true
		}
	}
	if strings.Contains(path, "/.ssh/") || strings.Contains(path, "/.gnupg/") {
		return true
	}
	for _, suffix := range []string{".pem", ".key", ".p12", ".pfx", ".kdbx"} {
		if strings.HasSuffix(path, suffix) {
			return true
		}
	}
	base := filepath.Base(path)
	return base == "id_rsa" || base == "id_ed25519" || base == ".env"
}
