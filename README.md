# 🐙 GitHub Webhook Impulse

A BubuStack Impulse that submits durable `StoryTrigger` requests from GitHub webhook events.

## 🌟 Highlights

- **HMAC-SHA256 signature validation** - Secure webhook verification
- **Event filtering** - Filter by event type, action, repository, branch
- **Smart session keys** - Automatic session keying by PR/issue/commit
- **Policy-based routing** - Route different events to different Stories
- **End event detection** - Graceful session cleanup on PR close/merge

## 🚀 Quick Start

1. Install the ImpulseTemplate:

```bash
kubectl apply -f Impulse.yaml
```

2. Create an Impulse instance:

```yaml
apiVersion: bubustack.io/v1alpha1
kind: Impulse
metadata:
  name: my-github-webhook
  namespace: default
spec:
  templateRef:
    name: github-webhook-impulse
  
  storyRef:
    name: my-github-story
  
  secrets:
    webhookSecret:
      name: github-webhook-secret
  
  with:
    path: /webhook
    validateSignature: true
    eventsAllowlist:
      - pull_request
      - issues
    actionsAllowlist:
      - opened
      - closed
      - labeled
```

3. Create the webhook secret:

```bash
kubectl create secret generic github-webhook-secret \
  --from-literal=WEBHOOK_SECRET=your-github-webhook-secret
```

4. Configure the webhook in GitHub:
   - Go to your repository → Settings → Webhooks → Add webhook
   - Payload URL: `https://your-ingress/webhook`
   - Content type: `application/json`
   - Secret: Same as `WEBHOOK_SECRET`
   - Events: Select events you want to receive

## ⚙️ Configuration (`Impulse.spec.with`)

### Event Filtering

```yaml
with:
  # Only accept these event types
  eventsAllowlist:
    - pull_request
    - push
    - issues
  
  # Only accept these actions
  actionsAllowlist:
    - opened
    - synchronize
    - closed
  
  # Only accept from these repositories
  repositoriesAllowlist:
    - owner/repo1
    - owner/repo2
  
  # Only accept these branches (supports globs)
  branchesAllowlist:
    - main
    - release/*
```

### Session Key Strategies

```yaml
with:
  # auto: Smart detection based on event type (default)
  # - PR events → repo-pr-123
  # - Issue events → repo-issue-456
  # - Push events → repo-refs/heads/main
  sessionKeyStrategy: auto
  
  # delivery: Use unique X-GitHub-Delivery header
  sessionKeyStrategy: delivery
  
  # custom: evaluate a template expression
  # available variables: .event, .action, .deliveryId, .payload, .repository
  sessionKeyStrategy: custom
  sessionKeyExpression: 'printf "%s-pr-%v" .repository.full_name .payload.pull_request.number'
```

### Policy-Based Routing

Route different events to different Stories:

```yaml
with:
  policies:
    - name: pr-review
      events:
        - pull_request
      actions:
        - opened
        - synchronize
      storyName: pr-review-story
    
    - name: issue-triage
      events:
        - issues
      actions:
        - opened
      storyName: issue-triage-story
    
    - name: release-notify
      events:
        - release
      actions:
        - published
      storyName: release-notification-story
```

### Session Lifecycle

```yaml
with:
  # Events that start a new session
  startEvents:
    - "pull_request:opened"
  
  # Events that end a session (calls StoryDispatcher.Stop)
  endEvents:
    - "pull_request:closed"
    - "pull_request:merged"
```

## 📥 Story Inputs

The impulse provides structured inputs to your Story:

```yaml
# Common fields (all events)
event: "pull_request"
action: "opened"
deliveryId: "abc123..."
repository: "owner/repo"
sender: "username"

# PR-specific fields
pullRequest:
  number: 123
  title: "Add feature X"
  state: "open"
  htmlUrl: "https://github.com/..."
  head:
    ref: "feature-branch"
    sha: "abc123..."
  base:
    ref: "main"
    sha: "def456..."
  user: "author"
  draft: false
  merged: false

# Issue-specific fields
issue:
  number: 456
  title: "Bug report"
  state: "open"
  labels: ["bug", "priority-high"]

# Comment fields (issue_comment)
comment:
  id: 789
  body: "This is a comment"
  user: "commenter"

# Push-specific fields
push:
  ref: "refs/heads/main"
  before: "abc123..."
  after: "def456..."
  commits: [...]
  pusher: "username"
```

## 📘 Example Stories

### PR Review Assistant

```yaml
apiVersion: bubustack.io/v1alpha1
kind: Story
metadata:
  name: pr-review-assistant
spec:
  pattern: batch
  
  steps:
    - name: fetch-diff
      ref:
        name: http-request
      with:
        url: "https://api.github.com/repos/{{ inputs.repository }}/pulls/{{ inputs.pullRequest.number }}"
        headers:
          Accept: "application/vnd.github.v3.diff"
    
    - name: review
      needs: [fetch-diff]
      ref:
        name: ai-reviewer
      with:
        code: "{{ steps['fetch-diff'].output.body }}"
    
    - name: post-comment
      needs: [review]
      ref:
        name: github-mcp
      with:
        action: callTool
        tool: create_issue_comment
        arguments:
          owner: "{{ inputs.repository | split('/') | first }}"
          repo: "{{ inputs.repository | split('/') | last }}"
          issue_number: "{{ inputs.pullRequest.number }}"
          body: "{{ steps['review'].output.text }}"
```

### Issue Triage Bot

```yaml
apiVersion: bubustack.io/v1alpha1
kind: Story
metadata:
  name: issue-triage-bot
spec:
  pattern: batch
  
  steps:
    - name: analyze
      ref:
        name: openai-chat
      with:
        userPrompt: |
          Analyze this GitHub issue and suggest:
          1. Priority (P0-P3)
          2. Labels (bug, feature, docs, etc.)
          3. Suggested assignee team
          
          Title: {{ inputs.issue.title }}
          Body: {{ inputs.issue.body }}
    
    - name: apply-labels
      needs: [analyze]
      ref:
        name: github-mcp
      with:
        action: callTool
        tool: add_issue_labels
        arguments:
          owner: "{{ inputs.repository | split('/') | first }}"
          repo: "{{ inputs.repository | split('/') | last }}"
          issue_number: "{{ inputs.issue.number }}"
          labels: "{{ steps['analyze'].output.structured.labels }}"
```

## 🩺 Health Endpoints

- `GET :8081/health` - Liveness probe
- `GET :8081/ready` - Readiness probe

## 🧪 Local Development

```bash
# Build binary
make build

# Run tests
make test

# Build Docker image
make docker-build VERSION=v0.1.0

# Push to registry
make docker-push VERSION=v0.1.0
```


## 🤝 Community & Support

- [Contributing](./CONTRIBUTING.md)
- [Support](./SUPPORT.md)
- [Security Policy](./SECURITY.md)
- [Code of Conduct](./CODE_OF_CONDUCT.md)
- [Discord](https://discord.gg/dysrB7D8H6)

## 📄 License

Copyright 2025 BubuStack.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
