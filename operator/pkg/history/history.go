// Package history is the operator's optional migration-metrics module. It
// records every Migration phase transition with wall-clock timings and
// derives per-step durations, workload accessibility and the downtime
// window of each migration, keeping a bounded in-memory history that the
// REST API serves to the dashboard. The module can be enabled and disabled
// at runtime: while disabled it drops transitions and reports enabled=false.
package history

import (
	"sync"
	"time"
)

// DefaultLimit bounds the number of migrations kept when main does not
// override it.
const DefaultLimit = 100

// Terminal phases (mirrors v1alpha1 without importing the API package so the
// module stays free of k8s dependencies).
const (
	phaseCompleted = "Completed"
	phaseFailed    = "Failed"
)

// Accessible reports whether the migrated workload is reachable during a
// phase: before the source pod is deleted (Pending/Syncing) and after the
// restore (Completed) the application serves traffic; the window in between
// is the migration's downtime. Failed is reported as not accessible until an
// operator intervenes.
func Accessible(phase string) bool {
	switch phase {
	case "", "Pending", "Syncing", "Completed":
		return true
	default:
		return false
	}
}

// Step is one phase a migration went through.
type Step struct {
	Phase      string     `json:"phase"`
	Message    string     `json:"message,omitempty"`
	StartedAt  time.Time  `json:"startedAt"`
	EndedAt    *time.Time `json:"endedAt,omitempty"`
	DurationMs int64      `json:"durationMs"`
	Accessible bool       `json:"accessible"`
}

// Record is the recorded history of one migration.
type Record struct {
	Name             string     `json:"name"`
	Namespace        string     `json:"namespace"`
	Workload         string     `json:"workload,omitempty"`
	SourcePod        string     `json:"sourcePod,omitempty"`
	DestinationPod   string     `json:"destinationPod,omitempty"`
	SourceNode       string     `json:"sourceNode,omitempty"`
	TargetNode       string     `json:"targetNode,omitempty"`
	ProcessMigration bool       `json:"processMigration"`
	VolumeMigration  bool       `json:"volumeMigration"`
	Phase            string     `json:"phase"`
	Message          string     `json:"message,omitempty"`
	Accessible       bool       `json:"accessible"`
	StartedAt        time.Time  `json:"startedAt"`
	CompletedAt      *time.Time `json:"completedAt,omitempty"`
	TotalMs          int64      `json:"totalMs"`
	DowntimeMs       int64      `json:"downtimeMs"`
	Steps            []Step     `json:"steps"`
	// Seeded marks records rebuilt from CRs after an operator restart:
	// start/completion times are known but per-step detail is not.
	Seeded bool `json:"seeded,omitempty"`
}

func (rec *Record) terminal() bool {
	return rec.Phase == phaseCompleted || rec.Phase == phaseFailed
}

// Transition is one Migration phase change reported by the controller.
type Transition struct {
	Namespace        string
	Name             string
	Workload         string
	SourcePod        string
	DestinationPod   string
	SourceNode       string
	TargetNode       string
	ProcessMigration bool
	VolumeMigration  bool
	Phase            string
	Message          string
	Time             time.Time
}

// Summary aggregates the recorded migrations.
type Summary struct {
	Total         int              `json:"total"`
	Active        int              `json:"active"`
	Completed     int              `json:"completed"`
	Failed        int              `json:"failed"`
	SuccessRate   float64          `json:"successRate"`
	AvgTotalMs    int64            `json:"avgTotalMs"`
	AvgDowntimeMs int64            `json:"avgDowntimeMs"`
	PhaseAvgMs    map[string]int64 `json:"phaseAvgMs"`
}

// Store is the thread-safe, bounded migration history.
type Store struct {
	mu      sync.Mutex
	enabled bool
	limit   int
	order   []string // record keys, oldest first
	records map[string]*Record
}

// NewStore returns a Store keeping at most limit migrations (DefaultLimit
// when limit <= 0), collecting only while enabled.
func NewStore(enabled bool, limit int) *Store {
	if limit <= 0 {
		limit = DefaultLimit
	}
	return &Store{enabled: enabled, limit: limit, records: make(map[string]*Record)}
}

// Enabled reports whether the module is collecting.
func (s *Store) Enabled() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.enabled
}

// SetEnabled toggles collection at runtime. Already-recorded history is kept
// so re-enabling does not start from scratch.
func (s *Store) SetEnabled(enabled bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.enabled = enabled
}

