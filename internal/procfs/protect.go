package procfs

import (
	"fmt"
	"path/filepath"
	"strings"
)

// protectionReason 返回非空说明表示该进程受保护，kill / renice 一律拒绝；
// 返回空串表示不在保护名单内。kill 与 renice 共用同一份策略——保护是进程的属性，
// 不随动作不同而放松。判定只依赖 /proc 自身字段与运行期注入的选项，不做任何网络或
// 文件系统旁路探测。
//
// 名单（对应任务口径：PID 1 / kored 自身 / kore 插件进程 / 内核线程 / 显式名单）：
//  1. PID 1：init，用户态全部进程的祖先；
//  2. 插件自身（SelfPID）；
//  3. KORE_PROTECTED_PIDS 显式名单；
//  4. 插件父进程（生产环境即 kored 守护进程）；
//  5. comm=kored 的 kore 主进程；
//  6. comm 带 kore- 前缀的 kore 组件进程；
//  7. 内核线程（cmdline 为空且 stat 可读，ps(1) 的 [kthr] 同口径）；
//  8. 可执行文件位于 kore 插件状态树（/plugins/ 同根）或 socket 同目录的进程。
func (r *Runtime) protectionReason(record Record) string {
	pid := record.PID
	if pid == 1 {
		return "PID 1（init）是用户态所有进程的祖先，终止它将导致系统不可用"
	}
	if r.opts.SelfPID > 0 && pid == r.opts.SelfPID {
		return "process 插件自身（终止后进程管理能力不可用）"
	}
	for _, listed := range r.opts.ProtectedPIDs {
		if pid == listed {
			return fmt.Sprintf("pid %d 在 KORE_PROTECTED_PIDS 保护名单内", pid)
		}
	}
	if r.opts.ParentPID > 0 && pid == r.opts.ParentPID {
		return "kore 主进程（本插件的父进程，即 kored 守护进程）"
	}
	if record.Name != "" && !record.Restricted {
		if record.Name == "kored" {
			return "kore 主进程（comm=kored）：终止后 kore 全部能力不可用"
		}
		if strings.HasPrefix(record.Name, "kore-") {
			return "kore 组件进程（comm 以 kore- 前缀命名）"
		}
		if isKernelThread(record) {
			return "内核线程（cmdline 为空）：不接受用户态信号 / 优先级管理"
		}
	}
	if r.underKoreRuntime(record) {
		return "kore 插件进程（可执行文件位于 kore 运行目录或 socket 同目录）"
	}
	return ""
}

// isKernelThread 判定内核线程：cmdline 为空（ps(1) 的 [kthr] 口径）且挂在 kthreadd 子树上
// （ppid 0 = kthreadd 自身，ppid 2 = 其子内核线程）。后者是关键：用户态进程在 execve 中段
// 也会瞬态读到空 cmdline（comm 已换、argv 未接），但它的 ppid 是真实用户态父进程，
// 不落在 {0,2}，不会误判。restricted 记录是读取失败而非内核线程，显式排除。
func isKernelThread(record Record) bool {
	return record.Cmdline == "" && !record.Restricted && record.Name != "" &&
		(record.PPID == 0 || record.PPID == 2)
}

// underKoreRuntime 判断进程可执行文件是否落在 kore 运行资产目录内：
// 插件状态树（由插件自身 exe 推断的 <state>/plugins/ 前缀）或 socket 所在目录。
// 两个前缀都来自运行期注入（main 用 /proc/self/exe 与 KORE_PLUGIN_SOCK），测试可注入。
func (r *Runtime) underKoreRuntime(record Record) bool {
	if record.Exe == "" {
		return false
	}
	if r.pluginsPrefix != "" && strings.HasPrefix(record.Exe, r.pluginsPrefix) {
		return true
	}
	if r.opts.SocketDir != "" && filepath.Dir(record.Exe) == r.opts.SocketDir {
		return true
	}
	return false
}
