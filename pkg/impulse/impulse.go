// Package impulse implements the GitHub webhook impulse logic.
package impulse

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	sdk "github.com/bubustack/bubu-sdk-go"
	sdkcel "github.com/bubustack/bubu-sdk-go/cel"
	sdkengram "github.com/bubustack/bubu-sdk-go/engram"
	sdkk8s "github.com/bubustack/bubu-sdk-go/k8s"

	cfgpkg "github.com/bubustack/github-webhook-impulse/pkg/config"
)

const (
	githubEventPush        = "push"
	githubEventPullRequest = "pull_request"
)

// GitHubImpulse handles GitHub webhook events and submits StoryTrigger requests.
type GitHubImpulse struct {
	cfg           cfgpkg.Config
	webhookSecret string
	dispatcher    *sdk.StoryDispatcher
	evaluator     *sdkcel.Evaluator
	logger        *slog.Logger
}

// New creates a new GitHubImpulse instance.
func New() *GitHubImpulse {
	return &GitHubImpulse{
		logger: slog.New(slog.NewJSONHandler(os.Stdout, nil)),
	}
}

type webhookRequest struct {
	body       []byte
	eventType  string
	deliveryID string
	signature  string
	action     string
	repository string
	sender     string
	payload    map[string]any
}

// Init initializes the GitHub impulse with configuration and secrets.
func (i *GitHubImpulse) Init(ctx context.Context, cfg cfgpkg.Config, secrets *sdkengram.Secrets) error {
	logger := i.loggerWithContext()

	// Verify target story is configured via Impulse.spec.storyRef
	targetStory, err := sdk.GetTargetStory()
	if err != nil {
		return fmt.Errorf("failed to get target story from environment: %w", err)
	}
	logger.Info("Target story resolved",
		slog.String("name", targetStory.Name),
		slog.String("namespace", targetStory.Namespace))

	i.cfg = cfg

	// Load webhook secret if provided
	if secrets != nil {
		if secret, ok := secrets.Get("WEBHOOK_SECRET"); ok {
			i.webhookSecret = secret
			logger.Info("Webhook secret loaded for signature validation")
		}
	}

	// Warn if signature validation enabled but no secret
	if i.cfg.ValidateSignature && i.webhookSecret == "" {
		logger.Warn("Signature validation enabled but no webhook secret provided")
	}

	// Initialize story dispatcher
	i.dispatcher = sdk.NewStoryDispatcher()
	evaluator, err := sdkcel.NewEvaluator(logger, sdkcel.Config{})
	if err != nil {
		return fmt.Errorf("failed to initialize template evaluator: %w", err)
	}
	i.evaluator = evaluator

	logger.Info("GitHub webhook impulse initialized",
		slog.String("path", i.cfg.Path),
		slog.Bool("validateSignature", i.cfg.ValidateSignature),
		slog.String("sessionKeyStrategy", i.cfg.SessionKeyStrategy),
		slog.Any("eventsAllowlist", i.cfg.EventsAllowlist),
		slog.Any("actionsAllowlist", i.cfg.ActionsAllowlist))

	return nil
}

// Run begins listening for GitHub webhooks.
func (i *GitHubImpulse) Run(ctx context.Context, k8sClient *sdkk8s.Client) error {
	logger := i.loggerWithContext()

	mux := http.NewServeMux()
	mux.HandleFunc(i.cfg.Path, i.webhookHandler())

	// Health endpoints on separate port
	healthMux := http.NewServeMux()
	healthMux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		writePlainResponse(w, "ok")
	})
	healthMux.HandleFunc("/ready", func(w http.ResponseWriter, r *http.Request) {
		writePlainResponse(w, "ready")
	})

	// Start health server
	healthServer := &http.Server{Addr: ":8081", Handler: healthMux}
	go func() {
		logger.Info("Health server starting", slog.String("addr", ":8081"))
		if err := healthServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logger.Error("Health server error", slog.Any("error", err))
		}
	}()

	// Start webhook server
	server := &http.Server{Addr: ":8080", Handler: mux}
	go func() {
		<-ctx.Done()
		logger.Info("Shutting down servers")
		_ = server.Shutdown(context.Background())
		_ = healthServer.Shutdown(context.Background())
	}()

	logger.Info("GitHub webhook server starting",
		slog.String("addr", ":8080"),
		slog.String("path", i.cfg.Path))

	return server.ListenAndServe()
}

