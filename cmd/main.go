package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/ebitengine/oto/v3"
	"github.com/hajimehoshi/go-mp3"
	"github.com/joho/godotenv"
	"github.com/sashabaranov/go-openai"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
)

// MinTextBufferForTtsCharLength is a tradeoff between API call latency and jitter from missing TTSed chunks.
// This also correlates with "human breathing" in the output intonation.
const MinTextBufferForTtsCharLength = 10

// OpenAiSampleRate - this I have measured by decodedMp3.SampleRate
const OpenAiSampleRate = 24000

var httpClient = &http.Client{}

func executeChatRequest(client *openai.Client, prompt string, outputChan chan string) {
	log.Info().Str("prompt", prompt).Msg("executeChatRequest")
	startTime := time.Now()
	lastDataReceivedPrintoutTime := time.Now()

	// Create a chat completion request
	chatRequest := openai.ChatCompletionRequest{
		// Model: "gpt-3.5-turbo",
		Model: "gpt-4",
		Messages: []openai.ChatCompletionMessage{
			{
				Role:    "user",
				Content: prompt,
			},
		},
		Temperature: 0,
	}

	// Create a chat completion stream
	ctx := context.Background()
	completionStream, createStreamErr := client.CreateChatCompletionStream(ctx, chatRequest)
	if createStreamErr != nil {
		log.Panic().Msgf("Failed to create chat completion stream: %v", createStreamErr)
	}

	var contentBuilder strings.Builder
	var debugChunkBuilder strings.Builder

	firstContent := true
	for {
		response, streamRecvErr := completionStream.Recv()
		if firstContent {
			log.Warn().Dur("latency", time.Since(startTime)).Msg("TRACING HACK: first chat completion received")
			firstContent = false
		}

		// Process the response
		for _, choice := range response.Choices {
			content := choice.Delta.Content
			outputChan <- content
			contentBuilder.WriteString(content)
			debugChunkBuilder.WriteString(content)

			if time.Since(lastDataReceivedPrintoutTime) >= time.Second {
				lastDataReceivedPrintoutTime = time.Now() // Update the last printout time
				lastChunk := debugChunkBuilder.String()
				debugChunkBuilder.Reset()
				log.Debug().Float64("time_elapsed", time.Since(startTime).Seconds()).Str("last_content", lastChunk).Msgf("ChatCompletionStream Data Status")
			}
		}

		// We only handle the error at the end - since we can get io.EOF with the last token.
		if streamRecvErr != nil {
			close(outputChan)
			if errors.Is(streamRecvErr, io.EOF) {
				break // Stream closed, exit loop
			}
			log.Error().Msgf("Error reading from stream, closing: %v\n", streamRecvErr)
			break
		}
	}

	result := contentBuilder.String()
	log.Info().Msgf("Full response received %.2f seconds after request", time.Since(startTime).Seconds())
	log.Info().Msgf("Full conversation received: %s", result)
}

// This is to by-pass not-yet-implemented APIs in go-openai
func sendRequest(openAIAPIKey string, method string, endpoint string, requestStr string) (result []byte, err error) {
	requestStart := time.Now()
	// Construct the request body
	reqBody := strings.NewReader(requestStr)

	// Create and send the request
	req, err := http.NewRequest(method, "https://api.openai.com/v1/"+endpoint, reqBody)
	if err != nil {
		return
	}
	req.Header.Add("Authorization", "Bearer "+openAIAPIKey)
	req.Header.Add("Content-Type", "application/json")

	// Send the request
	resp, err := httpClient.Do(req)
	if err != nil {
		return
	}
	defer func() { resp.Body.Close() }()

	log.Debug().Dur("request_time", time.Since(requestStart)).Str("method", method).Str("endpoint", endpoint).Int("status_code", resp.StatusCode).Msg("request done")

	if resp.StatusCode != http.StatusOK {
		errMsg, _ := io.ReadAll(resp.Body)
		err = fmt.Errorf("received non-200 status %d from %s: %s", resp.StatusCode, endpoint, errMsg)
		log.Debug().Err(err).Str("method", method).Str("endpoint", endpoint).Str("requestStr", requestStr).Msg("request to openai failed")
		return
	}

	readStart := time.Now()
	result, err = io.ReadAll(resp.Body)
	log.Debug().Dur("response_body_read_time", time.Since(readStart)).Int("response_byte_size", len(result)).Str("endpoint", endpoint).Msg("request body read done")
	if err != nil {
		err = fmt.Errorf("could not read response %w", err)
		return
	}
	return
}

// TTSPayload for sendTTSRequest
type TTSPayload struct {
	Model          string  `json:"model"`
	Input          string  `json:"input"`
	Voice          string  `json:"voice"`
	ResponseFormat string  `json:"response_format"`
	Speed          float64 `json:"speed"`
}

