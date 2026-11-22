package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/pengtang1024/kore/pkg/pluginapi"
)

// dispatchCall 把方法调用打到 dispatch 并返回响应。
func dispatchCall(t *testing.T, method, params string) *pluginapi.Response {
	t.Helper()
	request := &pluginapi.Request{
		Type: pluginapi.TypeRequest, ID: "id", ReqID: "r",
		Method: method, Params: json.RawMessage(params),
	}
	response, shutdown := dispatch(request, testConfig(), newTestRuntime(), testLogger())
	if shutdown {
		t.Fatalf("%s 不应触发 shutdown", method)
	}
	return response
}

// TestDispatchList 真实扫描 /proc，裸方法名与全名都能命中。
func TestDispatchList(t *testing.T) {
	for _, method := range []string{"process.list", "list"} {
		response := dispatchCall(t, method, `{"limit":5}`)
		if response.Error != nil {
			t.Fatalf("%s 失败: %+v", method, response.Error)
		}
		result := string(response.Result)
		if !strings.Contains(result, `"schema":"process.list.v1"`) || !strings.Contains(result, `"items":[`) {
			t.Fatalf("%s 结果不符: %s", method, result)
		}
	}
}

// TestDispatchDetail 当前进程详情可读，且回显请求 pid。
func TestDispatchDetail(t *testing.T) {
	response := dispatchCall(t, "process.detail", fmt.Sprintf(`{"pid":%d}`, os.Getpid()))
	if response.Error != nil {
		t.Fatalf("detail 失败: %+v", response.Error)
	}
	result := string(response.Result)
	if !strings.Contains(result, `"schema":"process.detail.v1"`) || !strings.Contains(result, fmt.Sprintf(`"pid":%d`, os.Getpid())) {
		t.Fatalf("detail 结果不符: %s", result)
	}
}

// TestDispatchTree 当前进程树至少包含根节点。
func TestDispatchTree(t *testing.T) {
	response := dispatchCall(t, "process.tree", fmt.Sprintf(`{"pid":%d}`, os.Getpid()))
	if response.Error != nil {
		t.Fatalf("tree 失败: %+v", response.Error)
	}
	if !strings.Contains(string(response.Result), `"schema":"process.tree.v1"`) {
		t.Fatalf("tree 结果不符: %s", response.Result)
	}
}

// TestDispatchKillGuards 经协议层验证 pid 1 / 自身护栏。
func TestDispatchKillGuards(t *testing.T) {
	pidOne := dispatchCall(t, "process.kill", `{"pid":1}`)
	if pidOne.Error == nil || pidOne.Error.Code != pluginapi.E_INVALID {
		t.Fatalf("pid=1 错误=%+v，期望 E_INVALID", pidOne.Error)
	}
	self := dispatchCall(t, "process.kill", fmt.Sprintf(`{"pid":%d}`, os.Getpid()))
	if self.Error == nil || self.Error.Code != pluginapi.E_UNAUTHORIZED {
		t.Fatalf("kill 自身错误=%+v，期望 E_UNAUTHORIZED", self.Error)
	}
}

// TestDispatchPing 存活指标正常返回。
func TestDispatchPing(t *testing.T) {
	response := dispatchCall(t, "kore.ping", `{}`)
	if !strings.Contains(string(response.Result), `"status":"ready"`) {
		t.Fatalf("ping 结果=%s", response.Result)
	}
}

// TestDispatchUnknown 未知方法返回 E_NOTFOUND 且不带结果。
func TestDispatchUnknown(t *testing.T) {
	response := dispatchCall(t, "process.exec", `{}`)
	if response.Error == nil || response.Error.Code != pluginapi.E_NOTFOUND {
		t.Fatalf("未知方法错误=%+v，期望 E_NOTFOUND", response.Error)
	}
	if response.Result != nil {
		t.Fatalf("出错时不应带结果: %s", response.Result)
	}
}
