# ocireg

A tiny OCI registry for a WD My Cloud EX2 Ultra (ARMv7). Pulls are anonymous;
push needs a password set on the registry's home page.

- `./build.sh` builds `ocireg-armv7`.
- `./package.sh` builds the installable `dist/*.bin` (needs go, curl, docker).
- `./make-cert.sh NAS-IP-OR-NAME...` makes a private CA and server certificate.

## HTTPS (needed for OpenShell)

OpenShell's VM driver pulls images over HTTPS only, so the registry needs a
certificate that the pulling machine trusts. Everything is done on the
registry's home page (Apps > OCI Registry):

1. Set a push password. Certificate changes need it.
2. On any machine with openssl: `./make-cert.sh 192.168.1.239 nas.local`
   (list every address or name used after `--from`). This writes `certs/`.
3. On the home page, upload `server.crt`, `server.key` and `ca.crt`. HTTPS is
   active at once, with no restart, and plain HTTP is redirected to it.
4. On each machine that pulls (diamond), use the page's link to download the CA
   certificate. Check its SHA-256 fingerprint against the page, then trust it:

       sudo cp ocireg-ca.crt /usr/local/share/ca-certificates/ocireg-ca.crt
       sudo update-ca-certificates

   then restart the OpenShell gateway and driver services.
5. For `podman push`, the same system trust applies, or put the CA in
   `/etc/containers/certs.d/192.168.1.239:5000/ca.crt`.

The certificate downloads at `/tls/ca.crt` and `/tls/server.crt` are public and
work over plain HTTP, so a client can fetch the CA before it trusts the server.
The private key is never served. Uploading it over plain HTTP sends it
unencrypted, so do that on a network you trust.

Keep `certs/ca.key` private; it can sign certificates your machines will trust.

The server's `-tls-dir DIR` flag turns this on (the package passes
`ocireg_data/tls`). The files are `server.crt`, `server.key` and `ca.crt`; you
can also copy them there by hand.
