package audioio

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"github.com/petrzlen/vocode-golang/pkg/audio_utils"
	"github.com/rs/zerolog/log"
	"io"
	"os"
	"runtime/debug"
	"strconv"
	"sync"
	"time"
)

type twilioHandler struct {
	startMessage       *TwilioMessage // To keep the initial config
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

// GetReader implements WebsocketMessageHandler.GetReader
func (th *twilioHandler) GetReader() chan<- []byte {
	return th.readChan
}

// GetWriter implements WebsocketMessageHandler.GetWriter
func (th *twilioHandler) GetWriter() <-chan []byte {
	return th.writeChan
}

// Play implements OutputDevice.Play
func (th *twilioHandler) Play(audioOutput io.Reader) (*sync.WaitGroup, error) {
	// TODO: Technically we can implement the sync.WaitGroup with Mark messages

	wavBytes, err := io.ReadAll(audioOutput)
	if err != nil {
		return nil, fmt.Errorf("cannot read audioInput %w", err)
	}
	pcmBytes, err := audio_utils.ConvertWavToOneByteMulawSamples(wavBytes, 8000)
	if err != nil {
		return nil, fmt.Errorf("cannot convert audioOutput to mulaw samples %w", err)
	}

	base64String := base64.StdEncoding.EncodeToString(pcmBytes)

	// Twilio: The media payload should not contain audio file type header bytes.
	// Providing header bytes will cause the media to be streamed incorrectly.
	// https://www.twilio.com/docs/voice/twiml/stream#message-media-to-twilio

	mediaMessage := TwilioMessage{
		Event: "media",
		Media: &TwilioMediaPayload{
			Track:     "outbound",
			Chunk:     strconv.Itoa(th.mediaLastSeqNum + 1),
			Timestamp: strconv.Itoa(int(time.Since(th.startTime).Milliseconds())),
			Payload:   base64String,
		},
	}

	th.sendMessage(mediaMessage)
	return nil, nil
}

func (th *twilioHandler) handleConnectedMessage(msg TwilioMessage) {
	if msg.Protocol == nil || *msg.Protocol != "Call" {
		log.Error().Msgf("msg.Protocol unexpected: %v", msg.Protocol)
	}
	if msg.Version == nil || *msg.Version != "1.0.0" {
		log.Error().Msgf("msg.Version unexpected: %v", msg.Version)
	}
}

func (th *twilioHandler) getStreamId() string {
	if th.startMessage == nil {
		log.Debug().Msg("tried to get getStreamId before first startMessage")
		return ""
	}
	return th.startMessage.Start.StreamSid
}

func (th *twilioHandler) handleStartMessage(msg TwilioMessage) {
	if msg.Start == nil {
		log.Error().Msgf("msg.Start is nil for msg.event = 'start': %v", msg)
		return
	}
	// Some validation first:fddfs
	if !isInList("inbound", msg.Start.Tracks) {
		log.Error().Msgf("'inbound' NOT in Start.Tracks: %v", msg.Start.Tracks)
	}
	// Here "outbound" really means just what kind of events we get - since we send all outbound audio, we
	// don't need to get it back.
	// https://www.twilio.com/docs/voice/twiml/stream#attributes-track
	if isInList("outbound", msg.Start.Tracks) {
		log.Error().Msgf("'outbound' IS in Start.Tracks: %v", msg.Start.Tracks)
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
	th.Play(bytes.NewReader(wavBytes))
}

func (th *twilioHandler) handleMediaMessage(msg TwilioMessage) {
	if msg.Media == nil {
		log.Error().Msgf("msg.Media is nil for msg.event = 'media': %v", msg)
		return
	}
	if msg.Media.Track == "outbound" {
		log.Debug().Msg("received track='outbound' media type, ignoring")
		return
	}

	// https://en.wikipedia.org/wiki/%CE%9C-law_algorithm
	mulawAudioData, err := base64.StdEncoding.DecodeString(msg.Media.Payload)
	if err != nil {
		log.Error().Err(err).Msg("Failed to decode base64 audio data")
		return
	}
	th.allMulawAudioBytes = append(th.allMulawAudioBytes, mulawAudioData...)
}

func (th *twilioHandler) handleStopMessage(msg TwilioMessage) {
	if msg.Stop == nil {
		log.Error().Msgf("msg.Stop is nil for msg.event = 'stop': %v", msg)
		return
	}
}

func (th *twilioHandler) handleMarkMessage(msg TwilioMessage) {
	if msg.Mark == nil {
		log.Error().Msgf("msg.Mark is nil for msg.event = 'mark': %v", msg)
		return
	}
}

func (th *twilioHandler) sendMessage(msg TwilioMessage) {
	th.writeLastSeqNum++
	msg.SequenceNumber = strconv.Itoa(th.writeLastSeqNum)
	msg.StreamSid = th.getStreamId()

	msgBytes, err := json.Marshal(msg)
	errLog(err, "jsonMarshal TwilioMessage") // shouldn't happen

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
	var message TwilioMessage
	err := json.Unmarshal(msg, &message)
	if err != nil {
		// Maybe I just wrongfully implemented, or they changed the API
		log.Error().Err(err).Msgf("couldn't decode message from websocket: %s", string(msg))
		return
	}

	log.Debug().Msgf("received message: %s", string(msg))

	switch message.Event {
	case "connected":
		th.handleConnectedMessage(message)
	case "start":
		th.handleStartMessage(message)
	case "media":
		th.handleMediaMessage(message)
	case "stop":
		th.handleStopMessage(message)
	case "mark":
		th.handleMarkMessage(message)
	case "clear":
		th.handleMarkMessage(message)
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

// isInList checks if a string is present in a slice of strings.
func isInList(str string, list []string) bool {
	for _, v := range list {
		if v == str {
			return true
		}
	}
	return false
}
