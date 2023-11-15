package transcriber

import "io"

type Transcriber interface {
	SendAudio(input io.Reader, fileExtension string, prompt string) (result string, err error)
}
