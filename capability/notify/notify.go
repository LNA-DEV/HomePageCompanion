// Package notify defines notify/v1: telling someone that something happened.
// A building block with something worth telling offers an optional "notify"
// slot; the operator connects it, e.g. to a webpush instance.
package notify

import (
	"context"

	"github.com/LNA-DEV/HomePageCompanion/companion"
)

// Notifier delivers messages.
type Notifier interface {
	// Send delivers to one recipient of the notifier (a subscription id).
	Send(ctx context.Context, to string, message Message) error
	// Broadcast delivers to every recipient.
	Broadcast(ctx context.Context, message Message) error
}

var NotifyV1 = companion.NewCapability[Notifier]("notify", 1)

// Message is a notification.
type Message struct {
	Title string `json:"title"`
	Body  string `json:"body"`
	URL   string `json:"url,omitempty"`
	Icon  string `json:"icon,omitempty"`
}
