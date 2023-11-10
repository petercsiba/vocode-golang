package main

import (
	"context"
	"errors"
	"github.com/joho/godotenv"
	"github.com/sashabaranov/go-openai"
	"io"
	"os"
	"strings"
	"time"

	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
)

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

	// Create a chat completion request
	chatRequest := openai.ChatCompletionRequest{
		Model: "gpt-3.5-turbo",
		Messages: []openai.ChatCompletionMessage{
			{
				Role:    "user",
				Content: "Count to 100, with a comma between each number and no newlines. E.g., 1, 2, 3, ...",
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

		chunkTime := time.Since(startTime)
		log.Debug().Msgf("Data received %.2f seconds after request: %+v\n", chunkTime.Seconds(), response)

		// We only handle the error at the end - since we can get io.EOF with the last token.
		if streamRecvErr != nil {
			if errors.Is(streamRecvErr, io.EOF) {
				break // Stream closed, exit loop
			}
			log.Error().Msgf("Error reading from stream, closing: %v\n", streamRecvErr)
			break
		}
	}

	log.Info().Msgf("Full response received %.2f seconds after request", time.Since(startTime).Seconds())
	log.Info().Msgf("Full conversation received: %s", contentBuilder.String())
}
