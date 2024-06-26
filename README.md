# Ankh

[![codecov](https://codecov.io/github/mikefero/ankh/graph/badge.svg?token=7F8518RX0T)](https://codecov.io/github/mikefero/ankh)

Ankh is a robust and flexible WebSocket client/server written in Go. It provides
a set of interfaces and types to handle WebSocket connections, allowing you to
build scalable real-time applications.

## Getting Started

### Installation

To use Ankh, you'll need to have Go installed on your system. You can
[download and install Go from the official website].

### WebSocket Client/Server

Features:

- **Efficient Connection Management**: Ankh streamlines upgrading HTTP to
  WebSocket connections and their ongoing management.
- **Thread-safe Session Interaction**: Provides thread-safe mechanisms to send
  messages and close connections, ensuring safe concurrent access to interact
	directly with connected clients.
- **Customizable Event Handlers and Lifecycle Management**: Customize handlers
  for a variety of WebSocket events including connections, disconnections,
	messages, pings, and errors, ensuring robust lifecycle management.
- **TLS Security**: Supports secure WebSocket connections through configurable
  TLS, enhancing data security.
- **Versatile Path and Handler Configuration**: Supports multiple paths with
  specific handlers, allowing for diverse client requirements and interactions.

#### Usage

##### WebSocket Client

###### 1. Define Your Event Handlers

Implement the `WebSocketClientEventHandler` interface to handle WebSocket
events:

```go
type MyWebSocketClientHandler struct{}

func (h *MyWebSocketClientHandler) OnConnectedHandler(resp *http.Response, session *ankh.Session) error {
    // Handle post-connection setup
    fmt.Println("connected to server")
    return nil
}

func (h *MyWebSocketClientHandler) OnDisconnectionHandler() {
    // Handle disconnection cleanup
    fmt.Println("disconnected from server")
}

func (h *MyWebSocketClientHandler) OnDisconnectionErrorHandler(err error) {
    // Handle disconnection errors
    fmt.Println("disconnection error:", err)
}

func (h *MyWebSocketClientHandler) OnPongHandler(appData string) {
    // Handle pong messages
    fmt.Println("pong received:", appData)
}

func (h *MyWebSocketClientHandler) OnReadMessageHandler(messageType int, data []byte) {
    // Handle incoming messages
    fmt.Println("message received:", string(data))
}

func (h *MyWebSocketClientHandler) OnReadMessageErrorHandler(err error) {
    // Handle read message errors
    fmt.Println("read message error:", err)
}

func (h *MyWebSocketClientHandler) OnReadMessagePanicHandler(err error) {
    // Handle read message panic
    fmt.Println("read message panic:", err)
}
```

###### 2. Create and Configure the WebSocket Client

Configure the client with the appropriate options:

```go
opts := ankh.WebSocketClientOpts{
    Handler:          &MyWebSocketClientHandler{},
    HandShakeTimeout: 10 * time.Second,
    ServerURL:        url.URL{Scheme: "ws", Host: "localhost:3737", Path: "/path"},
    TLSConfig:        nil, // Or provide a TLS configuration for secure connections
}

client, err := ankh.NewWebSocketClient(opts)
if err != nil {
    log.Fatalf("failed to create client: %v", err)
}
```

###### 3. Run the Client

Run the client within a context to manage its lifecycle:

```go
ctx, cancel := context.WithCancel(context.Background())
defer cancel()

if err := client.Run(ctx); err != nil {
    log.Fatalf("client error: %v", err)
}
```

###### Example

Here's a complete example combining the above steps:

```go
package main

import (
    "context"
    "fmt"
    "log"
    "net/http"
    "net/url"
    "time"

    "github.com/mikefero/ankh"
)

type MyWebSocketClientHandler struct{}

func (h *MyWebSocketClientHandler) OnConnectedHandler(resp *http.Response, session *ankh.Session) error {
    fmt.Println("connected to server")
    return nil
}

func (h *MyWebSocketClientHandler) OnDisconnectionHandler() {
    fmt.Println("disconnected from server")
}

func (h *MyWebSocketClientHandler) OnDisconnectionErrorHandler(err error) {
    fmt.Println("disconnection error:", err)
}

func (h *MyWebSocketClientHandler) OnPongHandler(appData string) {
    fmt.Println("pong received:", appData)
}

func (h *MyWebSocketClientHandler) OnReadMessageHandler(messageType int, data []byte) {
    fmt.Println("message received:", string(data))
}

func (h *MyWebSocketClientHandler) OnReadMessageErrorHandler(err error) {
    fmt.Println("read message error:", err)
}

func (h *MyWebSocketClientHandler) OnReadMessagePanicHandler(err error) {
    fmt.Println("read message panic:", err)
}

func main() {
    opts := ankh.WebSocketClientOpts{
        Handler:          &MyWebSocketClientHandler{},
        HandShakeTimeout: 10 * time.Second,
        ServerURL:        url.URL{Scheme: "ws", Host: "localhost:3737", Path: "/path"},
    }

    client, err := ankh.NewWebSocketClient(opts)
    if err != nil {
        log.Fatalf("failed to create client: %v", err)
    }

    ctx, cancel := context.WithCancel(context.Background())
    defer cancel()

    if err := client.Run(ctx); err != nil {
        log.Fatalf("client error: %v", err)
    }
}
```

##### WebSocket Server

###### 1. Define Your Event Handlers

Implement the `WebSocketServerEventHandler` interface to handle WebSocket
events:

```go
type MyWebSocketServerHandler struct{
	sessions map[any]*ankh.Session
	mutex    sync.Mutex
}

func (h *MyWebSocketServerHandler) OnConnectionHandler(w http.ResponseWriter, r *http.Request) (any, error) {
	// Authenticate the client and return a client key
	return "clientKey", nil
}

func (h *MyWebSocketServerHandler) OnConnectedHandler(clientKey any, session Session) error {
	// Handle post-connection setup
	log.Printf("client connected: %v", clientKey)

	// Store the session in a thread-safe manner
	h.mutex.Lock()
	defer h.mutex.Unlock()
	h.sessions[clientKey] = session
	return nil
}

func (h *MyWebSocketServerHandler) OnDisconnectionHandler(clientKey any) {
	// Handle disconnection cleanup
	log.Printf("client disconnected: %v", clientKey)

	// Remove the session in a thread-safe manner
	h.mutex.Lock()
	defer h.mutex.Unlock()
	delete(h.sessions, clientKey)
}

func (h *MyWebSocketServerHandler) OnDisconnectionErrorHandler(clientKey any, err error) {
	// Handle disconnection errors; OnDisconnectionHandler is still called
	log.Printf("disconnection error: %v, client: %v", err, clientKey)
}

func (h *MyWebSocketServerHandler) OnPingHandler(clientKey any, appData string) []byte {
	// Handle ping messages
	log.Printf("ping received from client: %v, data: %v", clientKey, appData)
	return []byte("pong message or nil")
}

func (h *MyWebSocketServerHandler) OnReadMessageHandler(clientKey any, messageType int, data []byte) {
	// Handle incoming messages
	log.Printf("message received from client: %v, type: %v, data: %s", clientKey, messageType, string(data))
}

func (h *MyWebSocketServerHandler) OnReadMessageErrorHandler(clientKey any, err error) {
	// Handle read message errors
	log.Printf("read message error: %v, client: %v", err, clientKey)
}

func (h *MyWebSocketServerHandler) OnReadMessagePanicHandler(clientKey any, err error) {
	// Handle read message panic
	log.Printf("read message panic: %v, client: %v", err, clientKey)
}

func (h *MyWebSocketServerHandler) OnWebSocketUpgraderErrorHandler(clientKey any, err error) {
	// Handle WebSocket upgrader errors
	log.Printf("websocket upgrader error: %v, client: %v", err, clientKey)
}
```

###### 2. Create and Configure the WebSocket Server

Configure the server with the appropriate options:

```go
opts := WebSocketServerOpts{
	Address:            "localhost:3737",
	IsKeepAlivesEnabled: true,
	PathHandlers:        PathHandlers{"/path": &MyWebSocketServerHandler{}},
	ReadHeaderTimeout:   10 * time.Second,
	ReadTimeout:         10 * time.Second,
	ShutdownTimeout:     5 * time.Second,
	TLSConfig:           nil, // Or provide a TLS configuration for secure connections
}

server, err := NewWebSocketServer(opts)
if err != nil {
	log.Fatalf("failed to create server: %v", err)
}
```

###### 3. Run the Server

Run the server within a context to manage its lifecycle:

```go
ctx, cancel := context.WithCancel(context.Background())
defer cancel()

if err := server.Run(ctx); err != nil {
	log.Fatalf("Server error: %v", err)
}
```

###### Example

Here's a complete example combining the above steps:

```go
package main

import (
	"context"
	"log"
	"net/http"
	"time"

	"github.com/mikefero/ankh"
)

type MyWebSocketServerHandler struct{
	sessions map[any]*ankh.Session
	mutex    sync.Mutex
}

func (h *MyWebSocketServerHandler) OnConnectionHandler(w http.ResponseWriter, r *http.Request) (any, error) {
	return "clientKey", nil
}

func (h *MyWebSocketServerHandler) OnConnectedHandler(clientKey any, session *ankh.Session) error {
	log.Printf("client connected: %v", clientKey)

	// Send a welcome message
	err := session.Send([]byte("Welcome to the Ankh WebSocket server!"))
	if err != nil {
		log.Printf("failed to send welcome message: %v", err)
		return err
	}

	// Store the session in a thread-safe manner
	h.mutex.Lock()
	defer h.mutex.Unlock()
	h.sessions[clientKey] = session
	return nil
}

func (h *MyWebSocketServerHandler) OnDisconnectionHandler(clientKey any) {
	log.Printf("client disconnected: %v", clientKey)

	// Remove the session in a thread-safe manner
	h.mutex.Lock()
	defer h.mutex.Unlock()
	delete(h.sessions, clientKey)
}

func (h *MyWebSocketServerHandler) OnDisconnectionErrorHandler(clientKey any, err error) {
	log.Printf("disconnection error: %v, client: %v", err, clientKey)
}

func (h *MyWebSocketServerHandler) OnPingHandler(clientKey any, appData string) []byte {
	log.Printf("ping received from client: %v, data: %v", clientKey, appData)
	return []byte("pong message or nil")
}

func (h *MyWebSocketServerHandler) OnReadMessageHandler(clientKey any, messageType int, data []byte) {
	log.Printf("message received from client: %v, type: %v, data: %s", clientKey, messageType, string(data))
}

func (h *MyWebSocketServerHandler) OnReadMessageErrorHandler(clientKey any, err error) {
	log.Printf("read message error: %v, client: %v", err, clientKey)
}

func (h *MyWebSocketServerHandler) OnReadMessagePanicHandler(clientKey any, err error) {
	log.Printf("read message panic: %v, client: %v", err, clientKey)
}

func (h *MyWebSocketServerHandler) OnWebSocketUpgraderErrorHandler(clientKey any, err error) {
	log.Printf("websocket upgrader error: %v, client: %v", err, clientKey)
}

func main() {
	opts := ankh.WebSocketServerOpts{
		Address:             "localhost:3737",
		IsKeepAlivesEnabled: true,
		PathHandlers:        ankh.PathHandlers{"/path": &MyWebSocketServerHandler{}},
		ReadHeaderTimeout:   10 * time.Second,
		ReadTimeout:         10 * time.Second,
		ShutdownTimeout:     5 * time.Second,
	}

	server, err := ankh.NewWebSocketServer(opts)
	if err != nil {
		log.Fatalf("failed to create server: %v", err)
		return
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := server.Run(ctx); err != nil {
		log.Fatalf("server error: %v", err)
	}
}
```

#### Handling Sessions

The Session type provides thread-safe methods to interact with a connected
WebSocket client/server. You can use it to send messages or close the
connection.

- **Close the Connection**: To close the client/server connection, use the
	`Close` method.
- **Send a Ping Message**: To send a ping message to the client/server, use the
  `Ping` method.
- **Send a Binary Message**: To send a binary message to the client/server, use
  the `Send` method.

## License

This project is licensed under the Apache License, Version 2.0. See the
[LICENSE] file for details.

## Acknowledgements

- [Gorilla WebSocket] - A fast, well-tested, and widely used WebSocket library
  in Go.
- [golangci-lint] - A fast Go linters runner for Go. It runs linters in
  parallel, caching their results for much faster runs.
- [mockio] - A mocking framework for Go that helps in creating and using mocks
  for testing purposes.

[download and install Go from the official website]: https://golang.org/dl/
[LICENSE]: LICENSE
[Gorilla WebSocket]: https://github.com/gorilla/websocket
[golangci-lint]: https://github.com/golangci/golangci-lint
[mockio]: https://github.com/ovechkin-dm/mockio/mock
