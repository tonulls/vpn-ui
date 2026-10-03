package controller

import "github.com/mhsanaei/3x-ui/v2/web/service"

// DonateEntry is one operator-configured support destination. The alias keeps
// the server-rendered dashboard model concise while settings remain the source.
type DonateEntry = service.DonationEntry

func donationEntriesForDisplay(settings service.DonationSettings) []DonateEntry {
	if !settings.Enabled {
		return nil
	}
	return settings.Entries
}
