package services

import (
	"encoding/json"
	"fmt"
	"log"
	"strings"
)

// ============================================
// DEMO SERVICE - AI-powered demo flow
// No auth required, uses existing AIService
// ============================================

type DemoService struct {
	AI *AIService
}

func NewDemoService(ai *AIService) *DemoService {
	return &DemoService{AI: ai}
}

// ============================================
// TYPES
// ============================================

type DemoScenarioOption struct {
	ID       string `json:"id"`
	Text     string `json:"text"`
	Feedback string `json:"feedback"`
}

type DemoScenario struct {
	Question string               `json:"question"`
	Context  string               `json:"context"`
	Options  []DemoScenarioOption `json:"options"`
}

type DemoEvaluation struct {
	Score      int      `json:"score"` // 1-10
	Feedback   string   `json:"feedback"`
	Strengths  []string `json:"strengths"`
	Weaknesses []string `json:"weaknesses"`
}

type DemoVoiceEvaluation struct {
	Transcription string   `json:"transcription"`
	Score         int      `json:"score"` // 1-10
	Feedback      string   `json:"feedback"`
	Strengths     []string `json:"strengths"`
	Weaknesses    []string `json:"weaknesses"`
	Clarity       int      `json:"clarity"`    // 1-5
	Confidence    int      `json:"confidence"` // 1-5
}

// ============================================
// FOLLOW-UP SCENARIO TYPE
// ============================================

type DemoFollowupScenario struct {
	Question string `json:"question"`
	Context  string `json:"context"`
}

type CompetencyScore struct {
	Trait string `json:"trait"`
	Score int    `json:"score"`
}

type CompetencyReport struct {
	OverallScore int               `json:"overallScore"`
	Summary      string            `json:"summary"`
	Competencies []CompetencyScore `json:"competencies"`
}

// ============================================
// GENERATE FOLLOW-UP SCENARIO
// ============================================

func (s *DemoService) GenerateFollowupScenario(introduction string, originalQuestion string, selectedOptionText string, selectedOptionFeedback string, roundNumber int) (*DemoFollowupScenario, error) {
	systemPrompt := fmt.Sprintf(`You are an expert business simulation designer for KK's War Room — a startup simulation platform.

A demo user chose a specific action in response to a business scenario. Your job is to generate ONE follow-up scenario that is a DIRECT CONSEQUENCE of their choice.

This is the follow-up for round %d of 3.

RULES:
1. The follow-up MUST be a realistic consequence that DIRECTLY stems from the option they chose
2. It should explore a deeper trade-off, complication, or second-order effect of their decision
3. Write in second person ("Because you decided to... now you face...")
4. Start by acknowledging their choice, then present the new challenge
5. End with a clear question like "How do you handle this?" or "What's your next move?"
6. Keep the scenario under 350 characters
7. The context should be a brief 1-line description of what skill this follow-up tests

You MUST respond in valid JSON:
{
  "question": "<follow-up scenario that stems from their choice>",
  "context": "<brief 1-line context about which business skill this tests>"
}`, roundNumber)

	userPrompt := fmt.Sprintf(`BUSINESS IDEA: %s

ORIGINAL SCENARIO: %s

OPTION THE USER CHOSE: %s

CONSEQUENCE OF THAT CHOICE: %s

Generate a follow-up scenario that explores the deeper consequences and trade-offs of this specific choice.`, introduction, originalQuestion, selectedOptionText, selectedOptionFeedback)

	messages := []ChatMessage{
		{Role: "system", Content: systemPrompt},
		{Role: "user", Content: userPrompt},
	}

	var followup DemoFollowupScenario
	var lastErr error

	for attempt := 1; attempt <= 3; attempt++ {
		resp, err := s.AI.Call(messages)
		if err != nil {
			lastErr = err
			log.Printf("[DemoService] GenerateFollowupScenario attempt %d failed: %v", attempt, err)
			continue
		}

		content := resp.Content
		cleaned := stripMarkdownCodeBlocks(content)

		start := strings.Index(cleaned, "{")
		end := strings.LastIndex(cleaned, "}")
		if start >= 0 && end > start {
			if err := json.Unmarshal([]byte(cleaned[start:end+1]), &followup); err != nil {
				lastErr = fmt.Errorf("parse error: %w", err)
				log.Printf("[DemoService] GenerateFollowupScenario parse attempt %d: %v", attempt, err)
				continue
			}
			break
		}
		lastErr = fmt.Errorf("no JSON found in response")
	}

	if lastErr != nil && followup.Question == "" {
		return nil, fmt.Errorf("failed to generate follow-up scenario after 3 attempts: %w", lastErr)
	}

	return &followup, nil
}

