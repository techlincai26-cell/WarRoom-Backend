package services

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	"time"
)

// ============================================
// AI SERVICE - Gemini Integration
// ============================================

type AIService struct {
	APIKey string
	Model  string
	client *http.Client
}

func NewAIService() *AIService {
	apiKey := firstNonEmpty(
		os.Getenv("GEMINI_API_KEY"),
		os.Getenv("GOOGLE_API_KEY"),
		os.Getenv("GEMINI_API_KEY_TIER1"),
	)

	model := os.Getenv("AI_MODEL")
	if model == "" {
		model = "gemini-2.5-flash-lite"
	}

	log.Printf("[AIService] Initialized with model: %s, API key set: %v", model, apiKey != "")

	return &AIService{
		APIKey: apiKey,
		Model:  model,
		client: &http.Client{
			Timeout: 30 * time.Second,
		},
	}
}

// BatchEvaluateOpenText evaluates multiple open-text responses in a single batched AI call.
type BatchEvaluationItem struct {
	QuestionID   string   `json:"questionId"`
	QuestionText string   `json:"questionText"`
	ResponseText string   `json:"responseText"`
	Competencies []string `json:"competencies"`
}

type BatchEvaluationResult struct {
	Results []struct {
		QuestionID string         `json:"questionId"`
		Evaluation TextEvaluation `json:"evaluation"`
	} `json:"results"`
}

func (ai *AIService) BatchEvaluateOpenText(
	tasks []BatchEvaluationItem,
	competencyDefinitions map[string]CompetencyDef,
) (map[string]*TextEvaluation, error) {
	if len(tasks) == 0 {
		return map[string]*TextEvaluation{}, nil
	}

	// 1. Build context
	relevantComps := make(map[string]bool)
	for _, t := range tasks {
		for _, c := range t.Competencies {
			relevantComps[c] = true
		}
	}

	compContext := ""
	for code := range relevantComps {
		if def, ok := competencyDefinitions[code]; ok {
			compContext += fmt.Sprintf("\n%s - %s:\n  Developing (P1): %s\n  Strong (P2): %s\n  Advanced (P3): %s\n",
				code, def.Name,
				strings.Join(def.Developing, "; "),
				strings.Join(def.Strong, "; "),
				strings.Join(def.Advanced, "; "),
			)
		}
	}

	systemPrompt := fmt.Sprintf(`You are an expert business simulation evaluator for KK's War Room 2.0.
You evaluate multiple participant responses based on competency rubrics. 

COMPETENCIES BEING ASSESSED:
%s

SCORING RULES:
- P1 (Developing/1): Clear competency gaps or risky reasoning likely to fail in real execution.
- P2 (Strong/2): Competency is demonstrated with workable logic and practical trade-offs in typical business conditions.
- P3 (Advanced/3): Competency is demonstrated with strategic depth, clear execution mechanics, and strong pressure-handling.

CALIBRATION RULES:
- Start from P2 as the baseline for a coherent, relevant answer.
- Move to P1 only when there is explicit evidence of harmful gaps, weak logic, or avoidance of the question.
- Move to P3 only when there is explicit evidence of advanced strategic thinking plus concrete execution detail.
- Do not penalize concise answers if they are logically sound and directly answer the prompt.
- Use only evidence present in the response; avoid assumptions.

IDENTIFYING SIGNALS:
- "Positive Signals": Evidence of mastery, foresight, or efficiency.
- "Negative Signals": Evidence of oversight, excessive risk, or reactive behavior.

You MUST respond in valid JSON format:
{
  "results": [
    {
      "questionId": "<qid>",
      "evaluation": {
        "proficiency": <1|2|3>,
        "feedback": "<2-3 sentence assessment>",
        "reasoning": "<why this proficiency level specifically in relation to the indicators above>",
        "strengths": ["<specific positive signal identified>"],
        "weaknesses": ["<specific negative signal identified>"],
        "signals": {
          "positive": ["<detailed strength>"],
          "negative": ["<detailed weakness>"]
        }
      }
    }
  ]
}

Be analytical, hyper-focused on the competency rubric, and provide evidence-backed feedback based on their specific response.`, compContext)

	var userPromptBuilder strings.Builder
	userPromptBuilder.WriteString("Please evaluate the following responses:\n\n")
	for _, t := range tasks {
		userPromptBuilder.WriteString(fmt.Sprintf("ID: %s\nQUESTION: %s\nRESPONSE: %s\nCOMPETENCIES: %s\n---\n",
			t.QuestionID, t.QuestionText, t.ResponseText, strings.Join(t.Competencies, ", ")))
	}

	messages := []ChatMessage{
		{Role: "system", Content: systemPrompt},
		{Role: "user", Content: userPromptBuilder.String()},
	}

	resp, err := ai.Call(messages)
	if err != nil {
		log.Printf("[AIService] BatchEvaluateOpenText AI call failed: %v", err)
		results := make(map[string]*TextEvaluation)
		for _, t := range tasks {
			results[t.QuestionID] = &TextEvaluation{Proficiency: 2, Feedback: "Default score due to AI error."}
		}
		return results, nil
	}

	var batchResult BatchEvaluationResult
	content := resp.Content
	start := strings.Index(content, "{")
	end := strings.LastIndex(content, "}")
	if start >= 0 && end > start {
		if err := json.Unmarshal([]byte(content[start:end+1]), &batchResult); err != nil {
			log.Printf("[AIService] BatchEvaluateOpenText unmarshal failed: %v", err)
		}
	}

	finalResults := make(map[string]*TextEvaluation)
	for _, r := range batchResult.Results {
		// Clamp proficiency
		if r.Evaluation.Proficiency < 1 {
			r.Evaluation.Proficiency = 1
		}
		if r.Evaluation.Proficiency > 3 {
			r.Evaluation.Proficiency = 3
		}
		finalResults[r.QuestionID] = &r.Evaluation
	}

	// Ensure all tasks have a result
	for _, t := range tasks {
		if _, ok := finalResults[t.QuestionID]; !ok {
			finalResults[t.QuestionID] = &TextEvaluation{Proficiency: 2, Feedback: "Fallback score."}
		}
	}

	return finalResults, nil
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

// ChatMessage represents a message in the conversation
type ChatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// AIResponse represents the response from AI
type AIResponse struct {
	Content string `json:"content"`
}

// ============================================
// Gemini API types
// ============================================

type geminiInlineData struct {
	MimeType string `json:"mimeType"`
	Data     string `json:"data"` // base64-encoded
}

type geminiPart struct {
	Text       string            `json:"text,omitempty"`
	InlineData *geminiInlineData `json:"inlineData,omitempty"`
}

type geminiContent struct {
	Role  string       `json:"role,omitempty"`
	Parts []geminiPart `json:"parts"`
}

type geminiRequest struct {
	Contents          []geminiContent        `json:"contents"`
	SystemInstruction *geminiContent         `json:"systemInstruction,omitempty"`
	GenerationConfig  map[string]interface{} `json:"generationConfig,omitempty"`
}

type geminiResponse struct {
	Candidates []struct {
		Content struct {
			Parts []struct {
				Text string `json:"text"`
			} `json:"parts"`
			Role string `json:"role"`
		} `json:"content"`
	} `json:"candidates"`
	Error *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
		Status  string `json:"status"`
	} `json:"error"`
}

