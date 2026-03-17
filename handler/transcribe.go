package handler

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/avvvet/semma-api/config"
	"github.com/avvvet/semma-api/service"
	"github.com/avvvet/semma-api/store"
	"github.com/google/uuid"
)

type TranscribeResponse struct {
	Text           string            `json:"text"`
	Duration       float64           `json:"duration"`
	ProcessingTime float64           `json:"processing_time"`
	Segments       []service.Segment `json:"segments"`
}

type ErrorResponse struct {
	Error string `json:"error"`
}

func Transcribe(cfg *config.Config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()

		// limit request body size
		r.Body = http.MaxBytesReader(w, r.Body, cfg.MaxFileSize)

		// parse multipart form
		if err := r.ParseMultipartForm(cfg.MaxFileSize); err != nil {
			writeError(w, "file too large, max 2MB", http.StatusBadRequest)
			return
		}

		file, header, err := r.FormFile("file")
		if err != nil {
			writeError(w, "missing audio file", http.StatusBadRequest)
			return
		}
		defer file.Close()

		// save uploaded file to temp
		tmpID := uuid.New().String()
		tmpDir := os.TempDir()
		inputPath := filepath.Join(tmpDir, tmpID+filepath.Ext(header.Filename))
		wavPath := filepath.Join(tmpDir, tmpID+"_out.wav")

		// write uploaded file
		tmpFile, err := os.Create(inputPath)
		if err != nil {
			writeError(w, "server error", http.StatusInternalServerError)
			return
		}
		defer os.Remove(inputPath)
		defer os.Remove(wavPath)

		if _, err = io.Copy(tmpFile, file); err != nil {
			tmpFile.Close()
			writeError(w, "server error", http.StatusInternalServerError)
			return
		}
		tmpFile.Close()

		// convert to wav 16kHz mono using ffmpeg
		cmd := exec.Command("ffmpeg",
			"-i", inputPath,
			"-ar", "16000",
			"-ac", "1",
			"-c:a", "pcm_s16le",
			"-y",
			wavPath,
		)
		out, err := cmd.CombinedOutput()
		log.Printf("ffmpeg output: %s", out)
		if err != nil {
			log.Printf("ffmpeg error: %v", err)
			writeError(w, "audio conversion failed", http.StatusBadRequest)
			return
		}

		// send to Ruach
		result, err := service.Transcribe(cfg.RuachURL, cfg.ModelName, wavPath)
		if err != nil {
			log.Printf("whisper error: %v", err)
			writeError(w, "transcription failed", http.StatusInternalServerError)
			return
		}

		// check duration limit
		if result.Duration > cfg.MaxDuration {
			writeError(w,
				fmt.Sprintf("audio too long, max %.0f seconds", cfg.MaxDuration),
				http.StatusBadRequest,
			)
			return
		}

		// store in recent
		processingTime := time.Since(start).Seconds()
		t := store.Transcription{
			ID:             tmpID,
			Text:           result.Text,
			Duration:       result.Duration,
			ProcessingTime: processingTime,
			CreatedAt:      time.Now(),
		}
		if err := store.Add(t); err != nil {
			log.Printf("store error: %v", err)
		}

		// respond
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(TranscribeResponse{
			Text:           result.Text,
			Duration:       result.Duration,
			ProcessingTime: processingTime,
			Segments:       result.Segments,
		})
	}
}

func writeError(w http.ResponseWriter, msg string, code int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(ErrorResponse{Error: msg})
}
