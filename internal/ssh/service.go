package ssh

// Service serves the renderer's SSH key channels:
//   ssh:status, ssh:generate, ssh:addToAgent, ssh:test
// It mirrors registerSshHandlers in src/main/ssh.ts. Every exported method is
// callable from the page, so nothing else is exported here; the work happens
// on Keys. No method returns or logs a private key or a passphrase.

import (
	"context"
	"fmt"

	"github.com/GabrySpada/gitcito/internal/shared"
)

// Service is the bound facade over Keys.
type Service struct {
	k *Keys
}

// NewService builds the facade. main.go passes New(*shot).
func NewService(k *Keys) *Service {
	return &Service{k: k}
}

// recoverErr turns a panic into an error: nothing may panic across the bridge.
// The panic value is not echoed, since a passphrase could be in scope.
func recoverErr(err *error) {
	if p := recover(); p != nil {
		*err = fmt.Errorf("ssh: internal error")
	}
}

// Status lists the public keys, which of them the agent holds, and whether
// there is an agent.
// channel: ssh:status
func (s *Service) Status(ctx context.Context) (out shared.SshStatus, err error) {
	defer recoverErr(&err)
	return s.k.Status(ctx)
}

// Generate creates an ed25519 key pair and returns the public key's path.
// channel: ssh:generate
func (s *Service) Generate(ctx context.Context, name, comment, passphrase string) (out string, err error) {
	defer recoverErr(&err)
	return s.k.Generate(ctx, name, comment, passphrase)
}

// AddToAgent loads a key into the running agent; an empty string on success,
// else the reason.
// channel: ssh:addToAgent
func (s *Service) AddToAgent(ctx context.Context, publicKeyPath, passphrase string) (out string, err error) {
	defer recoverErr(&err)
	return s.k.AddToAgent(ctx, publicKeyPath, passphrase)
}

// Test runs `ssh -T` against a known host (github, gitlab, bitbucket, azure).
// channel: ssh:test
func (s *Service) Test(ctx context.Context, host string) (out shared.SshTest, err error) {
	defer recoverErr(&err)
	return s.k.Test(ctx, host), nil
}
