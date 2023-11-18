package models

import (
	"github.com/rs/zerolog/log"
	"time"
)

type Trace struct {
	CreatedAt time.Time
	Creator   string

	ReceivedAt time.Time

	ProcessedAt time.Time
	Processor   string
	// TODO(devx): Add some extra metadata as it evolves
}

func (t Trace) Log() {
	log.Trace().Time("created_at", t.CreatedAt).Str("creator", t.Creator).Time("processed_at", t.ProcessedAt).Str("processor", t.Processor).Dur("dur_to_process", t.ProcessedAt.Sub(t.CreatedAt)).Msgf("tracing")
}

type AudioDataEvent int

// Declare constants with the custom type. These are your enum values.
const (
	AudioInput AudioDataEvent = iota
	AudioOutput
	SubmitPrompt
)

// AudioData
// TODO: This should grow up into a more realistic Event like Twilio or Vocode has
// i.e. distinguish inbound/outbound pipelines; hierarchy of Conversation -> Message -> Event
// and more event types like Silence / Interrupt / Stop / Finished.
type AudioData struct {
	EventType AudioDataEvent
	ByteData  []byte
	Format    string
	Length    time.Duration
	Text      string // text representation
	Trace     Trace
}

func NewAudioDataSubmit(creator string) AudioData {
	return AudioData{
		EventType: SubmitPrompt,
		Trace:     NewTrace(creator),
	}
}

func NewTrace(creator string) Trace {
	return Trace{
		CreatedAt: time.Now(),
		Creator:   creator,
	}
}

type Message struct {
	Role       string
	Content    string
	FinishedAt time.Time
}

// Conversation for the Chat API
// TODO: We can stop using it once the Assistant API supports streaming
type Conversation struct {
	StartedAt time.Time
	Messages  []Message
}

func NewConversationSimple(text string) Conversation {
	return Conversation{
		StartedAt: time.Now(),
		Messages: []Message{
			{Role: "user", Content: text, FinishedAt: time.Now()},
		},
	}
}

func (c *Conversation) Add(role string, content string) {
	c.Messages = append(c.Messages, Message{
		Role:       role,
		Content:    content,
		FinishedAt: time.Now(),
	})
}

func (c *Conversation) GetLastPrompt() string {
	if len(c.Messages) == 0 {
		return "" // Yes, I am a bit lazy
	}
	return c.Messages[len(c.Messages)-1].Content
}

func (c *Conversation) DebugLog() {
	log.Debug().Msg("DUMPING FULL CONVERSATION")
	for i, message := range c.Messages {
		at := message.FinishedAt.Sub(c.StartedAt)
		log.Debug().Int("i", i).Str("role", message.Role).Dur("since_started", at).Msg(message.Content)
	}
}
