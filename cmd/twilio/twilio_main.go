/*
To live test it:
flyctl deploy
flyctl launch
ngrok http 8081
# take the printed url s/https/wss and update the Twiml config (if not already)
# https://console.twilio.com/us1/develop/phone-numbers/manage/incoming
# <?xml version="1.0" encoding="UTF-8"?>
# <Response>
#    <Connect>
#        <!-- <Stream url="wss://vocode-golang.fly.dev/ws" /> -->
#      	<Stream url="wss://7e98-24-130-57-37.ngrok-free.app/ws" />
#    </Connect>
# </Response>
go run cmd/twilio/twilio_main.go

# websocat wss://vocode-golang.fly.dev/ws
# call your Twilio number
*/
package main

import (
	"github.com/joho/godotenv"
	"github.com/petrzlen/vocode-golang/internal/networking"
	"github.com/petrzlen/vocode-golang/internal/utils"
	"github.com/petrzlen/vocode-golang/pkg/agent"
	"github.com/petrzlen/vocode-golang/pkg/audioio"
	"github.com/petrzlen/vocode-golang/pkg/models"
	"github.com/petrzlen/vocode-golang/pkg/synthesizer"
	"github.com/petrzlen/vocode-golang/pkg/transcriber"
	"github.com/pkg/errors"
	"github.com/rs/zerolog/log"
	"github.com/sashabaranov/go-openai"
	"net/http"
	"os"
	"runtime/debug"
)

func submitChatPromptRoutine(chatAgent agent.ChatAgent, transcribedTextChan chan models.AudioData, allChatOutputChan chan string) {
	var fullConvo models.Conversation
	fullConvo.Add("assistant", "You are an agent on a phone call, be concise.")

	chatPrompt := ""
	for inputTextChunk := range transcribedTextChan {
		// NOTE: This also gets emitted when silence is detected, and if there is too much silence it can spam this.
		if inputTextChunk.EventType == models.SubmitPrompt {
			// TODO: There are garbage prompts like " ", " You ", "Bye-bye" which we should ignore
			// IDEA: We might want to use words-per-minute as good detection if someone is speaking or not.
			// * i.e. we can refactor models.AudioData into input/output ones, and trace steps for it,
			//   then we submit ONLY IF there were say three words said within 3 seconds or so.
			if len(chatPrompt) < 15 {
				log.Warn().Msgf("chatPrompt is too short %d, skipping", len(chatPrompt))
				chatPrompt = ""
				continue
			}

			fullConvo.Add("user", chatPrompt)
			chatPrompt = ""

			// TODO: this can lead to multiple agents producing at the same time.
			subChatOutputChan := make(chan string, 10)
			go func() {
				// TODO: memory of what system said
				errLog(chatAgent.RunPrompt(agent.SlowerAndSmarter, fullConvo, subChatOutputChan), "chatAgent.RunPrompt")
			}()
			go func() {
				for chatOutput := range subChatOutputChan {
					allChatOutputChan <- chatOutput
				}
			}()
		}
		chatPrompt += inputTextChunk.Text + " "
	}
}

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
	chatAgent := agent.NewOpenAIChatAgent(client)
	tts := synthesizer.NewOpenAITTS(openAIAPIKey)

	twilioHandlerFactory := func() networking.WebsocketMessageHandler {
		handler := audioio.NewTwilioHandler()

		inputAudioChunksChan := make(chan models.AudioData, 100000)
		inputTextChunksChan := make(chan models.AudioData, 100000)
		earlyTranscriptChan := make(chan string, 10)

		allChatOutputChan := make(chan string, 100000)
		allChatOutputChan <- "Hi this is Voxana AMA, ask me anything."
		audioToPlayChan := make(chan models.AudioData) // non-buffer

		go transcriber.TranscribeAudioRoutine(whisper, inputAudioChunksChan, inputTextChunksChan, earlyTranscriptChan)
		go synthesizer.TextToSpeechAndEncodeRoutine(tts, allChatOutputChan, audioToPlayChan)

		go submitChatPromptRoutine(chatAgent, inputTextChunksChan, allChatOutputChan)

		// TODO: need to add output buffer to collect what was actually played
		go audioio.PlayAudioChunksRoutine(handler, audioToPlayChan)

		ftl(handler.StartRecording(inputAudioChunksChan))

		return handler
	}

	// For fly.io
	port := os.Getenv("PORT")
	if port == "" {
		port = "8081"
	}

	http.HandleFunc("/ws", networking.NewWebsocketHandlerFunc(twilioHandlerFactory))
	ftl(http.ListenAndServe(":"+port, nil))
}

func ftl(err error) {
	if err != nil {
		log.Fatal().Err(err).Msg("sth essential failed")
		debug.PrintStack()
	}
}

func errLog(err error, what string) {
	if err != nil {
		log.Error().Err(errors.WithStack(err)).Msg(what)
	}
}
