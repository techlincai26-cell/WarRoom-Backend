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
)

// ============================================
// AI SERVICE - Gemini Integration
// ============================================

type AIService struct {
	APIKey string
	Model  string
}

func NewAIService() *AIService {
	apiKey := os.Getenv("GEMINI_API_KEY")

	model := os.Getenv("AI_MODEL")
	if model == "" {
		model = "gemini-2.0-flash-lite-preview-02-05"
	}

	log.Printf("[AIService] Initialized with model: %s, API key set: %v", model, apiKey != "")

	return &AIService{
		APIKey: apiKey,
		Model:  model,
	}
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

type geminiPart struct {
	Text string `json:"text"`
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
		// Fallback mock when no API key
		return &AIResponse{Content: `{"proficiency": 2, "feedback": "Good response demonstrating foundational understanding.", "reasoning": "Response shows awareness of key concepts."}`}, nil
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
			"maxOutputTokens": 1024,
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

	req, err := http.NewRequest("POST", url, bytes.NewBuffer(body))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("API request failed: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		log.Printf("[AIService] Gemini API error %d: %s", resp.StatusCode, string(respBody))
		return nil, fmt.Errorf("Gemini API error %d: %s", resp.StatusCode, string(respBody))
	}

	var result geminiResponse
	if err := json.Unmarshal(respBody, &result); err != nil {
		return nil, fmt.Errorf("failed to parse response: %w", err)
	}

	if result.Error != nil {
		return nil, fmt.Errorf("Gemini error: %s", result.Error.Message)
	}

	if len(result.Candidates) == 0 || len(result.Candidates[0].Content.Parts) == 0 {
		return nil, fmt.Errorf("no content in Gemini response")
	}

	content := result.Candidates[0].Content.Parts[0].Text
	return &AIResponse{Content: content}, nil
}

// ============================================
// EVALUATE OPEN TEXT RESPONSE
// ============================================

type TextEvaluation struct {
	Proficiency int      `json:"proficiency"` // 1=P1, 2=P2, 3=P3
	Feedback    string   `json:"feedback"`
	Reasoning   string   `json:"reasoning"`
	Strengths   []string `json:"strengths"`
	Weaknesses  []string `json:"weaknesses"`
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
- P1 (Developing/1): The competency is present but inconsistent under pressure. Requires focused development.
- P2 (Strong/2): The competency is reliable and functional in most business situations.
- P3 (Advanced/3): The competency is consistently demonstrated under pressure and supports scalable leadership.

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
// entire simulation journey — what went well, what went wrong, and improvement areas.
func (ai *AIService) GenerateDetailedAnalysis(
	competencyRanking []map[string]interface{},
	responseSummaries string,
	investorSummary string,
	entrepreneurType string,
	roleFit string,
	userIdea string,
) (string, error) {

	systemPrompt := `You are an expert business coach and entrepreneurship evaluator for KK's War Room 2.0 simulation.
You have reviewed a founder's complete simulation journey — every decision, investor interaction, and competency score.

Generate a DETAILED evaluation report in the following format. Use clear headings and bullet points.

## What You Did Well
- 3-5 specific strengths based on their decisions and scores

## What Went Wrong
- 3-5 specific mistakes or weak areas, referencing actual decisions they made

## What Could Have Been Done Better
- 3-5 actionable improvements with concrete examples

## Competency Deep Dive
- For each competency scored below 2.5, explain WHY it was low based on their responses

## Role Fit Analysis
- Explain why the identified role/archetype matches their decision patterns
- Suggest the best organizational environment for them

## Key Takeaways
- 3 most important lessons from their simulation

RULES:
- Be specific and reference actual decisions, not generic advice
- Be constructive but honest — point out real weaknesses
- Use the competency data to support your analysis
- Keep it under 800 words total
- Write in second person ("You demonstrated...", "Your decision to...")`

	userPrompt := fmt.Sprintf(`Founder's Business Idea: %s
Entrepreneur Type: %s
Recommended Role: %s

Competency Rankings:
%s

Key Decisions & Responses:
%s

Investor Interactions:
%s

Based on all this data, provide a comprehensive evaluation.`, userIdea, entrepreneurType, roleFit, formatRankings(competencyRanking), responseSummaries, investorSummary)

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

func formatRankings(rankings []map[string]interface{}) string {
	var lines []string
	for _, r := range rankings {
		lines = append(lines, fmt.Sprintf("- %s (%s): %.2f — %s",
			r["code"], r["name"], r["weightedAverage"], r["category"]))
	}
	return strings.Join(lines, "\n")
}
