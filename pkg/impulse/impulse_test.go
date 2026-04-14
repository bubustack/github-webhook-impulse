package impulse

import (
	"log/slog"
	"os"
	"testing"

	sdkcel "github.com/bubustack/bubu-sdk-go/cel"

	cfgpkg "github.com/bubustack/github-webhook-impulse/pkg/config"
)

func TestGenerateSessionKeyCustomStrategy(t *testing.T) {
	t.Parallel()

	evaluator, err := sdkcel.NewEvaluator(slog.New(slog.NewTextHandler(os.Stdout, nil)), sdkcel.Config{})
	if err != nil {
		t.Fatalf("create evaluator: %v", err)
	}
	t.Cleanup(evaluator.Close)

	imp := &GitHubImpulse{
		cfg: cfgpkg.Config{
			SessionKeyStrategy:   "custom",
			SessionKeyExpression: `printf "%s-pr-%v" .repository.full_name .payload.pull_request.number`,
		},
		evaluator: evaluator,
		logger:    slog.New(slog.NewTextHandler(os.Stdout, nil)),
	}

	payload := map[string]any{
		"repository": map[string]any{
			"full_name": "octo-org/octo-repo",
		},
		"pull_request": map[string]any{
			"number": 42,
		},
	}

	got := imp.generateSessionKey("pull_request", "opened", "delivery-123", payload)
	want := "octo-org/octo-repo-pr-42"
	if got != want {
		t.Fatalf("custom session key = %q, want %q", got, want)
	}
}

func TestGenerateSessionKeyCustomStrategyFallsBackOnEvalError(t *testing.T) {
	t.Parallel()

	evaluator, err := sdkcel.NewEvaluator(slog.New(slog.NewTextHandler(os.Stdout, nil)), sdkcel.Config{})
	if err != nil {
		t.Fatalf("create evaluator: %v", err)
	}
	t.Cleanup(evaluator.Close)

	imp := &GitHubImpulse{
		cfg: cfgpkg.Config{
			SessionKeyStrategy:   "custom",
			SessionKeyExpression: `repository..bad`,
		},
		evaluator: evaluator,
		logger:    slog.New(slog.NewTextHandler(os.Stdout, nil)),
	}

	got := imp.generateSessionKey("pull_request", "opened", "delivery-123", map[string]any{})
	if got != "delivery-123" {
		t.Fatalf("fallback session key = %q, want %q", got, "delivery-123")
	}
}
