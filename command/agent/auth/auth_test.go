package auth

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/hashicorp/go-hclog"
	"github.com/hashicorp/vault/api"
	"github.com/hashicorp/vault/builtin/credential/userpass"
	vaulthttp "github.com/hashicorp/vault/http"
	"github.com/hashicorp/vault/sdk/helper/logging"
	"github.com/hashicorp/vault/sdk/logical"
	"github.com/hashicorp/vault/vault"
)

type userpassTestMethod struct{}

func newUserpassTestMethod(t *testing.T, client *api.Client) AuthMethod {
	err := client.Sys().EnableAuthWithOptions("userpass", &api.EnableAuthOptions{
		Type: "userpass",
		Config: api.AuthConfigInput{
			DefaultLeaseTTL: "1s",
			MaxLeaseTTL:     "3s",
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	return &userpassTestMethod{}
}

func (u *userpassTestMethod) Authenticate(_ context.Context, client *api.Client) (string, http.Header, map[string]interface{}, error) {
	_, err := client.Logical().WriteWithContext(context.Background(), "auth/userpass/users/foo", map[string]interface{}{
		"password": "bar",
	})
	if err != nil {
		return "", nil, nil, err
	}
	return "auth/userpass/login/foo", nil, map[string]interface{}{
		"password": "bar",
	}, nil
}

func (u *userpassTestMethod) NewCreds() chan struct{} {
	return nil
}

func (u *userpassTestMethod) CredSuccess() {
}

func (u *userpassTestMethod) Shutdown() {
}

func TestAuthHandler(t *testing.T) {
	logger := logging.NewVaultLogger(hclog.Trace)
	coreConfig := &vault.CoreConfig{
		Logger: logger,
		CredentialBackends: map[string]logical.Factory{
			"userpass": userpass.Factory,
		},
	}
	cluster := vault.NewTestCluster(t, coreConfig, &vault.TestClusterOptions{
		HandlerFunc: vaulthttp.Handler,
	})
	cluster.Start()
	defer cluster.Cleanup()

	vault.TestWaitActive(t, cluster.Cores[0].Core)
	client := cluster.Cores[0].Client

	ctx, cancelFunc := context.WithCancel(context.Background())

	ah := NewAuthHandler(&AuthHandlerConfig{
		Logger: logger.Named("auth.handler"),
		Client: client,
	})

	am := newUserpassTestMethod(t, client)
	errCh := make(chan error)
	go func() {
		errCh <- ah.Run(ctx, am)
	}()

	// Consume tokens so we don't block
	stopTime := time.Now().Add(5 * time.Second)
	closed := false
consumption:
	for {
		select {
		case err := <-errCh:
			if err != nil {
				t.Fatal(err)
			}
			break consumption
		case <-ah.OutputCh:
		case <-ah.TemplateTokenCh:
		// Nothing
		case <-time.After(stopTime.Sub(time.Now())):
			if !closed {
				cancelFunc()
				closed = true
			}
		}
	}
}

func TestAgentBackoff(t *testing.T) {
	max := 1024 * time.Second
	backoff := newAgentBackoff(max)

	// Test initial value
	if backoff.current != initialBackoff {
		t.Fatalf("expected 1s initial backoff, got: %v", backoff.current)
	}

	// Test that backoff values are in expected range (75-100% of 2*previous)
	for i := 0; i < 9; i++ {
		old := backoff.current
		backoff.next()

		expMax := 2 * old
		expMin := 3 * expMax / 4

		if backoff.current < expMin || backoff.current > expMax {
			t.Fatalf("expected backoff in range %v to %v, got: %v", expMin, expMax, backoff)
		}
	}

	// Test that backoff is capped
	for i := 0; i < 100; i++ {
		backoff.next()
		if backoff.current > max {
			t.Fatalf("backoff exceeded max of 100s: %v", backoff)
		}
	}

	// Test reset
	backoff.reset()
	if backoff.current != initialBackoff {
		t.Fatalf("expected 1s backoff after reset, got: %v", backoff.current)
	}
}
