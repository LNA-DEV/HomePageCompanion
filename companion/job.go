package companion

import (
	"context"
	"encoding/json"
	"time"
)

// Job is background work the core runs: on a schedule, or by hand through
// "run now". The core never runs the same job twice at once, recovers a
// panic and cancels the context after Timeout.
type Job struct {
	Name string
	// Schedule is a cron spec with seconds ("0 0 18 * * *"); empty means
	// manual runs only.
	Schedule string
	Timeout  time.Duration // 0 = one hour
	Run      func(ctx context.Context) error
	// RunModule is used for module-level jobs (Info.Jobs) instead of Run.
	RunModule func(ctx context.Context, mc ModuleContext) error
}

// Event is something that happened in one building block, delivered to the
// others that subscribed. Data is the JSON of the event's payload type.
type Event struct {
	ID     string          `json:"id"`
	Type   string          `json:"type"`   // "social.post.published/v1"
	Source string          `json:"source"` // emitting building block
	Time   time.Time       `json:"time"`
	Data   json.RawMessage `json:"data"`
}

// Decode unmarshals the payload into v.
func (e Event) Decode(v any) error { return json.Unmarshal(e.Data, v) }
