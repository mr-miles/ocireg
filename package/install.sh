#!/bin/sh

log=/tmp/debug_apkg
echo "APKG_DEBUG: $0 $@" >> $log

INSTALL_DIR=$(readlink -f $1)
. ${INSTALL_DIR}/env

NAS_PROG=$(readlink -f $2)

echo "move package data" >> $log
mv ${INSTALL_DIR} ${NAS_PROG} 2>&1 >> $log

chmod +x ${NAS_PROG}/${APKG_NAME}/${BINARY}
mkdir -p ${DATA_DIR}/oci
