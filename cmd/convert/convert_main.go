package main

import (
	"encoding/base64"
	"fmt"
	"github.com/go-audio/audio"
	"github.com/petrzlen/vocode-golang/internal/utils"
	"github.com/petrzlen/vocode-golang/pkg/audio_utils"
	"github.com/rs/zerolog/log"
	"os"
	"runtime/debug"
)

const twilioBase64Mulaw = "e/Pu8P11cXf99vb8eHN3/vf2/HZsbv/v6/B7bm149/D3fHd98+30d2xtevLs735ubHX06+79cm959e7zfHR2//j4fnd2eXz++/f4/Ht0c3r47/D7eHf+9PT/b2xz9uvt/W5sdvTt83ltbXb8+Pt8ev339/x7en759fp8d3r89/j9fHv/+/v/e3l5eHd6e3t9///++/n6/Pz+/ff2+n58fA=="

type EncodingConfig struct {
	SampleRate  int
	BitDepth    int
	NumChannels int
	AudioFormat int
}

// TODO(lazy peter): Make this into a proper Golang test.
func testMulawWav() *audio.IntBuffer {
	mulawBytes, err := base64.StdEncoding.DecodeString(twilioBase64Mulaw)
	ftl(err)
	fmt.Printf("%v\n", mulawBytes)

	intBuffer := audio_utils.DecodeFromMulaw(mulawBytes, 8000)
	fmt.Printf("%v\n", *intBuffer)

	recodedMulawBytes, err := audio_utils.EncodeToMulaw(intBuffer, 8000)
	ftl(err)

	recodedMulawBase64 := base64.StdEncoding.EncodeToString(recodedMulawBytes)
	if twilioBase64Mulaw != recodedMulawBase64 {
		fmt.Println("mulaws do not match")
		fmt.Println(twilioBase64Mulaw)
		fmt.Println(recodedMulawBase64)
	}

	return intBuffer
}

func testWav(intBuffer *audio.IntBuffer) {
	configs := []EncodingConfig{{
		SampleRate:  8000,
		BitDepth:    16,
		NumChannels: 1,
		AudioFormat: 7,
	}, {
		SampleRate:  16000, // higher sample size
		BitDepth:    16,
		NumChannels: 1,
		AudioFormat: 7,
	}, {
		SampleRate:  8000,
		BitDepth:    8, // lower bitDepth
		NumChannels: 1,
		AudioFormat: 7,
	}, {
		SampleRate:  8000,
		BitDepth:    16,
		NumChannels: 2, // higher channels
		AudioFormat: 7,
	}, {
		SampleRate:  8000,
		BitDepth:    16,
		NumChannels: 1,
		AudioFormat: 1, // PCM audio format
	}}
	for _, cfg := range configs {
		// wavData, err := audio_utils.EncodeToWav(intData, cfg.SampleRate, cfg.BitDepth, cfg.NumChannels, cfg.AudioFormat)
		wavData, err := audio_utils.EncodeToWav(intBuffer, cfg.BitDepth, cfg.AudioFormat)
		ftl(err)
		fmt.Printf("%v\n", wavData)

		filename := fmt.Sprintf("output/%d-%d-%d-%d.wav", cfg.SampleRate, cfg.BitDepth, cfg.NumChannels, cfg.AudioFormat)
		ftl(os.WriteFile(filename, wavData, 0644))
	}
	/* Then you can visualize the diffs, for example you can see that wav.Encoder does NOT resample stuff (only updates header)
	F1=output/8000-16-1-7.wav; F2=output/16000-16-1-7.wav; xxd $F1 > $F1.xxd; xxd $F2 > $F2.xxd; diff $F1.xxd $F2.xxd
	2c2
	< 00000010: 1000 0000 0700 0100 401f 0000 803e 0000  ........@....>..
	---
	> 00000010: 1000 0000 0700 0100 803e 0000 007d 0000  .........>...}..
	*/
}

func main() {
	utils.SetupZerolog()
	testMulawWav()
}

func ftl(err error) {
	if err != nil {
		log.Fatal().Err(err).Msg("sth essential failed")
		debug.PrintStack()
	}
}
