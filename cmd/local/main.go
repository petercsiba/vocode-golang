package main

import (
	"bytes"
	"errors"
	"fmt"
	"github.com/hajimehoshi/go-mp3"
	"github.com/joho/godotenv"
	"github.com/petrzlen/vocode-golang/internal/utils"
	"github.com/petrzlen/vocode-golang/pkg/agent"
	"github.com/petrzlen/vocode-golang/pkg/audioio"
	"github.com/petrzlen/vocode-golang/pkg/models"
	"github.com/petrzlen/vocode-golang/pkg/synthesizer"
	"github.com/petrzlen/vocode-golang/pkg/transcriber"
	"github.com/sashabaranov/go-openai"
	"io"
	"os"
	"os/signal"
	"runtime/debug"
	"strings"
	"syscall"
	"time"

	"github.com/rs/zerolog/log"
)

// MinTextBufferForTtsCharLength is mostly to prevent saying like "1,"
// in other cases it's best to just start as soon as first chat completions arrive.
const MinTextBufferForTtsCharLength = 3

// OpenAiSampleRate - this I have measured by decodedMp3.SampleRate
const OpenAiSampleRate = 24000

func setupSignalHandler(cleanup func()) {
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGINT, syscall.SIGSEGV)

	go func() {
		sig := <-sigs
		log.Info().Msgf("Received signal: %v\n", sig)

		cleanup()

		// Exit if necessary
		os.Exit(1)
	}()
}

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

