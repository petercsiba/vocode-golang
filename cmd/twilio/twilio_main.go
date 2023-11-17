package main

import (
	"github.com/joho/godotenv"
	"github.com/petrzlen/vocode-golang/internal/networking"
	"github.com/petrzlen/vocode-golang/internal/utils"
	"github.com/petrzlen/vocode-golang/pkg/audioio"
	"github.com/petrzlen/vocode-golang/pkg/models"
	"github.com/petrzlen/vocode-golang/pkg/transcriber"
	"github.com/rs/zerolog/log"
	"github.com/sashabaranov/go-openai"
	"net/http"
	"os"
	"runtime/debug"
)

func main() {
	utils.SetupZerolog()

	// Load the .env file
	err := godotenv.Load()
	if err != nil {
		log.Warn().Msgf("Cannot load .env file")
	}
	openAIAPIKey := os.Getenv("OPEN_AI_API_KEY")
	if openAIAPIKey == "" {
		log.Panic().Msgf("OPEN_AI_API_KEY is not set")
	}
	client := openai.NewClient(openAIAPIKey)

	whisper := transcriber.NewOpenAIWhisper(client)

	twilioHandlerFactory := func() networking.WebsocketMessageHandler {
		inputAudioChunksChan := make(chan models.AudioData, 100000)
		inputTextChunksChan := make(chan models.AudioData, 100000)
		earlyTranscriptChan := make(chan string, 10)

		go transcriber.TranscribeAudioRoutine(whisper, inputAudioChunksChan, inputTextChunksChan, earlyTranscriptChan)

		handler := audioio.NewTwilioHandler()
		ftl(handler.StartRecording(inputAudioChunksChan))

		return handler
	}

	http.HandleFunc("/ws", networking.NewWebsocketHandlerFunc(twilioHandlerFactory))
	ftl(http.ListenAndServe(":8081", nil))
}

func ftl(err error) {
	if err != nil {
		log.Fatal().Err(err).Msg("sth essential failed")
		debug.PrintStack()
	}
}
