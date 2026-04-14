# --- Build Stage ---
FROM golang:1.26-bookworm AS builder

WORKDIR /src

# Copy source code.
COPY . .

RUN go mod download

# Build the binary.
RUN CGO_ENABLED=0 GOOS=linux go build -o github-webhook-impulse .

# --- Final Stage ---
FROM gcr.io/distroless/static:nonroot

COPY --from=builder /src/github-webhook-impulse /github-webhook-impulse

ENTRYPOINT ["/github-webhook-impulse"]
