package procfs

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/pengtang1024/kore/pkg/pluginapi"
)

// 资源趋势与守护建议的边界常量。
const (
	// historyCapacity 是系统级趋势环形缓冲的固定点数。
	historyCapacity = 60
	// historyInterval 是最小采样间隔：不足则复用上一点，不产生新采样。
	historyInterval = 30 * time.Second
	// trendCapacity 是单进程趋势的固定点数。
	trendCapacity = 60
	// trendMinInterval 是单进程 CPU 差分的最小间隔，太短返回 insufficient_data。
	trendMinInterval = 2 * time.Second
	// trendMaxPIDs 是趋势 state 里保留的进程数上限（防无界增长）。
	trendMaxPIDs = 128
	// trendDefaultWindow / trendMaxWindow 是 process.trend 返回点数。
	trendDefaultWindow = 10
	trendMaxWindow     = 60
	// historyFileName / trendFileName 是插件 state 下的落盘文件（0600）。
	historyFileName = "history.json"
	trendFileName   = "trend.json"
	// watchdogBoundary 是守护建议的固定边界声明。
	watchdogBoundary = "kore 不做进程守护：本方法只生成 systemd unit 建议文本，不写 unit、不启动服务、不常驻守护循环；守护与自启归 systemd。"
)

// HistoryPoint 是系统级资源趋势的一个采样点。
type HistoryPoint struct {
	T           string  `json:"t"`
	CPUPercent  float64 `json:"cpu_percent"`
	MemUsedKiB  uint64  `json:"mem_used_kib"`
	MemTotalKiB uint64  `json:"mem_total_kib"`
	Load1       float64 `json:"load1"`
}

// historyState 是 history.json 的落盘形状（Prev* 只用于下次 CPU 差分）。
type historyState struct {
	Points    []HistoryPoint `json:"points"`
	Truncated bool           `json:"truncated"`
	PrevTotal uint64         `json:"prev_total"`
	PrevBusy  uint64         `json:"prev_busy"`
	PrevAt    string         `json:"prev_at"`
}

// historyParams 是 process.history 入参（无字段，保留显式空对象）。
type historyParams struct{}

// historyResult 是 process.history 的结果。
type historyResult struct {
	Schema    string         `json:"schema"`
	Points    []HistoryPoint `json:"points"`
	IntervalS int            `json:"interval_s"`
	Count     int            `json:"count"`
	Truncated bool           `json:"truncated"`
	Sampled   bool           `json:"sampled"`
	Note      string         `json:"note,omitempty"`
}

// callHistory 只在被调用时采样：间隔不足 historyInterval 就复用上一点，
// 插件内没有任何 ticker / 定时 goroutine（G-19）。
func (r *Runtime) callHistory(raw json.RawMessage) (json.RawMessage, *pluginapi.Error) {
	var p historyParams
	if perr := decodeParams(raw, &p); perr != nil {
		return nil, perr
	}
	r.stateMu.Lock()
	defer r.stateMu.Unlock()
	state, perr := r.loadHistoryState()
	if perr != nil {
		return nil, perr
	}
	now := r.now()
	result := historyResult{Schema: "process.history.v1", IntervalS: int(historyInterval / time.Second), Truncated: state.Truncated}
	lastAt, _ := time.Parse(time.RFC3339, state.PrevAt)
	if len(state.Points) > 0 && !lastAt.IsZero() && now.Sub(lastAt) < historyInterval {
		result.Points = state.Points
		result.Count = len(state.Points)
		result.Note = fmt.Sprintf("距上次采样不足 %ds，复用上一点（零 ticker：只在本方法被调用时采样）", int(historyInterval/time.Second))
		return encode(result)
	}
	point, prevTotal, prevBusy, perr := r.sampleSystem(now, state.PrevTotal, state.PrevBusy)
	if perr != nil {
		return nil, perr
	}
	state.Points = append(state.Points, point)
	if len(state.Points) > historyCapacity {
		state.Points = state.Points[len(state.Points)-historyCapacity:]
		state.Truncated = true
	}
	state.PrevTotal, state.PrevBusy = prevTotal, prevBusy
	state.PrevAt = point.T
	if perr := r.saveHistoryState(state); perr != nil {
		return nil, perr
	}
	result.Points = state.Points
	result.Count = len(state.Points)
	result.Truncated = state.Truncated
	result.Sampled = true
	return encode(result)
}

