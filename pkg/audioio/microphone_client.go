// TLDR; Go itself cannot work with Microphone's well
// BUT it can bind with C-libraries which can do this with a bit of black-magic.
package audioio

import (
	"fmt"
	"github.com/gen2brain/malgo"
	"github.com/petrzlen/vocode-golang/pkg/audio_utils"
	"github.com/rs/zerolog/log"
	"os"
	"strings"
	"time"

	"github.com/petrzlen/vocode-golang/pkg/models"
)

func dbg(err error) {
	if err != nil {
		log.Debug().Err(err).Msg("sth non-essential failed")
	}
}

const MyDeviceInputChannels uint32 = 1
const MyDeviceSampleRate uint32 = 44100

type microphone struct {
	device       *malgo.Device
	deviceConfig malgo.DeviceConfig
	malgoContext *malgo.AllocatedContext

	recordingStart time.Time
	recordingChan  chan models.AudioData

	pSampleData          []byte
	pSampleDataBufferIdx int
}

// NewMicrophone inits the microphone device,
// you should defer StopRecording
func NewMicrophone() (result InputDevice, err error) {
	log.Info().Msg("malgo init context (miniaudio)")
	ctx, err := malgo.InitContext(nil, malgo.ContextConfig{}, func(message string) {
		log.Debug().Msg(strings.Replace("malgo devices: "+message, "\n", "", -1))
	})
	if err != nil {
		err = fmt.Errorf("cannot init malgo context %w", err)
		return
	}

	deviceConfig := malgo.DefaultDeviceConfig(malgo.Duplex)
	deviceConfig.Capture.Format = malgo.FormatS16
	deviceConfig.Capture.Channels = MyDeviceInputChannels
	// TODO: maybe doing lower would fasten transcription up?
	deviceConfig.SampleRate = MyDeviceSampleRate
	deviceConfig.Alsa.NoMMap = 1

	result = &microphone{
		device:               nil,
		deviceConfig:         deviceConfig,
		malgoContext:         ctx,
		recordingChan:        nil,
		pSampleData:          make([]byte, 0),
		pSampleDataBufferIdx: 0,
	}
	return
}

func (m *microphone) getFormat() malgo.FormatType {
	return m.deviceConfig.Capture.Format
}

func (m *microphone) getSampleRate() uint32 {
	return m.deviceConfig.SampleRate
}

func (m *microphone) getNumChannels() uint32 {
	return m.deviceConfig.Capture.Channels
}

// StartRecording can only be called once for NewMicrophone
// Mostly from https://github.com/gen2brain/malgo/blob/master/_examples/capture/capture.go
func (m *microphone) StartRecording(recordingChan chan models.AudioData) (err error) {
	m.recordingChan = recordingChan
	format := m.getFormat()
	sizeInBytes := uint32(malgo.SampleSizeInBytes(format))
	if sizeInBytes != 2 {
		log.Fatal().Uint32("size_in_bytes", sizeInBytes).Msgf("Expected 2 bytes for sample %s", format)
	}

	// Some black-magic event-handling which I don't really understand.
	// https://github.com/gen2brain/malgo/blob/master/_examples/capture/capture.go
	onRecvFrames := func(pSample2, pSample []byte, framecount uint32) {
		// Empirically, len(pSample) is 480, so for sample rate 44100 it's triggered about every 10ms.
		// sampleCount := framecount * deviceConfig.Capture.Channels * sizeInBytes
		m.pSampleData = append(m.pSampleData, pSample...)
		m.pSampleDataBufferIdx = m.maybeFlushBuffer(false)
	}

	captureCallbacks := malgo.DeviceCallbacks{
		Data: onRecvFrames,
	}
	m.device, err = malgo.InitDevice(m.malgoContext.Context, m.deviceConfig, captureCallbacks)
	if err != nil {
		err = fmt.Errorf("cannot init malgo device with config %v: %w", m.deviceConfig, err)
		return
	}

	log.Info().Msg("malgo START recording...")
	m.recordingStart = time.Now()
	err = m.device.Start()
	if err != nil {
		err = fmt.Errorf("cannot start malgo device %w", err)
		return
	}
	return
}

func (m *microphone) StopRecording() (entireRecording []byte, err error) {
	log.Info().Dur("recording_duration", time.Since(m.recordingStart)).Msg("malgo STOP recording")
	log.Warn().Msg("TRACING HACK: malgo STOP")
	dbg(m.device.Stop())
	dbg(m.malgoContext.Uninit())

	// TODO(P0, ux): IF we can detect silence, than two things:
	// * We can use silence to stop the recording
	// * We do NOT need to send the end silence for transcription (can give us 500-1000ms).

	// Since we chunk up stuff - there might be some leftovers.
	// TODO(P0, ux): This is a major contributor to the Stop to Playback latency
	m.maybeFlushBuffer(true)

	// WRITE IT INTO A WAV STUFF
	// Might NOT work with non-1 number of channels
	entireRecording, err = audio_utils.ConvertByteSamplesToWav(m.pSampleData, m.getSampleRate(), m.getNumChannels())

	m.malgoContext.Free()
	return
}

