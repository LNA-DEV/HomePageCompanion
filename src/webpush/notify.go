package webpush

import (
	"encoding/json"
	"log"

	"github.com/LNA-DEV/HomePageCompanion/config"
	"github.com/LNA-DEV/HomePageCompanion/database"
	"github.com/LNA-DEV/HomePageCompanion/models"
	"github.com/SherClockHolmes/webpush-go"
)

func BroadcastNotification(message models.Notification) {
    var subscriptions []models.NotificationSubscription

    // Load all subscriptions
    if err := database.Db.Find(&subscriptions).Error; err != nil {
        log.Printf("Error loading subscriptions: %v", err)
        return
    }

    log.Printf("Sending notifications to %d users", len(subscriptions))

    // Send to each
    for _, sub := range subscriptions {
        if err := SendNotification(sub, message); err != nil {
            log.Printf("Failed to send to user %s: %v", sub.ID, err)
        } else {
            log.Printf("Notification sent to user %s", sub.ID)
        }
    }
}

func SendNotification(subscription models.NotificationSubscription, message models.Notification) error {
	sub := webpush.Subscription{
		Endpoint: subscription.Endpoint,
		Keys: webpush.Keys{
			Auth:   subscription.Auth,
			P256dh: subscription.P256dh,
		},
	}

	// Convert the message to JSON
	payload, err := json.Marshal(message)
	if err != nil {
		return err
	}

	// Send the notification with the payload
	resp, err := webpush.SendNotification(payload, &sub, &webpush.Options{
		Subscriber:      config.Data.Webpush.Subscriber,
		VAPIDPrivateKey: VapidKey.PrivateKey,
		VAPIDPublicKey:  VapidKey.PublicKey,
		TTL:             30,
	})
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	return nil
}
