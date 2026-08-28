[CmdletBinding()]
param(
    [switch]$SelfTest,
    [switch]$BehaviorTest,
    [switch]$LinuxIntegrationTest,
    [string]$EnvironmentFile,
    [string]$SecretDirectory,
    [string]$TrustedDeploymentAnchor,
    [ValidateSet('baseline', 'direct-tls')]
    [string]$DeploymentMode
)

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'

$builderReference = 'docker.io/library/golang:1.26.7-bookworm@sha256:e8c859f5632dcfde7b32d2012b4351728f6437930887c2f6a91ea242459e5514'
$runtimeReference = 'gcr.io/distroless/static-debian13:nonroot@sha256:1c2c046bc09ed40fad370b599a0b1ae7987f55b01e247cf27a7c27cd97e5bbc7'
$postgresReference = 'docker.io/library/postgres:18.6-bookworm@sha256:1c59e2c3c818eaa0f0628f695b36e7c9e362d6b219b36a54a32df645cbd7e1af'
$frontendReference = 'docker/dockerfile:1.7@sha256:a57df69d0ea827fb7266491f2813635de6f17269be881f696fbfdf2d83dda33e'
$shaImagePattern = '^[a-z0-9][a-z0-9._:/-]*@sha256:[0-9a-f]{64}$'
$maximumEnvironmentBytes = 65536
$minimumComposeVersion = [version]'2.30.0'
$expectedDeploymentAnchor = '/etc/rig-relay'

function Add-CheckError {
    param(
        [System.Collections.Generic.List[string]]$Errors,
        [string]$Code
    )
    if (-not $Errors.Contains($Code)) {
        $Errors.Add($Code)
    }
}

function Test-ContainsAll {
    param(
        [string]$Text,
        [string[]]$Required,
        [System.Collections.Generic.List[string]]$Errors,
        [string]$Code
    )
    foreach ($value in $Required) {
        if (-not $Text.Contains($value)) {
            Add-CheckError $Errors $Code
            return
        }
    }
}

function Test-DockerfileText {
    param([string]$Text)
    $errors = [System.Collections.Generic.List[string]]::new()
    if (-not $Text.StartsWith("# syntax=$frontendReference`n", [System.StringComparison]::Ordinal)) {
        Add-CheckError $errors 'docker_frontend_reference'
    }
    $fromMatches = [regex]::Matches($Text, '(?m)^FROM\s+(?:--platform=\$BUILDPLATFORM\s+)?([^\s]+)(?:\s+AS\s+[A-Za-z0-9_-]+)?\s*$')
    if ($fromMatches.Count -ne 2) {
        Add-CheckError $errors 'docker_from_count'
    }
    else {
        foreach ($match in $fromMatches) {
            if (-not [regex]::IsMatch($match.Groups[1].Value, $shaImagePattern, [System.Text.RegularExpressions.RegexOptions]::CultureInvariant)) {
                Add-CheckError $errors 'docker_from_pin'
            }
        }
        if ($fromMatches[0].Groups[1].Value -cne $builderReference) {
            Add-CheckError $errors 'docker_builder_reference'
        }
        if ($fromMatches[1].Groups[1].Value -cne $runtimeReference) {
            Add-CheckError $errors 'docker_runtime_reference'
        }
    }
    Test-ContainsAll $Text @(
        'FROM --platform=$BUILDPLATFORM',
        'ARG TARGETOS=linux',
        'ARG TARGETARCH',
        'CGO_ENABLED=0 GOOS="$TARGETOS" GOARCH="$TARGETARCH" GOTOOLCHAIN=local',
        'go build -mod=readonly -trimpath -buildvcs=false -ldflags="-s -w -buildid="',
        'COPY --from=build --chmod=0555 /out/rig-relay /usr/local/bin/rig-relay',
        'COPY --from=build --chmod=0555 /out/rig-relay-probe /usr/local/bin/rig-relay-probe',
        'USER 65532:65532',
        'ENTRYPOINT ["/usr/local/bin/rig-relay"]',
        'org.opencontainers.image.source="https://github.com/hostd/hostd"'
    ) $errors 'docker_build_contract'
    if ($Text -match '(?im)^\s*ADD\s+' -or $Text -match '(?im)^\s*COPY\s+\.\s+' -or $Text -match '(?im)\b(curl|wget|apt-get|apk)\b') {
        Add-CheckError $errors 'docker_context_or_packages'
    }
    if ($fromMatches.Count -eq 2) {
        $runtimeText = $Text.Substring($fromMatches[1].Index)
        if ($runtimeText -match '(?im)^\s*RUN\s+' -or $runtimeText -match '(?im)^\s*CMD\s+(?!\[)') {
            Add-CheckError $errors 'docker_runtime_shell'
        }
    }
    return $errors.ToArray()
}

function Test-DockerignoreText {
    param([string]$Text)
    $errors = [System.Collections.Generic.List[string]]::new()
    $lines = @($Text -split "`r?`n" | ForEach-Object { $_.Trim() } | Where-Object { $_ -ne '' -and -not $_.StartsWith('#') })
    $required = @(
        '**', '!go.mod', '!go.sum', '!cmd/', '!cmd/rig-relay/', '!cmd/rig-relay/**',
        '!cmd/rig-relay-probe/', '!cmd/rig-relay-probe/**', '!internal/', '!internal/relay/', '!internal/relay/**'
    )
    if ($lines.Count -eq 0 -or $lines[0] -cne '**') {
        Add-CheckError $errors 'context_deny_default'
    }
    foreach ($line in $required) {
        if ($lines -cnotcontains $line) {
            Add-CheckError $errors 'context_required_source'
        }
    }
    foreach ($line in $lines) {
        if ($required -cnotcontains $line) {
            Add-CheckError $errors 'context_allowlist'
        }
    }
    return $errors.ToArray()
}

function Test-SafeIdentifier {
    param([string]$Value)
    return $null -ne $Value -and $Value.Length -ge 1 -and $Value.Length -le 255 -and $Value -cmatch '^[A-Za-z0-9._-]+$'
}

function Test-DNSName {
    param([string]$Value)
    if ($null -eq $Value -or $Value.Length -lt 1 -or $Value.Length -gt 253 -or $Value.EndsWith('.', [System.StringComparison]::Ordinal) -or $Value.Contains('*')) {
        return $false
    }
    $parsedAddress = $null
    if ([System.Net.IPAddress]::TryParse($Value, [ref]$parsedAddress)) {
        return $false
    }
    foreach ($label in @($Value -split '\.')) {
        if ($label.Length -lt 1 -or $label.Length -gt 63 -or $label[0] -eq '-' -or $label[$label.Length - 1] -eq '-' -or $label -cnotmatch '^[A-Za-z0-9-]+$') {
            return $false
        }
    }
    return $true
}

function Test-PublicHTTPSURL {
    param([string]$Value)
    if ($null -eq $Value -or $Value.Length -lt 1 -or $Value.Length -gt 2048 -or $Value -cnotmatch '^https://[^/?#\\]+/?$') {
        return $false
    }
    $parsed = $null
    if (-not [System.Uri]::TryCreate($Value, [System.UriKind]::Absolute, [ref]$parsed) -or $null -eq $parsed -or
        $parsed.Scheme -cne 'https' -or $parsed.Host -eq '' -or $parsed.UserInfo -ne '' -or $parsed.Query -ne '' -or $parsed.Fragment -ne '' -or
        ($parsed.AbsolutePath -cne '/' -and $parsed.AbsolutePath -cne '')) {
        return $false
    }
    if (-not $parsed.IsDefaultPort -and ($parsed.Port -lt 1 -or $parsed.Port -gt 65535)) {
        return $false
    }
    if ($parsed.HostNameType -eq [System.UriHostNameType]::Dns) {
        if (-not (Test-DNSName $parsed.DnsSafeHost)) {
            return $false
        }
    }
    elseif ($parsed.HostNameType -ne [System.UriHostNameType]::IPv4 -and $parsed.HostNameType -ne [System.UriHostNameType]::IPv6) {
        return $false
    }
    $canonicalWithoutSlash = "https://$($parsed.Authority)"
    return $Value -ceq $canonicalWithoutSlash -or $Value -ceq "$canonicalWithoutSlash/"
}

function Test-PositiveInt64 {
    param([string]$Value)
    if ($null -eq $Value -or $Value -cnotmatch '^\+?[0-9]+$') {
        return $false
    }
    $parsed = [int64]0
    return [int64]::TryParse($Value, [System.Globalization.NumberStyles]::AllowLeadingSign, [System.Globalization.CultureInfo]::InvariantCulture, [ref]$parsed) -and $parsed -gt 0
}

function Test-PublishPort {
    param([string]$Value)
    if ($null -eq $Value -or $Value -cnotmatch '^[0-9]+$') {
        return $false
    }
    $parsed = [uint32]0
    return [uint32]::TryParse($Value, [System.Globalization.NumberStyles]::None, [System.Globalization.CultureInfo]::InvariantCulture, [ref]$parsed) -and $parsed -ge 1 -and $parsed -le 65535
}

function Test-EdgeNetworkName {
    param([string]$Value)
    return $null -ne $Value -and $Value.Length -ge 1 -and $Value.Length -le 128 -and $Value -cmatch '^[A-Za-z0-9][A-Za-z0-9_.-]*$'
}

function Test-EnvironmentText {
    param(
        [string]$Text,
        [switch]$Example
    )
    $errors = [System.Collections.Generic.List[string]]::new()
    $values = @{}
    $allowed = @(
        'HOSTD_RELAY_IMAGE', 'HOSTD_RELAY_PUBLIC_BASE_URL', 'HOSTD_RELAY_GITHUB_CLIENT_ID',
        'HOSTD_RELAY_GITHUB_APP_ID', 'HOSTD_RELAY_TLS_SERVER_NAME', 'HOSTD_RELAY_EDGE_NETWORK',
        'HOSTD_RELAY_SECRET_DIRECTORY', 'HOSTD_RELAY_PUBLISH_ADDRESS', 'HOSTD_RELAY_PUBLISH_PORT'
    )
    foreach ($line in @($Text -split "`n")) {
        if ($line -eq '' -or $line.StartsWith('#')) {
            continue
        }
        if ($line.Contains("`r") -or $line -cne $line.Trim()) {
            Add-CheckError $errors 'env_syntax'
            continue
        }
        if ($line -notmatch '^([A-Z][A-Z0-9_]*)=([^\r\n]*)$') {
            Add-CheckError $errors 'env_syntax'
            continue
        }
        $name, $value = $matches[1], $matches[2]
        if ($allowed -cnotcontains $name) {
            Add-CheckError $errors 'env_unknown_key'
        }
        if ($values.ContainsKey($name)) {
            Add-CheckError $errors 'env_duplicate'
            continue
        }
        $values[$name] = $value
        if ($name -cne 'HOSTD_RELAY_SECRET_DIRECTORY' -and $name -match '(PASSWORD|SECRET|TOKEN|PRIVATE_KEY|WEBHOOK|ENROLLMENT_KEY|POSTGRES_DSN)') {
            Add-CheckError $errors 'env_contains_secret_key'
        }
    }
    foreach ($required in @(
        'HOSTD_RELAY_IMAGE', 'HOSTD_RELAY_PUBLIC_BASE_URL', 'HOSTD_RELAY_GITHUB_CLIENT_ID',
        'HOSTD_RELAY_GITHUB_APP_ID', 'HOSTD_RELAY_TLS_SERVER_NAME', 'HOSTD_RELAY_EDGE_NETWORK',
        'HOSTD_RELAY_SECRET_DIRECTORY', 'HOSTD_RELAY_PUBLISH_ADDRESS', 'HOSTD_RELAY_PUBLISH_PORT'
    )) {
        if (-not $values.ContainsKey($required)) {
            Add-CheckError $errors 'env_required_nonsecret'
        }
    }
    if ($values.Count -ne $allowed.Count) {
        Add-CheckError $errors 'env_key_cardinality'
    }
    if ($values.ContainsKey('HOSTD_RELAY_IMAGE')) {
        if ($Example) {
            if ($values['HOSTD_RELAY_IMAGE'] -cne 'registry.example.invalid/hostd/rig-relay@sha256:REPLACE_WITH_64_LOWERCASE_HEX_CHARACTERS') {
                Add-CheckError $errors 'env_example_image_placeholder'
            }
        }
        elseif (-not [regex]::IsMatch($values['HOSTD_RELAY_IMAGE'], $shaImagePattern, [System.Text.RegularExpressions.RegexOptions]::CultureInvariant)) {
            Add-CheckError $errors 'env_relay_image_pin'
        }
    }
    if ($values.ContainsKey('HOSTD_RELAY_PUBLIC_BASE_URL') -and -not (Test-PublicHTTPSURL $values['HOSTD_RELAY_PUBLIC_BASE_URL'])) {
        Add-CheckError $errors 'env_public_url'
    }
    if ($values.ContainsKey('HOSTD_RELAY_GITHUB_CLIENT_ID') -and -not (Test-SafeIdentifier $values['HOSTD_RELAY_GITHUB_CLIENT_ID'])) {
        Add-CheckError $errors 'env_github_client_id'
    }
    if ($values.ContainsKey('HOSTD_RELAY_GITHUB_APP_ID') -and -not (Test-PositiveInt64 $values['HOSTD_RELAY_GITHUB_APP_ID'])) {
        Add-CheckError $errors 'env_github_app_id'
    }
    if ($values.ContainsKey('HOSTD_RELAY_TLS_SERVER_NAME') -and -not (Test-DNSName $values['HOSTD_RELAY_TLS_SERVER_NAME'])) {
        Add-CheckError $errors 'env_tls_sni'
    }
    if ($values.ContainsKey('HOSTD_RELAY_SECRET_DIRECTORY') -and -not [System.IO.Path]::IsPathRooted($values['HOSTD_RELAY_SECRET_DIRECTORY'])) {
        Add-CheckError $errors 'env_secret_directory_absolute'
    }
    if ($values.ContainsKey('HOSTD_RELAY_PUBLISH_ADDRESS') -and $values['HOSTD_RELAY_PUBLISH_ADDRESS'] -cne '127.0.0.1') {
        Add-CheckError $errors 'env_public_default'
    }
    if ($values.ContainsKey('HOSTD_RELAY_PUBLISH_PORT') -and -not (Test-PublishPort $values['HOSTD_RELAY_PUBLISH_PORT'])) {
        Add-CheckError $errors 'env_publish_port'
    }
    if ($values.ContainsKey('HOSTD_RELAY_EDGE_NETWORK') -and -not (Test-EdgeNetworkName $values['HOSTD_RELAY_EDGE_NETWORK'])) {
        Add-CheckError $errors 'env_edge_network'
    }
    return $errors.ToArray()
}

