#!/bin/bash
set -e

# Set permissions
user="$(id -u)"
if [ "$user" = '0' ]; then
	[ -d "/mosquitto" ] && chown -R mosquitto:mosquitto /mosquitto || true
fi

# Wait for the DMTCP init container to populate the shared volume.
while [ ! -x /dmtcp/bin/dmtcp_launch ]; do
	echo "waiting for DMTCP binaries in /dmtcp/bin ..."
	sleep 1
done

CKPT_DIR="${DMTCP_CHECKPOINT_DIR:-/dmtcp/checkpoints}"

# --- MyceDrive Execution Agent handshake -----------------------------------
# go-agent registers this container with the Migration Coordinator. On a
# migration target it blocks while receiving the source pod's checkpoints,
# then execs dmtcp_restart in place: in that case the line below only
# returns once the RESTORED application exits, and go-agent leaves a
# ".restored" marker so we must NOT launch a fresh instance afterwards.
if [ -x /dmtcp/bin/go-agent ]; then
	/dmtcp/bin/go-agent "${VOLUME_ROOT_DIR:-}" "${CHECKPOINT_ROUNDS:-1}" || true
	if [ -f "$CKPT_DIR/.restored" ]; then
		echo "restored application finished; exiting without fresh launch"
		exit 0
	fi
fi

# Fresh launch under DMTCP so future checkpoints are possible.
exec /dmtcp/bin/dmtcp_launch -j ${START_UP:-/usr/sbin/mosquitto}
