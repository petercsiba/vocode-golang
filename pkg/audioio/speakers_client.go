package audioio

import (
	"fmt"
	"github.com/ebitengine/oto/v3"
	"github.com/rs/zerolog/log"
	"io"
	"sync"
	"time"
)

// speakers ended up more complicated as it seems;
// this is because we have to:
//   - allow Playback to be stopped
//   - poll monitor the device if it's still playing
//   - protect against double-play for better ux
//
// The state flow is:
//  1. currentPlayer == nil => nothing going on
//  2. Starts grabs mutex => starting to play
//  3. Stop (or recording done) grabs mutex, interrupts the device and waits until it stops playing.
//  4. Before another Start, you either have to wait on currentDone, or call Stop().
//
// Invariant: There is at most one playerMonitorRoutine running at the same time.
type speakers struct {
	otoContext *oto.Context

	currentPlayer *oto.Player
	currentDone   *sync.WaitGroup

	mutex    sync.Mutex // Protects currentPlayer and stopFlag
	stopFlag bool       // Indicates if playback should be stopped early
}

func NewSpeakers(sampleRate int, numChannels int) (OutputDevice, error) {
	op := &oto.NewContextOptions{
		SampleRate:   sampleRate,
		ChannelCount: numChannels,
		Format:       oto.FormatSignedInt16LE,
	}

	// Remember that you should **not** create more than one context
	log.Info().Msgf("setupOtoPlayer - will wait until ready")
	otoCtx, readyChan, err := oto.NewContext(op)
	if err != nil {
		return nil, err
	}
	<-readyChan // Wait for the audio hardware to be ready (about 200ms empirically)
	log.Info().Msgf("setupOtoPlayer - context ready")

	return &speakers{
		otoContext:    otoCtx,
		currentPlayer: nil,
		stopFlag:      false,
	}, nil
}

// Play plays the entire stream and returns a WaitGroup if a routine wants to block until done.
func (s *speakers) Play(audioOutput io.Reader) (*sync.WaitGroup, error) {
	s.mutex.Lock()
	defer s.mutex.Unlock()

	if s.currentPlayer != nil {
		return nil, fmt.Errorf("currentPlayer isn't nil, you need to call Stop first")
	}

	// Refresh state
	s.currentDone = &sync.WaitGroup{}
	s.currentDone.Add(1)

	s.currentPlayer = s.otoContext.NewPlayer(audioOutput)
	s.currentPlayer.Play()

	// Monitors and properly stops / closes the player when so decided.
	// Invariant: There is at most one playerMonitorRoutine running at the same time.
	go s.playerMonitorRoutine()

	return s.currentDone, nil
}

// Stop, TODO: needs more battle-testing
func (s *speakers) Stop() error {
	s.mutex.Lock()

	if s.stopFlag {
		s.mutex.Unlock()
		// This can only really happen if multiple callers request Stop in a very brief period.
		return fmt.Errorf("double-stop called, the player is already being stopped")
	}

	if s.currentPlayer == nil {
		log.Debug().Msg("currentPlayer is already stopped")
		s.mutex.Unlock()
		return nil
	}

	log.Debug().Msg("currentPlayer is stopping ...")
	s.stopFlag = true
	s.currentPlayer.Pause()
	untilStopped := s.currentDone // we copy it over as it can become nil otherwise
	s.mutex.Unlock()

	untilStopped.Wait()
	return nil
}

func (s *speakers) playerMonitorRoutine() {
	log.Debug().Msg("playerMonitorRoutine start")
	// Signal that the current playback has finished and we ready for the next one
	defer s.currentDone.Done()

	startTime := time.Now()
	for {
		s.mutex.Lock()
		playing := s.currentPlayer.IsPlaying()
		stop := s.stopFlag
		s.mutex.Unlock()

		if !playing || stop {
			break
		}

		time.Sleep(time.Millisecond)
	}

	// NOTE: It's fine to have an unlocked passage here, as the only currentPlayer = nil is below.
	s.mutex.Lock()
	err := s.currentPlayer.Close()
	if err != nil {
		log.Error().Err(err).Msg("player.Close failed")
	}
	s.currentPlayer = nil

	s.currentDone = nil
	s.stopFlag = false

	s.mutex.Unlock()

	log.Debug().Dur("playback_duration", time.Since(startTime)).Msg("current playback done playerMonitorRoutine")
}
