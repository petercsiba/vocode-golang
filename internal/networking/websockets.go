package networking

import (
	"github.com/gorilla/websocket"
	"github.com/rs/zerolog/log"
	"net/http"
	"runtime/debug"
)

// WebsocketMessageHandler usage:
// * Read from GetReader chan until closed (which means the other party closed it)
// * Write into GetWriter chan until you want - if you close it than the websocket will be closed gracefully.
//
// NOTE: This assumes the message encoding is websocket.TestMessage type (NOT websocket.Binary).
// TextMessage should cover most of the real-world cases, as these days everyone just sends JSON stuff.
type WebsocketMessageHandler interface {
	// GetReader is where websocket.ReadMessage will produce messages into UNTIL the websocket is closed,
	// then the Reader chan will be CLOSED, i.e. do NOT close this channel yourself as panic is a guaranteed.
	GetReader() chan<- []byte
	// GetWriter is where you can write response - upon channel close, or invalid message produced,
	// the websocket will attempt to close gracefully.
	GetWriter() <-chan []byte
}

var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool {
		return true // Adjust the origin check as needed
	},
}

func getClientIpAddress(r *http.Request) (clientIP string) {
	// Get client IP from RemoteAddr
	clientIP = r.RemoteAddr

	// Check for real IP in headers (useful if behind proxy)
	if realIP := r.Header.Get("X-Real-IP"); realIP != "" {
		clientIP = realIP
	} else if forwardedFor := r.Header.Get("X-Forwarded-For"); forwardedFor != "" {
		clientIP = forwardedFor
	}
	return
}

// NewWebsocketHandlerFunc takes the raw http reader / writer,
// and abstracts it into WebsocketMessageHandler which works at the chan []byte message level.
func NewWebsocketHandlerFunc(createHandler func() WebsocketMessageHandler) func(w http.ResponseWriter, r *http.Request) {
	return func(w http.ResponseWriter, r *http.Request) {
		handler := createHandler()
		log.Info().Str("client_ip", getClientIpAddress(r)).Str("method", r.Method).Str("request_url", r.URL.String()).Msg("NewWebsocketHandlerFunc attempting to establish a websocket connection")

		defer func() { close(handler.GetReader()) }()

		ws, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			errLog(err, "websocket upgrader.Upgrade")
			return
		}
		defer func() { errLog(ws.Close(), "websocket.Close()") }()

		// Start a goroutine for sending messages
		// TODO(P1, ux): Interrupts / sigkills should also clean up all alive websocket connections.
		go func() {
			for {
				msg, ok := <-handler.GetWriter()
				// Channel closed by the user, attempt to close connection gracefully.
				// That will also end up the reader routine.
				if !ok {
					log.Info().Msg("websocket writer channel closed, attempting to close connection gracefully")
					msg := websocket.FormatCloseMessage(websocket.CloseNormalClosure, "")
					errLog(ws.WriteMessage(websocket.CloseMessage, msg), "websocket.CloseMessage gracefully")
					return
				}

				if err := ws.WriteMessage(websocket.TextMessage, msg); err != nil {
					if websocket.IsCloseError(err, websocket.CloseNormalClosure, websocket.CloseNoStatusReceived) {
						log.Info().Msg("websocket too late to write message, as already closed")
					} else {
						errLog(err, "ws.WriteMessage")
					}
					return
				}
			}
		}()

		log.Info().Msg("NewWebsocketHandlerFunc starting to read from the websocket")
		for {
			_, msg, err := ws.ReadMessage()
			if err != nil {
				if websocket.IsCloseError(err, websocket.CloseNormalClosure, websocket.CloseNoStatusReceived) {
					log.Info().Msg("websocket connection closed normally from the other party")
				} else {
					log.Error().Err(err).Msgf("couldn't read message from websocket: %s", string(msg))
				}
				// Usually, nothing good will happen ever after a bad websocket message
				return
			}
			handler.GetReader() <- msg
		}
	}
}

func errLog(err error, what string) {
	if err != nil {
		log.Error().Err(err).Msg(what)
		debug.PrintStack()
	}
}