// ============================================
// CORE AI CALL - Gemini API
// ============================================

func (ai *AIService) Call(messages []ChatMessage) (*AIResponse, error) {
	if ai.APIKey == "" {
		return nil, fmt.Errorf("Gemini API key is not configured (set GEMINI_API_KEY, GOOGLE_API_KEY, or GEMINI_API_KEY_TIER1)")
	}

	// Build Gemini request from ChatMessage format
	var systemText string
	var contents []geminiContent

	for _, msg := range messages {
		switch msg.Role {
		case "system":
			systemText = msg.Content
		case "user":
			contents = append(contents, geminiContent{
				Role:  "user",
				Parts: []geminiPart{{Text: msg.Content}},
			})
		case "assistant", "model":
			contents = append(contents, geminiContent{
				Role:  "model",
				Parts: []geminiPart{{Text: msg.Content}},
			})
		}
	}

	gemReq := geminiRequest{
		Contents: contents,
		GenerationConfig: map[string]interface{}{
			"temperature":     0.3,
			"maxOutputTokens": 2048,
		},
	}

	if systemText != "" {
		gemReq.SystemInstruction = &geminiContent{
			Parts: []geminiPart{{Text: systemText}},
		}
	}

	body, err := json.Marshal(gemReq)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}

	url := fmt.Sprintf("https://generativelanguage.googleapis.com/v1beta/models/%s:generateContent?key=%s", ai.Model, ai.APIKey)

	var respBody []byte
	var result geminiResponse

	maxRetries := 3
	for i := 0; i <= maxRetries; i++ {
		req, errReq := http.NewRequest("POST", url, bytes.NewBuffer(body))
		if errReq != nil {
			return nil, fmt.Errorf("failed to create request: %w", errReq)
		}
		req.Header.Set("Content-Type", "application/json")

		resp, errDo := ai.client.Do(req)
		if errDo != nil {
			if i == maxRetries {
				return nil, fmt.Errorf("API request failed after %d retries: %w", maxRetries, errDo)
			}
			log.Printf("[AIService] Attempt %d failed: %v. Retrying...", i+1, errDo)
			time.Sleep(time.Duration(1<<uint(i)) * time.Second)
			continue
		}

		respBody, errDo = io.ReadAll(resp.Body)
		resp.Body.Close()

		if errDo != nil {
			if i == maxRetries {
				return nil, fmt.Errorf("failed to read response after %d retries: %w", maxRetries, errDo)
			}
			log.Printf("[AIService] Attempt %d failed to read response. Retrying...", i+1)
			time.Sleep(time.Duration(1<<uint(i)) * time.Second)
			continue
		}

		if resp.StatusCode != http.StatusOK {
			errMsg := fmt.Sprintf("Gemini API error %d: %s", resp.StatusCode, string(respBody))
			// Retry on 429 Too Many Requests or 503 Service Unavailable Errors
			if (resp.StatusCode == 429 || resp.StatusCode == 503) && i < maxRetries {
				log.Printf("[AIService] Retryable error on attempt %d: %s", i+1, errMsg)
				time.Sleep(time.Duration(1<<uint(i)) * time.Second)
				continue
			}
			log.Printf("[AIService] %s", errMsg)
			return nil, fmt.Errorf("%s", errMsg)
		}

		if errUnmarshal := json.Unmarshal(respBody, &result); errUnmarshal != nil {
			return nil, fmt.Errorf("failed to parse response: %w", errUnmarshal)
		}

		break // Success, exit retry loop
	}

	if result.Error != nil {
		return nil, fmt.Errorf("Gemini error: %s", result.Error.Message)
	}

	if len(result.Candidates) == 0 || len(result.Candidates[0].Content.Parts) == 0 {
		return nil, fmt.Errorf("no content in Gemini response")
	}

	// Gemini 2.5 "thinking" models return multiple parts: thought + answer.
	// Always use the LAST text part which contains the actual response.
	parts := result.Candidates[0].Content.Parts
	content := parts[len(parts)-1].Text
	return &AIResponse{Content: content}, nil
}

// ============================================
// CALL WITH AUDIO - Gemini Inline Audio
// ============================================