// webhookHandler handles incoming GitHub webhook requests.
func (i *GitHubImpulse) webhookHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		logger := i.loggerWithContext()

		if r.Method != http.MethodPost {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}

		req, err := i.readWebhookRequest(r)
		if err != nil {
			logger.Error("Failed to decode webhook request", slog.Any("error", err))
			http.Error(w, "Failed to parse webhook request", http.StatusBadRequest)
			return
		}

		logger = logger.With(
			slog.String("event", req.eventType),
			slog.String("delivery", req.deliveryID),
			slog.String("action", req.action),
			slog.String("repository", req.repository),
			slog.String("sender", req.sender),
		)

		// Validate signature if enabled
		if i.cfg.ValidateSignature && i.webhookSecret != "" {
			if !i.validateSignature(req.body, req.signature) {
				logger.Warn("Invalid webhook signature")
				http.Error(w, "Invalid signature", http.StatusUnauthorized)
				return
			}
		}

		// Apply filters
		if !i.shouldAcceptEvent(req.eventType, req.action, req.repository, req.payload) {
			logger.Debug("Event filtered out")
			writePlainResponse(w, "Event filtered")
			return
		}

		// Check for end events (stop session)
		eventKey := req.eventKey()
		if i.isEndEvent(req.eventType, req.action) {
			sessionKey := i.generateSessionKey(req.eventType, req.action, req.deliveryID, req.payload)
			logger.Info("End event received, stopping session", slog.String("sessionKey", sessionKey))
			if _, err := i.dispatcher.Stop(r.Context(), sessionKey); err != nil {
				logger.Warn("Failed to stop session", slog.Any("error", err))
			}
			writePlainResponse(w, "Session stopped")
			return
		}

		// Find matching policy or use defaults
		storyName, additionalInputs := i.matchPolicy(req.eventType, req.action, req.repository, req.payload)
		sessionKey := i.generateSessionKey(req.eventType, req.action, req.deliveryID, req.payload)
		inputs := i.buildInputs(
			req.eventType,
			req.action,
			req.deliveryID,
			req.repository,
			req.sender,
			req.payload,
			additionalInputs,
		)

		result, err := i.dispatcher.Trigger(r.Context(), sdk.StoryTriggerRequest{
			Key:       sessionKey,
			StoryName: storyName, // Empty means SDK resolves from Impulse.spec.storyRef
			Inputs:    inputs,
		})
		if err != nil {
			logger.Error("Failed to trigger story", slog.Any("error", err))
			http.Error(w, "Failed to trigger story", http.StatusInternalServerError)
			return
		}
		storyRunName := resolveStoryRunName(result)
		if storyRunName == "" {
			logger.Error("Trigger returned no StoryRun identity",
				slog.String("sessionKey", sessionKey),
				slog.String("eventKey", eventKey))
			http.Error(w, "Failed to resolve triggered StoryRun", http.StatusInternalServerError)
			return
		}

		logger.Info("StoryRun triggered",
			slog.String("storyRun", storyRunName),
			slog.String("sessionKey", sessionKey),
			slog.String("eventKey", eventKey))

		writeJSONResponse(w, http.StatusAccepted, map[string]string{
			"status":     "accepted",
			"storyRun":   storyRunName,
			"sessionKey": sessionKey,
		})
	}
}

func (i *GitHubImpulse) readWebhookRequest(r *http.Request) (*webhookRequest, error) {
	defer func() {
		_ = r.Body.Close()
	}()
	body, err := io.ReadAll(r.Body)
	if err != nil {
		return nil, fmt.Errorf("read request body: %w", err)
	}
	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, fmt.Errorf("unmarshal payload: %w", err)
	}
	return &webhookRequest{
		body:       body,
		eventType:  r.Header.Get("X-GitHub-Event"),
		deliveryID: r.Header.Get("X-GitHub-Delivery"),
		signature:  r.Header.Get("X-Hub-Signature-256"),
		action:     getString(payload, "action"),
		repository: getNestedString(payload, "repository", "full_name"),
		sender:     getNestedString(payload, "sender", "login"),
		payload:    payload,
	}, nil
}

