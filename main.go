package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"syscall"

	sdk "github.com/bubustack/bubu-sdk-go"

	"github.com/bubustack/github-webhook-impulse/pkg/impulse"
)

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	if err := sdk.RunImpulse(ctx, impulse.New()); err != nil {
		log.Fatalf("github-webhook impulse failed: %v", err)
	}
}
