package sse

import (
	"testing"
	"time"
)

func TestSubscribePublishReceive(t *testing.T) {
	b := NewBroker()
	ch, cancel := b.Subscribe("proj:dev")
	defer cancel()

	ev := Event{Project: "proj", Environment: "dev", ETag: "abc123"}
	b.Publish("proj:dev", ev)

	select {
	case got := <-ch:
		if got.ETag != "abc123" {
			t.Fatalf("got ETag %q, want %q", got.ETag, "abc123")
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for event")
	}
}

func TestTwoSubscribersSameTopic(t *testing.T) {
	b := NewBroker()
	ch1, cancel1 := b.Subscribe("proj:dev")
	defer cancel1()
	ch2, cancel2 := b.Subscribe("proj:dev")
	defer cancel2()

	ev := Event{Project: "proj", Environment: "dev", ETag: "xyz"}
	b.Publish("proj:dev", ev)

	for _, ch := range []<-chan Event{ch1, ch2} {
		select {
		case got := <-ch:
			if got.ETag != "xyz" {
				t.Fatalf("got ETag %q, want %q", got.ETag, "xyz")
			}
		case <-time.After(time.Second):
			t.Fatal("timed out waiting for event on subscriber")
		}
	}
}

func TestDifferentTopicNoReceive(t *testing.T) {
	b := NewBroker()
	ch, cancel := b.Subscribe("proj:staging")
	defer cancel()

	b.Publish("proj:dev", Event{Project: "proj", Environment: "dev", ETag: "nope"})

	select {
	case got := <-ch:
		t.Fatalf("unexpected event on wrong topic: %+v", got)
	case <-time.After(100 * time.Millisecond):
		// correct — nothing received
	}
}

func TestSlowSubscriberDoesNotBlockFast(t *testing.T) {
	b := NewBroker()

	// slow subscriber: never reads from the channel (buffer 8, then drops)
	_, cancelSlow := b.Subscribe("proj:dev")
	defer cancelSlow()

	// fast subscriber: reads immediately
	chFast, cancelFast := b.Subscribe("proj:dev")
	defer cancelFast()

	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 20; i++ {
			b.Publish("proj:dev", Event{ETag: "e"})
		}
	}()

	select {
	case <-done:
		// good — all 20 publishes finished without hanging
	case <-time.After(time.Second):
		t.Fatal("Publish blocked on slow subscriber")
	}

	// fast subscriber should have received some events (up to its buffer size)
	received := 0
loop:
	for {
		select {
		case <-chFast:
			received++
		default:
			break loop
		}
	}
	if received == 0 {
		t.Fatal("fast subscriber received no events")
	}
}

func TestCancelIsIdempotent(t *testing.T) {
	b := NewBroker()
	_, cancel := b.Subscribe("proj:dev")
	cancel()
	cancel() // must not panic
}

func TestCancelClosesChannel(t *testing.T) {
	b := NewBroker()
	ch, cancel := b.Subscribe("proj:dev")

	cancel()

	// channel should be closed
	select {
	case _, ok := <-ch:
		if ok {
			t.Fatal("expected channel to be closed, got a value")
		}
	case <-time.After(time.Second):
		t.Fatal("channel was not closed after cancel")
	}

	// subsequent publishes should not panic
	b.Publish("proj:dev", Event{ETag: "after-cancel"})
}
