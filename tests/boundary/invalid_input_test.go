//go:build linux

// 异常输入：非法 PID、非法信号、非法进程名、非对象 params、非法排序 / 窗口 / limit。
// 这些用例复用 tests/e2e 与 internal/procfs 已有 10 个代表性断言的契约，但固定在
// tests/boundary/ 下，由 production 判据① 门禁直接取证。
package boundary

import (
	"testing"

	"github.com/pengtang1024/kore/pkg/pluginapi"
)

// TestInvalidInputRejectsRepresentativeCases 对非法入参逐一断言协议错误码，
// 宁可在入口拒绝，也不把脏参数带进 /proc 读取或信号投递。
func TestInvalidInputRejectsRepresentativeCases(t *testing.T) {
	rt := newRuntime(t, "")
	cases := []struct {
		name     string
		method   string
		params   string
		wantCode string
		wantHint string
	}{
		{"detail pid 为 0", "process.detail", `{"pid":0}`, pluginapi.E_INVALID, "pid"},
		{"detail pid 为负", "process.detail", `{"pid":-7}`, pluginapi.E_INVALID, "pid"},
		{"detail params 非对象", "process.detail", `[]`, pluginapi.E_INVALID, "params"},
		{"kill 缺 confirm", "process.kill", `{"pid":4242,"name":"x"}`, pluginapi.E_INVALID, "confirm"},
		{"kill 非法信号", "process.kill", `{"pid":4242,"confirm":true,"name":"x","signal":"USR2"}`, pluginapi.E_INVALID, "signal"},
		{"kill KILL 缺 force", "process.kill", `{"pid":4242,"confirm":true,"name":"x","signal":"KILL"}`, pluginapi.E_INVALID, "force"},
		{"kill 进程名为空白", "process.kill", `{"pid":4242,"confirm":true,"name":"   "}`, pluginapi.E_INVALID, "name"},
		{"kill params 非法 JSON", "process.kill", `{"pid":`, pluginapi.E_INVALID, "params"},
		{"renice 缺 value", "process.renice", `{"pid":4242,"confirm":true}`, pluginapi.E_INVALID, "value"},
		{"renice nice 越界", "process.renice", `{"pid":4242,"value":20,"confirm":true}`, pluginapi.E_INVALID, "nice"},
		{"ports 负 pid", "process.ports", `{"pid":-1}`, pluginapi.E_INVALID, "pid"},
		{"trend pid 为 0", "process.trend", `{"pid":0}`, pluginapi.E_INVALID, "pid"},
		{"trend window 越界", "process.trend", `{"pid":1,"window":61}`, pluginapi.E_INVALID, "window"},
		{"list 非法排序键", "process.list", `{"sort":"bogus"}`, pluginapi.E_INVALID, "sort"},
		{"list limit 越界", "process.list", `{"limit":0}`, pluginapi.E_INVALID, "limit"},
		{"未知方法", "process.nope", `{}`, pluginapi.E_NOTFOUND, "未知方法"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			perr := callError(t, rt, tc.method, tc.params)
			if perr.Code != tc.wantCode {
				t.Fatalf("错误码=%q，期望 %q（message=%q）", perr.Code, tc.wantCode, perr.Message)
			}
			if !contains(perr.Message, tc.wantHint) {
				t.Fatalf("错误信息 %q 未含 %q", perr.Message, tc.wantHint)
			}
		})
	}
}

// contains 是 strings.Contains 的本地封装，避免本文件为一行判断引入额外 import。
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
