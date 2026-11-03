#!/bin/sh

[ -f /tmp/debug_apkg ] && echo "APKG_DEBUG: $0 $@" >> /tmp/debug_apkg

[ -n "$(pidof ocireg)" ] && pkill ocireg

APKG_PATH=$(readlink -f $1)
. ${APKG_PATH}/env

# remove the app; ${DATA_DIR} (images, push token) is kept on purpose
rm -rf ${APKG_PATH}
rm -rf /var/www/${APKG_NAME}