function ConvertFrom-EnvironmentText {
    param([string]$Text)
    $values = @{}
    foreach ($line in @($Text -split "`n")) {
        if ($line -eq '' -or $line.StartsWith('#')) {
            continue
        }
        if ($line -match '^([A-Z][A-Z0-9_]*)=([^\r\n]*)$') {
            $values[$matches[1]] = $matches[2]
        }
    }
    return $values
}

function Test-DocumentationText {
    param([string]$Text)
    $errors = [System.Collections.Generic.List[string]]::new()
    Test-ContainsAll $Text @(
        'pwsh -NoProfile -File scripts/check-relay-packaging.ps1 -SelfTest',
        'pwsh -NoProfile -File scripts/check-relay-packaging.ps1 -BehaviorTest',
        'HOSTD_RELAY_RUN_LINUX_PREFLIGHT_TESTS=1',
        '-LinuxIntegrationTest',
        'docker buildx build --file deploy/relay/Dockerfile',
        '-TrustedDeploymentAnchor /etc/rig-relay',
        '-SecretDirectory /etc/rig-relay/secrets',
        '-DeploymentMode baseline',
        '-DeploymentMode direct-tls',
        'Docker Compose v2.30.0 or newer',
        'residual root/admin TOCTOU',
        '30 days',
        '7 days',
        'GitHub does not automatically redeliver failed webhook deliveries',
        'UID/GID `65532:65532`',
        'UID/GID `999:999`',
        'must contain no trailing CR or LF byte',
        'Windows Docker Desktop deployment with these file-backed secrets is unsupported',
        'Those fakes do not prove Linux `lstat`',
        'stat -c ''%u:%g %a %F %n''',
        'Unit 9',
        'Cloud account, DNS, region, and live provisioning are outside this repository delivery.'
    ) $errors 'docs_required_operations'
    return $errors.ToArray()
}

function Test-SecretsIgnoreText {
    param([string]$Text)
    $lines = @($Text -split "`r?`n" | ForEach-Object { $_.Trim() } | Where-Object { $_ -ne '' -and -not $_.StartsWith('#') })
    if ($lines.Count -ne 2 -or $lines[0] -cne '*' -or $lines[1] -cne '!.gitignore') {
        return @('secrets_gitignore')
    }
    return @()
}

function Get-LStatMetadata {
    param(
        [string]$Path,
        [System.Collections.IDictionary]$Adapter
    )
    if ($null -ne $Adapter -and $Adapter.Contains('LStat')) {
        return & $Adapter['LStat'] $Path
    }
    $lines = @(& /usr/bin/stat '--format=%F|%u|%g|%a|%s|%d|%i|%Y|%Z' '--' $Path 2>$null)
    if ($LASTEXITCODE -ne 0 -or $lines.Count -ne 1 -or $lines[0] -notmatch '^([^|]+)\|([0-9]+)\|([0-9]+)\|([0-7]+)\|([0-9]+)\|([0-9]+)\|([0-9]+)\|(-?[0-9]+)\|(-?[0-9]+)$') {
        return $null
    }
    return @{
        Type = $matches[1]
        UID = $matches[2]
        GID = $matches[3]
        Mode = $matches[4]
        ModeValue = [Convert]::ToInt32($matches[4], 8)
        Size = [int64]$matches[5]
        Device = $matches[6]
        Inode = $matches[7]
        MTime = $matches[8]
        CTime = $matches[9]
    }
}

function Read-DeploymentBytes {
    param(
        [string]$Path,
        [System.Collections.IDictionary]$Adapter
    )
    if ($null -ne $Adapter -and $Adapter.Contains('ReadAllBytes')) {
        return [byte[]](& $Adapter['ReadAllBytes'] $Path)
    }
    return [System.IO.File]::ReadAllBytes($Path)
}

function Get-DeploymentDirectoryEntries {
    param(
        [string]$Path,
        [System.Collections.IDictionary]$Adapter
    )
    if ($null -ne $Adapter -and $Adapter.Contains('ListEntries')) {
        return @(& $Adapter['ListEntries'] $Path)
    }
    return @(Get-ChildItem -LiteralPath $Path -Force | ForEach-Object { $_.Name })
}

function Get-CanonicalDeploymentPath {
    param([string]$Path)
    if ($null -eq $Path -or -not $Path.StartsWith('/', [System.StringComparison]::Ordinal) -or ($Path.Length -gt 1 -and $Path.EndsWith('/', [System.StringComparison]::Ordinal))) {
        return $null
    }
    $components = @($Path.Substring(1) -split '/')
    if ($components.Count -eq 0) {
        return '/'
    }
    foreach ($component in $components) {
        if ($component -eq '' -or $component -eq '.' -or $component -eq '..') {
            return $null
        }
    }
    return '/' + ($components -join '/')
}

function Get-ProtectedPathSegments {
    param([string]$Path)
    $segments = [System.Collections.Generic.List[string]]::new()
    $segments.Add('/')
    $current = ''
    foreach ($component in @($Path.TrimStart('/') -split '/')) {
        if ($component -eq '') {
            continue
        }
        $current += "/$component"
        $segments.Add($current)
    }
    return $segments.ToArray()
}

function Test-ProtectedHierarchy {
    param(
        [string]$Path,
        [ValidateSet('file', 'directory')]
        [string]$LeafType,
        [string]$AdministratorUID,
        [System.Collections.Generic.List[string]]$Errors,
        [string]$CodePrefix,
        [System.Collections.IDictionary]$Adapter
    )
    $segments = @(Get-ProtectedPathSegments $Path)
    for ($index = 0; $index -lt $segments.Count; $index++) {
        $segment = $segments[$index]
        $metadata = Get-LStatMetadata $segment $Adapter
        if ($null -eq $metadata) {
            Add-CheckError $Errors "${CodePrefix}_metadata"
            return
        }
        $isLeaf = $index -eq ($segments.Count - 1)
        if ($isLeaf -and $LeafType -eq 'file') {
            if ($metadata.Type -cne 'regular file') {
                Add-CheckError $Errors "${CodePrefix}_nonregular"
            }
            continue
        }
        if ($metadata.Type -cne 'directory') {
            Add-CheckError $Errors "${CodePrefix}_ancestor_type"
            return
        }
        if ($metadata.UID -cne '0' -and $metadata.UID -cne $AdministratorUID) {
            Add-CheckError $Errors "${CodePrefix}_ancestor_owner"
        }
        if (($metadata.ModeValue -band 0x12) -ne 0) {
            Add-CheckError $Errors "${CodePrefix}_ancestor_writable"
        }
    }
}

function Test-ProtectedDeploymentPaths {
    param(
        [string]$Anchor,
        [string]$EnvironmentPath,
        [string]$SecretsPath,
        [System.Collections.IDictionary]$Adapter,
        [string]$ExpectedAnchor = $expectedDeploymentAnchor
    )
    $errors = [System.Collections.Generic.List[string]]::new()
    $powerShellMajor = if ($null -ne $Adapter -and $Adapter.Contains('PowerShellMajor')) { [int]$Adapter['PowerShellMajor'] } else { $PSVersionTable.PSVersion.Major }
    $platform = if ($null -ne $Adapter -and $Adapter.Contains('Platform')) { [string]$Adapter['Platform'] } elseif ([System.Environment]::OSVersion.Platform -eq [System.PlatformID]::Win32NT) { 'windows' } else { 'linux' }
    if ($powerShellMajor -lt 7) {
        Add-CheckError $errors 'powershell_7_required'
        return $errors.ToArray()
    }
    if ($platform -cne 'linux') {
        Add-CheckError $errors 'windows_compose_secrets_unsupported'
        return $errors.ToArray()
    }
    if ($null -eq $Adapter -and (-not (Test-Path -LiteralPath '/usr/bin/stat' -PathType Leaf) -or -not (Test-Path -LiteralPath '/usr/bin/id' -PathType Leaf))) {
        Add-CheckError $errors 'deployment_gnu_tools_unavailable'
        return $errors.ToArray()
    }
    $resolvedAnchor = Get-CanonicalDeploymentPath $Anchor
    $resolvedEnvironment = Get-CanonicalDeploymentPath $EnvironmentPath
    $resolvedSecrets = Get-CanonicalDeploymentPath $SecretsPath
    if ($null -eq $resolvedAnchor -or $null -eq $resolvedEnvironment -or $null -eq $resolvedSecrets) {
        Add-CheckError $errors 'deployment_paths_not_absolute'
        return $errors.ToArray()
    }
    if ($resolvedAnchor -cne $ExpectedAnchor -or $resolvedAnchor -cne $Anchor -or $resolvedEnvironment -cne $EnvironmentPath -or $resolvedSecrets -cne $SecretsPath) {
        Add-CheckError $errors 'deployment_path_not_canonical'
        return $errors.ToArray()
    }
    if ($resolvedEnvironment -cne "$resolvedAnchor/relay.env" -or $resolvedSecrets -cne "$resolvedAnchor/secrets") {
        Add-CheckError $errors 'deployment_path_coupling'
        return $errors.ToArray()
    }
    $usingAdapterUID = $null -ne $Adapter -and $Adapter.Contains('CurrentUID')
    $administratorUID = @(if ($usingAdapterUID) { & $Adapter['CurrentUID'] } else { & /usr/bin/id '-u' 2>$null })
    if ((-not $usingAdapterUID -and $LASTEXITCODE -ne 0) -or $administratorUID.Count -ne 1 -or $administratorUID[0] -notmatch '^[0-9]+$') {
        Add-CheckError $errors 'deployment_administrator_uid'
        return $errors.ToArray()
    }
    $administratorUID = $administratorUID[0]
    Test-ProtectedHierarchy $resolvedEnvironment 'file' $administratorUID $errors 'env_file' $Adapter
    Test-ProtectedHierarchy $resolvedSecrets 'directory' $administratorUID $errors 'secret_directory' $Adapter
    if ($errors.Count -ne 0) {
        return $errors.ToArray()
    }

    $environmentMetadata = Get-LStatMetadata $resolvedEnvironment $Adapter
    if ($environmentMetadata.UID -cne '0' -and $environmentMetadata.UID -cne $administratorUID) {
        Add-CheckError $errors 'env_file_owner'
    }
    if ($environmentMetadata.Mode -cne '400' -and $environmentMetadata.Mode -cne '600') {
        Add-CheckError $errors 'env_file_mode'
    }
    if ($environmentMetadata.Size -le 0 -or $environmentMetadata.Size -gt $maximumEnvironmentBytes) {
        Add-CheckError $errors 'env_file_size'
    }

    $secretDirectoryMetadata = Get-LStatMetadata $resolvedSecrets $Adapter
    if ($secretDirectoryMetadata.UID -cne '0' -or $secretDirectoryMetadata.GID -cne '0') {
        Add-CheckError $errors 'secret_directory_owner'
    }
    if ($secretDirectoryMetadata.Mode -cne '700') {
        Add-CheckError $errors 'secret_directory_mode'
    }

    [byte[]]$environmentBytes = $null
    try {
        $environmentBytes = Read-DeploymentBytes $resolvedEnvironment $Adapter
        if ($environmentBytes.Length -le 0 -or $environmentBytes.Length -gt $maximumEnvironmentBytes) {
            Add-CheckError $errors 'env_file_size'
        }
        if ([Array]::IndexOf($environmentBytes, [byte]13) -ge 0) {
            Add-CheckError $errors 'env_file_crlf'
        }
        $encoding = [System.Text.UTF8Encoding]::new($false, $true)
        $environmentText = $encoding.GetString($environmentBytes)
        $errors.AddRange([string[]]@(Test-EnvironmentText $environmentText))
        $environmentValues = ConvertFrom-EnvironmentText $environmentText
        if (-not $environmentValues.ContainsKey('HOSTD_RELAY_SECRET_DIRECTORY') -or (Get-CanonicalDeploymentPath $environmentValues['HOSTD_RELAY_SECRET_DIRECTORY']) -cne $resolvedSecrets) {
            Add-CheckError $errors 'env_secret_directory_coupling'
        }
    }
    catch {
        Add-CheckError $errors 'env_file_unreadable'
    }
    finally {
        if ($null -ne $environmentBytes) {
            [Array]::Clear($environmentBytes, 0, $environmentBytes.Length)
        }
    }

    $expected = [ordered]@{
        'postgres-password.txt'       = @{ UID = '999'; GID = '999'; Maximum = 4096; SingleLine = $true; Exact = 0 }
        'relay-postgres-dsn.txt'      = @{ UID = '65532'; GID = '65532'; Maximum = 16384; SingleLine = $true; Exact = 0 }
        'github-client-secret.txt'    = @{ UID = '65532'; GID = '65532'; Maximum = 4096; SingleLine = $true; Exact = 0 }
        'github-app-private-key.pem'  = @{ UID = '65532'; GID = '65532'; Maximum = 1048576; SingleLine = $false; Exact = 0 }
        'github-webhook-secret.txt'   = @{ UID = '65532'; GID = '65532'; Maximum = 65536; SingleLine = $true; Exact = 0 }
        'enrollment-key.bin'          = @{ UID = '65532'; GID = '65532'; Maximum = 32; SingleLine = $false; Exact = 32 }
        'relay-tls-certificate.pem'   = @{ UID = '65532'; GID = '65532'; Maximum = 4194304; SingleLine = $false; Exact = 0 }
        'relay-tls-private-key.pem'   = @{ UID = '65532'; GID = '65532'; Maximum = 4194304; SingleLine = $false; Exact = 0 }
        'relay-tls-ca.pem'            = @{ UID = '65532'; GID = '65532'; Maximum = 1048576; SingleLine = $false; Exact = 0 }
    }
    foreach ($entry in @(Get-DeploymentDirectoryEntries $resolvedSecrets $Adapter)) {
        if (-not $expected.Contains([string]$entry)) {
            Add-CheckError $errors 'secret_directory_extra_entry'
        }
    }
    foreach ($name in $expected.Keys) {
        $path = "$resolvedSecrets/$name"
        $metadata = Get-LStatMetadata $path $Adapter
        if ($null -eq $metadata) {
            Add-CheckError $errors 'secret_file_missing'
            continue
        }
        if ($metadata.Type -cne 'regular file') {
            Add-CheckError $errors 'secret_file_nonregular'
            continue
        }
        $specification = $expected[$name]
        if ($metadata.UID -cne $specification.UID -or $metadata.GID -cne $specification.GID) {
            Add-CheckError $errors 'secret_file_owner'
        }
        if ($metadata.Mode -cne '400' -and $metadata.Mode -cne '600') {
            Add-CheckError $errors 'secret_file_mode'
        }
        if ($metadata.Size -le 0 -or $metadata.Size -gt $specification.Maximum -or ($specification.Exact -gt 0 -and $metadata.Size -ne $specification.Exact)) {
            Add-CheckError $errors 'secret_file_size'
            continue
        }
        if ($specification.SingleLine -or $specification.Exact -gt 0) {
            [byte[]]$bytes = $null
            try {
                $bytes = Read-DeploymentBytes $path $Adapter
                if ($specification.SingleLine -and ([Array]::IndexOf($bytes, [byte]10) -ge 0 -or [Array]::IndexOf($bytes, [byte]13) -ge 0)) {
                    Add-CheckError $errors 'secret_file_line_ending'
                }
                if ($specification.Exact -gt 0 -and $bytes.Length -ne $specification.Exact) {
                    Add-CheckError $errors 'secret_file_size'
                }
            }
            catch {
                Add-CheckError $errors 'secret_file_unreadable'
            }
            finally {
                if ($null -ne $bytes) {
                    [Array]::Clear($bytes, 0, $bytes.Length)
                }
            }
        }
    }
    return $errors.ToArray()
}

