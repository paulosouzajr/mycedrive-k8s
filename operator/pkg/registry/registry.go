// Package registry holds the operator's in-memory, mutex-protected view of
// every Execution Agent registration. It is the shared state between the REST
// API (written to by agents) and the Migration controller (read to advance
// migration phases). The MigratableWorkload controller mirrors records into
// CRD status so the registry can be re-seeded after an operator restart.
package registry

import (
	"net"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// defaultTransferPort is the Execution Agent's default transfer listener
// port (kept in sync with v1alpha1.DefaultTransferPort).
const defaultTransferPort = 2486

// joinHostPort normalises an agent address to "host:port". Addresses that
// already carry a port are returned unchanged; bare hosts get port, or the
// default transfer port when port is 0.
func joinHostPort(address string, port int) string {
	if address == "" {
		return ""
	}
	if _, _, err := net.SplitHostPort(address); err == nil {
		return address
	}
	if port <= 0 {
		port = defaultTransferPort
	}
	if strings.Contains(address, ":") {
		// Bare IPv6 literal.
		return net.JoinHostPort(address, strconv.Itoa(port))
	}
	return address + ":" + strconv.Itoa(port)
}

// PodRecord is one registered Execution Agent.
type PodRecord struct {
	Name          string
	Address       string
	LastAddress   string
	ContainerPort int
	Node          string

	// WorkloadNamespace/WorkloadName link the pod to its MigratableWorkload
	// once the controller has matched it.
	WorkloadNamespace string
	WorkloadName      string

	// Migration flow flags, driven by the Migration controller and the REST
	// handlers:
	//   Migrating       — an active Migration targets this pod; /remove
	//                     answers needsCheckpoint=true.
	//   CheckpointReady — the source EA called /copy.
	//   DestRegistered  — a registration arrived while Migrating (the
	//                     destination EA came up; StatefulSet same-name flow).
	//   Restored        — the destination EA called /restored.
	Migrating       bool
	CheckpointDir   string
	CheckpointReady bool
	DestRegistered  bool
	Restored        bool

	// Mechanism toggles resolved from the MigratableWorkload, propagated
	// to the Execution Agent via /register, /remove and /poll responses.
	ProcessMigration bool
	VolumeMigration  bool

	// Pre-downtime overlay sync progress (volume migration): SyncRounds is
	// requested by the controller, SyncRound is the last round the source
	// EA reported via POST /sync.
	SyncRounds int
	SyncRound  int

	// DestAddress is the migration-target EA's transfer endpoint
	// ("host:port"), captured from the duplicate /register that arrives
	// while a migration is armed. Returned to the source EA in the /remove
	// response so it can stream checkpoints directly to the destination.
	DestAddress string

	Registrations int
	RegisteredAt  time.Time
	LastSeen      time.Time
}

// Registry is a thread-safe pod registration store keyed by pod name.
type Registry struct {
	mu      sync.RWMutex
	records map[string]*PodRecord
}

// New returns an empty Registry.
func New() *Registry {
	return &Registry{records: make(map[string]*PodRecord)}
}

// Register records a pod registration and returns a snapshot of the record as
// it was *before* this call, plus whether the pod was previously unknown.
// Re-registration while a migration is active marks the destination as up.
func (r *Registry) Register(name, address string, port int) (prev PodRecord, isNew bool) {
	r.mu.Lock()
	defer r.mu.Unlock()

	now := time.Now()
	rec, ok := r.records[name]
	if !ok {
		r.records[name] = &PodRecord{
			Name:          name,
			Address:       address,
			ContainerPort: port,
			Registrations: 1,
			RegisteredAt:  now,
			LastSeen:      now,
		}
		return PodRecord{}, true
	}

	prev = *rec
	rec.LastAddress = rec.Address
	rec.Address = address
	if port != 0 {
		rec.ContainerPort = port
	}
	rec.Registrations++
	rec.LastSeen = now
	if rec.Migrating {
		// Duplicate registration while armed: this caller is the migration
		// target. Record its transfer endpoint so /remove can hand it to
		// the source EA for the direct checkpoint stream.
		rec.DestRegistered = true
		rec.DestAddress = joinHostPort(address, port)
	}
	return prev, false
}

// Get returns a snapshot of the record for name.
func (r *Registry) Get(name string) (PodRecord, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	rec, ok := r.records[name]
	if !ok {
		return PodRecord{}, false
	}
	return *rec, true
}

// List returns snapshots of all records sorted by pod name.
func (r *Registry) List() []PodRecord {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]PodRecord, 0, len(r.records))
	for _, rec := range r.records {
		out = append(out, *rec)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Delete removes the record for name.
func (r *Registry) Delete(name string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.records, name)
}