// ============================================
// GENERATE PITCH SCENARIO
// ============================================

func (s *DemoService) GeneratePitchScenario(introduction string) (*DemoFollowupScenario, error) {
	systemPrompt := `You are an expert investor and pitch coach for KK's War Room.
Based on the startup idea, generate a specific, high-pressure pitching scenario.
The user will have to deliver an elevator pitch in response.

RULES:
1. Set the scene (e.g., "You're in an elevator with a top Tier-1 VC..." or "You're on stage at Demo Day...")
2. The scenario should end with a clear prompt like "What's your 60-second pitch?" or "They ask: 'Give me the quick pitch, why should I care?'"
3. Keep it under 350 characters.
4. The context should be "Pitching & Communication".

You MUST respond in valid JSON:
{
  "question": "<pitch scenario description>",
  "context": "Pitching & Communication"
}`

	userPrompt := fmt.Sprintf("BUSINESS IDEA: %s", introduction)

	messages := []ChatMessage{
		{Role: "system", Content: systemPrompt},
		{Role: "user", Content: userPrompt},
	}

	var pitch DemoFollowupScenario
	resp, err := s.AI.Call(messages)
	if err != nil {
		return nil, err
	}

	content := stripMarkdownCodeBlocks(resp.Content)
	if err := json.Unmarshal([]byte(content), &pitch); err != nil {
		return nil, err
	}

	return &pitch, nil
}

// ============================================
// GENERATE PITCH Q&A SCENARIO
// ============================================

func (s *DemoService) GeneratePitchQnAScenario(introduction, pitchResponse string, round int) (*DemoFollowupScenario, error) {
	focus := "Market & Customer Validation"
	if round == 2 {
		focus = "Monetization & Scalability"
	}
	systemPrompt := fmt.Sprintf(`You are an expert investor for KK's War Room.
The founder just pitched. Ask a tough, realistic follow-up question.
This is Question %d, focus on: %s.

RULES:
1. Ask the question directly as the investor.
2. Keep it under 250 characters.
3. The context must summarize what the question tests (e.g. "Defending Market Strategy").

You MUST respond in valid JSON:
{
  "question": "<investor's question>",
  "context": "<brief description of skill tested>"
}`, round, focus)

	userPrompt := fmt.Sprintf("BUSINESS IDEA: %s\n\nFOUNDER'S PITCH: %s", introduction, pitchResponse)

	messages := []ChatMessage{
		{Role: "system", Content: systemPrompt},
		{Role: "user", Content: userPrompt},
	}

	var qna DemoFollowupScenario
	resp, err := s.AI.Call(messages)
	if err != nil {
		return nil, err
	}

	content := stripMarkdownCodeBlocks(resp.Content)
	if err := json.Unmarshal([]byte(content), &qna); err != nil {
		return nil, err
	}

	return &qna, nil
}

// ============================================
// GENERATE NEGOTIATION SCENARIO
// ============================================

