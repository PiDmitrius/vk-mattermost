package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/SevereCloud/vksdk/v3/api"
	"github.com/SevereCloud/vksdk/v3/api/params"
	"github.com/SevereCloud/vksdk/v3/events"
	longpoll "github.com/SevereCloud/vksdk/v3/longpoll-bot"
	"github.com/gorilla/websocket"
)

// --- config ---

type Config struct {
	VKToken         string  `json:"vk_token"`
	AllowedUsers    []int   `json:"allowed_users"`
	MaxToken        string  `json:"max_token"`
	MaxAllowedUsers []int64 `json:"max_allowed_users"`
	Listen          string  `json:"listen"`
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
	if cfg.VKToken == "" && cfg.MaxToken == "" {
		return nil, fmt.Errorf("at least one of vk_token or max_token is required")
	}
	if cfg.Listen == "" {
		cfg.Listen = ":8065"
	}
	return &cfg, nil
}

// --- MAX Bot API ---

const maxAPIBase = "https://platform-api.max.ru"

var maxHTTP = &http.Client{Timeout: 90 * time.Second}

type maxUser struct {
	UserID   int64  `json:"user_id"`
	Name     string `json:"name"`
	Username string `json:"username"`
}

type maxMessageBody struct {
	Mid  string `json:"mid"`
	Seq  int64  `json:"seq"`
	Text string `json:"text"`
}

type maxRecipient struct {
	ChatID   int64  `json:"chat_id,omitempty"`
	ChatType string `json:"chat_type,omitempty"`
	UserID   int64  `json:"user_id,omitempty"`
}

type maxMessage struct {
	Sender    maxUser        `json:"sender"`
	Recipient maxRecipient   `json:"recipient"`
	Timestamp int64          `json:"timestamp"`
	Body      maxMessageBody `json:"body"`
}

type maxUpdate struct {
	UpdateType string     `json:"update_type"`
	Timestamp  int64      `json:"timestamp"`
	Message    maxMessage `json:"message"`
}

type maxUpdatesResp struct {
	Updates []maxUpdate `json:"updates"`
	Marker  *int64      `json:"marker"`
}

func maxRequest(token, method, path string, body io.Reader) (*http.Response, error) {
	req, err := http.NewRequest(method, maxAPIBase+path, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	return maxHTTP.Do(req)
}

func maxGetMe(token string) (*maxUser, error) {
	resp, err := maxRequest(token, "GET", "/me", nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		b, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("GET /me: %d %s", resp.StatusCode, string(b))
	}
	var u maxUser
	if err := json.NewDecoder(resp.Body).Decode(&u); err != nil {
		return nil, err
	}
	return &u, nil
}

func maxPollUpdates(token string, marker *int64) (*maxUpdatesResp, error) {
	path := "/updates?timeout=30&types=message_created"
	if marker != nil {
		path += "&marker=" + strconv.FormatInt(*marker, 10)
	}
	resp, err := maxRequest(token, "GET", path, nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		b, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("GET /updates: %d %s", resp.StatusCode, string(b))
	}
	var r maxUpdatesResp
	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
		return nil, err
	}
	return &r, nil
}

func maxSendMessage(token string, userID, chatID int64, text string) error {
	body := fmt.Sprintf(`{"text":%s}`, mustJSON(text))
	var query string
	if chatID != 0 {
		query = fmt.Sprintf("/messages?chat_id=%d", chatID)
	} else {
		query = fmt.Sprintf("/messages?user_id=%d", userID)
	}
	resp, err := maxRequest(token, "POST", query, strings.NewReader(body))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("POST /messages: %d %s", resp.StatusCode, string(b))
	}
	return nil
}

