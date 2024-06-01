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
	"encoding/pem"
	"errors"
	"fmt"
	"io"
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
			Organization: []string{"fero"},
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

func waitForCapture[T any](captor matchers.ArgumentCaptor[T]) {
	for i := 0; i < 100; i++ {
		if len(captor.Values()) != 0 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
}

type webSocketServer struct {
	address      string
	cancel       context.CancelFunc
	mockHandlers []ankh.WebSocketServerEventHandler
	tlsConfig    *tls.Config
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

func createWebSocketServer(t *testing.T, enableTLS bool) *webSocketServer {
	t.Helper()

	address := findUnusedLocalAddress(t)
	handler1 := Mock[ankh.WebSocketServerEventHandler]()
	handler2 := Mock[ankh.WebSocketServerEventHandler]()

	var tlsConfig *tls.Config
	if enableTLS {
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
		tlsConfig: tlsConfig,
	}
}

type webSocketClient struct {
	conn        *websocket.Conn
	cancel      context.CancelFunc
	close       func()
	pingMessage func(message string)
	readMessage func() (messageType int, p []byte, err error)
}

func createWebSocketClient(t *testing.T, address string, path string, enableTLS bool,
) (*webSocketClient, error) {
	t.Helper()

	webSocketDialer := &websocket.Dialer{
		HandshakeTimeout: 5 * time.Second,
	}
	scheme := "ws"
	if enableTLS {
		scheme = "wss"
		tlsConfig := generateTestCertificate(t)
		tlsConfig.ServerName = "fero.example.com"
		webSocketDialer.TLSClientConfig = tlsConfig
	}

	u := url.URL{
		Scheme: scheme,
		Host:   address,
		Path:   path,
	}
	ctx, cancel := context.WithCancel(context.Background())
	connChan := make(chan *websocket.Conn)
	errChan := make(chan error)

	go func() {
		conn, _, err := webSocketDialer.DialContext(ctx, u.String(), nil)
		if err != nil {
			errChan <- err
			return
		}
		connChan <- conn
	}()

	select {
	case conn := <-connChan:
		return &webSocketClient{
			conn:   conn,
			cancel: cancel,
			close: func() {
				if err := conn.WriteMessage(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, "")); err != nil {
					t.Fatalf("unable to close socket connection: %v", err)
				}
			},
			pingMessage: func(message string) {
				if err := conn.WriteMessage(websocket.PingMessage, []byte(message)); err != nil {
					t.Fatalf("unable to send ping message: %v", err)
				}
			},
			readMessage: func() (int, []byte, error) {
				defaultClientReadTimeout := 1 * time.Second
				clientReadTimeoutStr := os.Getenv("ANKH_TEST_CLIENT_READ_TIMEOUT")
				clientReadTimeout := defaultClientReadTimeout
				if len(clientReadTimeoutStr) != 0 {
					var err error
					clientReadTimeout, err = time.ParseDuration(clientReadTimeoutStr)
					if err != nil {
						t.Fatalf("failed to parse timeout from ANKH_TEST_WAIT_FOR: %v", err)
					}
				}
				ctx, cancel := context.WithTimeout(context.Background(), clientReadTimeout)
				defer cancel()

				result := make(chan struct {
					messageType int
					p           []byte
					err         error
				})

				go func() {
					var r io.Reader
					var messageType int
					var err error

					messageType, r, err = conn.NextReader()
					if err != nil {
						result <- struct {
							messageType int
							p           []byte
							err         error
						}{messageType, nil, err}
						return
					}
					p, err := io.ReadAll(r)
					result <- struct {
						messageType int
						p           []byte
						err         error
					}{messageType, p, err}
				}()

				select {
				case res := <-result:
					return res.messageType, res.p, res.err
				case <-ctx.Done():
					return 0, nil, ctx.Err()
				}
			},
		}, nil
	case err := <-errChan:
		defer cancel()
		return nil, fmt.Errorf("failed to connect client WebSocket: %v", err)
	case <-ctx.Done():
		defer cancel()
		return nil, fmt.Errorf("context cancelled before WebSocket connection could be established")
	}
}

func validateClientWebSocketClosed(t *testing.T, client *webSocketClient) {
	t.Helper()

	_, _, err := client.readMessage()
	var closeErr *websocket.CloseError
	if !errors.As(err, &closeErr) {
		t.Fatalf("expected close error, got %v", err)
	}
	require.Equal(t, websocket.CloseNormalClosure, closeErr.Code)
}

func waitFor(t *testing.T) {
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
			captor1ClientSession := Captor[ankh.ClientSession]()
			captor2ClientSession := Captor[ankh.ClientSession]()
			captor3ClientSession := Captor[ankh.ClientSession]()
			captor4ClientSession := Captor[ankh.ClientSession]()
			WhenSingle(handler1.OnConnectedHandler(Exact("client-key-1"), captor1ClientSession.Capture())).ThenReturn(nil)
			WhenSingle(handler1.OnConnectedHandler(Exact("client-key-2"), captor2ClientSession.Capture())).ThenReturn(nil)
			WhenSingle(handler2.OnConnectedHandler(Exact("client-key-3"), captor3ClientSession.Capture())).ThenReturn(nil)
			WhenSingle(handler2.OnConnectedHandler(Exact("client-key-4"), captor4ClientSession.Capture())).ThenReturn(nil)
			client1, err := createWebSocketClient(t, webSocketServer.address, "/path1", tt.withTLS)
			require.NoError(t, err)
			client2, err := createWebSocketClient(t, webSocketServer.address, "/path1", tt.withTLS)
			require.NoError(t, err)
			client3, err := createWebSocketClient(t, webSocketServer.address, "/path2", tt.withTLS)
			require.NoError(t, err)
			client4, err := createWebSocketClient(t, webSocketServer.address, "/path2", tt.withTLS)
			require.NoError(t, err)
			defer func() {
				client1.cancel()
				client2.cancel()
				client3.cancel()
				client4.cancel()
			}()

			t.Log("verify WebSocket clients connected")
			waitForCapture(captor1ClientSession)
			waitForCapture(captor2ClientSession)
			waitForCapture(captor3ClientSession)
			waitForCapture(captor4ClientSession)
			Verify(handler1, Times(2)).OnConnectionHandler(Any[http.ResponseWriter](), Any[*http.Request]())
			Verify(handler2, Times(2)).OnConnectionHandler(Any[http.ResponseWriter](), Any[*http.Request]())
			Verify(handler1, Once()).OnConnectedHandler(Exact("client-key-1"), Any[ankh.ClientSession]())
			Verify(handler1, Once()).OnConnectedHandler(Exact("client-key-2"), Any[ankh.ClientSession]())
			Verify(handler2, Once()).OnConnectedHandler(Exact("client-key-3"), Any[ankh.ClientSession]())
			Verify(handler2, Once()).OnConnectedHandler(Exact("client-key-4"), Any[ankh.ClientSession]())
			VerifyNoMoreInteractions(handler1)
			VerifyNoMoreInteractions(handler2)

			t.Log("verify closing the connection of the WebSocket client will close the connection with the server")
			// Close the WebSocket server connection and wait for client to receive the close message
			captor2ClientSession.Last().Close()
			_, _, err = client2.readMessage()
			var closeErr *websocket.CloseError
			if !errors.As(err, &closeErr) {
				t.Fatalf("expected close error for client2, got %v", err)
			}
			require.Equal(t, websocket.CloseNormalClosure, closeErr.Code)
			waitFor(t) // wait for the close messages to be handled
			Verify(handler1, Once()).OnDisconnectionHandler(Exact("client-key-2"))
			VerifyNoMoreInteractions(handler1)

			t.Log("verify ping message from the client to the WebSocket server is handled")
			When(handler1.OnPingHandler(Exact("client-key-1"), Exact("client1"))).ThenReturn([]byte("client1-pong"), nil)
			When(handler2.OnPingHandler(Exact("client-key-3"), Exact("client3"))).ThenReturn([]byte("client3-pong"), nil)
			When(handler2.OnPingHandler(Exact("client-key-4"), Exact("client4"))).ThenReturn([]byte("client4-pong"), nil)
			client1.pingMessage("client1")
			client3.pingMessage("client3")
			client4.pingMessage("client4")
			waitFor(t) // wait for the ping messages to be handled
			Verify(handler1, Once()).OnPingHandler(Exact("client-key-1"), Exact("client1"))
			Verify(handler2, Once()).OnPingHandler(Exact("client-key-3"), Exact("client3"))
			Verify(handler2, Once()).OnPingHandler(Exact("client-key-4"), Exact("client4"))
			VerifyNoMoreInteractions(handler1)
			VerifyNoMoreInteractions(handler2)

			t.Log("verify message from WebSocket server to the client is sent")
			captor1ClientSession.Last().Send([]byte("fero-1"))
			messageType, data, err := client1.readMessage()
			require.NoError(t, err)
			require.Equal(t, websocket.BinaryMessage, messageType)
			require.Equal(t, []byte("fero-1"), data)
			captor3ClientSession.Last().Send([]byte("fero-3"))
			messageType, data, err = client3.readMessage()
			require.NoError(t, err)
			require.Equal(t, websocket.BinaryMessage, messageType)
			require.Equal(t, []byte("fero-3"), data)
			captor4ClientSession.Last().Send([]byte("fero-4"))
			messageType, data, err = client4.readMessage()
			require.NoError(t, err)
			require.Equal(t, websocket.BinaryMessage, messageType)
			require.Equal(t, []byte("fero-4"), data)

			t.Log("verify closing the connection of the WebSocket server will close the connection with the client")
			webSocketServer.cancel()
			validateClientWebSocketClosed(t, client1)
			validateClientWebSocketClosed(t, client3)
			validateClientWebSocketClosed(t, client4)
			waitFor(t) // wait for the close messages to be handled
			Verify(handler1, Once()).OnDisconnectionHandler(Exact("client-key-1"))
			Verify(handler2, Once()).OnDisconnectionHandler(Exact("client-key-3"))
			Verify(handler2, Once()).OnDisconnectionHandler(Exact("client-key-4"))
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
			When(handler.OnConnectionHandler(Any[http.ResponseWriter](), Any[*http.Request]())).ThenReturn("client-key", nil)
			client, err := createWebSocketClient(t, webSocketServer.address, "/path1", tt.withTLS)
			require.NoError(t, err)
			client.conn.Close()
			waitFor(t) // wait for handlers to be called

			Verify(handler, Once()).OnConnectionHandler(Any[http.ResponseWriter](), Any[*http.Request]())
			Verify(handler, Once()).OnConnectedHandler(Exact("client-key"), Any[ankh.ClientSession]())
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
			When(handler.OnConnectionHandler(Any[http.ResponseWriter](), Any[*http.Request]())).ThenReturn("", errors.New("connection denied"))
			_, err := createWebSocketClient(t, webSocketServer.address, "/path1", tt.withTLS)
			require.Error(t, err)
			waitFor(t) // wait for OnConnectionHandler to be called

			Verify(handler, Once()).OnConnectionHandler(Any[http.ResponseWriter](), Any[*http.Request]())
			VerifyNoMoreInteractions(handler)
		})

		t.Run(fmt.Sprintf("server connection handles WebSocket upgrade%s", tt.suffix), func(t *testing.T) {
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
				defer conn.Close()
			} else {
				var err error
				conn, err = net.Dial("tcp", webSocketServer.address)
				require.NoError(t, err)
				defer conn.Close()
			}

			// Perform a WebSocket handshake manually which uses a missing key and version
			fmt.Fprint(conn, "GET /path1 HTTP/1.1\r\n")
			fmt.Fprintf(conn, "Host: %s\r\n", webSocketServer.address)
			fmt.Fprintf(conn, "Upgrade: websocket\r\n")
			fmt.Fprintf(conn, "Connection: Upgrade\r\n")
			fmt.Fprintf(conn, "\r\n")
			waitFor(t) // wait for the upgrade to be handled

			Verify(handler, Once()).OnConnectionHandler(Any[http.ResponseWriter](), Any[*http.Request]())
			Verify(handler, Once()).OnWebSocketUpgraderErrorHandler(Any[any](), Any[error]())
			VerifyNoMoreInteractions(handler)
		})
	}
}
