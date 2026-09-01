package server

import (
	"sync"
	"time"
)

// explainJob is one generation in flight or finished. Generating an
// explanation takes as long as the model takes, which is regularly
// more than the sixty seconds a reverse proxy gives an upstream, so
// the work outlives the request that started it and the browser polls
// for the result.
type explainJob struct {
	Done     bool
	Text     string
	Err      string
	Started  time.Time
	Finished time.Time
}

// explainJobs holds the generations by incident. A job is kept after
// it finishes so a page reload finds the answer instead of asking the
// model again; retention is short because an explanation is a reading,
// not a record.
type explainJobs struct {
	mutex     sync.Mutex
	retention time.Duration
	jobs      map[string]*explainJob
}

func newExplainJobs(retention time.Duration) *explainJobs {
	return &explainJobs{
		retention: retention,
		jobs:      map[string]*explainJob{},
	}
}

// start registers a job for the incident and reports whether the
// caller must run it. A generation already in flight is never doubled:
// a second Explain click joins the first one.
func (e *explainJobs) start(incidentID string) bool {
	e.mutex.Lock()
	defer e.mutex.Unlock()

	e.prune()

	if job, exists := e.jobs[incidentID]; exists && !job.Done {
		return false
	}

	e.jobs[incidentID] = &explainJob{Started: time.Now()}

	return true
}

func (e *explainJobs) finish(incidentID string, text string, errMessage string) {
	e.mutex.Lock()
	defer e.mutex.Unlock()

	job, exists := e.jobs[incidentID]
	if !exists {
		// Dismissed while the model was answering: the reader moved on.
		return
	}

	job.Done = true
	job.Text = text
	job.Err = errMessage
	job.Finished = time.Now()
}

// get returns a copy of the job, if any.
func (e *explainJobs) get(incidentID string) (explainJob, bool) {
	e.mutex.Lock()
	defer e.mutex.Unlock()

	e.prune()

	job, exists := e.jobs[incidentID]
	if !exists {
		return explainJob{}, false
	}

	return *job, true
}

func (e *explainJobs) forget(incidentID string) {
	e.mutex.Lock()
	defer e.mutex.Unlock()

	delete(e.jobs, incidentID)
}

// prune drops the finished jobs past the retention, and the ones a
// crash left running. Called with the lock held.
func (e *explainJobs) prune() {
	now := time.Now()

	for id, job := range e.jobs {
		switch {
		case job.Done && now.Sub(job.Finished) > e.retention:
			delete(e.jobs, id)
		case !job.Done && now.Sub(job.Started) > e.retention+explainTimeout:
			delete(e.jobs, id)
		}
	}
}
