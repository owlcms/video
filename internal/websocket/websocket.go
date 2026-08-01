package websocket

import (
	"net/http"

	"github.com/gorilla/websocket"
	"github.com/owlcms/video/internal/logging"
)

var (
	Clients  = make(map[*websocket.Conn]bool)
	Upgrader = websocket.Upgrader{
		ReadBufferSize:  1024,
		WriteBufferSize: 1024,
		CheckOrigin: func(r *http.Request) bool {
			return true
		},
	}
)

// SendMessage sends a message to all connected WebSocket clients
func SendMessage(message string) {
	for client := range Clients {
		if err := client.WriteMessage(websocket.TextMessage, []byte(message)); err != nil {
			logging.ErrorLogger.Printf("Error sending message: %v", err)
			client.Close()
			delete(Clients, client)
		}
	}
}
