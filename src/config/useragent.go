package config

// UserAgent identifies the companion to the third-party services it calls on
// the website's behalf (tile builds, routing, GBIF). Their usage policies ask
// for an application name, a version and a contact; the contact is the site.
func UserAgent() string {
	contact := "https://github.com/LNA-DEV/HomePageCompanion"
	if Data.Security.Domain != "" {
		contact = "https://" + Data.Security.Domain
	}
	return "HomePageCompanion/1.0 (+" + contact + ")"
}
