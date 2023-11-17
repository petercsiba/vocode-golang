package audioio

import (
	"github.com/petrzlen/vocode-golang/pkg/models"
	"io"
	"sync"
)

// InputDevice
// TODO(P1, wip): This interface was made around microphones,
// might want to change it to say Init(), PauseRecording(), Close().
type InputDevice interface {
	StartRecording(recordingChan chan models.AudioData) error
	StopRecording() ([]byte, error)
}

type OutputDevice interface {
	Play(audioOutput io.Reader) (*sync.WaitGroup, error)
	Stop() error
}