// Function to find the last index where the average of the last 100 bytes is below 90.
func findLastIndexBelowAverage(data []byte, windowSize int, threshold float64) int {
	n := len(data)
	if n < windowSize {
		log.Trace().Int("last_index", -1).Int("data_size", len(data)).Int("window_size", windowSize).Float64("threshold", threshold).Msg("findLastIndexBelowAverage window size too big")
		return -1 // Not enough data to form a window
	}

	lastIndex := -1
	var sum int

	// Initialize the first window
	for i := 0; i < windowSize; i++ {
		sum += int(data[i])
	}

	// Iterate over the array
	for i := windowSize; i < n; i++ {
		avg := float64(sum) / float64(windowSize)
		if avg < threshold {
			lastIndex = i
		}

		// Update the sum to include the next byte and exclude the oldest byte
		sum -= int(data[i-windowSize])
		sum += int(data[i])
	}

	log.Trace().Int("last_index", lastIndex).Int("data_size", len(data)).Int("window_size", windowSize).Float64("threshold", threshold).Msg("findLastIndexBelowAverage returned")
	return lastIndex
}

func sampleCountForMilliseconds(sampleRate uint32, numChannels uint32, milliseconds int) int {
	return int(int64(milliseconds) * int64(sampleRate) * int64(numChannels) / int64(1000))
}

func (m *microphone) maybeFlushBuffer(isEnd bool) int {
	sampleRate := m.getSampleRate()
	numChannels := m.getNumChannels()

	flushByteSizeThreshold := 2 * int(sampleRate*2) // About two seconds
	shouldFlush := isEnd || ((len(m.pSampleData) - m.pSampleDataBufferIdx) > flushByteSizeThreshold)
	// log.Trace().Bool("should_flush", shouldFlush).Int("flush_threshold_byte_size", flushByteSizeThreshold).Int("len_sample_data", len(pSampleData)).Msg("malgoOutputMaybeFlushBuffer")
	if !shouldFlush {
		return m.pSampleDataBufferIdx
	}
	startIndex := m.pSampleDataBufferIdx
	endIndex := len(m.pSampleData)
	windowSize := sampleCountForMilliseconds(sampleRate, numChannels, 20)

	// Poor mens VAP to detect silence: for now just better understand this, later we can do better.
	// TODO(P1, ux): See how vocode or WebRTC VAP does that
	if !isEnd { // when isEnd, we just take the end
		newData := m.pSampleData[startIndex:]
		// TODO: empiric for "silence" - in real world we need much better, Do min an max between 100 and 140
		threshold := 110.0
		candidateIndex := findLastIndexBelowAverage(newData, windowSize, threshold)
		// end of "silence" is likely already when new voice is coming in, so take midpoint
		if candidateIndex >= 0 {
			// Whisper: Minimum audio length is 0.1 seconds.
			if candidateIndex >= sampleCountForMilliseconds(sampleRate, numChannels, 250) {
				endIndex = startIndex + candidateIndex - (windowSize / 2)
			} else {
				log.Trace().Msg("not enough 'non-silence' from the beginning")
				return startIndex
			}
		} else {
			endIndex = len(m.pSampleData)
		}
		if endIndex%2 == 1 { // this should only happen in the `candidateIndex >= 0` case
			endIndex--
		}
	}

	if !isEnd && endIndex == len(m.pSampleData) {
		// TODO: This makes the algorithm N^2 worst case - which is fine as I am just experimenting for now.
		// log.Trace().Msg("could not find below threshold, waiting for more data")
		return startIndex
	}

	log.Trace().Int("start_byte_index", startIndex).Int("end_byte_index", endIndex).Msg("flushing pSample data into wav output")

	byteData := m.pSampleData[m.pSampleDataBufferIdx:endIndex]
	wavData, err := audio_utils.ConvertByteSamplesToWav(byteData, sampleRate, numChannels)
	if err != nil {
		log.Error().Err(err).Int("byte_data_length", len(byteData)).Msg("could not convert byteData to wavData")
		return endIndex
	}

	audioData := models.AudioData{
		ByteData: wavData,
		Format:   "wav",
		Length:   time.Duration(float64(len(wavData)) / float64(sampleRate)),
		Trace: models.Trace{
			DataName:  "audio_data",
			CreatedAt: time.Now(),
			Creator:   "microphone_client",
		},
	}
	m.recordingChan <- audioData
	dbg(os.WriteFile(fmt.Sprintf("output/%d-%d.wav", startIndex, endIndex), wavData, 0644))
	if isEnd {
		log.Info().Msg("closing wavChunksChan from malgoOutputMaybeFlushBuffer")
		close(m.recordingChan)
	}

	return endIndex
}
