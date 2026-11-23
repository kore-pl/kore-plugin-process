//go:build linux

package procfs

import (
	"encoding/json"
	"fmt"
	"os"
	"syscall"
	"testing"
	"time"

	"github.com/pengtang1024/kore/pkg/pluginapi"
)

// killRun 调用 process.kill 并解析成结构。
func killRun(t *testing.T, rt *Runtime, params string) killResult {
	t.Helper()
	raw, perr := rt.Call("process.kill", json.RawMessage(params))
	if perr != nil {
		t.Fatalf("process.kill(%s) 失败: %+v", params, perr)
	}
	var result killResult
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatalf("解析 kill 结果失败: %v", err)
	}
	return result
}

// killExpect 断言 kill 返回预期错误码与关键词。
func killExpect(t *testing.T, rt *Runtime, params, code, hint string) {
	t.Helper()
	_, perr := rt.Call("process.kill", json.RawMessage(params))
	if perr == nil || perr.Code != code {
		t.Fatalf("%s 错误=%+v，期望 %s", params, perr, code)
	}
	if !contains(perr.Message, hint) {
		t.Fatalf("%s 错误信息 %q 未含 %q", params, perr.Message, hint)
	}
}

// killSrc 构造含一个可杀进程 fake 执行面，参数即记录。
func killSrc(pid int, name string) *fakeSource {
	return &fakeSource{records: []Record{{PID: pid, Name: name, State: 'S', Cmdline: name}}, probeState: 'Z'}
}

// TestKillRequiresConfirm 护栏第一层：缺 confirm:true 一律拒绝，不读 /proc 不投递。
func TestKillRequiresConfirm(t *testing.T) {
	src := killSrc(4245, "sleep")
	rt := testRuntime(t, src, nil)
	killExpect(t, rt, `{"pid":4245,"name":"sleep"}`, pluginapi.E_INVALID, "confirm")
	if len(src.signals) != 0 {
		t.Fatalf("未确认不应投递信号: %v", src.signals)
	}
}

// TestKillRequiresName 护栏第三层入参面：缺 name（防 PID 复用复核值）拒绝。
func TestKillRequiresName(t *testing.T) {
	rt := testRuntime(t, killSrc(4245, "sleep"), nil)
	killExpect(t, rt, `{"pid":4245,"confirm":true}`, pluginapi.E_INVALID, "name")
	killExpect(t, rt, `{"pid":4245,"confirm":true,"name":"  "}`, pluginapi.E_INVALID, "name")
}

// TestKillNameMismatchIsConflict 护栏第三层：name 与 /proc comm 不符视为 PID 已复用，拒绝并提示刷新。
func TestKillNameMismatchIsConflict(t *testing.T) {
	src := killSrc(4245, "sleep")
	rt := testRuntime(t, src, nil)
	killExpect(t, rt, `{"pid":4245,"confirm":true,"name":"nginx"}`, pluginapi.E_CONFLICT, "复用")
	if len(src.signals) != 0 {
		t.Fatalf("名称不符不应投递信号: %v", src.signals)
	}
}

// TestKillRejectsPidZeroOrLess pid<1 是非法入参；PID 1 是保护名单，两者错误码不同。
func TestKillRejectsPidZeroOrLess(t *testing.T) {
	rt := testRuntime(t, killSrc(1, "systemd"), nil)
	for _, params := range []string{`{"pid":0,"confirm":true,"name":"x"}`, `{"pid":-3,"confirm":true,"name":"x"}`} {
		killExpect(t, rt, params, pluginapi.E_INVALID, "pid")
	}
	killExpect(t, rt, `{"pid":1,"confirm":true,"name":"systemd"}`, pluginapi.E_UNAUTHORIZED, "init")
}

