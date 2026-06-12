#!/usr/bin/env bash
# make-migratable.sh — Patch an existing StatefulSet so it participates in
# MyceDrive live migration (DMTCP process checkpointing + overlayfs volume
# checkpointing) and create the matching MigratableWorkload CR for the operator.
#
# Usage:
#   ./scripts/make-migratable.sh -n NAMESPACE -s STATEFULSET [OPTIONS]
#
# Required:
#   -n NAMESPACE            Kubernetes namespace of the target StatefulSet
#   -s STATEFULSET          Name of the StatefulSet to patch
#
# Optional:
#   -c CONTAINER            Application container to wrap (default: first container)
#   -u START_CMD            Startup command wrapped by DMTCP, e.g.
#                           "/usr/sbin/mosquitto -c /etc/mosquitto/mosquitto.conf"
#   -m MC_HOST              Operator service hostname
#                           (default: mycedrive.mig-ready.svc.cluster.local)
#   -i DMTCP_IMAGE          DMTCP sidecar image (default: mycedrive/dmtcp:dev)
#   -d CKPT_DIR             Checkpoint directory in the pod (default: /dmtcp/checkpoints)
#   --process-migration     true|false — enable DMTCP process checkpointing (default: true)
#   --volume-migration      true|false — enable overlayfs volume checkpointing (default: true)
#   --pre-sync-rounds N     Pre-migration dirty-page sync rounds, N >= 0 (default: 1)
#   --no-cr                 Skip creating the MigratableWorkload CR
#   --dry-run               Print all generated YAML; do not apply anything
#   -h                      Show this help message
#
# What the script does:
#   1. Adds an emptyDir volume "dmtcp-shared".
#   2. Adds a dmtcp-init initContainer that copies DMTCP binaries into the volume.
#   3. Adds a "dmtcp" sidecar container running dmtcp_coordinator on port 7779.
#   4. Patches the application container with:
#        - volumeMount for /dmtcp
#        - env vars: MIGR_COOR, POD_NAME, POD_IP, START_UP,
#          DMTCP_COORD_HOST, DMTCP_CHECKPOINT_DIR
#        - ENABLE_PROCESS_MIGRATION / ENABLE_VOLUME_MIGRATION (when non-default)
#        - preStop lifecycle hook calling /dmtcp/bin/end_container
#   5. Labels the pod template mig-ready=true.
#   6. Creates a ClusterRoleBinding for the pod's ServiceAccount.
#   7. Creates a MigratableWorkload CR (mycedrive.io/v1alpha1) unless --no-cr.
#
# Idempotent: safe to run more than once against the same StatefulSet.

set -euo pipefail

###############################################################################
# Defaults
###############################################################################
NAMESPACE=""
STATEFULSET=""
CONTAINER=""
MC_HOST="mycedrive.mig-ready.svc.cluster.local"
DMTCP_IMAGE="mycedrive/dmtcp:dev"
CKPT_DIR="/dmtcp/checkpoints"
START_CMD=""
PROCESS_MIG="true"
VOLUME_MIG="true"
PRE_SYNC_ROUNDS="1"
NO_CR=false
DRY_RUN=false

###############################################################################
# Helpers
###############################################################################
log() { printf '[make-migratable] %s\n' "$*" >&2; }
die() { log "ERROR: $*"; exit 1; }

require_cmd() {
    command -v "$1" >/dev/null 2>&1 || die "Required command not found: $1"
}

# Print YAML in dry-run, or validate-then-apply live.
kubectl_apply() {
    local label="$1" yaml="$2"
    if [[ "${DRY_RUN}" == "true" ]]; then
        printf '\n### %s (dry-run) ###\n%s\n' "${label}" "${yaml}"
        return
    fi
    printf '%s\n' "${yaml}" | kubectl apply --dry-run=client -f - >/dev/null
    printf '%s\n' "${yaml}" | kubectl apply -f -
    log "Applied: ${label}"
}

validate_bool() {
    [[ "$1" == "true" || "$1" == "false" ]] \
        || die "$2 must be 'true' or 'false', got '$1'"
}

validate_nonneg_int() {
    [[ "$1" =~ ^[0-9]+$ ]] \
        || die "$2 must be a non-negative integer, got '$1'"
}

###############################################################################
# Argument parsing
###############################################################################
while [[ $# -gt 0 ]]; do
    case "$1" in
        -n)                   NAMESPACE="$2";       shift 2 ;;
        -s)                   STATEFULSET="$2";     shift 2 ;;
        -c)                   CONTAINER="$2";       shift 2 ;;
        -u)                   START_CMD="$2";       shift 2 ;;
        -m)                   MC_HOST="$2";         shift 2 ;;
        -i)                   DMTCP_IMAGE="$2";     shift 2 ;;
        -d)                   CKPT_DIR="$2";        shift 2 ;;
        --process-migration)  PROCESS_MIG="$2";     shift 2 ;;
        --volume-migration)   VOLUME_MIG="$2";      shift 2 ;;
        --pre-sync-rounds)    PRE_SYNC_ROUNDS="$2"; shift 2 ;;
        --no-cr)              NO_CR=true;           shift ;;
        --dry-run)            DRY_RUN=true;         shift ;;
        -h|--help)
            sed -n '/^# Usage:/,/^[^#]/{ s/^# \{0,2\}//; p }' "$0" | head -40
            exit 0 ;;
        *) die "Unknown argument: $1" ;;
    esac
