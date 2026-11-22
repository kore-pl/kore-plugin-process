//go:build linux

package e2e

import (
	"encoding/json"
	"net"
	"os/exec"
	"testing"
	"time"

	"github.com/pengtang1024/kore/pkg/pluginapi"
)

// 本文件是 E2E 的协议帧辅助：假内核侧的 hello / request / response 读写。
// handshake 读 kore.hello 并回 ok 响应。
func handshake(t *testing.T, conn net.Conn) {
	t.Helper()
	request := readRequest(t, conn)
	if request.Method != "kore.hello" {
		t.Fatalf("首帧应为 kore.hello，实际 %s", request.Method)
	}
	response := &pluginapi.Response{
		Type: pluginapi.TypeResponse, ID: request.ID, ReqID: request.ReqID,
		Result: json.RawMessage(`{"ok":true,"protocol":"kore.plugin.v1"}`),
	}
	if err := pluginapi.WriteJSON(conn, response); err != nil {
		t.Fatalf("回写 hello 失败: %v", err)
	}
}

// request 发送一次方法调用并读回响应。
func request(t *testing.T, conn net.Conn, id, method string, params any) *pluginapi.Response {
	t.Helper()
	raw, err := json.Marshal(params)
	if err != nil {
		t.Fatalf("序列化 %s 参数失败: %v", method, err)
	}
	req := &pluginapi.Request{
		Type: pluginapi.TypeRequest, ID: id, ReqID: "r-" + id, Method: method, Params: raw,
	}
	if err := pluginapi.WriteJSON(conn, req); err != nil {
		t.Fatalf("发送 %s 失败: %v", method, err)
	}
	return readResponse(t, conn)
}

// mustCall 要求调用成功并返回 result 原文。
func mustCall(t *testing.T, conn net.Conn, id, method string, params any) string {
	t.Helper()
	response := request(t, conn, id, method, params)
	if response.Error != nil {
		t.Fatalf("%s 失败: %+v", method, response.Error)
	}
	return string(response.Result)
}

// readRequest 读下一帧并断言是 request。
func readRequest(t *testing.T, conn net.Conn) *pluginapi.Request {
	t.Helper()
	env, err := pluginapi.DecodeFrame(readPayload(t, conn))
	if err != nil {
		t.Fatalf("解码请求失败: %v", err)
	}
	request, ok := env.(*pluginapi.Request)
	if !ok {
		t.Fatalf("期望 request，实际 %T", env)
	}
	return request
}

// readResponse 读下一帧并断言是 response。
func readResponse(t *testing.T, conn net.Conn) *pluginapi.Response {
	t.Helper()
	env, err := pluginapi.DecodeFrame(readPayload(t, conn))
	if err != nil {
		t.Fatalf("解码响应失败: %v", err)
	}
	response, ok := env.(*pluginapi.Response)
	if !ok {
		t.Fatalf("期望 response，实际 %T", env)
	}
	return response
}

// readPayload 读一整帧。
func readPayload(t *testing.T, conn net.Conn) []byte {
	t.Helper()
	payload, err := pluginapi.ReadFrame(conn, pluginapi.MaxFrameSize)
	if err != nil {
		t.Fatalf("读帧失败: %v", err)
	}
	return payload
}

// waitExit 等待插件进程退出。
func waitExit(t *testing.T, process *exec.Cmd) {
	t.Helper()
	done := make(chan error, 1)
	go func() { done <- process.Wait() }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("插件退出异常: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("插件未在 5s 内退出")
	}
}
