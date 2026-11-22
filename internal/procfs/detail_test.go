//go:build linux

package procfs

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pengtang1024/kore/pkg/pluginapi"
)

// detailRun 调用 process.detail 并解析成结构。
func detailRun(t *testing.T, rt *Runtime, pid int) detailResult {
	t.Helper()
	raw, perr := rt.Call("process.detail", json.RawMessage(fmt.Sprintf(`{"pid":%d}`, pid)))
	if perr != nil {
		t.Fatalf("process.detail(%d) 失败: %+v", pid, perr)
	}
	var result detailResult
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatalf("解析 detail 结果失败: %v", err)
	}
	return result
}

// TestDetailCurrentProcess 真读当前测试进程：stat/status/cmdline + exe/cwd + 启动时间。
func TestDetailCurrentProcess(t *testing.T) {
	pid := os.Getpid()
	rt := NewRuntime(Options{SelfPID: pid, MaskArgs: true}, nil)
	result := detailRun(t, rt, pid)
	if result.PID != pid || result.Name == "" || result.State == "" {
		t.Fatalf("基础字段不符: %+v", result)
	}
	if result.User == "" || result.UID != uint32(os.Getuid()) {
		t.Fatalf("uid/user 不符: uid=%d user=%q", result.UID, result.User)
	}
	if result.Status.VmRSSKiB <= 0 || result.Status.VmPeakKiB < result.Status.VmRSSKiB {
		t.Fatalf("VmRSS/VmPeak 不符: rss=%d peak=%d", result.Status.VmRSSKiB, result.Status.VmPeakKiB)
	}
	if result.Status.Threads < 1 {
		t.Fatalf("Threads=%d 应为正数", result.Status.Threads)
	}
	if result.Exe == "" || result.Cwd == "" {
		t.Fatalf("exe/cwd 为空: exe=%q cwd=%q", result.Exe, result.Cwd)
	}
	if result.CLKTCK != CLKTCK {
		t.Fatalf("clk_tck=%d，期望 %d", result.CLKTCK, CLKTCK)
	}
	if result.StartedAt == "" {
		t.Fatal("started_at 为空（btime 可读时应能换算）")
	}
	if _, err := time.Parse(time.RFC3339, result.StartedAt); err != nil {
		t.Fatalf("started_at 不是 RFC3339: %q (%v)", result.StartedAt, err)
	}
	if result.ElapsedS < 0 {
		t.Fatalf("elapsed_s=%d 不应为负", result.ElapsedS)
	}
}

// TestDetailNonexistent 不存在 pid 返回 E_NOTFOUND。
func TestDetailNonexistent(t *testing.T) {
	rt := NewRuntime(Options{SelfPID: os.Getpid(), MaskArgs: true}, nil)
	if _, perr := rt.Call("process.detail", json.RawMessage(`{"pid":99999999}`)); perr == nil || perr.Code != pluginapi.E_NOTFOUND {
		t.Fatalf("不存在的 pid 应返回 E_NOTFOUND，实际 %+v", perr)
	}
}

// TestDetailInvalidPID 非正整数直接拒绝。
func TestDetailInvalidPID(t *testing.T) {
	rt := testRuntime(t, sampleFixture(), nil)
	for _, params := range []string{`{"pid":0}`, `{"pid":-5}`, `{}`} {
		if _, perr := rt.Call("process.detail", json.RawMessage(params)); perr == nil || perr.Code != pluginapi.E_INVALID {
			t.Fatalf("%s 应返回 E_INVALID，实际 %+v", params, perr)
		}
	}
}

// TestDetailMask 详情同样走脱敏。
func TestDetailMask(t *testing.T) {
	src := &fakeSource{records: []Record{{
		PID: 4242, Name: "sleep", State: 'S', UID: 1000, RSSKiB: 2048, Threads: 1,
		Cmdline: "sleep 300 --token=abc123", Exe: "/usr/bin/sleep", Cwd: "/tmp", StartTicks: 500,
	}}}
	rt := testRuntime(t, src, nil)
	result := detailRun(t, rt, 4242)
	if got := result.Cmdline; got != "sleep 300 --token=***" {
		t.Fatalf("detail cmdline 脱敏不符: %q", got)
	}
	if result.Exe != "/usr/bin/sleep" || result.Cwd != "/tmp" || result.Status.Threads != 1 {
		t.Fatalf("detail 字段不符: %+v", result)
	}
}