func textToSpeechAndEncodeRoutine(tts synthesizer.Synthesizer, textChan <-chan string, audioOutputChan chan<- models.AudioData) {
	log.Info().Msgf("textToSpeechAndEncodeRoutine started")
	var buffer string

	firstEligibleBuffer := true
	for {
		select {
		case text, ok := <-textChan:
			if ok {
				buffer += text
			}
			// log.Trace().Str("text", text).Bool("ok", ok).Str("buffer", buffer).Msg("text received")
			if (len(buffer) > MinTextBufferForTtsCharLength && isPunctuationMarkAtEnd(buffer)) || (!ok && buffer != "") {
				if firstEligibleBuffer {
					log.Warn().Msg("TRACING HACK: first eligible buffer triggered")
					firstEligibleBuffer = false
				}
				// Process the buffer;
				// Speed 1.15 was reverse engineered from the ChatGPT app
				audioOutput, err := tts.CreateSpeech(buffer, 1.15)
				if err == nil {
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
func playAudioChunksRoutine(audioOutput audioio.OutputDevice, rawAudioBytesChan chan []byte) {
	log.Info().Msgf("playAudioChunksRoutine started")

	i := 0
	for rawAudioBytes := range rawAudioBytesChan {
		i += 1
		if i <= 2 { // Doing 2, cause first is filler word.
			log.Warn().Int("num", i).Msg("TRACING HACK: tts received")
		}

		// log.Debug().Msgf("attempting to play %d bytes of mp3", len(rawAudioBytes))
		startTime := time.Now()

		// TODO(prod, P0): Only do this locally to debug stuff
		debugFilename := fmt.Sprintf("output/%d.mp3", i)
		err := os.WriteFile(debugFilename, rawAudioBytes, 0644)
		if err != nil {
			log.Debug().Msgf("cannot write debug file %s", debugFilename)
		}

		// TODO: Can we just Play the rawAudioBytes in here?
		decodedMp3, decodedMp3Err := mp3.NewDecoder(bytes.NewReader(rawAudioBytes))
		if decodedMp3Err != nil && !errors.Is(decodedMp3Err, io.EOF) {
			log.Error().Err(decodedMp3Err).Msg("mp3.NewDecoder failed, skipping chunk")
			continue
		}
		log.Debug().Int("sample_rate", decodedMp3.SampleRate()).Int64("byte_size", decodedMp3.Length()).Msg("player START")

		waitTilDone, err := audioOutput.Play(decodedMp3) // Sub-millisecond time
		if i == 1 {
			log.Warn().Msg("TRACING HACK: first playback started")
		}
		if err != nil {
			log.Error().Err(err).Msgf("cannot play decoded mp3")
		} else {
			waitTilDone.Wait()
		}

		log.Debug().Dur("duration", time.Since(startTime)).Msg("player DONE")
	}
	log.Info().Msgf("playAudioChunksRoutine finished")
}

func compareToFullTranscript(transcriber transcriber.Transcriber, wavBytes []byte, finalTranscriptFromSlices string) {
	fullResult, err := transcriber.SendAudio(bytes.NewReader(wavBytes), "wav", "")
	dbg(err)
	log.Info().Str("full_transcript", fullResult).Str("sliced_together_transcript", finalTranscriptFromSlices).Msg("comparing full transcript to from slices")
}

func fillerWordRoutine(chatAgent agent.ChatAgent, tts synthesizer.Synthesizer, earlyTranscriptChan chan string, audioOutputChan chan models.AudioData) {
	log.Info().Msgf("fillerWordRoutine START")
	// This is the 5-10
	earlyTranscript := <-earlyTranscriptChan
	log.Info().Msgf("fillerWordRoutine received earlyTranscript %s", earlyTranscript)

	fillerWords := "Uhm, ..."
	if len(earlyTranscript) < 20 {
		log.Debug().Msg("earlyTranscript too short")
	} else {
		//prompt := fmt.Sprintf("I want to respond to this input text with a few filler words; example 1: Hm, San Francisco... Example 2: Alright, your fathers birthday... Input text: %s", earlyTranscript)
		//prompt := fmt.Sprintf("Generate a few filler words with mentioning the topic to be used while i am thinking. Only output the filler words, up to 5 words. The input text: %s", earlyTranscript)
		//fillerPrompt := fmt.Sprintf("generate the most appropriate filler word to this transcript, only output a single word: %s", earlyTranscript)
		//fillerPromptResult := make(chan string, 1000)
		//go executeChatRequest(client, "gpt-3.5-turbo", fillerPrompt, fillerPromptResult)
		//// go executeChatRequest(client, "gpt-4", prompt, promptResult)
		//fillerWords := "... "
		//for token := range fillerPromptResult {
		//	fillerWords += token
		//}
		//fillerWords += " "

		fillerWords = "Hmm got it, "
		topicPrompt := fmt.Sprintf("what is the main object/subject asked for in this transcript, only return the object/subject name using maximum of 3 words: %s", earlyTranscript)
		topicPromptResult := make(chan string, 1000)
		go func() {
			dbg(chatAgent.RunPrompt(agent.SlowerAndSmarter, models.NewConversationSimple(topicPrompt), topicPromptResult))
		}()
		for token := range topicPromptResult {
			fillerWords += token
		}
		fillerWords += "... ."
	}

	// Speed 1.0, filler words are more natural to produce slow.
	fillerWordAudioBytes, err := tts.CreateSpeech(fillerWords, 1.0)
	if err == nil {
		log.Info().Msgf("generating filler words %s", fillerWords)
		audioOutputChan <- fillerWordAudioBytes
	} else {
		log.Error().Err(err).Msg("cannot generate filler words")
	}
	log.Info().Msgf("fillerWordRoutine END")
}

// Goroutine to handle user input
func userInterruptRoutine(stopChan chan struct{}) {
	fmt.Println("Press Enter to stop output and make new input...")
	_, err := fmt.Scanln()
	dbg(err)
	close(stopChan) // Send interrupt signal
}

func playTTSUntilInterruptRoutine(ttsOutputBuffer chan models.AudioData, audioToPlayChan chan []byte) string {
	log.Info().Msg("playTTSUntilInterruptRoutine START")
	interruptChan := make(chan struct{})
	go userInterruptRoutine(interruptChan)

	var outputText strings.Builder
	// Main loop for processing ttsOutputBuffer
	for {
		select {
		case ttsOutput, ok := <-ttsOutputBuffer:
			if !ok {
				log.Info().Msg("ttsOutputBuffer closed. playTTSUntilInterruptRoutine STOP")
				return outputText.String()
			}
			select {
			// Plays the audio
			case audioToPlayChan <- ttsOutput.ByteData:
				outputText.WriteString(ttsOutput.Text)
			case <-interruptChan:
				log.Info().Msg("Interrupt received. playTTSUntilInterruptRoutine STOP")
				return outputText.String()
			}
		case <-interruptChan:
			log.Info().Msg("Interrupt received. playTTSUntilInterruptRoutine STOP")
			return outputText.String()
		}
	}
	// TODO: ideally we should call outputAudio.Stop() -- to not wait until the entire sentence is done
}

// Based off their Python version of the code https://cookbook.openai.com/examples/how_to_stream_completions
// Translated with GPT-4: https://chat.openai.com/c/c723eeaa-2c24-42c2-aabb-0f5582d0f031
// Using https://github.com/sashabaranov/go-openai/blob/d6f3bdcdac9172ab5248d6be8c3e1761446a434c/chat_stream.go#L62
func main() {
	setupStart := time.Now()
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

	audioOutput, err := audioio.NewSpeakers(OpenAiSampleRate, 2)
	ftl(err)

	log.Debug().Dur("setup_time", time.Since(setupStart)).Msg("setup done")
	// ==== SETUP DONE

	runLoop := true
	setupSignalHandler(func() {
		runLoop = false
	})

	audioToPlayChan := make(chan []byte) // non-buffer
	inputAudioChunksChan := make(chan models.AudioData, 100000)
	inputTextChunksChan := make(chan models.AudioData, 100000)
	earlyTranscriptChan := make(chan string, 10)
	go playAudioChunksRoutine(audioOutput, audioToPlayChan)
	go transcriber.TranscribeAudioRoutine(whisper, inputAudioChunksChan, inputTextChunksChan, earlyTranscriptChan)

	fullConvo := &models.Conversation{}

	i := 0
	for runLoop {
		i++
		chatOutputChan := make(chan string, 100000)
		ttsOutputBuffer := make(chan models.AudioData, 3)

		// TODO: feels to be allocating too much but shrug
		// finalTranscriptChan := make(chan string, 1)
		go fillerWordRoutine(chatAgent, tts, earlyTranscriptChan, ttsOutputBuffer)

		audioInput, err := audioio.NewMicrophone() // About 200ms
		ftl(err)
		err = audioInput.StartRecording(inputAudioChunksChan)
		ftl(err)

		fmt.Println("Press Enter to submit your input...")
		_, err = fmt.Scanln()
		dbg(err)

		entireWavRecording, err := audioInput.StopRecording()
		dbg(err)

		// For debug purposes write the output to a real file so we can replay it.
		dbg(os.WriteFile(fmt.Sprintf("output/entire-recording-%d.wav", i), entireWavRecording, 0644))

		chatPrompt := ""
		for inputTextChunk := range inputTextChunksChan {
			if inputTextChunk.EventType == models.SubmitPrompt {
				break
			}
			chatPrompt += inputTextChunk.Text + " "
		}
		fullConvo.Add("user", chatPrompt)
		// Just for debug
		go compareToFullTranscript(whisper, entireWavRecording, chatPrompt)

		// Documentation for the chat and rawAudio routines intent / design:
		// https://chat.openai.com/share/9ae89c13-9f66-4500-b719-dcd07dd6454d
		go textToSpeechAndEncodeRoutine(tts, chatOutputChan, ttsOutputBuffer)

		// TODO(P2, mem-leaks): Better propagate errors so channels can be properly closed.
		go func() {
			dbg(chatAgent.RunPrompt(agent.SlowerAndSmarter, fullConvo, chatOutputChan))
		}()
		// TODO: Use the assistant, allPrompts is too hacky lol

		outputText := playTTSUntilInterruptRoutine(ttsOutputBuffer, audioToPlayChan)
		// TODO(P0, ux): We have to stop the Chat and TTS routines to free up the API resources.

		fullConvo.Add("assistant", outputText)
		fullConvo.DebugLog()
	}
}

func dbg(err error) {
	if err != nil {
		log.Debug().Err(err).Msg("sth non-essential failed")
		debug.PrintStack()
	}
}

func ftl(err error) {
	if err != nil {
		log.Fatal().Err(err).Msg("sth essential failed")
		debug.PrintStack()
	}
}
