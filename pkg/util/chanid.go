package util

import "fmt"

// Channel ID builders. Follow the convention from the design doc:
//   world                               World channel (global unique)
//   alliance:{allianceId}               Alliance channel
//   activity:{activityType}:{groupId}   Activity group channel
//   private:{min(uid1,uid2)}:{max(...)} Private chat (uids sorted)
//   system:global                       System notification

func WorldChannelID() string {
	return "world"
}

func AllianceChannelID(allianceID int64) string {
	return fmt.Sprintf("alliance:%d", allianceID)
}

func ActivityChannelID(activityType string, groupID int64) string {
	return fmt.Sprintf("activity:%s:%d", activityType, groupID)
}

func PrivateChannelID(uid1, uid2 int64) string {
	if uid1 > uid2 {
		uid1, uid2 = uid2, uid1
	}
	return fmt.Sprintf("private:%d:%d", uid1, uid2)
}

func SystemChannelID() string {
	return "system:global"
}
