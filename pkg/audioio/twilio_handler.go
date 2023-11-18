package audioio

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"github.com/go-audio/audio"
	"github.com/petrzlen/vocode-golang/pkg/audio_utils"
	"github.com/petrzlen/vocode-golang/pkg/models"
	"github.com/pkg/errors"
	"github.com/rs/zerolog/log"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

const MulawSilenceByte = 0xff
const TwilioMulawSampleRate = 8000

// TODO(P0, race): There are definitely race conditions here, think about it. Especially around
// graceful shutdown and channel closes.
type twilioHandler struct {
	// Twilio Protocol
	startMessage       *TwilioMessage // To keep the initial config
	startTime          time.Time
	allMulawAudioBytes []byte
	readChan           chan []byte

	mediaLastSeqNum int
	writeLastSeqNum int
	writeChan       chan []byte
	isStopped       bool

	// Package interface
	recordingChan chan models.AudioData
	// speechStartsIdx <= silenceStartsIdx || silenceStartsIdx == -1
	speechStartsIdx  int
	silenceStartsIdx int
	currentWindowIdx int
}

func NewTwilioHandler() *twilioHandler {
	result := &twilioHandler{
		// Twilio Protocol
		startMessage:       nil,
		allMulawAudioBytes: make([]byte, 0),
		readChan:           make(chan []byte, 100),
		mediaLastSeqNum:    0,
		writeLastSeqNum:    0,
		writeChan:          make(chan []byte, 100),
		isStopped:          false,

		// Package interface
		recordingChan: nil,

		speechStartsIdx:  -1,
		silenceStartsIdx: -1,
		currentWindowIdx: 0,
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

// StartRecording implements InputDevice.StartRecording
// -- NOTE: The recording actually starts when the Websocket is established.
// When the readChan is closed (i.e. connection is dropped), then it will also call StopRecording()
func (th *twilioHandler) StartRecording(recordingChan chan models.AudioData) error {
	th.recordingChan = recordingChan
	return nil
}

// StopRecording implements InputDevice.StopRecording
func (th *twilioHandler) StopRecording() ([]byte, error) {
	err := th.Stop()

	return nil, err
}

// Stop implements OutputDevice.Stop
func (th *twilioHandler) Stop() error {
	th.isStopped = true
	log.Info().Str("stream_id", th.getStreamId()).Msg("writeChan close")

	// This will trigger the websocket close,
	// which will then trigger the readChan to close,
	// which then triggers the recordingChan to close.
	close(th.writeChan)

	return nil
}

// Play implements OutputDevice.Play
// TODO(P1, devx): Technically we can implement the sync.WaitGroup with Mark messages over Twilio's protocol
func (th *twilioHandler) Play(intBuffer *audio.IntBuffer) (*sync.WaitGroup, error) {
	// Twilio: The media payload should not contain audio file type header bytes.
	// Providing header bytes will cause the media to be streamed incorrectly.
	// https://www.twilio.com/docs/voice/twiml/stream#message-media-to-twilio
	mulawBytes, err := audio_utils.EncodeToMulaw(intBuffer, TwilioMulawSampleRate)
	if err != nil {
		return nil, fmt.Errorf("cannot convert intBuffer into mulawBytes: %w", err)
	}

	base64String := base64.StdEncoding.EncodeToString(mulawBytes)

	th.mediaLastSeqNum++
	mediaMessage := TwilioMessage{
		Event: "media",
		Media: &TwilioMediaPayload{
			Track:     "outbound",
			Chunk:     strconv.Itoa(th.mediaLastSeqNum),
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
	// == First, some validations
	if msg.Start == nil {
		log.Error().Msgf("msg.Start is nil for msg.event = 'start': %v", msg)
		return
	}
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
		SampleRate: TwilioMulawSampleRate,
		Channels:   1,
	}
	if msg.Start.MediaFormat != expectedMediaFormat {
		log.Error().Msgf("unexpected media format in Start.MediaFormat: %v", msg.Start.MediaFormat)
	}

	// == Then the real stuff
	th.startMessage = &msg
	th.startTime = time.Now()
}

func (th *twilioHandler) handleMediaMessage(msg TwilioMessage) {
	if msg.Media == nil {
		log.Error().Str("stream_id", th.getStreamId()).Msgf("msg.Media is nil for msg.event = 'media': %v", msg)
		return
	}
	if msg.Media.Track != "inbound" {
		log.Debug().Str("stream_id", th.getStreamId()).Msgf("received track='%s' media type, ignoring", msg.Media.Track)
		return
	}

	// https://en.wikipedia.org/wiki/%CE%9C-law_algorithm
	mulawAudioData, err := base64.StdEncoding.DecodeString(msg.Media.Payload)
	if err != nil {
		log.Error().Str("stream_id", th.getStreamId()).Err(err).Msg("Failed to decode base64 audio data")
		return
	}
	th.allMulawAudioBytes = append(th.allMulawAudioBytes, mulawAudioData...)

	th.maybeSubmitAudioOutput()
}

// TODO(P1, devx): Feels like the "VAD" or "silence detection" should be abstracted away
// as this custom logic is getting overly complex.
// BUT then every input method has different sensitivity / properties / expectations from UX.
func (th *twilioHandler) maybeSubmitAudioOutput() {
	speechThresholdCount := 2 * TwilioMulawSampleRate
	silenceThresholdCount := 5 * TwilioMulawSampleRate
	maxSilenceLength := 0

	if len(th.allMulawAudioBytes) < speechThresholdCount {
		return
	}

	for ; th.currentWindowIdx < len(th.allMulawAudioBytes); th.currentWindowIdx++ {
		if th.currentWindowIdx%10000 == 0 {
			log.Trace().Int("all_size", len(th.allMulawAudioBytes)).Int("speechStartsIdx", th.speechStartsIdx).Int("silenceStartsIdx", th.silenceStartsIdx).Int("currentWindowIdx", th.currentWindowIdx).Int("longestSilence", maxSilenceLength).Msg("maybeSubmitAudioOutput")
		}

		b := th.allMulawAudioBytes[th.currentWindowIdx]

		// TODO(P1, ux): Use some kind of a VAD
		// Detect start of non-silence, interpreted as speech.
		if b != MulawSilenceByte && th.speechStartsIdx < 0 {
			th.speechStartsIdx = th.currentWindowIdx
		}
		if th.speechStartsIdx < 0 {
			continue
		}
		// From now on true that: th.speechStartsIdx >= 0
		submitAudio := false
		submitPrompt := false

		// Evaluate if there was enough silence after a speech has started
		if b == MulawSilenceByte {
			if th.silenceStartsIdx == -1 {
				th.silenceStartsIdx = th.currentWindowIdx
			}
			silenceLength := th.currentWindowIdx - th.silenceStartsIdx
			if silenceLength >= silenceThresholdCount {
				submitAudio = true
				submitPrompt = true
			}
			// A debug param mostly to adjust thresholds when they fail
			if silenceLength > maxSilenceLength {
				maxSilenceLength = silenceLength
			}
		} else {
			th.silenceStartsIdx = -1
		}

		// Enough speech to submit audio
		if th.currentWindowIdx-th.speechStartsIdx >= speechThresholdCount {
			if th.silenceStartsIdx > 0 && th.currentWindowIdx-th.silenceStartsIdx >= 100 {
				submitAudio = true
			}
		}

		if submitAudio {
			rawMulawSlice := th.allMulawAudioBytes[th.speechStartsIdx:th.silenceStartsIdx]
			// Too short would result into garbage (or HTTP 4xx)
			if len(rawMulawSlice) >= TwilioMulawSampleRate/10 {
				log.Info().Bool("submit_prompt", submitPrompt).Int("all_size", len(th.allMulawAudioBytes)).Int("speechStartsIdx", th.speechStartsIdx).Int("silenceStartsIdx", th.silenceStartsIdx).Int("currentWindowIdx", th.currentWindowIdx).Msg("detected enough speech with enough silence to submit audio")

				intBuffer := audio_utils.DecodeFromMulaw(rawMulawSlice, TwilioMulawSampleRate)
				wavBytes, err := audio_utils.EncodeToWavSimple(intBuffer)
				errLog(err, "maybeSubmitAudioOutput.EncodeToWavSimple") // shouldn't happen

				dbg(os.WriteFile(fmt.Sprintf("output/%d-%d.wav", th.speechStartsIdx, th.silenceStartsIdx), wavBytes, 0644))

				th.recordingChan <- models.AudioData{
					EventType: models.AudioInput,
					ByteData:  wavBytes,
					Format:    "wav",
					Length:    time.Duration(float64(len(rawMulawSlice)) / float64(TwilioMulawSampleRate)),
					Trace:     models.NewTrace("twilio.stream"),
				}

				th.speechStartsIdx = th.silenceStartsIdx // Note, this can make the next slice 0
			}
		}

		// Note: By having a long silence required in the end, the TranscriberRoutine will
		// be likely done, and we can submit chat request right away.
		if submitPrompt {
			log.Info().Int("all_size", len(th.allMulawAudioBytes)).Int("speechStartsIdx", th.speechStartsIdx).Int("silenceStartsIdx", th.silenceStartsIdx).Int("currentWindowIdx", th.currentWindowIdx).Msg("enough silence to submitPrompt")
			th.recordingChan <- models.NewAudioDataSubmit("twilio.silence")

			th.speechStartsIdx = -1
			th.silenceStartsIdx = -1
		}
	}
}

func (th *twilioHandler) handleStopMessage(msg TwilioMessage) {
	if msg.Stop == nil {
		log.Error().Str("stream_id", th.getStreamId()).Msgf("msg.Stop is nil for msg.event = 'stop': %v", msg)
		return
	}
}

func (th *twilioHandler) handleMarkMessage(msg TwilioMessage) {
	if msg.Mark == nil {
		log.Error().Str("stream_id", th.getStreamId()).Msgf("msg.Mark is nil for msg.event = 'mark': %v", msg)
		return
	}
}

func logMessage(direction string, msg []byte) {
	// For outbound media events, it can be VERY long so we truncate to avoid logspam.
	msgStr := truncatePayload(string(msg))
	log.Debug().Msgf("%s message: %v", direction, msgStr)
}

func (th *twilioHandler) sendMessage(msg TwilioMessage) {
	if th.isStopped {
		log.Debug().Str("stream_id", th.getStreamId()).Msgf("cannot send message after twilioHandler isStopped: %v", msg)
		return
	}

	th.writeLastSeqNum++
	msg.SequenceNumber = strconv.Itoa(th.writeLastSeqNum)
	msg.StreamSid = th.getStreamId()

	msgBytes, err := json.Marshal(msg)
	errLog(err, "jsonMarshal TwilioMessage") // shouldn't happen
	logMessage("sending", msgBytes)

	th.writeChan <- msgBytes
}

func (th *twilioHandler) readMessagesUntilChanClosed() {
	for msg := range th.readChan {
		th.handleMessage(msg)
	}

	// After reading done, there is no more to produce.
	log.Info().Str("stream_id", th.getStreamId()).Msg("th.recordingChan CLOSE")
	close(th.recordingChan)

	th.debugDumpAllRecording()
}

func (th *twilioHandler) handleMessage(msgBytes []byte) {
	var msg TwilioMessage
	err := json.Unmarshal(msgBytes, &msg)
	if err != nil {
		// Maybe I just wrongfully implemented, or they changed the API
		log.Error().Err(err).Msgf("couldn't decode msg from websocket: %s", string(msgBytes))
		return
	}

	// To prevent log-spam, we only log non-media messages, or every 100th media message.
	if msg.Media == nil || strings.HasSuffix(msg.Media.Chunk, "00") {
		logMessage("received", msgBytes)
	}

	switch msg.Event {
	case "connected":
		th.handleConnectedMessage(msg)
	case "start":
		th.handleStartMessage(msg)
	case "media":
		th.handleMediaMessage(msg)
	case "stop":
		th.handleStopMessage(msg)
	case "mark":
		th.handleMarkMessage(msg)
	case "clear":
		th.handleMarkMessage(msg)
	default:
		log.Error().Err(fmt.Errorf("unknown msg.Event %s", msg.Event)).Msg("")
	}
}

func (th *twilioHandler) debugDumpAllRecording() {
	// https://github.com/go-audio/wav/issues/29
	// https://stackoverflow.com/questions/59767373/convert-8khz-mulaw-to-16khz-pcm-in-real-time
	intBuffer := audio_utils.DecodeFromMulaw(th.allMulawAudioBytes, TwilioMulawSampleRate)
	wavAudioBytes, err := audio_utils.EncodeToWavSimple(intBuffer)
	dbg(err)

	debugOutputFilename := "output/entire-phone-recording.wav"
	log.Info().Str("stream_id", th.getStreamId()).Msgf("websocket finished, gonna write %d bytes to %s", len(wavAudioBytes), debugOutputFilename)
	dbg(os.WriteFile(debugOutputFilename, wavAudioBytes, 0644))
}

func errLog(err error, what string) {
	if err != nil {
		log.Error().Err(errors.WithStack(err)).Msg(what)
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
