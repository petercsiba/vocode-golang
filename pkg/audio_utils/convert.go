package audio_utils

import (
	"encoding/binary"
	"fmt"
	"github.com/go-audio/audio"
	"github.com/go-audio/wav"
	"github.com/rs/zerolog/log"
	"github.com/spf13/afero"
	"io"
)

func dbg(err error) {
	if err != nil {
		log.Debug().Err(err).Msg("sth non-essential failed")
	}
}

// ConvertByteSamplesToWav assumes S16 encoding (or two bytes per value)
func ConvertByteSamplesToWav(byteData []byte, sampleRate uint32, numChannels uint32) (result []byte, err error) {
	intData := twoByteDataToIntSlice(byteData)
	return ConvertIntSamplesToWav(intData, sampleRate, numChannels)
}

// ConvertIntSamplesToWav assumes S16 encoding (or two bytes per value)
func ConvertIntSamplesToWav(intData []int, sampleRate uint32, numChannels uint32) (result []byte, err error) {
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

	// Convert audio_utils data to IntBuffer
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
