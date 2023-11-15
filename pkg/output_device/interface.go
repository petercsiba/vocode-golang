package output_device

import (
	"io"
	"sync"
)

type AudioOutputDevice interface {
	Play(audioOutput io.Reader) (*sync.WaitGroup, error)
	Stop() error
}
