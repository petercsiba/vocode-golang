package audioio

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"github.com/gorilla/websocket"
	"github.com/petrzlen/vocode-golang/pkg/audio_utils"
	"github.com/rs/zerolog/log"
	"net/http"
	"os"
	"runtime/debug"
)

var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool {
		return true // Adjust the origin check as needed
	},
}

// TwilioWebsocketMessage is a base struct for all Websocket events with Twilio.
// Also used as TwilioConnectedMessage:
// The first message sent once a WebSocket connection is established is the Connected event.
// This message describes the protocol to expect in the following messages.
type TwilioWebsocketMessage struct {
	Event          string `json:"event"`
	SequenceNumber string `json:"sequenceNumber"`
	// Other fields based on the message type
}

// TwilioStartMessage contains important metadata about the stream and is sent immediately after the Connected message.
// It is only sent once at the start of the Stream.
// Example:
//
//	{
//	 "event": "start",
//	 "sequenceNumber": "1",
//	 "start": {
//	   "accountSid": "ACa9051c185ce5367cfeabc4e1915038f3",
//	   "streamSid": "MZ863b44f4a82195cf458ba745d43438d6",
//	   "callSid": "CAc9a9dea7c7b17cdb88ce6f0e0532625c",
//	   "tracks": [
//	     "inbound"
//	   ],
//	   "mediaFormat": {
//	     "encoding": "audio/x-mulaw",
//	     "sampleRate": 8000,
//	     "channels": 1
//	   }
//	 },
//	 "streamSid": "MZ863b44f4a82195cf458ba745d43438d6"
//	}
type TwilioStartMessage struct {
	TwilioWebsocketMessage
	Start struct {
		StreamSid        string            `json:"streamSid"`
		AccountSid       string            `json:"accountSid"`
		CallSid          string            `json:"callSid"`
		Tracks           []string          `json:"tracks"`
		CustomParameters map[string]string `json:"customParameters"`
		MediaFormat      struct {
			Encoding   string `json:"encoding"`
			SampleRate int    `json:"sampleRate"`
			Channels   int    `json:"channels"`
		} `json:"mediaFormat"`
	} `json:"start"`
}

func handleStartMessage(msg TwilioStartMessage) {
	// process the start message
	// store stream metadata for future use
}

type TwilioMediaMessage struct {
	TwilioWebsocketMessage
	Media struct {
		Track     string `json:"track"`
		Chunk     string `json:"chunk"`
		Timestamp string `json:"timestamp"`
		// This is base64 encoded audio/x-mulaw - which is a form of audio compression commonly used in telephony.
		Payload string `json:"payload"`
	} `json:"media"`
}

var allMulawAudioBytes []byte = make([]byte, 0)

func handleMediaMessage(mediaMessage TwilioMediaMessage) {
	// https://en.wikipedia.org/wiki/%CE%9C-law_algorithm
	mulawAudioData, err := base64.StdEncoding.DecodeString(mediaMessage.Media.Payload)
	if err != nil {
		log.Error().Err(err).Msg("Failed to decode base64 audio data")
		return
	}
	allMulawAudioBytes = append(allMulawAudioBytes, mulawAudioData...)
}

type TwilioStopMessage struct {
	TwilioWebsocketMessage
	Stop struct {
		AccountSid string `json:"accountSid"`
		CallSid    string `json:"callSid"`
	} `json:"stop"`
}

func handleStopMessage(msg TwilioStopMessage) {
	// handle the stop event, maybe clean up resources
}

type TwilioMarkMessage struct {
	TwilioWebsocketMessage
	Mark struct {
		Name string `json:"name"`
	} `json:"mark"`
}

func handleMarkMessage(msg TwilioMarkMessage) {
	// process the mark message
}

func handleConnections(w http.ResponseWriter, r *http.Request) {
	ws, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		// Handle error
	}
	defer ws.Close()

	// Here, you'll interact with the Twilio audio streams
	for {
		_, msg, err := ws.ReadMessage()
		if err != nil {
			if websocket.IsCloseError(err, websocket.CloseNormalClosure, websocket.CloseNoStatusReceived) {
				log.Info().Msg("WebSocket connection closed normally")
			} else {
				log.Error().Err(err).Msgf("couldn't read mefdssage from websocket: %s", string(msg))
			}
			// Usually, nothing good will happen ever after a bad websocket message
			break
		}

		var message TwilioWebsocketMessage
		err = json.Unmarshal(msg, &message)
		if err != nil {
			// Maybe I just wrongfully implemented, or they changed the API
			log.Error().Err(err).Msgf("couldn't decode message from websocket: %s", string(msg))
			continue
		}

		log.Debug().Msgf("received message: %s", string(msg))

		switch message.Event {
		case "connected":
			// handle connected event
		case "start":
			var startMessage TwilioStartMessage
			errLog(json.Unmarshal(msg, &startMessage), "json.Unmarshal startMessage")
			handleStartMessage(startMessage)
		case "media":
			var mediaMessage TwilioMediaMessage
			errLog(json.Unmarshal(msg, &mediaMessage), "json.Unmarshal mediaMessage")
			handleMediaMessage(mediaMessage)
		case "stop":
			var stopMessage TwilioStopMessage
			errLog(json.Unmarshal(msg, &stopMessage), "json.Unmarshal stopMessage")
			handleStopMessage(stopMessage)
			break
		case "mark":
			var markMessage TwilioMarkMessage
			errLog(json.Unmarshal(msg, &markMessage), "json.Unmarshal markMessage")
			handleMarkMessage(markMessage)
		default:
			log.Error().Err(fmt.Errorf("unknown message.Event %s", message.Event)).Msg("")
		}
	}

	// https://github.com/go-audio/wav/issues/29
	// https://stackoverflow.com/questions/59767373/convert-8khz-mulaw-to-16khz-pcm-in-real-time
	wavAudioBytes, err := audio_utils.ConvertOneByteMulawSamplesToWav(allMulawAudioBytes, 8000, 16000)
	dbg(err)

	log.Info().Msgf("websocket finished, gonna write %d bytes", len(wavAudioBytes))
	dbg(os.WriteFile("output/entire-phone-recording.wav", wavAudioBytes, 0644))
}

//type twilioHandler struct {
//}

func NewTwilioHandler() {
	http.HandleFunc("/ws", handleConnections)
	ftl(http.ListenAndServe(":8081", nil))
}

func errLog(err error, what string) {
	if err != nil {
		log.Error().Err(err).Msg(what)
		debug.PrintStack()
	}
}

func ftl(err error) {
	if err != nil {
		log.Fatal().Err(err).Msg("sth essential failed")
		debug.PrintStack()
	}
}
