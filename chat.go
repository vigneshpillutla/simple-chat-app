package main

import (
	"encoding/json"
	"log"
	"net/http"
	"os"
	"sync"

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

	cs.roomMutex.Unlock()
}

func (cs *chatServer) deleteRoom(roomId string) {
	cs.roomMutex.Lock()
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

// type chatServer struct {
// 	// subscriberMessageBuffer int
// 	serveMux      http.ServeMux
// 	subscribersMu sync.Mutex
// 	subscribers   map[*subscriber]struct{}
// }

// type subscriber struct {
// 	msgs chan []byte
// 	// closeSlow func()
// }

// func newChatServer() *chatServer {
// 	cs := &chatServer{
// 		// subscriberMessageBuffer: 16,
// 		subscribers: make(map[*subscriber]struct{}),
// 	}

// 	cs.serveMux.HandleFunc("/subscribe", cs.subscribeHandler)
// 	cs.serveMux.HandleFunc("/publish", cs.publishHandler)

// 	return cs
// }

// func (cs *chatServer) addSubscriber(s *subscriber) {
// 	cs.subscribersMu.Lock()
// 	cs.subscribers[s] = struct{}{}
// 	cs.subscribersMu.Unlock()
// }

// func (cs *chatServer) deleteSubscriber(s *subscriber) {
// 	cs.subscribersMu.Lock()
// 	delete(cs.subscribers, s)
// 	cs.subscribersMu.Unlock()
// }

// func (cs *chatServer) subscribe(ctx context.Context, w http.ResponseWriter, r *http.Request) error {
// 	var mu sync.Mutex
// 	s := &subscriber{
// 		msgs: make(chan []byte),
// 	}

// 	cs.addSubscriber(s)
// 	defer cs.deleteSubscriber(s)

// 	log.Println("Upgrading to WS")
// 	c, err := websocket.Accept(w, r, &websocket.AcceptOptions{
// 		InsecureSkipVerify: true,
// 	})

// 	if err != nil {
// 		log.Fatal(err)
// 		return err
// 	}
// 	log.Println("Upgraded to WS")
// 	mu.Lock()
// 	ctx = c.CloseRead(ctx)

// 	// go func() {
// 	// 	defer func() {
// 	// 		log.Println("Closing WebSocket reader goroutine")
// 	// 		c.Close(websocket.StatusNormalClosure, "Connection closed")
// 	// 	}()

// 	// 	for {
// 	// 		// Read message from the WebSocket
// 	// 		msgType, msg, err := c.Read(ctx)
// 	// 		if err != nil {
// 	// 			// Handle normal WebSocket closure or other non-fatal errors
// 	// 			if websocket.CloseStatus(err) == websocket.StatusNormalClosure ||
// 	// 				websocket.CloseStatus(err) == websocket.StatusGoingAway ||
// 	// 				err == io.EOF {
// 	// 				log.Println("WebSocket closed gracefully.")
// 	// 				return
// 	// 			}
// 	// 			log.Println("WebSocket read error:", err)
// 	// 			return
// 	// 		}

// 	// 		// Log and process the received message
// 	// 		if msgType == websocket.MessageText {
// 	// 			cs.publish(msg, s)
// 	// 			log.Printf("Received message from client: %s\n", msg)
// 	// 		}
// 	// 	}
// 	// }()

// 	for {
// 		msg := <-s.msgs
// 		err := c.Write(ctx, websocket.MessageText, msg)
// 		if err != nil {
// 			return err
// 		}
// 	}
// }

// func (cs *chatServer) publish(msg []byte, publishedBy *subscriber) {
// 	cs.subscribersMu.Lock()
// 	defer cs.subscribersMu.Unlock()

// 	// cs.publishLimiter.Wait(context.Background())

// 	for s := range cs.subscribers {
// 		if s != publishedBy {
// 			s.msgs <- msg
// 		}
// 		// select {
// 		// case s.msgs <- msg:
// 		// default:
// 		// 	go s.closeSlow()
// 		// }
// 	}
// }

// func (cs *chatServer) subscribeHandler(w http.ResponseWriter, r *http.Request) {
// 	fmt.Print("hit subscribe handler")
// 	err := cs.subscribe(r.Context(), w, r)
// 	if errors.Is(err, context.Canceled) {
// 		return
// 	}
// 	if websocket.CloseStatus(err) == websocket.StatusNormalClosure ||
// 		websocket.CloseStatus(err) == websocket.StatusGoingAway {
// 		return
// 	}
// 	if err != nil {
// 		fmt.Printf("%v", err)
// 		return
// 	}
// }

// func (cs *chatServer) publishHandler(w http.ResponseWriter, r *http.Request) {
// 	if r.Method != "POST" {
// 		http.Error(w, http.StatusText(http.StatusMethodNotAllowed), http.StatusMethodNotAllowed)
// 		return
// 	}

// 	type message struct {
// 		Msg    string `json:"msg"`
// 		UserID string `json:"userID"`
// 	}

// 	var data message

// 	decoder := json.NewDecoder((r.Body))

// 	err := decoder.Decode(&data)

// 	if err != nil {
// 		log.Printf("Error decoding the body")
// 		http.Error(w, "Invalid body", http.StatusBadRequest)
// 	}

// 	log.Printf("Data received: %#v", data)

// 	// body := http.MaxBytesReader(w, r.Body, 8192)
// 	// msg, err := io.ReadAll(body)
// 	// if err != nil {
// 	// 	http.Error(w, http.StatusText(http.StatusRequestEntityTooLarge), http.StatusRequestEntityTooLarge)
// 	// 	return
// 	// }

// 	// cs.publish(msg, nil)

// 	// w.WriteHeader(http.StatusAccepted)
// }