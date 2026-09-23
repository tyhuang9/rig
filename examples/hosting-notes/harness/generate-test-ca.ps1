$ErrorActionPreference = "Stop"
$root = Split-Path -Parent $PSCommandPath
$certs = Join-Path $root "certs"
New-Item -ItemType Directory -Force -Path $certs | Out-Null

& openssl req -x509 -newkey rsa:2048 -nodes -days 2 `
  -keyout (Join-Path $certs "test-ca.key") -out (Join-Path $certs "test-ca.crt") `
  -subj "/CN=Rig hosting notes fixture CA"
if ($LASTEXITCODE -ne 0) { exit $LASTEXITCODE }

foreach ($name in @("postgres", "https")) {
  $hostName = "$name.fixture.test"
  $config = Join-Path $root "openssl-$name.cnf"
  $key = Join-Path $certs "$hostName.key"
  $csr = Join-Path $certs "$hostName.csr"
  $certificate = Join-Path $certs "$hostName.crt"
  & openssl req -newkey rsa:2048 -nodes -keyout $key -out $csr -config $config
  if ($LASTEXITCODE -ne 0) { exit $LASTEXITCODE }
  & openssl x509 -req -days 2 -in $csr -CA (Join-Path $certs "test-ca.crt") `
    -CAkey (Join-Path $certs "test-ca.key") -CAcreateserial -out $certificate `
    -extfile $config -extensions v3_req
  if ($LASTEXITCODE -ne 0) { exit $LASTEXITCODE }
  Remove-Item -LiteralPath $csr
}