// ArmInfo describes an active migration targeting a pod.
type ArmInfo struct {
	CheckpointDir    string
	ProcessMigration bool
	VolumeMigration  bool
	SyncRounds       int
}

// Arm marks a pod as the target of an active Migration. The record is
// created if the pod has not registered yet, so a Migration can be armed
// before its source EA first checks in.
func (r *Registry) Arm(name string, info ArmInfo) {
	r.mu.Lock()
	defer r.mu.Unlock()
	rec, ok := r.records[name]
	if !ok {
		rec = &PodRecord{Name: name, RegisteredAt: time.Now()}
		r.records[name] = rec
	}
	rec.Migrating = true
	if info.CheckpointDir != "" {
		rec.CheckpointDir = info.CheckpointDir
	}
	rec.ProcessMigration = info.ProcessMigration
	rec.VolumeMigration = info.VolumeMigration
	rec.SyncRounds = info.SyncRounds
}

// Disarm clears the active-migration flag and all flow flags on a pod.
func (r *Registry) Disarm(name string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	rec, ok := r.records[name]
	if !ok {
		return
	}
	rec.Migrating = false
	rec.CheckpointReady = false
	rec.DestRegistered = false
	rec.Restored = false
	rec.SyncRounds = 0
	rec.SyncRound = 0
	rec.DestAddress = ""
}

// RecordSyncRound stores the latest completed pre-downtime overlay sync
// round reported by the source EA (POST /sync). Returns false when the pod
// is unknown.
func (r *Registry) RecordSyncRound(name string, round int) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	rec, ok := r.records[name]
	if !ok {
		return false
	}
	if round > rec.SyncRound {
		rec.SyncRound = round
	}
	return true
}

// NeedsCheckpoint reports whether the named pod must checkpoint before
// stopping (i.e. an active Migration targets it).
func (r *Registry) NeedsCheckpoint(name string) (needs, known bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	rec, ok := r.records[name]
	if !ok {
		return false, false
	}
	return rec.Migrating, true
}

// MarkCheckpointReady records that the source EA finished writing checkpoint
// files (POST /copy). Returns false when the pod is unknown.
func (r *Registry) MarkCheckpointReady(name, dir string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	rec, ok := r.records[name]
	if !ok {
		return false
	}
	rec.CheckpointReady = true
	if dir != "" {
		rec.CheckpointDir = dir
	}
	return true
}

// MarkRestored records that the destination EA completed a DMTCP restore
// (POST /restored). Returns false when the pod is unknown.
func (r *Registry) MarkRestored(name string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	rec, ok := r.records[name]
	if !ok {
		return false
	}
	rec.Restored = true
	return true
}

// SetWorkload links a registered pod to its MigratableWorkload.
func (r *Registry) SetWorkload(podName, namespace, workload string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if rec, ok := r.records[podName]; ok {
		rec.WorkloadNamespace = namespace
		rec.WorkloadName = workload
	}
}

// SetNode records the node a registered pod runs on.
func (r *Registry) SetNode(podName, node string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if rec, ok := r.records[podName]; ok {
		rec.Node = node
	}
}

// Seed inserts records that are not already present. Used at startup to
// restore state mirrored into MigratableWorkload statuses.
func (r *Registry) Seed(records []PodRecord) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for i := range records {
		rec := records[i]
		if _, exists := r.records[rec.Name]; exists {
			continue
		}
		cp := rec
		r.records[rec.Name] = &cp
	}
}
