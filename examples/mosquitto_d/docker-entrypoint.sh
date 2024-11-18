#!/bin/bash
set -e
 

# Set permissions
user="$(id -u)"
if [ "$user" = '0' ]; then
	[ -d "/mosquitto" ] && chown -R mosquitto:mosquitto /mosquitto || true
fi

python3 init.py

/dmtcp/bin/dmtcp_launch -j /usr/sbin/mosquitto

#exec "$@"