// RecordTransition appends a phase change to the migration's history,
// closing the previous step. Dropped while the module is disabled.
func (s *Store) RecordTransition(tr Transition) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.enabled {
		return
	}
	if tr.Time.IsZero() {
		tr.Time = time.Now()
	}

	key := tr.Namespace + "/" + tr.Name
	rec, ok := s.records[key]
	if !ok {
		rec = &Record{Name: tr.Name, Namespace: tr.Namespace, StartedAt: tr.Time}
		s.records[key] = rec
		s.order = append(s.order, key)
		s.evictLocked()
	}

	// Identifying fields can resolve late (e.g. sourcePod during Pending), so
	// refresh them on every transition.
	rec.Workload = tr.Workload
	rec.SourceNode = tr.SourceNode
	rec.TargetNode = tr.TargetNode
	rec.ProcessMigration = tr.ProcessMigration
	rec.VolumeMigration = tr.VolumeMigration
	if tr.SourcePod != "" {
		rec.SourcePod = tr.SourcePod
	}
	if tr.DestinationPod != "" {
		rec.DestinationPod = tr.DestinationPod
	}
	rec.Phase = tr.Phase
	rec.Message = tr.Message
	rec.Accessible = Accessible(tr.Phase)

	// Close the step the migration just left.
	if n := len(rec.Steps); n > 0 && rec.Steps[n-1].EndedAt == nil {
		last := &rec.Steps[n-1]
		if last.Phase == tr.Phase {
			last.Message = tr.Message // duplicate transition: refresh only
			return
		}
		ended := tr.Time
		last.EndedAt = &ended
		last.DurationMs = ended.Sub(last.StartedAt).Milliseconds()
	}

	if rec.terminal() {
		ended := tr.Time
		rec.CompletedAt = &ended
		rec.TotalMs = ended.Sub(rec.StartedAt).Milliseconds()
		rec.DowntimeMs = downtime(rec.Steps)
		return
	}
	rec.Steps = append(rec.Steps, Step{
		Phase:      tr.Phase,
		Message:    tr.Message,
		StartedAt:  tr.Time,
		Accessible: Accessible(tr.Phase),
	})
}

// Seed restores records (typically rebuilt from Migration CRs after an
// operator restart) without overwriting live entries.
func (s *Store) Seed(records []Record) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range records {
		rec := records[i]
		key := rec.Namespace + "/" + rec.Name
		if _, exists := s.records[key]; exists {
			continue
		}
		rec.Seeded = true
		rec.Accessible = Accessible(rec.Phase)
		if rec.CompletedAt != nil {
			rec.TotalMs = rec.CompletedAt.Sub(rec.StartedAt).Milliseconds()
		}
		cp := rec
		s.records[key] = &cp
		s.order = append(s.order, key)
	}
	s.evictLocked()
}

// Snapshot returns the history oldest-first, with live durations computed up
// to now for migrations still in flight.
func (s *Store) Snapshot() []Record {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	out := make([]Record, 0, len(s.order))
	for _, key := range s.order {
		rec := s.records[key]
		cp := *rec
		cp.Steps = make([]Step, len(rec.Steps))
		copy(cp.Steps, rec.Steps)
		if !cp.terminal() && !cp.StartedAt.IsZero() {
			if n := len(cp.Steps); n > 0 && cp.Steps[n-1].EndedAt == nil {
				cp.Steps[n-1].DurationMs = now.Sub(cp.Steps[n-1].StartedAt).Milliseconds()
			}
			cp.TotalMs = now.Sub(cp.StartedAt).Milliseconds()
			cp.DowntimeMs = downtime(cp.Steps)
		}
		out = append(out, cp)
	}
	return out
}

// Summarize aggregates a Snapshot into dashboard metrics.
func Summarize(records []Record) Summary {
	sum := Summary{PhaseAvgMs: map[string]int64{}}
	phaseTotals := map[string][2]int64{} // phase -> {sum, count}
	var totalMs, downtimeMs, finished int64
	for i := range records {
		rec := &records[i]
		sum.Total++
		switch rec.Phase {
		case phaseCompleted:
			sum.Completed++
		case phaseFailed:
			sum.Failed++
		default:
			sum.Active++
		}
		if rec.terminal() {
			finished++
			totalMs += rec.TotalMs
			downtimeMs += rec.DowntimeMs
		}
		for _, st := range rec.Steps {
			if st.EndedAt == nil {
				continue
			}
			agg := phaseTotals[st.Phase]
			phaseTotals[st.Phase] = [2]int64{agg[0] + st.DurationMs, agg[1] + 1}
		}
	}
	if done := sum.Completed + sum.Failed; done > 0 {
		sum.SuccessRate = float64(sum.Completed) / float64(done)
	}
	if finished > 0 {
		sum.AvgTotalMs = totalMs / finished
		sum.AvgDowntimeMs = downtimeMs / finished
	}
	for phase, agg := range phaseTotals {
		sum.PhaseAvgMs[phase] = agg[0] / agg[1]
	}
	return sum
}

// downtime sums the duration of the steps during which the workload was not
// accessible (open steps count as still running).
func downtime(steps []Step) int64 {
	var ms int64
	now := time.Now()
	for _, st := range steps {
		if st.Accessible {
			continue
		}
		if st.EndedAt != nil {
			ms += st.DurationMs
		} else {
			ms += now.Sub(st.StartedAt).Milliseconds()
		}
	}
	return ms
}

// evictLocked drops the oldest terminal records until the limit holds;
// in-flight migrations are never evicted. Callers hold s.mu.
func (s *Store) evictLocked() {
	for len(s.order) > s.limit {
		evicted := false
		for i, key := range s.order {
			if rec := s.records[key]; rec.terminal() || rec.Seeded {
				delete(s.records, key)
				s.order = append(s.order[:i], s.order[i+1:]...)
				evicted = true
				break
			}
		}
		if !evicted {
			return // everything active: keep over limit rather than lose data
		}
	}
}
