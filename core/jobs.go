package core

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/LNA-DEV/HomePageCompanion/companion"
	"github.com/robfig/cron"
)

// jobRun is one run of a job, kept for the UI and "run now" results.
type jobRun struct {
	ID         uint       `gorm:"primaryKey" json:"id"`
	Job        string     `gorm:"index" json:"job"` // "<owner>/<name>"
	Trigger    string     `json:"trigger"`          // schedule | manual
	StartedAt  time.Time  `gorm:"index" json:"startedAt"`
	FinishedAt *time.Time `json:"finishedAt,omitempty"`
	OK         bool       `json:"ok"`
	Error      string     `json:"error,omitempty"`
}

func (jobRun) TableName() string { return "core_job_runs" }

// ErrJobRunning is returned by RunNow while the job runs.
var ErrJobRunning = errors.New("job is already running")

type jobEntry struct {
	key     string // "<owner>/<name>"
	owner   string // instance ID or building block ID
	job     companion.Job
	mc      companion.ModuleContext
	running atomic.Bool
	lastRun atomic.Pointer[jobRun]
}

// scheduler runs jobs on their schedule and by hand. robfig/cron v1 has a
// seconds field and cannot remove entries, so the cron runs one tick per
// entry key and looks the job up when it fires.
type scheduler struct {
	c       *Core
	mu      sync.Mutex
	entries map[string]*jobEntry
	cron    *cron.Cron
	wg      sync.WaitGroup
	gen     int
}

func newScheduler(c *Core) *scheduler {
	return &scheduler{c: c, entries: map[string]*jobEntry{}}
}

func (s *scheduler) add(owner string, j companion.Job) {
	s.put(&jobEntry{key: owner + "/" + j.Name, owner: owner, job: j})
}

func (s *scheduler) addModule(owner string, j companion.Job, mc companion.ModuleContext) {
	s.put(&jobEntry{key: owner + "/" + j.Name, owner: owner, job: j, mc: mc})
}

func (s *scheduler) put(e *jobEntry) {
	if e.job.Schedule != "" {
		if _, err := cron.Parse(e.job.Schedule); err != nil {
			s.c.log.Printf("jobs: %s: invalid schedule %q: %v", e.key, e.job.Schedule, err)
			e.job.Schedule = ""
		}
	}
	s.mu.Lock()
	s.entries[e.key] = e
	started := s.cron != nil
	s.mu.Unlock()
	if started {
		s.reschedule()
	}
}

func (s *scheduler) removeOwner(owner string) {
	s.mu.Lock()
	changed := false
	for k, e := range s.entries {
		if e.owner == owner {
			delete(s.entries, k)
			changed = true
		}
	}
	started := s.cron != nil
	s.mu.Unlock()
	if changed && started {
		s.reschedule()
	}
}

func (s *scheduler) start() { s.reschedule() }

// reschedule replaces the cron with one holding the current entries.
func (s *scheduler) reschedule() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cron != nil {
		s.cron.Stop()
	}
	s.gen++
	s.cron = cron.New()
	for _, e := range s.entries {
		if e.job.Schedule == "" {
			continue
		}
		key := e.key
		_ = s.cron.AddFunc(e.job.Schedule, func() { s.fire(key, "schedule") })
	}
	s.cron.Start()
}

func (s *scheduler) stop() {
	s.mu.Lock()
	if s.cron != nil {
		s.cron.Stop()
		s.cron = nil
	}
	s.mu.Unlock()
	s.wg.Wait()
}

func (s *scheduler) fire(key, trigger string) {
	if _, err := s.run(key, trigger, false); err != nil && !errors.Is(err, ErrJobRunning) {
		s.c.log.Printf("jobs: %s: %v", key, err)
	}
}

// run starts a run. With wait, it returns once the run finished.
func (s *scheduler) run(key, trigger string, wait bool) (*jobRun, error) {
	s.mu.Lock()
	e := s.entries[key]
	s.mu.Unlock()
	if e == nil {
		return nil, fmt.Errorf("no job %s", key)
	}
	if !e.running.CompareAndSwap(false, true) {
		return nil, ErrJobRunning
	}
	r := &jobRun{Job: key, Trigger: trigger, StartedAt: time.Now()}
	if s.c.db != nil {
		s.c.db.Create(r)
	}
	done := make(chan struct{})
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		defer close(done)
		defer e.running.Store(false)
		timeout := e.job.Timeout
		if timeout <= 0 {
			timeout = time.Hour
		}
		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		defer cancel()
		err := safely(func() error {
			switch {
			case e.job.RunModule != nil:
				return e.job.RunModule(ctx, e.mc)
			case e.job.Run != nil:
				return e.job.Run(ctx)
			}
			return errors.New("job has no Run")
		})
		finished := time.Now()
		r.FinishedAt, r.OK = &finished, err == nil
		if err != nil {
			r.Error = truncate(err.Error(), 4000)
			s.c.log.Printf("jobs: %s failed: %v", key, err)
		}
		if s.c.db != nil {
			s.c.db.Save(r)
		}
		e.lastRun.Store(r)
		result := "ok"
		if err != nil {
			result = "error"
		}
		s.c.metrics.add("companion_job_runs_total", 1, "job", key, "result", result)
		s.c.metrics.observe("companion_job_duration_seconds", finished.Sub(r.StartedAt).Seconds(), "job", key)
	}()
	if wait {
		<-done
	}
	return r, nil
}

// JobInfo describes a job for the UI.
type JobInfo struct {
	Key      string    `json:"key"`
	Owner    string    `json:"owner"`
	Name     string    `json:"name"`
	Schedule string    `json:"schedule,omitempty"`
	Next     time.Time `json:"next,omitempty"`
	Running  bool      `json:"running"`
	LastRun  *jobRun   `json:"lastRun,omitempty"`
}

func (s *scheduler) list() []JobInfo {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []JobInfo
	for _, e := range s.entries {
		ji := JobInfo{Key: e.key, Owner: e.owner, Name: e.job.Name, Schedule: e.job.Schedule, Running: e.running.Load()}
		if e.job.Schedule != "" {
			if sched, err := cron.Parse(e.job.Schedule); err == nil {
				ji.Next = sched.Next(time.Now())
			}
		}
		if r := e.lastRun.Load(); r != nil {
			ji.LastRun = r
		} else if s.c.db != nil {
			var last jobRun
			if s.c.db.Where("job = ?", e.key).Order("id desc").Limit(1).Find(&last).RowsAffected > 0 {
				ji.LastRun = &last
			}
		}
		out = append(out, ji)
	}
	sort.Slice(out, func(a, b int) bool { return out[a].Key < out[b].Key })
	return out
}

// RunJob runs a job and waits for it; for tests and tools.
func (c *Core) RunJob(key string) error {
	r, err := c.jobs.run(key, "manual", true)
	if err != nil {
		return err
	}
	if !r.OK {
		return errors.New(r.Error)
	}
	return nil
}