function Test-ExactMapKeys {
    param(
        [System.Collections.IDictionary]$Map,
        [string[]]$Expected
    )
    if ($null -eq $Map -or $Map.Count -ne $Expected.Count) {
        return $false
    }
    foreach ($key in $Expected) {
        if (-not $Map.Contains($key)) {
            return $false
        }
    }
    return $true
}

function Test-ExactEffectiveNetworkKeys {
    param(
        [object]$Map,
        [string[]]$Expected
    )
    if ($Map -isnot [System.Collections.IDictionary]) {
        return $false
    }
    $expectedKeys = @($Expected)
    $ipamKeys = @($Map.Keys | Where-Object { [string]$_ -ieq 'ipam' })
    if ($ipamKeys.Count -gt 0) {
        if ($ipamKeys.Count -ne 1 -or [string]$ipamKeys[0] -cne 'ipam') {
            return $false
        }
        $ipam = $Map[$ipamKeys[0]]
        if ($ipam -isnot [System.Collections.IDictionary] -or $ipam.Count -ne 0) {
            return $false
        }
        $expectedKeys += 'ipam'
    }
    return Test-ExactMapKeys -Map $Map -Expected $expectedKeys
}

function Test-ExactStringArray {
    param(
        [object]$Value,
        [string[]]$Expected
    )
    if ($null -eq $Value -or $Value -is [string]) {
        return $false
    }
    $actual = @($Value)
    if ($actual.Count -ne $Expected.Count) {
        return $false
    }
    for ($index = 0; $index -lt $Expected.Count; $index++) {
        if ([string]$actual[$index] -cne $Expected[$index]) {
            return $false
        }
    }
    return $true
}

function ConvertTo-HashtableModel {
    param([object]$Value)
    if ($null -eq $Value) {
        return $null
    }
    if ($Value -is [System.Collections.IDictionary]) {
        $result = @{}
        foreach ($key in $Value.Keys) {
            $result[[string]$key] = ConvertTo-HashtableModel $Value[$key]
        }
        return $result
    }
    if ($Value -is [pscustomobject]) {
        $result = @{}
        foreach ($property in $Value.PSObject.Properties) {
            $result[$property.Name] = ConvertTo-HashtableModel $property.Value
        }
        return $result
    }
    if ($Value -is [System.Collections.IEnumerable] -and $Value -isnot [string]) {
        $items = [System.Collections.Generic.List[object]]::new()
        foreach ($item in $Value) {
            $items.Add((ConvertTo-HashtableModel $item))
        }
        Write-Output -NoEnumerate $items.ToArray()
        return
    }
    return $Value
}

function Test-StrictJSONSyntax {
    param([string]$Json)
    $options = [System.Text.Json.JsonDocumentOptions]::new()
    $options.AllowTrailingCommas = $false
    $options.CommentHandling = [System.Text.Json.JsonCommentHandling]::Disallow
    $document = $null
    try {
        $document = [System.Text.Json.JsonDocument]::Parse($Json, $options)
        return $true
    }
    catch [System.Text.Json.JsonException] {
        return $false
    }
    finally {
        if ($null -ne $document) {
            $document.Dispose()
        }
    }
}

function Get-JSONPropertyTokens {
    param(
        [string]$Json,
        [string]$PropertyName
    )
    $tokens = [System.Collections.Generic.List[object]]::new()
    for ($index = 0; $index -lt $Json.Length; $index++) {
        if ($Json[$index] -cne '"') {
            continue
        }
        $start = $index
        $index++
        while ($index -lt $Json.Length) {
            if ($Json[$index] -ceq '\') {
                $index += 2
                continue
            }
            if ($Json[$index] -ceq '"') {
                break
            }
            $index++
        }
        if ($index -ge $Json.Length) {
            break
        }
        $literal = $Json.Substring($start, $index - $start + 1)
        $cursor = $index + 1
        while ($cursor -lt $Json.Length -and $Json[$cursor] -in @(' ', "`t", "`r", "`n")) {
            $cursor++
        }
        if ($cursor -ge $Json.Length -or $Json[$cursor] -cne ':') {
            continue
        }
        $valueCursor = $cursor + 1
        while ($valueCursor -lt $Json.Length -and $Json[$valueCursor] -in @(' ', "`t", "`r", "`n")) {
            $valueCursor++
        }
        $isNullValue = $false
        if ($valueCursor + 4 -le $Json.Length -and $Json.Substring($valueCursor, 4) -ceq 'null') {
            $valueEnd = $valueCursor + 4
            $isNullValue = $valueEnd -eq $Json.Length -or $Json[$valueEnd] -in @(' ', "`t", "`r", "`n", ',', '}')
        }
        try {
            $name = ConvertFrom-Json -InputObject $literal
        }
        catch {
            continue
        }
        if ($name -is [string] -and [StringComparer]::OrdinalIgnoreCase.Equals($name, $PropertyName)) {
            $tokens.Add([pscustomobject]@{ Literal = $literal; Name = $name; IsNullValue = $isNullValue })
        }
    }
    return $tokens.ToArray()
}

