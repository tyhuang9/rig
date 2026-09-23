#!/usr/bin/env sh
set -eu

root=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
certs="$root/certs"

gateway_ip=${FIXTURE_HOST_GATEWAY_IP:-}
valid_gateway_ip() {
  case "$1" in *[!0-9.]* ) return 1 ;; esac
  printf '%s\n' "$1" | awk -F. '
    NF != 4 { exit 1 }
    {
      for (i = 1; i <= 4; i++) {
        if ($i !~ /^[0-9]+$/ || length($i) > 3 || $i ~ /^0[0-9]/ || $i + 0 > 255) exit 1
      }
    }
    END { if (NR != 1) exit 1 }
  '
}
if [ -n "$gateway_ip" ] && ! valid_gateway_ip "$gateway_ip"; then
  printf 'FIXTURE_HOST_GATEWAY_IP must be a canonical IPv4 address.\n' >&2
  exit 1
fi

mkdir -p "$certs"

openssl req -x509 -newkey rsa:2048 -nodes -days 2 \
  -keyout "$certs/test-ca.key" -out "$certs/test-ca.crt" \
  -subj "/CN=Rig hosting notes fixture CA"

for name in postgres https; do
  host="$name.fixture.test"
  config="$certs/openssl-$name.cnf"
  cp "$root/openssl-$name.cnf" "$config"
  if [ -n "$gateway_ip" ]; then
    printf 'IP.1 = %s\n' "$gateway_ip" >> "$config"
  fi
  openssl req -newkey rsa:2048 -nodes \
    -keyout "$certs/$host.key" -out "$certs/$host.csr" \
    -config "$config"
  openssl x509 -req -days 2 -in "$certs/$host.csr" \
    -CA "$certs/test-ca.crt" -CAkey "$certs/test-ca.key" -CAcreateserial \
    -out "$certs/$host.crt" -extfile "$config" -extensions v3_req
  rm "$certs/$host.csr" "$config"
done
chmod 600 "$certs"/*.key
