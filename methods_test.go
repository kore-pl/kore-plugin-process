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

// TestDispatchTree 子树模式包含根节点（v2 契约）。
func TestDispatchTree(t *testing.T) {
	response := dispatchCall(t, "process.tree", fmt.Sprintf(`{"root":%d}`, os.Getpid()))
	if response.Error != nil {
		t.Fatalf("tree 失败: %+v", response.Error)
	}
	if !strings.Contains(string(response.Result), `"schema":"process.tree.v2"`) {
		t.Fatalf("tree 结果不符: %s", response.Result)
	}
	fullTree := dispatchCall(t, "process.tree", `{}`)
	if fullTree.Error != nil || !strings.Contains(string(fullTree.Result), `"schema":"process.tree.v2"`) {
		t.Fatalf("全树结果不符: %+v %s", fullTree.Error, fullTree.Result)
	}
}

// TestDispatchKillGuards 经协议层验证保护名单：PID 1 与插件自身都返回 E_UNAUTHORIZED。
func TestDispatchKillGuards(t *testing.T) {
	pidOne := dispatchCall(t, "process.kill", `{"pid":1,"confirm":true,"name":"systemd"}`)
	if pidOne.Error == nil || pidOne.Error.Code != pluginapi.E_UNAUTHORIZED {
		t.Fatalf("pid=1 错误=%+v，期望 E_UNAUTHORIZED", pidOne.Error)
	}
	self := dispatchCall(t, "process.kill", fmt.Sprintf(`{"pid":%d,"confirm":true,"name":"process"}`, os.Getpid()))
	if self.Error == nil || self.Error.Code != pluginapi.E_UNAUTHORIZED {
		t.Fatalf("kill 自身错误=%+v，期望 E_UNAUTHORIZED", self.Error)
	}
}

// TestDispatchRenice 经协议层验证 renice 分发：缺确认 E_INVALID；目标缺失 E_NOTFOUND。
// 真实 renice 链路由 internal/procfs 的真实 sleeper 用例覆盖，这里只钉协议接线。
func TestDispatchRenice(t *testing.T) {
	noConfirm := dispatchCall(t, "process.renice", fmt.Sprintf(`{"pid":%d,"value":10}`, os.Getpid()))
	if noConfirm.Error == nil || noConfirm.Error.Code != pluginapi.E_INVALID {
		t.Fatalf("缺 confirm 错误=%+v，期望 E_INVALID", noConfirm.Error)
	}
	gone := dispatchCall(t, "process.renice", `{"pid":4000000,"value":10,"confirm":true,"name":"x"}`)
	if gone.Error == nil || gone.Error.Code != pluginapi.E_NOTFOUND {
		t.Fatalf("目标缺失错误=%+v，期望 E_NOTFOUND", gone.Error)
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