func (ai *AIService) CallWithAudio(systemPrompt string, userText string, audioBase64 string, audioMimeType string) (*AIResponse, error) {
	if ai.APIKey == "" {
		return nil, fmt.Errorf("Gemini API key is not configured")
	}

	// Build parts: audio inline data + text prompt
	userParts := []geminiPart{
		{
			InlineData: &geminiInlineData{
				MimeType: audioMimeType,
				Data:     audioBase64,
			},
		},
		{Text: userText},
	}

	gemReq := geminiRequest{
		Contents: []geminiContent{
			{Role: "user", Parts: userParts},
		},
		GenerationConfig: map[string]interface{}{
			"temperature":     0.3,
			"maxOutputTokens": 4096,
		},
	}

	if systemPrompt != "" {
		gemReq.SystemInstruction = &geminiContent{
			Parts: []geminiPart{{Text: systemPrompt}},
		}
	}

	body, err := json.Marshal(gemReq)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}

	url := fmt.Sprintf("https://generativelanguage.googleapis.com/v1beta/models/%s:generateContent?key=%s", ai.Model, ai.APIKey)

	var respBody []byte
	var result geminiResponse

	maxRetries := 3
	for i := 0; i <= maxRetries; i++ {
		req, errReq := http.NewRequest("POST", url, bytes.NewBuffer(body))
		if errReq != nil {
			return nil, fmt.Errorf("failed to create request: %w", errReq)
		}
		req.Header.Set("Content-Type", "application/json")

		resp, errDo := ai.client.Do(req)
		if errDo != nil {
			if i == maxRetries {
				return nil, fmt.Errorf("API request failed after %d retries: %w", maxRetries, errDo)
			}
			log.Printf("[AIService] Audio call attempt %d failed: %v. Retrying...", i+1, errDo)
			time.Sleep(time.Duration(1<<uint(i)) * time.Second)
			continue
		}

		respBody, errDo = io.ReadAll(resp.Body)
		resp.Body.Close()

		if errDo != nil {
			if i == maxRetries {
				return nil, fmt.Errorf("failed to read response after %d retries: %w", maxRetries, errDo)
			}
			log.Printf("[AIService] Audio call attempt %d failed to read response. Retrying...", i+1)
			time.Sleep(time.Duration(1<<uint(i)) * time.Second)
			continue
		}

		if resp.StatusCode != http.StatusOK {
			errMsg := fmt.Sprintf("Gemini API error %d: %s", resp.StatusCode, string(respBody))
			if (resp.StatusCode == 429 || resp.StatusCode == 503) && i < maxRetries {
				log.Printf("[AIService] Retryable error on attempt %d: %s", i+1, errMsg)
				time.Sleep(time.Duration(1<<uint(i)) * time.Second)
				continue
			}
			log.Printf("[AIService] %s", errMsg)
			return nil, fmt.Errorf("%s", errMsg)
		}

		if errUnmarshal := json.Unmarshal(respBody, &result); errUnmarshal != nil {
			return nil, fmt.Errorf("failed to parse response: %w", errUnmarshal)
		}

		break
	}

	if result.Error != nil {
		return nil, fmt.Errorf("Gemini error: %s", result.Error.Message)
	}

	if len(result.Candidates) == 0 || len(result.Candidates[0].Content.Parts) == 0 {
		return nil, fmt.Errorf("no content in Gemini audio response")
	}

	parts := result.Candidates[0].Content.Parts
	content := parts[len(parts)-1].Text
	return &AIResponse{Content: content}, nil
}

// ============================================
// ANALYZE PITCH AUDIO
// ============================================

type PitchAudioAnalysis struct {
	Transcription string   `json:"transcription"`
	Feedback      string   `json:"feedback"`
	Strengths     []string `json:"strengths"`
	Weaknesses    []string `json:"weaknesses"`
	OverallScore  int      `json:"overallScore"` // 1-10
	Clarity       int      `json:"clarity"`      // 1-5
	Confidence    int      `json:"confidence"`   // 1-5
	Persuasion    int      `json:"persuasion"`   // 1-5
}

func (ai *AIService) AnalyzePitchAudio(audioBase64 string, mimeType string) (*PitchAudioAnalysis, error) {
	systemPrompt := `You are an expert investor panel evaluator for KK's War Room 2.0 business simulation.
You are listening to a founder's investment pitch. Your task is to:
1. Transcribe the spoken audio accurately
2. Evaluate the pitch quality

Respond in valid JSON:
{
  "transcription": "<exact transcription of what was said>",
  "feedback": "<2-3 sentence overall assessment of the pitch>",
  "strengths": ["<strength1>", "<strength2>"],
  "weaknesses": ["<weakness1>", "<weakness2>"],
  "overallScore": <1-10>,
  "clarity": <1-5 how clear and structured>,
  "confidence": <1-5 how confident the speaker sounds>,
  "persuasion": <1-5 how persuasive the pitch is>
}

EVALUATION CRITERIA:
- Problem-Solution clarity: Did they clearly state the problem and their solution?
- Market understanding: Do they know their target customer?
- Differentiation: What makes them unique vs competitors?
- Traction/Validation: Did they mention any proof of concept?
- The Ask: Did they clearly state how much capital and for what equity?
- Delivery: Confidence, pace, clarity of speech

Be analytical and honest. If the audio is unclear or too short, note that in feedback.
If the audio only contains silence, background noise, or is too short to contain coherent speech, YOU MUST strictly return "[No speech detected]" for the transcription and set all scores to 1. DO NOT invent, hallucinate, or auto-generate filler text (such as "thank you", "hello", etc.).`

	userText := "Listen to this founder's investment pitch and provide your analysis."

	resp, err := ai.CallWithAudio(systemPrompt, userText, audioBase64, mimeType)
	if err != nil {
		log.Printf("[AIService] AnalyzePitchAudio failed: %v", err)
		return &PitchAudioAnalysis{
			Transcription: "[Audio analysis failed]",
			Feedback:      "Unable to analyze pitch audio at this time.",
			Strengths:     []string{},
			Weaknesses:    []string{},
			OverallScore:  5,
			Clarity:       3,
			Confidence:    3,
			Persuasion:    3,
		}, nil
	}

	var analysis PitchAudioAnalysis
	content := resp.Content
	start := strings.Index(content, "{")
	end := strings.LastIndex(content, "}")
	if start >= 0 && end > start {
		if err := json.Unmarshal([]byte(content[start:end+1]), &analysis); err != nil {
			log.Printf("[AIService] AnalyzePitchAudio parse error: %v", err)
			return &PitchAudioAnalysis{
				Transcription: content,
				Feedback:      "Response received but could not be parsed.",
				OverallScore:  5,
			}, nil
		}
	}

	// Clamp scores
	if analysis.OverallScore < 1 {
		analysis.OverallScore = 1
	}
	if analysis.OverallScore > 10 {
		analysis.OverallScore = 10
	}
	if analysis.Clarity < 1 {
		analysis.Clarity = 1
	}
	if analysis.Clarity > 5 {
		analysis.Clarity = 5
	}
	if analysis.Confidence < 1 {
		analysis.Confidence = 1
	}
	if analysis.Confidence > 5 {
		analysis.Confidence = 5
	}
	if analysis.Persuasion < 1 {
		analysis.Persuasion = 1
	}
	if analysis.Persuasion > 5 {
		analysis.Persuasion = 5
	}

	return &analysis, nil
}

