package models

import (
	"github.com/rs/zerolog/log"
	"time"
)

type Trace struct {
	DataName string

	CreatedAt time.Time
	Creator   string

	ReceivedAt time.Time

	ProcessedAt time.Time
	Processor   string
	// TODO(devx): Add some extra metadata as it evolves
}

func (t Trace) Log() {
	log.Trace().Time("created_at", t.CreatedAt).Str("creator", t.Creator).Time("processed_at", t.ProcessedAt).Str("processor", t.Processor).Dur("dur_to_process", t.ProcessedAt.Sub(t.CreatedAt)).Msgf("trace of %s", t.DataName)
}

type AudioData struct {
	ByteData []byte
	Format   string
	Length   time.Duration
	Trace    Trace
}
