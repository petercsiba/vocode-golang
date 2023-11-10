package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
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

// This is to by-pass not-yet-implemented APIs in go-openai
func sendRequest(openAIAPIKey string, method string, endpoint string, requestStr string) (reader *bufio.Reader, doLater func(), err error) {
	client := &http.Client{}

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
	resp, err := client.Do(req)
	if err != nil {
		return
	}
	doLater = func() { resp.Body.Close() }

	reader = bufio.NewReader(resp.Body)
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
func sendTTSRequest(openAIAPIKey string, input string) {
	payload := TTSPayload{
		Model:          "tts-1",
		Input:          input,
		Voice:          "alloy",
		ResponseFormat: "mp3",
		Speed:          1.2,
	}
	reqStr, _ := json.Marshal(payload)
	reader, doLater, err := sendRequest(openAIAPIKey, "POST", "audio/speech", string(reqStr))
	if err != nil {
		log.Error().Msgf("could not do audio/speech for %s cause %v", reqStr, err)
		return
	}
	defer doLater()

	buf, err := io.ReadAll(reader)
	if err != nil {
		log.Error().Msgf("could not read response cause %v", err)
		return
	}
	err = os.WriteFile("test.mp3", buf, 0644)
	if err != nil {
		log.Error().Msgf("could not write file cause %v", err)
		return
	}
}

// Based off their Python version of the code https://cookbook.openai.com/examples/how_to_stream_completions
// Translated with GPT-4: https://chat.openai.com/c/c723eeaa-2c24-42c2-aabb-0f5582d0f031
// Using https://github.com/sashabaranov/go-openai/blob/d6f3bdcdac9172ab5248d6be8c3e1761446a434c/chat_stream.go#L62
func main() {
	// Initialize zerolog with console writer.
	output := zerolog.ConsoleWriter{Out: os.Stdout, TimeFormat: time.RFC3339}
	log.Logger = zerolog.New(output).With().Timestamp().Logger()

	// Load the .env file
	err := godotenv.Load()
	if err != nil {
		log.Warn().Msgf("Cannot load .env file")
	}
	openAIAPIKey := os.Getenv("OPEN_AI_API_KEY")
	if openAIAPIKey == "" {
		log.Fatal().Msgf("OPEN_AI_API_KEY is not set")
	}
	client := openai.NewClient(openAIAPIKey)

	startTime := time.Now()
	lastDataReceivedPrintoutTime := time.Now()

	// Create a chat completion request
	chatRequest := openai.ChatCompletionRequest{
		Model: "gpt-3.5-turbo",
		Messages: []openai.ChatCompletionMessage{
			{
				Role:    "user",
				Content: "Count to 30, with a comma between each number and no newlines. E.g., 1, 2, 3, ...",
			},
		},
		Temperature: 0,
	}

	// Create a chat completion stream
	ctx := context.Background()
	completionStream, createStreamErr := client.CreateChatCompletionStream(ctx, chatRequest)
	if createStreamErr != nil {
		log.Fatal().Msgf("Failed to create chat completion stream: %v", createStreamErr)
	}

	var contentBuilder strings.Builder

	for {
		response, streamRecvErr := completionStream.Recv()

		// Process the response
		for _, choice := range response.Choices {
			contentBuilder.WriteString(choice.Delta.Content)
		}

		if time.Since(lastDataReceivedPrintoutTime) >= time.Second {
			lastDataReceivedPrintoutTime = time.Now() // Update the last printout time
			chunkTime := time.Since(startTime)
			log.Debug().Msgf("Data received %.2f seconds after request: %+v\n", chunkTime.Seconds(), response)
		}

		// We only handle the error at the end - since we can get io.EOF with the last token.
		if streamRecvErr != nil {
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
	sendTTSRequest(openAIAPIKey, result)
}
