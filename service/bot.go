package service

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"mime/multipart"
	"net/http"
	"strconv"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"

	"github.com/avvvet/semma-api/config"
)

type TranscribeResponse struct {
	Text           string    `json:"text"`
	Duration       float64   `json:"duration"`
	ProcessingTime float64   `json:"processing_time"`
	Segments       []Segment `json:"segments"`
}

// StartBot runs polling (dev) or returns a webhook handler (prod).
// Call this from main.go after the HTTP server is already listening.
func StartBot(cfg *config.Config) (http.HandlerFunc, error) {
	bot, err := tgbotapi.NewBotAPI(cfg.BotToken)
	if err != nil {
		log.Printf("─────────────────────────────────────────")
		log.Printf("bot: ✗ failed to initialize")
		log.Printf("bot: reason → %v", err)
		log.Printf("bot: check  → BOT_TOKEN in .env is set and valid")
		log.Printf("─────────────────────────────────────────")
		return nil, fmt.Errorf("bot: init: %w", err)
	}

	log.Printf("─────────────────────────────────────────")
	log.Printf("bot: authorized as @%s (id: %d)", bot.Self.UserName, bot.Self.ID)
	if cfg.BotDev {
		log.Printf("bot: mode         → DEV (long polling)")
		log.Printf("bot: updates      → pulling from Telegram every 60s")
		log.Printf("bot: webhook      → not used")
	} else {
		log.Printf("bot: mode         → PROD (webhook)")
		log.Printf("bot: updates      → Telegram pushes to /telegram/webhook")
		log.Printf("bot: webhook url  → set via setWebhook before starting")
	}
	log.Printf("bot: max duration → %.0fs per audio", cfg.MaxDuration)
	log.Printf("bot: max filesize → %dMB", cfg.MaxFileSize/1024/1024)
	log.Printf("bot: transcribe   → http://localhost:%s/api/transcribe", cfg.APIPort)
	log.Printf("─────────────────────────────────────────")

	if cfg.BotDev {
		// polling — blocks in a goroutine, no HTTP handler needed
		go runPolling(bot, cfg)
		return nil, nil
	}

	// webhook — caller registers the returned handler at /telegram/webhook
	return webhookHandler(bot, cfg), nil
}

// runPolling is used locally during development.
func runPolling(bot *tgbotapi.BotAPI, cfg *config.Config) {
	u := tgbotapi.NewUpdate(0)
	u.Timeout = 60
	updates := bot.GetUpdatesChan(u)
	for update := range updates {
		go handleUpdate(bot, cfg, update) // each user handled concurrently
	}
}

// webhookHandler is used in production on Hetzner.
func webhookHandler(bot *tgbotapi.BotAPI, cfg *config.Config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var update tgbotapi.Update
		if err := json.NewDecoder(r.Body).Decode(&update); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		go handleUpdate(bot, cfg, update) // non-blocking
		w.WriteHeader(http.StatusOK)
	}
}

// handleUpdate routes incoming Telegram updates.
func handleUpdate(bot *tgbotapi.BotAPI, cfg *config.Config, update tgbotapi.Update) {
	if update.Message == nil {
		return
	}
	msg := update.Message
	chatID := msg.Chat.ID

	switch {
	case msg.Text == "/start":
		reply(bot, chatID,
			"👋 *Selam!*\n\nSend me an Amharic voice note and I'll transcribe it to text.\n\n"+
				"🎙 Just record and send — I'll do the rest.")

	case msg.Voice != nil:
		handleVoice(bot, cfg, chatID, msg.Voice.FileID, msg.Voice.Duration)

	case msg.Audio != nil:
		handleVoice(bot, cfg, chatID, msg.Audio.FileID, msg.Audio.Duration)

	default:
		reply(bot, chatID, "Send me a voice note 🎙")
	}
}

// handleVoice downloads the audio, sends it to /api/transcribe, replies with text.
func handleVoice(bot *tgbotapi.BotAPI, cfg *config.Config, chatID int64, fileID string, durationSec int) {
	if float64(durationSec) > cfg.MaxDuration {
		reply(bot, chatID, fmt.Sprintf(
			"⚠️ Audio is %ds — max is %.0fs for now. Send a shorter clip.",
			durationSec, cfg.MaxDuration,
		))
		return
	}

	// keep typing indicator alive until done
	done := make(chan struct{})
	go func() {
		for {
			select {
			case <-done:
				return
			default:
				sendTyping(bot, chatID)
				time.Sleep(4 * time.Second)
			}
		}
	}()
	defer close(done)

	// get download URL from Telegram
	fileURL, err := bot.GetFileDirectURL(fileID)
	if err != nil {
		log.Printf("bot: get file url: %v", err)
		reply(bot, chatID, "❌ Could not fetch your audio. Try again.")
		return
	}

	audioData, err := downloadBytes(fileURL)
	if err != nil {
		log.Printf("bot: download audio: %v", err)
		reply(bot, chatID, "❌ Download failed. Try again.")
		return
	}

	result, err := callTranscribe(cfg, audioData, "voice.ogg")
	if err != nil {
		log.Printf("bot: transcribe: %v", err)
		reply(bot, chatID, "⚠️ "+err.Error())
		return
	}

	// fake credits for now
	creditsRemaining := 120

	text := fmt.Sprintf(
		"%s\n\n⏱ %ds  |  ⚡ %.1fs  |  🪙 %ds left  |  semma.io\n📲 t.me/semmaio_bot",
		result.Text,
		int(result.Duration),
		result.ProcessingTime,
		creditsRemaining,
	)

	msg := tgbotapi.NewMessage(chatID, text)
	msg.ParseMode = ""
	if _, err := bot.Send(msg); err != nil {
		log.Printf("bot: send reply: %v", err)
	}
}

// callTranscribe posts audio bytes to your existing HTTP endpoint.
func callTranscribe(cfg *config.Config, audioData []byte, filename string) (*TranscribeResponse, error) {
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)

	fw, err := w.CreateFormFile("file", filename)
	if err != nil {
		return nil, err
	}
	if _, err = fw.Write(audioData); err != nil {
		return nil, err
	}
	w.Close()

	url := "http://localhost:" + cfg.APIPort + "/api/transcribe"
	resp, err := http.Post(url, w.FormDataContentType(), &buf)
	if err != nil {
		return nil, fmt.Errorf("http post: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		// try to extract the error message from JSON
		var errResp struct {
			Error string `json:"error"`
		}
		if json.Unmarshal(body, &errResp) == nil && errResp.Error != "" {
			return nil, fmt.Errorf("%s", errResp.Error)
		}
		return nil, fmt.Errorf("status %d: %s", resp.StatusCode, body)
	}

	var result TranscribeResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decode: %w", err)
	}
	return &result, nil
}

// downloadBytes fetches raw bytes from a URL.
func downloadBytes(url string) ([]byte, error) {
	resp, err := http.Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	return io.ReadAll(resp.Body)
}

// reply sends a markdown message to a chat.
func reply(bot *tgbotapi.BotAPI, chatID int64, text string) {
	msg := tgbotapi.NewMessage(chatID, text)
	msg.ParseMode = tgbotapi.ModeMarkdown
	if _, err := bot.Send(msg); err != nil {
		log.Printf("bot: send message to %s: %v", strconv.FormatInt(chatID, 10), err)
	}
}

// sendTyping sends the "typing..." action indicator.
func sendTyping(bot *tgbotapi.BotAPI, chatID int64) bool {
	action := tgbotapi.NewChatAction(chatID, tgbotapi.ChatTyping)
	_, err := bot.Request(action)
	return err == nil
}
