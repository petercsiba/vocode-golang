package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"github.com/hajimehoshi/go-mp3"
	"github.com/joho/godotenv"
	"github.com/petrzlen/vocode-golang/pkg/agent"
	"github.com/petrzlen/vocode-golang/pkg/audioio"
	"github.com/petrzlen/vocode-golang/pkg/models"
	"github.com/petrzlen/vocode-golang/pkg/synthesizer"
	"github.com/sashabaranov/go-openai"
	"io"
	"os"
	"os/signal"
	"regexp"
	"runtime/debug"
	"strings"
	"syscall"
	"time"

	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
)

// MinTextBufferForTtsCharLength is mostly to prevent saying like "1,"
// in other cases it's best to just start as soon as first chat completions arrive.
const MinTextBufferForTtsCharLength = 3

// OpenAiSampleRate - this I have measured by decodedMp3.SampleRate
const OpenAiSampleRate = 24000

func setupSignalHandler() {
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGINT, syscall.SIGSEGV)

	go func() {
		sig := <-sigs
		log.Error().Msgf("Received signal: %v\n", sig)

		// Print stack trace
		debug.PrintStack()

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

func textToSpeechAndEncodeRoutine(tts synthesizer.Synthesizer, textCh <-chan string, rawAudioBytesCh chan<- []byte) {
	log.Info().Msgf("textToSpeechAndEncodeRoutine started")
	var buffer string

	firstEligibleBuffer := true
	for {
		select {
		case text, ok := <-textCh:
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
				rawAudioBytes, err := tts.CreateSpeech(buffer, 1.15)
				if err == nil {
					rawAudioBytesCh <- rawAudioBytes
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

func playAudioChunksRoutine(audioOutput audioio.OutputDevice, rawAudioBytesCh chan []byte) {
	log.Info().Msgf("playAudioChunksRoutine started")

	i := 0
	for rawAudioBytes := range rawAudioBytesCh {
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
}

// removeNonEnglishAndMBC removes non-English characters and the "MBC" string from the input text.
// TODO: HACK, somewhat "silence" is transcribed with random Chinese characters for example:
// MBC 뉴스 이덕영입니다. Yeah, tell me. a bit about uh, written  in 100 words.  MBC 뉴스 이덕영입니다.
func removeNonEnglishAndMBC(text string) string {
	// Regular expression to match non-English characters.
	nonEnglishRegex := regexp.MustCompile(`[^\x00-\x7F]+`)
	text = nonEnglishRegex.ReplaceAllString(text, "")

	// Remove all occurrences of "MBC".
	text = strings.ReplaceAll(text, "MBC", "")

	return text
}

// TODO(P1, latency): Figure out by how much mp3 is faster than .WAV
//
//	3 tests on a 260KB wav vs 67KB mp3 it seems maybe 1100ms vs 1000ms, but there was a run when wav beat mp3 :/
func transcribeAudio(client *openai.Client, input io.Reader, fileExtension string, prompt string) (result string, err error) {
	startTime := time.Now()
	// TODO(P0, ux): Try running Whisper locally for quicker transcription speeds (and maybe no filler words needed).
	req := openai.AudioRequest{
		Model:    "whisper-1",
		Reader:   input,
		FilePath: fmt.Sprintf("this-file-does-not-exist-just-needs-extension.%s", fileExtension),
		// FilePath: "output/tell-me-about-ba.mp3",
		// NOTE: Giving the model the previous words improves accuracy.
		// Whisper can take up to 244 tokens, if more are passed than only the last are used.
		// TODO(P0, ux): Adding prompt with previous words should improve transcription
		// Language: "en",
		Prompt: prompt,
	}

	log.Debug().Str("model", req.Model).Str("prompt", prompt).Msg("create transcription request")
	resp, err := client.CreateTranscription(context.Background(), req)
	if err != nil {
		err = fmt.Errorf("cannot create transcription %w", err)
		return
	}

	//var contentBuilder strings.Builder
	//for _, segment := range resp.Segments {
	//	contentBuilder.WriteString(segment.Text)
	//}
	//result = contentBuilder.String()

	// TODO: Better "silence" detection
	result = removeNonEnglishAndMBC(resp.Text)
	if result != resp.Text {
		log.Info().Str("original_text", resp.Text).Str("processed_text", result).Msg("transcription post-processing removed some text")
	}

	log.Debug().Str("transcription", result).Dur("time_elapsed", time.Since(startTime)).Msg("received transcription")
	return
}

func transcribeAudioRoutine(client *openai.Client, audioChunksChan chan models.AudioData, finalTranscriptChan chan string, earlyTranscriptChan chan string) {
	log.Info().Msgf("transcribeAudioRoutine started")
	routineStart := time.Now()
	sendEarlyTranscript := true

	// Replace 'client' and 'transcribeAudio' with your actual client and function
	var transcriptBuilder strings.Builder
	for audioChunk := range audioChunksChan {
		audioChunk.Trace.ReceivedAt = time.Now()

		recordingBytes := audioChunk.ByteData
		previousWords := transcriptBuilder.String()
		transcript, err := transcribeAudio(client, bytes.NewReader(recordingBytes), "wav", previousWords)
		if err != nil {
			log.Error().Err(err).Int("wav_chunk_byte_length", len(recordingBytes)).Msg("cannot transcribe audio, skipping chunk")
			continue
		}
		transcriptBuilder.WriteString(transcript + " ")

		audioChunk.Trace.ProcessedAt = time.Now()
		audioChunk.Trace.Processor = "transcribe_open_ai_whisper"
		audioChunk.Trace.Log()

		if sendEarlyTranscript && time.Since(routineStart).Seconds() > 7 {
			sendEarlyTranscript = false
			log.Info().Msgf("transcribeAudioRoutine sending earlyTranscript")
			earlyTranscriptChan <- transcriptBuilder.String()
		}
	}

	finalTranscript := transcriptBuilder.String()
	log.Info().Msgf("transcribeAudioRoutine ended with finalTranscript %s", finalTranscript)
	finalTranscriptChan <- finalTranscript
}

func compareToFullTranscript(client *openai.Client, wavBytes []byte, finalTranscriptFromSlices string) {
	fullResult, err := transcribeAudio(client, bytes.NewReader(wavBytes), "wav", "")
	dbg(err)
	log.Info().Str("full_transcript", fullResult).Str("sliced_together_transcript", finalTranscriptFromSlices).Msg("comparing full transcript to from slices")
}

func fillerWordRoutine(chatAgent agent.ChatAgent, tts synthesizer.Synthesizer, earlyTranscriptChan chan string, audioOutputChan chan []byte) {
	log.Info().Msgf("fillerWordRoutine START")
	// This is the 5-10
	earlyTranscript := <-earlyTranscriptChan
	log.Info().Msgf("fillerWordRoutine received earlyTranscript %s", earlyTranscript)

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

	fillerWords := "hmm Got it, "
	topicPrompt := fmt.Sprintf("etract conversation title in 1-4 words from this transcript: %s", earlyTranscript)
	topicPromptResult := make(chan string, 1000)
	go dbg(chatAgent.RunPrompt(agent.SlowerAndSmarter, topicPrompt, topicPromptResult))
	for token := range topicPromptResult {
		fillerWords += token
	}
	fillerWords += "... ."

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

// Based off their Python version of the code https://cookbook.openai.com/examples/how_to_stream_completions
// Translated with GPT-4: https://chat.openai.com/c/c723eeaa-2c24-42c2-aabb-0f5582d0f031
// Using https://github.com/sashabaranov/go-openai/blob/d6f3bdcdac9172ab5248d6be8c3e1761446a434c/chat_stream.go#L62
func main() {
	setupSignalHandler()
	setupStart := time.Now()
	// Set up zerolog with custom output to include milliseconds in the timestamp
	log.Logger = zerolog.New(zerolog.ConsoleWriter{
		Out:        os.Stdout,
		TimeFormat: "2006-01-02T15:04:05.000-07:00", // Fake news, BUT we need milliseconds to debug stuff.
	}).With().Timestamp().Logger()
	// https://github.com/rs/zerolog/issues/114
	zerolog.TimeFieldFormat = time.RFC3339Nano

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
	chatAgent := agent.NewOpenAIChatAgent(client)
	tts := synthesizer.NewOpenAITTS(openAIAPIKey)

	// About 200ms
	audioInput, err := audioio.NewMicrophone()
	if err != nil {
		log.Panic().Err(err).Msgf("cannot init microphone")
	}
	audioOutput, err := audioio.NewSpeakers(OpenAiSampleRate, 2)
	if err != nil {
		log.Panic().Err(err).Msgf("cannot init speakers")
	}
	log.Debug().Dur("setup_time", time.Since(setupStart)).Msg("setup done")
	// ==== SETUP DONE

	audioChunksChan := make(chan models.AudioData, 100000) // TODO: feels to be allocating too much but shrug
	finalTranscriptChan := make(chan string, 1)
	earlyTranscriptChan := make(chan string, 1)
	chatOutputChan := make(chan string, 100000)
	rawAudioBytesChan := make(chan []byte, 100000)
	go transcribeAudioRoutine(client, audioChunksChan, finalTranscriptChan, earlyTranscriptChan)
	go fillerWordRoutine(chatAgent, tts, earlyTranscriptChan, rawAudioBytesChan)

	err = audioInput.StartRecording(audioChunksChan)
	if err != nil {
		log.Error().Err(err).Msg("malgo record failed")
		return
	}

	// TODO(P0, ux): Get this through VAD when we stop talking
	fmt.Println("Press Enter to stop recording...")
	_, err = fmt.Scanln()

	entireWavRecording, err := audioInput.StopRecording()
	if err != nil {
		log.Error().Err(err).Msg("audio input stop recording")
	}
	// For debug purposes write the output to a real file so we can replay it.
	dbg(os.WriteFile("output/entire-recording.wav", entireWavRecording, 0644))

	// Documentation for the chat and rawAudio routines intent / design:
	// https://chat.openai.com/share/9ae89c13-9f66-4500-b719-dcd07dd6454d
	go textToSpeechAndEncodeRoutine(tts, chatOutputChan, rawAudioBytesChan)
	go playAudioChunksRoutine(audioOutput, rawAudioBytesChan)

	chatPrompt := <-finalTranscriptChan
	go compareToFullTranscript(client, entireWavRecording, chatPrompt)
	// prompt := "Strep throat recovery timeline in 100 words"
	// prompt := "give me first 30 numbers as a sequence 1, 2, .. 30"
	// TODO(P2, mem-leaks): Better propagate errors so channels can be properly closed.
	dbg(chatAgent.RunPrompt(agent.SlowerAndSmarter, chatPrompt, chatOutputChan))

	// TODO: Better wait mechanism.
	time.Sleep(10 * time.Second)
	dbg(audioOutput.Stop()) // Don't really have to but want to try if it works.
}

func dbg(err error) {
	if err != nil {
		log.Debug().Err(err).Msg("sth non-essential failed")
	}
}
