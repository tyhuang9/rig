#!/usr/bin/env bash
set -euo pipefail

root="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)"
workflow="$root/.github/workflows/github-deployment-ci.yml"
package_name='github.com/hostd/hostd/internal/autodeploy'
test_name='TestPostgreSQLRelayOutageConvergesDurablyAcrossRelayAndControllerRestart'

# Exercise the exact pattern and jq filter used by CI, without evaluating shell text.
mapfile -t patterns < <(awk -F "'" '/^[[:space:]]*relay_setup_deadline_pattern=/ && NF == 3 { print $2 }' "$workflow")
mapfile -t filters < <(awk -F "'" 'index($0, "--arg diagnostic_pattern \"$relay_setup_deadline_pattern\"") && NF == 3 { print $2 }' "$workflow")
if (( ${#patterns[@]} != 1 || ${#filters[@]} != 1 )) || [[ -z "${patterns[0]:-}" || -z "${filters[0]:-}" ]]; then
  printf 'expected exactly one relay setup pattern and projection in the workflow\n' >&2
  exit 1
fi
diagnostic_pattern="${patterns[0]}"
projection_filter="${filters[0]}"

require_workflow_text() {
  if ! grep -F -- "$1" "$workflow" > /dev/null; then
    printf 'relay outage diagnostic workflow projection is missing: %s\n' "$1" >&2
    exit 1
  fi
}

require_workflow_text "test_name='$test_name'"
require_workflow_text 'go test -race -json -v -count=1 -timeout=5m ./internal/autodeploy -run "^${test_name}$"'
require_workflow_text 'run: bash scripts/check-relay-outage-diagnostic-projection_test.sh'

event() {
  jq -cn --arg package_name "$package_name" --arg test_name "$test_name" --arg output "$1" \
    '{Action: "output", Package: $package_name, Test: $test_name, Output: $output}'
}

project() {
  jq -c --arg package_name "$package_name" --arg test_name "$test_name" --arg diagnostic_pattern "$diagnostic_pattern" "$projection_filter"
}

accepted=0
rejected=0
assert_projects() {
  local actual
  actual="$(event "$1" | project)"
  if [[ "$actual" != "$2" ]]; then
    printf 'relay outage diagnostic projection=%q want=%q\n' "$actual" "$2" >&2
    exit 1
  fi
  accepted=$((accepted + 1))
}

assert_event_rejected() {
  local actual
  # A jq error must fail the contract, rather than count as successful rejection.
  actual="$(printf '%s\n' "$1" | project)"
  if [[ -n "$actual" ]]; then
    printf 'relay outage diagnostic projection accepted unexpected event: %q\n' "$actual" >&2
    exit 1
  fi
  rejected=$((rejected + 1))
}

assert_rejects() {
  assert_event_rejected "$(event "$1")"
}

assert_projects \
  $'    relay_outage_integration_test.go:81: relay_outage_setup_deadline phase=before_first_fixture_stop deadline_exceeded=true pre_stop_elapsed_ms=0 stop_elapsed_ms=20000\n' \
  '{"phase":"before_first_fixture_stop","deadline_exceeded":true,"pre_stop_elapsed_ms":0,"stop_elapsed_ms":20000}'
assert_projects \
  $'\trelay_outage_integration_test.go:123: relay_outage_setup_deadline phase=after_first_fixture_stop deadline_exceeded=true pre_stop_elapsed_ms=19999 stop_elapsed_ms=1' \
  '{"phase":"after_first_fixture_stop","deadline_exceeded":true,"pre_stop_elapsed_ms":19999,"stop_elapsed_ms":1}'
assert_projects \
  'relay_outage_integration_test.go:9: relay_outage_setup_deadline phase=after_first_fixture_stop deadline_exceeded=true pre_stop_elapsed_ms=20000 stop_elapsed_ms=0' \
  '{"phase":"after_first_fixture_stop","deadline_exceeded":true,"pre_stop_elapsed_ms":20000,"stop_elapsed_ms":0}'

prefix='    relay_outage_integration_test.go:81: '
message='relay_outage_setup_deadline phase=before_first_fixture_stop deadline_exceeded=true pre_stop_elapsed_ms=1 stop_elapsed_ms=1'
assert_rejects "$message"
assert_rejects "other_test.go:81: $message"
assert_rejects "relay_outage_integration_test.go:0: $message"
assert_rejects "${prefix}${message/phase=before_first_fixture_stop/phase=after_sync}"
assert_rejects "${prefix}${message/deadline_exceeded=true/deadline_exceeded=false}"
assert_rejects "${prefix}${message/deadline_exceeded=true/deadline_exceeded=yes}"
for field in pre_stop_elapsed_ms stop_elapsed_ms; do
  for value in -1 20001 01; do
    assert_rejects "${prefix}${message/$field=1/$field=$value}"
  done
done
assert_rejects "${prefix}${message} credential_text=ghp_example"
assert_rejects "credential_text=ghp_example ${prefix}${message}"
assert_rejects "${prefix}${message}"$'\nextra'
assert_rejects $'\n'"${prefix}${message}"
assert_rejects "${prefix}${message}\\n"
assert_rejects 'ordinary test progress'
assert_event_rejected ''
assert_event_rejected "$(event "${prefix}${message}" | jq -c '.Action = "fail"')"
assert_event_rejected "$(event "${prefix}${message}" | jq -c '.Package = "unrelated/package"')"
assert_event_rejected "$(event "${prefix}${message}" | jq -c '.Test = "TestUnrelated"')"

printf 'relay outage diagnostic projection contract passed: %d accepted, %d rejected\n' "$accepted" "$rejected"
