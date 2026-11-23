//go:build linux

package procfs

import "testing"

// TestProtectionReasonMatrix 保护名单判定矩阵：每类一条记录，断言原因非空且关键词正确；
// 普通进程必须返回空串（否则会把合法目标误判成受保护）。
func TestProtectionReasonMatrix(t *testing.T) {
	selfPID := 900000
	rt := NewRuntime(Options{
		ProcRoot: t.TempDir(), SelfPID: selfPID, ParentPID: 500,
		SocketDir: "/run/kore", SelfExe: "/srv/kore/.kore-local-state/plugins/process/0.3.0/bin/process",
		GOOS: "linux", ProtectedPIDs: []int{600},
	}, &fakeSource{})
	cases := []struct {
		name   string
		record Record
		hint   string
	}{
		{"pid1", Record{PID: 1, Name: "systemd", Cmdline: "/sbin/init"}, "init"},
		{"self", Record{PID: selfPID, Name: "process", Cmdline: "process"}, "插件自身"},
		{"env-listed", Record{PID: 600, Name: "db", Cmdline: "db"}, "KORE_PROTECTED_PIDS"},
		{"parent", Record{PID: 500, Name: "kored", Cmdline: "kored serve"}, "kored"},
		{"kored-comm", Record{PID: 700, Name: "kored", Cmdline: "kored serve"}, "kored"},
		{"kore-prefix", Record{PID: 701, Name: "kore-plugin-monitor", Cmdline: "kore-plugin-monitor"}, "kore-"},
		{"kernel-thread", Record{PID: 702, Name: "kworker/2:3"}, "内核线程"},
		{"state-tree", Record{PID: 703, Name: "monitor", Cmdline: "monitor", Exe: "/srv/kore/.kore-local-state/plugins/monitor/0.2.1/bin/monitor"}, "kore 插件进程"},
		{"socket-dir", Record{PID: 704, Name: "kored-dev", Cmdline: "kored-dev", Exe: "/run/kore/kored-dev"}, "kore"},
		{"normal", Record{PID: 800, Name: "nginx", Cmdline: "nginx -g daemon off;", Exe: "/usr/sbin/nginx"}, ""},
		{"normal-sleep", Record{PID: 801, Name: "sleep", Cmdline: "sleep 300"}, ""},
	}
	for _, tc := range cases {
		reason := rt.protectionReason(tc.record)
		if tc.hint == "" {
			if reason != "" {
				t.Fatalf("%s: 普通进程被判受保护: %q", tc.name, reason)
			}
			continue
		}
		if reason == "" || !contains(reason, tc.hint) {
			t.Fatalf("%s: 原因=%q，期望含 %q", tc.name, reason, tc.hint)
		}
	}
}

// TestKernelThreadEdgeCases 内核线程口径边界：restricted（读取失败）与空名不算内核线程，
// 有 cmdline 的用户态进程不算；cmdline 为空 + stat 可读才是。
func TestKernelThreadEdgeCases(t *testing.T) {
	cases := []struct {
		name   string
		record Record
		want   bool
	}{
		{"plain-kthread", Record{PID: 2, Name: "kthreadd"}, true},
		{"restricted-not-kthread", Record{PID: 3, Name: "hidepid", Restricted: true}, false},
		{"nameless-not-kthread", Record{PID: 4}, false},
		{"userspace", Record{PID: 5, Name: "bash", Cmdline: "bash"}, false},
	}
	for _, tc := range cases {
		if got := isKernelThread(tc.record); got != tc.want {
			t.Fatalf("%s: isKernelThread=%v，期望 %v", tc.name, got, tc.want)
		}
	}
}

// TestPluginsPrefixOf 状态树前缀推断：标准布局 / 多段版本 / 非 plugins 路径。
func TestPluginsPrefixOf(t *testing.T) {
	cases := []struct {
		selfExe string
		want    string
	}{
		{"/srv/kore/.kore-local-state/plugins/process/0.3.0/bin/process", "/srv/kore/.kore-local-state/plugins/"},
		{"/opt/kore/state/plugins/process/1.0.0/bin/process", "/opt/kore/state/plugins/"},
		{"/tmp/go-build/process", ""}, // 开发态直跑：关闭该检查
		{"", ""},
	}
	for _, tc := range cases {
		if got := pluginsPrefixOf(tc.selfExe); got != tc.want {
			t.Fatalf("pluginsPrefixOf(%q)=%q，期望 %q", tc.selfExe, got, tc.want)
		}
	}
}
