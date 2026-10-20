package main

// curatedZones feeds the ConfigFields enum, which the management UI renders as a dropdown.
// Any other valid IANA name written by hand in the YAML is accepted too.
var curatedZones = []string{
	"UTC",
	"Africa/Cairo", "Africa/Johannesburg", "Africa/Lagos", "Africa/Nairobi",
	"America/Anchorage", "America/Argentina/Buenos_Aires", "America/Bogota", "America/Chicago",
	"America/Denver", "America/Halifax", "America/Los_Angeles", "America/Mexico_City",
	"America/New_York", "America/Phoenix", "America/Sao_Paulo", "America/Toronto", "America/Vancouver",
	"Asia/Bangkok", "Asia/Dhaka", "Asia/Dubai", "Asia/Hong_Kong", "Asia/Jakarta", "Asia/Jerusalem",
	"Asia/Karachi", "Asia/Kolkata", "Asia/Kuala_Lumpur", "Asia/Manila", "Asia/Seoul", "Asia/Shanghai",
	"Asia/Singapore", "Asia/Taipei", "Asia/Tehran", "Asia/Tokyo",
	"Atlantic/Reykjavik",
	"Australia/Adelaide", "Australia/Brisbane", "Australia/Perth", "Australia/Sydney",
	"Europe/Amsterdam", "Europe/Berlin", "Europe/Istanbul", "Europe/London", "Europe/Madrid",
	"Europe/Moscow", "Europe/Paris", "Europe/Rome", "Europe/Zurich",
	"Pacific/Auckland", "Pacific/Honolulu",
}
