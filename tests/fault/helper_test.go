//go:build linux

// Package fault 对 process 插件做四类故障注入（kill / timeout / disk / cgroup）：
// 每个用例把可控执行面替换为始终返回 FaultError 的实现，断言四个方法都把执行面
// 故障映射成结构化协议错误码，而不是 panic 或伪成功。
package fault

import (
	"encoding/json"
	"io/fs"
	"testing"

	"github.com/pengtang1024/kore-plugin-process/internal/procfs"
	"github.com/pengtang1024/kore/pkg/pluginapi"
)

// faultSource 按注入类别让每个执行面方法都失败。
type faultSource struct {
	kind procfs.FaultKind
}

// Scan 注入故障。
func (f faultSource) Scan() ([]procfs.Record, error) {
	return nil, procfs.NewFaultError(f.kind, "scan 执行面故障")
}

// Stat 注入故障。
func (f faultSource) Stat(int) (procfs.Record, error) {
	return procfs.Record{}, procfs.NewFaultError(f.kind, "stat 执行面故障")
}

// Signal 注入故障。
func (f faultSource) Signal(int, procfs.Signal) error {
	return procfs.NewFaultError(f.kind, "signal 执行面故障")
}

// Probe 注入故障。
func (f faultSource) Probe(int) (byte, error) {
	return 0, procfs.NewFaultError(f.kind, "probe 执行面故障")
}

// methods 是故障注入覆盖的四个方法及其合法参数。
var methods = []struct {
	name   string
	method string
	params string
}{
	{"list", "process.list", `{"limit":5}`},
	{"detail", "process.detail", `{"pid":4242}`},
	{"kill", "process.kill", `{"pid":4242}`},
	{"tree", "process.tree", `{"pid":4242}`},
}

// newRuntime 构造指向注入执行面的 Runtime。
func newRuntime(t *testing.T, src procfs.Source) *procfs.Runtime {
	t.Helper()
	return procfs.NewRuntime(procfs.Options{
		ProcRoot: t.TempDir(), SelfPID: 0, MaskArgs: true, GOOS: "linux",
	}, src)
}

// assertFault 对四个方法注入同一类故障，断言 nil 结果 + 预期错误码与类别关键词。
func assertFault(t *testing.T, kind procfs.FaultKind, wantCode, wantHint string) {
	t.Helper()
	rt := newRuntime(t, faultSource{kind: kind})
	for _, m := range methods {
		t.Run(m.name, func(t *testing.T) {
			result, perr := rt.Call(m.method, json.RawMessage(m.params))
			if perr == nil {
				t.Fatalf("%s 注入 %s 后未返回错误（结果=%s）", m.method, kind, result)
			}
			if result != nil {
				t.Fatalf("%s 出错时不应带结果: %s", m.method, result)
			}
			if perr.Code != wantCode {
				t.Fatalf("%s 错误码=%q，期望 %q（message=%q）", m.method, perr.Code, wantCode, perr.Message)
			}
			if !contains(perr.Message, wantHint) {
				t.Fatalf("%s 错误信息 %q 未含类别关键词 %q", m.method, perr.Message, wantHint)
			}
		})
	}
}

// healthySource 是无故障执行面：证明故障断言不是"永远失败"的假通过。
type healthySource struct {
	records []procfs.Record
	signals []procfs.Signal
}

// Scan 返回预设记录。
func (h *healthySource) Scan() ([]procfs.Record, error) {
	return append([]procfs.Record(nil), h.records...), nil
}

// Stat 按 pid 返回记录。
func (h *healthySource) Stat(pid int) (procfs.Record, error) {
	for _, record := range h.records {
		if record.PID == pid {
			return record, nil
		}
	}
	return procfs.Record{}, fs.ErrNotExist
}

// Signal 记录投递。
func (h *healthySource) Signal(pid int, sig procfs.Signal) error {
	h.signals = append(h.signals, sig)
	return nil
}

// Probe 返回已退出状态，便于 kill 立即返回。
func (h *healthySource) Probe(int) (byte, error) { return 'Z', nil }

// sanityNoFault 验证无故障时四个方法均正常返回。
func sanityNoFault(t *testing.T) {
	t.Helper()
	src := &healthySource{records: []procfs.Record{
		{PID: 4242, PPID: 1, Name: "root", State: 'S'},
		{PID: 4243, PPID: 4242, Name: "child", State: 'S'},
		{PID: 4244, PPID: 4242, Name: "victim", State: 'S'},
	}}
	rt := newRuntime(t, src)
	cases := []struct {
		method string
		params string
	}{
		{"process.list", `{"limit":5}`},
		{"process.detail", `{"pid":4242}`},
		{"process.tree", `{"pid":4242}`},
		{"process.kill", `{"pid":4244}`},
	}
	for _, tc := range cases {
		result, perr := rt.Call(tc.method, json.RawMessage(tc.params))
		if perr != nil {
			t.Fatalf("无故障时 %s 意外返回错误: %+v", tc.method, perr)
		}
		if len(result) == 0 {
			t.Fatalf("无故障时 %s 返回空结果", tc.method)
		}
	}
	if len(src.signals) != 1 || src.signals[0] != procfs.SignalTERM {
		t.Fatalf("无故障 kill 未投递 TERM: %v", src.signals)
	}
}

// verifyErrorContract 断言错误体是标准协议结构（code 与 message 均非空）。
func verifyErrorContract(t *testing.T, perr *pluginapi.Error) {
	t.Helper()
	if perr == nil {
		t.Fatal("错误体为 nil")
	}
	if perr.Code == "" || perr.Message == "" {
		t.Fatalf("错误体字段不完整: %+v", perr)
	}
}

// contains 是 strings.Contains 的本地封装，避免每个 fault 文件重复 import strings。
func contains(s, sub string) bool {
	if len(sub) > len(s) {
		return false
	}
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