// TestKillRejectsSelf 护栏：拒绝向插件自身发信号（自身记录可读，判定发生在信号之前）。
func TestKillRejectsSelf(t *testing.T) {
	src := &fakeSource{records: []Record{{PID: 5000, Name: "process", State: 'S', Cmdline: "process"}}}
	rt := NewRuntime(Options{ProcRoot: t.TempDir(), SelfPID: 5000, MaskArgs: true, GOOS: "linux"}, src)
	killExpect(t, rt, `{"pid":5000,"confirm":true,"name":"process"}`, pluginapi.E_UNAUTHORIZED, "插件自身")
}

// TestKillRejectsKored 护栏：comm=kored 的 kore 主进程拒绝，且拒绝发生在信号之前。
func TestKillRejectsKored(t *testing.T) {
	src := killSrc(4242, "kored")
	rt := testRuntime(t, src, nil)
	killExpect(t, rt, `{"pid":4242,"confirm":true,"name":"kored"}`, pluginapi.E_UNAUTHORIZED, "kored")
	if len(src.signals) != 0 {
		t.Fatalf("拒绝后不应投递信号，实际 %v", src.signals)
	}
}

// TestKillRejectsKorePrefix 护栏：comm 以 kore- 前缀命名的组件进程拒绝。
func TestKillRejectsKorePrefix(t *testing.T) {
	rt := testRuntime(t, killSrc(4242, "kore-plugin-monitor"), nil)
	killExpect(t, rt, `{"pid":4242,"confirm":true,"name":"kore-plugin-monitor"}`, pluginapi.E_UNAUTHORIZED, "kore-")
}

// TestKillRejectsKernelThread 护栏：cmdline 为空（ps 的 [kthr] 口径）视为内核线程拒绝。
func TestKillRejectsKernelThread(t *testing.T) {
	src := &fakeSource{records: []Record{{PID: 77, Name: "kworker/0:1", State: 'S'}}}
	rt := testRuntime(t, src, nil)
	killExpect(t, rt, `{"pid":77,"confirm":true,"name":"kworker/0:1"}`, pluginapi.E_UNAUTHORIZED, "内核线程")
}

// TestKillRejectsProtectedConfig 护栏：KORE_PROTECTED_PIDS 名单拒绝。
func TestKillRejectsProtectedConfig(t *testing.T) {
	rt := NewRuntime(Options{
		ProcRoot: t.TempDir(), SelfPID: 999999, MaskArgs: true, GOOS: "linux",
		ProtectedPIDs: []int{4243},
	}, killSrc(4243, "db"))
	killExpect(t, rt, `{"pid":4243,"confirm":true,"name":"db"}`, pluginapi.E_UNAUTHORIZED, "KORE_PROTECTED_PIDS")
}

// TestKillRejectsParent 护栏：插件父进程（生产环境即 kored）拒绝。
func TestKillRejectsParent(t *testing.T) {
	rt := NewRuntime(Options{
		ProcRoot: t.TempDir(), SelfPID: 999999, ParentPID: 700, MaskArgs: true, GOOS: "linux",
	}, killSrc(700, "kored"))
	killExpect(t, rt, `{"pid":700,"confirm":true,"name":"kored"}`, pluginapi.E_UNAUTHORIZED, "kored")
}

// TestKillRejectsStateTreeSibling 护栏：exe 位于同一 kore 插件状态树（/plugins/ 同根）的进程拒绝。
func TestKillRejectsStateTreeSibling(t *testing.T) {
	src := &fakeSource{records: []Record{{
		PID: 800, Name: "monitor", State: 'S', Cmdline: "monitor --poll",
		Exe: "/srv/kore/.kore-local-state/plugins/monitor/0.2.1/bin/monitor",
	}}}
	rt := NewRuntime(Options{
		ProcRoot: t.TempDir(), SelfPID: 999999, MaskArgs: true, GOOS: "linux",
		SelfExe: "/srv/kore/.kore-local-state/plugins/process/0.3.0/bin/process",
	}, src)
	killExpect(t, rt, `{"pid":800,"confirm":true,"name":"monitor"}`, pluginapi.E_UNAUTHORIZED, "kore 插件进程")
}

