package registry

import (
	"sync"
	"testing"
	"time"
)

func TestRegisterNewAndDuplicate(t *testing.T) {
	r := New()

	prev, isNew := r.Register("web-0", "10.0.0.5:2486", 2486)
	if !isNew {
		t.Fatalf("first registration should be new")
	}
	if prev.Name != "" {
		t.Fatalf("previous snapshot of a new pod should be empty, got %+v", prev)
	}

	prev, isNew = r.Register("web-0", "10.0.0.9:2486", 2486)
	if isNew {
		t.Fatalf("second registration must not be new")
	}
	if prev.Address != "10.0.0.5:2486" {
		t.Fatalf("previous address = %q, want the first address", prev.Address)
	}

	rec, ok := r.Get("web-0")
	if !ok {
		t.Fatalf("record must exist")
	}
	if rec.Address != "10.0.0.9:2486" || rec.LastAddress != "10.0.0.5:2486" {
		t.Fatalf("address rotation wrong: %+v", rec)
	}
	if rec.Registrations != 2 {
		t.Fatalf("registrations = %d, want 2", rec.Registrations)
	}
	if rec.DestRegistered {
		t.Fatalf("DestRegistered must stay false without an armed migration")
	}
}

func TestMigrationFlowFlags(t *testing.T) {
	r := New()
	r.Register("web-0", "10.0.0.5:2486", 2486)

	r.Arm("web-0", ArmInfo{
		CheckpointDir:    "/dmtcp/checkpoints",
		ProcessMigration: true,
		VolumeMigration:  true,
		SyncRounds:       2,
	})

	if needs, known := r.NeedsCheckpoint("web-0"); !needs || !known {
		t.Fatalf("armed pod must need a checkpoint (needs=%v known=%v)", needs, known)
	}
	if needs, known := r.NeedsCheckpoint("ghost"); needs || known {
		t.Fatalf("unknown pod must not need a checkpoint")
	}

	// Pre-downtime sync rounds.
	if !r.RecordSyncRound("web-0", 1) {
		t.Fatalf("sync round on known pod must succeed")
	}
	r.RecordSyncRound("web-0", 2)
	r.RecordSyncRound("web-0", 1) // stale report must not regress
	if rec, _ := r.Get("web-0"); rec.SyncRound != 2 {
		t.Fatalf("SyncRound = %d, want 2", rec.SyncRound)
	}

	// Source EA wrote checkpoint files.
	if !r.MarkCheckpointReady("web-0", "/dmtcp/checkpoints") {
		t.Fatalf("checkpoint-ready on known pod must succeed")
	}

	// Destination EA re-registers under the same (StatefulSet) name.
	r.Register("web-0", "10.0.1.7:2486", 2486)
	rec, _ := r.Get("web-0")
	if !rec.DestRegistered {
		t.Fatalf("re-registration while migrating must set DestRegistered")
	}

	if !r.MarkRestored("web-0") {
		t.Fatalf("restored on known pod must succeed")
	}

	r.Disarm("web-0")
	rec, _ = r.Get("web-0")
	if rec.Migrating || rec.CheckpointReady || rec.DestRegistered || rec.Restored || rec.SyncRound != 0 {
		t.Fatalf("Disarm must clear all flow flags: %+v", rec)
	}
}

func TestArmBeforeRegistration(t *testing.T) {
	r := New()
	r.Arm("web-1", ArmInfo{CheckpointDir: "/ckpt", ProcessMigration: true})
	rec, ok := r.Get("web-1")
	if !ok || !rec.Migrating || rec.CheckpointDir != "/ckpt" {
		t.Fatalf("Arm must create a record for a not-yet-registered pod: %+v", rec)
	}
}

func TestSeedDoesNotOverwrite(t *testing.T) {
	r := New()
	r.Register("web-0", "live", 1)
	r.Seed([]PodRecord{
		{Name: "web-0", Address: "stale", RegisteredAt: time.Now()},
		{Name: "web-1", Address: "seeded", RegisteredAt: time.Now()},
	})
	if rec, _ := r.Get("web-0"); rec.Address != "live" {
		t.Fatalf("seed must not overwrite a live record")
	}
	if rec, ok := r.Get("web-1"); !ok || rec.Address != "seeded" {
		t.Fatalf("seed must add missing records")
	}
}

func TestListSortedAndConcurrentAccess(t *testing.T) {
	r := New()
	var wg sync.WaitGroup
	names := []string{"c-2", "a-0", "b-1"}
	for _, n := range names {
		wg.Add(1)
		go func(name string) {
			defer wg.Done()
			r.Register(name, name+":2486", 2486)
			r.Arm(name, ArmInfo{})
			r.Disarm(name)
		}(n)
	}
	wg.Wait()

	list := r.List()
	if len(list) != 3 {
		t.Fatalf("len(list) = %d, want 3", len(list))
	}
	if list[0].Name != "a-0" || list[1].Name != "b-1" || list[2].Name != "c-2" {
		t.Fatalf("list not sorted by name: %v", []string{list[0].Name, list[1].Name, list[2].Name})
	}
}
