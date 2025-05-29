package webpush

import (
	"log"

	"github.com/LNA-DEV/HomePageCompanion/config"
	"github.com/LNA-DEV/HomePageCompanion/database"
	"github.com/LNA-DEV/HomePageCompanion/models"
	"github.com/SherClockHolmes/webpush-go"
)

func BroadcastNotification(message string) {
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
            log.Printf("Failed to send to user %s: %v", sub.UserID, err)
        } else {
            log.Printf("Notification sent to user %s", sub.UserID)
        }
    }
}

func SendNotification(subscription models.NotificationSubscription, message string) error {
	sub := webpush.Subscription{
		Endpoint: subscription.Endpoint,
		Keys: webpush.Keys{
			Auth:   subscription.Auth,
			P256dh: subscription.P256dh,
		},
	}

	resp, err := webpush.SendNotification([]byte(message), &sub, &webpush.Options{
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