// TestKillRejectsSocketDirExe 护栏：exe 与插件 socket 同目录的进程拒绝（socket 同目录口径）。
func TestKillRejectsSocketDirExe(t *testing.T) {
	src := &fakeSource{records: []Record{{PID: 801, Name: "kored", State: 'S', Cmdline: "kored serve", Exe: "/run/kore/kored"}}}
	rt := NewRuntime(Options{
		ProcRoot: t.TempDir(), SelfPID: 999999, MaskArgs: true, GOOS: "linux",
		SocketDir: "/run/kore",
	}, src)
	killExpect(t, rt, `{"pid":801,"confirm":true,"name":"kored"}`, pluginapi.E_UNAUTHORIZED, "kore")
}

// TestKillForceGate KILL 属不可恢复：无 force 拒绝；force+confirm 才投递。
func TestKillForceGate(t *testing.T) {
	src := killSrc(4245, "sleep")
	rt := testRuntime(t, src, nil)
	killExpect(t, rt, `{"pid":4245,"confirm":true,"name":"sleep","signal":"KILL"}`, pluginapi.E_INVALID, "force")
	result := killRun(t, rt, `{"pid":4245,"confirm":true,"name":"sleep","signal":"KILL","force":true}`)
	if !result.Killed || result.Signal != "KILL" {
		t.Fatalf("force+confirm 的 KILL 结果=%+v", result)
	}
	if len(src.signals) != 1 || src.signals[0] != SignalKILL {
		t.Fatalf("实际投递=%v", src.signals)
	}
}

// TestKillInvalidSignal 只允许 TERM/KILL/HUP/INT。
func TestKillInvalidSignal(t *testing.T) {
	rt := testRuntime(t, killSrc(4244, "sleep"), nil)
	killExpect(t, rt, `{"pid":4244,"confirm":true,"name":"sleep","signal":"USR1"}`, pluginapi.E_INVALID, "signal")
}

// TestKillNotFound 目标不存在返回 E_NOTFOUND。
func TestKillNotFound(t *testing.T) {
	rt := testRuntime(t, &fakeSource{}, nil)
	killExpect(t, rt, `{"pid":4242,"confirm":true,"name":"x"}`, pluginapi.E_NOTFOUND, "不存在")
}

// TestKillSignalErrors 投递失败按 errno 映射：EPERM → E_NEED_ROOT（附提权提示）、ESRCH → E_NOTFOUND。
func TestKillSignalErrors(t *testing.T) {
	denied := &fakeSource{records: []Record{{PID: 4242, Name: "other", State: 'S', Cmdline: "other --serve"}}, signalErr: syscall.EPERM}
	_, perr := testRuntime(t, denied, nil).Call("process.kill", json.RawMessage(`{"pid":4242,"confirm":true,"name":"other"}`))
	if perr == nil || perr.Code != "E_NEED_ROOT" || !contains(perr.Message, "sudo") {
		t.Fatalf("EPERM 映射=%+v，期望 E_NEED_ROOT 且附提权提示", perr)
	}
	gone := &fakeSource{records: []Record{{PID: 4242, Name: "other", State: 'S', Cmdline: "other --serve"}}, signalErr: syscall.ESRCH}
	killExpect(t, testRuntime(t, gone, nil), `{"pid":4242,"confirm":true,"name":"other"}`, pluginapi.E_NOTFOUND, "不存在")
}

// TestKillSignalWhitelist 允许的信号能原样投递（均带 confirm+name 合法入参）。
func TestKillSignalWhitelist(t *testing.T) {
	for _, tc := range []struct {
		signal string
		want   Signal
	}{
		{"", SignalTERM},
		{"TERM", SignalTERM},
		{"HUP", SignalHUP},
		{"INT", SignalINT},
	} {
		src := killSrc(4245, "sleep")
		rt := testRuntime(t, src, nil)
		params := `{"pid":4245,"confirm":true,"name":"sleep"}`
		if tc.signal != "" {
			params = fmt.Sprintf(`{"pid":4245,"confirm":true,"name":"sleep","signal":%q}`, tc.signal)
		}
		result := killRun(t, rt, params)
		if !result.Killed || result.StillAlive {
			t.Fatalf("signal=%q 结果=%+v", tc.signal, result)
		}
		if result.Name != "sleep" {
			t.Fatalf("结果 name=%q，期望 sleep", result.Name)
		}
		if len(src.signals) != 1 || src.signals[0] != tc.want {
			t.Fatalf("signal=%q 实际投递=%v，期望 %v", tc.signal, src.signals, tc.want)
		}
	}
}

