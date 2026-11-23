//go:build linux

package procfs

import (
	"os"
	"syscall"
	"testing"
)

// TestRaceProbeSpawnStat 探测并守护 exec 后的真实瞬态：`exec.Command.Start` 返回后，
// /proc/<pid>/cmdline 仍可能短暂读到空串（本机可复现），但该进程的 ppid 是拉起它的
// 父进程，不落在 {0,2}。因此"空 cmdline"单独不足以判定内核线程——该瞬态用户进程
// 既不能进保护名单（否则 kill / renice 被间歇性误拒），其 comm 也必须已是契约值。
func TestRaceProbeSpawnStat(t *testing.T) {
	src := newProcSource("")
	rt := NewRuntime(Options{SelfPID: os.Getpid(), MaskArgs: true}, src)
	emptySeen := 0
	for i := 0; i < 60; i++ {
		sleeper := spawnRealSleep(t)
		pid := sleeper.Process.Pid
		record, err := src.Stat(pid)
		if err != nil {
			t.Fatalf("i=%d stat: %v", i, err)
		}
		if record.Cmdline == "" {
			emptySeen++
			if record.PPID == 0 || record.PPID == 2 {
				t.Fatalf("i=%d pid=%d 用户态子进程的 ppid=%d 落在内核线程口径", i, pid, record.PPID)
			}
			if isKernelThread(record) {
				t.Fatalf("i=%d pid=%d 空 cmdline 被误判为内核线程: %+v", i, pid, record)
			}
			if reason := rt.protectionReason(record); reason != "" {
				t.Fatalf("i=%d pid=%d 空 cmdline 瞬态被误判受保护: %q", i, pid, reason)
			}
		}
		if err := syscall.Kill(pid, syscall.SIGKILL); err != nil && err != syscall.ESRCH {
			t.Fatalf("i=%d kill: %v", i, err)
		}
		_ = sleeper.Wait()
	}
	t.Logf("空 cmdline 瞬态观测 %d/60 次；全部未被误判为内核线程或受保护进程", emptySeen)
}
