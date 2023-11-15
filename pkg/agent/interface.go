package agent

type ModelQuality int

// Declare constants with the custom type. These are your enum values.
const (
	FastAndCheap ModelQuality = iota
	SlowerAndSmarter
)

func (m ModelQuality) String() string {
	days := [...]string{
		"FastAndCheap",
		"SlowerAndSmarter",
	}

	if m < FastAndCheap || m > SlowerAndSmarter {
		return "Unknown"
	}

	return days[m]
}

// ChatAgent
// TODO: Feels like we need a better interface here, but lets wait until conversation.go evolves.
// - Probably needs to be stateful.
type ChatAgent interface {
	RunPrompt(modelQuality ModelQuality, prompt string, outputChan chan string) error
}
