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
	"crypto/tls"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/mikefero/ankh"
	. "github.com/ovechkin-dm/mockio/mock"
	"github.com/stretchr/testify/require"
)

type webSocketClient struct {
	cancel      context.CancelFunc
	client      *ankh.WebSocketClient
	mockHandler ankh.WebSocketClientEventHandler
	session     *ankh.Session
}

func createWebSocketClient(t *testing.T, serverURL url.URL, enableTLS bool) *webSocketClient {
	t.Helper()

	var tlsConfig *tls.Config
	if enableTLS {
		tlsConfig = generateTestCertificate(t)
		tlsConfig.ServerName = "ankh.example.com"
	}

	handler := Mock[ankh.WebSocketClientEventHandler]()
	client, err := ankh.NewWebSocketClient(ankh.WebSocketClientOpts{
		ServerURL: serverURL,
		RequestHeaders: map[string][]string{
			"X-Custom-Header": {"custom-value"},
		},
		Handler:          handler,
		HandShakeTimeout: 5 * time.Second,
		TLSConfig:        tlsConfig,
	})
	if err != nil {
		t.Fatalf("failed to create WebSocket client: %v", err)
		return nil
	}

	return &webSocketClient{
		client:      client,
		mockHandler: handler,
	}
}

func runWebSocketClient(t *testing.T, client *webSocketClient, shouldFail bool) {
	t.Helper()

	ctx, cancel := context.WithCancel(context.Background())
	client.cancel = cancel
	go func() {
		err := client.client.Run(ctx)
		if shouldFail {
			require.Error(t, err)
		} else {
			require.NoError(t, err)
		}
	}()

	client.session = &ankh.Session{}
	if !shouldFail {
		t.Log("verify WebSocket client connects")
		captorSession := Captor[*ankh.Session]()
		WhenSingle(client.mockHandler.OnConnectedHandler(Any[*http.Response](), captorSession.Capture())).ThenReturn(nil)
		waitForCapture(t, captorSession)
		client.session = captorSession.Last()
		Verify(client.mockHandler, Once()).OnConnectedHandler(Any[*http.Response](), Any[*ankh.Session]())
	}
}

