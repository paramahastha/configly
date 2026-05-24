package sse

import "sync"

type Event struct {
	Project     string `json:"project"`
	Environment string `json:"environment"`
	ETag        string `json:"etag"`
}

type subscriber struct {
	ch    chan Event
	topic string
}

type Broker struct {
	mu   sync.RWMutex
	subs map[*subscriber]struct{}
}

func NewBroker() *Broker { return &Broker{subs: map[*subscriber]struct{}{}} }

func (b *Broker) Subscribe(topic string) (<-chan Event, func()) {
	s := &subscriber{ch: make(chan Event, 8), topic: topic}
	b.mu.Lock()
	b.subs[s] = struct{}{}
	b.mu.Unlock()
	var once sync.Once
	return s.ch, func() {
		once.Do(func() {
			b.mu.Lock()
			delete(b.subs, s)
			b.mu.Unlock()
			close(s.ch)
		})
	}
}

func (b *Broker) Publish(topic string, ev Event) {
	b.mu.RLock()
	defer b.mu.RUnlock()
	for s := range b.subs {
		if s.topic != topic {
			continue
		}
		select {
		case s.ch <- ev:
		default:
			// Slow subscriber — drop.
		}
	}
}

func Topic(project, env string) string { return project + ":" + env }
