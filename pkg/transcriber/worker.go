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
		// TODO: This should reset
		previousWords := transcriptBuilder.String()
		transcript, err := transcriber.SendAudio(bytes.NewReader(recordingBytes), "wav", previousWords)
		if err != nil {
			log.Error().Err(err).Int("wav_chunk_byte_length", len(recordingBytes)).Msg("cannot transcribe audio, skipping chunk")
			continue
		}
		transcriptBuilder.WriteString(transcript)
		transcriptBuilder.WriteString(" ")
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
