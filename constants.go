package main

// List of supported event types
var supportedEventTypes = []string{
	// Messages and Communication
	"Message",
	"UndecryptableMessage",
	"Receipt",
	"MediaRetry",
	"ReadReceipt",

	// Groups and Contacts
	"GroupInfo",
	"JoinedGroup",
	"Picture",
	"BlocklistChange",
	"Blocklist",

	// Connection and Session
	"Connected",
	"Disconnected",
	"ConnectFailure",
	"KeepAliveRestored",
	"KeepAliveTimeout",
	"LoggedOut",
	"ClientOutdated",
	"TemporaryBan",
	"StreamError",
	"StreamReplaced",
	"PairSuccess",
	"PairError",
	"QR",
	"QRCode",
	"QRTimeout", 
	"QRSuccess",
	"QRScannedWithoutMultidevice",

	// Privacy and Settings
	"PrivacySettings",
	"PushNameSetting",
	"UserAbout",

	// Synchronization and State
	"AppState",
	"AppStateSyncComplete",
	"HistorySync",
	"OfflineSyncCompleted",
	"OfflineSyncPreview",

	// Calls
	"CallOffer",
	"CallAccept",
	"CallTerminate",
	"CallOfferNotice",
	"CallRelayLatency",

	// Presence and Activity
	"Presence",
	"ChatPresence",

	// Identity
	"IdentityChange",

	// Erros
	"CATRefreshError",

	// Newsletter (WhatsApp Channels)
	"NewsletterJoin",
	"NewsletterLeave",
	"NewsletterMuteChange",
	"NewsletterLiveUpdate",

	// Facebook/Meta Bridge
	"FBMessage",

	// Special - receives all events
	"All",
}

// Map for quick validation
var eventTypeMap map[string]bool

func init() {
	eventTypeMap = make(map[string]bool)
	for _, eventType := range supportedEventTypes {
		eventTypeMap[eventType] = true
	}
}

// Auxiliary function to validate event type
func isValidEventType(eventType string) bool {
	return eventTypeMap[eventType]
}

// S3 Environment Variables Constants
const (
	// Global S3 credentials (read from environment)
	S3_GLOBAL_ACCESS_KEY = "S3_ACCESS_KEY"
	S3_GLOBAL_SECRET_KEY = "S3_SECRET_KEY"
	S3_GLOBAL_ENDPOINT   = "S3_ENDPOINT"
	S3_GLOBAL_REGION     = "S3_REGION"
)
