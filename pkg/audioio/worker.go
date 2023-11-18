package audioio

import (
	"fmt"
	"github.com/go-audio/audio"
	"github.com/petrzlen/vocode-golang/pkg/audio_utils"
	"github.com/petrzlen/vocode-golang/pkg/models"
	"github.com/rs/zerolog/log"
	"os"
	"time"
)

func PlayAudioChunksRoutine(outputDevice OutputDevice, audioDataChan chan models.AudioData) {
	log.Info().Msgf("playAudioChunksRoutine started")

	i := 0
	for audioData := range audioDataChan {
		rawAudioBytes := audioData.ByteData
		fileFormat := audioData.Format

		i += 1
		if i <= 2 { // Doing 2, cause first is filler word.
			log.Warn().Int("num", i).Msg("TRACING HACK: tts received")
		}

		// log.Debug().Msgf("attempting to play %d bytes of mp3", len(rawAudioBytes))
		startTime := time.Now()

		// TODO(prod, P1): Only do this locally to debug stuff

		debugRawFilename := fmt.Sprintf("output/player-raw-%d.%s", i, fileFormat)
		dbg(os.WriteFile(debugRawFilename, rawAudioBytes, 0644))

		var intBuffer *audio.IntBuffer
		var err error = nil
		switch fileFormat {
		case "mp3":
			intBuffer, err = audio_utils.DecodeFromMp3(rawAudioBytes)
		case "flac":
			intBuffer, err = audio_utils.DecodeFromFlac(rawAudioBytes)

		default:
			log.Error().Msgf("unknown fileFormat for rawAudioBytes: %s", fileFormat)
		}

		if err != nil {
			log.Error().Err(err).Msg("mp3.NewDecoder failed, skipping chunk")
			continue
		}

		debugDecodedFilename := fmt.Sprintf("output/player-decoded-%d.intData", i)
		dbg(os.WriteFile(debugDecodedFilename, []byte(fmt.Sprintf("%v", intBuffer.Data)), 0644))

		waitTilDone, err := outputDevice.Play(intBuffer) // Sub-millisecond time
		if i == 1 {
			log.Warn().Msg("TRACING HACK: first playback started")
		}
		if err != nil {
			log.Error().Err(err).Msg("cannot play decoded in-memory wav")
		} else if waitTilDone != nil {
			waitTilDone.Wait()
		}

		log.Debug().Dur("duration", time.Since(startTime)).Msg("player DONE")
	}
	log.Info().Msgf("playAudioChunksRoutine finished")
}
