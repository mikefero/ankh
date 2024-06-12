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

// CloseConnection will terminate the connection with the client/server
// application. This handle will be given during the OnConnectedHandler callback
// and is guaranteed to be thread safe.
type CloseConnection func()

// PingMessage will send a ping message to the client/server application. This handle
// will be given during the OnConnectedHandler callback and is guaranteed to be
// thread safe.
type PingMessage func(data []byte) error

// SendMessage will send a binary message to the client/server application. This
// handle will be given during the OnConnectedHandler callback and is guaranteed
// to be thread safe.
type SendMessage func(data []byte) error

// Session represents the client/server session for the WebSocket.
type Session struct {
	// Close will close the connection with the client/server application.
	Close CloseConnection
	// Ping will send a ping message to the client/server application.
	Ping PingMessage
	// Send will send a binary message to the client/server application.
	Send SendMessage
}