function Test-EffectiveComposeJson {
    param(
        [string]$Json,
        [switch]$DirectTLS,
        [string]$ExpectedSecretDirectory,
        [System.Collections.IDictionary]$ExpectedEnvironment
    )
    $errors = [System.Collections.Generic.List[string]]::new()
    if (-not (Test-StrictJSONSyntax $Json)) {
        Add-CheckError $errors 'compose_effective_json_invalid'
        return $errors.ToArray()
    }
    $rawIPAMTokens = @(Get-JSONPropertyTokens -Json $Json -PropertyName 'ipam')
    $rawServiceNullTokens = @{
        command = @(Get-JSONPropertyTokens -Json $Json -PropertyName 'command')
        entrypoint = @(Get-JSONPropertyTokens -Json $Json -PropertyName 'entrypoint')
    }
    $acceptedServiceNullFields = @{ command = 0; entrypoint = 0 }
    try {
        $model = ConvertTo-HashtableModel (ConvertFrom-Json -InputObject $Json)
    }
    catch {
        Add-CheckError $errors 'compose_effective_json_invalid'
        return $errors.ToArray()
    }
    if ($null -eq $model -or $model -isnot [System.Collections.IDictionary] -or -not (Test-ExactMapKeys -Map $model -Expected @('name', 'networks', 'secrets', 'services', 'volumes')) -or [string]$model['name'] -cne 'rig-relay') {
        Add-CheckError $errors 'compose_effective_model_invalid'
        return $errors.ToArray()
    }
    $services = $model['services']
    if ($services -isnot [System.Collections.IDictionary] -or -not (Test-ExactMapKeys -Map $services -Expected @('postgres', 'relay'))) {
        Add-CheckError $errors 'compose_effective_services'
        return $errors.ToArray()
    }
    foreach ($serviceName in @('postgres', 'relay')) {
        $service = $services[$serviceName]
        if ($service -isnot [System.Collections.IDictionary]) {
            Add-CheckError $errors 'compose_effective_service_model'
            continue
        }
        $expectedServiceKeys = if ($serviceName -eq 'postgres') {
            @('cap_drop', 'command', 'cpus', 'entrypoint', 'environment', 'healthcheck', 'image', 'init', 'logging', 'mem_limit', 'networks', 'pids_limit', 'read_only', 'restart', 'secrets', 'security_opt', 'shm_size', 'stop_grace_period', 'tmpfs', 'user', 'volumes')
        }
        else {
            $keys = @('cap_drop', 'command', 'cpus', 'depends_on', 'entrypoint', 'environment', 'healthcheck', 'image', 'init', 'logging', 'mem_limit', 'networks', 'pids_limit', 'read_only', 'restart', 'secrets', 'security_opt', 'stop_grace_period', 'user')
            if ($DirectTLS) {
                $keys += 'ports'
            }
            $keys
        }
        if (-not (Test-ExactMapKeys -Map $service -Expected $expectedServiceKeys)) {
            Add-CheckError $errors 'compose_effective_service_keys'
            continue
        }
        foreach ($nullField in @('command', 'entrypoint')) {
            $exactFieldKeys = @($service.Keys | Where-Object { [string]$_ -ceq $nullField })
            if ($exactFieldKeys.Count -ne 1 -or $null -ne $service[$exactFieldKeys[0]]) {
                Add-CheckError $errors 'compose_effective_service_keys'
            }
            else {
                $acceptedServiceNullFields[$nullField]++
            }
        }
        $expectedUser = if ($serviceName -eq 'postgres') { '999:999' } else { '65532:65532' }
        if ([string]$service['user'] -cne $expectedUser -or $service['read_only'] -ne $true -or $service['init'] -ne $true) {
            Add-CheckError $errors 'compose_effective_nonroot_readonly'
        }
        if (-not (Test-ExactStringArray -Value $service['cap_drop'] -Expected @('ALL')) -or -not (Test-ExactStringArray -Value $service['security_opt'] -Expected @('no-new-privileges:true'))) {
            Add-CheckError $errors 'compose_effective_privilege_controls'
        }
        if ([string]$service['restart'] -cne 'unless-stopped' -or [string]$service['pids_limit'] -cne '256' -or [string]$service['stop_grace_period'] -cne '30s') {
            Add-CheckError $errors 'compose_effective_lifecycle_resources'
        }
        $expectedMemory = if ($serviceName -eq 'postgres') { @('1g', '1073741824') } else { @('512m', '536870912') }
        $expectedCPU = if ($serviceName -eq 'postgres') { @('1.5', '1500000000') } else { @('1', '1.0', '1000000000') }
        if ($expectedMemory -cnotcontains [string]$service['mem_limit'] -or $expectedCPU -cnotcontains [string]$service['cpus']) {
            Add-CheckError $errors 'compose_effective_lifecycle_resources'
        }
        $logging = $service['logging']
        if ($logging -isnot [System.Collections.IDictionary] -or -not (Test-ExactMapKeys -Map $logging -Expected @('driver', 'options')) -or [string]$logging['driver'] -cne 'local' -or
            $logging['options'] -isnot [System.Collections.IDictionary] -or -not (Test-ExactMapKeys -Map $logging['options'] -Expected @('compress', 'max-file', 'max-size')) -or
            [string]$logging['options']['max-size'] -cne '10m' -or [string]$logging['options']['max-file'] -cne '5' -or [string]$logging['options']['compress'] -cne 'true') {
            Add-CheckError $errors 'compose_effective_logging'
        }
        $volumes = @($service['volumes'])
        if ($serviceName -eq 'postgres') {
            if ($volumes.Count -ne 1 -or $volumes[0] -isnot [System.Collections.IDictionary] -or -not (Test-ExactMapKeys -Map $volumes[0] -Expected @('source', 'target', 'type', 'volume')) -or
                [string]$volumes[0]['type'] -cne 'volume' -or [string]$volumes[0]['source'] -cne 'relay-postgres-data' -or [string]$volumes[0]['target'] -cne '/var/lib/postgresql' -or
                $volumes[0]['volume'] -isnot [System.Collections.IDictionary] -or $volumes[0]['volume'].Count -ne 0) {
                Add-CheckError $errors 'compose_effective_volumes'
            }
        }
        elseif ($service.Contains('volumes') -and $volumes.Count -ne 0) {
            Add-CheckError $errors 'compose_effective_volumes'
        }
        foreach ($volume in $volumes) {
            if ($volume -is [System.Collections.IDictionary] -and ([string]$volume['type'] -ceq 'bind' -or [string]$volume['source'] -match 'docker\.sock' -or [string]$volume['target'] -match 'docker\.sock')) {
                Add-CheckError $errors 'compose_effective_volumes'
            }
        }
        $ports = @($service['ports'])
        if ($serviceName -eq 'postgres' -and $service.Contains('ports') -and $ports.Count -ne 0) {
            Add-CheckError $errors 'compose_effective_ports'
        }
        if ($serviceName -eq 'relay') {
            if (-not $DirectTLS -and $service.Contains('ports') -and $ports.Count -ne 0) {
                Add-CheckError $errors 'compose_effective_ports'
            }
            if ($DirectTLS) {
                if ($ports.Count -ne 1 -or $ports[0] -isnot [System.Collections.IDictionary] -or -not (Test-ExactMapKeys -Map $ports[0] -Expected @('host_ip', 'mode', 'protocol', 'published', 'target')) -or
                    [string]$ports[0]['host_ip'] -cne '127.0.0.1' -or [string]$ports[0]['mode'] -cne 'ingress' -or
                    [string]$ports[0]['target'] -cne '7346' -or [string]$ports[0]['published'] -cne [string]$ExpectedEnvironment['HOSTD_RELAY_PUBLISH_PORT'] -or [string]$ports[0]['protocol'] -cne 'tcp') {
                    Add-CheckError $errors 'compose_effective_ports'
                }
            }
        }
    }

    foreach ($nullField in @('command', 'entrypoint')) {
        $rawTokens = @($rawServiceNullTokens[$nullField])
        $exactTokens = @($rawTokens | Where-Object { [string]$_.Literal -ceq ('"' + $nullField + '"') })
        $exactNullTokens = @($exactTokens | Where-Object { $_.IsNullValue })
        if ($rawTokens.Count -ne 2 -or $exactTokens.Count -ne 2 -or $exactNullTokens.Count -ne 2 -or $rawTokens.Count -ne $acceptedServiceNullFields[$nullField]) {
            Add-CheckError $errors 'compose_effective_service_keys'
        }
    }

    $postgres = $services['postgres']
    $relay = $services['relay']
    if ([string]$postgres['image'] -cne $postgresReference -or [string]$relay['image'] -cne [string]$ExpectedEnvironment['HOSTD_RELAY_IMAGE']) {
        Add-CheckError $errors 'compose_effective_images'
    }
    $postgresEnvironment = $postgres['environment']
    if ($postgresEnvironment -isnot [System.Collections.IDictionary] -or -not (Test-ExactMapKeys -Map $postgresEnvironment -Expected @('POSTGRES_DB', 'POSTGRES_USER', 'POSTGRES_PASSWORD_FILE', 'POSTGRES_INITDB_ARGS')) -or
        [string]$postgresEnvironment['POSTGRES_DB'] -cne 'rig_relay' -or [string]$postgresEnvironment['POSTGRES_USER'] -cne 'rig_relay' -or
        [string]$postgresEnvironment['POSTGRES_PASSWORD_FILE'] -cne '/run/secrets/postgres_password' -or [string]$postgresEnvironment['POSTGRES_INITDB_ARGS'] -cne '--auth-host=scram-sha-256 --data-checksums') {
        Add-CheckError $errors 'compose_effective_environment'
    }
    $relayEnvironment = $relay['environment']
    $relayEnvironmentKeys = @(
        'HOSTD_RELAY_LISTEN_ADDRESS', 'HOSTD_RELAY_PUBLIC_BASE_URL', 'HOSTD_RELAY_LOOPBACK_DEVELOPMENT', 'HOSTD_RELAY_GITHUB_CLIENT_ID', 'HOSTD_RELAY_GITHUB_APP_ID',
        'HOSTD_RELAY_POSTGRES_DSN_FILE', 'HOSTD_RELAY_GITHUB_CLIENT_SECRET_FILE', 'HOSTD_RELAY_GITHUB_PRIVATE_KEY_FILE', 'HOSTD_RELAY_WEBHOOK_SECRET_FILE',
        'HOSTD_RELAY_ENROLLMENT_KEY_FILE', 'HOSTD_RELAY_TLS_CERTIFICATE_FILE', 'HOSTD_RELAY_TLS_PRIVATE_KEY_FILE'
    )
    if ($relayEnvironment -isnot [System.Collections.IDictionary] -or -not (Test-ExactMapKeys -Map $relayEnvironment -Expected $relayEnvironmentKeys) -or
        [string]$relayEnvironment['HOSTD_RELAY_LISTEN_ADDRESS'] -cne '0.0.0.0:7346' -or [string]$relayEnvironment['HOSTD_RELAY_LOOPBACK_DEVELOPMENT'] -cne 'false' -or
        [string]$relayEnvironment['HOSTD_RELAY_PUBLIC_BASE_URL'] -cne [string]$ExpectedEnvironment['HOSTD_RELAY_PUBLIC_BASE_URL'] -or
        [string]$relayEnvironment['HOSTD_RELAY_GITHUB_CLIENT_ID'] -cne [string]$ExpectedEnvironment['HOSTD_RELAY_GITHUB_CLIENT_ID'] -or
        [string]$relayEnvironment['HOSTD_RELAY_GITHUB_APP_ID'] -cne [string]$ExpectedEnvironment['HOSTD_RELAY_GITHUB_APP_ID'] -or
        [string]$relayEnvironment['HOSTD_RELAY_POSTGRES_DSN_FILE'] -cne '/run/secrets/relay_postgres_dsn' -or
        [string]$relayEnvironment['HOSTD_RELAY_GITHUB_CLIENT_SECRET_FILE'] -cne '/run/secrets/github_client_secret' -or
        [string]$relayEnvironment['HOSTD_RELAY_GITHUB_PRIVATE_KEY_FILE'] -cne '/run/secrets/github_app_private_key' -or
        [string]$relayEnvironment['HOSTD_RELAY_WEBHOOK_SECRET_FILE'] -cne '/run/secrets/github_webhook_secret' -or
        [string]$relayEnvironment['HOSTD_RELAY_ENROLLMENT_KEY_FILE'] -cne '/run/secrets/enrollment_key' -or
        [string]$relayEnvironment['HOSTD_RELAY_TLS_CERTIFICATE_FILE'] -cne '/run/secrets/relay_tls_certificate' -or
        [string]$relayEnvironment['HOSTD_RELAY_TLS_PRIVATE_KEY_FILE'] -cne '/run/secrets/relay_tls_private_key') {
        Add-CheckError $errors 'compose_effective_environment'
    }
    if (-not (Test-ExactStringArray -Value $postgres['tmpfs'] -Expected @(
        '/tmp:rw,noexec,nosuid,nodev,size=32m,mode=1777,uid=999,gid=999',
        '/var/run/postgresql:rw,noexec,nosuid,nodev,size=16m,mode=0700,uid=999,gid=999'
    )) -or @('128m', '134217728') -cnotcontains [string]$postgres['shm_size']) {
        Add-CheckError $errors 'compose_effective_postgres_storage'
    }
    $expectedNetworks = @{
        postgres = @('relay-database')
        relay = @('relay-database', 'relay-edge')
    }
    foreach ($serviceName in @('postgres', 'relay')) {
        $networks = $services[$serviceName]['networks']
        if ($networks -isnot [System.Collections.IDictionary] -or -not (Test-ExactMapKeys -Map $networks -Expected $expectedNetworks[$serviceName])) {
            Add-CheckError $errors 'compose_effective_networks'
        }
        else {
            foreach ($networkName in $expectedNetworks[$serviceName]) {
                $attachment = $networks[$networkName]
                if ($null -ne $attachment -and ($attachment -isnot [System.Collections.IDictionary] -or $attachment.Count -ne 0)) {
                    Add-CheckError $errors 'compose_effective_networks'
                }
            }
        }
    }
    $expectedServiceSecrets = @{
        postgres = @('postgres_password')
        relay = @('relay_postgres_dsn', 'github_client_secret', 'github_app_private_key', 'github_webhook_secret', 'enrollment_key', 'relay_tls_certificate', 'relay_tls_private_key', 'relay_tls_ca')
    }
    foreach ($serviceName in @('postgres', 'relay')) {
        $actual = @($services[$serviceName]['secrets'])
        if ($actual.Count -ne $expectedServiceSecrets[$serviceName].Count) {
            Add-CheckError $errors 'compose_effective_service_secrets'
            continue
        }
        for ($index = 0; $index -lt $actual.Count; $index++) {
            $item = $actual[$index]
            $expected = $expectedServiceSecrets[$serviceName][$index]
            if ($item -isnot [System.Collections.IDictionary] -or -not (Test-ExactMapKeys -Map $item -Expected @('source', 'target')) -or
                [string]$item['source'] -cne $expected -or [string]$item['target'] -cne $expected) {
                Add-CheckError $errors 'compose_effective_service_secrets'
            }
        }
    }
    if ($model['secrets'] -isnot [System.Collections.IDictionary] -or $model['secrets'].Count -ne 9) {
        Add-CheckError $errors 'compose_effective_secret_sources'
    }
    else {
        $secretFiles = @{
            postgres_password = 'postgres-password.txt'; relay_postgres_dsn = 'relay-postgres-dsn.txt'; github_client_secret = 'github-client-secret.txt'
            github_app_private_key = 'github-app-private-key.pem'; github_webhook_secret = 'github-webhook-secret.txt'; enrollment_key = 'enrollment-key.bin'
            relay_tls_certificate = 'relay-tls-certificate.pem'; relay_tls_private_key = 'relay-tls-private-key.pem'; relay_tls_ca = 'relay-tls-ca.pem'
        }
        foreach ($name in $secretFiles.Keys) {
            $definition = $model['secrets'][$name]
            if ($definition -isnot [System.Collections.IDictionary] -or -not (Test-ExactMapKeys -Map $definition -Expected @('file', 'name')) -or
                [string]$definition['name'] -cne "rig-relay_$name" -or [string]$definition['file'] -cne "$ExpectedSecretDirectory/$($secretFiles[$name])") {
                Add-CheckError $errors 'compose_effective_secret_sources'
            }
        }
    }
    if ($model['volumes'] -isnot [System.Collections.IDictionary] -or -not (Test-ExactMapKeys -Map $model['volumes'] -Expected @('relay-postgres-data')) -or
        $model['volumes']['relay-postgres-data'] -isnot [System.Collections.IDictionary] -or -not (Test-ExactMapKeys -Map $model['volumes']['relay-postgres-data'] -Expected @('name')) -or
        [string]$model['volumes']['relay-postgres-data']['name'] -cne 'rig-relay_relay-postgres-data') {
        Add-CheckError $errors 'compose_effective_named_volumes'
    }
    if ($model['networks'] -isnot [System.Collections.IDictionary] -or -not (Test-ExactMapKeys -Map $model['networks'] -Expected @('relay-database', 'relay-edge')) -or
        -not (Test-ExactEffectiveNetworkKeys -Map $model['networks']['relay-database'] -Expected @('internal', 'name')) -or
        -not (Test-ExactEffectiveNetworkKeys -Map $model['networks']['relay-edge'] -Expected @('external', 'name')) -or
        $model['networks']['relay-database']['internal'] -ne $true -or [string]$model['networks']['relay-database']['name'] -cne 'rig-relay_relay-database' -or
        $model['networks']['relay-edge']['external'] -ne $true -or
        [string]$model['networks']['relay-edge']['name'] -cne [string]$ExpectedEnvironment['HOSTD_RELAY_EDGE_NETWORK']) {
        Add-CheckError $errors 'compose_effective_networks'
    }
    $acceptedTopLevelIPAMCount = 0
    if ($model['networks'] -is [System.Collections.IDictionary]) {
        foreach ($networkName in @('relay-database', 'relay-edge')) {
            $definition = $model['networks'][$networkName]
            if ($definition -isnot [System.Collections.IDictionary]) {
                continue
            }
            $exactIPAMKeys = @($definition.Keys | Where-Object { [string]$_ -ceq 'ipam' })
            if ($exactIPAMKeys.Count -eq 1 -and $definition[$exactIPAMKeys[0]] -is [System.Collections.IDictionary] -and
                $definition[$exactIPAMKeys[0]].Count -eq 0) {
                $acceptedTopLevelIPAMCount++
            }
        }
    }
    $rawIPAMTokensAreExact = @($rawIPAMTokens | Where-Object { [string]$_.Literal -cne '"ipam"' }).Count -eq 0
    if (-not $rawIPAMTokensAreExact -or $rawIPAMTokens.Count -ne $acceptedTopLevelIPAMCount -or $acceptedTopLevelIPAMCount -gt 2) {
        Add-CheckError $errors 'compose_effective_networks'
    }
    $depends = $relay['depends_on']
    if ($depends -isnot [System.Collections.IDictionary] -or -not (Test-ExactMapKeys -Map $depends -Expected @('postgres')) -or
        $depends['postgres'] -isnot [System.Collections.IDictionary] -or -not (Test-ExactMapKeys -Map $depends['postgres'] -Expected @('condition', 'required', 'restart')) -or
        [string]$depends['postgres']['condition'] -cne 'service_healthy' -or $depends['postgres']['restart'] -ne $true -or $depends['postgres']['required'] -ne $true) {
        Add-CheckError $errors 'compose_effective_dependency'
    }
    $postgresHealth = $postgres['healthcheck']
    if ($postgresHealth -isnot [System.Collections.IDictionary] -or -not (Test-ExactMapKeys -Map $postgresHealth -Expected @('interval', 'retries', 'start_period', 'test', 'timeout')) -or
        -not (Test-ExactStringArray -Value $postgresHealth['test'] -Expected @('CMD-SHELL', 'pg_isready -q -U rig_relay -d rig_relay')) -or
        [string]$postgresHealth['interval'] -cne '10s' -or [string]$postgresHealth['timeout'] -cne '5s' -or [string]$postgresHealth['retries'] -cne '10' -or [string]$postgresHealth['start_period'] -cne '30s') {
        Add-CheckError $errors 'compose_effective_healthcheck'
    }
    $relayHealth = $relay['healthcheck']
    $relayHealthTest = @(
        'CMD', '/usr/local/bin/rig-relay-probe', '--base-url=https://127.0.0.1:7346',
        "--server-name=$([string]$ExpectedEnvironment['HOSTD_RELAY_TLS_SERVER_NAME'])", '--ca-file=/run/secrets/relay_tls_ca', '--endpoint=ready', '--timeout=5s'
    )
    if ($relayHealth -isnot [System.Collections.IDictionary] -or -not (Test-ExactMapKeys -Map $relayHealth -Expected @('interval', 'retries', 'start_period', 'test', 'timeout')) -or
        -not (Test-ExactStringArray -Value $relayHealth['test'] -Expected $relayHealthTest) -or
        [string]$relayHealth['interval'] -cne '15s' -or [string]$relayHealth['timeout'] -cne '7s' -or [string]$relayHealth['retries'] -cne '4' -or [string]$relayHealth['start_period'] -cne '45s') {
        Add-CheckError $errors 'compose_effective_healthcheck'
    }
    return $errors.ToArray()
}

function Get-SHA256Hex {
    param([byte[]]$Bytes)
    $algorithm = [System.Security.Cryptography.SHA256]::Create()
    try {
        return ([System.BitConverter]::ToString($algorithm.ComputeHash($Bytes))).Replace('-', '').ToLowerInvariant()
    }
    finally {
        $algorithm.Dispose()
    }
}

function Get-DeploymentIdentity {
    param(
        [string]$EnvironmentPath,
        [string]$SecretsPath,
        [System.Collections.IDictionary]$Adapter
    )
    $paths = @($EnvironmentPath, $SecretsPath)
    foreach ($name in @(
        'postgres-password.txt', 'relay-postgres-dsn.txt', 'github-client-secret.txt', 'github-app-private-key.pem',
        'github-webhook-secret.txt', 'enrollment-key.bin', 'relay-tls-certificate.pem', 'relay-tls-private-key.pem', 'relay-tls-ca.pem'
    )) {
        $paths += "$SecretsPath/$name"
    }
    $rows = [System.Collections.Generic.List[string]]::new()
    foreach ($path in $paths) {
        $metadata = Get-LStatMetadata $path $Adapter
        if ($null -eq $metadata) {
            return $null
        }
        $rows.Add("$path|$($metadata.Type)|$($metadata.UID)|$($metadata.GID)|$($metadata.Mode)|$($metadata.Size)|$($metadata.Device)|$($metadata.Inode)|$($metadata.MTime)|$($metadata.CTime)")
    }
    [byte[]]$environmentBytes = $null
    try {
        $environmentBytes = Read-DeploymentBytes $EnvironmentPath $Adapter
        $rows.Add("environment_sha256|$(Get-SHA256Hex $environmentBytes)")
    }
    catch {
        return $null
    }
    finally {
        if ($null -ne $environmentBytes) {
            [Array]::Clear($environmentBytes, 0, $environmentBytes.Length)
        }
    }
    $identityBytes = [System.Text.Encoding]::UTF8.GetBytes(($rows -join "`n"))
    try {
        return Get-SHA256Hex $identityBytes
    }
    finally {
        [Array]::Clear($identityBytes, 0, $identityBytes.Length)
    }
}

