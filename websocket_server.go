// Copyright © 2024 Michael Fero
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
// http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.
package ankh

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/http"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

// WebsocketServerEventHandler represents the callback handlers for the
// WebSocket server.
type WebSocketServerEventHandler interface {
	// OnConnectionHandler is a function callback for WebSocket connection that is
	// executed upon connection of the websocket and should be used to
	// authenticate the client connection request. The value returned should be
	// used as a key to identify the websocket client connection for all other
	// callbacks made.
	OnConnectionHandler(w http.ResponseWriter, r *http.Request) (any, error)

	// OnConnectedHandler is a function callback for indicating that the client
	// connection is completed while creating handles for closing connections and
	// sending messages on the WebSocket.
	OnConnectedHandler(clientKey any, session *Session) error

	// OnDisconnectionHandler is a function callback for WebSocket connections
	// that is executed upon disconnection of a client.
	OnDisconnectionHandler(clientKey any)

	// OnDisconnectionErrorHandler is a function callback for WebSocket
	// disconnection error received from the WebSocket.
	OnDisconnectionErrorHandler(clientKey any, err error)

	// OnPingHandler is a function callback for WebSocket connections during ping
	// operations. The byte array returned will be sent back to the client as a
	// pong message.
	OnPingHandler(clientKey any, appData string) []byte

	// OnReadMessageHandler is a function callback for WebSocket connection that
	// is executed upon a message received from the WebSocket.
	OnReadMessageHandler(clientKey any, messageType int, data []byte)

	// OnReadMessageErrorHandler is a function callback for WebSocket connection
	// read error received from the WebSocket.
	OnReadMessageErrorHandler(clientKey any, err error)

	// OnReadMessagePanicHandler is a function callback for WebSocket connection
	// read panic received from the WebSocket.
	OnReadMessagePanicHandler(clientKey any, err error)

	// OnWebSocketUpgraderErrorHandler is a function callback for WebSocket
	// connection upgrade error received from the WebSocket.
	OnWebSocketUpgraderErrorHandler(clientKey any, err error)
}

// PathHandlers is a map of WebSocket server handlers per path.
type PathHandlers map[string]WebSocketServerEventHandler

// WebSocketServerOpts are the options for the WebSocketServer.
type WebSocketServerOpts struct {
	// Address specifies the TCP address for the WebSocketServer to listen on, in
	// the form "host:port".
	Address string
	// IsKeepAlivesEnabled specifies whether the server should enable keep-alives.
	IsKeepAlivesEnabled bool
	// PathHandlers specifies the path handlers for the WebSocketServer. Each path
	// can have a different handler in order to handle different WebSocket
	// requirements.
	PathHandlers PathHandlers
	// ReadHeaderTimeout specifies the amount of time allowed to read the
	// headers of the request.
	ReadHeaderTimeout time.Duration
	// ReadTimeout specifies the amount of time allowed to read the entire
	// WebSocket message.
	ReadTimeout time.Duration
	// ShutdownTimeout specifies the amount of time allowed to shutdown the
	// WebSocketServer.
	ShutdownTimeout time.Duration
	// TLSConfig specifies the TLS configuration for the WebSocketServer.
	TLSConfig *tls.Config
}

// WebSocketServer is the server instance for a WebSocket.
type WebSocketServer struct {
	mux             *http.ServeMux
	pathHandlers    PathHandlers
	server          *http.Server
	shutdownTimeout time.Duration
}

// NewWebSocketServer creates a new WebSocketServer instance. Options are
// validated and will return an error if any are invalid.
func NewWebSocketServer(opts WebSocketServerOpts) (*WebSocketServer, error) {
	// Validate all WebSocketServer options
	address := strings.TrimSpace(opts.Address)
	if len(address) == 0 {
		return nil, errors.New("address must not be empty")
	}
	if opts.ShutdownTimeout <= 0 {
		return nil, errors.New("shutdown timeout must be > 0")
	}
	if len(opts.PathHandlers) == 0 {
		return nil, errors.New("path handlers must not be empty")
	}

	// Create the HTTP server for the WebSocketServer
	mux := http.NewServeMux()
	server := &http.Server{
		Addr:              address,
		Handler:           mux,
		ReadHeaderTimeout: opts.ReadHeaderTimeout,
		ReadTimeout:       opts.ReadTimeout,
		TLSConfig:         opts.TLSConfig,
	}
	server.SetKeepAlivesEnabled(opts.IsKeepAlivesEnabled)

	// Create the WebSocketServer instance
	return &WebSocketServer{
		mux:             mux,
		pathHandlers:    opts.PathHandlers,
		server:          server,
		shutdownTimeout: opts.ShutdownTimeout,
	}, nil
}

