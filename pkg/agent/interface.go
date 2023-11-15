package agent

import "github.com/petrzlen/vocode-golang/pkg/models"

type Interface interface {
	StartRecording(recordingChan chan models.AudioData) error
	StopRecording() ([]byte, error)
	// Stop() error
}
