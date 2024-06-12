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
	"net/http"
	"net/url"
	"runtime"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

// WebsocketClientEventHandler represents the callback handlers for the
// WebSocket client.
type WebSocketClientEventHandler interface {
	// OnConnectedHandler is a function callback for indicating that the client
	// connection is completed while creating handles for closing connections and
	// sending messages on the WebSocket.
	OnConnectedHandler(resp *http.Response, session Session) error

	// OnDisconnectionHandler is a function callback for WebSocket connections
	// that is executed upon disconnection of a client.
	OnDisconnectionHandler()

	// OnDisconnectionErrorHandler is a function callback for WebSocket
	// disconnection error received from the WebSocket.
	OnDisconnectionErrorHandler(err error)

	// OnPongHandler is a function callback for WebSocket connections during pong
	// operations.
	OnPongHandler(appData string)

	// OnReadMessageHandler is a function callback for WebSocket connection that
	// is executed upon a message received from the WebSocket.
	OnReadMessageHandler(messageType int, data []byte)

	// OnReadMessageErrorHandler is a function callback for WebSocket connection
	// read error received from the WebSocket.
	OnReadMessageErrorHandler(err error)

	// OnReadMessagePanicHandler is a function callback for WebSocket connection
	// read panic received from the WebSocket.
	OnReadMessagePanicHandler(err error)
}

// WebSocketClientOpts are the options for the WebSocketClient.
type WebSocketClientOpts struct {
	// Handler specifies the callback handler for the WebSocketClient.
	Handler WebSocketClientEventHandler
	// HandShakeTimeout specifies the amount of time allowed to complete the
	// WebSocket handshake.
	HandShakeTimeout time.Duration
	// ServerURL specifies the WebSocket server URL.
	ServerURL url.URL
	// TLSConfig specifies the TLS configuration for the WebSocketClient.
	TLSConfig *tls.Config
}

// WebSocketClient is the client instance for a WebSocket.
type WebSocketClient struct {
	handler   WebSocketClientEventHandler
	serverURL url.URL

	dialer      websocket.Dialer
	isConnected bool
	mutex       sync.Mutex
}

// NewWebSocketClient creates a new WebSocketClient instance. Options are
// validated and will return an error if any are invalid.
func NewWebSocketClient(opts WebSocketClientOpts) (*WebSocketClient, error) {
	if opts.Handler == nil {
		return nil, errors.New("handler must not be empty")
	}

	serverURL := opts.ServerURL
	serverURL.Scheme = "ws"
	dialer := websocket.Dialer{
		HandshakeTimeout: opts.HandShakeTimeout,
	}
	if opts.TLSConfig != nil {
		serverURL.Scheme = "wss"
		dialer.TLSClientConfig = opts.TLSConfig
	}

	return &WebSocketClient{
		serverURL: opts.ServerURL,
		handler:   opts.Handler,

		dialer:      dialer,
		isConnected: false,
	}, nil
}

func (c *WebSocketClient) Run(ctx context.Context) error {
	conn, resp, err := c.dialer.Dial(c.serverURL.String(), nil)
	if err != nil {
		return fmt.Errorf("unable to connect to server URL at %s (%s): %w", c.serverURL.String(), resp.Status, err)
	}
	defer conn.Close()
	defer c.closeConnection(conn)
	defer c.handler.OnDisconnectionHandler()

	if err := c.handler.OnConnectedHandler(resp, Session{
		// Generate a close function for the session
		Close: func() {
			c.closeConnection(conn)
		},
		// Generate a ping function for the session
		Ping: func(data []byte) error {
			return writeMessage(conn, &c.mutex, websocket.PingMessage, data)
		},
		// Generate a send message function for the session
		Send: func(data []byte) error {
			return writeMessage(conn, &c.mutex, websocket.BinaryMessage, data)
		},
	}); err != nil {
		// Unable to handle connection
		return fmt.Errorf("unable to handle connection: %w", err)
	}

	// Update the connection status
	c.mutex.Lock()
	c.isConnected = true
	c.mutex.Unlock()

	conn.SetPongHandler(func(appData string) error {
		c.handler.OnPongHandler(appData)
		return nil
	})

	// Start the read loop
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer func() {
			if r := recover(); r != nil {
				var stackBuf [4096]byte
				n := runtime.Stack(stackBuf[:], false)
				c.handler.OnReadMessagePanicHandler(fmt.Errorf("recovered from read panic: %v\n%s", r,
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
					// Close call may be sent from multiple areas of the client
					// application or by gorilla WebSocket automatically
					if errors.Is(err, websocket.ErrCloseSent) {
						return
					}
					var closeErr *websocket.CloseError
					if errors.As(err, &closeErr) {
						if !websocket.IsCloseError(err, websocket.CloseNormalClosure, websocket.CloseGoingAway) {
							c.handler.OnReadMessageErrorHandler(closeErr)
						}
						return
					}
					continue
				}
				c.handler.OnReadMessageHandler(messageType, data)
			}
		}
	}()

	select {
	case <-done:
	case <-ctx.Done():
	}

	// Update the connection status
	c.mutex.Lock()
	defer c.mutex.Unlock()
	c.isConnected = false
	return nil
}

// closeConnection will close the connection with the client application.
func (c *WebSocketClient) closeConnection(conn *websocket.Conn) {
	// Ensure closed message is not sent when connection is already closed
	err := writeMessage(conn, &c.mutex, websocket.CloseMessage,
		websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""))

	// Close call may be sent from multiple areas of the client application or
	// by gorilla WebSocket automatically; ensure disconnection handler is not
	// called twice
	if err != nil && !errors.Is(err, websocket.ErrCloseSent) {
		c.handler.OnDisconnectionErrorHandler(err)
	}
}

func (c *WebSocketClient) IsConnected() bool {
	c.mutex.Lock()
	defer c.mutex.Unlock()
	return c.isConnected
}
