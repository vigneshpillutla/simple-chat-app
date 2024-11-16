package main

import (
	"encoding/json"
	"log"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/google/uuid"
)

type ServerLogger struct {
	infoLog  *log.Logger
	errorLog *log.Logger
}

type chatRoom struct {
	id              string
	name            string
	subscriberMutex sync.Mutex
	subscribers     map[string]*subscriber
	ServerLogger
}

type subscriber struct {
	id          string
	displayName string
	msgs        chan MessageData
}

type chatServer struct {
	serveMux  http.ServeMux
	roomMutex sync.Mutex
	rooms     map[string]*chatRoom
	ServerLogger
}

type MessagePayload struct {
	SenderId          string `json:"senderId"`
	SenderDisplayName string `json:"senderDisplayName"`
	Text              string `json:"text"`
}
type MessageData struct {
	Type    string         `json:"type"`
	Payload MessagePayload `json:"payload"`
}

func (cs *chatServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "POST, GET, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type")

	// If the request method is OPTIONS, return a 200 response and stop further processing.
	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusOK)
		return
	}
	cs.serveMux.ServeHTTP(w, r)
}

func newChatServer() *chatServer {
	serverLogger := ServerLogger{
		infoLog:  log.New(os.Stdout, "INFO\t", log.LstdFlags),
		errorLog: log.New(os.Stderr, "Error\t", log.LstdFlags|log.Llongfile),
	}
	cs := &chatServer{
		rooms:        make(map[string]*chatRoom),
		ServerLogger: serverLogger,
	}

	cs.infoLog.Println("Initializing a new chat server")

	cs.serveMux = *http.NewServeMux()
	cs.serveMux.HandleFunc("/createRoom", cs.createRoomHandler)
	cs.serveMux.HandleFunc("/roomHealthCheck", cs.roomHealthCheckHandler)
	cs.serveMux.HandleFunc("/subscribe", cs.subscribeHandler)
	return cs
}

func (cs *chatServer) createRoom(roomId string, name string) {
	cs.roomMutex.Lock()
	cs.infoLog.Printf("Creating a new room\nID:%v\nName:%v\n", roomId, name)
	room := &chatRoom{
		id:           roomId,
		name:         name,
		subscribers:  make(map[string]*subscriber),
		ServerLogger: cs.ServerLogger,
	}
	cs.rooms[roomId] = room

	go func() {
		for {
			time.Sleep(30 * time.Second)
			cs.roomMutex.Lock()
			if room, ok := cs.rooms[roomId]; ok && len(room.subscribers) == 0 {
				cs.roomMutex.Unlock()
				cs.deleteRoom(roomId)
				return
			}
			cs.roomMutex.Unlock()
		}
	}()

	cs.roomMutex.Unlock()
}

func (cs *chatServer) deleteRoom(roomId string) {
	cs.roomMutex.Lock()
	cs.infoLog.Printf("Deleting room: %v",roomId)
	delete(cs.rooms, roomId)
	cs.roomMutex.Unlock()
}

func (room *chatRoom) addSubscriber(s *subscriber) {
	room.subscriberMutex.Lock()
	room.subscribers[s.id] = s
	room.subscriberMutex.Unlock()
}

func (room *chatRoom) deleteSubscriber(subscriberId string) {
	room.subscriberMutex.Lock()
	delete(room.subscribers, subscriberId)
	room.subscriberMutex.Unlock()
}

func (cs *chatServer) createRoomHandler(w http.ResponseWriter, r *http.Request) {
	cs.infoLog.Println("CreateRoomHandler")
	if r.Method != http.MethodPost {
		cs.clientError(w, http.StatusMethodNotAllowed)
		return
	}
	var data struct {
		RoomName string `json:"roomName"`
	}
	decoder := json.NewDecoder(r.Body)
	err := decoder.Decode(&data)

	if err != nil {
		cs.clientError(w, http.StatusBadRequest)
		return
	}

	roomId := uuid.NewString()

	cs.createRoom(roomId, data.RoomName)
	responseData := struct {
		Status   bool   `json:"status"`
		RoomId   string `json:"roomId"`
		RoomName string `json:"roomName"`
	}{
		Status:   true,
		RoomId:   roomId,
		RoomName: data.RoomName,
	}
	cs.respondWithJSON(w, responseData, http.StatusCreated)
}

