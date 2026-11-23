//go:build linux

package procfs

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/pengtang1024/kore/pkg/pluginapi"
)

// historyFixture 造最小 /proc 夹具（stat / meminfo / loadavg）。
func historyFixture(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	mustWrite := func(name, content string) {
		if err := os.WriteFile(filepath.Join(root, name), []byte(content), 0o644); err != nil {
			t.Fatalf("写 %s: %v", name, err)
		}
	}
	mustWrite("stat", "cpu  100 0 100 800 0 0 0 0 0 0\n")
	mustWrite("meminfo", "MemTotal:       16000000 kB\nMemAvailable:    8000000 kB\n")
	mustWrite("loadavg", "0.42 0.30 0.20 1/100 4242\n")
	return root
}

func historyRun(t *testing.T, rt *Runtime, params string) historyResult {
	t.Helper()
	raw, perr := rt.Call("process.history", json.RawMessage(params))
	if perr != nil {
		t.Fatalf("process.history 失败: %+v", perr)
	}
	var result historyResult
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatalf("解析 history: %v", err)
	}
	return result
}

func trendRun(t *testing.T, rt *Runtime, params string) trendResult {
	t.Helper()
	raw, perr := rt.Call("process.trend", json.RawMessage(params))
	if perr != nil {
		t.Fatalf("process.trend 失败: %+v", perr)
	}
	var result trendResult
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatalf("解析 trend: %v", err)
	}
	return result
}

func watchdogRun(t *testing.T, rt *Runtime, params string) watchdogResult {
	t.Helper()
	raw, perr := rt.Call("process.watchdog.suggest", json.RawMessage(params))
	if perr != nil {
		t.Fatalf("process.watchdog.suggest 失败: %+v", perr)
	}
	var result watchdogResult
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatalf("解析 watchdog: %v", err)
	}
	return result
}

// TestHistoryRingAndRestart 校验零 ticker 的追加/复用、60 点截断、state 0600 与重启续读。
func TestHistoryRingAndRestart(t *testing.T) {
	root := historyFixture(t)
	stateDir := t.TempDir()
	now := time.Unix(1700000000, 0)
	rt := NewRuntime(Options{ProcRoot: root, StateDir: stateDir, SelfPID: os.Getpid(), MaskArgs: true,
		Now: func() time.Time { return now }}, &fakeSource{})
	first := historyRun(t, rt, `{}`)
	if !first.Sampled || first.Count != 1 {
		t.Fatalf("首次采样不符: %+v", first)
	}
	now = now.Add(10 * time.Second)
	reused := historyRun(t, rt, `{}`)
	if reused.Sampled || reused.Count != 1 || !strings.Contains(reused.Note, "复用") {
		t.Fatalf("间隔不足应复用上一点: %+v", reused)
	}
	now = now.Add(21 * time.Second)
	second := historyRun(t, rt, `{}`)
	if !second.Sampled || second.Count != 2 {
		t.Fatalf("满 30s 应追加: %+v", second)
	}
	var last historyResult
	for i := 0; i < 61; i++ {
		now = now.Add(31 * time.Second)
		last = historyRun(t, rt, `{}`)
	}
	if last.Count != historyCapacity || !last.Truncated {
		t.Fatalf("环形缓冲应截断到 %d: count=%d truncated=%v", historyCapacity, last.Count, last.Truncated)
	}
	info, err := os.Stat(filepath.Join(stateDir, historyFileName))
	if err != nil {
		t.Fatalf("history state 不存在: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("history state 权限=%o，期望 600", info.Mode().Perm())
	}
	// 重启续读：新 Runtime 复用同一 stateDir，首点应是上一轮的第二点。
	restarted := NewRuntime(Options{ProcRoot: root, StateDir: stateDir, SelfPID: os.Getpid(), MaskArgs: true,
		Now: func() time.Time { return now }}, &fakeSource{})
	now = now.Add(31 * time.Second)
	continued := historyRun(t, restarted, `{}`)
	if continued.Count != historyCapacity || !continued.Truncated {
		t.Fatalf("重启后应续读并保持 60 点: %+v", continued)
	}
	if continued.Points[0].T != last.Points[1].T {
		t.Fatalf("重启后首点应为上一轮第二点：%q != %q", continued.Points[0].T, last.Points[1].T)
	}
}

// TestTrendInsufficientAndDiff 校验首次/间隔不足 insufficient_data、差分 CPU、state 0600 与重启续读。
func TestTrendInsufficientAndDiff(t *testing.T) {
	stateDir := t.TempDir()
	now := time.Unix(1700000000, 0)
	src := &fakeSource{records: []Record{{PID: 4242, Name: "sleep", State: 'S', Cmdline: "sleep 300", UTicks: 100, STicks: 50, RSSKiB: 2048}}}
	rt := NewRuntime(Options{ProcRoot: t.TempDir(), StateDir: stateDir, SelfPID: os.Getpid(), MaskArgs: true,
		Now: func() time.Time { return now }}, src)
	first := trendRun(t, rt, `{"pid":4242}`)
	if !first.InsufficientData || !strings.Contains(first.Hint, "首次") {
		t.Fatalf("首次采样应 insufficient_data: %+v", first)
	}
	now = now.Add(1 * time.Second)
	short := trendRun(t, rt, `{"pid":4242}`)
	if !short.InsufficientData || !strings.Contains(short.Hint, "不足") {
		t.Fatalf("间隔不足应 insufficient_data: %+v", short)
	}
	src.records[0].UTicks = 160 // +60 tick / 3s = 20%
	now = now.Add(3 * time.Second)
	sampled := trendRun(t, rt, `{"pid":4242}`)
	if sampled.InsufficientData || sampled.Count != 1 {
		t.Fatalf("满 2s 应出趋势点: %+v", sampled)
	}
	if sampled.Points[0].CPUPercent != 15 || sampled.Points[0].RSSKiB != 2048 {
		t.Fatalf("CPU 差分/内存不符: %+v", sampled.Points[0])
	}
	info, err := os.Stat(filepath.Join(stateDir, trendFileName))
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("trend state 应为 0600: info=%v err=%v", info, err)
	}
	// 重启续读：同一 stateDir 的新 Runtime 继续追加。
	restarted := NewRuntime(Options{ProcRoot: t.TempDir(), StateDir: stateDir, SelfPID: os.Getpid(), MaskArgs: true,
		Now: func() time.Time { return now }}, src)
	now = now.Add(3 * time.Second)
	src.records[0].UTicks = 220
	continued := trendRun(t, restarted, `{"pid":4242}`)
	if continued.InsufficientData || continued.Count != 2 {
		t.Fatalf("重启后应续读趋势: %+v", continued)
	}
}