// sampleSystem 读 /proc/stat、/proc/meminfo、/proc/loadavg 生成一个采样点。
func (r *Runtime) sampleSystem(now time.Time, prevTotal, prevBusy uint64) (HistoryPoint, uint64, uint64, *pluginapi.Error) {
	statData, err := os.ReadFile(filepath.Join(r.opts.ProcRoot, "stat"))
	if err != nil {
		return HistoryPoint{}, 0, 0, classify(err)
	}
	total, busy := parseSystemCPU(statData)
	if total == 0 {
		return HistoryPoint{}, 0, 0, internalErr("解析 /proc/stat cpu 行失败")
	}
	percent := 0.0
	if prevTotal > 0 && total > prevTotal && busy >= prevBusy {
		percent = float64(busy-prevBusy) / float64(total-prevTotal) * 100
		percent = round2(percent)
	}
	memTotal, memAvail := parseMemInfo(statData)
	if memTotal == 0 {
		memData, memErr := os.ReadFile(filepath.Join(r.opts.ProcRoot, "meminfo"))
		if memErr != nil {
			return HistoryPoint{}, 0, 0, classify(memErr)
		}
		memTotal, memAvail = parseMemInfo(memData)
	}
	load1 := 0.0
	if loadData, loadErr := os.ReadFile(filepath.Join(r.opts.ProcRoot, "loadavg")); loadErr == nil {
		fields := strings.Fields(string(loadData))
		if len(fields) > 0 {
			load1, _ = strconv.ParseFloat(fields[0], 64)
		}
	}
	return HistoryPoint{
		T: now.UTC().Format(time.RFC3339), CPUPercent: percent,
		MemUsedKiB: memTotal - memAvail, MemTotalKiB: memTotal, Load1: load1,
	}, total, busy, nil
}

// parseSystemCPU 解析 /proc/stat 首行，返回总 tick 与 busy tick（不含 idle+iowait）。
func parseSystemCPU(data []byte) (total, busy uint64) {
	for _, line := range strings.Split(string(data), "\n") {
		if !strings.HasPrefix(line, "cpu ") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 5 {
			return 0, 0
		}
		values := make([]uint64, 0, len(fields)-1)
		for _, field := range fields[1:] {
			value, err := strconv.ParseUint(field, 10, 64)
			if err != nil {
				break
			}
			values = append(values, value)
		}
		if len(values) < 4 {
			return 0, 0
		}
		for _, value := range values {
			total += value
		}
		idle := values[3]
		if len(values) > 4 {
			idle += values[4]
		}
		return total, total - idle
	}
	return 0, 0
}

// parseMemInfo 解析 /proc/meminfo 的 MemTotal / MemAvailable（KiB）。
func parseMemInfo(data []byte) (total, available uint64) {
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		value, err := strconv.ParseUint(fields[1], 10, 64)
		if err != nil {
			continue
		}
		switch fields[0] {
		case "MemTotal:":
			total = value
		case "MemAvailable:":
			available = value
		}
	}
	return total, available
}

// round2 保留两位小数，避免趋势点出现长浮点尾巴。
func round2(value float64) float64 {
	return float64(int64(value*100+0.5)) / 100
}

// TrendPoint 是单进程趋势的一个采样点。
type TrendPoint struct {
	T          string  `json:"t"`
	CPUPercent float64 `json:"cpu_percent"`
	RSSKiB     int64   `json:"rss_kib"`
}

