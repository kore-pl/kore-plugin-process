//go:build linux

package procfs

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/pengtang1024/kore/pkg/pluginapi"
)

// mustWriteFile 写入测试夹具文件（自动建父目录）。
func mustWriteFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("建目录 %s: %v", path, err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("写文件 %s: %v", path, err)
	}
}

// mustSymlink 建 fd 符号链接。
func mustSymlink(t *testing.T, target, path string) {
	t.Helper()
	if err := os.Symlink(target, path); err != nil {
		t.Fatalf("建符号链接 %s -> %s: %v", path, target, err)
	}
}

// buildPortsFixture 构造假 /proc：tcp/tcp6 表 + 进程 1234 的 fd 链接。
func buildPortsFixture(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	const header = "  sl  local_address rem_address   st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode\n"
	mustWriteFile(t, filepath.Join(root, "net", "tcp"), header+
		"   0: 0100007F:1F90 00000000:0000 0A 00000000:00000000 00:00000000 00000000  1000        0 100 1\n"+
		"   1: 0100007F:1F90 0100007F:D431 01 00000000:00000000 00:00000000 00000000  1000        0 101 1\n"+
		"garbage line\n")
	mustWriteFile(t, filepath.Join(root, "net", "tcp6"), header+
		"   0: 00000000000000000000000001000000:0050 00000000000000000000000000000000:0000 0A 00000000:00000000 00:00000000 00000000  1000 0 102 1\n")
	pidDir := filepath.Join(root, "1234")
	mustWriteFile(t, filepath.Join(pidDir, "comm"), "nginx\n")
	for _, item := range []struct{ fd, target string }{
		{"3", "socket:[100]"}, // LISTEN 8080
		{"4", "socket:[101]"}, // 已建立连接，不是监听
		{"5", "/etc/shadow"},  // 敏感路径
		{"6", "anon_inode:[eventpoll]"},
		{"7", "socket:[102]"}, // LISTEN ::1:80
	} {
		if err := os.MkdirAll(filepath.Join(pidDir, "fd"), 0o755); err != nil {
			t.Fatalf("建 fd 目录: %v", err)
		}
		mustSymlink(t, item.target, filepath.Join(pidDir, "fd", item.fd))
	}
	return root
}

// portsRun 调用 process.ports 并解析结果。
func portsRun(t *testing.T, rt *Runtime, params string) portsResult {
	t.Helper()
	raw, perr := rt.Call("process.ports", json.RawMessage(params))
	if perr != nil {
		t.Fatalf("process.ports(%s) 失败: %+v", params, perr)
	}
	var result portsResult
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatalf("解析 ports 结果: %v", err)
	}
	return result
}

// TestPortsParseAndReverseLookup 解析 tcp/tcp6 的 LISTEN 行，并按 fd 反查到进程；
// 已建立连接不算监听；socket / file / anon_inode 归类正确；敏感路径被标记。
func TestPortsParseAndReverseLookup(t *testing.T) {
	root := buildPortsFixture(t)
	rt := NewRuntime(Options{ProcRoot: root, SelfPID: os.Getpid(), MaskArgs: true}, &fakeSource{})
	result := portsRun(t, rt, `{"pid":1234}`)
	if result.Schema != "process.ports.v1" || result.PID != 1234 || result.ScannedProcesses != 1 {
		t.Fatalf("基础字段不符: %+v", result)
	}
	if len(result.Listeners) != 2 {
		t.Fatalf("监听端口数=%d，期望 2（8080/tcp + 80/tcp6）：%+v", len(result.Listeners), result.Listeners)
	}
	first := result.Listeners[0]
	if first.Port != 80 || first.Protocol != "tcp6" || first.Address != "::1" || first.Inode != 102 {
		t.Fatalf("tcp6 监听不符: %+v", first)
	}
	second := result.Listeners[1]
	if second.Port != 8080 || second.Protocol != "tcp" || second.Address != "127.0.0.1" ||
		second.PID != 1234 || second.Name != "nginx" || second.Inode != 100 {
		t.Fatalf("tcp 监听不符: %+v", second)
	}
	if len(result.FDs) != 5 {
		t.Fatalf("fd 数=%d，期望 5：%+v", len(result.FDs), result.FDs)
	}
	byFD := map[string]PortFD{}
	for _, fd := range result.FDs {
		byFD[fd.FD] = fd
	}
	if fd := byFD["3"]; fd.Kind != "socket" || fd.Port != 8080 || fd.Protocol != "tcp" || fd.Inode != 100 {
		t.Fatalf("fd3 归类不符: %+v", fd)
	}
	if fd := byFD["4"]; fd.Kind != "socket" || fd.Port != 0 {
		t.Fatalf("fd4 已建立连接不应算监听: %+v", fd)
	}
	if fd := byFD["5"]; fd.Kind != "file" || !fd.Sensitive {
		t.Fatalf("fd5 敏感路径未标记: %+v", fd)
	}
	if fd := byFD["6"]; fd.Kind != "other" || fd.Sensitive {
		t.Fatalf("fd6 anon_inode 归类不符: %+v", fd)
	}
	if fd := byFD["7"]; fd.Kind != "socket" || fd.Port != 80 || fd.Protocol != "tcp6" {
		t.Fatalf("fd7 归类不符: %+v", fd)
	}
}

// TestPortsMissingPID 不存在的 pid 返回 E_NOTFOUND；负 pid 是非法入参。
func TestPortsMissingPID(t *testing.T) {
	rt := NewRuntime(Options{ProcRoot: buildPortsFixture(t), SelfPID: os.Getpid(), MaskArgs: true}, &fakeSource{})
	if _, perr := rt.Call("process.ports", json.RawMessage(`{"pid":999999}`)); perr == nil || perr.Code != pluginapi.E_NOTFOUND {
		t.Fatalf("不存在 pid 错误=%+v，期望 E_NOTFOUND", perr)
	}
	if _, perr := rt.Call("process.ports", json.RawMessage(`{"pid":-1}`)); perr == nil || perr.Code != pluginapi.E_INVALID {
		t.Fatalf("负 pid 错误=%+v，期望 E_INVALID", perr)
	}
}

// TestPortsAllProcesses 缺省 pid 扫描全部进程：命中夹具进程，无 fd 的进程被跳过。
func TestPortsAllProcesses(t *testing.T) {
	root := buildPortsFixture(t)
	if err := os.MkdirAll(filepath.Join(root, "5678"), 0o755); err != nil {
		t.Fatalf("建无 fd 进程目录: %v", err)
	}
	rt := NewRuntime(Options{ProcRoot: root, SelfPID: os.Getpid(), MaskArgs: true}, &fakeSource{})
	result := portsRun(t, rt, `{}`)
	if result.ScannedProcesses != 1 {
		t.Fatalf("扫描进程数=%d，期望 1（5678 无 fd 被跳过）", result.ScannedProcesses)
	}
	if len(result.Listeners) != 2 {
		t.Fatalf("全扫描监听数=%d，期望 2：%+v", len(result.Listeners), result.Listeners)
	}
	if result.PID != 0 {
		t.Fatalf("全扫描 PID 应为 0: %+v", result)
	}
	_ = fmt.Sprintf("%v", result.Listeners)
}