done

[[ -n "${NAMESPACE}" ]]   || die "-n NAMESPACE is required"
[[ -n "${STATEFULSET}" ]] || die "-s STATEFULSET is required"

validate_bool        "${PROCESS_MIG}"    "--process-migration"
validate_bool        "${VOLUME_MIG}"     "--volume-migration"
validate_nonneg_int  "${PRE_SYNC_ROUNDS}" "--pre-sync-rounds"

require_cmd kubectl

###############################################################################
# Resolve target container name and ServiceAccount.
# In dry-run mode with -c supplied we skip the live cluster read.
###############################################################################
SA="default"
SS_JSON=""

if [[ "${DRY_RUN}" == "true" && -n "${CONTAINER}" ]]; then
    log "Dry-run with -c supplied; skipping live StatefulSet lookup"
    log "Target container: ${CONTAINER}"
else
    log "Inspecting StatefulSet ${NAMESPACE}/${STATEFULSET}..."
    SS_JSON=$(kubectl get statefulset "${STATEFULSET}" -n "${NAMESPACE}" -o json 2>/dev/null) \
        || die "StatefulSet '${STATEFULSET}' not found in namespace '${NAMESPACE}'"

    if [[ -z "${CONTAINER}" ]]; then
        CONTAINER=$(printf '%s' "${SS_JSON}" \
            | grep -o '"name": "[^"]*"' | head -5 \
            | awk -F'"' 'NR==2{print $4}')
        [[ -n "${CONTAINER}" ]] \
            || die "Could not determine first container name; pass -c explicitly"
        log "Target container: ${CONTAINER} (auto-detected)"
    else
        log "Target container: ${CONTAINER}"
    fi
fi

if [[ -n "${SS_JSON}" ]]; then
    _sa=$(printf '%s' "${SS_JSON}" \
        | grep -o '"serviceAccountName": "[^"]*"' \
        | awk -F'"' '{print $4}' || true)
    [[ -n "${_sa}" ]] && SA="${_sa}"
fi
log "ServiceAccount: ${SA}"

###############################################################################
# 1. ClusterRoleBinding — binds the pod SA to the operator's ClusterRole so
#    the operator can discover and manage this pod.
###############################################################################
CRB_NAME="mycedrive-app-binding-${NAMESPACE}-${SA}"
CRB_YAML=$(cat <<EOF
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRoleBinding
metadata:
  name: ${CRB_NAME}
  labels:
    app.kubernetes.io/managed-by: make-migratable
subjects:
  - kind: ServiceAccount
    name: ${SA}
    namespace: ${NAMESPACE}
roleRef:
  kind: ClusterRole
  name: mycedrive-coordinator-role
  apiGroup: rbac.authorization.k8s.io
EOF
)

if [[ "${DRY_RUN}" == "false" ]] \
        && kubectl get clusterrolebinding "${CRB_NAME}" >/dev/null 2>&1; then
    log "ClusterRoleBinding ${CRB_NAME} already exists; skipping"
else
    kubectl_apply "ClusterRoleBinding/${CRB_NAME}" "${CRB_YAML}"
fi

###############################################################################
# 2. Build optional env-toggle lines for ENABLE_PROCESS_MIGRATION /
#    ENABLE_VOLUME_MIGRATION.  Only injected when the caller explicitly sets
#    them to false (agent defaults are true, so no need to repeat the default).
###############################################################################
TOGGLE_ENV=""
if [[ "${PROCESS_MIG}" == "false" ]]; then
    TOGGLE_ENV="${TOGGLE_ENV}
            - name: ENABLE_PROCESS_MIGRATION
              value: \"false\""
fi
if [[ "${VOLUME_MIG}" == "false" ]]; then
    TOGGLE_ENV="${TOGGLE_ENV}
            - name: ENABLE_VOLUME_MIGRATION
              value: \"false\""
fi

###############################################################################
# 3. Strategic-merge patch for the StatefulSet pod template.
#    mergeKey for volumes/initContainers/containers is "name", so the patch
#    adds new entries and leaves existing ones untouched.
###############################################################################
PRESTOP_CMD='["/dmtcp/bin/end_container","'"${MC_HOST}"'","$(POD_NAME)","'"${CKPT_DIR}"'"]'