// ============================================
// ANALYZE INVESTOR RESPONSE AUDIO
// ============================================

type InvestorResponseAudioAnalysis struct {
	Transcription  string   `json:"transcription"`
	PrimaryScore   int      `json:"primary_score"`
	BiasTraitScore int      `json:"bias_trait_score"`
	RedFlags       []string `json:"red_flags"`
	Reaction       string   `json:"reaction"`
}

func (ai *AIService) AnalyzeInvestorResponseAudio(
	audioBase64 string,
	mimeType string,
	investorName string,
	investorLens string,
	biasTraitName string,
	question string,
) (*InvestorResponseAudioAnalysis, error) {

	systemPrompt := fmt.Sprintf(`You are %s, an investor on KK's War Room panel.
Your primary lens: %s
Your bias trait to evaluate: %s

You asked the founder: "%s"

Now listen to their spoken response. Your task is to:
1. Transcribe their spoken response accurately
2. Evaluate it as %s would

RED FLAG triggers (each causes -1 penalty):
- Blames others for failures
- Avoids or deflects the question
- Overuse of hype language without substance
- Defensive or aggressive tone
- Contradictions detected in their story

CRITICAL RULE: NEVER mention internal simulation mechanics, game terms, or phases (like "phase 2 of negotiation"). Stay completely in character as a real-world investor reacting to a real-world pitch.

Respond in valid JSON:
{
  "transcription": "<exact transcription of what was said>",
  "primary_score": <1-5 how well they answered>,
  "bias_trait_score": <1-5 how well they demonstrate %s>,
  "red_flags": ["<flag1>"] or [],
  "reaction": "<2-3 sentence in-character reaction as %s>"
}

If the audio only contains silence, background noise, or is too short to contain coherent speech, YOU MUST strictly return "[No speech detected]" for the transcription and set all scores to 1. DO NOT invent, hallucinate, or auto-generate filler text (such as "thank you", "hello", etc.).`,
		investorName, investorLens, biasTraitName, question, investorName, biasTraitName, investorName)

	userText := fmt.Sprintf("Listen to this founder's response to %s's question and evaluate it.", investorName)

	resp, err := ai.CallWithAudio(systemPrompt, userText, audioBase64, mimeType)
	if err != nil {
		log.Printf("[AIService] AnalyzeInvestorResponseAudio failed: %v", err)
		return &InvestorResponseAudioAnalysis{
			Transcription:  "[Audio analysis failed]",
			PrimaryScore:   3,
			BiasTraitScore: 3,
			RedFlags:       []string{},
			Reaction:       "I need to think about this.",
		}, nil
	}

	var analysis InvestorResponseAudioAnalysis
	content := resp.Content
	start := strings.Index(content, "{")
	end := strings.LastIndex(content, "}")
	if start >= 0 && end > start {
		json.Unmarshal([]byte(content[start:end+1]), &analysis)
	}

	// Clamp scores
	if analysis.PrimaryScore < 1 {
		analysis.PrimaryScore = 1
	}
	if analysis.PrimaryScore > 5 {
		analysis.PrimaryScore = 5
	}
	if analysis.BiasTraitScore < 1 {
		analysis.BiasTraitScore = 1
	}
	if analysis.BiasTraitScore > 5 {
		analysis.BiasTraitScore = 5
	}

	// Ensure reaction is never empty
	if strings.TrimSpace(analysis.Reaction) == "" {
		if analysis.PrimaryScore >= 4 {
			analysis.Reaction = fmt.Sprintf("That's a strong answer. I like what I'm hearing. — %s", investorName)
		} else if analysis.PrimaryScore >= 3 {
			analysis.Reaction = fmt.Sprintf("Fair enough. You've got potential but I need more. — %s", investorName)
		} else {
			analysis.Reaction = fmt.Sprintf("I'm not convinced. You need to do better. — %s", investorName)
		}
	}

	return &analysis, nil
}

// ============================================
// EVALUATE OPEN TEXT RESPONSE
// ============================================

type TextEvaluation struct {
	Proficiency int                `json:"proficiency"` // 1=P1, 2=P2, 3=P3
	Feedback    string             `json:"feedback"`
	Reasoning   string             `json:"reasoning"`
	Strengths   []string           `json:"strengths"`
	Weaknesses  []string           `json:"weaknesses"`
	Signals     *EvaluationSignals `json:"signals,omitempty"`
}

type EvaluationSignals struct {
	Positive []string `json:"positive"`
	Negative []string `json:"negative"`
}

