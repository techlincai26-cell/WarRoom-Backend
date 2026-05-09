package services

import (
	"context"
	"encoding/base64"
	"log"
	"os"
	"strings"

	texttospeech "cloud.google.com/go/texttospeech/apiv1"
	"cloud.google.com/go/texttospeech/apiv1/texttospeechpb"
	"google.golang.org/api/option"
)

type TTSService struct {
	client *texttospeech.Client
}

func NewTTSService() *TTSService {
	ctx := context.Background()

	var opts []option.ClientOption
	if apiKey := os.Getenv("GCP_API_KEY"); apiKey != "" {
		opts = append(opts, option.WithAPIKey(apiKey))
	} else if saJSON := os.Getenv("GCP_SERVICE_ACCOUNT_JSON"); saJSON != "" {
		opts = append(opts, option.WithCredentialsJSON([]byte(saJSON)))
	}

	// Create the TTS client. Since we're likely using GCP Application Default Credentials,
	// or GOOGLE_APPLICATION_CREDENTIALS, this will automatically pick it up.
	// If GCP_API_KEY is provided in the environment, we use it instead.
	client, err := texttospeech.NewClient(ctx, opts...)
	if err != nil {
		log.Printf("Warning: Failed to create GCP TTS client: %v. Using Mock TTS.", err)
		return &TTSService{client: nil}
	}

	return &TTSService{
		client: client,
	}
}

// GenerateVoice takes text and gender, synthesizes speech, and returns the base64 encoded audio content.
func (s *TTSService) GenerateVoice(ctx context.Context, text string, gender string) (string, error) {
	if s.client == nil {
		// Mock TTS enabled for local development without credentials
		log.Printf("[TTSService] Mocking TTS for: %s", text)
		// Return a silent MP3 or tiny placeholder base64 (this is a valid 1KB silent MP3 base64)
		silentMP3 := "//MQA0AAAAAABIAAAAAEMBAH//////////4gAACAABgBv//qQcAAAAT+AAABv/wgAEAAAABAQD//v8A/wD+P3//f3////7////8"
		return silentMP3, nil
	}

	// Choose voice based on gender (case-insensitive)
	voiceName := "en-US-Journey-D" // Male
	if strings.EqualFold(gender, "female") {
		voiceName = "en-US-Journey-F" // Female
	}

	log.Printf("[TTSService] Generating voice for gender: %s -> mapped to %s", gender, voiceName)

	req := &texttospeechpb.SynthesizeSpeechRequest{
		Input: &texttospeechpb.SynthesisInput{
			InputSource: &texttospeechpb.SynthesisInput_Text{Text: text},
		},
		Voice: &texttospeechpb.VoiceSelectionParams{
			LanguageCode: "en-US",
			Name:         voiceName,
		},
		AudioConfig: &texttospeechpb.AudioConfig{
			AudioEncoding: texttospeechpb.AudioEncoding_MP3,
		},
	}

	resp, err := s.client.SynthesizeSpeech(ctx, req)
	if err != nil {
		log.Printf("[TTSService] SynthesizeSpeech failed: %v. Using Mock TTS.", err)
		// Fallback to mock TTS if generation fails at runtime
		silentMP3 := "//MQA0AAAAAABIAAAAAEMBAH//////////4gAACAABgBv//qQcAAAAT+AAABv/wgAEAAAABAQD//v8A/wD+P3//f3////7////8"
		return silentMP3, err
	}

	// The resp.AudioContent is a byte slice, encode it to base64
	base64Encoded := base64.StdEncoding.EncodeToString(resp.AudioContent)
	return base64Encoded, nil
}
