//go:build linux

package e2e

import (
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pengtang1024/kore/pkg/pluginapi"
)

// TestProcessE2E 真实插件进程全链路：列表 → 详情 → 进程树 → 信号 → 复查 → 护栏 → 脱敏。
func TestProcessE2E(t *testing.T) {
	tmp := t.TempDir()
	binary := buildPlugin(t)
	socket := filepath.Join(tmp, "process.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatalf("监听 UDS 失败: %v", err)
	}
	defer func() { _ = listener.Close() }()
	_ = listener.(*net.UnixListener).SetDeadline(time.Now().Add(120 * time.Second))

	process, stderr := startPlugin(t, binary, socket)
	conn, err := listener.Accept()
	if err != nil {
		t.Fatalf("接受插件连接失败: %v (stderr=%s)", err, stderr.String())
	}
	defer func() { _ = conn.Close() }()
	_ = conn.SetDeadline(time.Now().Add(120 * time.Second))
	handshake(t, conn)

	// 1) 首次 list：真实进程 + cpu_pct 全 0 且带说明
	first := parseList(t, mustCall(t, conn, "e2e-list-1", "process.list", map[string]any{"limit": 5}))
	if first.Schema != "process.list.v1" || first.Count == 0 || first.Count > 5 {
		t.Fatalf("首次 list 结果不符: %+v", first)
	}
	if !first.FirstSample || !strings.Contains(first.CPUNote, "首次") {
		t.Fatalf("首次采样标记/说明不符: first_sample=%v note=%q", first.FirstSample, first.CPUNote)
	}
	for _, item := range first.Items {
		if item.PID <= 0 || item.Name == "" || item.User == "" || item.State == "" || item.CPUPercent != 0 {
			t.Fatalf("首条进程字段不符: %+v", item)
		}
	}
	t.Logf("E2E_LIST_META schema=%s count=%d total=%d sort=%s first_sample=%v masked_args=%v",
		first.Schema, first.Count, first.Total, first.Sort, first.FirstSample, first.MaskedArgs)
	t.Logf("E2E_LIST_HEAD %s", itemsJSON(t, first.Items, 5))
	if first.Total < 2 {
		t.Fatalf("total=%d，未读到真实进程列表", first.Total)
	}

	// 2) 插件自身必须在列表里（证明读的是本机 /proc，不是占位数据）
	full := parseList(t, mustCall(t, conn, "e2e-list-plugin", "process.list", map[string]any{"limit": 500}))
	pluginItem, ok := findItem(full.Items, process.Process.Pid)
	if !ok {
		t.Fatalf("插件自身 pid %d 未出现在 list（total=%d）", process.Process.Pid, full.Total)
	}
	t.Logf("E2E_PLUGIN_ITEM %s", itemsJSON(t, []listItem{pluginItem}, 1))

	// 3) 起一个真实 sleep 300
	sleeper := spawnRealSleep(t)
	sleepPID := sleeper.Process.Pid
	t.Logf("E2E_SLEEP_STARTED pid=%d", sleepPID)

	// 非法信号先于 kill 验证：只允许 TERM/KILL/HUP/INT
	badSignal := request(t, conn, "e2e-bad-signal", "process.kill", map[string]any{"pid": sleepPID, "signal": "USR1"})
	if badSignal.Error == nil || badSignal.Error.Code != pluginapi.E_INVALID {
		t.Fatalf("非法信号错误=%+v，期望 E_INVALID", badSignal.Error)
	}
	t.Logf("E2E_INVALID_SIGNAL %s: %s", badSignal.Error.Code, badSignal.Error.Message)

	// 4) detail 指向 sleep，并核对 status / exe / cwd / 启动时间
	detail := parseDetail(t, mustCall(t, conn, "e2e-detail", "process.detail", map[string]any{"pid": sleepPID}))
	if detail.Schema != "process.detail.v1" || detail.PID != sleepPID || detail.Name != "sleep" {
		t.Fatalf("detail 基础字段不符: %+v", detail)
	}
	if !strings.Contains(detail.Cmdline, "sleep 300") || filepath.Base(detail.Exe) != "sleep" || detail.Cwd == "" {
		t.Fatalf("detail cmdline/exe/cwd 不符: cmdline=%q exe=%q cwd=%q", detail.Cmdline, detail.Exe, detail.Cwd)
	}
	if detail.Status.VmRSSKiB <= 0 || detail.Status.Threads < 1 || detail.CLKTCK != 100 || detail.StartedAt == "" {
		t.Fatalf("detail status/启动时间不符: %+v", detail)
	}
	if _, err := time.Parse(time.RFC3339, detail.StartedAt); err != nil {
		t.Fatalf("started_at 非 RFC3339: %q", detail.StartedAt)
	}
	detailJSON, _ := json.Marshal(detail)
	t.Logf("E2E_DETAIL_JSON %s", detailJSON)

	// 5) list 能查到该 sleep（默认 cpu 排序）
	listItemFound, ok := pollListItem(t, conn, sleepPID)
	if !ok {
		t.Fatalf("sleep pid %d 未出现在 list", sleepPID)
	}
	t.Logf("E2E_SLEEP_IN_LIST %s", itemsJSON(t, []listItem{listItemFound}, 1))

	// 6) tree 以测试进程为根，sleep 与插件均为其直接子进程
	tree := parseTree(t, mustCall(t, conn, "e2e-tree", "process.tree", map[string]any{"pid": os.Getpid()}))
	if tree.Schema != "process.tree.v1" || tree.Root != os.Getpid() || tree.Count == 0 {
		t.Fatalf("tree 基础字段不符: %+v", tree)
	}
	foundSleep, foundPlugin := false, false
	for _, node := range tree.Nodes {
		if node.PID == sleepPID && node.PPID == os.Getpid() && node.Depth == 1 {
			foundSleep = true
		}
		if node.PID == process.Process.Pid && node.Depth == 1 {
			foundPlugin = true
		}
	}
	if !foundSleep || !foundPlugin {
		t.Fatalf("tree 未包含 sleep/插件子节点: %+v", tree.Nodes)
	}
	treeJSON, _ := json.Marshal(tree)
	t.Logf("E2E_TREE_JSON %s", treeJSON)

	// 7) kill 默认 TERM，500ms 内确认不存活
	killed := parseKill(t, mustCall(t, conn, "e2e-kill", "process.kill", map[string]any{"pid": sleepPID}))
	if killed.Schema != "process.kill.v1" || !killed.Killed || killed.StillAlive || killed.Signal != "TERM" {
		t.Fatalf("kill 结果不符: %+v", killed)
	}
	_ = sleeper.Wait() // 收尸，避免残留僵尸
	waitGone(t, sleepPID, 3*time.Second)
	killJSON, _ := json.Marshal(killed)
	t.Logf("E2E_KILL_JSON %s", killJSON)

	// 8) 二次 list 确认消失（同一 limit=500 查询）
	after := parseList(t, mustCall(t, conn, "e2e-list-after", "process.list", map[string]any{"limit": 500}))
	if _, exists := findItem(after.Items, sleepPID); exists {
		t.Fatalf("kill 后 list 仍包含 pid %d", sleepPID)
	}
	if _, statErr := os.Stat(procPath(sleepPID)); statErr == nil {
		t.Fatalf("kill 后 /proc/%d 仍存在", sleepPID)
	}
	t.Logf("E2E_LIST_AFTER_KILL count=%d total=%d contains_sleep=%v", after.Count, after.Total, false)

	// 9) 护栏：pid 1 与插件自身必须被拒（贴原始错误）
	pidOne := request(t, conn, "e2e-kill-pid1", "process.kill", map[string]any{"pid": 1})
	if pidOne.Error == nil || pidOne.Error.Code != pluginapi.E_INVALID {
		t.Fatalf("kill pid 1 错误=%+v，期望 E_INVALID", pidOne.Error)
	}
	t.Logf("E2E_KILL_PID1_REJECTED %s: %s", pidOne.Error.Code, pidOne.Error.Message)

	selfKill := request(t, conn, "e2e-kill-self", "process.kill", map[string]any{"pid": process.Process.Pid})
	if selfKill.Error == nil || selfKill.Error.Code != pluginapi.E_UNAUTHORIZED {
		t.Fatalf("kill 自身错误=%+v，期望 E_UNAUTHORIZED", selfKill.Error)
	}
	t.Logf("E2E_KILL_SELF_REJECTED %s: %s", selfKill.Error.Code, selfKill.Error.Message)

	// 10) 命令行脱敏：cmdline 恰为 `sleep 300 --password=secret`
	masked := spawnSelfSleeper(t, "sleep", "300", "--password=secret")
	maskPID := masked.Process.Pid
	maskItem, ok := pollListItem(t, conn, maskPID)
	if !ok {
		t.Fatalf("脱敏验证进程 %d 未出现在 list", maskPID)
	}
	if !strings.Contains(maskItem.Cmdline, "--password=***") || strings.Contains(maskItem.Cmdline, "secret") {
		t.Fatalf("命令行未脱敏: %q", maskItem.Cmdline)
	}
	t.Logf("E2E_MASK_ITEM %s", itemsJSON(t, []listItem{maskItem}, 1))
	maskKill := parseKill(t, mustCall(t, conn, "e2e-mask-kill", "process.kill", map[string]any{"pid": maskPID}))
	if !maskKill.Killed || maskKill.StillAlive {
		t.Fatalf("脱敏进程 kill 结果不符: %+v", maskKill)
	}
	_ = masked.Wait()
	waitGone(t, maskPID, 3*time.Second)
	maskKillJSON, _ := json.Marshal(maskKill)
	t.Logf("E2E_MASK_KILL_JSON %s", maskKillJSON)

	// 11) 优雅退出，stderr 不得出现 panic
	shutdown := mustCall(t, conn, "e2e-shutdown", "kore.shutdown", map[string]any{})
	if !strings.Contains(shutdown, `"ok":true`) {
		t.Fatalf("shutdown 结果不符: %s", shutdown)
	}
	t.Logf("E2E_SHUTDOWN %s", shutdown)
	waitExit(t, process)
	if strings.Contains(stderr.String(), "panic:") {
		t.Fatalf("插件 stderr 出现 panic: %s", stderr.String())
	}

	// 12) 收尾证据：两个 sleep 均无残留
	for _, pid := range []int{sleepPID, maskPID} {
		if _, err := os.Stat(procPath(pid)); err == nil {
			t.Fatalf("收尾时 /proc/%d 仍存在（残留进程）", pid)
		}
	}
	t.Logf("E2E_NO_RESIDUAL sleep_pid=%d mask_pid=%d all_gone=true", sleepPID, maskPID)
}
