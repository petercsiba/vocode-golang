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

// ConvertTwoByteSamplesToWav assumes S16 encoding (or two bytes per value)
func ConvertTwoByteSamplesToWav(byteData []byte, sampleRate uint32, numChannels uint32) (result []byte, err error) {
	intData := twoByteDataToIntSlice(byteData)

	// For most parameters, we just do the same in both input and output.
	inputBuffer := &audio.IntBuffer{
		Data: intData,
		Format: &audio.Format{
			SampleRate:  int(sampleRate),
			NumChannels: int(numChannels),
		},
		SourceBitDepth: 16,
	}

	audioFormat := 1
	return convertIntSamplesToWav(inputBuffer, sampleRate, numChannels, audioFormat)
}

// ConvertOneByteMulawSamplesToWav assumes encoding 7 (or one byte per value)
func ConvertOneByteMulawSamplesToWav(byteData []byte, inputSampleRate, outputSampleRate uint32) (result []byte, err error) {
	// https://github.com/go-audio/wav/issues/29
	intData := oneByteDataToIntSlice(byteData)
	sourceBitDepth := 8
	numChannels := uint32(1)
	audioFormat := 7

	inputBuffer := &audio.IntBuffer{
		Data: intData,
		Format: &audio.Format{
			SampleRate:  int(inputSampleRate),
			NumChannels: int(numChannels),
		},
		SourceBitDepth: sourceBitDepth,
	}

	return convertIntSamplesToWav(inputBuffer, outputSampleRate, numChannels, audioFormat)
}

// ConvertIntSamplesToWav assumes S16 encoding (or two bytes per value)
func convertIntSamplesToWav(inputBuffer *audio.IntBuffer, sampleRate uint32, numChannels uint32, audioFormat int) (result []byte, err error) {
	if len(inputBuffer.Data) == 0 {
		return // Nothing to do
	}

	// Create a new in-memory file system
	fs := afero.NewMemMapFs()
	// Create an in-memory file to support io.WriteSeeker needed for NewEncoder which is needed for finalizing headers.
	inMemoryFilename := "in-memory-output.wav"
	inMemoryFile, err := fs.Create(inMemoryFilename)
	dbg(err)
	// We will call Close ourselves.

	outputBitDepth := 16
	iSampleRate := int(sampleRate)
	iNumChannels := int(numChannels)
	// TODO: Should we somewhat adjust outputSampleRate? Here we re-use the input one.
	// Create a new WAV wavEncoder
	wavEncoder := wav.NewEncoder(inMemoryFile, iSampleRate, outputBitDepth, iNumChannels, audioFormat)
	log.Debug().Int("int_data_length", len(inputBuffer.Data)).Int("sample_rate", iSampleRate).Int("source_bit_depth", inputBuffer.SourceBitDepth).Int("output_bit_depth", outputBitDepth).Int("num_channels", iNumChannels).Int("audio_format", audioFormat).Msg("encoding int stream output as a wav")
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

func oneByteDataToIntSlice(bytes []byte) []int {
	intData := make([]int, len(bytes))
	for i, b := range bytes {
		intData[i] = int(b)
	}
	return intData
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
