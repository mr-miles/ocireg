#!/bin/sh

[ -f /tmp/debug_apkg ] && echo "APKG_DEBUG: $0 $@" >> /tmp/debug_apkg

APKG_PATH=$(readlink -f $1)
. ${APKG_PATH}/env

# link the web page into the NAS web root
WEBDIR="/var/www/${APKG_NAME}/"
mkdir -p ${WEBDIR}
ln -sf ${APKG_PATH}/web/* ${WEBDIR}
