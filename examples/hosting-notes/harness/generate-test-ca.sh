#!/usr/bin/env sh
set -eu

root=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
certs="$root/certs"
mkdir -p "$certs"

openssl req -x509 -newkey rsa:2048 -nodes -days 2 \
  -keyout "$certs/test-ca.key" -out "$certs/test-ca.crt" \
  -subj "/CN=Rig hosting notes fixture CA"

for name in postgres https; do
  host="$name.fixture.test"
  openssl req -newkey rsa:2048 -nodes \
    -keyout "$certs/$host.key" -out "$certs/$host.csr" \
    -config "$root/openssl-$name.cnf"
  openssl x509 -req -days 2 -in "$certs/$host.csr" \
    -CA "$certs/test-ca.crt" -CAkey "$certs/test-ca.key" -CAcreateserial \
    -out "$certs/$host.crt" -extfile "$root/openssl-$name.cnf" -extensions v3_req
  rm "$certs/$host.csr"
done
chmod 600 "$certs"/*.key