func (s *DemoService) GenerateNegotiationScenario(introduction, pitchResponse string, round int, previousContext string) (*DemoFollowupScenario, error) {

	var systemPrompt string
	var userPrompt string

	if round == 1 {
		systemPrompt = `You are a tough but fair investor negotiating a deal for KK's War Room.
The founder has just pitched their idea to you. You are interested but want to push for a specific deal.

RULES:
1. Respond to their pitch briefly ("I like the vision, but $10M is too high...")
2. Present a specific deal offer (e.g., "$250k for 15% equity", "We'll do $500k but only if you hit X milestone first")
3. End with a question about their counter-offer or reasoning.
4. Keep it under 400 characters.
5. The context should be "Negotiation & Deal-making".

You MUST respond in valid JSON:
{
  "question": "<investor's response and deal offer>",
  "context": "Negotiation & Deal-making"
}`
		userPrompt = fmt.Sprintf("BUSINESS IDEA: %s\n\nFOUNDER'S PITCH: %s", introduction, pitchResponse)
	} else {
		systemPrompt = `You are a tough but fair investor negotiating a deal for KK's War Room.
The founder has just countered your initial offer. Respond to their counter-offer.

RULES:
1. React to their counter-offer logically (accept with a condition, reject and hold firm, or meet in the middle).
2. End with a final stance or question.
3. Keep it under 400 characters.
4. The context should be "Counter-Negotiation".

You MUST respond in valid JSON:
{
  "question": "<investor's response to the counter-offer>",
  "context": "Counter-Negotiation"
}`
		userPrompt = fmt.Sprintf("BUSINESS IDEA: %s\n\nPREVIOUS NEGOTIATION:\n%s", introduction, previousContext)
	}

	messages := []ChatMessage{
		{Role: "system", Content: systemPrompt},
		{Role: "user", Content: userPrompt},
	}

	var negotiation DemoFollowupScenario
	resp, err := s.AI.Call(messages)
	if err != nil {
		return nil, err
	}

	content := stripMarkdownCodeBlocks(resp.Content)
	if err := json.Unmarshal([]byte(content), &negotiation); err != nil {
		return nil, err
	}

	return &negotiation, nil
}

// ============================================
// GENERATE SCENARIO
// ============================================

type rawDemoScenario struct {
	Question string `json:"question"`
	Context  string `json:"context"`
	Options  []struct {
		ID       string `json:"id"`
		Text     string `json:"text"`
		Feedback string `json:"feedback"`
	} `json:"options"`
}

func (s *DemoService) GenerateScenario(introduction string, roundNumber int, previousScenarios string) (*DemoScenario, error) {
	systemPrompt := fmt.Sprintf(`You are an expert business simulation designer for KK's War Room — a startup simulation platform.

A demo user has just described what they're building. Your job is to generate ONE realistic, challenging business scenario question based on their business idea.

This is round %d of 3. Each round should test a DIFFERENT aspect of entrepreneurship:
- Round 1: Market & Customer challenge (product-market fit, customer acquisition, competition)
- Round 2: Financial & Operations challenge (cash flow, budgeting, scaling operations)
- Round 3: Crisis & Leadership challenge (team conflict, PR crisis, pivoting under pressure)

RULES:
1. The scenario MUST be directly related to the user's specific business idea
2. Present a realistic pressure situation that a founder would face
3. Write the scenario in second person ("You discover that..." / "Your biggest client...")
4. End with a clear question like "How do you handle this?" or "What's your next move?"
5. Provide exactly 4 response options of varying quality
6. Each option should have realistic feedback explaining the consequence
7. Keep the scenario under 300 characters, options under 150 chars each, feedback under 120 chars each

%s

You MUST respond in valid JSON:
{
  "question": "<scenario description ending with a question>",
  "context": "<brief 1-line context about which business skill this tests>",
  "options": [
    {"id": "A", "text": "<option text>", "feedback": "<consequence/feedback>"},
    {"id": "B", "text": "<option text>", "feedback": "<consequence/feedback>"},
    {"id": "C", "text": "<option text>", "feedback": "<consequence/feedback>"},
    {"id": "D", "text": "<option text>", "feedback": "<consequence/feedback>"}
  ]
}`, roundNumber, func() string {
		if previousScenarios != "" {
			return fmt.Sprintf("PREVIOUS SCENARIOS (do NOT repeat similar themes):\n%s", previousScenarios)
		}
		return ""
	}())

	userPrompt := fmt.Sprintf("The user's business idea:\n\n%s\n\nGenerate scenario round %d.", introduction, roundNumber)

	messages := []ChatMessage{
		{Role: "system", Content: systemPrompt},
		{Role: "user", Content: userPrompt},
	}

	var scenario rawDemoScenario
	var lastErr error

	for attempt := 1; attempt <= 3; attempt++ {
		resp, err := s.AI.Call(messages)
		if err != nil {
			lastErr = err
			log.Printf("[DemoService] GenerateScenario attempt %d failed: %v", attempt, err)
			continue
		}

		content := resp.Content
		cleaned := stripMarkdownCodeBlocks(content)

		// Find JSON
		start := strings.Index(cleaned, "{")
		end := strings.LastIndex(cleaned, "}")
		if start >= 0 && end > start {
			if err := json.Unmarshal([]byte(cleaned[start:end+1]), &scenario); err != nil {
				lastErr = fmt.Errorf("parse error: %w", err)
				log.Printf("[DemoService] GenerateScenario parse attempt %d: %v", attempt, err)
				continue
			}
			break
		}
		lastErr = fmt.Errorf("no JSON found in response")
	}

	if lastErr != nil && scenario.Question == "" {
		return nil, fmt.Errorf("failed to generate scenario after 3 attempts: %w", lastErr)
	}

	// Validate
	if len(scenario.Options) != 4 {
		return nil, fmt.Errorf("AI generated %d options instead of 4", len(scenario.Options))
	}

	result := &DemoScenario{
		Question: scenario.Question,
		Context:  scenario.Context,
		Options:  make([]DemoScenarioOption, len(scenario.Options)),
	}
	for i, o := range scenario.Options {
		result.Options[i] = DemoScenarioOption{
			ID:       o.ID,
			Text:     o.Text,
			Feedback: o.Feedback,
		}
	}

	return result, nil
}

