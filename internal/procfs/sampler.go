package procfs

import (
	"math"
	"sync"
	"time"
)

// Sampler 保存上一次 process.list 的 CPU tick 快照，用于差分成 cpu_pct。
// 没有后台 ticker：只在 list 调用时更新基线；Mutex 保证并发请求不会互相污染快照。
type Sampler struct {
	mu    sync.Mutex
	ticks map[int]uint64
	at    time.Time
}

// NewSampler 构造空采样器：首次 Sample 只建基线，cpu_pct 全 0。
func NewSampler() *Sampler { return &Sampler{ticks: make(map[int]uint64)} }

// Sample 计算本批进程相对上次快照的 CPU 百分比：
//
//	cpu_pct = (ticks_now - ticks_prev) / CLKTCK / 距上次采样秒数 × 100
//
// 首次调用（无基线）与本次新出现的进程返回 0；单核归一，多核满载可超过 100。
func (s *Sampler) Sample(now time.Time, records []Record) (map[int]float64, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	current := make(map[int]uint64, len(records))
	for _, record := range records {
		current[record.PID] = record.Ticks()
	}
	percent := make(map[int]float64, len(records))
	if s.at.IsZero() {
		s.ticks, s.at = current, now
		return percent, true
	}
	elapsed := now.Sub(s.at).Seconds()
	if elapsed <= 0 {
		return percent, false
	}
	for pid, ticks := range current {
		previous, ok := s.ticks[pid]
		if !ok || ticks < previous {
			continue
		}
		delta := float64(ticks-previous) / float64(CLKTCK)
		percent[pid] = math.Round(delta/elapsed*10000) / 100
	}
	s.ticks, s.at = current, now
	return percent, false
}
