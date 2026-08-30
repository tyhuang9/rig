package tui

import (
	"context"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/hostd/hostd/internal/apicontract"
)

func TestFollowEventBurstCoalescesSnapshotsAndKeepsTerminalResultExact(t *testing.T) {
	f := &fakeClient{}
	m := switchboardModel(f)
	job := activeJob("job-a", "app-a", "deploy")
	f.job = job
	m.followedJob, m.followedJobID = job, job.ID
	m.followGeneration = 7
	m.followContext = context.Background()
	m.followEvents = make(chan apicontract.JobEvent)
	m.followErrors = make(chan error)

	_, first := m.Update(followEventMsg{generation: 7, event: apicontract.JobEvent{ID: 1, JobID: job.ID, Phase: "prepare", Code: "phase_started"}})
	firstFetch := snapshotCommandFromBatch(t, first)
	for id := int64(2); id <= 25; id++ {
		_, cmd := m.Update(followEventMsg{generation: 7, event: apicontract.JobEvent{ID: id, JobID: job.ID, Phase: "apply", Code: "phase_started"}})
		if cmd == nil {
			t.Fatalf("event %d stopped draining the follow stream", id)
		}
	}
	if f.jobCalls != 0 {
		t.Fatalf("burst performed GET before the coalesced command ran: %d", f.jobCalls)
	}

	firstSnapshot := firstFetch().(jobSnapshotMsg)
	_, followup := m.Update(firstSnapshot)
	if followup == nil {
		t.Fatal("events accumulated during the snapshot did not request one coalesced refresh")
	}
	secondSnapshot := snapshotMessageFromCommand(t, followup)
	_, _ = m.Update(secondSnapshot)
	if f.jobCalls != 2 {
		t.Fatalf("25-event burst performed %d exact job GETs, want 2", f.jobCalls)
	}

	terminal := job
	terminal.Status, terminal.Progress = "succeeded", 100
	f.job = terminal
	_, terminalCmd := m.Update(followEventMsg{generation: 7, event: apicontract.JobEvent{ID: 26, JobID: job.ID, Phase: "succeeded", Code: "job_succeeded"}})
	terminalSnapshot := snapshotCommandFromBatch(t, terminalCmd)().(jobSnapshotMsg)
	m.Update(terminalSnapshot)
	if f.jobCalls != 3 || m.screen != screenResult || m.result == nil || m.result.JobID != job.ID {
		t.Fatalf("terminal refresh calls=%d screen=%v result=%+v", f.jobCalls, m.screen, m.result)
	}
}

func TestFollowEOFAfterPendingSnapshotPerformsFinalExactRefresh(t *testing.T) {
	f := &fakeClient{}
	m := switchboardModel(f)
	job := activeJob("job-a", "app-a", "deploy")
	f.job = job
	m.followedJob, m.followedJobID = job, job.ID
	m.followGeneration = 3
	m.followContext = context.Background()
	m.followEvents = make(chan apicontract.JobEvent)
	m.followErrors = make(chan error)

	_, eventCmd := m.Update(followEventMsg{generation: 3, event: apicontract.JobEvent{ID: 1, JobID: job.ID, Code: "phase_started"}})
	firstFetch := snapshotCommandFromBatch(t, eventCmd)
	if _, cmd := m.Update(followEventMsg{generation: 3, done: true}); cmd != nil {
		t.Fatal("EOF started a concurrent exact refresh while one was pending")
	}
	firstSnapshot := firstFetch().(jobSnapshotMsg)
	terminal := job
	terminal.Status, terminal.Progress = "succeeded", 100
	f.job = terminal
	_, finalFetch := m.Update(firstSnapshot)
	finalSnapshot := snapshotMessageFromCommand(t, finalFetch)
	m.Update(finalSnapshot)
	if f.jobCalls != 2 || m.screen != screenResult || m.result == nil || m.result.JobID != job.ID {
		t.Fatalf("EOF exact refresh calls=%d screen=%v result=%+v", f.jobCalls, m.screen, m.result)
	}
}

type blockingSnapshotClient struct {
	*fakeClient
	started chan context.Context
}

func (c *blockingSnapshotClient) Job(ctx context.Context, _ string) (apicontract.Job, error) {
	c.started <- ctx
	<-ctx.Done()
	return apicontract.Job{}, ctx.Err()
}

func TestSwitchingFollowCancelsSnapshotAndIgnoresStaleGeneration(t *testing.T) {
	client := &blockingSnapshotClient{fakeClient: &fakeClient{}, started: make(chan context.Context, 1)}
	m := NewModel(context.Background(), client, "http://127.0.0.1:7345")
	m.followedJob = activeJob("job-a", "app-a", "deploy")
	_ = m.startFollowing("job-a", 0, true)
	generationA := m.followGeneration
	fetch := snapshotCommandFromBatch(t, m.startFollowSnapshot(generationA, false))
	result := make(chan jobSnapshotMsg, 1)
	go func() { result <- fetch().(jobSnapshotMsg) }()

	var snapshotCtx context.Context
	select {
	case snapshotCtx = <-client.started:
	case <-time.After(time.Second):
		t.Fatal("snapshot did not start")
	}
	_ = m.startFollowing("job-b", 0, true)
	m.followedJob = activeJob("job-b", "app-b", "deploy")
	select {
	case <-snapshotCtx.Done():
	case <-time.After(time.Second):
		t.Fatal("switching follows did not cancel the stale snapshot context")
	}

	var stale jobSnapshotMsg
	select {
	case stale = <-result:
	case <-time.After(time.Second):
		t.Fatal("cancelled stale snapshot did not complete")
	}
	if stale.generation != generationA || stale.err == nil {
		t.Fatalf("stale snapshot=%+v", stale)
	}
	m.Update(stale)
	if m.followedJobID != "job-b" || m.followedJob.ID != "job-b" || m.followGeneration == generationA {
		t.Fatalf("stale snapshot changed current follow: id=%q job=%q generation=%d", m.followedJobID, m.followedJob.ID, m.followGeneration)
	}
}

func snapshotCommandFromBatch(t *testing.T, cmd tea.Cmd) tea.Cmd {
	t.Helper()
	if cmd == nil {
		t.Fatal("snapshot command is nil")
	}
	msg := cmd()
	batch, ok := msg.(tea.BatchMsg)
	if !ok || len(batch) < 2 {
		t.Fatalf("snapshot command did not continue draining the stream: %T", msg)
	}
	return batch[len(batch)-1]
}

func snapshotMessageFromCommand(t *testing.T, cmd tea.Cmd) jobSnapshotMsg {
	t.Helper()
	if cmd == nil {
		t.Fatal("snapshot command is nil")
	}
	msg := cmd()
	if snapshot, ok := msg.(jobSnapshotMsg); ok {
		return snapshot
	}
	batch, ok := msg.(tea.BatchMsg)
	if !ok || len(batch) == 0 {
		t.Fatalf("unexpected snapshot command message: %T", msg)
	}
	snapshot, ok := batch[len(batch)-1]().(jobSnapshotMsg)
	if !ok {
		t.Fatalf("batch did not end with a snapshot command")
	}
	return snapshot
}
