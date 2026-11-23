package main

import (
	"encoding/json"
	"log/slog"
	"strings"

	"github.com/pengtang1024/kore-plugin-process/internal/procfs"
	"github.com/pengtang1024/kore/pkg/pluginapi"
)

// dispatch 把请求分发为响应帧；第二个返回值表示收到 kore.shutdown。
func dispatch(request *pluginapi.Request, cfg config, procRuntime *procfs.Runtime, logger *slog.Logger) (*pluginapi.Response, bool) {
	response := &pluginapi.Response{Type: pluginapi.TypeResponse, ID: request.ID, ReqID: request.ReqID}
	switch request.Method {
	case "kore.shutdown":
		response.Result = json.RawMessage(`{"ok":true}`)
		logger.Info("收到 kore.shutdown，优雅退出")
		return response, true
	case "kore.ping":
		response.Result = pingResult(cfg.startedAt)
		return response, false
	case "kore.reload":
		response.Result = json.RawMessage(`{"ok":true,"config_digest":""}`)
		return response, false
	}
	method, ok := normalizeMethod(request.Method, cfg.id)
	if !ok {
		response.Error = methodError(pluginapi.E_NOTFOUND, "未知方法: "+request.Method)
		return response, false
	}
	result, perr := procRuntime.Call(method, request.Params)
	if perr != nil {
		response.Error = perr
		response.Result = nil
		return response, false
	}
	response.Result = result
	return response, false
}

// businessVerbs 是 process 插件的业务方法动作清单：info.json 的 method_capabilities、
// 本文件的方法分发与 method_capabilities_test.go 的一致性断言共用这一份事实来源。
// 新增方法必须同步业务实现（Runtime.Call 分支）、info.json 与测试分类表。
var businessVerbs = []string{"list", "detail", "kill", "tree", "renice"}

// normalizeMethod 把 <id>.<verb> 或裸 <verb> 归一为 process.<verb>，非业务方法返回 false。
func normalizeMethod(method, id string) (string, bool) {
	verb := strings.TrimPrefix(method, id+".")
	for _, candidate := range businessVerbs {
		if verb == candidate {
			return "process." + candidate, true
		}
	}
	return "", false
}

// methodError 构造协议错误体。
func methodError(code string, msg string) *pluginapi.Error {
	return &pluginapi.Error{Code: code, Message: msg}
}
