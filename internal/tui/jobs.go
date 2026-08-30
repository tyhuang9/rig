package tui

import (
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/hostd/hostd/internal/apicontract"
)

const (
	maxRecentEvents = 20
	maxEventBytes   = 64 << 10
	maxPhases       = 12
)

type phaseState struct {
	Name      string
	Completed bool
}

func isTerminalJob(job apicontract.Job) bool {
	switch strings.ToLower(job.Status) {
	case "succeeded", "failed", "cancelled", "interrupted", "needs_attention":
		return true
	default:
		return false
	}
}
func isFollowTerminal(job apicontract.Job) bool {
	return isTerminalJob(job) || strings.EqualFold(job.Status, "waiting_user")
}
func isActiveJob(job apicontract.Job) bool {
	switch strings.ToLower(job.Status) {
	case "queued", "assigned", "running", "waiting_external", "waiting_user":
		return true
	default:
		return false
	}
}

func isCancellationPending(job apicontract.Job) bool {
	status, phase := strings.ToLower(strings.TrimSpace(job.Status)), strings.ToLower(strings.TrimSpace(job.Phase))
	return status == "cancelling" || phase == "cancelling" || (status == "waiting_external" && phase == "cancelling")
}

func relevantJob(appID string, jobs []apicontract.Job) *apicontract.Job {
	matched := make([]apicontract.Job, 0)
	for _, job := range jobs {
		if job.ResourceType == "application" && job.ResourceID == appID {
			matched = append(matched, job)
		}
	}
	if len(matched) == 0 {
		return nil
	}
	sort.SliceStable(matched, func(i, j int) bool {
		ia, ja := isActiveJob(matched[i]), isActiveJob(matched[j])
		if ia != ja {
			return ia
		}
		it, ierr := time.Parse(time.RFC3339Nano, matched[i].UpdatedAt)
		jt, jerr := time.Parse(time.RFC3339Nano, matched[j].UpdatedAt)
		if ierr == nil && jerr == nil && !it.Equal(jt) {
			return it.After(jt)
		}
		return matched[i].ID < matched[j].ID
	})
	job := matched[0]
	return &job
}

func appendBoundedEvent(events []apicontract.JobEvent, event apicontract.JobEvent) []apicontract.JobEvent {
	event.Message = sanitizeIdentity(event.Message, maxAPITextBytes)
	event.Phase = sanitizeIdentity(event.Phase, 256)
	events = append(events, event)
	bytes, start := 0, len(events)
	for start > 0 && len(events)-start < maxRecentEvents {
		size := len(events[start-1].Message) + len(events[start-1].Phase)
		if bytes+size > maxEventBytes {
			break
		}
		bytes += size
		start--
	}
	return append([]apicontract.JobEvent(nil), events[start:]...)
}

func updatePhases(phases []phaseState, phase string) []phaseState {
	phase = sanitizeIdentity(phase, 256)
	if phase == "" {
		return phases
	}
	for i := range phases {
		if phases[i].Name == phase {
			for j := range phases {
				phases[j].Completed = j < i
			}
			return phases
		}
	}
	if len(phases) > 0 {
		phases[len(phases)-1].Completed = true
	}
	phases = append(phases, phaseState{Name: phase})
	if len(phases) > maxPhases {
		phases = append([]phaseState(nil), phases[len(phases)-maxPhases:]...)
	}
	return phases
}

func jobSummary(job apicontract.Job) string {
	label := statusWord(job.Status)
	if isCancellationPending(job) {
		label = "Cancelling"
	}
	if isActiveJob(job) {
		label += " " + percent(job.Progress)
	}
	return sanitizeIdentity(job.Type, 128) + " · " + label
}
func percent(value int) string {
	if value < 0 {
		value = 0
	}
	if value > 100 {
		value = 100
	}
	return strconv.Itoa(value) + "%"
}
