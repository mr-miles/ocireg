#!/usr/bin/env bash
# Make a private CA and a server certificate for the registry.
#
#   ./make-cert.sh 192.168.1.239 nas.local
#
# Every argument becomes a name the certificate is valid for: IP addresses and
# host names, whatever clients type after "--from". Output is in certs/:
#   ca.crt                 install on each machine that pulls (see README.md)
#   server.crt server.key  copy to the NAS (see README.md)
# ca.key stays here; keep it private. Re-run to replace the server certificate;
# an existing CA is reused so machines that already trust it keep working.
set -euo pipefail
[ $# -ge 1 ] || { echo "usage: $0 <ip-or-name>..." >&2; exit 2; }
cd "$(dirname "$0")"
mkdir -p certs
cd certs

if [ ! -f ca.key ] || [ ! -f ca.crt ]; then
    openssl ecparam -name prime256v1 -genkey -noout -out ca.key
    openssl req -x509 -new -key ca.key -sha256 -days 3650 -subj "/CN=ocireg private CA" \
        -addext "basicConstraints=critical,CA:TRUE,pathlen:0" \
        -addext "keyUsage=critical,keyCertSign,cRLSign" -out ca.crt
    chmod 600 ca.key
fi

san=""
for n in "$@"; do
    if [[ "$n" =~ ^[0-9.]+$ ]] || [[ "$n" == *:* ]]; then san+="IP:$n,"; else san+="DNS:$n,"; fi
done
san="${san%,}"

openssl ecparam -name prime256v1 -genkey -noout -out server.key
openssl req -new -key server.key -subj "/CN=$1" -out server.csr
openssl x509 -req -in server.csr -CA ca.crt -CAkey ca.key -CAcreateserial -sha256 -days 825 \
    -extfile <(printf 'basicConstraints=CA:FALSE\nkeyUsage=critical,digitalSignature\nextendedKeyUsage=serverAuth\nsubjectAltName=%s\n' "$san") \
    -out server.crt
rm -f server.csr ca.srl
chmod 600 server.key
echo "Made certs/server.crt valid for: $san (825 days)"
