package main

import (
	"fmt"
	"io"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/hostd/hostd/internal/relay/wss"
)

var jobNames = [...]string{"recovery_scan", "redelivery", "expiry", "maintenance"}
var jobResults = [...]string{"success", "failure", "contended"}
var webhookOutcomes = [...]string{"auth_invalid", "invalid", "ignored", "duplicate", "persisted", "store_failure"}
var durationBounds = [...]time.Duration{100 * time.Millisecond, time.Second, 5 * time.Second, 30 * time.Second, 5 * time.Minute, 30 * time.Minute}
var backgroundItemKinds = [...]string{"expired_enrollments", "expired_rotations", "pruned_durable"}
var readinessStates = [...]string{"cached_success", "cached_failure", "stale_success", "wait_canceled", "probe_success", "probe_failure"}

type metrics struct {
	jobs             [len(jobNames)][len(jobResults)]atomic.Uint64
	jobDuration      [len(jobNames)][len(jobResults)][len(durationBounds) + 1]atomic.Uint64
	jobDurationNanos [len(jobNames)][len(jobResults)]atomic.Uint64
	jobLastSuccess   [len(jobNames)]atomic.Int64
	backgroundItems  [len(backgroundItemKinds)]atomic.Uint64
	webhooks         [len(webhookOutcomes)]atomic.Uint64
	readiness        [len(readinessStates)]atomic.Uint64
	httpServerErrors atomic.Uint64
	websocket        interface{ Stats() wss.Stats }
	accepting        *atomic.Bool
	listener         *listenerStats
	http             *relayHTTPHandler
	now              func() time.Time
}

func (m *metrics) currentTime() time.Time {
	if m != nil && m.now != nil {
		return m.now().UTC()
	}
	return time.Now().UTC()
}

func (m *metrics) observe(job, result string, duration time.Duration) {
	if m == nil {
		return
	}
	if duration < 0 {
		duration = 0
	}
	for i, name := range jobNames {
		if name != job {
			continue
		}
		for j, value := range jobResults {
			if value != result {
				continue
			}
			m.jobs[i][j].Add(1)
			m.jobDurationNanos[i][j].Add(uint64(duration))
			for bucket, bound := range durationBounds {
				if duration <= bound {
					m.jobDuration[i][j][bucket].Add(1)
				}
			}
			m.jobDuration[i][j][len(durationBounds)].Add(1)
			if result == "success" {
				m.jobLastSuccess[i].Store(m.currentTime().Unix())
			}
			return
		}
	}
}

func (m *metrics) observeBackgroundItems(kind string, count uint64) {
	if m == nil || count == 0 {
		return
	}
	for i, candidate := range backgroundItemKinds {
		if kind == candidate {
			m.backgroundItems[i].Add(count)
			return
		}
	}
}

func (m *metrics) ObserveWebhook(outcome string) {
	if m == nil {
		return
	}
	for i, candidate := range webhookOutcomes {
		if outcome == candidate {
			m.webhooks[i].Add(1)
			return
		}
	}
}

func (m *metrics) ObserveReadiness(_ string, cacheState string) {
	if m == nil {
		return
	}
	for i, candidate := range readinessStates {
		if cacheState == candidate {
			m.readiness[i].Add(1)
			return
		}
	}
}

func (m *metrics) ServeHTTP(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	m.writeTo(w)
}

