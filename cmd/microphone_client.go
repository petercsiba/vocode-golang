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

const MyDeviceInputChannels = 1
const MyDeviceSampleRate = 44100

func chk(err error) {
	if err != nil {
		log.Panic().Err(err).Msg("total fail")
	}
}

func dbg(err error) {
	if err != nil {
		log.Debug().Err(err).Msg("sth non-essential failed")
	}
}

// This assumes S16 encoding (or two bytes per value)
func convertBytesToWav(intData []int, sampleRate int, numChannels int) (result []byte, err error) {
	log.Debug().Int("int_length", len(intData)).Int("sample_rate", sampleRate).Msg("convertBytesToWav")
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
	inputBuffer := &audio.IntBuffer{Data: intData, Format: &audio.Format{SampleRate: sampleRate, NumChannels: numChannels}}

	// Create a new WAV wavEncoder
	bitDepth := 16
	audioFormat := 1
	wavEncoder := wav.NewEncoder(inMemoryFile, sampleRate, bitDepth, numChannels, audioFormat)
	log.Debug().Int("sample_rate", sampleRate).Int("bit_depth", bitDepth).Int("num_channels", numChannels).Int("audio_format", audioFormat).Msg("encoding int stream output as a wav")
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
	// For debug purposes write the output to a real file so we can replay it.
	dbg(os.WriteFile("output/recording.wav", result, 0644))
	return
}

// Mostly from https://github.com/gen2brain/malgo/blob/master/_examples/capture/capture.go
func malgoRecord() (result []byte, err error) {
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
	deviceConfig.Capture.Channels = MyDeviceInputChannels
	//deviceConfig.Playback.Format = malgo.FormatS16
	//deviceConfig.Playback.Channels = 1
	deviceConfig.SampleRate = MyDeviceSampleRate
	deviceConfig.Alsa.NoMMap = 1

	// var playbackSampleCount uint32
	var capturedSampleCount uint32
	pCapturedSamples := make([]byte, 0)

	// Some black-magic event-handling which I don't really understand.
	sizeInBytes := uint32(malgo.SampleSizeInBytes(deviceConfig.Capture.Format))
	onRecvFrames := func(pSample2, pSample []byte, framecount uint32) {
		sampleCount := framecount * deviceConfig.Capture.Channels * sizeInBytes
		newCapturedSampleCount := capturedSampleCount + sampleCount
		pCapturedSamples = append(pCapturedSamples, pSample...)
		capturedSampleCount = newCapturedSampleCount
	}

	captureCallbacks := malgo.DeviceCallbacks{
		Data: onRecvFrames,
	}
	device, err := malgo.InitDevice(ctx.Context, deviceConfig, captureCallbacks)
	if err != nil {
		err = fmt.Errorf("cannot init malgo device with config %v: %w", deviceConfig, err)
		return
	}

	log.Info().Msg("malgo start recording...")
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
	log.Debug().Dur("recording_duration", time.Since(timeStart)).Msg("malgo stop recording")

	device.Uninit()

	// Convert byte slice to int16 slice for FormatS16
	intData := make([]int, len(pCapturedSamples)/2)
	for i := 0; i < len(pCapturedSamples); i += 2 {
		intData[i/2] = int(binary.LittleEndian.Uint16(pCapturedSamples[i : i+2]))
	}

	// WRITE IT INTO A WAV STUFF
	// Might NOT work with non-1 number of channels
	result, err = convertBytesToWav(intData, int(deviceConfig.SampleRate), int(deviceConfig.Capture.Channels))
	return
}

func convertInt16SliceToIntSlice(input []int16) []int {
	output := make([]int, len(input))
	for i, v := range input {
		output[i] = int(v)
	}
	return output
}

// TODO: I never got this fully work - keeping in case someone wants to finish up the bytes to wav part (and interrupt).
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
