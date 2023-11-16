package audioio

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"github.com/petrzlen/vocode-golang/pkg/audio_utils"
	"github.com/rs/zerolog/log"
	"os"
	"runtime/debug"
	"strconv"
	"time"
)

type twilioHandler struct {
	startMessage       *TwilioStartMessage // To keep the initial config
	startTime          time.Time
	allMulawAudioBytes []byte
	readChan           chan []byte

	mediaLastSeqNum int
	writeLastSeqNum int
	writeChan       chan []byte
}

func NewTwilioHandler() *twilioHandler {
	result := &twilioHandler{
		startMessage:       nil,
		allMulawAudioBytes: make([]byte, 0),
		readChan:           make(chan []byte, 100),
		mediaLastSeqNum:    0,
		writeLastSeqNum:    0,
		writeChan:          make(chan []byte, 100),
	}
	go result.readMessagesUntilChanClosed()
	return result
}

func (th *twilioHandler) GetReader() chan<- []byte {
	return th.readChan
}

func (th *twilioHandler) GetWriter() <-chan []byte {
	return th.writeChan
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
type TwilioStartMessage struct {
	TwilioWebsocketMessage
	Start TwilioStart `json:"start"`
}

type TwilioStart struct {
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

// isInList checks if a string is present in a slice of strings.
func isInList(str string, list []string) bool {
	for _, v := range list {
		if v == str {
			return true
		}
	}
	return false
}

func (th *twilioHandler) handleStartMessage(msg TwilioStartMessage) {
	// Some validation first:fddfs
	if !isInList("inbound", msg.Start.Tracks) {
		log.Error().Msgf("'inbound' not in Start.Tracks: %v", msg.Start.Tracks)
	}
	if !isInList("outbound", msg.Start.Tracks) {
		log.Error().Msgf("'outbound' not in Start.Tracks: %v", msg.Start.Tracks)
	}
	expectedMediaFormat := TwilioMediaFormat{
		Encoding:   "audio/x-mulaw",
		SampleRate: 8000,
		Channels:   1,
	}
	if msg.Start.MediaFormat != expectedMediaFormat {
		log.Error().Msgf("unexpected media format in Start.MediaFormat: %v", msg.Start.MediaFormat)
	}

	// Real stuff
	th.startMessage = &msg
	th.startTime = time.Now()

	// As a test, we just replay the previous recording
	wavBytes, err := os.ReadFile("output/test-greeting.wav")
	errLog(err, "os.ReadFile test file")
	pcmBytes, err := audio_utils.ConvertWavToOneByteMulawSamples(wavBytes, 8000)

	errLog(err, "ConvertWavToOneByteMulawSamples test file")
	base64String := base64.StdEncoding.EncodeToString(pcmBytes)

	// Twilio: The media payload should not contain audio file type header bytes.
	// Providing header bytes will cause the media to be streamed incorrectly.
	// https://www.twilio.com/docs/voice/twiml/stream#message-media-to-twilio

	mediaMessage := TwilioMediaMessage{
		TwilioWebsocketMessage: TwilioWebsocketMessage{
			Event:          "media",
			SequenceNumber: strconv.Itoa(th.writeLastSeqNum + 1), // TODO: Have a more robust way
		},
		StreamSid: th.startMessage.Start.StreamSid,
		Media: TwilioMedia{
			Track:     "outbound",
			Chunk:     strconv.Itoa(th.mediaLastSeqNum + 1),
			Timestamp: strconv.Itoa(int(time.Since(th.startTime).Milliseconds())),
			Payload:   base64String,
		},
	}

	th.writeMessage(mediaMessage)
}

// TwilioMediaMessage https://www.twilio.com/docs/voice/twiml/stream#message-media
type TwilioMediaMessage struct {
	TwilioWebsocketMessage
	StreamSid string      `json:"streamSid"`
	Media     TwilioMedia `json:"media"`
}

type TwilioMedia struct {
	// One of inbound or outbound
	Track string `json:"track"`
	// The chunk for the message. The first message will begin with "1" and increment with each subsequent message.
	Chunk string `json:"chunk"`
	// Presentation Timestamp in Milliseconds from the start of the stream.
	Timestamp string `json:"timestamp"`
	// This is base64 encoded audio/x-mulaw - which is a form of audio compression commonly used in telephony.
	Payload string `json:"payload"`
}

func (th *twilioHandler) handleMediaMessage(mediaMessage TwilioMediaMessage) {
	// https://en.wikipedia.org/wiki/%CE%9C-law_algorithm
	mulawAudioData, err := base64.StdEncoding.DecodeString(mediaMessage.Media.Payload)
	if err != nil {
		log.Error().Err(err).Msg("Failed to decode base64 audio data")
		return
	}
	th.allMulawAudioBytes = append(th.allMulawAudioBytes, mulawAudioData...)
}

type TwilioStopMessage struct {
	TwilioWebsocketMessage
	Stop struct {
		AccountSid string `json:"accountSid"`
		CallSid    string `json:"callSid"`
	} `json:"stop"`
}

func (th *twilioHandler) handleStopMessage(msg TwilioStopMessage) {
	// handle the stop event, maybe clean up resources
}

type TwilioMarkMessage struct {
	TwilioWebsocketMessage
	Mark struct {
		Name string `json:"name"`
	} `json:"mark"`
}

func (th *twilioHandler) handleMarkMessage(msg TwilioMarkMessage) {
	// process the mark message
}

func (th *twilioHandler) writeMessage(msg interface{}) {
	msgBytes, err := json.Marshal(msg)
	errLog(err, "jsonMarshal TwilioWebsocketMessage") // shouldn't happen

	log.Debug().Msgf("sending message: %s", string(msgBytes))
	th.writeChan <- msgBytes
}

func (th *twilioHandler) readMessagesUntilChanClosed() {
	for msg := range th.readChan {
		th.handleMessage(msg)
	}

	// https://github.com/go-audio/wav/issues/29
	// https://stackoverflow.com/questions/59767373/convert-8khz-mulaw-to-16khz-pcm-in-real-time
	wavAudioBytes, err := audio_utils.ConvertOneByteMulawSamplesToWav(th.allMulawAudioBytes, 8000, 16000)
	dbg(err)

	log.Info().Msgf("websocket finished, gonna write %d bytes", len(wavAudioBytes))
	dbg(os.WriteFile("output/entire-phone-recording.wav", wavAudioBytes, 0644))
}

func (th *twilioHandler) handleMessage(msg []byte) {
	var message TwilioWebsocketMessage
	err := json.Unmarshal(msg, &message)
	if err != nil {
		// Maybe I just wrongfully implemented, or they changed the API
		log.Error().Err(err).Msgf("couldn't decode message from websocket: %s", string(msg))
		return
	}

	log.Debug().Msgf("received message: %s", string(msg))

	switch message.Event {
	case "connected":
		// handle connected event
	case "start":
		var startMessage TwilioStartMessage
		errLog(json.Unmarshal(msg, &startMessage), "json.Unmarshal startMessage")
		th.handleStartMessage(startMessage)
	case "media":
		var mediaMessage TwilioMediaMessage
		errLog(json.Unmarshal(msg, &mediaMessage), "json.Unmarshal mediaMessage")
		th.handleMediaMessage(mediaMessage)
	case "stop":
		var stopMessage TwilioStopMessage
		errLog(json.Unmarshal(msg, &stopMessage), "json.Unmarshal stopMessage")
		th.handleStopMessage(stopMessage)
		break
	case "mark":
		var markMessage TwilioMarkMessage
		errLog(json.Unmarshal(msg, &markMessage), "json.Unmarshal markMessage")
		th.handleMarkMessage(markMessage)
	default:
		log.Error().Err(fmt.Errorf("unknown message.Event %s", message.Event)).Msg("")
	}
}

func errLog(err error, what string) {
	if err != nil {
		log.Error().Err(err).Msg(what)
		debug.PrintStack()
	}
}
