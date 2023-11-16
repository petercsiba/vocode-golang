package main

import (
	"github.com/petrzlen/vocode-golang/internal/networking"
	"github.com/petrzlen/vocode-golang/internal/utils"
	"github.com/petrzlen/vocode-golang/pkg/audioio"
	"github.com/rs/zerolog/log"
	"net/http"
	"runtime/debug"
)

func main() {
	utils.SetupZerolog()

	twilioHandlerFactory := func() networking.WebsocketMessageHandler {
		return audioio.NewTwilioHandler()
	}

	http.HandleFunc("/ws", networking.NewWebsocketHandlerFunc(twilioHandlerFactory))
	ftl(http.ListenAndServe(":8081", nil))
}

func ftl(err error) {
	if err != nil {
		log.Fatal().Err(err).Msg("sth essential failed")
		debug.PrintStack()
	}
}
