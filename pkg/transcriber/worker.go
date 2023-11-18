package transcriber

import (
	"bytes"
	"github.com/petrzlen/vocode-golang/pkg/models"
	"github.com/rs/zerolog/log"
	"strings"
	"time"
)

// TranscribeAudioRoutine is intended to run for the entire lifespan of a conversation
func TranscribeAudioRoutine(transcriber Transcriber, audioChunksChan chan models.AudioData, textChunksChan chan models.AudioData, earlyTranscriptChan chan string) string {
	log.Info().Msgf("TranscribeAudioRoutine started")

	var earlyTranscriptStartTime *time.Time
	sendEarlyTranscript := true

	// Replace 'client' and 'transcribeAudio' with your actual client and function
	var transcriptBuilder strings.Builder
	transcriptRepetitions := 0

	for audioChunk := range audioChunksChan {
		if earlyTranscriptStartTime == nil {
			earlyTranscriptStartTime = &audioChunk.Trace.CreatedAt
		}
		audioChunk.Trace.ReceivedAt = time.Now()

		if audioChunk.EventType == models.SubmitPrompt {
			log.Info().Msg("TranscribeAudioRoutine encountered SubmitPrompt; will clear state to start working on the next")

			transcriptBuilder.Reset()
			earlyTranscriptStartTime = nil
			sendEarlyTranscript = true

			textChunksChan <- audioChunk
			continue
		}

		recordingBytes := audioChunk.ByteData
		previousWords := transcriptBuilder.String()
		transcript, err := transcriber.SendAudio(bytes.NewReader(recordingBytes), "wav", previousWords)
		if err != nil {
			log.Error().Err(err).Int("wav_chunk_byte_length", len(recordingBytes)).Msg("cannot transcribe audio, skipping chunk")
			continue
		}
		// TODO(P0, ux): Here, we need to detect if a question was finished, interrupt voiced or passed turn to agent
		// E.g. silence in whisper can be repeating last prompt words over and over like:
		// * .. in 100 words. All right. All right. Well, please, let's do it. All right. Go. All right. All right.
		// TODO: Add audio length here as a threshold
		if len(transcript) >= 3 && strings.HasSuffix(previousWords, transcript) {
			transcriptRepetitions += 1
		} else {
			transcriptRepetitions = 0
		}
		if transcriptRepetitions >= 2 {
			log.Info().Msgf("transcripts repeated itself for %d times, gonna submit prompt. Transcript: %s", transcriptRepetitions, transcript)
			textChunksChan <- models.NewAudioDataSubmit("transcriber.worker")
			continue
		}
		if transcriptRepetitions > 0 {
			log.Info().Msgf("transcript repeated previous words, skipping audio for: %s", transcript)
			continue
		}

		transcriptBuilder.WriteString(" ")
		transcriptBuilder.WriteString(transcript)

		audioChunk.Text = transcript
		audioChunk.Trace.ProcessedAt = time.Now()
		audioChunk.Trace.Processor = "transcribe_open_ai_whisper"
		audioChunk.Trace.Log()
		textChunksChan <- audioChunk

		if sendEarlyTranscript && time.Since(*earlyTranscriptStartTime).Seconds() > 7 {
			sendEarlyTranscript = false
			select {
			case earlyTranscriptChan <- transcriptBuilder.String():
				log.Info().Msgf("TranscribeAudioRoutine sending earlyTranscript")
			default:
				log.Warn().Msgf("could NOT send earlyTranscript cause channel full")
			}
		}
	}

	finalTranscript := transcriptBuilder.String()
	log.Info().Msgf("TranscribeAudioRoutine ended with finalTranscript %s", finalTranscript)
	close(textChunksChan)
	return finalTranscript
}