// TestKillStillAlive 目标忽略信号时，500ms 窗口后如实返回 still_alive=true。
func TestKillStillAlive(t *testing.T) {
	src := &fakeSource{records: []Record{{PID: 4246, Name: "stubborn", State: 'S', Cmdline: "stubborn --loop"}}, probeState: 'S'}
	rt := testRuntime(t, src, nil)
	result := killRun(t, rt, `{"pid":4246,"confirm":true,"name":"stubborn"}`)
	if !result.Killed || !result.StillAlive {
		t.Fatalf("忽略信号的进程结果=%+v", result)
	}
	if result.WaitedMS < 450 {
		t.Fatalf("确认窗口 %dms 少于 450ms", result.WaitedMS)
	}
	if result.Note == "" {
		t.Fatal("still_alive=true 时应给出说明")
	}
}

// TestKillZombieCountsDead 僵尸进程视为已退出（避免把已死进程报成存活）。
func TestKillZombieCountsDead(t *testing.T) {
	src := &fakeSource{records: []Record{{PID: 4247, Name: "zombie", State: 'Z', Cmdline: "zombie"}}, probeState: 'Z'}
	rt := testRuntime(t, src, nil)
	result := killRun(t, rt, `{"pid":4247,"confirm":true,"name":"zombie"}`)
	if !result.Killed || result.StillAlive {
		t.Fatalf("僵尸进程结果=%+v", result)
	}
}

// TestKillRealSleep 起一个真实长睡子进程，用默认 TERM（confirm+name 四层全过）杀掉并确认不存活。
func TestKillRealSleep(t *testing.T) {
	sleeper := spawnRealSleep(t)
	pid := sleeper.Process.Pid
	rt := NewRuntime(Options{SelfPID: os.Getpid(), MaskArgs: true}, nil)
	result := killRun(t, rt, fmt.Sprintf(`{"pid":%d,"confirm":true,"name":"sleep"}`, pid))
	if !result.Killed || result.StillAlive {
		t.Fatalf("kill sleep 结果=%+v", result)
	}
	if result.Signal != "TERM" || result.PID != pid || result.Name != "sleep" {
		t.Fatalf("结果信号/pid/name=%q/%d/%q", result.Signal, result.PID, result.Name)
	}
	_ = sleeper.Wait() // 回收子进程；被信号终止时 Wait 返回 error，这里只关心收尸
	waitGone(t, pid, 2*time.Second)
}

// TestKillRealSleepKilledByPidReuseNameCheck PID 复用演练：对同一 pid 报一个错误的名字必须被拒，
// 且目标仍存活——证明复核发生在信号之前。
func TestKillRealSleepKilledByPidReuseNameCheck(t *testing.T) {
	sleeper := spawnRealSleep(t)
	pid := sleeper.Process.Pid
	t.Cleanup(func() {
		_ = syscall.Kill(pid, syscall.SIGKILL)
		_ = sleeper.Wait()
	})
	rt := NewRuntime(Options{SelfPID: os.Getpid(), MaskArgs: true}, nil)
	killExpect(t, rt, fmt.Sprintf(`{"pid":%d,"confirm":true,"name":"nginx"}`, pid), pluginapi.E_CONFLICT, "复用")
	if _, err := os.Stat(fmt.Sprintf("/proc/%d", pid)); err != nil {
		t.Fatalf("名称复核拒绝后目标进程应仍存活: %v", err)
	}
}