// TestWatchdogSuggestIdempotent 校验片段渲染幂等、边界声明与 pid/name 二选一。
func TestWatchdogSuggestIdempotent(t *testing.T) {
	src := &fakeSource{records: []Record{{PID: 4242, Name: "nginx", State: 'S', Cmdline: "nginx -g daemon off;"}}}
	rt := NewRuntime(Options{ProcRoot: t.TempDir(), SelfPID: os.Getpid(), MaskArgs: true}, src)
	byPID := watchdogRun(t, rt, `{"pid":4242}`)
	byName := watchdogRun(t, rt, `{"name":"nginx"}`)
	if !reflect.DeepEqual(byPID, byName) {
		t.Fatalf("pid 与 name 两种入参应渲染同一建议:\n%+v\n%+v", byPID, byName)
	}
	if byPID.UnitName != "kore-guard-nginx.service" || byPID.ExecStart == "" {
		t.Fatalf("unit 名/ExecStart 不符: %+v", byPID)
	}
	if !strings.Contains(byPID.UserUnit, "Restart=always") || !strings.Contains(byPID.UserUnit, "WantedBy=default.target") {
		t.Fatalf("user unit 片段不符:\n%s", byPID.UserUnit)
	}
	if !strings.Contains(byPID.SystemUnit, "WantedBy=multi-user.target") {
		t.Fatalf("system unit 片段不符:\n%s", byPID.SystemUnit)
	}
	if !strings.Contains(byPID.Boundary, "kore 不做进程守护") || !strings.Contains(byPID.Boundary, "systemd") {
		t.Fatalf("边界声明不符: %q", byPID.Boundary)
	}
	if !strings.Contains(byPID.ScopeGuide, "systemctl --user") || !strings.Contains(byPID.ScopeGuide, "sudo systemctl") {
		t.Fatalf("二选一说明不符: %q", byPID.ScopeGuide)
	}
	if _, perr := rt.Call("process.watchdog.suggest", json.RawMessage(`{}`)); perr == nil || perr.Code != pluginapi.E_INVALID {
		t.Fatalf("缺 pid/name 应 E_INVALID: %+v", perr)
	}
	if _, perr := rt.Call("process.watchdog.suggest", json.RawMessage(`{"name":"nope"}`)); perr == nil || perr.Code != pluginapi.E_NOTFOUND {
		t.Fatalf("未知 name 应 E_NOTFOUND: %+v", perr)
	}
}