// ============================================
// EVALUATE TEXT RESPONSE
// ============================================

func (s *DemoService) EvaluateTextResponse(introduction, question, response string) (*DemoEvaluation, error) {
	systemPrompt := `You are an expert business mentor evaluating a founder's response to a business scenario in KK's War Room simulation.

Evaluate their response considering:
- Strategic thinking: Did they consider multiple angles?
- Practicality: Is their approach realistic and actionable?
- Risk awareness: Did they account for potential downsides?
- Leadership: Does their response show leadership qualities?
- Business acumen: Do they understand the business implications?

You MUST respond in valid JSON:
{
  "score": <1-10 overall score>,
  "feedback": "<2-3 sentence assessment of their response>",
  "strengths": ["<strength1>", "<strength2>"],
  "weaknesses": ["<weakness1>", "<weakness2>"]
}

Be constructive but honest. Reference their specific response, not generic advice.`

	userPrompt := fmt.Sprintf(`BUSINESS IDEA: %s

SCENARIO QUESTION: %s

FOUNDER'S RESPONSE: %s

Evaluate this response.`, introduction, question, response)

	messages := []ChatMessage{
		{Role: "system", Content: systemPrompt},
		{Role: "user", Content: userPrompt},
	}

	resp, err := s.AI.Call(messages)
	if err != nil {
		log.Printf("[DemoService] EvaluateTextResponse failed: %v", err)
		return &DemoEvaluation{
			Score:      5,
			Feedback:   "Unable to evaluate response at this time.",
			Strengths:  []string{},
			Weaknesses: []string{},
		}, nil
	}

	var eval DemoEvaluation
	content := resp.Content
	start := strings.Index(content, "{")
	end := strings.LastIndex(content, "}")
	if start >= 0 && end > start {
		if err := json.Unmarshal([]byte(content[start:end+1]), &eval); err != nil {
			log.Printf("[DemoService] EvaluateTextResponse parse error: %v", err)
			return &DemoEvaluation{
				Score:    5,
				Feedback: content,
			}, nil
		}
	}

	// Clamp score
	if eval.Score < 1 {
		eval.Score = 1
	}
	if eval.Score > 10 {
		eval.Score = 10
	}

	return &eval, nil
}

// ============================================
// EVALUATE VOICE RESPONSE
// ============================================

