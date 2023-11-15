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

type ChatAgent interface {
	RunPrompt(modelQuality ModelQuality, prompt string, outputChan chan string) error
}