// TestDetailRestricted 目录不可读时返回 restricted=true 的部分详情，而不是整体报错。
func TestDetailRestricted(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root 不受目录权限限制，跳过")
	}
	root := t.TempDir()
	writeFixtureBootTime(t, root)
	dir := filepath.Join(root, "1234")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("建 fixture 目录失败: %v", err)
	}
	for name, content := range map[string]string{
		"stat":    fixtureStat("secret", 'S', 1, 0, 0, 100),
		"status":  "Uid:\t0\t0\t0\t0\nVmRSS:\t 10 kB\nThreads:\t1\n",
		"cmdline": "secret\x00--password=x\x00",
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatalf("写 fixture 文件失败: %v", err)
		}
	}
	if err := os.Chmod(dir, 0o000); err != nil {
		t.Fatalf("chmod fixture 失败: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })

	rt := NewRuntime(Options{ProcRoot: root, SelfPID: 999999, MaskArgs: true}, nil)
	result := detailRun(t, rt, 1234)
	if !result.Restricted || result.RestrictedReason == "" {
		t.Fatalf("受限进程未标记: %+v", result)
	}
}

// TestScanRestrictedDoesNotFailRequest 一个进程不可读时，整次 list 仍成功并逐条标记。
func TestScanRestrictedDoesNotFailRequest(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root 不受目录权限限制，跳过")
	}
	root := t.TempDir()
	writeFixtureBootTime(t, root)
	readable := filepath.Join(root, "111")
	if err := os.MkdirAll(readable, 0o755); err != nil {
		t.Fatalf("建可读 fixture 失败: %v", err)
	}
	for name, content := range map[string]string{
		"stat":    fixtureStat("visible", 'S', 1, 5, 6, 200),
		"status":  "Uid:\t1000\t1000\t1000\t1000\nVmRSS:\t 321 kB\nThreads:\t2\n",
		"cmdline": "visible\x00--name=x\x00",
	} {
		if err := os.WriteFile(filepath.Join(readable, name), []byte(content), 0o644); err != nil {
			t.Fatalf("写可读 fixture 失败: %v", err)
		}
	}
	hidden := filepath.Join(root, "222")
	if err := os.MkdirAll(hidden, 0o755); err != nil {
		t.Fatalf("建受限 fixture 失败: %v", err)
	}
	if err := os.Chmod(hidden, 0o000); err != nil {
		t.Fatalf("chmod 受限 fixture 失败: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(hidden, 0o755) })

	rt := NewRuntime(Options{ProcRoot: root, SelfPID: 999999, MaskArgs: true}, nil)
	result := listRun(t, rt, `{"limit":500}`)
	if result.Count != 2 {
		t.Fatalf("应同时返回可读与受限进程，count=%d", result.Count)
	}
	restricted, visible := 0, 0
	for _, item := range result.Items {
		if item.Restricted {
			restricted++
		} else {
			visible++
		}
	}
	if restricted != 1 || visible != 1 {
		t.Fatalf("restricted/visible=%d/%d，期望 1/1", restricted, visible)
	}
}

// writeFixtureBootTime 给 fixture procroot 写一份最小 /proc/stat（含 btime）。
func writeFixtureBootTime(t *testing.T, root string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(root, "stat"), []byte("cpu  1 2 3\nbtime 1700000000\n"), 0o644); err != nil {
		t.Fatalf("写 fixture /proc/stat 失败: %v", err)
	}
}

// fixtureStat 组装一份字段数合法（20 个 comm 后字段）的 /proc/<pid>/stat。
func fixtureStat(name string, state byte, ppid int, utime, stime, starttime uint64) string {
	fields := make([]string, 20)
	for i := range fields {
		fields[i] = "0"
	}
	fields[0] = string(state)
	fields[1] = fmt.Sprintf("%d", ppid)
	fields[11] = fmt.Sprintf("%d", utime)
	fields[12] = fmt.Sprintf("%d", stime)
	fields[19] = fmt.Sprintf("%d", starttime)
	return fmt.Sprintf("4242 (%s) %s", name, strings.Join(fields, " "))
}
