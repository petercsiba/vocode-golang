package main

import (
	"bufio"
	"encoding/json"
	"github.com/joho/godotenv"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
)

type Choice struct {
	Index        int                    `json:"index"`
	Delta        map[string]interface{} `json:"delta"`
	FinishReason *string                `json:"finish_reason"`
}

type EventData struct {
	ID      string   `json:"id"`
	Object  string   `json:"object"`
	Created int64    `json:"created"`
	Model   string   `json:"model"`
	Choices []Choice `json:"choices"`
}

// Based off their Python version of the code https://cookbook.openai.com/examples/how_to_stream_completions
// Translated with GPT-4: https://chat.openai.com/c/c723eeaa-2c24-42c2-aabb-0f5582d0f031
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

	client := &http.Client{}
	startTime := time.Now()

	// Construct the request body
	reqBody := strings.NewReader(`{
		"model": "gpt-3.5-turbo",
		"messages": [
			{"role": "user", "content": "Count to 100, with a comma between each number and no newlines. E.g., 1, 2, 3, ..."}
		],
		"temperature": 0,
		"stream": true
	}`)

	// Create and send the request
	req, _ := http.NewRequest("POST", "https://api.openai.com/v1/chat/completions", reqBody)
	req.Header.Add("Authorization", "Bearer "+openAIAPIKey)
	req.Header.Add("Content-Type", "application/json")

	// Send the request
	resp, _ := client.Do(req)
	defer resp.Body.Close()

	// Use bufio.Reader to read the stream line by line
	reader := bufio.NewReader(resp.Body)

	var contentBuilder strings.Builder

	isDone := false
	for !isDone {
		line, err := reader.ReadString('\n')
		if err != nil {
			log.Error().Msgf("Error reading stream:", err)
			break
		}

		// Ignore empty lines and comments in the stream
		if line == "\n" || strings.HasPrefix(line, ":") {
			continue
		}

		// Handle event field (this seems to NOT happen)
		if strings.HasPrefix(line, "event:") {
			eventType := strings.TrimSpace(strings.TrimPrefix(line, "event:"))
			log.Info().Msgf("Event type:", eventType)
			continue
		}

		// Handle data field

		if strings.HasPrefix(line, "data:") {
			dataJSON := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
			var eventData EventData
			if err := json.Unmarshal([]byte(dataJSON), &eventData); err != nil {
				log.Error().Msgf("Error unmarshaling data:", err)
				continue
			}

			// Iterate over choices and append content if it exists
			for _, choice := range eventData.Choices {
				if content, ok := choice.Delta["content"]; ok {
					contentStr, ok := content.(string)
					if !ok {
						log.Error().Msgf("Content is not a string %+v", content)
						continue
					}
					contentBuilder.WriteString(contentStr)
				}
				if choice.FinishReason != nil {
					log.Info().Msgf("Response reading finished with " + *choice.FinishReason)
					isDone = true
				}
			}

			chunkTime := time.Since(startTime)
			log.Info().Msgf("Data received %.2f seconds after request: %+v", chunkTime.Seconds(), eventData)
			continue
		}

		log.Warn().Msgf("unhandled line: `%s`", line)
	}

	log.Info().Msgf("Full response received %.2f seconds after request", time.Since(startTime).Seconds())
	log.Info().Msgf("Full conversation received: %s", contentBuilder.String())
}
