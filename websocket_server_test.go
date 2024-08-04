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
package ankh_test

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net"
	"net/http"
	"net/url"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/mikefero/ankh"
	"github.com/stretchr/testify/require"

	"github.com/ovechkin-dm/mockio/matchers"
	. "github.com/ovechkin-dm/mockio/mock"
)

func generateTestCertificate(t *testing.T) *tls.Config {
	t.Helper()

	priv, err := rsa.GenerateKey(rand.Reader, 1024)
	if err != nil {
		t.Fatalf("failed to generate private key: %v", err)
	}

	template := x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject: pkix.Name{
			Organization: []string{"ankh"},
		},
		NotBefore:             time.Now(),
		NotAfter:              time.Now().Add(30 * time.Minute),
		KeyUsage:              x509.KeyUsageKeyEncipherment | x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
	}

	derBytes, err := x509.CreateCertificate(rand.Reader, &template, &template, &priv.PublicKey, priv)
	if err != nil {
		t.Fatalf("failed to create certificate: %v", err)
	}

	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: derBytes})
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(priv)})
	cert, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		t.Fatalf("failed to load key pair: %v", err)
	}

	return &tls.Config{
		Certificates:       []tls.Certificate{cert},
		InsecureSkipVerify: true,
	}
}

func waitForServer(t *testing.T, address string, establishConnection bool) {
	t.Helper()

	timeout := 10 * time.Second
	deadline := time.Now().Add(timeout)
	for {
		var conn net.Conn
		var err error

		conn, err = net.Dial("tcp", address)
		if err == nil {
			conn.Close()
		}

		if establishConnection && err == nil {
			return
		}
		if !establishConnection && err != nil {
			return
		}

		if time.Now().After(deadline) {
			if establishConnection {
				t.Fatalf("server at %s did not become available within %v", address, timeout)
			} else {
				t.Fatalf("server at %s did not become unavailable within %v", address, timeout)
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func waitFor(t *testing.T) {
	t.Helper()

	defaultWaitFor := 100 * time.Millisecond
	waitForStr := os.Getenv("ANKH_TEST_WAIT_FOR")
	waitFor := defaultWaitFor
	if len(waitForStr) != 0 {
		var err error
		waitFor, err = time.ParseDuration(waitForStr)
		if err != nil {
			t.Fatalf("failed to parse timeout from ANKH_TEST_WAIT_FOR: %v", err)
		}
	}

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		time.Sleep(waitFor)
	}()
	wg.Wait()
}

func waitForCapture[T any](t *testing.T, captor matchers.ArgumentCaptor[T]) {
	t.Helper()

	defaultWaitForCapture := 100 * time.Millisecond
	waitForCapturStr := os.Getenv("ANKH_TEST_WAIT_FOR_CAPTURE")
	waitForCapture := defaultWaitForCapture
	if len(waitForCapturStr) != 0 {
		var err error
		waitForCapture, err = time.ParseDuration(waitForCapturStr)
		if err != nil {
			t.Fatalf("failed to parse timeout from ANKH_TEST_WAIT_FOR_CAPTURE: %v", err)
		}
	}

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 100; i++ {
			if len(captor.Values()) != 0 {
				return
			}
			time.Sleep(waitForCapture)
		}
	}()
	wg.Wait()
}

func findUnusedLocalAddress(t *testing.T) string {
	t.Helper()

	var address string
	for i := 0; i < 10; i++ {
		if listener, err := net.Listen("tcp", "localhost:0"); err == nil {
			address = listener.Addr().String()
			listener.Close()
			break
		}
	}
	if len(address) == 0 {
		t.Fatalf("failed to get random address after 10 attempts")
	}
	return address
}

type webSocketServer struct {
	address      string
	cancel       context.CancelFunc
	mockHandlers []ankh.WebSocketServerEventHandler
	serverURL    url.URL
	tlsConfig    *tls.Config
}

func createWebSocketServer(t *testing.T, enableTLS bool) *webSocketServer {
	t.Helper()

	address := findUnusedLocalAddress(t)
	serverURL := url.URL{
		Scheme: "ws",
		Host:   address,
	}
	handler1 := Mock[ankh.WebSocketServerEventHandler]()
	handler2 := Mock[ankh.WebSocketServerEventHandler]()

	var tlsConfig *tls.Config
	if enableTLS {
		serverURL.Scheme = "wss"
		tlsConfig = generateTestCertificate(t)
	}

	opts := ankh.WebSocketServerOpts{
		Address:             address,
		IsKeepAlivesEnabled: true,
		PathHandlers: ankh.PathHandlers{
			"/path1": handler1,
			"/path2": handler2,
		},
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       5 * time.Second,
		ShutdownTimeout:   5 * time.Second,
		TLSConfig:         tlsConfig,
	}
	server, err := ankh.NewWebSocketServer(opts)
	if err != nil {
		t.Fatalf("failed to create WebSocket server: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		err := server.Run(ctx)
		require.NoError(t, err)
	}()
	waitForServer(t, address, true)

	return &webSocketServer{
		address: address,
		cancel:  cancel,
		mockHandlers: []ankh.WebSocketServerEventHandler{
			handler1,
			handler2,
		},
		serverURL: serverURL,
		tlsConfig: tlsConfig,
	}
}

func TestWebSocketServer(t *testing.T) {
	t.Run("error when creating WebSocket server with empty address", func(t *testing.T) {
		t.Parallel()

		_, err := ankh.NewWebSocketServer(ankh.WebSocketServerOpts{
			ShutdownTimeout: 5 * time.Second,
		})
		require.Error(t, err)
		require.ErrorContains(t, err, "address must not be empty")
	})

	t.Run("error when creating WebSocket server with empty shutdown timeout", func(t *testing.T) {
		t.Parallel()

		_, err := ankh.NewWebSocketServer(ankh.WebSocketServerOpts{
			Address: "localhost:0",
		})
		require.Error(t, err)
		require.ErrorContains(t, err, "shutdown timeout must be > 0")
	})

	t.Run("error when creating WebSocket server with empty path handlers", func(t *testing.T) {
		t.Parallel()

		_, err := ankh.NewWebSocketServer(ankh.WebSocketServerOpts{
			Address:         "localhost:0",
			ShutdownTimeout: 5 * time.Second,
		})
		require.Error(t, err)
		require.ErrorContains(t, err, "path handlers must not be empty")
	})

	tests := []struct {
		withTLS bool
		suffix  string
	}{
		{false, ""},
		{true, " with TLS"},
	}
	for _, tt := range tests {
		tt := tt // create a new instance of tt for each iteration (loopclosure)

		t.Run(fmt.Sprintf("can start a ankh WebSocket server and handle multiple clients%s", tt.suffix), func(t *testing.T) {
			t.Parallel()
			SetUp(t)

			t.Log("creating WebSocket server")
			webSocketServer := createWebSocketServer(t, tt.withTLS)
			handler1 := webSocketServer.mockHandlers[0]
			handler2 := webSocketServer.mockHandlers[1]
			defer webSocketServer.cancel()

			t.Log("creating WebSocket clients")
			When(handler1.OnConnectionHandler(Any[http.ResponseWriter](), Any[*http.Request]())).
				ThenReturn("client-key-1", nil).
				ThenReturn("client-key-2", nil)
			When(handler2.OnConnectionHandler(Any[http.ResponseWriter](), Any[*http.Request]())).
				ThenReturn("client-key-3", nil).
				ThenReturn("client-key-4", nil)
			captor1Session := Captor[*ankh.Session]()
			captor2Session := Captor[*ankh.Session]()
			captor3Session := Captor[*ankh.Session]()
			captor4Session := Captor[*ankh.Session]()
			WhenSingle(handler1.OnConnectedHandler(Exact("client-key-1"), captor1Session.Capture())).ThenReturn(nil)
			WhenSingle(handler1.OnConnectedHandler(Exact("client-key-2"), captor2Session.Capture())).ThenReturn(nil)
			WhenSingle(handler2.OnConnectedHandler(Exact("client-key-3"), captor3Session.Capture())).ThenReturn(nil)
			WhenSingle(handler2.OnConnectedHandler(Exact("client-key-4"), captor4Session.Capture())).ThenReturn(nil)
			path1ServerURL := webSocketServer.serverURL
			path1ServerURL.Path = "/path1"
			path2ServerURL := webSocketServer.serverURL
			path2ServerURL.Path = "/path2"
			client1 := createWebSocketClient(t, path1ServerURL, tt.withTLS)
			client2 := createWebSocketClient(t, path1ServerURL, tt.withTLS)
			client3 := createWebSocketClient(t, path2ServerURL, tt.withTLS)
			client4 := createWebSocketClient(t, path2ServerURL, tt.withTLS)
			runWebSocketClient(t, client1, false)
			runWebSocketClient(t, client2, false)
			runWebSocketClient(t, client3, false)
			runWebSocketClient(t, client4, false)
			defer func() {
				client1.cancel()
				client2.cancel()
				client3.cancel()
				client4.cancel()
			}()
			if tt.withTLS {
				tlsV13 := (uint16(tls.VersionTLS13))
				require.NotNil(t, captor1Session.Last().ConnectionState)
				require.Equal(t, tlsV13, captor1Session.Last().ConnectionState.Version)
				require.NotNil(t, captor2Session.Last().ConnectionState)
				require.Equal(t, tlsV13, captor2Session.Last().ConnectionState.Version)
				require.NotNil(t, captor3Session.Last().ConnectionState)
				require.Equal(t, tlsV13, captor3Session.Last().ConnectionState.Version)
				require.NotNil(t, captor4Session.Last().ConnectionState)
				require.Equal(t, tlsV13, captor4Session.Last().ConnectionState.Version)
			}

			t.Log("verify WebSocket clients connected")
			Verify(handler1, Times(2)).OnConnectionHandler(Any[http.ResponseWriter](), Any[*http.Request]())
			Verify(handler2, Times(2)).OnConnectionHandler(Any[http.ResponseWriter](), Any[*http.Request]())
			Verify(handler1, Once()).OnConnectedHandler(Exact("client-key-1"), Any[*ankh.Session]())
			Verify(handler1, Once()).OnConnectedHandler(Exact("client-key-2"), Any[*ankh.Session]())
			Verify(handler2, Once()).OnConnectedHandler(Exact("client-key-3"), Any[*ankh.Session]())
			Verify(handler2, Once()).OnConnectedHandler(Exact("client-key-4"), Any[*ankh.Session]())
			VerifyNoMoreInteractions(handler1)
			VerifyNoMoreInteractions(handler2)

			t.Log("verify closing the connection of the WebSocket client will close the connection with the server")
			// Close the WebSocket server connection and wait for client to receive the close message
			captor2Session.Last().Close()
			waitFor(t) // wait for the close messages to be handled
			Verify(handler1, Once()).OnDisconnectionHandler(Exact("client-key-2"))
			VerifyNoMoreInteractions(handler1)
			require.False(t, client2.client.IsConnected())

			// This is mainly for coverage as the client application doesn't have ping
			// event handler
			t.Log("server can send ping messages to the client")
			captor1Session.Last().Ping([]byte("ankh-server"))
			captor3Session.Last().Ping([]byte("ankh-server"))
			captor4Session.Last().Ping([]byte("ankh-server"))

			t.Log("verify ping message from the client to the WebSocket server is handled")
			WhenSingle(handler1.OnPingHandler(Exact("client-key-1"), Exact("client1"))).ThenReturn([]byte("client1-pong"))
			WhenSingle(handler2.OnPingHandler(Exact("client-key-3"), Exact("client3"))).ThenReturn([]byte("client3-pong"))
			WhenSingle(handler2.OnPingHandler(Exact("client-key-4"), Exact("client4"))).ThenReturn([]byte("client4-pong"))
			client1.session.Ping([]byte("client1"))
			client3.session.Ping([]byte("client3"))
			client4.session.Ping([]byte("client4"))
			waitFor(t) // wait for the ping messages to be handled
			Verify(handler1, Once()).OnPingHandler(Exact("client-key-1"), Exact("client1"))
			Verify(handler2, Once()).OnPingHandler(Exact("client-key-3"), Exact("client3"))
			Verify(handler2, Once()).OnPingHandler(Exact("client-key-4"), Exact("client4"))
			VerifyNoMoreInteractions(handler1)
			VerifyNoMoreInteractions(handler2)

			t.Log("verify message from WebSocket server to the client is sent")
			captor1ReadMessage := Captor[[]byte]()
			captor3ReadMessage := Captor[[]byte]()
			captor4ReadMessage := Captor[[]byte]()
			captor1Session.Last().Send([]byte("ankh-1"))
			waitFor(t) // wait for the read messages to be handled
			Verify(client1.mockHandler, Once()).OnReadMessageHandler(Exact(websocket.BinaryMessage), captor1ReadMessage.Capture())
			captor3Session.Last().Send([]byte("ankh-3"))
			waitFor(t) // wait for the read messages to be handled
			Verify(client3.mockHandler, Once()).OnReadMessageHandler(Exact(websocket.BinaryMessage), captor3ReadMessage.Capture())
			waitFor(t) // wait for the read messages to be handled
			captor4Session.Last().Send([]byte("ankh-4"))
			Verify(client4.mockHandler, Once()).OnReadMessageHandler(Exact(websocket.BinaryMessage), captor4ReadMessage.Capture())
			waitForCapture(t, captor1ReadMessage)
			require.Equal(t, []byte("ankh-1"), captor1ReadMessage.Last())
			waitForCapture(t, captor3ReadMessage)
			require.Equal(t, []byte("ankh-3"), captor3ReadMessage.Last())
			waitForCapture(t, captor4ReadMessage)
			require.Equal(t, []byte("ankh-4"), captor4ReadMessage.Last())

			t.Log("verify closing the connection of the WebSocket server will close the connection with the client")
			webSocketServer.cancel()
			waitFor(t) // wait for the close messages to be handled
			Verify(handler1, Once()).OnDisconnectionHandler(Exact("client-key-1"))
			Verify(handler2, Once()).OnDisconnectionHandler(Exact("client-key-3"))
			Verify(handler2, Once()).OnDisconnectionHandler(Exact("client-key-4"))
			require.False(t, client1.client.IsConnected())
			require.False(t, client3.client.IsConnected())
			require.False(t, client4.client.IsConnected())
		})

		t.Run(fmt.Sprintf("a error occurs when starting WebSocket server with invalid address%s", tt.suffix), func(t *testing.T) {
			t.Parallel()
			SetUp(t)

			address := "invalid-address:0"
			handler := Mock[ankh.WebSocketServerEventHandler]()
			var tlsConfig *tls.Config
			if tt.withTLS {
				tlsConfig = generateTestCertificate(t)
			}
			opts := ankh.WebSocketServerOpts{
				Address: address,
				PathHandlers: ankh.PathHandlers{
					"/path": handler,
				},
				ShutdownTimeout: 5 * time.Second,
				TLSConfig:       tlsConfig,
			}

			server, err := ankh.NewWebSocketServer(opts)
			require.NoError(t, err)
			errChan := make(chan error)
			go func() {
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				defer close(errChan)
				err := server.Run(ctx)
				errChan <- err
			}()

			select {
			case err := <-errChan:
				require.Error(t, err)
			case <-time.After(5 * time.Second):
				t.Fatal("timeout waiting for server to return error")
			}

			VerifyNoMoreInteractions(handler)
		})

		t.Run(fmt.Sprintf("improperly closing the connection of the WebSocket server will close the connection with the client%s", tt.suffix), func(t *testing.T) {
			t.Parallel()
			SetUp(t)
			webSocketServer := createWebSocketServer(t, tt.withTLS)
			defer webSocketServer.cancel()

			handler := webSocketServer.mockHandlers[0]
			captor := Captor[*http.Request]()
			When(handler.OnConnectionHandler(Any[http.ResponseWriter](), captor.Capture())).ThenReturn("client-key", nil)

			var conn net.Conn
			if tt.withTLS {
				var err error
				conn, err = tls.Dial("tcp", webSocketServer.address, webSocketServer.tlsConfig)
				require.NoError(t, err)
			} else {
				var err error
				conn, err = net.Dial("tcp", webSocketServer.address)
				require.NoError(t, err)
			}
			defer conn.Close()

			// Perform the WebSocket upgrade request handshake manually
			key := make([]byte, 16)
			_, err := rand.Read(key)
			require.NoError(t, err)
			secWebSocketKey := base64.StdEncoding.EncodeToString(key)
			request := "GET /path1 HTTP/1.1\r\n" +
				"Host: %s\r\n" +
				"Upgrade: websocket\r\n" +
				"Connection: Upgrade\r\n" +
				"Sec-WebSocket-Key: %s\r\n" +
				"Sec-WebSocket-Version: 13\r\n\r\n"
			fmt.Fprintf(conn, request, webSocketServer.address, secWebSocketKey)

			// Read the response
			var responseBuffer [4096]byte
			n, err := conn.Read(responseBuffer[:])
			require.NoError(t, err)
			response := string(responseBuffer[:n])
			require.Contains(t, response, "HTTP/1.1 101 Switching Protocols")

			waitForCapture(t, captor)
			require.NotNil(t, captor.Last())
			conn.Close()
			waitFor(t) // wait for handlers to be called

			Verify(handler, Once()).OnConnectionHandler(Any[http.ResponseWriter](), Any[*http.Request]())
			Verify(handler, Once()).OnConnectedHandler(Exact("client-key"), Any[*ankh.Session]())
			Verify(handler, Once()).OnDisconnectionHandler(Exact("client-key"))
			Verify(handler, Once()).OnReadMessageErrorHandler(Exact("client-key"), Any[error]()) // connection closed improperly
			VerifyNoMoreInteractions(handler)
		})

		t.Run(fmt.Sprintf("deny client connection to the WebSocket server%s", tt.suffix), func(t *testing.T) {
			t.Parallel()
			SetUp(t)
			webSocketServer := createWebSocketServer(t, tt.withTLS)
			defer webSocketServer.cancel()

			handler := webSocketServer.mockHandlers[0]
			captor := Captor[*http.Request]()
			When(handler.OnConnectionHandler(Any[http.ResponseWriter](), captor.Capture())).ThenReturn("", errors.New("connection denied"))
			serverURL := webSocketServer.serverURL
			serverURL.Path = "/path1"
			client := createWebSocketClient(t, serverURL, tt.withTLS)
			runWebSocketClient(t, client, true)
			require.NotEmpty(t, client)
			waitForCapture(t, captor)
			require.NotNil(t, captor.Last())

			Verify(handler, Once()).OnConnectionHandler(Any[http.ResponseWriter](), Any[*http.Request]())
			VerifyNoMoreInteractions(handler)
		})

		t.Run(fmt.Sprintf("server connection handles WebSocket upgrade error%s", tt.suffix), func(t *testing.T) {
			t.Parallel()
			SetUp(t)
			webSocketServer := createWebSocketServer(t, tt.withTLS)
			handler := webSocketServer.mockHandlers[0]
			defer webSocketServer.cancel()

			var conn net.Conn
			if tt.withTLS {
				var err error
				conn, err = tls.Dial("tcp", webSocketServer.address, webSocketServer.tlsConfig)
				require.NoError(t, err)
			} else {
				var err error
				conn, err = net.Dial("tcp", webSocketServer.address)
				require.NoError(t, err)
			}
			defer conn.Close()

			// Perform a WebSocket handshake manually which uses a missing key and version
			request := "GET /path1 HTTP/1.1\r\n" +
				"Host: %s\r\n" +
				"Upgrade: websocket\r\n" +
				"Connection: Upgrade\r\n\r\n"
			fmt.Fprintf(conn, request, webSocketServer.address)
			waitFor(t) // wait for handlers to be called

			Verify(handler, Once()).OnConnectionHandler(Any[http.ResponseWriter](), Any[*http.Request]())
			Verify(handler, Once()).OnWebSocketUpgraderErrorHandler(Any[any](), Any[error]())
			VerifyNoMoreInteractions(handler)
		})
	}
}