func TestWebSocketClient(t *testing.T) {
	t.Run("error when creating WebSocket client with empty handler", func(t *testing.T) {
		t.Parallel()

		_, err := ankh.NewWebSocketClient(ankh.WebSocketClientOpts{
			ServerURL: url.URL{},
		})
		require.Error(t, err)
		require.ErrorContains(t, err, "handler must not be empty")
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

		t.Run(fmt.Sprintf("can start a WebSocket client and handle events%s", tt.suffix), func(t *testing.T) {
			t.Parallel()
			SetUp(t)

			t.Log("creating WebSocket server")
			server := createWebSocketServer(t, tt.withTLS)
			serverHandler := server.mockHandlers[0]
			captorServerSession := Captor[*ankh.Session]()
			captorServerRequest := Captor[*http.Request]()
			When(serverHandler.OnConnectionHandler(Any[http.ResponseWriter](), captorServerRequest.Capture())).ThenReturn("client-key", nil)
			WhenSingle(serverHandler.OnConnectedHandler(Exact("client-key"), captorServerSession.Capture())).ThenReturn(nil)
			defer server.cancel()

			t.Log("creating WebSocket client")
			serverURL := server.serverURL
			serverURL.Path = "/path1"
			client := createWebSocketClient(t, serverURL, tt.withTLS)
			runWebSocketClient(t, client, false)
			handler := client.mockHandler
			defer client.cancel()

			t.Log("verify client connection request headers are sent to the server")
			waitForCapture(t, captorServerRequest)
			require.Equal(t, "custom-value", captorServerRequest.Last().Header.Get("X-Custom-Header"))

			t.Log("obtain server session for further client testing")
			waitForCapture(t, captorServerSession)
			serverSession := captorServerSession.Last()
			if tt.withTLS {
				require.NotNil(t, serverSession.ConnectionState)
				require.Equal(t, (uint16(tls.VersionTLS13)), serverSession.ConnectionState.Version)
			}
			Verify(serverHandler, Once()).OnConnectionHandler(Any[http.ResponseWriter](), Any[*http.Request]())
			Verify(serverHandler, Once()).OnConnectedHandler(Exact("client-key"), Any[*ankh.Session]())

			t.Log("verify ping message is received from the server and client receives pong message")
			captorPing := Captor[string]()
			WhenSingle(serverHandler.OnPingHandler(Exact("client-key"), captorPing.Capture())).ThenReturn([]byte("ankh-server"))
			client.session.Ping([]byte("ping"))
			waitForCapture(t, captorPing)
			require.Equal(t, "ping", captorPing.Last())
			Verify(serverHandler, Once()).OnPingHandler(Exact("client-key"), Exact("ping"))
			Verify(handler, Once()).OnPongHandler(Exact("ankh-server"))
			VerifyNoMoreInteractions(handler)

			t.Log("verify message from the client to the server is received")
			captorServerReadMessage := Captor[[]byte]()
			client.session.Send([]byte("ankh-client"))
			waitFor(t) // wait for the read messages to be handled
			Verify(serverHandler, Once()).OnReadMessageHandler(Exact("client-key"), Exact(websocket.BinaryMessage), captorServerReadMessage.Capture())
			waitForCapture(t, captorServerReadMessage)
			require.Equal(t, []byte("ankh-client"), captorServerReadMessage.Last())

			t.Log("verify message from the server to the client is received")
			captorClientReadMessage := Captor[[]byte]()
			serverSession.Send([]byte("ankh-server"))
			waitFor(t) // wait for the read messages to be handled
			Verify(handler, Once()).OnReadMessageHandler(Exact(websocket.BinaryMessage), captorClientReadMessage.Capture())
			waitForCapture(t, captorClientReadMessage)
			require.Equal(t, []byte("ankh-server"), captorClientReadMessage.Last())
			VerifyNoMoreInteractions(handler)

			t.Log("verify closing the connection of the WebSocket client will close the connection with the server")
			client.session.Close()
			waitFor(t) // wait for the close messages to be handled
			Verify(serverHandler, Once()).OnDisconnectionHandler(Exact("client-key"))
			require.False(t, client.client.IsConnected())
			Verify(handler, Once()).OnDisconnectionHandler()
			VerifyNoMoreInteractions(handler)
		})

		t.Run(fmt.Sprintf("verify connection error occurs client returns error on connected event%s", tt.suffix), func(t *testing.T) {
			t.Parallel()
			SetUp(t)

			server := createWebSocketServer(t, tt.withTLS)
			serverHandler := server.mockHandlers[0]
			When(serverHandler.OnConnectionHandler(Any[http.ResponseWriter](), Any[*http.Request]())).ThenReturn("client-key", nil)
			serverURL := server.serverURL
			serverURL.Path = "/path1"
			client := createWebSocketClient(t, serverURL, tt.withTLS)
			handler := client.mockHandler
			WhenSingle(handler.OnConnectedHandler(Any[*http.Response](), Any[*ankh.Session]())).ThenReturn(errors.New("connection error"))
			runWebSocketClient(t, client, true)
			defer client.cancel()

			waitFor(t) // wait connected handler to be called
			Verify(handler, Once()).OnConnectedHandler(Any[*http.Response](), Any[*ankh.Session]())
			Verify(handler, Once()).OnDisconnectionHandler()
			VerifyNoMoreInteractions(handler)
		})

		t.Run(fmt.Sprintf("verify closing server connection terminates client%s", tt.suffix), func(t *testing.T) {
			t.Parallel()
			SetUp(t)

			server := createWebSocketServer(t, tt.withTLS)
			serverHandler := server.mockHandlers[0]
			captorServerSession := Captor[*ankh.Session]()
			When(serverHandler.OnConnectionHandler(Any[http.ResponseWriter](), Any[*http.Request]())).ThenReturn("client-key", nil)
			When(serverHandler.OnConnectedHandler(Exact("client-key"), captorServerSession.Capture())).ThenReturn(nil)
			serverURL := server.serverURL
			serverURL.Path = "/path1"
			client := createWebSocketClient(t, serverURL, tt.withTLS)
			handler := client.mockHandler
			WhenSingle(handler.OnConnectedHandler(Any[*http.Response](), Any[*ankh.Session]())).ThenReturn(errors.New("connection error"))
			runWebSocketClient(t, client, true)
			defer client.cancel()

			waitForCapture(t, captorServerSession)
			serverSession := captorServerSession.Last()
			serverSession.Close()
			waitFor(t) // wait connected handler to be called
			Verify(handler, Once()).OnConnectedHandler(Any[*http.Response](), Any[*ankh.Session]())
			Verify(handler, Once()).OnDisconnectionHandler()
			VerifyNoMoreInteractions(handler)
		})
	}
}