func (ai *AIService) EvaluateOpenText(
	questionText string,
	responseText string,
	competencies []string,
	competencyDefinitions map[string]CompetencyDef,
) (*TextEvaluation, error) {

	// Build competency context
	compContext := ""
	for _, code := range competencies {
		if def, ok := competencyDefinitions[code]; ok {
			compContext += fmt.Sprintf("\n%s - %s:\n  Developing (P1): %s\n  Strong (P2): %s\n  Advanced (P3): %s\n",
				code, def.Name,
				strings.Join(def.Developing, "; "),
				strings.Join(def.Strong, "; "),
				strings.Join(def.Advanced, "; "),
			)
		}
	}

	systemPrompt := fmt.Sprintf(`You are an expert business simulation evaluator for KK's War Room 2.0.
You evaluate participant responses based on competency rubrics. 

COMPETENCIES BEING ASSESSED:
%s

SCORING RULES:
- P1 (Developing/1): Clear competency gaps or risky logic that would weaken real execution.
- P2 (Strong/2): Competency is demonstrated with practical and coherent reasoning in normal business conditions.
- P3 (Advanced/3): Competency is demonstrated with strong strategic depth and concrete execution clarity under pressure.

CALIBRATION RULES:
- Begin from P2 for any coherent and relevant response.
- Downgrade to P1 only when clear evidence shows weak reasoning, critical omissions, or harmful trade-offs.
- Upgrade to P3 only when clear evidence shows advanced, non-obvious judgment and execution detail.
- Keep scoring evidence-based and avoid harsh penalties for brevity.

You MUST respond in valid JSON format:
{
  "proficiency": <1|2|3>,
  "feedback": "<2-3 sentence assessment>",
  "reasoning": "<why this proficiency level>",
  "strengths": ["<strength1>", "<strength2>"],
  "weaknesses": ["<weakness1>"]
}

Be analytical, respectful, grounded. Not motivational hype. Not dramatic. Focus on observable behavior patterns.`, compContext)

	userPrompt := fmt.Sprintf("QUESTION: %s\n\nPARTICIPANT RESPONSE: %s\n\nEvaluate this response and assign a proficiency level (P1/P2/P3) for the assessed competencies.", questionText, responseText)

	messages := []ChatMessage{
		{Role: "system", Content: systemPrompt},
		{Role: "user", Content: userPrompt},
	}

	resp, err := ai.Call(messages)
	if err != nil {
		log.Printf("[AIService] EvaluateOpenText AI call failed: %v", err)
		// Fallback to P2 on error
		return &TextEvaluation{
			Proficiency: 2,
			Feedback:    "Response evaluated with default scoring due to AI service error.",
			Reasoning:   fmt.Sprintf("AI service error: %v", err),
		}, nil
	}

	var eval TextEvaluation
	if err := json.Unmarshal([]byte(resp.Content), &eval); err != nil {
		// Try to extract JSON from the response
		content := resp.Content
		start := strings.Index(content, "{")
		end := strings.LastIndex(content, "}")
		if start >= 0 && end > start {
			if err2 := json.Unmarshal([]byte(content[start:end+1]), &eval); err2 != nil {
				return &TextEvaluation{
					Proficiency: 2,
					Feedback:    "Response received but could not be parsed.",
					Reasoning:   "Parse error",
				}, nil
			}
		} else {
			return &TextEvaluation{
				Proficiency: 2,
				Feedback:    content,
				Reasoning:   "Unstructured response",
			}, nil
		}
	}

	// Clamp proficiency
	if eval.Proficiency < 1 {
		eval.Proficiency = 1
	}
	if eval.Proficiency > 3 {
		eval.Proficiency = 3
	}

	return &eval, nil
}

// CompetencyDef is a simplified competency definition for AI prompts
type CompetencyDef struct {
	Name       string
	Developing []string
	Strong     []string
	Advanced   []string
}

// ============================================
// GENERATE MENTOR GUIDANCE
// ============================================

func (ai *AIService) GenerateMentorGuidance(
	mentorName string,
	mentorStyle string,
	mentorTone string,
	phaseContext string,
	participantBusiness string,
	userQuestion string,
) (string, error) {

	systemPrompt := fmt.Sprintf(`You are %s, a mentor in KK's War Room business simulation.

Your guidance style: %s
Your tone: %s

You are providing strategic guidance to a participant who has used one of their 3 lifelines to consult you.
The participant's business: %s
Current simulation context: %s

RULES:
- Stay in character as %s
- Directly answer the specific question the user asks you
- Give practical, actionable advice tailored to their question
- Don't simply give away the exact "right answer", but guide their strategic thinking
- Keep response to 3-5 sentences
- Be encouraging but realistic`, mentorName, mentorStyle, mentorTone, participantBusiness, phaseContext, mentorName)

	var userPrompt string
	if userQuestion != "" {
		userPrompt = fmt.Sprintf("The participant has a specific question for you:\n\n\"%s\"\n\nWhat advice would you give in character?", userQuestion)
	} else {
		userPrompt = "The participant has asked for general guidance for their current situation. What advice would you give in character?"
	}

	messages := []ChatMessage{
		{Role: "system", Content: systemPrompt},
		{Role: "user", Content: userPrompt},
	}

	resp, err := ai.Call(messages)
	if err != nil {
		return fmt.Sprintf("As %s would say: Trust your instincts here, but make sure you've done the research first. Consider the long-term impact of this decision.", mentorName), nil
	}

	return resp.Content, nil
}

// ============================================
// GENERATE INVESTOR REACTION
// ============================================

type InvestorEvaluation struct {
	PrimaryScore   int      `json:"primary_score"`    // 1-5
	BiasTraitScore int      `json:"bias_trait_score"` // 1-5
	RedFlags       []string `json:"red_flags"`
	Reaction       string   `json:"reaction"`
}

