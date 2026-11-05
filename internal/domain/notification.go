package domain

type IncidentDelivery struct {
	DestinationID string
	LeaseID       string
	Attempts      int
	Event         IncidentEvent
}
type NotificationStatus struct {
	Configured bool `json:"configured"`
	Pending    int  `json:"pending"`
	Failed     int  `json:"failed"`
}