func (s *DemoService) EvaluateVoiceResponse(introduction, question, audioBase64, mimeType string) (*DemoVoiceEvaluation, error) {
	systemPrompt := fmt.Sprintf(`You are an expert business mentor evaluating a founder's spoken response to a business scenario in KK's War Room simulation.

BUSINESS CONTEXT: %s

SCENARIO: %s

Your tasks:
1. Transcribe the spoken audio accurately
2. Evaluate the quality of their response
3. Assess their vocal delivery (clarity, confidence)

You MUST respond in valid JSON:
{
  "transcription": "<exact transcription of what was said>",
  "score": <1-10 overall score>,
  "feedback": "<2-3 sentence assessment>",
  "strengths": ["<strength1>", "<strength2>"],
  "weaknesses": ["<weakness1>"],
  "clarity": <1-5 speech clarity>,
  "confidence": <1-5 vocal confidence>
}

EVALUATION CRITERIA:
- Strategic thinking and business acumen
- Clarity and structure of the response
- Confidence and conviction in delivery
- Practicality and actionability

If the audio is silent or too short, set transcription to "[No speech detected]" and all scores to 1.
DO NOT invent or hallucinate speech content.`, introduction, question)

	userText := "Listen to this founder's response and evaluate it."

	resp, err := s.AI.CallWithAudio(systemPrompt, userText, audioBase64, mimeType)
	if err != nil {
		log.Printf("[DemoService] EvaluateVoiceResponse failed: %v", err)
		return &DemoVoiceEvaluation{
			Transcription: "[Audio analysis failed]",
			Score:         5,
			Feedback:      "Unable to analyze audio at this time.",
			Strengths:     []string{},
			Weaknesses:    []string{},
			Clarity:       3,
			Confidence:    3,
		}, nil
	}

	var eval DemoVoiceEvaluation
	content := resp.Content
	start := strings.Index(content, "{")
	end := strings.LastIndex(content, "}")
	if start >= 0 && end > start {
		json.Unmarshal([]byte(content[start:end+1]), &eval)
	}

	// Clamp scores
	if eval.Score < 1 {
		eval.Score = 1
	}
	if eval.Score > 10 {
		eval.Score = 10
	}
	if eval.Clarity < 1 {
		eval.Clarity = 1
	}
	if eval.Clarity > 5 {
		eval.Clarity = 5
	}
	if eval.Confidence < 1 {
		eval.Confidence = 1
	}
	if eval.Confidence > 5 {
		eval.Confidence = 5
	}

	return &eval, nil
}

// ============================================
// GENERATE COMPETENCY REPORT
// ============================================

func (s *DemoService) GenerateCompetencyReport(summary string) (*CompetencyReport, error) {
	systemPrompt := `You are an expert startup assessor for KK's War Room.
Based on the transcript of a founder's performance across various simulation stages, evaluate their core competencies.

There are exactly 5 competencies to evaluate:
"Problem Sensing", "Financial Discipline", "Strategic Thinking", "Power & Influence", "Value Creation".

For each, provide a score from 1-10.
Calculate the overall average score (1-10).
Provide a 2-3 sentence overall summary of their founder profile.

You MUST respond in valid JSON:
{
  "overallScore": 8,
  "summary": "<2-3 sentence breakdown>",
  "competencies": [
    {"trait": "Problem Sensing", "score": 8},
    {"trait": "Financial Discipline", "score": 7},
    {"trait": "Strategic Thinking", "score": 9},
    {"trait": "Power & Influence", "score": 6},
    {"trait": "Value Creation", "score": 8}
  ]
}`

	userPrompt := fmt.Sprintf("FOUNDER PERFORMANCE SUMMARY:\n\n%s", summary)

	messages := []ChatMessage{
		{Role: "system", Content: systemPrompt},
		{Role: "user", Content: userPrompt},
	}

	resp, err := s.AI.Call(messages)
	if err != nil {
		log.Printf("[DemoService] GenerateCompetencyReport failed: %v", err)
		return nil, err
	}

	var report CompetencyReport
	content := stripMarkdownCodeBlocks(resp.Content)
	if err := json.Unmarshal([]byte(content), &report); err != nil {
		return nil, err
	}

	return &report, nil
}

