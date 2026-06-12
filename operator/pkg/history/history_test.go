package history

import (
	"testing"
	"time"
)

// transitionAt builds a Transition for the test migration at base+offset.
func transitionAt(phase, message string, at time.Time) Transition {
	return Transition{
		Namespace:  "mig-ready",
		Name:       "web-abc12",
		Workload:   "web",
		SourcePod:  "web-0",
		SourceNode: "node-1",
		TargetNode: "node-2",
		Phase:      phase,
		Message:    message,
		Time:       at,
	}
}

// TestRecordLifecycle drives a full migration through the store and checks
// step durations, downtime and accessibility.
func TestRecordLifecycle(t *testing.T) {
	s := NewStore(true, 10)
	base := time.Now().Add(-time.Minute)

	s.RecordTransition(transitionAt("Pending", "preparing", base))
	s.RecordTransition(transitionAt("Syncing", "round 1", base.Add(2*time.Second)))
	s.RecordTransition(transitionAt("Checkpointing", "checkpointing", base.Add(10*time.Second)))
	s.RecordTransition(transitionAt("Transferring", "transferring", base.Add(13*time.Second)))
	s.RecordTransition(transitionAt("Restoring", "restoring", base.Add(15*time.Second)))
	s.RecordTransition(transitionAt("Completed", "restored", base.Add(20*time.Second)))

	recs := s.Snapshot()
	if len(recs) != 1 {
		t.Fatalf("snapshot length = %d, want 1", len(recs))
	}
	rec := recs[0]

	if rec.Phase != "Completed" || rec.CompletedAt == nil {
		t.Fatalf("record not completed: phase=%q completedAt=%v", rec.Phase, rec.CompletedAt)
	}
	if !rec.Accessible {
		t.Fatalf("completed migration must be accessible")
	}
	if rec.TotalMs != 20000 {
		t.Fatalf("totalMs = %d, want 20000", rec.TotalMs)
	}
	// Downtime = Checkpointing (3s) + Transferring (2s) + Restoring (5s).
	if rec.DowntimeMs != 10000 {
		t.Fatalf("downtimeMs = %d, want 10000", rec.DowntimeMs)
	}

	wantSteps := []struct {
		phase      string
		durationMs int64
		accessible bool
	}{
		{"Pending", 2000, true},
		{"Syncing", 8000, true},
		{"Checkpointing", 3000, false},
		{"Transferring", 2000, false},
		{"Restoring", 5000, false},
	}
	if len(rec.Steps) != len(wantSteps) {
		t.Fatalf("steps = %d, want %d (%+v)", len(rec.Steps), len(wantSteps), rec.Steps)
	}
	for i, want := range wantSteps {
		got := rec.Steps[i]
		if got.Phase != want.phase || got.DurationMs != want.durationMs || got.Accessible != want.accessible {
			t.Errorf("step %d = {%s %d %v}, want {%s %d %v}",
				i, got.Phase, got.DurationMs, got.Accessible, want.phase, want.durationMs, want.accessible)
		}
		if got.EndedAt == nil {
			t.Errorf("step %d must be closed", i)
		}
	}

	sum := Summarize(recs)
	if sum.Total != 1 || sum.Completed != 1 || sum.SuccessRate != 1 {
		t.Fatalf("summary = %+v", sum)
	}
	if sum.AvgTotalMs != 20000 || sum.AvgDowntimeMs != 10000 {
		t.Fatalf("summary averages = %+v", sum)
	}
	if sum.PhaseAvgMs["Syncing"] != 8000 {
		t.Fatalf("phaseAvgMs[Syncing] = %d, want 8000", sum.PhaseAvgMs["Syncing"])
	}
}

// TestActiveRecordLiveDurations checks that in-flight migrations report
// durations up to now and the not-accessible status during downtime phases.
func TestActiveRecordLiveDurations(t *testing.T) {
	s := NewStore(true, 10)
	base := time.Now().Add(-10 * time.Second)
	s.RecordTransition(transitionAt("Pending", "preparing", base))
	s.RecordTransition(transitionAt("Checkpointing", "checkpointing", base.Add(2*time.Second)))

	rec := s.Snapshot()[0]
	if rec.Accessible {
		t.Fatalf("checkpointing migration must not be accessible")
	}
	if rec.TotalMs < 9000 || rec.DowntimeMs < 7000 {
		t.Fatalf("live durations too small: total=%d downtime=%d", rec.TotalMs, rec.DowntimeMs)
	}
	if open := rec.Steps[len(rec.Steps)-1]; open.EndedAt != nil || open.DurationMs < 7000 {
		t.Fatalf("open step must report live duration: %+v", open)
	}
}

// TestDisabledDropsTransitions checks the runtime toggle: disabled stores
// drop transitions but keep already-recorded history.
func TestDisabledDropsTransitions(t *testing.T) {
	s := NewStore(false, 10)
	s.RecordTransition(transitionAt("Pending", "preparing", time.Now()))
	if len(s.Snapshot()) != 0 {
		t.Fatalf("disabled store must drop transitions")
	}

	s.SetEnabled(true)
	s.RecordTransition(transitionAt("Pending", "preparing", time.Now()))
	if len(s.Snapshot()) != 1 {
		t.Fatalf("enabled store must record")
	}

	s.SetEnabled(false)
	s.RecordTransition(Transition{Namespace: "mig-ready", Name: "other", Phase: "Pending", Time: time.Now()})
	if got := len(s.Snapshot()); got != 1 {
		t.Fatalf("re-disabled store must keep history and drop new transitions, got %d records", got)
	}
}

// TestEvictionKeepsActive checks the bounded buffer evicts oldest terminal
// records first and never drops in-flight migrations.
func TestEvictionKeepsActive(t *testing.T) {
	s := NewStore(true, 2)
	base := time.Now().Add(-time.Minute)

	for _, name := range []string{"done-1", "done-2"} {
		tr := transitionAt("Pending", "", base)
		tr.Name = name
		s.RecordTransition(tr)
		tr.Phase = "Completed"
		tr.Time = base.Add(time.Second)
		s.RecordTransition(tr)
	}
	active := transitionAt("Pending", "", base)
	active.Name = "active-1"
	s.RecordTransition(active)

	recs := s.Snapshot()
	if len(recs) != 2 {
		t.Fatalf("records = %d, want 2", len(recs))
	}
	if recs[0].Name != "done-2" || recs[1].Name != "active-1" {
		t.Fatalf("eviction must drop the oldest terminal record: %s, %s", recs[0].Name, recs[1].Name)
	}
}

// TestSeed checks restart seeding: coarse records, no duplicate overwrite.
func TestSeed(t *testing.T) {
	s := NewStore(true, 10)
	s.RecordTransition(transitionAt("Pending", "live", time.Now()))

	completed := time.Now()
	s.Seed([]Record{
		{Namespace: "mig-ready", Name: "web-abc12", Phase: "Completed"}, // duplicate of live record
		{Namespace: "mig-ready", Name: "old-1", Phase: "Completed", StartedAt: completed.Add(-30 * time.Second), CompletedAt: &completed},
	})

	recs := s.Snapshot()
	if len(recs) != 2 {
		t.Fatalf("records = %d, want 2", len(recs))
	}
	if recs[0].Phase != "Pending" {
		t.Fatalf("seed must not overwrite the live record, got phase %q", recs[0].Phase)
	}
	old := recs[1]
	if !old.Seeded || old.TotalMs != 30000 || !old.Accessible {
		t.Fatalf("seeded record wrong: %+v", old)
	}
}
