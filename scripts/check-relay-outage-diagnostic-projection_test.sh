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

# The setup diagnostic carries only a closed phase and error class. Exercise the
# exact second CI projection, with the same package/test/action filtering.
mapfile -t patterns < <(awk -F "'" '/^[[:space:]]*relay_setup_failure_pattern=/ && NF == 3 { print $2 }' "$workflow")
mapfile -t filters < <(awk -F "'" 'index($0, "--arg diagnostic_pattern \"$relay_setup_failure_pattern\"") && NF == 3 { print $2 }' "$workflow")
if (( ${#patterns[@]} != 1 || ${#filters[@]} != 1 )) || [[ -z "${patterns[0]:-}" || -z "${filters[0]:-}" ]]; then
  printf 'expected exactly one relay setup failure pattern and projection in the workflow\n' >&2
  exit 1
fi
diagnostic_pattern="${patterns[0]}"
projection_filter="${filters[0]}"
accepted=0
rejected=0

for phase in controller_seed relay_enrollment_create relay_enrollment_claim relay_enrollment_complete; do
  for error_class in deadline canceled constraint other; do
    assert_projects \
      "${prefix}relay_outage_setup_failure phase=${phase} error_class=${error_class}"$'\n' \
      "{\"phase\":\"${phase}\",\"error_class\":\"${error_class}\"}"
  done
done
assert_projects \
  $'\trelay_outage_integration_test.go:9: relay_outage_setup_failure phase=controller_seed error_class=other' \
  '{"phase":"controller_seed","error_class":"other"}'

message='relay_outage_setup_failure phase=controller_seed error_class=other'
assert_rejects "$message"
assert_rejects "other_test.go:81: $message"
assert_rejects "relay_outage_integration_test.go:0: $message"
assert_rejects "relay_outage_integration_test.go:01: $message"
assert_rejects "${prefix}${message/phase=controller_seed/phase=relay_enrollment}"
assert_rejects "${prefix}${message/phase=controller_seed/phase=}"
for error_class in timeout cancelled CONSTRAINT unknown ''; do
  assert_rejects "${prefix}${message/error_class=other/error_class=$error_class}"
done
assert_rejects "${prefix}${message} detail=synthetic_sensitive_text"
assert_rejects "detail=synthetic_sensitive_text ${prefix}${message}"
assert_rejects "${prefix}${message}"$'\nextra'
assert_rejects $'\n'"${prefix}${message}"
assert_rejects "${prefix}${message}"$'\n\n'
assert_rejects "${prefix}${message}\\n"
assert_rejects "${prefix}${message} "
assert_rejects "${prefix}${message}"$'\r\n'
assert_rejects "${prefix}relay_outage_setup_failure error_class=other phase=controller_seed"
assert_rejects 'ordinary test progress'
assert_event_rejected ''
assert_event_rejected "$(event "${prefix}${message}" | jq -c '.Action = "fail"')"
assert_event_rejected "$(event "${prefix}${message}" | jq -c '.Package = "unrelated/package"')"
assert_event_rejected "$(event "${prefix}${message}" | jq -c '.Test = "TestUnrelated"')"

printf 'relay setup failure projection contract passed: %d accepted, %d rejected\n' "$accepted" "$rejected"

# Exercise the exact initial-sync observation grammar and projection. The
# observer callback elapsed fields are bounded delivery times, not claims about
# when the underlying supervisor transition occurred.
mapfile -t patterns < <(awk -F "'" '/^[[:space:]]*relay_initial_sync_pattern=/ && NF == 3 { print $2 }' "$workflow")
mapfile -t filters < <(awk -F "'" 'index($0, "--arg diagnostic_pattern \"$relay_initial_sync_pattern\"") && NF == 3 { print $2 }' "$workflow")
if (( ${#patterns[@]} != 1 || ${#filters[@]} != 1 )) || [[ -z "${patterns[0]:-}" || -z "${filters[0]:-}" ]]; then
  printf 'expected exactly one relay initial-sync pattern and projection in the workflow\n' >&2
  exit 1
fi
diagnostic_pattern="${patterns[0]}"
projection_filter="${filters[0]}"
accepted=0
rejected=0

message='timed out waiting for initial authenticated subscription sync: wait_condition=initial_sync_timeout sqlite_active_controller_subscriptions=1 sqlite_subscription_sync_heads=1 sqlite_inflight_sync_sets=0 postgres_relay_sessions=0 postgres_controller_leases=0 postgres_subscriptions_sync_commands=0 postgres_current_relay_subscriptions=0 supervisor_state=connecting supervisor_outcome=empty supervisor_paused=false transport_connecting_count=1 transport_connecting_observer_elapsed_ms=10 transport_authenticating_count=0 transport_authenticating_observer_elapsed_ms=-1 transport_ready_count=0 transport_ready_observer_elapsed_ms=-1 supervisor_event_kind=handshake supervisor_event_stage=connecting supervisor_event_outcome=empty supervisor_event_attempt=1 supervisor_observer_dropped=0'
assert_projects \
  "${prefix}${message}"$'\n' \
  '{"wait_condition":"initial_sync_timeout","sqlite_active_controller_subscriptions":1,"sqlite_subscription_sync_heads":1,"sqlite_inflight_sync_sets":0,"postgres_relay_sessions":0,"postgres_controller_leases":0,"postgres_subscriptions_sync_commands":0,"postgres_current_relay_subscriptions":0,"supervisor_state":"connecting","supervisor_outcome":"empty","supervisor_paused":false,"transport_connecting_count":1,"transport_connecting_observer_elapsed_ms":10,"transport_authenticating_count":0,"transport_authenticating_observer_elapsed_ms":-1,"transport_ready_count":0,"transport_ready_observer_elapsed_ms":-1,"supervisor_event_kind":"handshake","supervisor_event_stage":"connecting","supervisor_event_outcome":"empty","supervisor_event_attempt":1,"supervisor_observer_dropped":0}'

boundary='timed out waiting for initial authenticated subscription sync: wait_condition=setup_context_done sqlite_active_controller_subscriptions=-1 sqlite_subscription_sync_heads=1000 sqlite_inflight_sync_sets=-1 postgres_relay_sessions=1000 postgres_controller_leases=-1 postgres_subscriptions_sync_commands=1000 postgres_current_relay_subscriptions=-1 supervisor_state=unavailable supervisor_outcome=unavailable supervisor_paused=true transport_connecting_count=0 transport_connecting_observer_elapsed_ms=-1 transport_authenticating_count=1000 transport_authenticating_observer_elapsed_ms=20000 transport_ready_count=1 transport_ready_observer_elapsed_ms=0 supervisor_event_kind=unavailable supervisor_event_stage=unavailable supervisor_event_outcome=unavailable supervisor_event_attempt=1000000 supervisor_observer_dropped=1000000'
assert_projects \
  $'\t'"relay_outage_integration_test.go:9: ${boundary}" \
  '{"wait_condition":"setup_context_done","sqlite_active_controller_subscriptions":-1,"sqlite_subscription_sync_heads":1000,"sqlite_inflight_sync_sets":-1,"postgres_relay_sessions":1000,"postgres_controller_leases":-1,"postgres_subscriptions_sync_commands":1000,"postgres_current_relay_subscriptions":-1,"supervisor_state":"unavailable","supervisor_outcome":"unavailable","supervisor_paused":true,"transport_connecting_count":0,"transport_connecting_observer_elapsed_ms":-1,"transport_authenticating_count":1000,"transport_authenticating_observer_elapsed_ms":20000,"transport_ready_count":1,"transport_ready_observer_elapsed_ms":0,"supervisor_event_kind":"unavailable","supervisor_event_stage":"unavailable","supervisor_event_outcome":"unavailable","supervisor_event_attempt":1000000,"supervisor_observer_dropped":1000000}'

ready_message='timed out waiting for initial authenticated subscription sync: wait_condition=initial_sync_timeout sqlite_active_controller_subscriptions=0 sqlite_subscription_sync_heads=0 sqlite_inflight_sync_sets=0 postgres_relay_sessions=0 postgres_controller_leases=0 postgres_subscriptions_sync_commands=0 postgres_current_relay_subscriptions=0 supervisor_state=ready supervisor_outcome=empty supervisor_paused=false transport_connecting_count=1 transport_connecting_observer_elapsed_ms=0 transport_authenticating_count=1 transport_authenticating_observer_elapsed_ms=1 transport_ready_count=1 transport_ready_observer_elapsed_ms=2 supervisor_event_kind=ready supervisor_event_stage=ready supervisor_event_outcome=empty supervisor_event_attempt=0 supervisor_observer_dropped=1'
assert_projects \
  "${prefix}${ready_message}" \
  '{"wait_condition":"initial_sync_timeout","sqlite_active_controller_subscriptions":0,"sqlite_subscription_sync_heads":0,"sqlite_inflight_sync_sets":0,"postgres_relay_sessions":0,"postgres_controller_leases":0,"postgres_subscriptions_sync_commands":0,"postgres_current_relay_subscriptions":0,"supervisor_state":"ready","supervisor_outcome":"empty","supervisor_paused":false,"transport_connecting_count":1,"transport_connecting_observer_elapsed_ms":0,"transport_authenticating_count":1,"transport_authenticating_observer_elapsed_ms":1,"transport_ready_count":1,"transport_ready_observer_elapsed_ms":2,"supervisor_event_kind":"ready","supervisor_event_stage":"ready","supervisor_event_outcome":"empty","supervisor_event_attempt":0,"supervisor_observer_dropped":1}'

assert_rejects "$message"
assert_rejects "other_test.go:81: $message"
assert_rejects "relay_outage_integration_test.go:0: $message"
assert_rejects "${prefix}${message/wait_condition=initial_sync_timeout/wait_condition=unknown}"
assert_rejects "${prefix}${message/sqlite_active_controller_subscriptions=1/sqlite_active_controller_subscriptions=1001}"
assert_rejects "${prefix}${message/transport_connecting_observer_elapsed_ms=10/transport_connecting_observer_elapsed_ms=20001}"
assert_rejects "${prefix}${message/supervisor_event_attempt=1/supervisor_event_attempt=1000001}"
assert_rejects "${prefix}${message/supervisor_observer_dropped=0/supervisor_observer_dropped=1000001}"
assert_rejects "${prefix}${message/transport_authenticating_observer_elapsed_ms=-1/transport_authenticating_observer_elapsed_ms=0}"
assert_rejects "${prefix}${message/transport_connecting_count=1/transport_connecting_count=0}"
assert_rejects "${prefix}${message/supervisor_event_kind=handshake/supervisor_event_kind=private_kind}"
assert_rejects "${prefix}${message} credential_text=ghp_example"
assert_rejects "credential_text=ghp_example ${prefix}${message}"
assert_rejects "${prefix}${message}"$'\nextra'
assert_rejects $'\n'"${prefix}${message}"
assert_rejects "${prefix}${message}\\n"
assert_rejects "${prefix}${message/wait_condition=initial_sync_timeout sqlite_active_controller_subscriptions=1/sqlite_active_controller_subscriptions=1 wait_condition=initial_sync_timeout}"
assert_event_rejected ''
assert_event_rejected "$(event "${prefix}${message}" | jq -c '.Action = "fail"')"
assert_event_rejected "$(event "${prefix}${message}" | jq -c '.Package = "unrelated/package"')"
assert_event_rejected "$(event "${prefix}${message}" | jq -c '.Test = "TestUnrelated"')"

printf 'relay initial-sync observation projection contract passed: %d accepted, %d rejected\n' "$accepted" "$rejected"
