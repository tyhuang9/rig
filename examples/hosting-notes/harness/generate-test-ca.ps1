param([string]$HostGatewayIp = $env:FIXTURE_HOST_GATEWAY_IP)

$ErrorActionPreference = "Stop"
$root = Split-Path -Parent $PSCommandPath
$certs = Join-Path $root "certs"

if ($HostGatewayIp) {
  $parsedAddress = $null
  if ($HostGatewayIp -notmatch '^([0-9]{1,3}\.){3}[0-9]{1,3}$' -or
      -not [System.Net.IPAddress]::TryParse($HostGatewayIp, [ref]$parsedAddress) -or
      $parsedAddress.AddressFamily -ne [System.Net.Sockets.AddressFamily]::InterNetwork -or
      $parsedAddress.ToString() -ne $HostGatewayIp) {
    throw "HostGatewayIp must be a canonical IPv4 address."
  }
}

New-Item -ItemType Directory -Force -Path $certs | Out-Null

& openssl req -x509 -newkey rsa:2048 -nodes -days 2 `
  -keyout (Join-Path $certs "test-ca.key") -out (Join-Path $certs "test-ca.crt") `
  -subj "/CN=Rig hosting notes fixture CA"
if ($LASTEXITCODE -ne 0) { exit $LASTEXITCODE }

foreach ($name in @("postgres", "https")) {
  $hostName = "$name.fixture.test"
  $config = Join-Path $certs "openssl-$name.cnf"
  Copy-Item -LiteralPath (Join-Path $root "openssl-$name.cnf") -Destination $config -Force
  if ($HostGatewayIp) { Add-Content -LiteralPath $config -Value "IP.1 = $HostGatewayIp" -Encoding ascii }
  $key = Join-Path $certs "$hostName.key"
  $csr = Join-Path $certs "$hostName.csr"
  $certificate = Join-Path $certs "$hostName.crt"
  & openssl req -newkey rsa:2048 -nodes -keyout $key -out $csr -config $config
  if ($LASTEXITCODE -ne 0) { exit $LASTEXITCODE }
  & openssl x509 -req -days 2 -in $csr -CA (Join-Path $certs "test-ca.crt") `
    -CAkey (Join-Path $certs "test-ca.key") -CAcreateserial -out $certificate `
    -extfile $config -extensions v3_req
  if ($LASTEXITCODE -ne 0) { exit $LASTEXITCODE }
  Remove-Item -LiteralPath $csr, $config
}
