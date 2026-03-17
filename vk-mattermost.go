package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/SevereCloud/vksdk/v3/api"
	"github.com/SevereCloud/vksdk/v3/api/params"
	"github.com/SevereCloud/vksdk/v3/events"
	longpoll "github.com/SevereCloud/vksdk/v3/longpoll-bot"
	"github.com/gorilla/websocket"
)

type Config struct {
	VKToken      string `json:"vk_token"`
	AllowedUsers []int  `json:"allowed_users"`
	Listen       string `json:"listen"`
}

func loadConfig() (*Config, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	path := filepath.Join(home, ".config", "vk-mattermost", "config.json")
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("config not found at %s: %w", path, err)
	}
	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("config parse error: %w", err)
	}
	if cfg.VKToken == "" {
		return nil, fmt.Errorf("vk_token is required")
	}
	if len(cfg.AllowedUsers) == 0 {
		return nil, fmt.Errorf("allowed_users is required (at least one)")
	}
	if cfg.Listen == "" {
		cfg.Listen = ":8065"
	}
	return &cfg, nil
}

var (
	cfg        *Config
	vkAPI      *api.VK
	allowedSet map[int]struct{}
)

const (
	botUserID = "vk-group-bot"
	teamID    = "vk-team"
	vkUserPfx = "vk-user-"
	vkChanPfx = "dm-chan-"
)

func mmUserID(vkID int) string { return fmt.Sprintf("%s%d", vkUserPfx, vkID) }
func mmChanID(vkID int) string { return fmt.Sprintf("%s%d", vkChanPfx, vkID) }

type Hub struct {
	mu      sync.Mutex
	clients map[*wsClient]struct{}
}

type wsClient struct {
	conn *websocket.Conn
	send chan []byte
}

var hub = &Hub{clients: make(map[*wsClient]struct{})}

func (h *Hub) register(c *wsClient) {
	h.mu.Lock()
	h.clients[c] = struct{}{}
	h.mu.Unlock()
}

func (h *Hub) unregister(c *wsClient) {
	h.mu.Lock()
	delete(h.clients, c)
	h.mu.Unlock()
}

func (h *Hub) broadcast(msg []byte) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for c := range h.clients {
		select {
		case c.send <- msg:
		default:
		}
	}
}

func sendVKMessage(peerID int, text string) error {
	b := params.NewMessagesSendBuilder()
	b.PeerID(peerID)
	b.Message(text)
	b.RandomID(int(time.Now().UnixNano() % 1e9))
	_, err := vkAPI.MessagesSend(b.Params)
	return err
}

var (
	seqMu sync.Mutex
	seqN  int64
)

func nextSeq() int64 {
	seqMu.Lock()
	defer seqMu.Unlock()
	seqN++
	return seqN
}

type mmPost struct {
	ID        string `json:"id"`
	CreateAt  int64  `json:"create_at"`
	UpdateAt  int64  `json:"update_at"`
	UserID    string `json:"user_id"`
	ChannelID string `json:"channel_id"`
	Message   string `json:"message"`
	Type      string `json:"type"`
}

func pushPostedEvent(fromVKUserID int, text string) {
	now := time.Now().UnixMilli()
	post := mmPost{
		ID:        fmt.Sprintf("vkpost-%d", now),
		CreateAt:  now,
		UpdateAt:  now,
		UserID:    mmUserID(fromVKUserID),
		ChannelID: mmChanID(fromVKUserID),
		Message:   text,
	}
	postJSON, _ := json.Marshal(post)
	event := map[string]interface{}{
		"event": "posted",
		"data": map[string]interface{}{
			"channel_display_name": fmt.Sprintf("VK DM %d", fromVKUserID),
			"channel_name":         fmt.Sprintf("vk-dm-%d", fromVKUserID),
			"channel_type":         "D",
			"post":                 string(postJSON),
			"sender_name":          fmt.Sprintf("vk_%d", fromVKUserID),
			"team_id":              teamID,
		},
		"broadcast": map[string]interface{}{
			"channel_id": mmChanID(fromVKUserID),
			"user_id":    "",
			"team_id":    teamID,
		},
		"seq": nextSeq(),
	}
	msg, _ := json.Marshal(event)
	hub.broadcast(msg)
}

func jsonResp(w http.ResponseWriter, code int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(v)
}

func handleUsersMe(w http.ResponseWriter, r *http.Request) {
	jsonResp(w, 200, map[string]interface{}{
		"id":         botUserID,
		"username":   "vk_bot",
		"first_name": "VK",
		"last_name":  "Bot",
		"nickname":   "vk_bot",
		"email":      "bot@vk.local",
		"roles":      "system_user",
	})
}

func handleGetUser(w http.ResponseWriter, r *http.Request) {
	userID := r.PathValue("user_id")
	if userID == botUserID || userID == "me" {
		handleUsersMe(w, r)
		return
	}
	jsonResp(w, 200, map[string]interface{}{
		"id":         userID,
		"username":   userID,
		"first_name": "VK",
		"last_name":  "User",
		"nickname":   userID,
		"email":      userID + "@vk.local",
		"roles":      "system_user",
	})
}

func handleUserTeams(w http.ResponseWriter, r *http.Request) {
	jsonResp(w, 200, []map[string]interface{}{
		{"id": teamID, "name": "vk-team", "display_name": "VK Team"},
	})
}

