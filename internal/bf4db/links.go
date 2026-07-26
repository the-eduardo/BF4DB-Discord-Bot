package bf4db

import (
	"fmt"
	"time"
)

// ProfileURL is the player's BF4DB page.
func ProfileURL(personaID int) string {
	return fmt.Sprintf("https://bf4db.com/player/%d/", personaID)
}

// CheatReportURL is a bf4cheatreport.com query anchored at now.
func CheatReportURL(personaID int, now time.Time) string {
	return fmt.Sprintf("https://bf4cheatreport.com/?pid=%d&uid=&cnt=200&startdate=%s",
		personaID, now.Format("200601021504"))
}

// AgencyURL is the battlefield.agency profile for the persona.
func AgencyURL(personaID int) string {
	return fmt.Sprintf("https://battlefield.agency/player/by-persona_id/bf4/%d", personaID)
}
