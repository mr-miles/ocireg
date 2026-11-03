#!/bin/sh

[ -f /tmp/debug_apkg ] && echo "APKG_DEBUG: $0 $@" >> /tmp/debug_apkg

APKG_PATH=$(readlink -f "${1:-$(dirname "$0")}")
. ${APKG_PATH}/env

mkdir -p ${DATA_DIR}/oci

[ -n "$(pidof ${BINARY})" ] && exit 0

# The push password is kept in ${DATA_DIR}/push-token and is set from the app's
# page (Apps > OCI Registry). Push stays off until one is set.
nohup ${APKG_PATH}/${BINARY} -dir ${DATA_DIR}/oci -addr :${PORT} \
    -token-file ${DATA_DIR}/push-token >> /tmp/${APKG_NAME}.log 2>&1 &