func (r *webhookRequest) eventKey() string {
	return fmt.Sprintf("%s:%s", r.eventType, r.action)
}

func resolveStoryRunName(result *sdk.StoryTriggerResult) string {
	if result != nil && result.StoryRun != nil {
		if name := strings.TrimSpace(result.StoryRun.Name); name != "" {
			return name
		}
	}
	if result != nil && result.Session != nil {
		return strings.TrimSpace(result.Session.StoryRun)
	}
	return ""
}

func writePlainResponse(w http.ResponseWriter, body string) {
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(body))
}

func writeJSONResponse(w http.ResponseWriter, status int, payload any) {
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

// validateSignature validates the GitHub webhook signature.
func (i *GitHubImpulse) validateSignature(payload []byte, signature string) bool {
	if signature == "" {
		return false
	}

	// Remove "sha256=" prefix
	signature = strings.TrimPrefix(signature, "sha256=")

	mac := hmac.New(sha256.New, []byte(i.webhookSecret))
	mac.Write(payload)
	expectedMAC := hex.EncodeToString(mac.Sum(nil))

	return hmac.Equal([]byte(signature), []byte(expectedMAC))
}

// shouldAcceptEvent checks if the event passes all filters.
func (i *GitHubImpulse) shouldAcceptEvent(eventType, action, repository string, payload map[string]any) bool {
	// Check events allowlist
	if len(i.cfg.EventsAllowlist) > 0 {
		if !contains(i.cfg.EventsAllowlist, eventType) {
			return false
		}
	}

	// Check actions allowlist
	if len(i.cfg.ActionsAllowlist) > 0 && action != "" {
		if !contains(i.cfg.ActionsAllowlist, action) {
			return false
		}
	}

	// Check repositories allowlist
	if len(i.cfg.RepositoriesAllowlist) > 0 && repository != "" {
		if !contains(i.cfg.RepositoriesAllowlist, repository) {
			return false
		}
	}

	// Check branches allowlist (for push and pull_request events)
	if len(i.cfg.BranchesAllowlist) > 0 {
		branch := i.extractBranch(eventType, payload)
		if branch != "" && !matchesGlob(i.cfg.BranchesAllowlist, branch) {
			return false
		}
	}

	return true
}

// extractBranch extracts the branch name from the payload based on event type.
func (i *GitHubImpulse) extractBranch(eventType string, payload map[string]any) string {
	switch eventType {
	case githubEventPush:
		ref := getString(payload, "ref")
		return strings.TrimPrefix(ref, "refs/heads/")
	case githubEventPullRequest:
		return getNestedString(payload, githubEventPullRequest, "head", "ref")
	case "create", "delete":
		if getString(payload, "ref_type") == "branch" {
			return getString(payload, "ref")
		}
	}
	return ""
}

// generateSessionKey generates a session key based on the configured strategy.
func (i *GitHubImpulse) generateSessionKey(eventType, action, deliveryID string, payload map[string]any) string {
	repository := getNestedString(payload, "repository", "full_name")

	switch i.cfg.SessionKeyStrategy {
	case "delivery":
		return deliveryID

	case "custom":
		if key := i.generateCustomSessionKey(eventType, action, deliveryID, payload); key != "" {
			return key
		}
		return deliveryID
	}

	return i.autoSessionKey(eventType, payload, repository, deliveryID)
}

func (i *GitHubImpulse) generateCustomSessionKey(
	eventType, action, deliveryID string,
	payload map[string]any,
) string {
	expr := strings.TrimSpace(i.cfg.SessionKeyExpression)
	if expr == "" || i.evaluator == nil {
		return ""
	}
	vars := map[string]any{
		"event":      eventType,
		"action":     action,
		"deliveryId": deliveryID,
		"payload":    payload,
	}
	if repo, ok := payload["repository"].(map[string]any); ok {
		vars["repository"] = repo
	}
	evaluated, err := i.evaluator.EvaluateExpression(context.Background(), expr, vars)
	if err != nil {
		i.loggerWithContext().Warn(
			"Failed to evaluate custom session key expression; falling back to delivery id",
			slog.String("expression", expr),
			slog.Any("error", err),
		)
		return ""
	}
	return strings.TrimSpace(fmt.Sprint(evaluated))
}

func (i *GitHubImpulse) autoSessionKey(
	eventType string,
	payload map[string]any,
	repository, deliveryID string,
) string {
	switch eventType {
	case githubEventPullRequest, "pull_request_review", "pull_request_review_comment":
		prNum := getNestedInt(payload, githubEventPullRequest, "number")
		if prNum > 0 {
			return fmt.Sprintf("%s-pr-%d", repository, prNum)
		}
	case "issues", "issue_comment":
		issueNum := getNestedInt(payload, "issue", "number")
		if issueNum > 0 {
			return fmt.Sprintf("%s-issue-%d", repository, issueNum)
		}
	case githubEventPush:
		return fmt.Sprintf("%s-%s", repository, getString(payload, "ref"))
	case "check_run", "check_suite":
		checkID := getNestedInt(payload, "check_run", "id")
		if checkID == 0 {
			checkID = getNestedInt(payload, "check_suite", "id")
		}
		if checkID > 0 {
			return fmt.Sprintf("%s-check-%d", repository, checkID)
		}
	case "workflow_run":
		runID := getNestedInt(payload, "workflow_run", "id")
		if runID > 0 {
			return fmt.Sprintf("%s-workflow-%d", repository, runID)
		}
	case "release":
		releaseID := getNestedInt(payload, "release", "id")
		if releaseID > 0 {
			return fmt.Sprintf("%s-release-%d", repository, releaseID)
		}
	case "deployment", "deployment_status":
		deployID := getNestedInt(payload, "deployment", "id")
		if deployID > 0 {
			return fmt.Sprintf("%s-deploy-%d", repository, deployID)
		}
	}
	return deliveryID
}

// matchPolicy finds the first matching policy and returns story name and additional inputs.
func (i *GitHubImpulse) matchPolicy(
	eventType, action, repository string,
	payload map[string]any,
) (string, map[string]any) {
	for _, policy := range i.cfg.Policies {
		// Check event match
		if len(policy.Events) > 0 && !contains(policy.Events, eventType) {
			continue
		}

		// Check action match
		if len(policy.Actions) > 0 && !contains(policy.Actions, action) {
			continue
		}

		// Check repository match
		if len(policy.Repositories) > 0 && !contains(policy.Repositories, repository) {
			continue
		}

		// Check branch match
		if len(policy.Branches) > 0 {
			branch := i.extractBranch(eventType, payload)
			if !matchesGlob(policy.Branches, branch) {
				continue
			}
		}

		// TODO: Evaluate CEL condition if present

		return policy.StoryName, policy.InputsTransform
	}

	return "", nil
}

// buildInputs constructs Story trigger inputs from the webhook payload.
func (i *GitHubImpulse) buildInputs(
	eventType, action, deliveryID, repository, sender string,
	payload map[string]any,
	additional map[string]any,
) map[string]any {
	inputs := map[string]any{
		"event":      eventType,
		"action":     action,
		"deliveryId": deliveryID,
		"repository": repository,
		"sender":     sender,
	}

	// Add event-specific fields
	switch eventType {
	case githubEventPullRequest, "pull_request_review", "pull_request_review_comment":
		if pr, ok := payload[githubEventPullRequest].(map[string]any); ok {
			inputs["pullRequest"] = map[string]any{
				"number":  pr["number"],
				"title":   pr["title"],
				"state":   pr["state"],
				"htmlUrl": pr["html_url"],
				"head": map[string]any{
					"ref": getNestedString(pr, "head", "ref"),
					"sha": getNestedString(pr, "head", "sha"),
				},
				"base": map[string]any{
					"ref": getNestedString(pr, "base", "ref"),
					"sha": getNestedString(pr, "base", "sha"),
				},
				"user":   getNestedString(pr, "user", "login"),
				"draft":  pr["draft"],
				"merged": pr["merged"],
			}
		}

	case "issues", "issue_comment":
		if issue, ok := payload["issue"].(map[string]any); ok {
			inputs["issue"] = map[string]any{
				"number":  issue["number"],
				"title":   issue["title"],
				"state":   issue["state"],
				"htmlUrl": issue["html_url"],
				"user":    getNestedString(issue, "user", "login"),
				"labels":  extractLabels(issue),
			}
		}
		if comment, ok := payload["comment"].(map[string]any); ok {
			inputs["comment"] = map[string]any{
				"id":      comment["id"],
				"body":    comment["body"],
				"user":    getNestedString(comment, "user", "login"),
				"htmlUrl": comment["html_url"],
			}
		}

	case githubEventPush:
		inputs["push"] = map[string]any{
			"ref":     payload["ref"],
			"before":  payload["before"],
			"after":   payload["after"],
			"commits": payload["commits"],
			"pusher":  getNestedString(payload, "pusher", "name"),
		}

	case "release":
		if release, ok := payload["release"].(map[string]any); ok {
			inputs["release"] = map[string]any{
				"id":         release["id"],
				"tagName":    release["tag_name"],
				"name":       release["name"],
				"body":       release["body"],
				"draft":      release["draft"],
				"prerelease": release["prerelease"],
				"htmlUrl":    release["html_url"],
			}
		}

	case "workflow_run":
		if wf, ok := payload["workflow_run"].(map[string]any); ok {
			inputs["workflowRun"] = map[string]any{
				"id":         wf["id"],
				"name":       wf["name"],
				"status":     wf["status"],
				"conclusion": wf["conclusion"],
				"headBranch": wf["head_branch"],
				"headSha":    wf["head_sha"],
				"htmlUrl":    wf["html_url"],
			}
		}
	}

	// Include raw payload if configured
	if i.cfg.IncludeRawPayload {
		inputs["rawPayload"] = payload
	}

	// Merge additional inputs from policy
	for k, v := range additional {
		inputs[k] = v
	}

	return inputs
}

// isEndEvent checks if the event/action combination is an end event.
func (i *GitHubImpulse) isEndEvent(eventType, action string) bool {
	eventKey := fmt.Sprintf("%s:%s", eventType, action)
	for _, end := range i.cfg.EndEvents {
		if end == eventType || end == eventKey {
			return true
		}
	}
	return false
}

// loggerWithContext returns a logger with context fields.
func (i *GitHubImpulse) loggerWithContext() *slog.Logger {
	return i.logger.With(slog.String("component", "github-webhook-impulse"))
}

// Helper functions

func getString(m map[string]any, key string) string {
	if v, ok := m[key].(string); ok {
		return v
	}
	return ""
}

func getNestedString(m map[string]any, keys ...string) string {
	current := m
	for i, key := range keys {
		if i == len(keys)-1 {
			return getString(current, key)
		}
		if next, ok := current[key].(map[string]any); ok {
			current = next
		} else {
			return ""
		}
	}
	return ""
}

func getNestedInt(m map[string]any, keys ...string) int {
	current := m
	for i, key := range keys {
		if i == len(keys)-1 {
			if v, ok := current[key].(float64); ok {
				return int(v)
			}
			if v, ok := current[key].(int); ok {
				return v
			}
			return 0
		}
		if next, ok := current[key].(map[string]any); ok {
			current = next
		} else {
			return 0
		}
	}
	return 0
}

func contains(slice []string, item string) bool {
	for _, s := range slice {
		if s == item {
			return true
		}
	}
	return false
}

func matchesGlob(patterns []string, value string) bool {
	for _, pattern := range patterns {
		if matched, _ := filepath.Match(pattern, value); matched {
			return true
		}
	}
	return false
}

func extractLabels(issue map[string]any) []string {
	labels, ok := issue["labels"].([]any)
	if !ok {
		return nil
	}
	result := make([]string, 0, len(labels))
	for _, l := range labels {
		if lm, ok := l.(map[string]any); ok {
			if name, ok := lm["name"].(string); ok {
				result = append(result, name)
			}
		}
	}
	return result
}
