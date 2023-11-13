// TLDR; Go itself cannot work with Microphone's well
// BUT it can bind with C-libraries which can do this with a bit of black-magic.
package main

import (
	"encoding/binary"
	"fmt"
	"github.com/gen2brain/malgo"
	"github.com/go-audio/audio"
	"github.com/go-audio/wav"
	"github.com/rs/zerolog/log"
	"github.com/spf13/afero"
	"io"
	"os"
	"strings"
	"time"
)

const MyDeviceInputChannels uint32 = 1
const MyDeviceSampleRate uint32 = 44100

func dbg(err error) {
	if err != nil {
		log.Debug().Err(err).Msg("sth non-essential failed")
	}
}

// This assumes S16 encoding (or two bytes per value)
func convertBytesToWav(intData []int, sampleRate uint32, numChannels uint32) (result []byte, err error) {
	iSampleRate := int(sampleRate)
	iNumChannels := int(numChannels)

	if len(intData) == 0 {
		return // Nothing to do
	}

	// Create a new in-memory file system
	fs := afero.NewMemMapFs()
	// Create an in-memory file to support io.WriteSeeker needed for NewEncoder which is needed for finalizing headers.
	inMemoryFilename := "in-memory-output.wav"
	inMemoryFile, err := fs.Create(inMemoryFilename)
	dbg(err)
	// We will call Close ourselves.

	// Convert audio data to IntBuffer
	inputBuffer := &audio.IntBuffer{Data: intData, Format: &audio.Format{SampleRate: iSampleRate, NumChannels: iNumChannels}}

	// Create a new WAV wavEncoder
	bitDepth := 16
	audioFormat := 1
	wavEncoder := wav.NewEncoder(inMemoryFile, iSampleRate, bitDepth, iNumChannels, audioFormat)
	log.Debug().Int("int_data_length", len(intData)).Int("sample_rate", iSampleRate).Int("bit_depth", bitDepth).Int("num_channels", iNumChannels).Int("audio_format", audioFormat).Msg("encoding int stream output as a wav")
	// Write to WAV wavEncoder
	if err = wavEncoder.Write(inputBuffer); err != nil {
		err = fmt.Errorf("cannot encode byte output as wav %w", err)
		return
	}

	// Close the wavEncoder to flush any remaining data and finalize the WAV file
	if err = wavEncoder.Close(); err != nil {
		err = fmt.Errorf("cannot finish wav encoding %w", err)
		return
	}

	// We close and re-open the file so we can properly read-all of its contents.
	dbg(inMemoryFile.Close())
	inMemoryFileReopen, err := fs.Open(inMemoryFilename)
	dbg(err)
	result, err = io.ReadAll(inMemoryFileReopen)
	dbg(err)
	if err == nil && len(result) == 0 {
		err = fmt.Errorf("wav output is empty when input was not")
		return
	}
	return
}