func handleDirectChannel(w http.ResponseWriter, r *http.Request) {
	var userIDs []string
	if err := json.NewDecoder(r.Body).Decode(&userIDs); err != nil {
		jsonResp(w, 400, map[string]string{"message": "bad request"})
		return
	}
	chanID := "dm-unknown"
	userID := "vk-unknown"
	for _, id := range userIDs {
		if id != botUserID && strings.HasPrefix(id, vkUserPfx) {
			userID = id
			chanID = vkChanPfx + id[len(vkUserPfx):]
			break
		}
	}
	jsonResp(w, 200, map[string]interface{}{
		"id":           chanID,
		"name":         botUserID + "__" + userID,
		"display_name": "VK DM",
		"type":         "D",
		"team_id":      "",
	})
}

func handleGetChannel(w http.ResponseWriter, r *http.Request) {
	chanID := r.PathValue("channel_id")
	jsonResp(w, 200, map[string]interface{}{
		"id":           chanID,
		"name":         chanID,
		"display_name": "VK DM",
		"type":         "D",
		"team_id":      "",
	})
}

func handleCreatePost(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ChannelID string `json:"channel_id"`
		Message   string `json:"message"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		jsonResp(w, 400, map[string]string{"message": "bad request"})
		return
	}
	var peerID int
	fmt.Sscanf(body.ChannelID, vkChanPfx+"%d", &peerID)
	if peerID == 0 {
		jsonResp(w, 400, map[string]string{"message": "cannot resolve peer_id from channel_id"})
		return
	}
	log.Printf("-> VK [%d]: %q", peerID, body.Message)
	if err := sendVKMessage(peerID, body.Message); err != nil {
		log.Printf("[ERR] VK send: %v", err)
		jsonResp(w, 500, map[string]string{"message": err.Error()})
		return
	}
	now := time.Now().UnixMilli()
	jsonResp(w, 201, mmPost{
		ID:        fmt.Sprintf("out-%d", now),
		CreateAt:  now,
		UpdateAt:  now,
		UserID:    botUserID,
		ChannelID: body.ChannelID,
		Message:   body.Message,
	})
}

func handleTyping(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(200)
}

var upgrader = websocket.Upgrader{CheckOrigin: func(r *http.Request) bool { return true }}

func handleWebSocket(w http.ResponseWriter, r *http.Request) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("[ERR] WS upgrade: %v", err)
		return
	}
	client := &wsClient{conn: conn, send: make(chan []byte, 64)}
	hub.register(client)
	log.Println("[OK] OpenClaw WS connected")

	go func() {
		defer conn.Close()
		for msg := range client.send {
			if err := conn.WriteMessage(websocket.TextMessage, msg); err != nil {
				break
			}
		}
	}()

	defer func() {
		hub.unregister(client)
		close(client.send)
		log.Println("[--] OpenClaw WS disconnected")
	}()

	for {
		_, data, err := conn.ReadMessage()
		if err != nil {
			break
		}
		var msg map[string]interface{}
		if json.Unmarshal(data, &msg) != nil {
			continue
		}
		if msg["action"] == "authentication_challenge" {
			ack, _ := json.Marshal(map[string]interface{}{
				"status":    "OK",
				"seq_reply": msg["seq"],
			})
			client.send <- ack
			log.Println("[OK] WS auth ok")
		}
	}
}

func makeRouter() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v4/users/me", handleUsersMe)
	mux.HandleFunc("GET /api/v4/users/{user_id}", handleGetUser)
	mux.HandleFunc("GET /api/v4/users/{user_id}/teams", handleUserTeams)
	mux.HandleFunc("POST /api/v4/channels/direct", handleDirectChannel)
	mux.HandleFunc("GET /api/v4/channels/{channel_id}", handleGetChannel)
	mux.HandleFunc("POST /api/v4/posts", handleCreatePost)
	mux.HandleFunc("POST /api/v4/users/me/typing", handleTyping)
	mux.HandleFunc("GET /api/v4/websocket", handleWebSocket)
	return mux
}

func main() {
	var err error
	cfg, err = loadConfig()
	if err != nil {
		log.Fatalf("config: %v", err)
	}

	allowedSet = make(map[int]struct{}, len(cfg.AllowedUsers))
	for _, id := range cfg.AllowedUsers {
		allowedSet[id] = struct{}{}
	}

	vkAPI = api.NewVK(cfg.VKToken)

	lp, err := longpoll.NewLongPollCommunity(vkAPI)
	if err != nil {
		log.Fatalf("longpoll init: %v", err)
	}

	groups, _ := vkAPI.GroupsGetByID(nil)
	if len(groups.Groups) > 0 {
		g := groups.Groups[0]
		log.Printf("[OK] VK group: [%d] %s", g.ID, g.Name)
	}

	lp.MessageNew(func(ctx context.Context, obj events.MessageNewObject) {
		msg := obj.Message
		if _, ok := allowedSet[msg.FromID]; !ok {
			log.Printf("[--] ignored from=%d", msg.FromID)
			return
		}
		log.Printf("<- VK [%d]: %q", msg.FromID, msg.Text)
		pushPostedEvent(msg.FromID, msg.Text)
	})

	go func() {
		log.Println("[OK] VK LongPoll started")
		if err := lp.Run(); err != nil {
			log.Fatalf("longpoll: %v", err)
		}
	}()

	log.Printf("[OK] listening on %s", cfg.Listen)
	if err := http.ListenAndServe(cfg.Listen, makeRouter()); err != nil {
		log.Fatal(err)
	}
}
