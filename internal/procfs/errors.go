package procfs

import (
	"errors"
	"io/fs"

	"github.com/pengtang1024/kore/pkg/pluginapi"
)

// classify 把执行面错误映射为协议错误码：故障注入类别优先，其次业务错误，最后按 errno 归类。
// 所有 message 只描述原因与操作，不拼接宿主绝对路径。
func classify(err error) *pluginapi.Error {
	var fe *FaultError
	if errors.As(err, &fe) {
		switch fe.Kind {
		case FaultKill:
			return upstream("进程被信号 9 终止: " + fe.Message)
		case FaultTimeout:
			return upstream("调用超时: " + fe.Message)
		case FaultDisk:
			return internalErr("磁盘满: " + fe.Message)
		case FaultCgroup:
			return unsupported("cgroup 限额触发: " + fe.Message)
		}
	}
	var oe *OpError
	if errors.As(err, &oe) {
		return mapOpError(oe)
	}
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return notFound("进程不存在或已退出")
	case errors.Is(err, fs.ErrPermission):
		return unauthorized("权限不足：只能操作同 uid 进程")
	}
	return internalErr("读取 /proc 失败")
}

// mapOpError 把业务错误类别映射为协议错误码。
func mapOpError(oe *OpError) *pluginapi.Error {
	switch oe.Kind {
	case ErrInvalid:
		return invalid(oe.Message)
	case ErrNotFound:
		return notFound(oe.Message)
	case ErrPermission:
		return unauthorized(oe.Message)
	case ErrUnsupported:
		return unsupported(oe.Message)
	default:
		return internalErr(oe.Message)
	}
}

// invalid 构造参数/策略校验失败错误。
func invalid(msg string) *pluginapi.Error {
	return &pluginapi.Error{Code: pluginapi.E_INVALID, Message: msg}
}

// notFound 构造资源不存在错误。
func notFound(msg string) *pluginapi.Error {
	return &pluginapi.Error{Code: pluginapi.E_NOTFOUND, Message: msg}
}

// unauthorized 构造权限 / 保护策略拒绝错误。
func unauthorized(msg string) *pluginapi.Error {
	return &pluginapi.Error{Code: pluginapi.E_UNAUTHORIZED, Message: msg}
}

// unsupported 构造平台 / 能力不支持错误。
func unsupported(msg string) *pluginapi.Error {
	return &pluginapi.Error{Code: pluginapi.E_UNSUPPORTED, Message: msg}
}

// upstream 构造上游故障错误。
func upstream(msg string) *pluginapi.Error {
	return &pluginapi.Error{Code: pluginapi.E_UPSTREAM, Message: msg}
}

// internalErr 构造内部错误。
func internalErr(msg string) *pluginapi.Error {
	return &pluginapi.Error{Code: pluginapi.E_INTERNAL, Message: msg}
}
