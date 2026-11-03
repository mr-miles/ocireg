#!/bin/sh

[ -f /tmp/debug_apkg ] && echo "APKG_DEBUG: $0 $@" >> /tmp/debug_apkg

APKG_PATH=$(readlink -f "${1:-$(dirname "$0")}")
. ${APKG_PATH}/env

mkdir -p ${DATA_DIR}/oci

# Push is disabled unless this file exists and holds a password.
if [ -s ${DATA_DIR}/push-token ]; then
    export OCIREG_PUSH_TOKEN=$(cat ${DATA_DIR}/push-token)
fi

[ -n "$(pidof ${BINARY})" ] && exit 0

nohup ${APKG_PATH}/${BINARY} -dir ${DATA_DIR}/oci -addr :${PORT} >> /tmp/${APKG_NAME}.log 2>&1 &