// TODO(devx, P1): Replace with the official one after implemented
// https://github.com/sashabaranov/go-openai/pull/528/files?diff=unified&w=0
func sendTTSRequest(openAIAPIKey string, input string) (rawAudioBytes []byte, err error) {
	log.Debug().Str("input", input).Msg("sendTTSRequest start")

	payload := TTSPayload{
		Model:          "tts-1",
		Input:          input,
		Voice:          "alloy",
		ResponseFormat: "mp3", // TODO(ux, P1): Opus should be a better format for streaming, using mp3 for ease.
		Speed:          1.0,
	}
	reqStr, _ := json.Marshal(payload)
	rawAudioBytes, err = sendRequest(openAIAPIKey, "POST", "audio/speech", string(reqStr))
	if err != nil {
		err = fmt.Errorf("could not do audio/speech for %s cause %w", reqStr, err)
		return
	}
	return
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

func textToSpeechAndEncodeRoutine(openAIAPIKey string, textCh <-chan string, rawAudioBytesCh chan<- []byte) {
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
				// Process the buffer
				rawAudioBytes, err := sendTTSRequest(openAIAPIKey, buffer)
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

func setupOtoContext(sampleRate int, channelCount int) *oto.Context {
	op := &oto.NewContextOptions{
		SampleRate:   sampleRate,
		ChannelCount: channelCount,
		Format:       oto.FormatSignedInt16LE,
	}

	// Remember that you should **not** create more than one context
	log.Info().Msgf("setupOtoPlayer - will wait until ready")
	otoCtx, readyChan, err := oto.NewContext(op)
	if err != nil {
		log.Panic().Err(err)
	}
	<-readyChan // Wait for the audio hardware to be ready
	log.Info().Msgf("setupOtoPlayer - context ready")
	return otoCtx
}

func playAudioChunksRoutine(otoCtx *oto.Context, rawAudioBytesCh chan []byte) {
	log.Info().Msgf("playAudioChunksRoutine started")

	i := 0
	for rawAudioBytes := range rawAudioBytesCh {
		i += 1
		if i == 1 {
			log.Warn().Msg("TRACING HACK: first tts received")
		}

		log.Debug().Msgf("attempting to play %d bytes of mp3", len(rawAudioBytes))
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
		log.Trace().Int("sample_rate", decodedMp3.SampleRate()).Int64("byte_size", decodedMp3.Length()).Msg("player START")
		player := otoCtx.NewPlayer(decodedMp3) // Sub-millisecond time
		player.Play()
		if i == 1 {
			log.Warn().Msg("TRACING HACK: first playback started")
		}

		// Wait for the chunk to finish playing
		for player.IsPlaying() {
			// TODO(P0, ux): We would need to handle interrupts here
			time.Sleep(time.Millisecond)
		}
		// Close the player when done
		err = player.Close()
		if err != nil {
			log.Error().Err(err).Msg("player.Close failed")
		}
		log.Debug().Dur("duration", time.Since(startTime)).Msg("player DONE")
	}
}

// TODO(P1, latency): Figure out by how much mp3 is faster than .WAV
//
//	3 tests on a 260KB wav vs 67KB mp3 it seems maybe 1100ms vs 1000ms, but there was a run when wav beat mp3 :/
func transcribeAudio(client *openai.Client, input io.Reader, fileExtension string) (result string, err error) {
	startTime := time.Now()
	req := openai.AudioRequest{
		Model:    "whisper-1",
		Reader:   input,
		FilePath: fmt.Sprintf("this-file-does-not-exist-just-needs-extension.%s", fileExtension),
		// FilePath: "output/tell-me-about-ba.mp3",
		//Prompt:      "some previous words",  // TODO
	}
	resp, err := client.CreateTranscription(context.Background(), req)
	if err != nil {
		err = fmt.Errorf("cannot create transcription %w", err)
		return
	}
	log.Warn().Dur("transcription_time", time.Since(startTime)).Msg("TRACING HACK: create transcription done")

	//var contentBuilder strings.Builder
	//for _, segment := range resp.Segments {
	//	contentBuilder.WriteString(segment.Text)
	//}
	//result = contentBuilder.String()
	result = resp.Text
	log.Debug().Str("transcription", result).Dur("time_elapsed", time.Since(startTime)).Msg("received transcription")
	return
}

// Based off their Python version of the code https://cookbook.openai.com/examples/how_to_stream_completions
// Translated with GPT-4: https://chat.openai.com/c/c723eeaa-2c24-42c2-aabb-0f5582d0f031
// Using https://github.com/sashabaranov/go-openai/blob/d6f3bdcdac9172ab5248d6be8c3e1761446a434c/chat_stream.go#L62
func main() {
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

	// About 200ms
	otoCtx := setupOtoContext(OpenAiSampleRate, 2)
	log.Debug().Dur("setup_time", time.Since(setupStart)).Msg("setup done")
	// ==== SETUP DONE

	recordingBytes, err := malgoRecord()
	if err != nil {
		log.Error().Err(err).Msg("malgo record failed")
		return
	}

	transcript, err := transcribeAudio(client, bytes.NewReader(recordingBytes), "wav")
	if err != nil {
		log.Error().Err(err).Msg("cannot transcribe audio")
		return
	}
	log.Info().Str("transcript", transcript).Msg("transcript received")

	// Documentation for the routines intent / design:
	// https://chat.openai.com/share/9ae89c13-9f66-4500-b719-dcd07dd6454d
	chatOutputChan := make(chan string, 100000)
	rawAudioBytesChan := make(chan []byte, 100000)
	go textToSpeechAndEncodeRoutine(openAIAPIKey, chatOutputChan, rawAudioBytesChan)
	go playAudioChunksRoutine(otoCtx, rawAudioBytesChan)

	prompt := transcript
	// prompt := "Strep throat recovery timeline in 100 words"
	// prompt := "give me first 30 numbers as a sequence 1, 2, .. 30"
	// TODO(P2, mem-leaks): Better propagate errors so channels can be properly closed.
	executeChatRequest(client, prompt, chatOutputChan)

	// TODO: Better wait mechanism.
	time.Sleep(10 * time.Second)
}