func (ai *AIService) EvaluateInvestorResponse(
	investorName string,
	investorLens string,
	biasTraitName string,
	question string,
	response string,
) (*InvestorEvaluation, error) {

	systemPrompt := fmt.Sprintf(`You are %s, an investor on KK's War Room panel.
Your primary lens: %s
Your bias trait to evaluate: %s

You are evaluating a founder's response to your question.

RED FLAG triggers (each causes -1 penalty):
- Blames others for failures
- Avoids or deflects the question
- Overuse of hype language without substance
- Defensive or aggressive tone
- Contradictions detected in their story

CRITICAL RULE: NEVER mention internal simulation mechanics, game terms, or phases (like "phase 2 of negotiation"). Stay completely in character as a real-world investor reacting to a real-world pitch.

Respond in valid JSON:
{
  "primary_score": <1-5 how well they answered>,
  "bias_trait_score": <1-5 how well they demonstrate %s>,
  "red_flags": ["<flag1>"] or [],
  "reaction": "<2-3 sentence in-character reaction as %s>"
}`, investorName, investorLens, biasTraitName, biasTraitName, investorName)

	userPrompt := fmt.Sprintf("QUESTION: %s\n\nFOUNDER'S RESPONSE: %s", question, response)

	messages := []ChatMessage{
		{Role: "system", Content: systemPrompt},
		{Role: "user", Content: userPrompt},
	}

	resp, err := ai.Call(messages)
	if err != nil {
		return &InvestorEvaluation{
			PrimaryScore:   3,
			BiasTraitScore: 3,
			RedFlags:       []string{},
			Reaction:       "Interesting. I need to think about this more.",
		}, nil
	}

	var eval InvestorEvaluation
	content := resp.Content
	start := strings.Index(content, "{")
	end := strings.LastIndex(content, "}")
	if start >= 0 && end > start {
		json.Unmarshal([]byte(content[start:end+1]), &eval)
	}

	// Clamp scores
	if eval.PrimaryScore < 1 {
		eval.PrimaryScore = 1
	}
	if eval.PrimaryScore > 5 {
		eval.PrimaryScore = 5
	}
	if eval.BiasTraitScore < 1 {
		eval.BiasTraitScore = 1
	}
	if eval.BiasTraitScore > 5 {
		eval.BiasTraitScore = 5
	}

	// Ensure reaction is never empty — the frontend relies on it to advance the flow
	if strings.TrimSpace(eval.Reaction) == "" {
		if eval.PrimaryScore >= 4 {
			eval.Reaction = fmt.Sprintf("That's a strong answer. I like what I'm hearing from you. — %s", investorName)
		} else if eval.PrimaryScore >= 3 {
			eval.Reaction = fmt.Sprintf("Fair enough. You've got potential but I need to see more conviction. — %s", investorName)
		} else {
			eval.Reaction = fmt.Sprintf("I'm not convinced. You need to do better than that. — %s", investorName)
		}
	}

	return &eval, nil
}

// ============================================
// GENERATE ARCHETYPE NARRATIVE
// ============================================

func (ai *AIService) GenerateArchetypeNarrative(
	competencyRanking []map[string]interface{},
	stageDecisions []map[string]interface{},
	entrepreneurType string,
	roleFit string,
) (string, error) {

	systemPrompt := `You are an expert evaluator generating the KK War Room 2.0 Archetype Narrative.

RULES:
- Archetype MUST be derived only from simulation behavior patterns
- No generic traits, no external assumptions
- Tone: Analytical, respectful, grounded
- NOT motivational hype, NOT dramatic
- Focus on observable patterns from the simulation
- Keep to 2-3 paragraphs maximum

STRUCTURE:
"Across the 12-month simulation, your strongest and most stable competencies were [top competencies]. During [specific stages], you consistently [observed patterns]. In [situations], you [specific behaviors].

Based on these observable patterns, your simulation reflects [Role/Type] related roles comes naturally to you and someone who [behavioral summary]."`

	compData, _ := json.Marshal(competencyRanking)
	decisionData, _ := json.Marshal(stageDecisions)

	userPrompt := fmt.Sprintf(`Generate an archetype narrative based on:

COMPETENCY RANKING (highest to lowest):
%s

KEY DECISIONS ACROSS STAGES:
%s

ENTREPRENEUR TYPE: %s
ORGANIZATIONAL ROLE FIT: %s`, string(compData), string(decisionData), entrepreneurType, roleFit)

	messages := []ChatMessage{
		{Role: "system", Content: systemPrompt},
		{Role: "user", Content: userPrompt},
	}

	resp, err := ai.Call(messages)
	if err != nil {
		return fmt.Sprintf("Based on simulation performance, the participant demonstrates characteristics aligned with %s roles, with strongest competencies in the areas that support this fit.", roleFit), nil
	}

	return resp.Content, nil
}

// ============================================
// GENERATE LEADER CHALLENGE QUESTION
// ============================================

// GenerateLeaderChallenge creates an end-of-phase challenge question from a leader
// based on the participant's stage responses. Returns the question and the leader name.
func (ai *AIService) GenerateLeaderChallenge(
	stageID string,
	responsesSummary string,
	userIdea string,
	leaderName string,
	leaderSpecialization string,
) (string, error) {

	systemPrompt := fmt.Sprintf(`You are %s, a business leader and evaluator in KK's War Room 2.0 simulation.
Your specialization: %s

You have just reviewed a founder's decisions during the %s stage of their startup journey.
Your job is to ask ONE pointed, challenging follow-up question based on their actual decisions.

RULES:
- Ask exactly ONE question — no preamble, no multiple questions
- The question must directly challenge or probe a SPECIFIC decision they made
- Be direct, analytical, and pressure-testing (not aggressive)
- The question should force them to defend their reasoning
- Keep the question under 2 sentences
- Do NOT use generic business clichés
- Only output the question itself, nothing else`, leaderName, leaderSpecialization, stageID)

	userPrompt := fmt.Sprintf(`Founder's business idea: %s

Their decisions this phase:
%s

Based on these specific decisions, ask ONE challenging follow-up question.`, userIdea, responsesSummary)

	messages := []ChatMessage{
		{Role: "system", Content: systemPrompt},
		{Role: "user", Content: userPrompt},
	}

	resp, err := ai.Call(messages)
	if err != nil {
		return fmt.Sprintf("Given your decisions this phase, how would you handle a major unexpected setback that challenges your core assumptions about %s?", stageID), nil
	}

	// Clean up the response (remove quotes if AI wrapped it)
	question := strings.TrimSpace(resp.Content)
	question = strings.Trim(question, `"'`)
	return question, nil
}

// ============================================
// GENERATE DETAILED POST-SIMULATION ANALYSIS
// ============================================