func twoByteDataToIntSlice(audioData []byte) []int {
	intData := make([]int, len(audioData)/2)
	for i := 0; i < len(audioData); i += 2 {
		// Convert the pCapturedSamples byte slice to int16 slice for FormatS16 as we go
		value := int(binary.LittleEndian.Uint16(audioData[i : i+2]))
		intData[i/2] = value
	}
	return intData
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

func malgoOutputMaybeFlushBuffer(pSampleData []byte, pSampleDataBufferIdx int, wavChunksChan chan []byte, sampleRate uint32, numChannels uint32, isEnd bool) int {
	flushByteSizeThreshold := 2 * int(sampleRate*2) // About two seconds
	shouldFlush := isEnd || ((len(pSampleData) - pSampleDataBufferIdx) > flushByteSizeThreshold)
	// log.Trace().Bool("should_flush", shouldFlush).Int("flush_threshold_byte_size", flushByteSizeThreshold).Int("len_sample_data", len(pSampleData)).Msg("malgoOutputMaybeFlushBuffer")
	if !shouldFlush {
		return pSampleDataBufferIdx
	}
	startIndex := pSampleDataBufferIdx
	endIndex := len(pSampleData)
	windowSize := int(sampleRate) * 2 / 50 // about 20ms

	// Poor mens VAP to detect silence: for now just better understand this, later we can do better.
	// TODO(P1, ux): See how vocode or WebRTC VAP does that
	if !isEnd { // when isEnd, we just take the end
		newData := pSampleData[startIndex:]
		// TODO: empiric for "silence" - in real world we need much better, Do min an max between 100 and 140
		threshold := 110.0
		candidateIndex := findLastIndexBelowAverage(newData, windowSize, threshold)
		// end of "silence" is likely already when new voice is coming in, so take midpoint
		if candidateIndex >= 0 {
			endIndex = startIndex + candidateIndex - (windowSize / 2)
		} else {
			endIndex = len(pSampleData)
		}
		if endIndex%2 == 1 { // this should only happen in the `candidateIndex >= 0` case
			endIndex--
		}
	}

	if !isEnd && endIndex == len(pSampleData) {
		// TODO: This makes the algorithm N^2 worst case - which is fine as I am just experimenting for now.
		log.Trace().Msg("could not find below threshold, waiting for more data")
		return startIndex
	}

	log.Trace().Int("start_byte_index", startIndex).Int("end_byte_index", endIndex).Msg("flushing pSample data into wav output")

	byteData := pSampleData[pSampleDataBufferIdx:endIndex]
	intData := twoByteDataToIntSlice(byteData)
	wavData, err := convertBytesToWav(intData, sampleRate, numChannels)
	if err != nil {
		log.Error().Err(err).Int("int_data_length", len(intData)).Msg("could not convert intData to wavData")
		return endIndex
	}
	wavChunksChan <- wavData
	dbg(os.WriteFile(fmt.Sprintf("output/%d-%d.wav", startIndex, endIndex), wavData, 0644))
	if isEnd {
		log.Info().Msg("closing wavChunksChan from malgoOutputMaybeFlushBuffer")
		close(wavChunksChan)
	}

	return endIndex
}

// Mostly from https://github.com/gen2brain/malgo/blob/master/_examples/capture/capture.go
func malgoRecord(wavChunksChan chan []byte) (result []byte, err error) {
	log.Info().Msg("malgo record (miniaudio)")
	ctx, err := malgo.InitContext(nil, malgo.ContextConfig{}, func(message string) {
		log.Debug().Msg(strings.Replace("malgo devices: "+message, "\n", "", -1))
	})
	if err != nil {
		err = fmt.Errorf("cannot init malgo context %w", err)
		return
	}
	defer func() {
		_ = ctx.Uninit()
		ctx.Free()
	}()

	deviceConfig := malgo.DefaultDeviceConfig(malgo.Duplex)
	deviceConfig.Capture.Format = malgo.FormatS16
	channels := MyDeviceInputChannels
	deviceConfig.Capture.Channels = channels
	sampleRate := MyDeviceSampleRate
	deviceConfig.SampleRate = sampleRate // TODO: maybe doing lower would fasten transcription up?
	deviceConfig.Alsa.NoMMap = 1

	sizeInBytes := uint32(malgo.SampleSizeInBytes(deviceConfig.Capture.Format))
	if sizeInBytes != 2 {
		log.Fatal().Uint32("size_in_bytes", sizeInBytes).Msgf("Expected 2 bytes for sample %s", deviceConfig.Capture.Format)
	}

	pSampleData := make([]byte, 0)
	pSampleDataBufferIdx := 0
	// Some black-magic event-handling which I don't really understand.
	// https://github.com/gen2brain/malgo/blob/master/_examples/capture/capture.go
	onRecvFrames := func(pSample2, pSample []byte, framecount uint32) {
		// Empirically, len(pSample) is 480, so for sample rate 44100 it's triggered about every 10ms.
		// sampleCount := framecount * deviceConfig.Capture.Channels * sizeInBytes
		pSampleData = append(pSampleData, pSample...)
		pSampleDataBufferIdx = malgoOutputMaybeFlushBuffer(pSampleData, pSampleDataBufferIdx, wavChunksChan, sampleRate, channels, false)
	}

	captureCallbacks := malgo.DeviceCallbacks{
		Data: onRecvFrames,
	}
	device, err := malgo.InitDevice(ctx.Context, deviceConfig, captureCallbacks)
	if err != nil {
		err = fmt.Errorf("cannot init malgo device with config %v: %w", deviceConfig, err)
		return
	}

	log.Info().Msg("malgo START recording...")
	timeStart := time.Now()
	err = device.Start()
	if err != nil {
		err = fmt.Errorf("cannot start malgo device %w", err)
		return
	}

	// TODO(P0, ux): Get this through VAD when we stop talking
	fmt.Println("Press Enter to stop recording...")
	_, err = fmt.Scanln()
	dbg(err)
	log.Info().Dur("recording_duration", time.Since(timeStart)).Msg("malgo STOP recording")
	log.Warn().Msg("TRACING HACK: malgo STOP")

	device.Uninit()
	// TODO(P0, ux): IF we can detect silence, than two things:
	// * We can use silence to stop the recording
	// * We do NOT need to send the end silence for transcription (can give us 500-1000ms).
	malgoOutputMaybeFlushBuffer(pSampleData, pSampleDataBufferIdx, wavChunksChan, sampleRate, channels, true)

	// WRITE IT INTO A WAV STUFF
	// Might NOT work with non-1 number of channels
	result, err = convertBytesToWav(twoByteDataToIntSlice(pSampleData), sampleRate, channels)
	return
}

// TODO: I never got this fully work - keeping in case someone wants to finish up the bytes to wav part (and interrupt).
//
//func chk(err error) {
//	if err != nil {
//		log.Panic().Err(err).Msg("total fail")
//	}
//}
//
//func recordAudioPortaudioToWav() (result []byte, err error) {
//	// Initialize PortAudio
//	chk(portaudio.Initialize())
//	defer func() { dbg(portaudio.Terminate()) }()
//
//	// Set up your microphone stream parameters
//	inputChannels := MyDeviceInputChannels
//	outputChannels := 0
//	sampleRate := MyDeviceSampleRate
//	// TODO: Figure out relationship between buffer size, sample rate and max recording time
//	int16FramesBuffer := make([]int16, 64) // []int yields "invalid Buffer type []int" in portaudio.go
//
//	deviceInfo, err := portaudio.DefaultInputDevice()
//	if err != nil {
//		dbg(err)
//	} else {
//		log.Debug().Msgf("default input device info %v", *deviceInfo)
//	}
//
//	log.Debug().Int("input_channels", inputChannels).Int("output_channels", outputChannels).Int("sample_rate", sampleRate).Int("buffer_size", len(int16FramesBuffer)).Msg("portaudio open default stream")
//	stream, err := portaudio.OpenDefaultStream(inputChannels, outputChannels, float64(sampleRate), len(int16FramesBuffer), &int16FramesBuffer)
//	if err != nil {
//		err = fmt.Errorf("cannot open default portaudio stream %w", err)
//		return
//	}
//	defer func() { dbg(stream.Close()) }()
//
//	sig := make(chan os.Signal, 1)
//	signal.Notify(sig, os.Interrupt, os.Kill)
//
//	if err = stream.Start(); err != nil {
//		err = fmt.Errorf("cannot start portaudio stream %w", err)
//		return
//	}
//	// Read and write audio data
//	rawInput := make([]int16, 0)
//loop:
//	for {
//		chk(stream.Read())
//		rawInput = append(rawInput, int16FramesBuffer...)
//		select {
//		case <-sig:
//			log.Info().Msg("SIG received to stop the loop")
//			break loop
//		default:
//		}
//	}
//
//	if err = stream.Stop(); err != nil {
//		err = fmt.Errorf("cannot stop portaudio stream %w", err)
//		return
//	}
//
//	intData := convertInt16SliceToIntSlice(rawInput)
//	result, err = convertBytesToWav(intData, sampleRate, inputChannels)
//	return
//}
