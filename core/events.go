package core

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/LNA-DEV/HomePageCompanion/companion"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// eventRow is an event in the outbox.
type eventRow struct {
	ID        string `gorm:"primaryKey"`
	Type      string `gorm:"index"`
	Source    string
	Data      string
	CreatedAt time.Time `gorm:"index"`
}

func (eventRow) TableName() string { return "core_events" }

// deliveryRow is one event for one subscriber. Status: pending, done,
// failed (given up).
type deliveryRow struct {
	ID         uint   `gorm:"primaryKey"`
	EventID    string `gorm:"uniqueIndex:idx_delivery"`
	Subscriber string `gorm:"uniqueIndex:idx_delivery"` // a building block ("autoupload") or an instance ("autoupload:blog")
	Status     string `gorm:"index"`
	Attempts   int
	NextAt     time.Time `gorm:"index"`
	LastError  string
	UpdatedAt  time.Time
}

func (deliveryRow) TableName() string { return "core_deliveries" }

// retryGaps are the waits after the 1st, 2nd … failed delivery; after the
// last one the delivery is given up.
var retryGaps = []time.Duration{10 * time.Second, time.Minute, 5 * time.Minute, 30 * time.Minute, 2 * time.Hour, 12 * time.Hour}

// bus delivers events at least once: Emit writes the event and one delivery
// per subscriber in one transaction; a worker delivers and retries.
type bus struct {
	c      *Core
	mu     sync.Mutex // one delivery pass at a time
	wake   chan struct{}
	stopCh chan struct{}
	wg     sync.WaitGroup
	// now is replaceable in tests.
	now func() time.Time
}

func newBus(c *Core) *bus {
	return &bus{c: c, wake: make(chan struct{}, 1), now: time.Now}
}

func newEventID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// subscribers of an event type: building blocks with OnEvent, instances
// with HandleEvent, services granted the event.
func (b *bus) subscribers(eventType string) []string {
	b.c.mu.RLock()
	defer b.c.mu.RUnlock()
	var out []string
	for id, blk := range b.c.blocks {
		if !subscribes(blk.info, eventType) {
			continue
		}
		if blk.info.OnEvent != nil {
			out = append(out, id)
		}
		for _, in := range b.c.instances {
			if in.block != id {
				continue
			}
			if _, ok := in.value.(companion.EventHandler); ok && in.service == nil {
				out = append(out, in.id)
			}
		}
	}
	for _, in := range b.c.instances {
		if in.service != nil && in.service.granted(eventType) {
			out = append(out, in.id)
		}
	}
	sort.Strings(out)
	return out
}

func subscribes(info companion.Info, eventType string) bool {
	for _, t := range info.Subscribes {
		if t == eventType {
			return true
		}
	}
	return false
}