// trendProc 是单进程上次采样（tick 基线 + 已保留的点）。
type trendProc struct {
	Ticks  uint64       `json:"ticks"`
	At     string       `json:"at"`
	Points []TrendPoint `json:"points"`
}

// trendState 是 trend.json 的落盘形状。
type trendState struct {
	Procs map[string]trendProc `json:"procs"`
}

// trendParams 是 process.trend 入参。
type trendParams struct {
	PID    int `json:"pid"`
	Window int `json:"window,omitempty"`
}

// trendResult 是 process.trend 的结果。
type trendResult struct {
	Schema           string       `json:"schema"`
	PID              int          `json:"pid"`
	Name             string       `json:"name"`
	Points           []TrendPoint `json:"points"`
	Count            int          `json:"count"`
	Window           int          `json:"window"`
	InsufficientData bool         `json:"insufficient_data"`
	Hint             string       `json:"hint,omitempty"`
}

// callTrend 用 /proc/<pid>/stat 的 utime+stime 差分算 CPU；上次采样持久化在 state，
// 两次调用间隔 < trendMinInterval 时返回 insufficient_data，不伪造数据。
func (r *Runtime) callTrend(raw json.RawMessage) (json.RawMessage, *pluginapi.Error) {
	var p trendParams
	if perr := decodeParams(raw, &p); perr != nil {
		return nil, perr
	}
	if p.PID < 1 {
		return nil, invalid(fmt.Sprintf("pid 必须为正整数（实际 %d）", p.PID))
	}
	window := p.Window
	if window == 0 {
		window = trendDefaultWindow
	}
	if window < 1 || window > trendMaxWindow {
		return nil, invalid(fmt.Sprintf("window 必须在 1–%d 之间", trendMaxWindow))
	}
	r.stateMu.Lock()
	defer r.stateMu.Unlock()
	state, perr := r.loadTrendState()
	if perr != nil {
		return nil, perr
	}
	record, err := r.src.Stat(p.PID)
	if err != nil {
		return nil, classify(err)
	}
	now := r.now()
	key := strconv.Itoa(p.PID)
	proc := state.Procs[key]
	points := tailTrend(proc.Points, window)
	result := trendResult{Schema: "process.trend.v1", PID: p.PID, Name: record.Name, Window: window, Points: points, Count: len(points)}
	lastAt, _ := time.Parse(time.RFC3339, proc.At)
	if proc.At == "" {
		state.Procs[key] = trendProc{Ticks: record.Ticks(), At: now.UTC().Format(time.RFC3339), Points: proc.Points}
		if perr := r.saveTrendState(state); perr != nil {
			return nil, perr
		}
		result.InsufficientData = true
		result.Hint = fmt.Sprintf("首次采样：已记录 tick 基线，请至少 %ds 后再调用", int(trendMinInterval/time.Second))
		return encode(result)
	}
	elapsed := now.Sub(lastAt)
	if elapsed < trendMinInterval {
		result.InsufficientData = true
		result.Hint = fmt.Sprintf("两次调用间隔 %dms 不足 %ds，已复用上次采样；请稍后再试", elapsed.Milliseconds(), int(trendMinInterval/time.Second))
		return encode(result)
	}
	ticks := record.Ticks()
	if ticks < proc.Ticks {
		// PID 复用：tick 回绕，重置基线而不是给出负增量。
		state.Procs[key] = trendProc{Ticks: ticks, At: now.UTC().Format(time.RFC3339), Points: proc.Points}
		if perr := r.saveTrendState(state); perr != nil {
			return nil, perr
		}
		result.InsufficientData = true
		result.Hint = "检测到 pid 复用（tick 回绕），已重置基线；请稍后再调用"
		return encode(result)
	}
	percent := round2(float64(ticks-proc.Ticks) / float64(CLKTCK) / elapsed.Seconds() * 100)
	proc.Points = append(proc.Points, TrendPoint{T: now.UTC().Format(time.RFC3339), CPUPercent: percent, RSSKiB: record.RSSKiB})
	if len(proc.Points) > trendCapacity {
		proc.Points = proc.Points[len(proc.Points)-trendCapacity:]
	}
	proc.Ticks, proc.At = ticks, now.UTC().Format(time.RFC3339)
	state.Procs[key] = proc
	if perr := r.saveTrendState(state); perr != nil {
		return nil, perr
	}
	result.Points = tailTrend(proc.Points, window)
	result.Count = len(result.Points)
	return encode(result)
}

