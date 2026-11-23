//go:build linux

package procfs

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"testing"
	"time"
)

// TestMain 充当"可控 sleep 进程"：子进程以自定义 argv 启动本测试二进制时，
// 直接睡 300s，从而能构造出 cmdline 恰为 `sleep 300 --password=secret` 的真实进程。
func TestMain(m *testing.M) {
	if os.Getenv("PROC_TEST_SLEEPER") == "1" {
		time.Sleep(300 * time.Second)
		os.Exit(0)
	}
	os.Exit(m.Run())
}

// fakeSource 是可控执行面：用于确定性排序 / 上限 / 护栏 / 故障测试。
type fakeSource struct {
	records    []Record
	scanErr    error
	statErr    error
	signalErr  error
	probeState byte
	probeErr   error
	signals    []Signal
	probed     []int
	niceCalls  []niceCall
	niceErr    error
}

// niceCall 记录一次 SetNice 调用。
type niceCall struct {
	pid   int
	value int
}

// Scan 返回记录副本。
func (f *fakeSource) Scan() ([]Record, error) {
	if f.scanErr != nil {
		return nil, f.scanErr
	}
	return append([]Record(nil), f.records...), nil
}

// Stat 按 pid 查找记录；找不到返回 fs.ErrNotExist。
func (f *fakeSource) Stat(pid int) (Record, error) {
	if f.statErr != nil {
		return Record{}, f.statErr
	}
	for _, record := range f.records {
		if record.PID == pid {
			return record, nil
		}
	}
	return Record{}, fs.ErrNotExist
}

// Signal 记录投递的信号并返回预设错误。
func (f *fakeSource) Signal(pid int, sig Signal) error {
	f.signals = append(f.signals, sig)
	return f.signalErr
}

// SetNice 记录优先级调整并同步改写记录（模拟真实内核：renice 后 stat 读到新值）。
func (f *fakeSource) SetNice(pid int, value int) error {
	f.niceCalls = append(f.niceCalls, niceCall{pid: pid, value: value})
	for i := range f.records {
		if f.records[i].PID == pid {
			f.records[i].Nice = value
		}
	}
	return f.niceErr
}

// Probe 返回预设状态字符。
func (f *fakeSource) Probe(pid int) (byte, error) {
	f.probed = append(f.probed, pid)
	if f.probeErr != nil {
		return 0, f.probeErr
	}
	return f.probeState, nil
}

// spawnSleeper 以自定义 argv 启动本测试二进制作为长睡进程；默认 SIGTERM 即退出。
// 注意 comm 恒为测试二进制名（argv 改名不影响 comm），需要 comm="sleep" 的场景用 spawnRealSleep。
func spawnSleeper(t *testing.T, argv ...string) *exec.Cmd {
	t.Helper()
	command := exec.Command(os.Args[0])
	command.Args = argv
	command.Env = append(os.Environ(), "PROC_TEST_SLEEPER=1")
	command.Stdout, command.Stderr = io.Discard, io.Discard
	if err := command.Start(); err != nil {
		t.Fatalf("启动 sleeper 失败: %v", err)
	}
	t.Cleanup(func() {
		if command.ProcessState == nil {
			_ = command.Process.Kill()
			_, _ = command.Process.Wait()
		}
	})
	waitExists(t, command.Process.Pid, 2*time.Second)
	return command
}

// spawnRealSleep 启动真实 /bin/sleep 300（comm=sleep），并等 exec 完成——
// 子进程在 exec 前后 /proc/<pid> 已存在，cmdline 可能短暂读到空串（见
// raceprobe_test.go 的真实瞬态），所以先等 comm 稳定再做 name 复核。
func spawnRealSleep(t *testing.T) *exec.Cmd {
	t.Helper()
	path, err := exec.LookPath("sleep")
	if err != nil {
		t.Fatalf("找不到 sleep 命令: %v", err)
	}
	command := exec.Command(path, "300")
	command.Stdout, command.Stderr = io.Discard, io.Discard
	if err := command.Start(); err != nil {
		t.Fatalf("启动 sleep 失败: %v", err)
	}
	t.Cleanup(func() {
		if command.ProcessState == nil {
			_ = command.Process.Kill()
			_, _ = command.Process.Wait()
		}
	})
	waitExists(t, command.Process.Pid, 2*time.Second)
	waitComm(t, command.Process.Pid, "sleep", 2*time.Second)
	return command
}

// waitComm 轮询 /proc/<pid>/stat 直到 comm 等于 want（exec 完成的可靠信号）。
func waitComm(t *testing.T, pid int, want string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if info, err := parseStatFile(pid); err == nil && info.Name == want {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("pid %d 的 comm 在 %s 内未变成 %q", pid, timeout, want)
}

// parseStatFile 读取 /proc/<pid>/stat 并解析（用于复核真实 nice / comm 生效）。
func parseStatFile(pid int) (statInfo, error) {
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return statInfo{}, err
	}
	return parseStat(string(data))
}

// waitExists 等待 /proc/<pid> 出现（子进程 exec 完成）。
func waitExists(t *testing.T, pid int, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(fmt.Sprintf("/proc/%d", pid)); err == nil {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("pid %d 在 %s 内未出现", pid, timeout)
}

// waitGone 等待 /proc/<pid> 消失（进程被回收或已死）。
func waitGone(t *testing.T, pid int, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(fmt.Sprintf("/proc/%d", pid)); errors.Is(err, fs.ErrNotExist) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("pid %d 在 %s 内未消失", pid, timeout)
}

// testRuntime 构造使用 fakeSource 的 Runtime；ProcRoot 指向临时目录避免读真实 /proc。
func testRuntime(t *testing.T, src Source, now func() time.Time) *Runtime {
	t.Helper()
	return NewRuntime(Options{
		ProcRoot: t.TempDir(),
		SelfPID:  999999,
		MaskArgs: true,
		GOOS:     "linux",
		Now:      now,
	}, src)
}

// fixedClock 返回可推进的假时钟。
func fixedClock(start time.Time) (func() time.Time, func(time.Duration)) {
	current := start
	return func() time.Time { return current }, func(delta time.Duration) { current = current.Add(delta) }
}
