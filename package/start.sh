#!/bin/sh

[ -f /tmp/debug_apkg ] && echo "APKG_DEBUG: $0 $@" >> /tmp/debug_apkg

APKG_PATH=$(readlink -f "${1:-$(dirname "$0")}")
. ${APKG_PATH}/env

mkdir -p ${DATA_DIR}/oci

[ -n "$(pidof ${BINARY})" ] && exit 0

# The push password (${DATA_DIR}/push-token) and the HTTPS certificates
# (${DATA_DIR}/tls) are both managed on the app's page (Apps > OCI Registry).
# Push stays off until a password is set; HTTPS is used once a certificate is
# installed.
nohup ${APKG_PATH}/${BINARY} -dir ${DATA_DIR}/oci -addr :${PORT} \
    -token-file ${DATA_DIR}/push-token -tls-dir ${DATA_DIR}/tls >> /tmp/${APKG_NAME}.log 2>&1 &
