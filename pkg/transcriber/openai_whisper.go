package transcriber

import (
	"context"
	"fmt"
	"github.com/rs/zerolog/log"
	"github.com/sashabaranov/go-openai"
	"io"
	"regexp"
	"strings"
	"time"
)

type openAIWhisper struct {
	client *openai.Client
}

func NewOpenAIWhisper(client *openai.Client) Transcriber {
	return &openAIWhisper{
		client: client,
	}
}

// SendAudio TODO(P1, latency): Figure out by how much mp3 is faster than .WAV
// 3 tests on a 260KB wav vs 67KB mp3 it seems maybe 1100ms vs 1000ms, but there was a run when wav beat mp3 :/
func (o *openAIWhisper) SendAudio(input io.Reader, fileExtension string, prompt string) (result string, err error) {
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
	resp, err := o.client.CreateTranscription(context.Background(), req)
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