// tailTrend 返回最近 window 个点。
func tailTrend(points []TrendPoint, window int) []TrendPoint {
	if len(points) > window {
		points = points[len(points)-window:]
	}
	out := make([]TrendPoint, len(points))
	copy(out, points)
	return out
}

// watchdogParams 是 process.watchdog.suggest 入参：pid / name 二选一。
type watchdogParams struct {
	PID  int    `json:"pid"`
	Name string `json:"name"`
}

// watchdogResult 是守护自启建议（纯文本，不落盘、不执行）。
type watchdogResult struct {
	Schema     string `json:"schema"`
	PID        int    `json:"pid"`
	Name       string `json:"name"`
	UnitName   string `json:"unit_name"`
	ExecStart  string `json:"exec_start"`
	UserUnit   string `json:"user_unit"`
	SystemUnit string `json:"system_unit"`
	ScopeGuide string `json:"scope_guide"`
	Boundary   string `json:"boundary"`
}

// callWatchdogSuggest 只生成 systemd unit 文本与二选一说明；kore 不实现守护循环。
func (r *Runtime) callWatchdogSuggest(raw json.RawMessage) (json.RawMessage, *pluginapi.Error) {
	var p watchdogParams
	if perr := decodeParams(raw, &p); perr != nil {
		return nil, perr
	}
	name := strings.TrimSpace(p.Name)
	if p.PID == 0 && name == "" {
		return nil, invalid("必须提供 pid 或 name 之一")
	}
	if p.PID < 0 {
		return nil, invalid("pid 必须为正整数")
	}
	var record Record
	if p.PID > 0 {
		stat, err := r.src.Stat(p.PID)
		if err != nil {
			return nil, classify(err)
		}
		record = stat
	} else {
		records, err := r.src.Scan()
		if err != nil {
			return nil, classify(err)
		}
		sort.Slice(records, func(i, j int) bool { return records[i].PID < records[j].PID })
		found := false
		for _, candidate := range records {
			if candidate.Name == name {
				record, found = candidate, true
				break
			}
		}
		if !found {
			return nil, notFound(fmt.Sprintf("未找到名为 %q 的进程", name))
		}
	}
	exec := strings.TrimSpace(maskCmdline(record.Cmdline, r.opts.MaskArgs))
	if exec == "" {
		exec = strings.TrimSpace(record.Exe)
	}
	if exec == "" {
		exec = record.Name
	}
	unitName := "kore-guard-" + sanitizeUnitName(record.Name) + ".service"
	return encode(watchdogResult{
		Schema: "process.watchdog.suggest.v1", PID: record.PID, Name: record.Name, UnitName: unitName,
		ExecStart:  exec,
		UserUnit:   renderUnit(record, exec, "default.target"),
		SystemUnit: renderUnit(record, exec, "multi-user.target"),
		ScopeGuide: "二选一，均由你在终端执行：\n" +
			"① 用户级（无需 root）：把 user_unit 写入 ~/.config/systemd/user/" + unitName + "，然后 `systemctl --user daemon-reload && systemctl --user enable --now " + unitName + "`\n" +
			"② 系统级（需 root）：把 system_unit 写入 /etc/systemd/system/" + unitName + "，然后 `sudo systemctl daemon-reload && sudo systemctl enable --now " + unitName + "`",
		Boundary: watchdogBoundary,
	})
}

