package audioio

import (
	"fmt"
	"regexp"
)

// TwilioMessage is a base struct for all Websocket events with Twilio.
type TwilioMessage struct {
	// Event either of "connected", "start", "media", "stop", "mark" or "clear"
	Event          string `json:"event"`
	SequenceNumber string `json:"sequenceNumber"`
	StreamSid      string `json:"streamSid,omitempty"`

	Start *TwilioStartPayload `json:"start,omitempty"`
	Media *TwilioMediaPayload `json:"media,omitempty"`
	Stop  *TwilioStopPayload  `json:"stop,omitempty"`
	Mark  *TwilioMarkPayload  `json:"mark,omitempty"`
	// No extra payload for event = "clear"

	// Payload for event = "connected"
	Protocol *string `json:"protocol,omitempty"`
	Version  *string `json:"version,omitempty"`
}

// TwilioStartPayload contains important metadata about the stream and is sent immediately after the Connected message.
type TwilioStartPayload struct {
	StreamSid        string            `json:"streamSid"`
	AccountSid       string            `json:"accountSid"`
	CallSid          string            `json:"callSid"`
	Tracks           []string          `json:"tracks"`
	CustomParameters map[string]string `json:"customParameters"`
	MediaFormat      TwilioMediaFormat `json:"mediaFormat"`
}

type TwilioMediaFormat struct {
	Encoding   string `json:"encoding"`
	SampleRate int    `json:"sampleRate"`
	Channels   int    `json:"channels"`
}

// TwilioMediaPayload https://www.twilio.com/docs/voice/twiml/stream#message-media
type TwilioMediaPayload struct {
	// One of inbound or outbound
	Track string `json:"track"`
	// The chunk for the message. The first message will begin with "1" and increment with each subsequent message.
	Chunk string `json:"chunk"`
	// Presentation Timestamp in Milliseconds from the start of the stream.
	Timestamp string `json:"timestamp"`
	// This is base64 encoded audio/x-mulaw - which is a form of audio compression commonly used in telephony.
	Payload string `json:"payload"`
}

// TwilioStopPayload https://www.twilio.com/docs/voice/twiml/stream#message-stop
type TwilioStopPayload struct {
	AccountSid string `json:"accountSid"`
	CallSid    string `json:"callSid"`
}

// TwilioMarkPayload https://www.twilio.com/docs/voice/twiml/stream#message-mark
type TwilioMarkPayload struct {
	Name string `json:"name"`
}

// truncatePayload shortens the "payload" field in a JSON string to the first 100 characters.
func truncatePayload(jsonStr string) string {
	re := regexp.MustCompile(`"payload":\s*"(.*?)"`)
	return re.ReplaceAllStringFunc(jsonStr, func(m string) string {
		matches := re.FindStringSubmatch(m)
		if len(matches) > 1 {
			payload := matches[1]
			if len(payload) > 100 {
				return fmt.Sprintf(`"payload": "%.100s ... (truncated)"`, payload)
			}
		}
		return m
	})
}
