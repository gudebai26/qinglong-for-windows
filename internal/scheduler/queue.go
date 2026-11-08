package scheduler

import (
	"sync"
)

// queue is the Go counterpart of a p-queue instance: a FIFO of jobs with a
// concurrency ceiling. Node keeps one queue for scheduled runs and a separate
// one for manual runs, and so does the panel - a burst of manual runs must not
// starve the timers, and vice versa.
type queue struct {
	mu     sync.Mutex
	cond   *sync.Cond
	jobs   []func()
	active int
	limit  int
	closed bool
}

func newQueue(limit int) *queue {
	if limit < 1 {
		limit = 1
	}
	q := &queue{limit: limit}
	q.cond = sync.NewCond(&q.mu)
	go q.dispatch()
	return q
}

// Submit appends a job. It never blocks the caller: the trigger goroutine has
// to stay on schedule even when the queue is backed up.
func (q *queue) Submit(job func()) {
	q.mu.Lock()
	if q.closed {
		q.mu.Unlock()
		return
	}
	q.jobs = append(q.jobs, job)
	q.cond.Signal()
	q.mu.Unlock()
}

// SetLimit applies cronConcurrency at runtime, like setCustomLimit().
func (q *queue) SetLimit(limit int) {
	if limit < 1 {
		return
	}
	q.mu.Lock()
	q.limit = limit
	q.cond.Broadcast()
	q.mu.Unlock()
}

// Close drops queued jobs and stops the dispatcher. Jobs already running are
// left alone; the service ends their processes during shutdown.
func (q *queue) Close() {
	q.mu.Lock()
	q.closed = true
	q.jobs = nil
	q.cond.Broadcast()
	q.mu.Unlock()
}

func (q *queue) dispatch() {
	for {
		q.mu.Lock()
		for !q.closed && (len(q.jobs) == 0 || q.active >= q.limit) {
			q.cond.Wait()
		}
		if q.closed {
			q.mu.Unlock()
			return
		}
		job := q.jobs[0]
		q.jobs = q.jobs[1:]
		q.active++
		q.mu.Unlock()

		go func(job func()) {
			defer q.finish()
			job()
		}(job)
	}
}

func (q *queue) finish() {
	q.mu.Lock()
	q.active--
	q.cond.Broadcast()
	q.mu.Unlock()
}
