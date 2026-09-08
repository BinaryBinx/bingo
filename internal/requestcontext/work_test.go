package requestcontext

import (
	"context"
	"sync"
	"testing"
	"time"
)

func TestWorkTrackerSealsAdmissionAndRetainsDetachedWork(t *testing.T) {
	tracker := NewWorkTracker()
	if !tracker.TryStart() {
		t.Fatal("initial admission rejected")
	}
	tracker.Retain()
	done := tracker.Stop()
	if tracker.TryStart() {
		t.Fatal("admitted after Stop")
	}
	tracker.Done()
	select {
	case <-done:
		t.Fatal("detached work dropped")
	default:
	}
	tracker.Done()
	select {
	case <-done:
	default:
		t.Fatal("drain never published")
	}
	tracker.Stop() // idempotent
}

func TestWorkTrackerConcurrentAdmissionAndStop(t *testing.T) {
	for round := 0; round < 100; round++ {
		tracker := NewWorkTracker()
		start := make(chan struct{})
		var workers sync.WaitGroup
		for i := 0; i < 32; i++ {
			workers.Go(func() {
				<-start
				if tracker.TryStart() {
					tracker.Retain()
					tracker.Done()
					tracker.Done()
				}
			})
		}
		close(start)
		done := tracker.Stop()
		workers.Wait()
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Fatal("racing worker lost its release")
		}
	}
}

func BenchmarkPerformanceDeadlineParent(b *testing.B) {
	parent, cancel := context.WithCancel(context.Background())
	defer cancel()
	appParent := WithWorkTracker(parent, NewWorkTracker())
	for _, item := range []struct {
		name   string
		parent context.Context
	}{
		{"standalone-fallback", serverContext{Context: context.Background(), done: make(chan struct{})}},
		{"app-shared", appParent},
	} {
		b.Run(item.name, func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				_, cancel := context.WithTimeout(item.parent, time.Minute)
				cancel()
			}
		})
	}
}
