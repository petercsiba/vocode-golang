package synthesizer

import (
	"fmt"
	"github.com/petrzlen/vocode-golang/pkg/models"
	"github.com/rs/zerolog/log"
	"os"
)

// MinTextBufferForTtsCharLength is mostly to prevent saying like "1,"
// in other cases it's best to just start as soon as first chat completions arrive.
const MinTextBufferForTtsCharLength = 3

func isPunctuationMarkAtEnd(s string) bool {
	if len(s) == 0 {
		return false
	}
	lastChar := s[len(s)-1]
	switch lastChar {
	case ',', '.', '?', '!', ';', ':':
		return true
	default:
		return false
	}
}

// TextToSpeechAndEncodeRoutine
// TODO: We should implement some interrupt / stoppage here.
func TextToSpeechAndEncodeRoutine(tts Synthesizer, textChan <-chan string, audioOutputChan chan<- models.AudioData) {
	log.Info().Msgf("textToSpeechAndEncodeRoutine started")
	var buffer string

	i := 0
	for {
		select {
		case text, ok := <-textChan:
			if ok {
				buffer += text
			}
			// log.Trace().Str("text", text).Bool("ok", ok).Str("buffer", buffer).Msg("text received")
			if (len(buffer) > MinTextBufferForTtsCharLength && isPunctuationMarkAtEnd(buffer)) || (!ok && buffer != "") {
				i++
				if i == 1 {
					log.Warn().Msg("TRACING HACK: first eligible buffer triggered")
				}
				// Process the buffer;
				// Speed 1.15 was reverse engineered from the ChatGPT app
				audioOutput, err := tts.CreateSpeech(buffer, 1.15)
				if err == nil {
					// TODO(prod, P1): Only do this locally to debug stuff
					debugFilename := fmt.Sprintf("output/tts-%d.%s", i, audioOutput.Format)
					dbg(os.WriteFile(debugFilename, audioOutput.ByteData, 0644))

					audioOutputChan <- audioOutput
				} else {
					log.Error().Msgf("cannot buffer tts text for %s cause %v", buffer, err)
				}
				buffer = "" // Clear the buffer after processing
			}
			if !ok {
				log.Info().Msgf("textToSpeechAndEncodeRoutine ended")
				return
			}
		}
	}
}

func dbg(err error) {
	if err != nil {
		log.Debug().Err(err).Msg("sth non-essential failed")
	}
}