// renderUnit 渲染 systemd unit 文本；纯函数（同一输入恒等），便于幂等测试。
func renderUnit(record Record, exec, wantedBy string) string {
	return "[Unit]\n" +
		"Description=kore 守护建议：" + record.Name + "（由用户创建，kore 不代管）\n" +
		"After=network.target\n\n" +
		"[Service]\n" +
		"Type=simple\n" +
		"ExecStart=" + exec + "\n" +
		"Restart=always\n" +
		"RestartSec=5s\n\n" +
		"[Install]\n" +
		"WantedBy=" + wantedBy + "\n"
}

// sanitizeUnitName 把进程名转成合法 unit 片段：小写、非字母数字转 -、去首尾 -。
func sanitizeUnitName(name string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(name) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
	}
	clean := strings.Trim(b.String(), "-")
	if clean == "" {
		return "process"
	}
	return clean
}

// stateDirPath 返回插件 state 下文件名；StateDir 为空时返回空串（内存态）。
func (r *Runtime) stateDirPath(name string) string {
	if r.opts.StateDir == "" {
		return ""
	}
	return filepath.Join(r.opts.StateDir, name)
}

// loadHistoryState 读 history.json；文件不存在返回空态，损坏报错（不静默丢历史）。
func (r *Runtime) loadHistoryState() (historyState, *pluginapi.Error) {
	state := historyState{Points: []HistoryPoint{}}
	path := r.stateDirPath(historyFileName)
	if path == "" {
		if r.memHistory.Points == nil {
			r.memHistory.Points = []HistoryPoint{}
		}
		return r.memHistory, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return state, nil
		}
		return state, classify(err)
	}
	if err := json.Unmarshal(data, &state); err != nil {
		return state, internalErr("history state 损坏: " + err.Error())
	}
	if state.Points == nil {
		state.Points = []HistoryPoint{}
	}
	return state, nil
}

// saveHistoryState 原子写 history.json（0600）。
func (r *Runtime) saveHistoryState(state historyState) *pluginapi.Error {
	if r.opts.StateDir == "" {
		r.memHistory = state
		return nil
	}
	if err := writeStateAtomic(r.stateDirPath(historyFileName), state); err != nil {
		return internalErr("写 history state 失败: " + err.Error())
	}
	return nil
}

// loadTrendState 读 trend.json；不存在返回空表。
func (r *Runtime) loadTrendState() (trendState, *pluginapi.Error) {
	state := trendState{Procs: map[string]trendProc{}}
	path := r.stateDirPath(trendFileName)
	if path == "" {
		if r.memTrend.Procs == nil {
			r.memTrend.Procs = map[string]trendProc{}
		}
		return r.memTrend, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return state, nil
		}
		return state, classify(err)
	}
	if err := json.Unmarshal(data, &state); err != nil {
		return state, internalErr("trend state 损坏: " + err.Error())
	}
	if state.Procs == nil {
		state.Procs = map[string]trendProc{}
	}
	return state, nil
}

// saveTrendState 原子写 trend.json（0600），并裁剪 pid 数量上限。
func (r *Runtime) saveTrendState(state trendState) *pluginapi.Error {
	if len(state.Procs) > trendMaxPIDs {
		keys := make([]string, 0, len(state.Procs))
		for key := range state.Procs {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys[:len(keys)-trendMaxPIDs] {
			delete(state.Procs, key)
		}
	}
	if r.opts.StateDir == "" {
		r.memTrend = state
		return nil
	}
	if err := writeStateAtomic(r.stateDirPath(trendFileName), state); err != nil {
		return internalErr("写 trend state 失败: " + err.Error())
	}
	return nil
}

// writeStateAtomic 写 0600 临时文件后 rename，保证重启续读不会读到半截 JSON。
func writeStateAtomic(path string, value any) error {
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".state-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}