// GenerateDetailedAnalysis creates a comprehensive AI report analyzing the participant's
// GenerateDetailedAnalysis produces a multi-section narrative analysis of the
// entire simulation journey — what went well, what went wrong, and improvement areas.
func (ai *AIService) GenerateDetailedAnalysis(
	competencyRanking []map[string]interface{},
	responseSummaries string,
	investorSummary string,
	entrepreneurType string,
	roleFit string,
	userIdea string,
	simState map[string]interface{},
) (string, error) {

	systemPrompt := `You are an expert business coach and venture capital partner evaluating a founder's performance in "KK's War Room 2.0".
You have reviewed a founder's complete simulation journey — every decision, investor interaction, competency score, and final business state.

Generate a HIGH-LEVEL STRATEGIC EVALUATION report in the following format. Use clear headings and bullet points.

## Executive Summary
- Brief 2-3 sentence overview of the founder's overall performance and business viability.

## What You Did Well (Strategic Strengths)
- 3-5 specific strengths based on their decisions, scores, and business outcomes.
- Reference specific metrics (Revenue, Team growth, Product stage) if they confirm the success.

## Critical Mistakes & Blind Spots
- 3-5 specific mistakes referencing actual decisions or poor metric outcomes (e.g., high expenses, poor capital management).
- Be brutally honest but growth-oriented.

## Financial & Growth Evaluation
- Analyze their final business state: Revenue vs Expenses, Capital remaining, and the quality of their Budget Allocations.
- Was the business model sustainable by the end?

## Competency Deep Dive (The Gaps)
- For each competency scored below 2.5, explain WHY it was low based on their specific responses and decision patterns.

## Role Fit & Next Steps
- Explain why the identified role (e.g., CEO, COO, Product) matches their decision patterns.
- Suggest 3 concrete real-world actions to bridge their largest competency gap.

## Final Verdict
- A closing statement on whether this founder is "Investor Ready", "Growth Ready", or "Needs Co-founder Support".

RULES:
- Be hyper-specific. Reference actual decisions, investor reactions (e.g. "Kevin O'Leary walked out because..."), and final metrics.
- Use the second person ("You").
- Use professional yet encouraging "Venture Partner" tone.
- Total length: 700-1000 words.
- Format with clean Markdown.`

	stateJSON, _ := json.MarshalIndent(simState, "", "  ")

	userPrompt := fmt.Sprintf(`Founder's Business Idea: %s
Entrepreneur Type: %s
Recommended Role: %s

Final Business State (JSON):
%s

Competency Rankings:
%s

Key Decisions & Responses:
%s

Investor Interactions:
%s

Based on all this data, provide a comprehensive evaluation.`, userIdea, entrepreneurType, roleFit, string(stateJSON), formatRankings(competencyRanking), responseSummaries, investorSummary)

	messages := []ChatMessage{
		{Role: "system", Content: systemPrompt},
		{Role: "user", Content: userPrompt},
	}

	resp, err := ai.Call(messages)
	if err != nil {
		return "Detailed analysis could not be generated at this time.", err
	}

	return strings.TrimSpace(resp.Content), nil
}

// ============================================
// GENERATE DYNAMIC SCENARIO (MCQ)
// ============================================

type DynamicScenarioResponse struct {
	Question string `json:"question"`
	Options  []struct {
		Text        string      `json:"text"`
		Proficiency interface{} `json:"proficiency"` // 1, 2, or 3 (can be string or int)
		Feedback    string      `json:"feedback"`
	} `json:"options"`
}

