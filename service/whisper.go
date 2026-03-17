package service

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
)

type Segment struct {
	ID    int     `json:"id"`
	Start float64 `json:"start"`
	End   float64 `json:"end"`
	Text  string  `json:"text"`
}

type WhisperResponse struct {
	Task     string    `json:"task"`
	Language string    `json:"language"`
	Duration float64   `json:"duration"`
	Text     string    `json:"text"`
	Segments []Segment `json:"segments"`
}

func Transcribe(semmaURL, modelName, wavPath string) (*WhisperResponse, error) {
	f, err := os.Open(wavPath)
	if err != nil {
		return nil, fmt.Errorf("whisper: open wav: %w", err)
	}
	defer f.Close()

	// build multipart form
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)

	fw, err := w.CreateFormFile("file", filepath.Base(wavPath))
	if err != nil {
		return nil, fmt.Errorf("whisper: create form file: %w", err)
	}
	if _, err = io.Copy(fw, f); err != nil {
		return nil, fmt.Errorf("whisper: copy file: %w", err)
	}

	//w.WriteField("model", modelName)
	w.WriteField("language", "am")
	w.WriteField("response_format", "verbose_json")
	w.Close()

	// send request
	resp, err := http.Post(
		semmaURL+"/v1/audio/transcriptions",
		w.FormDataContentType(),
		&buf,
	)
	if err != nil {
		return nil, fmt.Errorf("whisper: http post: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("whisper: status %d: %s", resp.StatusCode, body)
	}

	var result WhisperResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("whisper: decode response: %w", err)
	}

	return &result, nil
}
