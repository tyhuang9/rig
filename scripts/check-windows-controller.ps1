$ErrorActionPreference = 'Stop'
$root = Split-Path -Parent $PSScriptRoot

if ([System.Environment]::OSVersion.Platform -ne [System.PlatformID]::Win32NT) {
  throw 'Windows controller checks must execute on Windows.'
}

$goos = (& go env GOOS).Trim()
if ($LASTEXITCODE -ne 0 -or $goos -ne 'windows') {
  throw "Windows controller checks require GOOS=windows; got '$goos'."
}

$checks = @(
  @{
    Package = './cmd/hostctl'
    Pattern = '^(TestWindowsSessionFileUsesSharedCurrentUserDPAPI|TestWindowsSessionFileRejectsCorruptOrPlaintextData)$'
    Tests = @(
      'TestWindowsSessionFileUsesSharedCurrentUserDPAPI',
      'TestWindowsSessionFileRejectsCorruptOrPlaintextData'
    )
  },
  @{
    Package = './internal/secretfile'
    Pattern = '^(TestWindowsSecretFileUsesCurrentUserDPAPI|TestWindowsSecretFileRejectsPlaintextAndCorruption)$'
    Tests = @(
      'TestWindowsSecretFileUsesCurrentUserDPAPI',
      'TestWindowsSecretFileRejectsPlaintextAndCorruption'
    )
  },
  @{
    Package = './internal/runtime/securetemp'
    Pattern = '^(TestProtectedFilesClearInputAndCleanupExactly|TestRecoverRemovesOnlyExactOwnedOperations|TestNewRejectsWindowsNamespaceBeforeFilesystemAccess)$'
    Tests = @(
      'TestProtectedFilesClearInputAndCleanupExactly',
      'TestRecoverRemovesOnlyExactOwnedOperations',
      'TestNewRejectsWindowsNamespaceBeforeFilesystemAccess'
    )
  },
  @{
    Package = './internal/runtime/process'
    Pattern = '^TestExecRunnerCancelsDescendantProcessTree$'
    Tests = @('TestExecRunnerCancelsDescendantProcessTree')
  }
)

Push-Location $root
try {
  foreach ($check in $checks) {
    $arguments = @('test', '-json', '-v', '-count=1', '-timeout=3m', $check.Package, '-run', $check.Pattern)
    $output = @(& go @arguments 2>&1)
    $exitCode = $LASTEXITCODE
    $output | ForEach-Object { Write-Output $_ }
    if ($exitCode -ne 0) {
      throw "Windows controller test command failed for $($check.Package)."
    }
    $events = @(
      foreach ($line in $output) {
        try {
          $line | ConvertFrom-Json -ErrorAction Stop
        } catch {
          continue
        }
      }
    )
    foreach ($testName in $check.Tests) {
      $passed = @($events | Where-Object { $_.Action -eq 'pass' -and $_.Test -eq $testName })
      if ($passed.Count -ne 1) {
        throw "Windows controller test did not execute successfully: $testName."
      }
    }
  }
} finally {
  Pop-Location
}

Write-Output 'Windows DPAPI, session, secure temporary storage, and process-tree tests executed successfully.'
