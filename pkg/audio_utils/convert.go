// Package audio_utils.convert deals with translating waveform samples so they can be used between systems, e.g.:
// * Local microphone produces 16bit samples with 44,100 khz
// * OpenAI TTS produces mp3, flac, opus with 24,000 sample rate
// * OpenAI Whisper takes wav
// * Twilio Telephony requires mulaw encoded 8bit with 8khz sample rate
// Usage:
// 1.) Convert your format to audio.IntBuffer
// 2.) Convert audio.IntBuffer to your desired format
// NOTE:
// * Everything happens in-memory to make deployment as easy as possible,
// * i.e. NO ffmpeg, files or other external libraries to loose hair for.
//
// TODO(P1, devx): Might be(en) worthwhile looking / migrating into https://github.com/faiface/beep/
package audio_utils

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"github.com/go-audio/audio"
	"github.com/go-audio/wav"
	"github.com/hajimehoshi/go-mp3"
	"github.com/mewkiz/flac"
	"github.com/pkg/errors"
	"github.com/rs/zerolog/log"
	"github.com/spf13/afero"
	"io"
)

// DecodeFromMulaw assumes one channel and encoding 7 (or one byte per value)
func DecodeFromMulaw(byteData []byte, inputSampleRate int) *audio.IntBuffer {
	// https://github.com/go-audio/wav/issues/29
	intData := make([]int, len(byteData))
	for i, b := range byteData {
		intData[i] = int(muLawToInt16(b))
	}

	numChannels := 1
	// sourceAudioFormat := 7
	return &audio.IntBuffer{
		Data: intData,
		Format: &audio.Format{
			SampleRate:  inputSampleRate,
			NumChannels: numChannels,
		},
		// Although the source had bit depth 8, we did muLawToInt16.
		SourceBitDepth: 16,
	}
}

func EncodeToMulaw(intBuffer *audio.IntBuffer, outputSampleRate int) ([]byte, error) {
	format := intBuffer.Format
	inputSampleRate := format.SampleRate
	log.Debug().Int("input_sample_rate", inputSampleRate).Int("output_sample_rate", outputSampleRate).Int("num_channels", format.NumChannels).Int("source_bit_depth", intBuffer.SourceBitDepth).Int("num_frames", intBuffer.NumFrames()).Msg("ConvertToMulawSamples read input")

	intData := intBuffer.Data
	if inputSampleRate != outputSampleRate {
		log.Debug().Msg("gonna resample mulaw intData")
		intData = ResampleSimple(intData, inputSampleRate, outputSampleRate)
	}

	outputBytes := make([]byte, len(intData))
	for i, intVal := range intData {
		outputBytes[i] = int16ToMuLaw(int16(intVal))
	}

	return outputBytes, nil
}

// EncodeToWavSimple is like EncodeToWav, but just uses the same sample formats as in the input.
func EncodeToWavSimple(inputBuffer *audio.IntBuffer) (result []byte, err error) {
	// sampleRate := inputBuffer.Format.SampleRate
	bitDepth := inputBuffer.SourceBitDepth
	// numChannels := inputBuffer.Format.NumChannels
	audioFormat := 1
	return EncodeToWav(inputBuffer, bitDepth, audioFormat)
}

// EncodeToWav takes inputBuffer and outputs encoded .wav with the desired sample format.
// NOTE: Empirically, we should keep the original sampleRate/bitDepth/channels if possible.
// TODO: I am semi-sure that audioFormat is ignored, as it can be derived from bitDepth / inputBuffer.SourceBithDepth.
func EncodeToWav(inputBuffer *audio.IntBuffer, bitDepth int, audioFormat int) (result []byte, err error) {
	if len(inputBuffer.Data) == 0 {
		return // Nothing to do
	}

	// Create an in-memory file to support io.WriteSeeker needed for NewEncoder which is needed for finalizing headers.
	fs := afero.NewMemMapFs()
	inMemoryFilename := "in-memory-writer.wav"
	inMemoryFile, err := fs.Create(inMemoryFilename)
	dbg(err)
	// We will call Close ourselves.

	// NOTE: we used to have sampleRate and numChannels as a parameter, BUT it seems to be doing nothing.
	sampleRate := inputBuffer.Format.SampleRate
	numChannels := inputBuffer.Format.NumChannels
	wavEncoder := wav.NewEncoder(inMemoryFile, sampleRate, bitDepth, numChannels, audioFormat)
	log.Debug().Int("int_data_length", len(inputBuffer.Data)).Int("sample_rate", sampleRate).Int("source_bit_depth", inputBuffer.SourceBitDepth).Int("output_bit_depth", bitDepth).Int("num_channels", numChannels).Int("audio_format", audioFormat).Msg("encoding int stream output as a wav")
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

	result, err = reReadInMemoryFile(fs, inMemoryFile)
	return
}

func reReadInMemoryFile(fs afero.Fs, inMemoryFile afero.File) ([]byte, error) {
	inMemoryFilename := inMemoryFile.Name()

	// We close and re-open the file so we can properly read-all of its contents.
	// TODO: Might be easier to just .Seek(0)
	dbg(inMemoryFile.Close())
	inMemoryFileReopen, err := fs.Open(inMemoryFilename)
	dbg(err)
	result, err := io.ReadAll(inMemoryFileReopen)
	dbg(err)
	if err == nil && len(result) == 0 {
		err = fmt.Errorf("wav output is empty when input was not")
	}
	return result, err
}