func mustJSON(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

// --- constants ---

const (
	botUserID  = "bridge-bot"
	teamID     = "bridge-team"
	vkUserPfx  = "vk-user-"
	vkChanPfx  = "vk-dm-"
	maxUserPfx = "max-user-"
	maxDMPfx   = "max-dm-"
	maxChatPfx = "max-chat-"
)

func vkMMUserID(vkID int) string        { return fmt.Sprintf("%s%d", vkUserPfx, vkID) }
func vkMMChanID(vkID int) string        { return fmt.Sprintf("%s%d", vkChanPfx, vkID) }
func maxMMUserID(maxID int64) string    { return fmt.Sprintf("%s%d", maxUserPfx, maxID) }
func maxMMDMChan(maxID int64) string    { return fmt.Sprintf("%s%d", maxDMPfx, maxID) }
func maxMMChatChan(chatID int64) string { return fmt.Sprintf("%s%d", maxChatPfx, chatID) }

// --- WebSocket hub ---

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

// --- VK send ---

var vkAPI *api.VK

func sendVKMessage(peerID int, text string) error {
	b := params.NewMessagesSendBuilder()
	b.PeerID(peerID)
	b.Message(text)
	b.RandomID(int(time.Now().UnixNano() % 1e9))
	_, err := vkAPI.MessagesSend(b.Params)
	return err
}

// --- Mattermost events ---

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

func pushPostedEvent(userID, channelID, senderName string, text string) {
	now := time.Now().UnixMilli()
	post := mmPost{
		ID:        fmt.Sprintf("post-%d", now),
		CreateAt:  now,
		UpdateAt:  now,
		UserID:    userID,
		ChannelID: channelID,
		Message:   text,
	}
	postJSON, _ := json.Marshal(post)
	event := map[string]interface{}{
		"event": "posted",
		"data": map[string]interface{}{
			"channel_display_name": channelID,
			"channel_name":         channelID,
			"channel_type":         "D",
			"post":                 string(postJSON),
			"sender_name":          senderName,
			"team_id":              teamID,
		},
		"broadcast": map[string]interface{}{
			"channel_id": channelID,
			"user_id":    "",
			"team_id":    teamID,
		},
		"seq": nextSeq(),
	}
	msg, _ := json.Marshal(event)
	hub.broadcast(msg)
}

// --- HTTP handlers ---

func jsonResp(w http.ResponseWriter, code int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(v)
}

func handleUsersMe(w http.ResponseWriter, r *http.Request) {
	jsonResp(w, 200, map[string]interface{}{
		"id":         botUserID,
		"username":   "bridge_bot",
		"first_name": "Bridge",
		"last_name":  "Bot",
		"nickname":   "bridge_bot",
		"email":      "bot@bridge.local",
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
		"first_name": "Bridge",
		"last_name":  "User",
		"nickname":   userID,
		"email":      userID + "@bridge.local",
		"roles":      "system_user",
	})
}

func handleUserTeams(w http.ResponseWriter, r *http.Request) {
	jsonResp(w, 200, []map[string]interface{}{
		{"id": teamID, "name": "bridge-team", "display_name": "Bridge Team"},
	})
}

func handleDirectChannel(w http.ResponseWriter, r *http.Request) {
	var userIDs []string
	if err := json.NewDecoder(r.Body).Decode(&userIDs); err != nil {
		jsonResp(w, 400, map[string]string{"message": "bad request"})
		return
	}
	chanID := "dm-unknown"
	userID := "unknown"
	for _, id := range userIDs {
		if id == botUserID {
			continue
		}
		userID = id
		// derive channel from user: vk-user-123 -> vk-dm-123, max-user-456 -> max-dm-456
		if strings.HasPrefix(id, vkUserPfx) {
			chanID = vkChanPfx + id[len(vkUserPfx):]
		} else if strings.HasPrefix(id, maxUserPfx) {
			chanID = maxDMPfx + id[len(maxUserPfx):]
		} else if strings.HasPrefix(id, maxChatPfx) {
			chanID = id // max-chat-{chatID} is both user and channel
		} else {
			chanID = "dm-" + id
		}
		break
	}
	jsonResp(w, 200, map[string]interface{}{
		"id":           chanID,
		"name":         botUserID + "__" + userID,
		"display_name": "Bridge DM",
		"type":         "D",
		"team_id":      "",
	})
}

func handleGetChannel(w http.ResponseWriter, r *http.Request) {
	chanID := r.PathValue("channel_id")
	jsonResp(w, 200, map[string]interface{}{
		"id":           chanID,
		"name":         chanID,
		"display_name": "Bridge DM",
		"type":         "D",
		"team_id":      "",
	})
}

var globalCfg *Config

func handleCreatePost(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ChannelID string `json:"channel_id"`
		Message   string `json:"message"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		jsonResp(w, 400, map[string]string{"message": "bad request"})
		return
	}

	var sendErr error

	switch {
	case strings.HasPrefix(body.ChannelID, vkChanPfx):
		var peerID int
		fmt.Sscanf(body.ChannelID, vkChanPfx+"%d", &peerID)
		if peerID == 0 {
			jsonResp(w, 400, map[string]string{"message": "cannot resolve vk peer_id"})
			return
		}
		log.Printf("-> VK [%d]: %q", peerID, body.Message)
		sendErr = sendVKMessage(peerID, body.Message)

	case strings.HasPrefix(body.ChannelID, maxChatPfx):
		var chatID int64
		fmt.Sscanf(body.ChannelID, maxChatPfx+"%d", &chatID)
		if chatID == 0 {
			jsonResp(w, 400, map[string]string{"message": "cannot resolve max chat_id"})
			return
		}
		log.Printf("-> MAX chat=%d: %q", chatID, body.Message)
		sendErr = maxSendMessage(globalCfg.MaxToken, 0, chatID, body.Message)

	case strings.HasPrefix(body.ChannelID, maxDMPfx):
		var userID int64
		fmt.Sscanf(body.ChannelID, maxDMPfx+"%d", &userID)
		if userID == 0 {
			jsonResp(w, 400, map[string]string{"message": "cannot resolve max user_id"})
			return
		}
		log.Printf("-> MAX [%d]: %q", userID, body.Message)
		sendErr = maxSendMessage(globalCfg.MaxToken, userID, 0, body.Message)

	default:
		jsonResp(w, 400, map[string]string{"message": "unknown channel prefix: " + body.ChannelID})
		return
	}

	if sendErr != nil {
		log.Printf("[ERR] send: %v", sendErr)
		jsonResp(w, 500, map[string]string{"message": sendErr.Error()})
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

// --- MAX long-polling ---

func maxPollLoop(token string, allowedSet map[int64]struct{}) {
	var marker *int64
	for {
		resp, err := maxPollUpdates(token, marker)
		if err != nil {
			log.Printf("[ERR] MAX poll: %v", err)
			time.Sleep(3 * time.Second)
			continue
		}
		marker = resp.Marker
		for _, upd := range resp.Updates {
			if upd.UpdateType != "message_created" {
				continue
			}
			senderID := upd.Message.Sender.UserID
			text := upd.Message.Body.Text
			chatID := upd.Message.Recipient.ChatID

			// /i -- dump update info to anyone
			if strings.TrimSpace(text) == "/i" {
				info, _ := json.MarshalIndent(upd.Message, "", "  ")
				reply := fmt.Sprintf("🦞\n```\n%s\n```", string(info))
				log.Printf("[/i] MAX %d chat=%d", senderID, chatID)
				_ = maxSendMessage(token, senderID, chatID, reply)
				continue
			}

			if _, ok := allowedSet[senderID]; !ok {
				log.Printf("[--] MAX ignored from=%d (%s @%s): %q",
					senderID, upd.Message.Sender.Name, upd.Message.Sender.Username, text)
				continue
			}
			if text == "" {
				continue
			}

			if chatID != 0 {
				log.Printf("<- MAX [%d] chat=%d: %q", senderID, chatID, text)
				chID := maxMMChatChan(chatID)
				pushPostedEvent(chID, chID,
					fmt.Sprintf("max_%d", senderID), text)
			} else {
				log.Printf("<- MAX [%d]: %q", senderID, text)
				pushPostedEvent(maxMMUserID(senderID), maxMMDMChan(senderID),
					fmt.Sprintf("max_%d", senderID), text)
			}
		}
	}
}

// --- main ---

func main() {
	cfg, err := loadConfig()
	if err != nil {
		log.Fatalf("config: %v", err)
	}
	globalCfg = cfg

	// --- VK ---
	if cfg.VKToken != "" {
		vkAllowed := make(map[int]struct{}, len(cfg.AllowedUsers))
		for _, id := range cfg.AllowedUsers {
			vkAllowed[id] = struct{}{}
		}

		vkAPI = api.NewVK(cfg.VKToken)

		lp, err := longpoll.NewLongPollCommunity(vkAPI)
		if err != nil {
			log.Fatalf("VK longpoll init: %v", err)
		}

		groups, _ := vkAPI.GroupsGetByID(nil)
		if len(groups.Groups) > 0 {
			g := groups.Groups[0]
			log.Printf("[OK] VK group: [%d] %s", g.ID, g.Name)
		}

		lp.MessageNew(func(ctx context.Context, obj events.MessageNewObject) {
			msg := obj.Message

			// /i -- dump message info to anyone
			if strings.TrimSpace(msg.Text) == "/i" {
				info, _ := json.MarshalIndent(map[string]interface{}{
					"from_id": msg.FromID,
					"peer_id": msg.PeerID,
					"date":    msg.Date,
				}, "", "  ")
				reply := fmt.Sprintf("🦞\n```\n%s\n```", string(info))
				log.Printf("[/i] VK %d peer=%d", msg.FromID, msg.PeerID)
				_ = sendVKMessage(msg.PeerID, reply)
				return
			}

			if _, ok := vkAllowed[msg.FromID]; !ok {
				log.Printf("[--] VK ignored from=%d", msg.FromID)
				return
			}
			log.Printf("<- VK [%d]: %q", msg.FromID, msg.Text)
			pushPostedEvent(vkMMUserID(msg.FromID), vkMMChanID(msg.FromID),
				fmt.Sprintf("vk_%d", msg.FromID), msg.Text)
		})

		go func() {
			log.Println("[OK] VK LongPoll started")
			if err := lp.Run(); err != nil {
				log.Printf("[ERR] VK longpoll: %v", err)
			}
		}()
	} else {
		log.Println("[--] VK: disabled (no vk_token)")
	}

	// --- MAX ---
	if cfg.MaxToken != "" {
		maxAllowed := make(map[int64]struct{}, len(cfg.MaxAllowedUsers))
		for _, id := range cfg.MaxAllowedUsers {
			maxAllowed[id] = struct{}{}
		}

		me, err := maxGetMe(cfg.MaxToken)
		if err != nil {
			log.Fatalf("MAX GET /me: %v", err)
		}
		log.Printf("[OK] MAX bot: [%d] %s (@%s)", me.UserID, me.Name, me.Username)

		go func() {
			log.Println("[OK] MAX LongPoll started")
			maxPollLoop(cfg.MaxToken, maxAllowed)
		}()
	} else {
		log.Println("[--] MAX: disabled (no max_token)")
	}

	log.Printf("[OK] listening on %s", cfg.Listen)
	if err := http.ListenAndServe(cfg.Listen, makeRouter()); err != nil {
		log.Fatal(err)
	}
}
