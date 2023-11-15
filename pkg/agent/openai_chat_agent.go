package agent

import (
	"context"
	"errors"
	"github.com/petrzlen/vocode-golang/pkg/models"
	"github.com/rs/zerolog/log"
	"github.com/sashabaranov/go-openai"
	"io"
	"strings"
	"time"
)

type openaiChatAgent struct {
	client *openai.Client
}

func NewOpenAIChatAgent(client *openai.Client) ChatAgent {
	return &openaiChatAgent{client: client}
}

func conversationToOpenAiMessages(conversation *models.Conversation) []openai.ChatCompletionMessage {
	result := make([]openai.ChatCompletionMessage, len(conversation.Messages))
	for i, message := range conversation.Messages {
		result[i].Role = message.Role
		result[i].Content = message.Content
	}
	return result
}

// RunPrompt
func (o *openaiChatAgent) RunPrompt(modelQuality ModelQuality, conversation *models.Conversation, outputChan chan string) error {
	model := "gpt-3.5-turbo"
	if modelQuality == SlowerAndSmarter {
		model = "gpt-4"
	}

	startTime := time.Now()
	lastDataReceivedPrintoutTime := time.Now()

	// Create a chat completion request
	chatRequest := openai.ChatCompletionRequest{
		Model:       model,
		Messages:    conversationToOpenAiMessages(conversation),
		Temperature: 0,
	}
	log.Info().Str("prompt", conversation.GetLastPrompt()).Str("model", chatRequest.Model).Float32("temperature", chatRequest.Temperature).Msg("executeChatRequest")

	// Create a chat completion stream
	ctx := context.Background()
	completionStream, createStreamErr := o.client.CreateChatCompletionStream(ctx, chatRequest)
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
			if errors.Is(streamRecvErr, io.EOF) {
				break // Stream closed, exit loop
			}
			log.Error().Msgf("Error reading from stream, closing: %v\n", streamRecvErr)
			break
		}
	}

	close(outputChan)
	result := contentBuilder.String()
	log.Info().Msgf("Full response received %.2f seconds after request", time.Since(startTime).Seconds())
	log.Info().Msgf("Full conversation received: %s", result)
	return nil
}