// DecodeFromFlac is a slightly modified version of flac2wav.go
// https://github.com/apatterson-cogo/flac/blob/e97902945092/cmd/flac2wav/flac2wav.go
func DecodeFromFlac(rawFlacBytes []byte) (*audio.IntBuffer, error) {
	stream, err := flac.New(bytes.NewReader(rawFlacBytes))
	if err != nil {
		return nil, fmt.Errorf("cannot create flac.New %w", err)
	}

	inSampleRate := int(stream.Info.SampleRate)
	inBitRate := int(stream.Info.BitsPerSample)
	inNumChannels := int(stream.Info.NChannels)
	log.Debug().Int("byte_length", len(rawFlacBytes)).Int("sample_rate", inSampleRate).Int("bit_rate", inBitRate).Int("num_channels", inNumChannels).Msg("ConvertFlacToWav input stream")

	var data []int
	for {
		// Decode FLAC audio samples.
		frame, err := stream.ParseNext()
		if err != nil {
			if err == io.EOF {
				break
			}
			return nil, errors.WithStack(err)
		}

		// Encode WAV audio samples.
		for i := 0; i < frame.Subframes[0].NSamples; i++ {
			for _, subframe := range frame.Subframes {
				sample := int(subframe.Samples[i])
				if frame.BitsPerSample == 8 {
					// WAV files with 8 bit-per-sample are stored with unsigned
					// values, WAV files with more than 8 bit-per-sample are stored
					// as signed values (ref page 59-60 of [1]).
					//
					// [1]: http://www-mmsp.ece.mcgill.ca/Documents/AudioFormats/WAVE/Docs/riffmci.pdf
					// ref: https://github.com/mewkiz/flac/issues/51#issuecomment-1046183409
					const midpointValue = 0x80
					sample += midpointValue
				}
				data = append(data, sample)
			}
		}
	}

	// NOTE: This part is different from the flac2wav.go example,
	// and it assumes that all frames have the same encoding config.
	return &audio.IntBuffer{
		Format: &audio.Format{
			NumChannels: inNumChannels,
			SampleRate:  inSampleRate,
		},
		Data:           data,
		SourceBitDepth: inBitRate,
	}, nil
}

func DecodeFromMp3(rawAudioBytes []byte) (*audio.IntBuffer, error) {
	decodedMp3, err := mp3.NewDecoder(bytes.NewReader(rawAudioBytes))
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("mp3.NewDecoder cannot read bytes %w", err)
	}
	sampleRate := decodedMp3.SampleRate()
	log.Debug().Int("sample_rate", sampleRate).Int64("byte_size", decodedMp3.Length()).Msg("player START decoded mp3")

	decodedMp3Bytes, err := io.ReadAll(decodedMp3)
	if err != nil {
		return nil, fmt.Errorf("cannot read all decoded mp3 bytes: %w", err)
	}

	// Surfacing the mp3.NewDecoder documentation:
	// * The stream is always formatted as 16bit (little endian) 2 channels
	// * even if the source is single channel MP3.
	// * Thus, a sample always consists of 4 bytes.
	intData := TwoByteDataToIntSlice(decodedMp3Bytes)
	// TODO(P1, ux): Understand if we are loosing quality here
	intData = StereoToMono(intData)
	return &audio.IntBuffer{
		Format: &audio.Format{
			NumChannels: 1, // original was 2, but we prefer to operate with 1 in vocode-golang
			SampleRate:  sampleRate,
		},
		Data:           intData,
		SourceBitDepth: 16,
	}, nil
}

func TwoByteDataToIntSlice(audioData []byte) []int {
	intData := make([]int, len(audioData)/2)
	for i := 0; i < len(audioData); i += 2 {
		// Convert the pCapturedSamples byte slice to int16 slice for FormatS16 as we go
		value := int(binary.LittleEndian.Uint16(audioData[i : i+2]))
		intData[i/2] = value
	}
	return intData
}

// StereoToMono was generated by GPT4.
func StereoToMono(stereo []int) []int {
	if len(stereo)%2 != 0 {
		log.Error().Err(fmt.Errorf("invalid stereo data: must have an even number of samples")).Msg("StereoToMono")
		return stereo
	}

	mono := make([]int, len(stereo)/2)
	for i := 0; i < len(stereo); i += 2 {
		left := stereo[i]
		right := stereo[i+1]
		mono[i/2] = (left + right) / 2
	}
	return mono
}

// ResampleSimple resamples the input audio data from inputSampleRate to outputSampleRate.
// The most straightforward approach for resampling is linear interpolation, suitable for small changes in sample rates.
// https://chat.openai.com/share/22c33099-f66c-4b90-b2aa-4d03ccb8e7fb
//
// TODO(P0, ux): For better quality, more sophisticated techniques like polyphase filtering or windowed Sinc interpolation are preferred.
// UNFORTUNATELY, all or most of them require an external library like ffmpeg, libsoxr, libsamplerate, libswresample
func ResampleSimple(input []int, inputSampleRate, outputSampleRate int) []int {
	if inputSampleRate == outputSampleRate {
		return input
	}

	inputLength := len(input)
	outputLength := int(float64(inputLength) * (float64(outputSampleRate) / float64(inputSampleRate)))
	output := make([]int, outputLength)

	for i := 0; i < outputLength-1; i++ {
		inputIndex := float64(i) * (float64(inputSampleRate) / float64(outputSampleRate))
		lowerIndex := int(inputIndex)
		upperIndex := lowerIndex + 1
		if upperIndex >= inputLength {
			upperIndex = inputLength - 1
		}

		t := inputIndex - float64(lowerIndex)
		output[i] = int((1-t)*float64(input[lowerIndex]) + t*float64(input[upperIndex]))
	}

	// Handle the last sample explicitly to avoid index out of range
	output[outputLength-1] = input[inputLength-1]

	return output
}

func dbg(err error) {
	if err != nil {
		log.Debug().Err(err).Msg("sth non-essential failed")
	}
}
