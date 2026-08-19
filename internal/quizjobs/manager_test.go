package quizjobs

import (
	"context"
	"testing"
	"time"
)

func TestManagerStartsWorkOutsideRequest(t *testing.T) {
	m := NewManager(time.Minute)
	started := make(chan struct{})
	release := make(chan struct{})

	job := m.Start(7, 11, func(_ context.Context, report func(Update)) (string, error) {
		close(started)
		report(Update{Stage: "research", Message: "working", Metrics: true, Total: 5, Sources: 2})
		<-release
		return "/study/11", nil
	})

	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("work did not start asynchronously")
	}

	status, ok := m.Get(job.ID, 7, 11)
	if !ok {
		t.Fatal("job not found for owner")
	}
	if status.Status != "running" && status.Status != "queued" {
		t.Fatalf("status = %q, want queued or running", status.Status)
	}

	if _, ok := m.Get(job.ID, 8, 11); ok {
		t.Fatal("foreign user can read job")
	}

	close(release)
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		status, _ = m.Get(job.ID, 7, 11)
		if status.Status == "succeeded" {
			if status.Redirect != "/study/11" || status.Stage != "research" || status.Sources != 2 || len(status.Activities) != 1 {
				t.Fatalf("completed job = %+v", status)
			}
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("job did not complete: %+v", status)
}