// Run starts the WebSocketServer and blocks until the context is done. Each
// path handler will be executed in a separate goroutine and will block until
// the context is done or the connection is closed; which can only be performed
// after a successful connection or by the client.
func (s *WebSocketServer) Run(ctx context.Context) error {
	// Register all paths with the HTTP server
	for path, handler := range s.pathHandlers {
		s.mux.HandleFunc(path, func(w http.ResponseWriter, r *http.Request) {
			// Note: The websocket.Upgrader is created for each path in order to allow
			// for future customization of the Upgrader with the WebSocketServerOpts
			s.handleConnection(ctx, w, r, websocket.Upgrader{}, handler)
		})
	}

	// Start the HTTP server with or without TLS
	errChannel := make(chan error)
	go func() {
		listener, err := net.Listen("tcp", s.server.Addr)
		if err != nil {
			errChannel <- err
			return
		}
		if s.server.TLSConfig != nil {
			listener = tls.NewListener(listener, s.server.TLSConfig)
		}
		err = s.server.Serve(listener)
		if err != nil {
			if errors.Is(err, http.ErrServerClosed) {
				errChannel <- err
				return
			}
		}
	}()

	select {
	case err := <-errChannel:
		return err
	case <-ctx.Done():
		ctx, ctxCancel := context.WithTimeout(context.Background(), s.shutdownTimeout)
		defer ctxCancel()
		if err := s.server.Shutdown(ctx); err != nil {
			return fmt.Errorf("unable to properly shutdown server: %w", err)
		}
	}

	return nil
}

// closeConnection will close the connection with the client application.
func (s *WebSocketServer) closeConnection(conn *websocket.Conn, mutex *sync.Mutex, clientKey any,
	handler WebSocketServerEventHandler,
) {
	// Ensure closed message is not sent when connection is already closed
	err := writeMessage(conn, mutex, websocket.CloseMessage,
		websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""))

	// Close call may be sent from multiple areas of the server application or
	// by gorilla WebSocket automatically; ensure disconnection handler is not
	// called twice
	if err != nil && !errors.Is(err, websocket.ErrCloseSent) {
		handler.OnDisconnectionErrorHandler(clientKey, err)
	}
}

// writeMessage will write a message to the client application.
func writeMessage(conn *websocket.Conn, mutex *sync.Mutex, messageType int, data []byte) error {
	mutex.Lock()
	defer mutex.Unlock()
	if err := conn.WriteMessage(messageType, data); err != nil {
		return fmt.Errorf("unable to write message: %w", err)
	}
	return nil
}

// handleConnection will handle the connection with the client application.
func (s *WebSocketServer) handleConnection(ctx context.Context, w http.ResponseWriter, r *http.Request,
	wsUpgrader websocket.Upgrader, handler WebSocketServerEventHandler,
) {
	// Create a mutex to ensure thread safety and fire the connection handler to
	// authenticate the client connection request
	var mutex sync.Mutex
	clientKey, err := handler.OnConnectionHandler(w, r)
	if err != nil {
		return
	}

	// Upgrade the HTTP connection to a WebSocket connection
	conn, err := wsUpgrader.Upgrade(w, r, nil)
	if err != nil {
		handler.OnWebSocketUpgraderErrorHandler(clientKey, err)
		return
	}
	defer conn.Close()
	defer s.closeConnection(conn, &mutex, clientKey, handler)
	defer handler.OnDisconnectionHandler(clientKey)

	conn.SetPingHandler(func(appData string) error {
		data := handler.OnPingHandler(clientKey, appData)
		return writeMessage(conn, &mutex, websocket.PongMessage, data)
	})

	if err := handler.OnConnectedHandler(clientKey, &Session{
		// Generate a close function for the session
		Close: func() {
			s.closeConnection(conn, &mutex, clientKey, handler)
		},
		// Generate a ping function for the session
		Ping: func(data []byte) error {
			return writeMessage(conn, &mutex, websocket.PingMessage, data)
		},
		// Generate a send message function for the session
		Send: func(data []byte) error {
			return writeMessage(conn, &mutex, websocket.BinaryMessage, data)
		},
	}); err != nil {
		// Unable to handle connection
		return
	}

	// Start the read loop
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer func() {
			if r := recover(); r != nil {
				var stackBuf [4096]byte
				n := runtime.Stack(stackBuf[:], false)
				handler.OnReadMessagePanicHandler(clientKey, fmt.Errorf("recovered from read panic: %v\n%s", r,
					string(stackBuf[:n])))
			}
		}()

		for {
			select {
			case <-ctx.Done():
				return
			default:
				messageType, data, err := conn.ReadMessage()
				if err != nil {
					// Close call may be sent from multiple areas of the server
					// application or by gorilla WebSocket automatically
					if errors.Is(err, websocket.ErrCloseSent) {
						return
					}
					var closeErr *websocket.CloseError
					if errors.As(err, &closeErr) {
						if !websocket.IsCloseError(err, websocket.CloseNormalClosure, websocket.CloseGoingAway) {
							handler.OnReadMessageErrorHandler(clientKey, closeErr)
						}
						return
					}
					continue
				}
				handler.OnReadMessageHandler(clientKey, messageType, data)
			}
		}
	}()

	select {
	case <-done:
	case <-ctx.Done():
	}
}
