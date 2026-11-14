// Package hub fans out live changes to anyone watching, such as the web UI.
package hub

import "sync"

// Message is one change. Type is "run", "step" or "log".
type Message struct {
	Type  string `json:"type"`
	RunID string `json:"run_id"`
	Data  any    `json:"data"`
}

// Hub delivers messages to subscribers without ever blocking the publisher.
// A subscriber that falls behind is dropped; its channel is closed, and it is
// expected to reconnect and fetch the current state again.
type Hub struct {
	mu   sync.Mutex
	subs map[*sub]struct{}
}

type sub struct {
	ch     chan Message
	filter func(Message) bool
}

func New() *Hub { return &Hub{subs: map[*sub]struct{}{}} }

// Subscribe returns a channel of messages that pass filter (nil means all),
// and a function to stop.
func (h *Hub) Subscribe(filter func(Message) bool) (<-chan Message, func()) {
	s := &sub{ch: make(chan Message, 512), filter: filter}
	h.mu.Lock()
	h.subs[s] = struct{}{}
	h.mu.Unlock()
	var once sync.Once
	return s.ch, func() {
		once.Do(func() {
			h.mu.Lock()
			if _, ok := h.subs[s]; ok {
				delete(h.subs, s)
				close(s.ch)
			}
			h.mu.Unlock()
		})
	}
}

// Publish sends m to every interested subscriber.
func (h *Hub) Publish(m Message) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for s := range h.subs {
		if s.filter != nil && !s.filter(m) {
			continue
		}
		select {
		case s.ch <- m:
		default:
			delete(h.subs, s)
			close(s.ch)
		}
	}
}
