package utils

import (
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
	"os"
	"time"
)

func SetupZerolog() {
	// Set up zerolog with custom output to include milliseconds in the timestamp
	log.Logger = zerolog.New(zerolog.ConsoleWriter{
		Out:        os.Stdout,
		TimeFormat: "2006-01-02T15:04:05.000-07:00", // Fake news, BUT we need milliseconds to debug stuff.
	}).With().Timestamp().Logger()
	// https://github.com/rs/zerolog/issues/114
	zerolog.TimeFieldFormat = time.RFC3339Nano
}