SS_PATCH=$(cat <<EOF
spec:
  template:
    metadata:
      labels:
        mig-ready: "true"
    spec:
      volumes:
        - name: dmtcp-shared
          emptyDir: {}
      initContainers:
        - name: dmtcp-init
          image: ${DMTCP_IMAGE}
          command:
            - sh
            - -c
            - "cp -r /share/. /dmtcp/ && echo 'DMTCP binaries ready'"
          volumeMounts:
            - name: dmtcp-shared
              mountPath: /dmtcp
      containers:
        - name: ${CONTAINER}
          env:
            - name: MIGR_COOR
              value: "${MC_HOST}"
            - name: POD_NAME
              valueFrom:
                fieldRef:
                  fieldPath: metadata.name
            - name: POD_IP
              valueFrom:
                fieldRef:
                  fieldPath: status.podIP
            - name: DMTCP_COORD_HOST
              value: "127.0.0.1"
            - name: DMTCP_CHECKPOINT_DIR
              value: "${CKPT_DIR}"
            - name: START_UP
              value: "${START_CMD}"${TOGGLE_ENV}
          volumeMounts:
            - name: dmtcp-shared
              mountPath: /dmtcp
          lifecycle:
            preStop:
              exec:
                command: ${PRESTOP_CMD}
        - name: dmtcp
          image: ${DMTCP_IMAGE}
          ports:
            - containerPort: 7779
              name: dmtcp-coord
          env:
            - name: DMTCP_CHECKPOINT_DIR
              value: "${CKPT_DIR}"
          volumeMounts:
            - name: dmtcp-shared
              mountPath: /share
EOF
)

if [[ "${DRY_RUN}" == "true" ]]; then
    printf '\n### StatefulSet strategic-merge patch (dry-run) ###\n%s\n' "${SS_PATCH}"
else
    FULL_YAML=$(cat <<EOF
apiVersion: apps/v1
kind: StatefulSet
metadata:
  name: ${STATEFULSET}
  namespace: ${NAMESPACE}
${SS_PATCH}
EOF
)
    printf '%s\n' "${FULL_YAML}" | kubectl apply --dry-run=client -f - >/dev/null \
        || die "kubectl dry-run validation of StatefulSet patch failed"
    kubectl patch statefulset "${STATEFULSET}" -n "${NAMESPACE}" \
        --type="strategic" -p "${SS_PATCH}"
    log "StatefulSet ${NAMESPACE}/${STATEFULSET} patched"
fi

###############################################################################
# 4. MigratableWorkload CR  (mycedrive.io/v1alpha1)
#    Omit fields that are at their defaults to keep the CR minimal.
#    The operator's legacy /migrate endpoint auto-creates a missing CR, so
#    this step is recommended but not strictly required (use --no-cr to skip).
###############################################################################
if [[ "${NO_CR}" == "true" ]]; then
    log "Skipping MigratableWorkload CR (--no-cr)"
else
    # Build optional spec fields — only emit non-default values.
    EXTRA_SPEC=""
    if [[ "${PROCESS_MIG}" == "false" ]]; then
        EXTRA_SPEC="${EXTRA_SPEC}
  processMigration: false"
    fi
    if [[ "${VOLUME_MIG}" == "false" ]]; then
        EXTRA_SPEC="${EXTRA_SPEC}
  volumeMigration: false"
    fi
    if [[ "${PRE_SYNC_ROUNDS}" != "1" ]]; then
        EXTRA_SPEC="${EXTRA_SPEC}
  preSyncRounds: ${PRE_SYNC_ROUNDS}"
    fi
    if [[ "${CKPT_DIR}" != "/dmtcp/checkpoints" ]]; then
        EXTRA_SPEC="${EXTRA_SPEC}
  checkpointDir: ${CKPT_DIR}"
    fi

    MW_YAML=$(cat <<EOF
apiVersion: mycedrive.io/v1alpha1
kind: MigratableWorkload
metadata:
  name: ${STATEFULSET}
  namespace: ${NAMESPACE}
  labels:
    app.kubernetes.io/managed-by: make-migratable
spec:
  workloadRef:
    kind: StatefulSet
    name: ${STATEFULSET}
  placementLabel:
    key: mig-ready
    value: "true"${EXTRA_SPEC}
EOF
)
    kubectl_apply "MigratableWorkload/${STATEFULSET}" "${MW_YAML}"
fi

###############################################################################
# Done
###############################################################################
log ""
log "Next steps:"
log "  1. Watch rollout:   kubectl rollout status sts/${STATEFULSET} -n ${NAMESPACE}"
log "  2. Check binaries:  kubectl exec -it ${STATEFULSET}-0 -n ${NAMESPACE} -c ${CONTAINER} -- ls /dmtcp/bin/"
log "  3. Check EA logs:   kubectl logs ${STATEFULSET}-0 -n ${NAMESPACE} -c ${CONTAINER} | grep -i register"
if [[ "${NO_CR}" == "false" ]]; then
    log "  4. Check CR:        kubectl get mw ${STATEFULSET} -n ${NAMESPACE}"
fi
log ""
log "See docs/making-statefulsets-migratable.md for image requirements and limitations."