func (ai *AIService) GenerateDynamicScenario(
	questionContext string,
	stageGoal string,
	researchBackground string,
	competencies []string,
	competencyDefinitions map[string]CompetencyDef,
	previousResponses string,
	userIdea string,
	leaderName string,
	domain string,
	isTechnical bool,
	level int,
	isFirstScenarioInPhase bool,
) (*DynamicScenarioResponse, error) {

	// Build competency context
	compContext := ""
	for _, code := range competencies {
		if def, ok := competencyDefinitions[code]; ok {
			compContext += fmt.Sprintf("\n%s - %s:\n  Developing (P1): %s\n  Strong (P2): %s\n  Advanced (P3): %s\n",
				code, def.Name,
				strings.Join(def.Developing, "; "),
				strings.Join(def.Strong, "; "),
				strings.Join(def.Advanced, "; "),
			)
		}
	}

	previousResponses = truncateForLog(previousResponses, 2400)

	var scenario DynamicScenarioResponse
	var lastParseErr error

	// Interpret level and technical context
	levelContext := "Professional business manager level. Use standard professional terminology."
	if level == 1 {
		levelContext = "Student/Entry level. Scale down the complexity of the business jargon to a school/college level. The leader's tone should be more of a friendly mentor or teacher while still posing a realistic challenge."
	}

	domainContext := "General Business"
	if domain != "" {
		domainContext = domain
	}
	if !isTechnical {
		domainContext += " (Non-Technical Focus - focus on operations, marketing, sales, or physical product, not deep software/tech engineering)"
	} else {
		domainContext += " (Technical Focus - suitable for software, deep tech, AI, etc.)"
	}

	firstScenarioInstruction := ""
	if isFirstScenarioInPhase {
		firstScenarioInstruction = "CRITICAL: Since this is the FIRST scenario of the phase, you MUST base the challenge on a REAL-WORLD CASE STUDY. Describe a scenario that actually happened to a well-known company in their specific domain (" + domainContext + "), and ask the user 'What would you do if it is your case?' or 'How would you navigate this scenario in your startup?'."
	}

	for attempt := 1; attempt <= 3; attempt++ {
		systemPrompt := fmt.Sprintf(`You are an expert business simulation designer for KK's War Room 2.0.
Your task is to generate a REAL-WORLD SIMULATION SCENARIO based on the participant's business journey.

SCENARIO SLOT:
%s

CURRENT STAGE GOAL:
%s

RESEARCH BACKGROUND:
%s

COMPETENCIES TO ANALYZE:
%s

PARTICIPANT'S BUSINESS IDEA:
%s

DOMAIN / INDUSTRY FOCUS:
%s

DIFFICULTY / TONE LEVEL:
%s

PREVIOUS DECISIONS SUMMARY:
%s

RULES:
1. Generate ONE challenging, realistic business scenario related to the current stage goal that directly resulted from the participant's previous decisions.
2. The scenario MUST be presented as being asked directly by the leader: %s, speaking in their authentic voice and tone.
3. EXPLICITLY reference their specific past decisions and overall performance from previous phases in the scenario description. Do NOT use a generic prefix. The leader should naturally weave the consequences of the user's actual past decisions into the new challenge.
4. %s
5. Provide exactly FOUR options.
6. Each option MUST include proficiency 1, 2, or 3 (at least one option for each level across all four).
7. Keep output concise: question <= 400 chars, each option text <= 140 chars, each feedback <= 120 chars.
8. Return ONLY valid, complete, and compact JSON that directly represents the DynamicScenarioResponse structure. No markdown, no code fences, no extra text, no wrapping JSON objects.

EXAMPLE OF EXPECTED JSON OUTPUT:
{
  "question": "A critical challenge has emerged...",
  "options": [
    {
      "text": "Option 1 Text",
      "proficiency": 3,
      "feedback": "Feedback for option 1"
    },
    {
      "text": "Option 2 Text",
      "proficiency": 2,
      "feedback": "Feedback for option 2"
    },
    {
      "text": "Option 3 Text",
      "proficiency": 1,
      "feedback": "Feedback for option 3"
    },
    {
      "text": "Option 4 Text",
      "proficiency": 2,
      "feedback": "Feedback for option 4"
    }
  ]
}`, questionContext, stageGoal, researchBackground, compContext, userIdea, domainContext, levelContext, previousResponses, leaderName, firstScenarioInstruction)

		userPrompt := "Return ONLY the JSON object as specified in the rules and example."
		if attempt > 1 {
			userPrompt = "Previous output was malformed or truncated. Return ONLY the complete and valid JSON object for DynamicScenarioResponse. No markdown, no backticks, no extra text, no wrapping JSON objects."
		}

		messages := []ChatMessage{
			{Role: "system", Content: systemPrompt},
			{Role: "user", Content: userPrompt},
		}

		resp, err := ai.Call(messages)
		if err != nil {
			return nil, err
		}

		scenario, lastParseErr = parseDynamicScenarioResponse(resp.Content)
		if lastParseErr == nil {
			break
		}

		log.Printf("[AIService] Dynamic scenario parse attempt %d failed: %v", attempt, lastParseErr)
		if attempt == 3 {
			return nil, lastParseErr
		}
	}

	// Validation: Ensure we have options
	if len(scenario.Options) == 0 {
		return nil, fmt.Errorf("AI generated zero options")
	}
	if len(scenario.Options) != 4 {
		return nil, fmt.Errorf("AI must generate exactly 4 options, got %d", len(scenario.Options))
	}

	for i := range scenario.Options {
		// Convert interface{} proficiency to int safely
		profInt := 2 // Default to 2
		switch v := scenario.Options[i].Proficiency.(type) {
		case float64:
			profInt = int(v)
		case string:
			if strings.Contains(v, "1") {
				profInt = 1
			}
			if strings.Contains(v, "3") {
				profInt = 3
			}
		case int:
			profInt = v
		}

		// Clamp
		if profInt < 1 {
			profInt = 1
		}
		if profInt > 3 {
			profInt = 3
		}

		// Update back the modified struct to int
		scenario.Options[i].Proficiency = profInt
	}

	return &scenario, nil
}

func parseDynamicScenarioResponse(content string) (DynamicScenarioResponse, error) {
	var scenario DynamicScenarioResponse

	cleaned := stripMarkdownCodeBlocks(content)

	// Find the outermost JSON object by balancing curly braces
	startIndex := -1
	endIndex := -1
	braceCount := 0

	for i, r := range cleaned {
		if r == '{' {
			if startIndex == -1 {
				startIndex = i // Found the first opening brace
			}
			braceCount++
		} else if r == '}' {
			braceCount--
			if braceCount == 0 && startIndex != -1 {
				endIndex = i // Found the matching closing brace for the first opening one
				break
			}
		}
	}

	if startIndex == -1 || endIndex == -1 {
		return scenario, fmt.Errorf("unstructured AI response: no complete JSON object found: %s", truncateForLog(cleaned, 200))
	}

	jsonStr := cleaned[startIndex : endIndex+1]
	if err := json.Unmarshal([]byte(jsonStr), &scenario); err != nil {
		return scenario, fmt.Errorf("failed to parse AI scenario JSON: %w for content: %s", err, truncateForLog(jsonStr, 200))
	}

	return scenario, nil
}

func formatRankings(rankings []map[string]interface{}) string {
	var lines []string
	for _, r := range rankings {
		lines = append(lines, fmt.Sprintf("- %s (%s): %.2f — %s",
			r["code"], r["name"], r["weightedAverage"], r["category"]))
	}
	return strings.Join(lines, "\n")
}

func stripMarkdownCodeBlocks(content string) string {
	// Look for ```json...``` or ```...``` blocks
	jsonStart := strings.Index(content, "```json")
	if jsonStart >= 0 {
		// Found markdown json block
		endIdx := strings.Index(content[jsonStart+7:], "```")
		if endIdx >= 0 {
			// Extract content between ```json and ```
			return strings.TrimSpace(content[jsonStart+7 : jsonStart+7+endIdx])
		}
		// No closing fence, strip prefix and continue
		return strings.TrimSpace(content[jsonStart+7:])
	}

	// Look for generic markdown blocks
	if strings.Contains(content, "```") {
		start := strings.Index(content, "```")
		if start >= 0 {
			end := strings.Index(content[start+3:], "```")
			if end >= 0 {
				return strings.TrimSpace(content[start+3 : start+3+end])
			}
			// No closing fence, strip opening fence and return remainder
			return strings.TrimSpace(content[start+3:])
		}
	}

	return strings.TrimSpace(content)
}

func truncateForLog(s string, maxLen int) string {
	if len(s) > maxLen {
		return s[:maxLen] + "..."
	}
	return s
}
