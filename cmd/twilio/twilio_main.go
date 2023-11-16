package main

import (
	"github.com/petrzlen/vocode-golang/internal/utils"
	"github.com/petrzlen/vocode-golang/pkg/audioio"
)

func main() {
	utils.SetupZerolog()

	audioio.NewTwilioHandler()
}