func (m *metrics) writeTo(w io.Writer) {
	stats := wss.Stats{}
	if m.websocket != nil {
		stats = m.websocket.Stats()
	}
	accepting := 0
	if m.accepting != nil && m.accepting.Load() {
		accepting = 1
	}
	fmt.Fprintln(w, "# HELP rig_relay_accepting Whether the relay is accepting readiness traffic.")
	fmt.Fprintln(w, "# TYPE rig_relay_accepting gauge")
	fmt.Fprintf(w, "rig_relay_accepting %d\n", accepting)
	fmt.Fprintln(w, "# HELP rig_relay_wss_connections_active Current admitted WebSocket transports, including handshakes.")
	fmt.Fprintln(w, "# TYPE rig_relay_wss_connections_active gauge")
	fmt.Fprintf(w, "rig_relay_wss_connections_active %d\n", stats.Active)
	fmt.Fprintln(w, "# HELP rig_relay_wss_sessions_authenticated Current authenticated WebSocket sessions.")
	fmt.Fprintln(w, "# TYPE rig_relay_wss_sessions_authenticated gauge")
	fmt.Fprintf(w, "rig_relay_wss_sessions_authenticated %d\n", stats.Authenticated)
	fmt.Fprintln(w, "# HELP rig_relay_wss_leases_active Current authenticated sessions holding a fenced lease.")
	fmt.Fprintln(w, "# TYPE rig_relay_wss_leases_active gauge")
	fmt.Fprintf(w, "rig_relay_wss_leases_active %d\n", stats.LeasesActive)
	fmt.Fprintln(w, "# HELP rig_relay_wss_outbound_queue_capacity_per_session Configured bounded outbound queue capacity per session.")
	fmt.Fprintln(w, "# TYPE rig_relay_wss_outbound_queue_capacity_per_session gauge")
	fmt.Fprintf(w, "rig_relay_wss_outbound_queue_capacity_per_session %d\n", stats.OutboundCapacity)
	fmt.Fprintln(w, "# HELP rig_relay_wss_connections_capacity Configured WebSocket connection capacity.")
	fmt.Fprintln(w, "# TYPE rig_relay_wss_connections_capacity gauge")
	fmt.Fprintf(w, "rig_relay_wss_connections_capacity %d\n", stats.Capacity)
	fmt.Fprintln(w, "# HELP rig_relay_wss_capacity_rejections_total Total WebSocket admissions rejected at capacity.")
	fmt.Fprintln(w, "# TYPE rig_relay_wss_capacity_rejections_total counter")
	fmt.Fprintf(w, "rig_relay_wss_capacity_rejections_total %d\n", stats.CapacityRejections)
	fmt.Fprintln(w, "# HELP rig_relay_wss_lifecycle_total Aggregate WebSocket lifecycle outcomes.")
	fmt.Fprintln(w, "# TYPE rig_relay_wss_lifecycle_total counter")
	for i, outcome := range wss.LifecycleOutcomeNames() {
		fmt.Fprintf(w, "rig_relay_wss_lifecycle_total{outcome=%q} %d\n", outcome, stats.LifecycleOutcomes[i])
	}
	fmt.Fprintln(w, "# HELP rig_relay_wss_deliveries_total Aggregate durable envelopes delivered.")
	fmt.Fprintln(w, "# TYPE rig_relay_wss_deliveries_total counter")
	for i, kind := range wss.DeliveryKindNames() {
		fmt.Fprintf(w, "rig_relay_wss_deliveries_total{kind=%q} %d\n", kind, stats.Deliveries[i])
	}
	fmt.Fprintln(w, "# HELP rig_relay_wss_decisions_total Aggregate controller decisions processed.")
	fmt.Fprintln(w, "# TYPE rig_relay_wss_decisions_total counter")
	for i, decision := range wss.DecisionNames() {
		fmt.Fprintf(w, "rig_relay_wss_decisions_total{decision=%q} %d\n", decision, stats.Decisions[i])
	}
	listenerActive, listenerCapacity, listenerSaturation := int64(0), int64(0), uint64(0)
	if m.listener != nil {
		listenerActive, listenerCapacity, listenerSaturation = m.listener.active.Load(), m.listener.capacity, m.listener.saturation.Load()
	}
	fmt.Fprintln(w, "# HELP rig_relay_listener_connections_active Current accepted TCP connections.")
	fmt.Fprintln(w, "# TYPE rig_relay_listener_connections_active gauge")
	fmt.Fprintf(w, "rig_relay_listener_connections_active %d\n", listenerActive)
	fmt.Fprintln(w, "# HELP rig_relay_listener_connections_capacity Hard accepted TCP connection capacity.")
	fmt.Fprintln(w, "# TYPE rig_relay_listener_connections_capacity gauge")
	fmt.Fprintf(w, "rig_relay_listener_connections_capacity %d\n", listenerCapacity)
	fmt.Fprintln(w, "# HELP rig_relay_listener_saturation_total Accept attempts observed while TCP capacity was full.")
	fmt.Fprintln(w, "# TYPE rig_relay_listener_saturation_total counter")
	fmt.Fprintf(w, "rig_relay_listener_saturation_total %d\n", listenerSaturation)
	httpActive, httpRejected := int64(0), uint64(0)
	httpCapacity := int64(serviceConcurrency)
	if m.http != nil {
		httpActive, httpRejected = m.http.serviceActive.Load(), m.http.serviceRejected.Load()
		if m.http.serviceSlots != nil {
			httpCapacity = int64(cap(m.http.serviceSlots))
		}
	}
	fmt.Fprintln(w, "# HELP rig_relay_http_service_active Current non-operational HTTP service handlers.")
	fmt.Fprintln(w, "# TYPE rig_relay_http_service_active gauge")
	fmt.Fprintf(w, "rig_relay_http_service_active %d\n", httpActive)
	fmt.Fprintln(w, "# HELP rig_relay_http_service_capacity Non-operational HTTP service concurrency capacity.")
	fmt.Fprintln(w, "# TYPE rig_relay_http_service_capacity gauge")
	fmt.Fprintf(w, "rig_relay_http_service_capacity %d\n", httpCapacity)
	fmt.Fprintln(w, "# HELP rig_relay_http_service_saturation_total Service requests rejected at concurrency capacity.")
	fmt.Fprintln(w, "# TYPE rig_relay_http_service_saturation_total counter")
	fmt.Fprintf(w, "rig_relay_http_service_saturation_total %d\n", httpRejected)
	fmt.Fprintln(w, "# HELP rig_relay_http_server_errors_total Sanitized aggregate net/http server errors.")
	fmt.Fprintln(w, "# TYPE rig_relay_http_server_errors_total counter")
	fmt.Fprintf(w, "rig_relay_http_server_errors_total{code=%q} %d\n", "server_error", m.httpServerErrors.Load())
	fmt.Fprintln(w, "# HELP rig_relay_webhook_outcomes_total Aggregate webhook outcomes.")
	fmt.Fprintln(w, "# TYPE rig_relay_webhook_outcomes_total counter")
	for i, outcome := range webhookOutcomes {
		fmt.Fprintf(w, "rig_relay_webhook_outcomes_total{outcome=%q} %d\n", outcome, m.webhooks[i].Load())
	}
	fmt.Fprintln(w, "# HELP rig_relay_readiness_total Readiness probe and cache outcomes.")
	fmt.Fprintln(w, "# TYPE rig_relay_readiness_total counter")
	for i, state := range readinessStates {
		fmt.Fprintf(w, "rig_relay_readiness_total{state=%q} %d\n", state, m.readiness[i].Load())
	}
	fmt.Fprintln(w, "# HELP rig_relay_background_runs_total Total background job outcomes.")
	fmt.Fprintln(w, "# TYPE rig_relay_background_runs_total counter")
	for i, job := range jobNames {
		for j, outcome := range jobResults {
			fmt.Fprintf(w, "rig_relay_background_runs_total{job=%q,outcome=%q} %d\n", job, outcome, m.jobs[i][j].Load())
		}
	}
	fmt.Fprintln(w, "# HELP rig_relay_background_duration_seconds Background job duration with fixed buckets through 1800 seconds.")
	fmt.Fprintln(w, "# TYPE rig_relay_background_duration_seconds histogram")
	for i, job := range jobNames {
		for j, outcome := range jobResults {
			for bucket, bound := range durationBounds {
				fmt.Fprintf(w, "rig_relay_background_duration_seconds_bucket{job=%q,outcome=%q,le=%q} %d\n", job, outcome, fmt.Sprintf("%g", bound.Seconds()), m.jobDuration[i][j][bucket].Load())
			}
			fmt.Fprintf(w, "rig_relay_background_duration_seconds_bucket{job=%q,outcome=%q,le=%q} %d\n", job, outcome, "+Inf", m.jobDuration[i][j][len(durationBounds)].Load())
			fmt.Fprintf(w, "rig_relay_background_duration_seconds_sum{job=%q,outcome=%q} %.6f\n", job, outcome, float64(m.jobDurationNanos[i][j].Load())/float64(time.Second))
			fmt.Fprintf(w, "rig_relay_background_duration_seconds_count{job=%q,outcome=%q} %d\n", job, outcome, m.jobs[i][j].Load())
		}
	}
	fmt.Fprintln(w, "# HELP rig_relay_background_last_success_timestamp_seconds Unix timestamp of the last successful run, or zero.")
	fmt.Fprintln(w, "# TYPE rig_relay_background_last_success_timestamp_seconds gauge")
	fmt.Fprintln(w, "# HELP rig_relay_background_last_success_age_seconds Age of the last successful run, or zero.")
	fmt.Fprintln(w, "# TYPE rig_relay_background_last_success_age_seconds gauge")
	nowUnix := m.currentTime().Unix()
	for i, job := range jobNames {
		last := m.jobLastSuccess[i].Load()
		age := int64(0)
		if last > 0 && nowUnix > last {
			age = nowUnix - last
		}
		fmt.Fprintf(w, "rig_relay_background_last_success_timestamp_seconds{job=%q} %d\n", job, last)
		fmt.Fprintf(w, "rig_relay_background_last_success_age_seconds{job=%q} %d\n", job, age)
	}
	fmt.Fprintln(w, "# HELP rig_relay_background_items_total Aggregate items expired or pruned by background jobs.")
	fmt.Fprintln(w, "# TYPE rig_relay_background_items_total counter")
	for i, kind := range backgroundItemKinds {
		fmt.Fprintf(w, "rig_relay_background_items_total{kind=%q} %d\n", kind, m.backgroundItems[i].Load())
	}
}
