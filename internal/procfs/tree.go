package procfs

import (
	"encoding/json"
	"fmt"
	"sort"

	"github.com/pengtang1024/kore/pkg/pluginapi"
)

// TreeNode 是进程树中的一个节点；depth 以请求的根为 0（子树模式）或各树根为 0（全树模式）。
// children_count 是该进程在本次 /proc 扫描中的直接子进程数（截断前口径，供前端提示）。
type TreeNode struct {
	PID           int    `json:"pid"`
	PPID          int    `json:"ppid"`
	Name          string `json:"name"`
	Depth         int    `json:"depth"`
	ChildrenCount int    `json:"children_count"`
}

// treeResult 是 process.tree 结构；truncated 表示因深度或节点上限被截断。
// root 参数缺省时输出全系统森林，roots 是各棵树的根（ppid 为 0 或父进程不在扫描集内）。
type treeResult struct {
	Schema    string     `json:"schema"`
	Roots     []int      `json:"roots"`
	Count     int        `json:"count"`
	Total     int        `json:"total"`
	Truncated bool       `json:"truncated"`
	MaxDepth  int        `json:"max_depth"`
	MaxNodes  int        `json:"max_nodes"`
	Nodes     []TreeNode `json:"nodes"`
}

// treeParams 是 process.tree 入参：root 缺省/0 表示全系统树；正整数表示以该 pid 为根的子树。
type treeParams struct {
	Root int `json:"root"`
}

// queueItem 是 BFS 队列元素：待展开的 pid 与它自身的深度。
type queueItem struct {
	pid   int
	depth int
}

// callTree 从一次 /proc 扫描构建 ppid 反查表，BFS 输出进程树。
// 节点 ≤500、深度 ≤10；命中上限时 truncated=true，不静默截断。
// 环防护：真实 /proc 的 PPID 由内核保证不成环，但 visited 集合让任何成环输入
// （伪造执行面 / 名字空间拼贴）也不会死循环。
func (r *Runtime) callTree(raw json.RawMessage) (json.RawMessage, *pluginapi.Error) {
	var p treeParams
	if perr := decodeParams(raw, &p); perr != nil {
		return nil, perr
	}
	if p.Root < 0 {
		return nil, invalid("root 必须为非负整数（0 或缺省表示全系统树）")
	}
	records, err := r.src.Scan()
	if err != nil {
		return nil, classify(err)
	}
	byPID, children := indexTree(records)
	result := treeResult{
		Schema: "process.tree.v2", Total: len(byPID),
		MaxDepth: MaxTreeDepth, MaxNodes: MaxTreeNodes,
	}
	if p.Root > 0 {
		root, ok := byPID[p.Root]
		if !ok {
			return nil, notFound(fmt.Sprintf("pid %d 不存在或已退出", p.Root))
		}
		result.Roots = []int{p.Root}
		nodes := []TreeNode{nodeOf(root, 0, children)}
		result.Nodes, result.Truncated = expandTree([]queueItem{{pid: p.Root}}, nodes, byPID, children)
	} else {
		roots := treeRoots(byPID)
		result.Roots = roots
		queue := make([]queueItem, 0, len(roots))
		nodes := make([]TreeNode, 0, len(roots))
		for _, root := range roots {
			nodes = append(nodes, nodeOf(byPID[root], 0, children))
			queue = append(queue, queueItem{pid: root})
		}
		result.Nodes, result.Truncated = expandTree(queue, nodes, byPID, children)
	}
	result.Count = len(result.Nodes)
	return encode(result)
}

// indexTree 把扫描记录整理成 pid 索引与 ppid 反查表；无名 / 不可读记录
// 没有 ppid 可靠信息，不进树。children 按 pid 升序排序保证输出确定。
func indexTree(records []Record) (map[int]Record, map[int][]int) {
	byPID := make(map[int]Record, len(records))
	children := make(map[int][]int, len(records))
	for _, record := range records {
		if record.Restricted || record.Name == "" {
			continue
		}
		byPID[record.PID] = record
		children[record.PPID] = append(children[record.PPID], record.PID)
	}
	for pid := range children {
		sort.Ints(children[pid])
	}
	return byPID, children
}

// treeRoots 找出全树模式的根：ppid 为 0，或父进程不在本次扫描集内（孤儿被重新认领前）。
// 返回按 pid 升序，保证多根顺序确定。
func treeRoots(byPID map[int]Record) []int {
	roots := make([]int, 0, 8)
	for pid, record := range byPID {
		if record.PPID == 0 {
			roots = append(roots, pid)
			continue
		}
		if _, parentAlive := byPID[record.PPID]; !parentAlive {
			roots = append(roots, pid)
		}
	}
	sort.Ints(roots)
	return roots
}

// expandTree 按 BFS 逐层展开队列；上限命中时立即收口并返回 truncated=true。
// 单根子树与多根全树共用：队列里预置根节点即可。
func expandTree(queue []queueItem, nodes []TreeNode, byPID map[int]Record, children map[int][]int) ([]TreeNode, bool) {
	truncated := false
	visited := make(map[int]bool, len(byPID))
	for _, item := range queue {
		visited[item.pid] = true
	}
	for len(queue) > 0 {
		item := queue[0]
		queue = queue[1:]
		childPIDs := children[item.pid]
		if len(childPIDs) == 0 {
			continue
		}
		if item.depth >= MaxTreeDepth {
			truncated = true
			continue
		}
		for _, childPID := range childPIDs {
			if visited[childPID] { // 成环 / 重复父（不应出现）也不二次展开
				continue
			}
			if len(nodes) >= MaxTreeNodes {
				truncated = true
				return nodes, truncated
			}
			visited[childPID] = true
			nodes = append(nodes, nodeOf(byPID[childPID], item.depth+1, children))
			queue = append(queue, queueItem{pid: childPID, depth: item.depth + 1})
		}
	}
	return nodes, truncated
}

// nodeOf 把采集记录转成树节点；children_count 取全扫描口径的直接子进程数。
func nodeOf(record Record, depth int, children map[int][]int) TreeNode {
	return TreeNode{
		PID: record.PID, PPID: record.PPID, Name: record.Name,
		Depth: depth, ChildrenCount: len(children[record.PID]),
	}
}
