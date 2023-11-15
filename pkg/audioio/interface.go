package audioio

import (
	"github.com/petrzlen/vocode-golang/pkg/models"
	"io"
	"sync"
)

type InputDevice interface {
	StartRecording(recordingChan chan models.AudioData) error
	StopRecording() ([]byte, error)
	// Stop() error
}

type OutputDevice interface {
	Play(audioOutput io.Reader) (*sync.WaitGroup, error)
	Stop() error
}
