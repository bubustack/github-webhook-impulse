// Package config defines configuration structures for the GitHub webhook impulse.
package config

// Config holds the GitHub webhook impulse configuration.
type Config struct {
	Path                  string   `json:"path" mapstructure:"path"`
	ValidateSignature     bool     `json:"validateSignature" mapstructure:"validateSignature"`
	EventsAllowlist       []string `json:"eventsAllowlist" mapstructure:"eventsAllowlist"`
	ActionsAllowlist      []string `json:"actionsAllowlist" mapstructure:"actionsAllowlist"`
	RepositoriesAllowlist []string `json:"repositoriesAllowlist" mapstructure:"repositoriesAllowlist"`
	BranchesAllowlist     []string `json:"branchesAllowlist" mapstructure:"branchesAllowlist"`
	SessionKeyStrategy    string   `json:"sessionKeyStrategy" mapstructure:"sessionKeyStrategy"`
	SessionKeyExpression  string   `json:"sessionKeyExpression" mapstructure:"sessionKeyExpression"`
	Policies              []Policy `json:"policies" mapstructure:"policies"`
	IncludeRawPayload     bool     `json:"includeRawPayload" mapstructure:"includeRawPayload"`
	StartEvents           []string `json:"startEvents" mapstructure:"startEvents"`
	EndEvents             []string `json:"endEvents" mapstructure:"endEvents"`
}

// Policy defines conditional routing for specific events.
type Policy struct {
	Name            string         `json:"name" mapstructure:"name"`
	Events          []string       `json:"events" mapstructure:"events"`
	Actions         []string       `json:"actions" mapstructure:"actions"`
	Repositories    []string       `json:"repositories" mapstructure:"repositories"`
	Branches        []string       `json:"branches" mapstructure:"branches"`
	Condition       string         `json:"condition" mapstructure:"condition"`
	StoryName       string         `json:"storyName" mapstructure:"storyName"`
	InputsTransform map[string]any `json:"inputsTransform" mapstructure:"inputsTransform"`
}

// DefaultConfig returns the default configuration.
func DefaultConfig() Config {
	return Config{
		Path:               "/webhook",
		ValidateSignature:  true,
		SessionKeyStrategy: "auto",
		IncludeRawPayload:  false,
	}
}