function Invoke-EffectiveComposePreflight {
    param(
        [string]$Mode,
        [string]$Anchor,
        [string]$EnvironmentPath,
        [string]$SecretsPath,
        [string]$BaseComposePath,
        [string]$DirectComposePath,
        [System.Collections.IDictionary]$Adapter,
        [string]$ExpectedAnchor = $expectedDeploymentAnchor
    )
    $errors = [System.Collections.Generic.List[string]]::new()
    $dockerCommand = $null
    $usingAdapterCompose = $null -ne $Adapter -and $Adapter.Contains('ComposeVersion') -and $Adapter.Contains('ComposeConfig')
    if ($usingAdapterCompose) {
        $versionOutput = @(& $Adapter['ComposeVersion'])
    }
    else {
        $dockerCommand = Get-Command docker -ErrorAction SilentlyContinue
        if ($null -eq $dockerCommand) {
            Add-CheckError $errors 'docker_unavailable'
            return $errors.ToArray()
        }
        $versionOutput = @(& $dockerCommand.Source compose version --short 2>$null)
    }
    if ((-not $usingAdapterCompose -and $LASTEXITCODE -ne 0) -or $versionOutput.Count -ne 1) {
        Add-CheckError $errors 'docker_compose_unavailable'
        return $errors.ToArray()
    }
    $normalizedVersion = $versionOutput[0].Trim().TrimStart('v') -replace '[-+].*$', ''
    try {
        if ([version]$normalizedVersion -lt $minimumComposeVersion) {
            Add-CheckError $errors 'docker_compose_version'
            return $errors.ToArray()
        }
    }
    catch {
        Add-CheckError $errors 'docker_compose_version'
        return $errors.ToArray()
    }

    # This is deliberately repeated immediately before Compose reads the files.
    # No file contents or Docker stderr are emitted by the preflight.
    $errors.AddRange([string[]]@(Test-ProtectedDeploymentPaths $Anchor $EnvironmentPath $SecretsPath $Adapter $ExpectedAnchor))
    if ($errors.Count -ne 0) {
        return $errors.ToArray()
    }
    $identityBefore = Get-DeploymentIdentity $EnvironmentPath $SecretsPath $Adapter
    if ($null -eq $identityBefore) {
        Add-CheckError $errors 'deployment_identity_unavailable'
        return $errors.ToArray()
    }
    [byte[]]$environmentBytesBefore = $null
    try {
        $environmentBytesBefore = Read-DeploymentBytes $EnvironmentPath $Adapter
        $environmentText = [System.Text.UTF8Encoding]::new($false, $true).GetString($environmentBytesBefore)
        $expectedEnvironment = ConvertFrom-EnvironmentText $environmentText
    }
    catch {
        Add-CheckError $errors 'env_file_unreadable'
        return $errors.ToArray()
    }
    finally {
        if ($null -ne $environmentBytesBefore) {
            [Array]::Clear($environmentBytesBefore, 0, $environmentBytesBefore.Length)
        }
    }
    $arguments = @('compose', '--env-file', $EnvironmentPath, '-f', $BaseComposePath)
    if ($Mode -eq 'direct-tls') {
        $arguments += @('-f', $DirectComposePath)
    }
    $arguments += @('config', '--format', 'json')
    if ($usingAdapterCompose) {
        $composeResult = & $Adapter['ComposeConfig'] $arguments
        $composeExitCode = [int]$composeResult['ExitCode']
        $rendered = @([string]$composeResult['Output'])
    }
    else {
        $rendered = @(& $dockerCommand.Source @arguments 2>$null)
        $composeExitCode = $LASTEXITCODE
    }
    if ($composeExitCode -ne 0 -or $rendered.Count -eq 0) {
        Add-CheckError $errors 'docker_compose_config_failed'
        return $errors.ToArray()
    }
    $json = $rendered -join "`n"
    $errors.AddRange([string[]]@(Test-ProtectedDeploymentPaths $Anchor $EnvironmentPath $SecretsPath $Adapter $ExpectedAnchor))
    $identityAfter = Get-DeploymentIdentity $EnvironmentPath $SecretsPath $Adapter
    if ($errors.Count -ne 0 -or $null -eq $identityAfter) {
        Add-CheckError $errors 'deployment_identity_unavailable'
        return $errors.ToArray()
    }
    if ($identityBefore -cne $identityAfter) {
        Add-CheckError $errors 'deployment_identity_changed'
        return $errors.ToArray()
    }
    $errors.AddRange([string[]]@(Test-EffectiveComposeJson $json -DirectTLS:($Mode -eq 'direct-tls') -ExpectedSecretDirectory $SecretsPath -ExpectedEnvironment $expectedEnvironment))
    return $errors.ToArray()
}

function Assert-MutationRejected {
    param(
        [string]$Name,
        [string[]]$Errors,
        [string]$Expected
    )
    if ($Errors -notcontains $Expected) {
        throw "self-test failed: $Name"
    }
}

function New-ValidEnvironmentText {
    param([string]$SecretsPath = '/etc/rig-relay/secrets')
    return @(
        "HOSTD_RELAY_IMAGE=registry.example.invalid/hostd/rig-relay@sha256:$('a' * 64)",
        'HOSTD_RELAY_PUBLIC_BASE_URL=https://relay.example.com',
        'HOSTD_RELAY_GITHUB_CLIENT_ID=Iv1.test_client',
        'HOSTD_RELAY_GITHUB_APP_ID=123456',
        'HOSTD_RELAY_TLS_SERVER_NAME=relay-backend.example.com',
        'HOSTD_RELAY_EDGE_NETWORK=rig-relay-edge',
        "HOSTD_RELAY_SECRET_DIRECTORY=$SecretsPath",
        'HOSTD_RELAY_PUBLISH_ADDRESS=127.0.0.1',
        'HOSTD_RELAY_PUBLISH_PORT=7346'
    ) -join "`n"
}

function New-EffectiveComposeModel {
    param(
        [System.Collections.IDictionary]$Environment,
        [string]$SecretsPath,
        [switch]$DirectTLS
    )
    $postgresLogging = [ordered]@{ driver = 'local'; options = [ordered]@{ compress = 'true'; 'max-file' = '5'; 'max-size' = '10m' } }
    $relayLogging = [ordered]@{ driver = 'local'; options = [ordered]@{ compress = 'true'; 'max-file' = '5'; 'max-size' = '10m' } }
    $postgres = [ordered]@{
        cap_drop = @('ALL')
        command = $null
        entrypoint = $null
        cpus = 1.5
        environment = [ordered]@{
            POSTGRES_DB = 'rig_relay'; POSTGRES_USER = 'rig_relay'; POSTGRES_PASSWORD_FILE = '/run/secrets/postgres_password'
            POSTGRES_INITDB_ARGS = '--auth-host=scram-sha-256 --data-checksums'
        }
        healthcheck = [ordered]@{ interval = '10s'; retries = 10; start_period = '30s'; test = @('CMD-SHELL', 'pg_isready -q -U rig_relay -d rig_relay'); timeout = '5s' }
        image = $postgresReference
        init = $true
        logging = $postgresLogging
        mem_limit = 1073741824
        networks = [ordered]@{ 'relay-database' = @{} }
        pids_limit = 256
        read_only = $true
        restart = 'unless-stopped'
        secrets = @([ordered]@{ source = 'postgres_password'; target = 'postgres_password' })
        security_opt = @('no-new-privileges:true')
        shm_size = 134217728
        stop_grace_period = '30s'
        tmpfs = @('/tmp:rw,noexec,nosuid,nodev,size=32m,mode=1777,uid=999,gid=999', '/var/run/postgresql:rw,noexec,nosuid,nodev,size=16m,mode=0700,uid=999,gid=999')
        user = '999:999'
        volumes = @([ordered]@{ source = 'relay-postgres-data'; target = '/var/lib/postgresql'; type = 'volume'; volume = @{} })
    }
    $relaySecretNames = @('relay_postgres_dsn', 'github_client_secret', 'github_app_private_key', 'github_webhook_secret', 'enrollment_key', 'relay_tls_certificate', 'relay_tls_private_key', 'relay_tls_ca')
    $relaySecrets = @($relaySecretNames | ForEach-Object { [ordered]@{ source = $_; target = $_ } })
    $relay = [ordered]@{
        cap_drop = @('ALL')
        command = $null
        entrypoint = $null
        cpus = 1.0
        depends_on = [ordered]@{ postgres = [ordered]@{ condition = 'service_healthy'; required = $true; restart = $true } }
        environment = [ordered]@{
            HOSTD_RELAY_LISTEN_ADDRESS = '0.0.0.0:7346'
            HOSTD_RELAY_PUBLIC_BASE_URL = [string]$Environment['HOSTD_RELAY_PUBLIC_BASE_URL']
            HOSTD_RELAY_LOOPBACK_DEVELOPMENT = 'false'
            HOSTD_RELAY_GITHUB_CLIENT_ID = [string]$Environment['HOSTD_RELAY_GITHUB_CLIENT_ID']
            HOSTD_RELAY_GITHUB_APP_ID = [string]$Environment['HOSTD_RELAY_GITHUB_APP_ID']
            HOSTD_RELAY_POSTGRES_DSN_FILE = '/run/secrets/relay_postgres_dsn'
            HOSTD_RELAY_GITHUB_CLIENT_SECRET_FILE = '/run/secrets/github_client_secret'
            HOSTD_RELAY_GITHUB_PRIVATE_KEY_FILE = '/run/secrets/github_app_private_key'
            HOSTD_RELAY_WEBHOOK_SECRET_FILE = '/run/secrets/github_webhook_secret'
            HOSTD_RELAY_ENROLLMENT_KEY_FILE = '/run/secrets/enrollment_key'
            HOSTD_RELAY_TLS_CERTIFICATE_FILE = '/run/secrets/relay_tls_certificate'
            HOSTD_RELAY_TLS_PRIVATE_KEY_FILE = '/run/secrets/relay_tls_private_key'
        }
        healthcheck = [ordered]@{
            interval = '15s'; retries = 4; start_period = '45s'
            test = @('CMD', '/usr/local/bin/rig-relay-probe', '--base-url=https://127.0.0.1:7346', "--server-name=$([string]$Environment['HOSTD_RELAY_TLS_SERVER_NAME'])", '--ca-file=/run/secrets/relay_tls_ca', '--endpoint=ready', '--timeout=5s')
            timeout = '7s'
        }
        image = [string]$Environment['HOSTD_RELAY_IMAGE']
        init = $true
        logging = $relayLogging
        mem_limit = 536870912
        networks = [ordered]@{ 'relay-database' = @{}; 'relay-edge' = @{} }
        pids_limit = 256
        read_only = $true
        restart = 'unless-stopped'
        secrets = $relaySecrets
        security_opt = @('no-new-privileges:true')
        stop_grace_period = '30s'
        user = '65532:65532'
    }
    if ($DirectTLS) {
        $relay['ports'] = @([ordered]@{ host_ip = '127.0.0.1'; mode = 'ingress'; protocol = 'tcp'; published = [string]$Environment['HOSTD_RELAY_PUBLISH_PORT']; target = 7346 })
    }
    $secretFiles = [ordered]@{
        postgres_password = 'postgres-password.txt'; relay_postgres_dsn = 'relay-postgres-dsn.txt'; github_client_secret = 'github-client-secret.txt'
        github_app_private_key = 'github-app-private-key.pem'; github_webhook_secret = 'github-webhook-secret.txt'; enrollment_key = 'enrollment-key.bin'
        relay_tls_certificate = 'relay-tls-certificate.pem'; relay_tls_private_key = 'relay-tls-private-key.pem'; relay_tls_ca = 'relay-tls-ca.pem'
    }
    $secrets = [ordered]@{}
    foreach ($name in $secretFiles.Keys) {
        $secrets[$name] = [ordered]@{ file = "$SecretsPath/$($secretFiles[$name])"; name = "rig-relay_$name" }
    }
    return [ordered]@{
        name = 'rig-relay'
        networks = [ordered]@{
            'relay-database' = [ordered]@{ internal = $true; name = 'rig-relay_relay-database' }
            'relay-edge' = [ordered]@{ external = $true; name = [string]$Environment['HOSTD_RELAY_EDGE_NETWORK'] }
        }
        secrets = $secrets
        services = [ordered]@{ postgres = $postgres; relay = $relay }
        volumes = [ordered]@{ 'relay-postgres-data' = [ordered]@{ name = 'rig-relay_relay-postgres-data' } }
    }
}

function ConvertTo-JSONText {
    param([object]$Model)
    return ConvertTo-Json -InputObject $Model -Depth 20 -Compress
}

