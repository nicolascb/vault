package misc

import (
	"context"
	"path"
	"testing"

	"github.com/go-test/deep"
	"github.com/hashicorp/go-hclog"
	"github.com/hashicorp/vault/helper/testhelpers"
	"github.com/hashicorp/vault/http"
	"github.com/hashicorp/vault/sdk/helper/logging"
	"github.com/hashicorp/vault/sdk/physical/inmem"
	"github.com/hashicorp/vault/vault"
	"go.uber.org/atomic"
)

func TestRecovery(t *testing.T) {
	logger := logging.NewVaultLogger(hclog.Debug).Named(t.Name())
	inm, err := inmem.NewTransactionalInmemHA(nil, logger)
	if err != nil {
		t.Fatal(err)
	}

	var keys [][]byte
	var secretUUID string
	var rootToken string
	{
		conf := vault.CoreConfig{
			Physical: inm,
			Logger:   logger,
		}
		opts := vault.TestClusterOptions{
			HandlerFunc: http.Handler,
			NumCores:    1,
		}

		cluster := vault.NewTestCluster(t, &conf, &opts)
		cluster.Start()
		defer cluster.Cleanup()

		client := cluster.Cores[0].Client
		rootToken = client.Token()
		fooVal := map[string]interface{}{"bar": 1.0}
		_, err = client.Logical().WriteWithContext(context.Background(), "secret/foo", fooVal)
		if err != nil {
			t.Fatal(err)
		}
		secret, err := client.Logical().ListWithContext(context.Background(), "secret/")
		if err != nil {
			t.Fatal(err)
		}
		if diff := deep.Equal(secret.Data["keys"], []interface{}{"foo"}); len(diff) > 0 {
			t.Fatalf("got=%v, want=%v, diff: %v", secret.Data["keys"], []string{"foo"}, diff)
		}
		mounts, err := cluster.Cores[0].Client.Sys().ListMountsWithContext(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		secretMount := mounts["secret/"]
		if secretMount == nil {
			t.Fatalf("secret mount not found, mounts: %v", mounts)
		}
		secretUUID = secretMount.UUID
		cluster.EnsureCoresSealed(t)
		keys = cluster.BarrierKeys
	}

	{
		// Now bring it up in recovery mode.
		var tokenRef atomic.String
		conf := vault.CoreConfig{
			Physical:     inm,
			Logger:       logger,
			RecoveryMode: true,
		}
		opts := vault.TestClusterOptions{
			HandlerFunc: http.Handler,
			NumCores:    1,
			SkipInit:    true,
			DefaultHandlerProperties: vault.HandlerProperties{
				RecoveryMode:  true,
				RecoveryToken: &tokenRef,
			},
		}
		cluster := vault.NewTestCluster(t, &conf, &opts)
		cluster.BarrierKeys = keys
		cluster.Start()
		defer cluster.Cleanup()

		client := cluster.Cores[0].Client
		recoveryToken := testhelpers.GenerateRoot(t, cluster, testhelpers.GenerateRecovery)
		_, err = testhelpers.GenerateRootWithError(t, cluster, testhelpers.GenerateRecovery)
		if err == nil {
			t.Fatal("expected second generate-root to fail")
		}
		client.SetToken(recoveryToken)

		secret, err := client.Logical().ListWithContext(context.Background(), path.Join("sys/raw/logical", secretUUID))
		if err != nil {
			t.Fatal(err)
		}
		if diff := deep.Equal(secret.Data["keys"], []interface{}{"foo"}); len(diff) > 0 {
			t.Fatalf("got=%v, want=%v, diff: %v", secret.Data, []string{"foo"}, diff)
		}

		_, err = client.Logical().DeleteWithContext(context.Background(), path.Join("sys/raw/logical", secretUUID, "foo"))
		if err != nil {
			t.Fatal(err)
		}
		cluster.EnsureCoresSealed(t)
	}

	{
		// Now go back to regular mode and verify that our changes are present
		conf := vault.CoreConfig{
			Physical: inm,
			Logger:   logger,
		}
		opts := vault.TestClusterOptions{
			HandlerFunc: http.Handler,
			NumCores:    1,
			SkipInit:    true,
		}
		cluster := vault.NewTestCluster(t, &conf, &opts)
		cluster.BarrierKeys = keys
		cluster.Start()
		defer cluster.Cleanup()

		testhelpers.EnsureCoresUnsealed(t, cluster)
		vault.TestWaitActive(t, cluster.Cores[0].Core)

		client := cluster.Cores[0].Client
		client.SetToken(rootToken)
		secret, err := client.Logical().ListWithContext(context.Background(), "secret/")
		if err != nil {
			t.Fatal(err)
		}
		if secret != nil {
			t.Fatal("expected no data in secret mount")
		}
	}
}
