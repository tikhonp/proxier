package jobs

import "sync"

// hub wakes live views of a job after each log flush or state change, so the
// stream never polls the database for new lines.
type hub struct {
	mu   sync.Mutex
	subs map[int64]map[chan struct{}]struct{}
}

func newHub() *hub { return &hub{subs: map[int64]map[chan struct{}]struct{}{}} }

func (h *hub) notify(id int64) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for c := range h.subs[id] {
		select {
		case c <- struct{}{}:
		default: // a wakeup is already pending
		}
	}
}

// Watch returns a channel that receives after each log flush or state change
// of job id, and a stop function.
func (s *System) Watch(id int64) (<-chan struct{}, func()) {
	c := make(chan struct{}, 1)
	s.hub.mu.Lock()
	if s.hub.subs[id] == nil {
		s.hub.subs[id] = map[chan struct{}]struct{}{}
	}
	s.hub.subs[id][c] = struct{}{}
	s.hub.mu.Unlock()
	return c, func() {
		s.hub.mu.Lock()
		defer s.hub.mu.Unlock()
		delete(s.hub.subs[id], c)
		if len(s.hub.subs[id]) == 0 {
			delete(s.hub.subs, id)
		}
	}
}