function New-FakePreflightContext {
    param([switch]$DirectTLS)
    $anchor = '/etc/rig-relay'
    $environmentPath = "$anchor/relay.env"
    $secretsPath = "$anchor/secrets"
    $environmentText = New-ValidEnvironmentText $secretsPath
    $environmentValues = ConvertFrom-EnvironmentText $environmentText
    $state = [ordered]@{
        Metadata = @{}
        Files = @{}
        Entries = @()
        ComposeJSON = ConvertTo-JSONText (New-EffectiveComposeModel $environmentValues $secretsPath -DirectTLS:$DirectTLS)
        OnCompose = $null
    }
    $newMetadata = {
        param($type, $uid, $gid, $mode, $size, $inode)
        return @{
            Type = $type; UID = [string]$uid; GID = [string]$gid; Mode = [string]$mode; ModeValue = [Convert]::ToInt32([string]$mode, 8)
            Size = [int64]$size; Device = '1'; Inode = [string]$inode; MTime = '100'; CTime = '100'
        }
    }
    $state.Metadata['/'] = & $newMetadata 'directory' 0 0 '755' 4096 1
    $state.Metadata['/etc'] = & $newMetadata 'directory' 0 0 '755' 4096 2
    $state.Metadata[$anchor] = & $newMetadata 'directory' 0 0 '755' 4096 3
    $state.Metadata[$secretsPath] = & $newMetadata 'directory' 0 0 '700' 4096 4
    $state.Files[$environmentPath] = [System.Text.Encoding]::UTF8.GetBytes($environmentText)
    $state.Metadata[$environmentPath] = & $newMetadata 'regular file' 1000 1000 '600' $state.Files[$environmentPath].Length 5
    $secretContent = [ordered]@{
        'postgres-password.txt' = [System.Text.Encoding]::UTF8.GetBytes('postgres-password-value')
        'relay-postgres-dsn.txt' = [System.Text.Encoding]::UTF8.GetBytes('postgresql://rig_relay:password@postgres:5432/rig_relay?sslmode=disable')
        'github-client-secret.txt' = [System.Text.Encoding]::UTF8.GetBytes('github-client-secret-value')
        'github-app-private-key.pem' = [System.Text.Encoding]::UTF8.GetBytes("private-key-fixture`n")
        'github-webhook-secret.txt' = [System.Text.Encoding]::UTF8.GetBytes('github-webhook-secret-value')
        'enrollment-key.bin' = [byte[]](1..32)
        'relay-tls-certificate.pem' = [System.Text.Encoding]::UTF8.GetBytes("certificate-fixture`n")
        'relay-tls-private-key.pem' = [System.Text.Encoding]::UTF8.GetBytes("tls-private-key-fixture`n")
        'relay-tls-ca.pem' = [System.Text.Encoding]::UTF8.GetBytes("ca-fixture`n")
    }
    $inode = 10
    foreach ($name in $secretContent.Keys) {
        $path = "$secretsPath/$name"
        $state.Files[$path] = $secretContent[$name]
        $uid = if ($name -eq 'postgres-password.txt') { '999' } else { '65532' }
        $state.Metadata[$path] = & $newMetadata 'regular file' $uid $uid '400' $secretContent[$name].Length $inode
        $state.Entries += $name
        $inode++
    }
    $adapter = [ordered]@{ Platform = 'linux'; PowerShellMajor = 7; State = $state }
    $adapter['LStat'] = { param($path) if ($state.Metadata.ContainsKey($path)) { return $state.Metadata[$path] }; return $null }.GetNewClosure()
    $adapter['CurrentUID'] = { return '1000' }
    $adapter['ReadAllBytes'] = { param($path) if (-not $state.Files.ContainsKey($path)) { throw 'missing fake file' }; return [byte[]]$state.Files[$path].Clone() }.GetNewClosure()
    $adapter['ListEntries'] = { param($path) if ($path -cne $secretsPath) { throw 'unexpected fake directory' }; return @($state.Entries) }.GetNewClosure()
    $adapter['ComposeVersion'] = { return '2.30.0' }
    $adapter['ComposeConfig'] = {
        param($arguments)
        if ($null -ne $state.OnCompose) {
            & $state.OnCompose $state
        }
        return @{ ExitCode = 0; Output = [string]$state.ComposeJSON }
    }.GetNewClosure()
    return @{
        Anchor = $anchor; EnvironmentPath = $environmentPath; SecretsPath = $secretsPath
        Environment = $environmentValues; State = $state; Adapter = $adapter
    }
}

function Assert-BehaviorErrors {
    param(
        [string]$Name,
        [string[]]$Errors,
        [string]$Expected
    )
    if ($Errors -notcontains $Expected) {
        throw "behavior test failed: $Name (expected $Expected; got $($Errors -join ','))"
    }
}

function Assert-BehaviorSuccess {
    param([string]$Name, [string[]]$Errors)
    if ($Errors.Count -ne 0) {
        throw "behavior test failed: $Name (got $($Errors -join ','))"
    }
}

function Assert-BehaviorOnlyError {
    param(
        [string]$Name,
        [string[]]$Errors,
        [string]$Expected
    )
    if ($Errors.Count -ne 1 -or $Errors[0] -cne $Expected) {
        throw "behavior test failed: $Name (expected only $Expected; got $($Errors -join ','))"
    }
}

function Replace-UniqueJSONFragment {
    param(
        [string]$Name,
        [string]$Json,
        [string]$Source,
        [string]$Destination
    )
    if ([regex]::Matches($Json, [regex]::Escape($Source)).Count -ne 1) {
        throw "behavior test failed: $Name source fragment was not unique"
    }
    $result = $Json.Replace($Source, $Destination)
    if ($result -ceq $Json -or [regex]::Matches($result, [regex]::Escape($Destination)).Count -ne 1) {
        throw "behavior test failed: $Name mutation was not exact"
    }
    return $result
}

