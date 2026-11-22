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

// Probe 返回预设状态字符。
func (f *fakeSource) Probe(pid int) (byte, error) {
	f.probed = append(f.probed, pid)
	if f.probeErr != nil {
		return 0, f.probeErr
	}
	return f.probeState, nil
}

// spawnSleeper 以自定义 argv 启动本测试二进制作为长睡进程；默认 SIGTERM 即退出。
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
