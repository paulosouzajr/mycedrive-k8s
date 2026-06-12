package functional

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-logr/logr"
	appsv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	mycedrivev1alpha1 "github.com/paulosouzajr/mycedrive-k8s/operator/api/v1alpha1"
	"github.com/paulosouzajr/mycedrive-k8s/operator/pkg/registry"
	"github.com/paulosouzajr/mycedrive-k8s/operator/pkg/restapi"
)

// newMigrateAPI starts the REST API backed by a fake Kubernetes client
// pre-loaded with objs, so /migrate's CR-creating path runs for real.
func newMigrateAPI(t *testing.T, objs ...client.Object) (client.Client, string) {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := appsv1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := mycedrivev1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	kc := fake.NewClientBuilder().WithScheme(scheme).WithObjects(objs...).Build()
	srv := &restapi.Server{
		Client:           kc,
		Registry:         registry.New(),
		DefaultNamespace: "mig-ready",
		Log:              logr.Discard(),
	}
	api := httptest.NewServer(srv.Handler())
	t.Cleanup(api.Close)
	return kc, api.URL
}

func postMigrate(t *testing.T, url string, payload any) (int, map[string]any) {
	t.Helper()
	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.Post(url+"/migrate", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("POST /migrate: %v", err)
	}
	defer resp.Body.Close()
	var decoded map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&decoded); err != nil {
		t.Fatalf("decode /migrate response: %v", err)
	}
	return resp.StatusCode, decoded
}

// TestMigrateLegacy_AutoCreatesCRs verifies the legacy request shape
// auto-creates a MigratableWorkload (kind detected from the cluster) and a
// Migration carrying the requested placement.
func TestMigrateLegacy_AutoCreatesCRs(t *testing.T) {
	sts := &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{Name: "web", Namespace: "mig-ready"},
	}
	kc, apiURL := newMigrateAPI(t, sts)

	code, resp := postMigrate(t, apiURL, map[string]any{
		"deployment": "web", "originNode": "node-a", "destNode": "node-b",
	})
	if code != http.StatusOK || resp["status"] != "migration_started" {
		t.Fatalf("legacy migrate = %d %v", code, resp)
	}

	var mw mycedrivev1alpha1.MigratableWorkload
	if err := kc.Get(context.Background(), types.NamespacedName{Namespace: "mig-ready", Name: "web"}, &mw); err != nil {
		t.Fatalf("MigratableWorkload not auto-created: %v", err)
	}
	if mw.Spec.WorkloadRef.Kind != mycedrivev1alpha1.WorkloadKindStatefulSet {
		t.Fatalf("auto-detected kind = %q, want StatefulSet", mw.Spec.WorkloadRef.Kind)
	}

	var migs mycedrivev1alpha1.MigrationList
	if err := kc.List(context.Background(), &migs, client.InNamespace("mig-ready")); err != nil {
		t.Fatal(err)
	}
	if len(migs.Items) != 1 {
		t.Fatalf("expected 1 Migration, got %d", len(migs.Items))
	}
	spec := migs.Items[0].Spec
	if spec.WorkloadName != "web" || spec.SourceNode != "node-a" || spec.TargetNode != "node-b" {
		t.Fatalf("Migration spec: %+v", spec)
	}
}

// TestMigrateNewShape_UsesExistingWorkload verifies the CRD-era request shape
// reuses a declared MigratableWorkload and records the pod name.
func TestMigrateNewShape_UsesExistingWorkload(t *testing.T) {
	mw := &mycedrivev1alpha1.MigratableWorkload{
		ObjectMeta: metav1.ObjectMeta{Name: "db", Namespace: "prod"},
		Spec: mycedrivev1alpha1.MigratableWorkloadSpec{
			WorkloadRef: mycedrivev1alpha1.WorkloadReference{
				Kind: mycedrivev1alpha1.WorkloadKindStatefulSet, Name: "db",
			},
		},
	}
	kc, apiURL := newMigrateAPI(t, mw)

	code, resp := postMigrate(t, apiURL, map[string]any{
		"workload": "db", "namespace": "prod", "podName": "db-0",
		"sourceNode": "node-a", "targetNode": "node-b",
	})
	if code != http.StatusOK {
		t.Fatalf("migrate = %d %v", code, resp)
	}

	var migs mycedrivev1alpha1.MigrationList
	if err := kc.List(context.Background(), &migs, client.InNamespace("prod")); err != nil {
		t.Fatal(err)
	}
	if len(migs.Items) != 1 || migs.Items[0].Spec.PodName != "db-0" {
		t.Fatalf("Migration list: %+v", migs.Items)
	}
}

// TestMigrateUnknownWorkload_404 verifies /migrate refuses workloads that are
// neither declared nor discoverable in the cluster.
func TestMigrateUnknownWorkload_404(t *testing.T) {
	_, apiURL := newMigrateAPI(t)
	code, _ := postMigrate(t, apiURL, map[string]any{
		"deployment": "ghost", "originNode": "a", "destNode": "b",
	})
	if code != http.StatusNotFound {
		t.Fatalf("migrate unknown workload = %d, want 404", code)
	}
}

// TestMigrateValidation_400 verifies incomplete requests are rejected before
// touching the cluster.
func TestMigrateValidation_400(t *testing.T) {
	_, apiURL := newMigrateAPI(t)
	code, _ := postMigrate(t, apiURL, map[string]any{"deployment": "web"})
	if code != http.StatusBadRequest {
		t.Fatalf("migrate without nodes = %d, want 400", code)
	}
}
