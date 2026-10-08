package billing

import "time"

func catalogGroupAllowed(userGroupID *string, allowed []string) bool {
	if userGroupID == nil {
		return false
	}
	for _, id := range allowed {
		if id == *userGroupID {
			return true
		}
	}
	return false
}

func catalogPriceCurrentlyValid(from, until *time.Time, now time.Time) bool {
	return (from == nil || !from.After(now)) && (until == nil || until.After(now))
}