func (cs *chatServer) roomHealthCheckHandler(w http.ResponseWriter, r *http.Request) {
	cs.infoLog.Println("Room health check handler")
	if r.Method != http.MethodGet {
		cs.clientError(w, http.StatusMethodNotAllowed)
		return
	}

	roomId := r.URL.Query().Get("roomId")

	if _, ok := cs.rooms[roomId]; ok {
		w.Write([]byte("Room exists"))
		return
	}

	cs.clientError(w, http.StatusNotFound)
}

func shouldSendMessage(data []byte) bool {
	var js json.RawMessage
	return json.Unmarshal(data, &js) != nil
}

func (cs *chatServer) subscribeHandler(w http.ResponseWriter, r *http.Request) {
	cs.infoLog.Println("Subscribe Handler")

	roomId := r.URL.Query().Get("roomId")
	room, ok := cs.rooms[roomId]

	if !ok {
		cs.clientError(w, http.StatusNotFound)
		return
	}

	subscriberName := r.URL.Query().Get("displayName")

	if subscriberName == "" {
		cs.clientError(w, http.StatusBadRequest)
		return
	}
	s := &subscriber{
		id:          uuid.NewString(),
		displayName: subscriberName,
		msgs:        make(chan MessageData),
	}

	cs.infoLog.Printf("Upgrading websocket connection for %v", subscriberName)
	ws, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		InsecureSkipVerify: true,
	})

	if err != nil {
		cs.errorLog.Fatal(err)
		cs.serverError(w, err)
		return
	}

	defer ws.Close(websocket.StatusNormalClosure, "Closing the websocket connection")
	room.addSubscriber(s)
	defer func() {
		room.deleteSubscriber(s.id)
		close(s.msgs)
	}()

	go func() {
		for {
			_, data, err := ws.Read(r.Context())
			cs.infoLog.Printf("Received text from %v", s.displayName)
			if err != nil {
				cs.errorLog.Printf("Failed to read from the websocket %v", err)
				s.msgs <- MessageData{Type: "terminate"}
				return
			}

			if shouldSendMessage(data) {
				messagePayload := MessagePayload{
					SenderId:          s.id,
					SenderDisplayName: s.displayName,
					Text:              string(data),
				}
				for _, member := range room.subscribers {
					if s.id != member.id {
						dataToSend := MessageData{
							Type:    "text",
							Payload: messagePayload,
						}
						cs.infoLog.Printf("Sending msg to %v", member.displayName)
						cs.infoLog.Printf("Msg %v", dataToSend)
						member.msgs <- dataToSend
					}
				}
				cs.infoLog.Println("Sending to self")
				s.msgs <- MessageData{
					Type:    "self",
					Payload: messagePayload,
				}
			}

		}
	}()

	for {
		select {

		case msgData := <-s.msgs:
			switch msgData.Type {
			case "terminate":
				cs.infoLog.Printf("Channel has been terminated for %v", s.displayName)
				return
			case "text", "self":
				payload, err := json.Marshal(msgData)

				if err != nil {
					cs.errorLog.Printf(err.Error())
					continue
				}
				cs.infoLog.Printf("Sending msg to client")
				err = ws.Write(r.Context(), websocket.MessageText, payload)

				if err != nil {
					closeStatus := websocket.CloseStatus(err)

					if closeStatus == websocket.StatusGoingAway || closeStatus == websocket.StatusNormalClosure {
						cs.infoLog.Println("Connection closed by the client")
						return
					}
					cs.errorLog.Printf("Unable to write to the socket %v", err)
					return
				}
			default:
				cs.errorLog.Printf("Unable to process the message %v", msgData)
			}
		case <-r.Context().Done():
			cs.infoLog.Println("Request context cancelled")
			return
		}
	}

}
