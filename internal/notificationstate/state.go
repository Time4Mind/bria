package notificationstate

type DeliveryState string

const (
	DeliveryConfirmed DeliveryState = "confirmed"
	DeliveryFailed    DeliveryState = "failed"
	DeliveryUnknown   DeliveryState = "unknown"
)

type PartReceipt struct {
	PartID    string
	MessageID int64
}
