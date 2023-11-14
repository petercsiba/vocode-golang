package input_device

import "github.com/petrzlen/vocode-golang/pkg/models"

type AudioInputDevice interface {
	StartRecording(recordingChan chan models.AudioData) error
	StopRecording() ([]byte, error)
	// Stop() error
}