function Invoke-PackagingBehaviorTests {
    $context = New-FakePreflightContext
    $errors = @(Invoke-EffectiveComposePreflight 'baseline' $context.Anchor $context.EnvironmentPath $context.SecretsPath 'compose.yaml' 'compose.direct-tls.yaml' $context.Adapter $context.Anchor)
    Assert-BehaviorSuccess 'valid baseline preflight' $errors

    $directContext = New-FakePreflightContext -DirectTLS
    $errors = @(Invoke-EffectiveComposePreflight 'direct-tls' $directContext.Anchor $directContext.EnvironmentPath $directContext.SecretsPath 'compose.yaml' 'compose.direct-tls.yaml' $directContext.Adapter $directContext.Anchor)
    Assert-BehaviorSuccess 'valid direct TLS preflight' $errors

    $testContext = New-FakePreflightContext
    $originalJSON = $testContext.State.ComposeJSON
    $databaseSource = '"internal":true,"name":"rig-relay_relay-database"'
    $databaseDestination = '"internal":true,"name":"rig-relay_relay-database","ipam":{}'
    $edgeSource = '"external":true,"name":"rig-relay-edge"'
    $edgeDestination = '"external":true,"name":"rig-relay-edge","ipam":{}'
    if ([regex]::Matches($originalJSON, [regex]::Escape($databaseSource)).Count -ne 1 -or
        [regex]::Matches($originalJSON, [regex]::Escape($edgeSource)).Count -ne 1) {
        throw 'behavior test failed: normalized empty IPAM fixture source fragments were not unique'
    }
    $testContext.State.ComposeJSON = $originalJSON.Replace($databaseSource, $databaseDestination).Replace($edgeSource, $edgeDestination)
    if ($testContext.State.ComposeJSON -ceq $originalJSON -or
        [regex]::Matches($testContext.State.ComposeJSON, [regex]::Escape($databaseDestination)).Count -ne 1 -or
        [regex]::Matches($testContext.State.ComposeJSON, [regex]::Escape($edgeDestination)).Count -ne 1 -or
        [regex]::Matches($testContext.State.ComposeJSON, [regex]::Escape('"ipam":{}')).Count -ne 2) {
        throw 'behavior test failed: normalized empty IPAM fixture mutation was not exact'
    }
    $errors = @(Invoke-EffectiveComposePreflight 'baseline' $testContext.Anchor $testContext.EnvironmentPath $testContext.SecretsPath 'compose.yaml' 'compose.direct-tls.yaml' $testContext.Adapter $testContext.Anchor)
    Assert-BehaviorSuccess 'valid baseline preflight with normalized empty IPAM' $errors

    foreach ($serviceName in @('postgres', 'relay')) {
        foreach ($nullField in @('command', 'entrypoint')) {
            $fieldPrefix = '"' + $serviceName + '":{"cap_drop":["ALL"],'
            if ($nullField -eq 'entrypoint') {
                $fieldPrefix += '"command":null,'
            }
            $fieldSource = $fieldPrefix + '"' + $nullField + '":null'

            $testContext = New-FakePreflightContext
            $testContext.State.ComposeJSON = Replace-UniqueJSONFragment "remove $serviceName $nullField" $testContext.State.ComposeJSON "$fieldSource," $fieldPrefix
            $errors = @(Test-EffectiveComposeJson $testContext.State.ComposeJSON -ExpectedSecretDirectory $testContext.SecretsPath -ExpectedEnvironment $testContext.Environment)
            Assert-BehaviorOnlyError "reject missing $serviceName $nullField" $errors 'compose_effective_service_keys'

            foreach ($mutation in @(
                @{ Name = 'scalar'; JSON = '"unsafe"' },
                @{ Name = 'empty array'; JSON = '[]' },
                @{ Name = 'object'; JSON = '{}' },
                @{ Name = 'list'; JSON = '["unsafe"]' }
            )) {
                $testContext = New-FakePreflightContext
                $testContext.State.ComposeJSON = Replace-UniqueJSONFragment "set $serviceName $nullField $($mutation.Name)" $testContext.State.ComposeJSON $fieldSource "$fieldPrefix`"$nullField`":$($mutation.JSON)"
                $errors = @(Test-EffectiveComposeJson $testContext.State.ComposeJSON -ExpectedSecretDirectory $testContext.SecretsPath -ExpectedEnvironment $testContext.Environment)
                Assert-BehaviorOnlyError "reject $serviceName $nullField $($mutation.Name)" $errors 'compose_effective_service_keys'
            }

            $caseVariant = $nullField.Substring(0, 1).ToUpperInvariant() + $nullField.Substring(1)
            $testContext = New-FakePreflightContext
            $testContext.State.ComposeJSON = Replace-UniqueJSONFragment "case-variant $serviceName $nullField" $testContext.State.ComposeJSON $fieldSource "$fieldPrefix`"$caseVariant`":null"
            $errors = @(Test-EffectiveComposeJson $testContext.State.ComposeJSON -ExpectedSecretDirectory $testContext.SecretsPath -ExpectedEnvironment $testContext.Environment)
            Assert-BehaviorOnlyError "reject $serviceName case-variant $nullField" $errors 'compose_effective_service_keys'

            $escapedField = if ($nullField -eq 'command') { '\u0063ommand' } else { '\u0065ntrypoint' }
            $testContext = New-FakePreflightContext
            $testContext.State.ComposeJSON = Replace-UniqueJSONFragment "escaped $serviceName $nullField" $testContext.State.ComposeJSON $fieldSource "$fieldPrefix`"$escapedField`":null"
            $errors = @(Test-EffectiveComposeJson $testContext.State.ComposeJSON -ExpectedSecretDirectory $testContext.SecretsPath -ExpectedEnvironment $testContext.Environment)
            Assert-BehaviorOnlyError "reject $serviceName escaped $nullField" $errors 'compose_effective_service_keys'

            foreach ($duplicateMembers in @(
                "`"$nullField`":`"unsafe`",`"$nullField`":null",
                "`"$nullField`":null,`"$nullField`":`"unsafe`""
            )) {
                $testContext = New-FakePreflightContext
                $testContext.State.ComposeJSON = Replace-UniqueJSONFragment "duplicate $serviceName $nullField $duplicateMembers" $testContext.State.ComposeJSON $fieldSource "$fieldPrefix$duplicateMembers"
                $errors = @(Test-EffectiveComposeJson $testContext.State.ComposeJSON -ExpectedSecretDirectory $testContext.SecretsPath -ExpectedEnvironment $testContext.Environment)
                Assert-BehaviorOnlyError "reject $serviceName duplicate $nullField $duplicateMembers" $errors 'compose_effective_service_keys'
            }
        }

        $servicePrefix = '"' + $serviceName + '":{"cap_drop":["ALL"],'
        $testContext = New-FakePreflightContext
        $testContext.State.ComposeJSON = Replace-UniqueJSONFragment "remove both $serviceName null service keys" $testContext.State.ComposeJSON ($servicePrefix + '"command":null,"entrypoint":null,') $servicePrefix
        $errors = @(Test-EffectiveComposeJson $testContext.State.ComposeJSON -ExpectedSecretDirectory $testContext.SecretsPath -ExpectedEnvironment $testContext.Environment)
        Assert-BehaviorOnlyError "reject missing both $serviceName null service keys" $errors 'compose_effective_service_keys'
    }

    foreach ($serviceName in @('postgres', 'relay')) {
        foreach ($key in @('extra_hosts', 'dns', 'dns_search', 'links', 'external_links', 'hostname', 'domainname', 'network_mode', 'pid', 'ipc', 'cgroup', 'runtime', 'devices', 'device_cgroup_rules', 'privileged', 'cap_add', 'build', 'pull_policy', 'configs', 'env_file', 'credential_spec', 'extends', 'profiles', 'unknown_effective_key')) {
            $testContext = New-FakePreflightContext
            $model = ConvertTo-HashtableModel (ConvertFrom-Json $testContext.State.ComposeJSON)
            $model['services'][$serviceName][$key] = if ($key -in @('privileged')) { $true } else { @('unsafe') }
            $testContext.State.ComposeJSON = ConvertTo-JSONText $model
            $errors = @(Test-EffectiveComposeJson $testContext.State.ComposeJSON -ExpectedSecretDirectory $testContext.SecretsPath -ExpectedEnvironment $testContext.Environment)
            Assert-BehaviorErrors "reject $serviceName service key $key" $errors 'compose_effective_service_keys'
        }
    }

    $testContext = New-FakePreflightContext
    $model = ConvertTo-HashtableModel (ConvertFrom-Json $testContext.State.ComposeJSON)
    $model['configs'] = @{}
    $errors = @(Test-EffectiveComposeJson (ConvertTo-JSONText $model) -ExpectedSecretDirectory $testContext.SecretsPath -ExpectedEnvironment $testContext.Environment)
    Assert-BehaviorErrors 'reject unknown top-level config' $errors 'compose_effective_model_invalid'

    $testContext = New-FakePreflightContext
    $model = ConvertTo-HashtableModel (ConvertFrom-Json $testContext.State.ComposeJSON)
    $model['services']['relay']['networks']['relay-edge'] = @{ aliases = @('api.github.com') }
    $errors = @(Test-EffectiveComposeJson (ConvertTo-JSONText $model) -ExpectedSecretDirectory $testContext.SecretsPath -ExpectedEnvironment $testContext.Environment)
    Assert-BehaviorErrors 'reject network alias rebind' $errors 'compose_effective_networks'

    $testContext = New-FakePreflightContext
    $testContext.State.ComposeJSON = $testContext.State.ComposeJSON.Replace('"relay-edge":{}', '"relay-edge":{"ipam":{}}')
    $errors = @(Test-EffectiveComposeJson $testContext.State.ComposeJSON -ExpectedSecretDirectory $testContext.SecretsPath -ExpectedEnvironment $testContext.Environment)
    Assert-BehaviorOnlyError 'reject service network attachment IPAM' $errors 'compose_effective_networks'

    foreach ($networkName in @('relay-database', 'relay-edge')) {
        foreach ($mutation in @(
            @{ Name = 'configured IPAM'; Value = @{ config = @(@{ subnet = '198.51.100.0/24' }) } },
            @{ Name = 'IPAM driver'; Value = @{ driver = 'default' } },
            @{ Name = 'unknown IPAM field'; Value = @{ unreviewed = @{} } },
            @{ Name = 'scalar IPAM'; Value = 'default' },
            @{ Name = 'array IPAM'; Value = @() },
            @{ Name = 'null IPAM'; Value = $null }
        )) {
            $testContext = New-FakePreflightContext
            $networkDefinition = if ($networkName -eq 'relay-database') {
                '"internal":true,"name":"rig-relay_relay-database"'
            }
            else {
                '"external":true,"name":"rig-relay-edge"'
            }
            $ipamJson = if ($null -eq $mutation.Value) { 'null' } else { ConvertTo-JSONText $mutation.Value }
            $testContext.State.ComposeJSON = $testContext.State.ComposeJSON.Replace($networkDefinition, "$networkDefinition,`"ipam`":$ipamJson")
            $errors = @(Test-EffectiveComposeJson $testContext.State.ComposeJSON -ExpectedSecretDirectory $testContext.SecretsPath -ExpectedEnvironment $testContext.Environment)
            Assert-BehaviorOnlyError "reject $networkName $($mutation.Name)" $errors 'compose_effective_networks'
        }

        foreach ($ipamKey in @('IPAM', 'Ipam')) {
            $testContext = New-FakePreflightContext
            $networkDefinition = if ($networkName -eq 'relay-database') {
                '"internal":true,"name":"rig-relay_relay-database"'
            }
            else {
                '"external":true,"name":"rig-relay-edge"'
            }
            $testContext.State.ComposeJSON = $testContext.State.ComposeJSON.Replace($networkDefinition, "$networkDefinition,`"$ipamKey`":{}")
            $errors = @(Test-EffectiveComposeJson $testContext.State.ComposeJSON -ExpectedSecretDirectory $testContext.SecretsPath -ExpectedEnvironment $testContext.Environment)
            Assert-BehaviorOnlyError "reject $networkName case-variant $ipamKey" $errors 'compose_effective_networks'
        }

        $testContext = New-FakePreflightContext
        $networkDefinition = if ($networkName -eq 'relay-database') {
            '"internal":true,"name":"rig-relay_relay-database"'
        }
        else {
            '"external":true,"name":"rig-relay-edge"'
        }
        $testContext.State.ComposeJSON = $testContext.State.ComposeJSON.Replace($networkDefinition, "$networkDefinition,`"ip\u0061m`":{}")
        $errors = @(Test-EffectiveComposeJson $testContext.State.ComposeJSON -ExpectedSecretDirectory $testContext.SecretsPath -ExpectedEnvironment $testContext.Environment)
        Assert-BehaviorOnlyError "reject $networkName escaped IPAM key" $errors 'compose_effective_networks'

        foreach ($duplicateMembers in @(
            '"ipam":{},"ipam":{"driver":"default"}',
            '"ipam":{"driver":"default"},"ipam":{}'
        )) {
            $testContext = New-FakePreflightContext
            $networkDefinition = if ($networkName -eq 'relay-database') {
                '"internal":true,"name":"rig-relay_relay-database"'
            }
            else {
                '"external":true,"name":"rig-relay-edge"'
            }
            $testContext.State.ComposeJSON = $testContext.State.ComposeJSON.Replace($networkDefinition, "$networkDefinition,$duplicateMembers")
            $errors = @(Test-EffectiveComposeJson $testContext.State.ComposeJSON -ExpectedSecretDirectory $testContext.SecretsPath -ExpectedEnvironment $testContext.Environment)
            Assert-BehaviorOnlyError "reject $networkName duplicate IPAM members $duplicateMembers" $errors 'compose_effective_networks'
        }

        foreach ($mutation in @(
            @{ Name = 'single-quoted IPAM property'; Members = '''ipam'':{}' },
            @{ Name = 'unquoted IPAM property'; Members = 'ipam:{}' }
        )) {
            $testContext = New-FakePreflightContext
            $networkDefinition = if ($networkName -eq 'relay-database') {
                '"internal":true,"name":"rig-relay_relay-database"'
            }
            else {
                '"external":true,"name":"rig-relay-edge"'
            }
            $testContext.State.ComposeJSON = $testContext.State.ComposeJSON.Replace($networkDefinition, "$networkDefinition,$($mutation.Members)")
            $errors = @(Test-EffectiveComposeJson $testContext.State.ComposeJSON -ExpectedSecretDirectory $testContext.SecretsPath -ExpectedEnvironment $testContext.Environment)
            Assert-BehaviorOnlyError "reject $networkName $($mutation.Name)" $errors 'compose_effective_json_invalid'
        }

        $testContext = New-FakePreflightContext
        $networkDefinition = if ($networkName -eq 'relay-database') {
            '"internal":true,"name":"rig-relay_relay-database"'
        }
        else {
            '"external":true,"name":"rig-relay-edge"'
        }
        $testContext.State.ComposeJSON = $testContext.State.ComposeJSON.Replace($networkDefinition, "$networkDefinition,`"attachable`":true")
        $errors = @(Test-EffectiveComposeJson $testContext.State.ComposeJSON -ExpectedSecretDirectory $testContext.SecretsPath -ExpectedEnvironment $testContext.Environment)
        Assert-BehaviorOnlyError "reject $networkName extra network key" $errors 'compose_effective_networks'

        $testContext = New-FakePreflightContext
        $testContext.State.ComposeJSON = $testContext.State.ComposeJSON.Replace($networkDefinition, "$networkDefinition,`"ipam`":{},`"attachable`":true")
        $errors = @(Test-EffectiveComposeJson $testContext.State.ComposeJSON -ExpectedSecretDirectory $testContext.SecretsPath -ExpectedEnvironment $testContext.Environment)
        Assert-BehaviorOnlyError "reject $networkName empty IPAM with extra network key" $errors 'compose_effective_networks'
    }

    $testContext = New-FakePreflightContext
    $testContext.State.ComposeJSON = "/* comment */$($testContext.State.ComposeJSON)"
    $errors = @(Test-EffectiveComposeJson $testContext.State.ComposeJSON -ExpectedSecretDirectory $testContext.SecretsPath -ExpectedEnvironment $testContext.Environment)
    Assert-BehaviorOnlyError 'reject JSON comment' $errors 'compose_effective_json_invalid'

    $testContext = New-FakePreflightContext
    $testContext.State.ComposeJSON = $testContext.State.ComposeJSON.Substring(0, $testContext.State.ComposeJSON.Length - 1) + ',}'
    $errors = @(Test-EffectiveComposeJson $testContext.State.ComposeJSON -ExpectedSecretDirectory $testContext.SecretsPath -ExpectedEnvironment $testContext.Environment)
    Assert-BehaviorOnlyError 'reject trailing JSON comma' $errors 'compose_effective_json_invalid'

    foreach ($serviceName in @('postgres', 'relay')) {
        $testContext = New-FakePreflightContext
        $model = ConvertTo-HashtableModel (ConvertFrom-Json $testContext.State.ComposeJSON)
        $model['services'][$serviceName]['extra_hosts'] = @('api.github.com=127.0.0.1')
        $testContext.State.ComposeJSON = ConvertTo-JSONText $model
        $errors = @(Invoke-EffectiveComposePreflight 'baseline' $testContext.Anchor $testContext.EnvironmentPath $testContext.SecretsPath 'compose.yaml' 'compose.direct-tls.yaml' $testContext.Adapter $testContext.Anchor)
        Assert-BehaviorErrors "reject rendered $serviceName extra_hosts" $errors 'compose_effective_service_keys'
    }

    foreach ($mutation in @(
        @{ Name = 'ancestor symlink'; Path = '/etc/rig-relay'; Field = 'Type'; Value = 'symbolic link'; Code = 'env_file_ancestor_type' },
        @{ Name = 'environment symlink'; Path = '/etc/rig-relay/relay.env'; Field = 'Type'; Value = 'symbolic link'; Code = 'env_file_nonregular' },
        @{ Name = 'environment wrong owner'; Path = '/etc/rig-relay/relay.env'; Field = 'UID'; Value = '2000'; Code = 'env_file_owner' },
        @{ Name = 'environment wrong mode'; Path = '/etc/rig-relay/relay.env'; Field = 'Mode'; Value = '644'; Code = 'env_file_mode' },
        @{ Name = 'ancestor wrong owner'; Path = '/etc/rig-relay'; Field = 'UID'; Value = '2000'; Code = 'env_file_ancestor_owner' },
        @{ Name = 'secret directory symlink'; Path = '/etc/rig-relay/secrets'; Field = 'Type'; Value = 'symbolic link'; Code = 'secret_directory_ancestor_type' },
        @{ Name = 'secret directory wrong owner'; Path = '/etc/rig-relay/secrets'; Field = 'UID'; Value = '1000'; Code = 'secret_directory_owner' },
        @{ Name = 'secret directory wrong mode'; Path = '/etc/rig-relay/secrets'; Field = 'Mode'; Value = '755'; Code = 'secret_directory_mode' },
        @{ Name = 'secret file symlink'; Path = '/etc/rig-relay/secrets/github-client-secret.txt'; Field = 'Type'; Value = 'symbolic link'; Code = 'secret_file_nonregular' },
        @{ Name = 'secret file wrong owner'; Path = '/etc/rig-relay/secrets/github-client-secret.txt'; Field = 'UID'; Value = '0'; Code = 'secret_file_owner' },
        @{ Name = 'secret file wrong mode'; Path = '/etc/rig-relay/secrets/github-client-secret.txt'; Field = 'Mode'; Value = '644'; Code = 'secret_file_mode' },
        @{ Name = 'writable ancestor'; Path = '/etc'; Field = 'Mode'; Value = '777'; Code = 'env_file_ancestor_writable' }
    )) {
        $testContext = New-FakePreflightContext
        $metadata = $testContext.State.Metadata[$mutation.Path]
        $metadata[$mutation.Field] = $mutation.Value
        if ($mutation.Field -eq 'Mode') {
            $metadata['ModeValue'] = [Convert]::ToInt32([string]$mutation.Value, 8)
        }
        $errors = @(Test-ProtectedDeploymentPaths $testContext.Anchor $testContext.EnvironmentPath $testContext.SecretsPath $testContext.Adapter $testContext.Anchor)
        Assert-BehaviorErrors $mutation.Name $errors $mutation.Code
    }

    $testContext = New-FakePreflightContext
    $errors = @(Test-ProtectedDeploymentPaths $testContext.Anchor $testContext.EnvironmentPath '/etc/rig-relay/other-secrets' $testContext.Adapter $testContext.Anchor)
    Assert-BehaviorErrors 'reject path coupling drift' $errors 'deployment_path_coupling'

    $testContext = New-FakePreflightContext
    $testContext.State.OnCompose = { param($state) $state.Metadata['/etc/rig-relay/relay.env']['Inode'] = '9999' }
    $errors = @(Invoke-EffectiveComposePreflight 'baseline' $testContext.Anchor $testContext.EnvironmentPath $testContext.SecretsPath 'compose.yaml' 'compose.direct-tls.yaml' $testContext.Adapter $testContext.Anchor)
    Assert-BehaviorErrors 'reject identity drift during Compose' $errors 'deployment_identity_changed'

    $validEnvironment = New-ValidEnvironmentText
    foreach ($mutation in @(
        @{ Name = 'URL userinfo'; Old = 'https://relay.example.com'; New = 'https://user@relay.example.com'; Code = 'env_public_url' },
        @{ Name = 'URL query'; Old = 'https://relay.example.com'; New = 'https://relay.example.com?x=1'; Code = 'env_public_url' },
        @{ Name = 'URL fragment'; Old = 'https://relay.example.com'; New = 'https://relay.example.com/#x'; Code = 'env_public_url' },
        @{ Name = 'URL nonroot path'; Old = 'https://relay.example.com'; New = 'https://relay.example.com/path'; Code = 'env_public_url' },
        @{ Name = 'URL opaque form'; Old = 'https://relay.example.com'; New = 'https:relay.example.com'; Code = 'env_public_url' },
        @{ Name = 'client identifier'; Old = 'Iv1.test_client'; New = 'client value'; Code = 'env_github_client_id' },
        @{ Name = 'App ID overflow'; Old = '123456'; New = '9223372036854775808'; Code = 'env_github_app_id' },
        @{ Name = 'SNI wildcard'; Old = 'relay-backend.example.com'; New = '*.example.com'; Code = 'env_tls_sni' },
        @{ Name = 'SNI IP'; Old = 'relay-backend.example.com'; New = '127.0.0.1'; Code = 'env_tls_sni' },
        @{ Name = 'SNI trailing dot'; Old = 'relay-backend.example.com'; New = 'relay.example.com.'; Code = 'env_tls_sni' },
        @{ Name = 'publish port overflow'; Old = '7346'; New = '65536'; Code = 'env_publish_port' },
        @{ Name = 'edge network injection'; Old = 'rig-relay-edge'; New = 'edge/network'; Code = 'env_edge_network' }
    )) {
        $errors = @(Test-EnvironmentText ($validEnvironment.Replace($mutation.Old, $mutation.New)))
        Assert-BehaviorErrors $mutation.Name $errors $mutation.Code
    }
    Assert-BehaviorSuccess 'valid environment semantics' @(Test-EnvironmentText $validEnvironment)
}

function Invoke-LinuxFixtureCommand {
    param(
        [string]$Command,
        [string[]]$ArgumentList
    )
    & $Command @ArgumentList *> $null
    if ($LASTEXITCODE -ne 0) {
        throw "Linux lstat integration fixture command failed: $Command"
    }
}

function Invoke-LinuxLStatIntegrationTests {
    if ($env:HOSTD_RELAY_RUN_LINUX_PREFLIGHT_TESTS -cne '1') {
        Write-Output 'relay packaging Linux lstat integration test skipped: set HOSTD_RELAY_RUN_LINUX_PREFLIGHT_TESTS=1 on an isolated Linux root test host'
        return
    }
    if ([System.Environment]::OSVersion.Platform -eq [System.PlatformID]::Win32NT) {
        throw 'Linux lstat integration test requested on a non-Linux host'
    }
    if ($PSVersionTable.PSVersion.Major -lt 7) {
        throw 'Linux lstat integration test requires PowerShell 7'
    }
    foreach ($command in @('/usr/bin/stat', '/usr/bin/id', '/usr/bin/chown', '/usr/bin/chmod', '/usr/bin/ln', '/usr/bin/unlink')) {
        if (-not (Test-Path -LiteralPath $command -PathType Leaf)) {
            throw "Linux lstat integration test requires $command"
        }
    }
    $currentUID = @(& /usr/bin/id '-u' 2>$null)
    if ($LASTEXITCODE -ne 0 -or $currentUID.Count -ne 1 -or $currentUID[0] -cne '0') {
        throw 'Linux lstat integration test requires an isolated root test host'
    }

    $suffixBytes = [byte[]]::new(8)
    [System.Security.Cryptography.RandomNumberGenerator]::Fill($suffixBytes)
    $suffix = ([System.BitConverter]::ToString($suffixBytes)).Replace('-', '').ToLowerInvariant()
    $anchor = "/var/lib/rig-relay-preflight-test-$PID-$suffix"
    $linkAnchor = "/var/lib/rig-relay-preflight-link-$PID-$suffix"
    if ($anchor -cnotmatch '^/var/lib/rig-relay-preflight-test-[0-9]+-[0-9a-f]{16}$' -or $linkAnchor -cnotmatch '^/var/lib/rig-relay-preflight-link-[0-9]+-[0-9a-f]{16}$') {
        throw 'Linux lstat integration fixture path validation failed'
    }
    $environmentPath = "$anchor/relay.env"
    $secretsPath = "$anchor/secrets"
    $secretOwners = [ordered]@{
        'postgres-password.txt' = '999:999'; 'relay-postgres-dsn.txt' = '65532:65532'; 'github-client-secret.txt' = '65532:65532'
        'github-app-private-key.pem' = '65532:65532'; 'github-webhook-secret.txt' = '65532:65532'; 'enrollment-key.bin' = '65532:65532'
        'relay-tls-certificate.pem' = '65532:65532'; 'relay-tls-private-key.pem' = '65532:65532'; 'relay-tls-ca.pem' = '65532:65532'
    }
    $secretBytes = [ordered]@{
        'postgres-password.txt' = [System.Text.Encoding]::UTF8.GetBytes('fixture-postgres-password')
        'relay-postgres-dsn.txt' = [System.Text.Encoding]::UTF8.GetBytes('postgresql://rig_relay:fixture@postgres:5432/rig_relay?sslmode=disable')
        'github-client-secret.txt' = [System.Text.Encoding]::UTF8.GetBytes('fixture-client-secret')
        'github-app-private-key.pem' = [System.Text.Encoding]::UTF8.GetBytes("fixture-private-key`n")
        'github-webhook-secret.txt' = [System.Text.Encoding]::UTF8.GetBytes('fixture-webhook-secret')
        'enrollment-key.bin' = [byte[]](1..32)
        'relay-tls-certificate.pem' = [System.Text.Encoding]::UTF8.GetBytes("fixture-certificate`n")
        'relay-tls-private-key.pem' = [System.Text.Encoding]::UTF8.GetBytes("fixture-tls-private-key`n")
        'relay-tls-ca.pem' = [System.Text.Encoding]::UTF8.GetBytes("fixture-ca`n")
    }
    $environmentOriginal = "$environmentPath.original"
    $secretsOriginal = "$secretsPath.original"
    $secretPath = "$secretsPath/github-client-secret.txt"
    $secretOriginal = "$secretPath.original"
    try {
        [System.IO.Directory]::CreateDirectory($secretsPath) | Out-Null
        [System.IO.File]::WriteAllBytes($environmentPath, [System.Text.Encoding]::UTF8.GetBytes((New-ValidEnvironmentText $secretsPath)))
        foreach ($name in $secretBytes.Keys) {
            [System.IO.File]::WriteAllBytes("$secretsPath/$name", $secretBytes[$name])
        }
        Invoke-LinuxFixtureCommand '/usr/bin/chown' @('root:root', $anchor, $environmentPath, $secretsPath)
        Invoke-LinuxFixtureCommand '/usr/bin/chmod' @('0755', $anchor)
        Invoke-LinuxFixtureCommand '/usr/bin/chmod' @('0600', $environmentPath)
        Invoke-LinuxFixtureCommand '/usr/bin/chmod' @('0700', $secretsPath)
        foreach ($name in $secretOwners.Keys) {
            Invoke-LinuxFixtureCommand '/usr/bin/chown' @($secretOwners[$name], "$secretsPath/$name")
            Invoke-LinuxFixtureCommand '/usr/bin/chmod' @('0400', "$secretsPath/$name")
        }

        Assert-BehaviorSuccess 'Linux valid protected hierarchy' @(Test-ProtectedDeploymentPaths $anchor $environmentPath $secretsPath $null $anchor)

        [System.IO.File]::Move($environmentPath, $environmentOriginal)
        try {
            Invoke-LinuxFixtureCommand '/usr/bin/ln' @('-s', $environmentOriginal, $environmentPath)
            Assert-BehaviorErrors 'Linux environment symlink substitution' @(Test-ProtectedDeploymentPaths $anchor $environmentPath $secretsPath $null $anchor) 'env_file_nonregular'
        }
        finally {
            if ($null -ne (Get-LStatMetadata $environmentPath $null)) { Invoke-LinuxFixtureCommand '/usr/bin/unlink' @($environmentPath) }
            [System.IO.File]::Move($environmentOriginal, $environmentPath)
        }

        [System.IO.Directory]::Move($secretsPath, $secretsOriginal)
        try {
            Invoke-LinuxFixtureCommand '/usr/bin/ln' @('-s', $secretsOriginal, $secretsPath)
            Assert-BehaviorErrors 'Linux secret directory symlink substitution' @(Test-ProtectedDeploymentPaths $anchor $environmentPath $secretsPath $null $anchor) 'secret_directory_ancestor_type'
        }
        finally {
            if ($null -ne (Get-LStatMetadata $secretsPath $null)) { Invoke-LinuxFixtureCommand '/usr/bin/unlink' @($secretsPath) }
            [System.IO.Directory]::Move($secretsOriginal, $secretsPath)
        }

        [System.IO.File]::Move($secretPath, $secretOriginal)
        try {
            Invoke-LinuxFixtureCommand '/usr/bin/ln' @('-s', $secretOriginal, $secretPath)
            Assert-BehaviorErrors 'Linux secret file symlink substitution' @(Test-ProtectedDeploymentPaths $anchor $environmentPath $secretsPath $null $anchor) 'secret_file_nonregular'
        }
        finally {
            if ($null -ne (Get-LStatMetadata $secretPath $null)) { Invoke-LinuxFixtureCommand '/usr/bin/unlink' @($secretPath) }
            [System.IO.File]::Move($secretOriginal, $secretPath)
        }

        Invoke-LinuxFixtureCommand '/usr/bin/chown' @('12345:0', $environmentPath)
        Assert-BehaviorErrors 'Linux environment wrong owner' @(Test-ProtectedDeploymentPaths $anchor $environmentPath $secretsPath $null $anchor) 'env_file_owner'
        Invoke-LinuxFixtureCommand '/usr/bin/chown' @('root:root', $environmentPath)
        Invoke-LinuxFixtureCommand '/usr/bin/chmod' @('0644', $environmentPath)
        Assert-BehaviorErrors 'Linux environment wrong mode' @(Test-ProtectedDeploymentPaths $anchor $environmentPath $secretsPath $null $anchor) 'env_file_mode'
        Invoke-LinuxFixtureCommand '/usr/bin/chmod' @('0600', $environmentPath)

        Invoke-LinuxFixtureCommand '/usr/bin/chown' @('12345:0', $anchor)
        Assert-BehaviorErrors 'Linux ancestor wrong owner' @(Test-ProtectedDeploymentPaths $anchor $environmentPath $secretsPath $null $anchor) 'env_file_ancestor_owner'
        Invoke-LinuxFixtureCommand '/usr/bin/chown' @('root:root', $anchor)
        Invoke-LinuxFixtureCommand '/usr/bin/chmod' @('0777', $anchor)
        Assert-BehaviorErrors 'Linux writable ancestor' @(Test-ProtectedDeploymentPaths $anchor $environmentPath $secretsPath $null $anchor) 'env_file_ancestor_writable'
        Invoke-LinuxFixtureCommand '/usr/bin/chmod' @('0755', $anchor)

        Invoke-LinuxFixtureCommand '/usr/bin/ln' @('-s', $anchor, $linkAnchor)
        Assert-BehaviorErrors 'Linux symlink ancestor' @(Test-ProtectedDeploymentPaths $linkAnchor "$linkAnchor/relay.env" "$linkAnchor/secrets" $null $linkAnchor) 'env_file_ancestor_type'
        Invoke-LinuxFixtureCommand '/usr/bin/unlink' @($linkAnchor)
    }
    finally {
        if ($null -ne (Get-LStatMetadata $linkAnchor $null)) {
            Invoke-LinuxFixtureCommand '/usr/bin/unlink' @($linkAnchor)
        }
        if ($anchor -cmatch '^/var/lib/rig-relay-preflight-test-[0-9]+-[0-9a-f]{16}$' -and (Test-Path -LiteralPath $anchor)) {
            Remove-Item -LiteralPath $anchor -Recurse -Force
        }
        foreach ($bytes in $secretBytes.Values) {
            [Array]::Clear($bytes, 0, $bytes.Length)
        }
        [Array]::Clear($suffixBytes, 0, $suffixBytes.Length)
    }
    Write-Output 'relay packaging Linux lstat integration test ok'
}

$repositoryRoot = [System.IO.Path]::GetFullPath((Join-Path $PSScriptRoot '..'))
$dockerfilePath = Join-Path $repositoryRoot 'deploy/relay/Dockerfile'
$dockerignorePath = Join-Path $repositoryRoot 'deploy/relay/Dockerfile.dockerignore'
$composePath = Join-Path $repositoryRoot 'deploy/relay/compose.yaml'
$directPath = Join-Path $repositoryRoot 'deploy/relay/compose.direct-tls.yaml'
$examplePath = Join-Path $repositoryRoot 'deploy/relay/.env.example'
$ignorePath = Join-Path $repositoryRoot 'deploy/relay/secrets/.gitignore'
$documentationPath = Join-Path $repositoryRoot 'docs/relay-operations.md'

$requiredFiles = @($dockerfilePath, $dockerignorePath, $composePath, $directPath, $examplePath, $ignorePath, $documentationPath)
foreach ($file in $requiredFiles) {
    if (-not (Test-Path -LiteralPath $file -PathType Leaf)) {
        Write-Error 'relay packaging check failed: required_file_missing'
        exit 1
    }
}

$dockerfile = Get-Content -Raw -LiteralPath $dockerfilePath
$dockerignore = Get-Content -Raw -LiteralPath $dockerignorePath
$compose = Get-Content -Raw -LiteralPath $composePath
$direct = Get-Content -Raw -LiteralPath $directPath
$example = Get-Content -Raw -LiteralPath $examplePath
$ignore = Get-Content -Raw -LiteralPath $ignorePath
$documentation = Get-Content -Raw -LiteralPath $documentationPath

$allErrors = @()
$allErrors += @(Test-DockerfileText $dockerfile)
$allErrors += @(Test-DockerignoreText $dockerignore)
$allErrors += @(Test-EnvironmentText $example -Example)
$allErrors += @(Test-SecretsIgnoreText $ignore)
$allErrors += @(Test-DocumentationText $documentation)

$deploymentInputs = @(@($EnvironmentFile, $SecretDirectory, $TrustedDeploymentAnchor) | Where-Object { $_ -ne '' })
if ($deploymentInputs.Count -ne 0 -and $deploymentInputs.Count -ne 3) {
    $allErrors += 'deployment_preflight_requires_anchor_env_and_secrets'
}
if ($DeploymentMode -ne '' -and $deploymentInputs.Count -ne 3) {
    $allErrors += 'deployment_mode_requires_protected_paths'
}
if ($deploymentInputs.Count -eq 3) {
    $allErrors += @(Test-ProtectedDeploymentPaths $TrustedDeploymentAnchor $EnvironmentFile $SecretDirectory)
}
if ($DeploymentMode -ne '' -and $allErrors.Count -eq 0) {
    $allErrors += @(Invoke-EffectiveComposePreflight $DeploymentMode $TrustedDeploymentAnchor $EnvironmentFile $SecretDirectory $composePath $directPath)
}

if ($SelfTest) {
    Assert-MutationRejected 'unpinned Dockerfile frontend' @(Test-DockerfileText ($dockerfile.Replace($frontendReference, 'docker/dockerfile:1.7'))) 'docker_frontend_reference'
    Assert-MutationRejected 'dynamic builder tag' @(Test-DockerfileText ($dockerfile.Replace($builderReference, 'docker.io/library/golang:latest'))) 'docker_from_pin'
    Assert-MutationRejected 'build context expansion' @(Test-DockerignoreText ($dockerignore + "`n!deploy/")) 'context_allowlist'
    Assert-MutationRejected 'mutable relay deployment image' @(Test-EnvironmentText ($example.Replace('registry.example.invalid/hostd/rig-relay@sha256:REPLACE_WITH_64_LOWERCASE_HEX_CHARACTERS', 'registry.example.invalid/hostd/rig-relay:latest'))) 'env_relay_image_pin'
    Assert-MutationRejected 'secret deployment environment' @(Test-EnvironmentText ($example + "`nHOSTD_RELAY_WEBHOOK_SECRET=literal")) 'env_contains_secret_key'
    Assert-MutationRejected 'unknown deployment environment key' @(Test-EnvironmentText ($example + "`nHOSTD_RELAY_UNREVIEWED=true")) 'env_unknown_key'
    Assert-MutationRejected 'public deployment bind' @(Test-EnvironmentText ($example.Replace('HOSTD_RELAY_PUBLISH_ADDRESS=127.0.0.1', 'HOSTD_RELAY_PUBLISH_ADDRESS=0.0.0.0'))) 'env_public_default'
    Assert-MutationRejected 'documentation drift' @(Test-DocumentationText ($documentation.Replace('scripts/check-relay-packaging.ps1 -SelfTest', 'scripts/check-relay-packaging.ps1 -Changed'))) 'docs_required_operations'
}

if ($BehaviorTest) {
    Invoke-PackagingBehaviorTests
}
if ($LinuxIntegrationTest) {
    Invoke-LinuxLStatIntegrationTests
}

$allErrors = @($allErrors | Sort-Object -Unique)
if ($allErrors.Count -ne 0) {
    Write-Error "relay packaging check failed: $($allErrors -join ', ')"
    exit 1
}

if ($SelfTest) {
    Write-Output 'relay packaging self-test ok'
}
if ($BehaviorTest) {
    Write-Output 'relay packaging behavior test ok'
}
Write-Output 'relay packaging check ok'
