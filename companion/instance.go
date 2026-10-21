package companion

import "context"

// Instance is one configured occurrence of a building block. It is usually a
// pointer to a struct whose exported fields are its config (json tags) and
// its slots (`companion:"slot=<name>"`). Fields tagged `companion:"secret"`
// are masked in logs, the UI and config dumps.
type Instance any

// Provisioner prepares an instance: the core calls Provision once the config
// and the slots are filled. ctx stays valid for the instance's lifetime.
type Provisioner interface {
	Provision(ctx Context) error
}

// Validator checks the config after Provision. An error stops the start (on
// a reload: keeps the old instance running).
type Validator interface {
	Validate() error
}

// Starter starts background work. Start must return; long-running work
// belongs in a goroutine or, better, a job.
type Starter interface {
	Start() error
}

// Stopper stops an instance, within the context's deadline.
type Stopper interface {
	Stop(ctx context.Context) error
}

// JobProvider lists an instance's jobs, e.g. a publish job whose schedule is
// the instance's cron field.
type JobProvider interface {
	Jobs() []Job
}

// EventHandler receives the event types the building block subscribes to
// (Info.Subscribes). Delivery is at least once; Event.ID deduplicates.
type EventHandler interface {
	HandleEvent(ctx context.Context, ev Event) error
}