func (b *bus) emit(ctx context.Context, source, eventType string, data any) error {
	if b.c.db == nil {
		return fmt.Errorf("events: no database yet")
	}
	payload, err := json.Marshal(data)
	if err != nil {
		return fmt.Errorf("events: %s: %w", eventType, err)
	}
	ev := eventRow{ID: newEventID(), Type: eventType, Source: source, Data: string(payload), CreatedAt: b.now()}
	subs := b.subscribers(eventType)
	err = b.c.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(&ev).Error; err != nil {
			return err
		}
		for _, s := range subs {
			if err := tx.Create(&deliveryRow{EventID: ev.ID, Subscriber: s, Status: "pending", NextAt: ev.CreatedAt}).Error; err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	b.c.metrics.add("companion_events_emitted_total", 1, "type", eventType)
	b.poke()
	return nil
}

func (b *bus) poke() {
	select {
	case b.wake <- struct{}{}:
	default:
	}
}

func (b *bus) start() {
	// Deliveries a crash interrupted are delivered again.
	if b.c.db != nil {
		b.c.db.Model(&deliveryRow{}).Where("status = ?", "delivering").Update("status", "pending")
	}
	b.stopCh = make(chan struct{})
	b.wg.Add(1)
	go func() {
		defer b.wg.Done()
		t := time.NewTicker(5 * time.Second)
		defer t.Stop()
		for {
			b.deliverDue()
			select {
			case <-b.stopCh:
				return
			case <-b.wake:
			case <-t.C:
			}
		}
	}()
}

func (b *bus) stop() {
	if b.stopCh != nil {
		close(b.stopCh)
		b.wg.Wait()
		b.stopCh = nil
	}
}

// deliverDue delivers every pending delivery whose time has come, oldest
// first.
func (b *bus) deliverDue() {
	if b.c.db == nil {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	for {
		var due []deliveryRow
		if err := b.c.db.Where("status = ? AND next_at <= ?", "pending", b.now()).Order("id").Limit(50).Find(&due).Error; err != nil {
			b.c.log.Printf("events: %v", err)
			return
		}
		if len(due) == 0 {
			return
		}
		for _, d := range due {
			b.deliver(d)
		}
		if len(due) < 50 {
			return
		}
	}
}

// DeliverNow delivers every due event synchronously; for tests.
func (c *Core) DeliverNow() { c.events.deliverDue() }

func (b *bus) deliver(d deliveryRow) {
	// Claim the delivery, so another process working on the same database
	// does not deliver it too.
	claim := b.c.db.Model(&deliveryRow{}).Where("id = ? AND status = ? AND attempts = ?", d.ID, "pending", d.Attempts).
		Update("status", "delivering")
	if claim.Error != nil || claim.RowsAffected == 0 {
		return
	}
	var ev eventRow
	if err := b.c.db.First(&ev, "id = ?", d.EventID).Error; err != nil {
		b.c.db.Model(&deliveryRow{}).Where("id = ?", d.ID).Updates(map[string]any{"status": "failed", "last_error": "event missing"})
		return
	}
	e := companion.Event{ID: ev.ID, Type: ev.Type, Source: ev.Source, Time: ev.CreatedAt, Data: json.RawMessage(ev.Data)}
	err := b.handle(d.Subscriber, e)
	if err == nil {
		b.c.db.Model(&deliveryRow{}).Where("id = ?", d.ID).Updates(map[string]any{"status": "done", "attempts": d.Attempts + 1, "last_error": ""})
		b.c.metrics.add("companion_events_delivered_total", 1, "type", ev.Type, "subscriber", d.Subscriber)
		return
	}
	attempts := d.Attempts + 1
	upd := map[string]any{"status": "pending", "attempts": attempts, "last_error": truncate(err.Error(), 2000)}
	if attempts > len(retryGaps) {
		upd["status"] = "failed"
		b.c.log.Printf("events: giving up %s for %s after %d attempts: %v", ev.Type, d.Subscriber, attempts, err)
		b.c.metrics.add("companion_events_failed_total", 1, "type", ev.Type, "subscriber", d.Subscriber)
	} else {
		upd["next_at"] = b.now().Add(retryGaps[attempts-1])
		b.c.metrics.add("companion_events_retried_total", 1, "type", ev.Type, "subscriber", d.Subscriber)
	}
	b.c.db.Model(&deliveryRow{}).Where("id = ?", d.ID).Updates(upd)
}

// errNoSubscriber is returned for a subscriber that no longer exists; the
// delivery is retried in case a reload brings it back.
var errNoSubscriber = fmt.Errorf("subscriber not running")

func (b *bus) handle(subscriber string, e companion.Event) error {
	b.c.mu.RLock()
	blk := b.c.blocks[subscriber]
	in := b.c.instances[subscriber]
	b.c.mu.RUnlock()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	switch {
	case blk != nil && blk.info.OnEvent != nil:
		return safely(func() error { return blk.info.OnEvent(blk.mc, e) })
	case in != nil && in.running && in.service != nil:
		return in.service.deliver(ctx, e)
	case in != nil && in.running:
		h, ok := in.value.(companion.EventHandler)
		if !ok {
			return errNoSubscriber
		}
		return safely(func() error { return h.HandleEvent(ctx, e) })
	}
	return errNoSubscriber
}

// eventStats counts deliveries by status, for the dashboard and /metrics.
func (b *bus) stats() map[string]int64 {
	out := map[string]int64{"pending": 0, "done": 0, "failed": 0}
	if b.c.db == nil {
		return out
	}
	type row struct {
		Status string
		N      int64
	}
	var rows []row
	b.c.db.Model(&deliveryRow{}).Select("status, count(*) as n").Group("status").Scan(&rows)
	for _, r := range rows {
		out[r.Status] = r.N
	}
	return out
}

// retryFailed puts given-up deliveries back in the queue.
func (b *bus) retryFailed() int64 {
	res := b.c.db.Model(&deliveryRow{}).Where("status = ?", "failed").
		Updates(map[string]any{"status": "pending", "attempts": 0, "next_at": b.now()})
	b.poke()
	return res.RowsAffected
}

// prune drops delivered events older than a week.
func (b *bus) prune() error {
	cutoff := b.now().Add(-7 * 24 * time.Hour)
	var old []string
	b.c.db.Model(&eventRow{}).Where("created_at < ?", cutoff).
		Where("NOT EXISTS (SELECT 1 FROM core_deliveries d WHERE d.event_id = core_events.id AND d.status = 'pending')").
		Pluck("id", &old)
	for len(old) > 0 {
		n := min(len(old), 500)
		batch := old[:n]
		old = old[n:]
		if err := b.c.db.Clauses(clause.Where{}).Where("event_id IN ?", batch).Delete(&deliveryRow{}).Error; err != nil {
			return err
		}
		if err := b.c.db.Where("id IN ?", batch).Delete(&eventRow{}).Error; err != nil {
			return err
		}
	}
	return nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
