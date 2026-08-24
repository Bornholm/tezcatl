package window

import "github.com/bornholm/tezcatl/internal/core/model"

// Ring is a fixed-capacity buffer keeping the most recent observations
// of a partition, used to attach the surrounding context to detected
// anomalies. It is not safe for concurrent use: each partition owns its
// own ring and partitions are processed sequentially.
type Ring struct {
	buf  []model.Observation
	next int
	full bool
}

func NewRing(capacity int) *Ring {
	return &Ring{
		buf: make([]model.Observation, max(1, capacity)),
	}
}

func (r *Ring) Add(obs model.Observation) {
	r.buf[r.next] = obs
	r.next = (r.next + 1) % len(r.buf)

	if r.next == 0 {
		r.full = true
	}
}

// Last returns up to n observations, from oldest to newest.
func (r *Ring) Last(n int) []model.Observation {
	size := r.next
	if r.full {
		size = len(r.buf)
	}

	if n > size {
		n = size
	}

	if n <= 0 {
		return nil
	}

	last := make([]model.Observation, 0, n)
	for i := range n {
		last = append(last, r.buf[(r.next-n+i+len(r.buf))%len(r.buf)])
	}

	return last
}
